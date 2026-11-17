package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Settings struct {
	WorkspaceID  string `json:"workspaceId"`
	Subscription string `json:"subscription"`
	Enabled      bool   `json:"enabled"`
}

type WorkerStatus struct {
	State       string     `json:"state"`
	Error       string     `json:"error,omitempty"`
	LastPoll    *time.Time `json:"lastPoll,omitempty"`
	LastMessage *time.Time `json:"lastMessage,omitempty"`
}

type bootstrap struct {
	DatabaseURL string `json:"databaseUrl"`
	WorkspaceID string `json:"workspaceId"`
}

func (server *Server) Close() {
	if server.store() != nil {
		server.store().Pool.Close()
	}
}

func (server *Server) Load(ctx context.Context) error {
	entries, err := filepath.Glob(filepath.Join(server.DataDir, "connection-*.json"))
	if err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(entries)))
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err != nil {
			return errors.New("cannot read private bootstrap configuration")
		}
		var config bootstrap
		if json.Unmarshal(data, &config) != nil || config.DatabaseURL == "" ||
			config.WorkspaceID == "" {
			continue
		}
		store, err := OpenStore(ctx, config.DatabaseURL)
		if err != nil {
			return errors.New(
				"configured PostgreSQL unavailable; restore connectivity rather than reinitializing",
			)
		}
		settings, err := store.Settings(ctx)
		if err != nil || settings.WorkspaceID != config.WorkspaceID {
			store.Pool.Close()
			return errors.New("bootstrap workspace identity does not match PostgreSQL")
		}
		server.runtime.Store(store)
		return nil
	}
	if len(entries) > 0 {
		return errors.New("no complete bootstrap record; restore private configuration from backup")
	}
	return nil
}

func (server *Server) persist(config bootstrap) error {
	if server.DataDir == "" {
		return errors.New("private persistent config directory required")
	}
	if err := os.MkdirAll(server.DataDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	name := filepath.Join(
		server.DataDir,
		fmt.Sprintf("connection-%020d-%s.json", time.Now().UnixNano(), randomToken()[:12]),
	)
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func decodeBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(writer, 400, "one valid JSON object required")
		return false
	}
	return true
}

func (server *Server) sessionInfo(writer http.ResponseWriter, request *http.Request) {
	store := server.store()
	var user *User
	csrf := ""
	setup := true
	if store != nil {
		count, err := store.UserCount(request.Context())
		if err != nil {
			server.failure(writer, err)
			return
		}
		setup = count == 0
		if cookie, err := request.Cookie("signaldesk_session"); err == nil {
			value, token, err := store.Session(request.Context(), cookie.Value)
			if err == nil {
				user = &value
				csrf = token
			}
		}
	}
	writeJSON(
		writer,
		200,
		map[string]any{
			"databaseConfigured": store != nil,
			"setupRequired":      setup,
			"user":               user,
			"csrf":               csrf,
			"local":              server.LocalHTTP,
		},
	)
}

func (server *Server) sessionCookie(token string, maxAge int) *http.Cookie {
	cookie := &http.Cookie{
		Name: "signaldesk_session", Value: token, Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
	}
	cookie.Secure = !server.LocalHTTP
	return cookie
}

func (server *Server) issueSession(writer http.ResponseWriter, token, csrf string, user User) {
	http.SetCookie(writer, server.sessionCookie(token, 43200))
	writeJSON(writer, 200, map[string]any{"user": user, "csrf": csrf})
}

func (server *Server) setup(writer http.ResponseWriter, request *http.Request) {
	server.setupMu.Lock()
	defer server.setupMu.Unlock()
	var body struct {
		DatabaseURL string `json:"databaseUrl"`
		Username    string `json:"username"`
		Name        string `json:"name"`
		Password    string `json:"password"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	store := server.store()
	fresh := store == nil
	if fresh {
		if len(body.DatabaseURL) > 8192 || strings.TrimSpace(body.DatabaseURL) == "" {
			writeError(writer, 400, "PostgreSQL connection URL required")
			return
		}
		var err error
		store, err = OpenStore(request.Context(), body.DatabaseURL)
		if err != nil {
			writeError(
				writer,
				400,
				"cannot connect to PostgreSQL; check address, credentials, TLS and schema permissions",
			)
			return
		}
		defer func() {
			if server.store() != store {
				store.Pool.Close()
			}
		}()
	}
	count, err := store.UserCount(request.Context())
	if err != nil {
		server.failure(writer, err)
		return
	}
	if !fresh && count > 0 {
		writeError(writer, 409, "workspace already initialized")
		return
	}
	if count == 0 {
		_, err = store.SaveUser(
			request.Context(),
			"",
			AccountInput{
				Username: body.Username,
				Name:     body.Name,
				Password: body.Password,
				Role:     roleAdmin,
				Enabled:  true,
			},
			"setup",
			true,
		)
		if err != nil {
			writeError(
				writer,
				400,
				"unable to create first administrator; check username and password requirements",
			)
			return
		}
		if server.Config.Demo {
			if err = store.SeedDemo(request.Context()); err != nil {
				server.failure(writer, err)
				return
			}
		}
	}
	token, csrf, user, err := store.Login(request.Context(), body.Username, body.Password)
	if err != nil || user.Role != roleAdmin {
		writeError(writer, 401, "existing database requires its administrator credentials")
		return
	}
	settings, err := store.Settings(request.Context())
	if err != nil {
		server.failure(writer, err)
		return
	}
	if fresh {
		if err := server.persist(
			bootstrap{DatabaseURL: body.DatabaseURL, WorkspaceID: settings.WorkspaceID},
		); err != nil {
			writeError(
				writer,
				503,
				"cannot persist private bootstrap configuration; check mounted directory permissions "+
					"and retry with the same administrator",
			)
			return
		}
		server.runtime.Store(store)
	}
	server.issueSession(writer, token, csrf, user)
}

func (server *Server) login(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	if server.store() == nil {
		writeError(writer, 409, "complete setup first")
		return
	}
	if len(body.Username) > 80 || len(body.Password) > 72 {
		writeError(writer, 401, "invalid credentials")
		return
	}
	token, csrf, user, err := server.store().Login(request.Context(), body.Username, body.Password)
	if err != nil {
		writeError(
			writer,
			401,
			"sign in failed; check credentials or wait one minute if rate limited",
		)
		return
	}
	server.issueSession(writer, token, csrf, user)
}

func (server *Server) logout(writer http.ResponseWriter, request *http.Request) {
	cookie, _ := request.Cookie("signaldesk_session")
	if _, err := server.store().Pool.Exec(
		request.Context(),
		"DELETE FROM app_sessions WHERE token_hash=$1",
		tokenHash(cookie.Value),
	); err != nil {
		server.failure(writer, err)
		return
	}
	http.SetCookie(writer, server.sessionCookie("", -1))
	writeJSON(writer, 200, map[string]bool{"ok": true})
}

func (server *Server) users(writer http.ResponseWriter, request *http.Request) {
	users, err := server.store().Users(request.Context())
	if err != nil {
		server.failure(writer, err)
		return
	}
	writeJSON(writer, 200, users)
}

func (server *Server) saveUser(writer http.ResponseWriter, request *http.Request) {
	var body AccountInput
	if !decodeBody(writer, request, &body) {
		return
	}
	user, err := server.store().
		SaveUser(request.Context(), request.PathValue("id"), body, currentUser(request).ID, false)
	if errors.Is(err, ErrConflict) {
		writeError(writer, 409, "at least one enabled administrator must remain")
		return
	}
	if errors.Is(err, ErrDenied) {
		writeError(writer, 403, "administrator required")
		return
	}
	if accountNotFound(err) {
		writeError(writer, 404, "user not found")
		return
	}
	if err != nil {
		writeError(writer, 400, ErrAccount.Error())
		return
	}
	writeJSON(writer, 200, user)
}

func (store *Store) Settings(ctx context.Context) (Settings, error) {
	var result Settings
	err := store.Pool.QueryRow(ctx, "SELECT workspace_id,subscription,enabled FROM app_settings WHERE id=true").
		Scan(&result.WorkspaceID, &result.Subscription, &result.Enabled)
	return result, err
}

func (server *Server) settings(writer http.ResponseWriter, request *http.Request) {
	settings, err := server.store().Settings(request.Context())
	if err != nil {
		server.failure(writer, err)
		return
	}
	writeJSON(writer, 200, settings)
}

var subscriptionPattern = regexp.MustCompile(
	`^projects/[a-z][a-z0-9\-:.]{4,62}/subscriptions/[A-Za-z][A-Za-z0-9\-_.~+%]{2,254}$`,
)

func (server *Server) saveSettings(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Subscription string `json:"subscription"`
		Enabled      bool   `json:"enabled"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	body.Subscription = strings.TrimSpace(body.Subscription)
	if (body.Subscription != "" && !subscriptionPattern.MatchString(body.Subscription)) ||
		(body.Enabled && body.Subscription == "") {
		writeError(writer, 400, "use projects/PROJECT_ID/subscriptions/SUBSCRIPTION_ID")
		return
	}
	tx, err := server.store().Pool.Begin(request.Context())
	if err != nil {
		server.failure(writer, err)
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	if _, err = tx.Exec(
		request.Context(),
		"UPDATE app_settings SET subscription=$1,enabled=$2,updated_at=now() WHERE id=true",
		body.Subscription,
		body.Enabled,
	); err == nil {
		_, err = tx.Exec(
			request.Context(),
			"INSERT INTO app_audit(actor,action,target) VALUES($1,'subscription.updated',$2)",
			currentUser(request).ID,
			body.Subscription,
		)
	}
	if err == nil {
		err = tx.Commit(request.Context())
	}
	if err != nil {
		server.failure(writer, err)
		return
	}
	server.settings(writer, request)
}

func (server *Server) saveDatabase(writer http.ResponseWriter, request *http.Request) {
	server.setupMu.Lock()
	defer server.setupMu.Unlock()
	var body struct {
		DatabaseURL string `json:"databaseUrl"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	if body.DatabaseURL == "" || len(body.DatabaseURL) > 8192 {
		writeError(writer, 400, "PostgreSQL URL required")
		return
	}
	store, err := OpenStore(request.Context(), body.DatabaseURL)
	if err != nil {
		writeError(writer, 400, "cannot connect with new database settings")
		return
	}
	defer store.Pool.Close()
	oldSettings, err := server.store().Settings(request.Context())
	if err != nil {
		server.failure(writer, err)
		return
	}
	newSettings, err := store.Settings(request.Context())
	if err != nil || newSettings.WorkspaceID != oldSettings.WorkspaceID {
		writeError(
			writer,
			409,
			"connection must point to the same restored workspace; restore its database backup before changing hosts",
		)
		return
	}
	if err = server.persist(
		bootstrap{DatabaseURL: body.DatabaseURL, WorkspaceID: newSettings.WorkspaceID},
	); err != nil {
		writeError(writer, 503, "cannot persist configuration")
		return
	}
	if _, err = server.store().Pool.Exec(
		request.Context(),
		"INSERT INTO app_audit(actor,action,target) VALUES($1,'database.updated',$2)",
		currentUser(request).ID,
		newSettings.WorkspaceID,
	); err != nil {
		server.failure(writer, err)
		return
	}
	writeJSON(writer, 200, map[string]bool{"saved": true, "restartRequired": true})
}
