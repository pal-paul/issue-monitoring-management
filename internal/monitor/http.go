package monitor

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

type Config struct {
	Dev  bool
	Demo bool
}

type Server struct {
	Store     *Store
	Config    Config
	DataDir   string
	LocalHTTP bool
	runtime   atomic.Pointer[Store]
	setupMu   sync.Mutex
	workerMu  sync.RWMutex
	worker    WorkerStatus
}

func (server *Server) store() *Store {
	if store := server.runtime.Load(); store != nil {
		return store
	}
	return server.Store
}

type identityKey struct{}

func currentUser(request *http.Request) User {
	user, _ := request.Context().Value(identityKey{}).(User)
	return user
}

func (server *Server) Handler(assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, 200, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if server.store() != nil {
			if err := server.store().Pool.Ping(ctx); err != nil {
				writeError(writer, 503, "database unavailable")
				return
			}
		}
		writeJSON(writer, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/session", server.sessionInfo)
	mux.HandleFunc("POST /api/setup", server.setup)
	mux.HandleFunc("POST /api/login", server.login)
	mux.Handle("POST /api/logout", server.require("issues.read", http.HandlerFunc(server.logout)))
	mux.Handle("GET /api/users", server.require("users.manage", http.HandlerFunc(server.users)))
	mux.Handle("POST /api/users", server.require("users.manage", http.HandlerFunc(server.saveUser)))
	mux.Handle(
		"PATCH /api/users/{id}",
		server.require("users.manage", http.HandlerFunc(server.saveUser)),
	)
	mux.Handle(
		"GET /api/settings",
		server.require("settings.manage", http.HandlerFunc(server.settings)),
	)
	mux.Handle(
		"PUT /api/settings",
		server.require("settings.manage", http.HandlerFunc(server.saveSettings)),
	)
	mux.Handle(
		"PUT /api/database",
		server.require("settings.manage", http.HandlerFunc(server.saveDatabase)),
	)
	if server.Config.Dev {
		mux.Handle(
			"POST /api/dev/messages",
			server.require("issues.acknowledge", http.HandlerFunc(server.receive)),
		)
	}
	mux.Handle("GET /api/issues", server.require("issues.read", http.HandlerFunc(server.list)))
	mux.Handle(
		"POST /api/issues/{id}/worklogs",
		server.require(permissionWorkLog, http.HandlerFunc(server.addWorkLog)),
	)
	mux.Handle(
		"GET /api/issues/{id}",
		server.require("issues.read", http.HandlerFunc(server.detail)),
	)
	mux.Handle(
		"PATCH /api/issues/{id}",
		server.require("issues.acknowledge", http.HandlerFunc(server.acknowledge)),
	)
	mux.Handle(
		"GET /api/overview",
		server.require(
			"issues.read",
			http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				result, err := server.store().Overview(request.Context())
				if err != nil {
					server.failure(writer, err)
					return
				}
				writeJSON(writer, 200, result)
			}),
		),
	)
	mux.Handle(
		"GET /api/connection",
		server.require(
			"issues.read",
			http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				settings, err := server.store().Settings(request.Context())
				if err != nil {
					server.failure(writer, err)
					return
				}
				server.workerMu.RLock()
				worker := server.worker
				server.workerMu.RUnlock()
				writeJSON(
					writer,
					200,
					map[string]any{
						"source":       "Google Cloud Monitoring",
						"transport":    "Pub/Sub pull",
						"database":     "PostgreSQL",
						"subscription": settings.Subscription,
						"enabled":      settings.Enabled,
						"demo":         server.Config.Demo,
						"worker":       worker,
					},
				)
			}),
		),
	)
	mux.Handle(
		"/api/",
		server.require(
			"issues.read",
			http.HandlerFunc(
				func(writer http.ResponseWriter, request *http.Request) { writeError(writer, 404, "endpoint not found") },
			),
		),
	)
	mux.Handle("/", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeError(writer, 405, "method not allowed")
			return
		}
		if request.URL.Path == "/" {
			if _, err := fs.Stat(assets, "index.html"); err != nil {
				writeError(
					writer,
					503,
					"frontend not built: run npm ci && npm run build, then rebuild Go",
				)
				return
			}
		}
		http.FileServer(http.FS(assets)).ServeHTTP(writer, request)
	}))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self'; connect-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		writer.Header().Set("Cache-Control", "no-store")
		if request.Method != "GET" && request.Method != "HEAD" {
			origin := request.Header.Get("Origin")
			if request.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(writer, 403, "cross-site request denied")
				return
			}
			if origin != "" {
				parsed, err := url.Parse(origin)
				scheme := "https"
				if server.LocalHTTP {
					scheme = "http"
				}
				if err != nil || parsed.Host != request.Host || parsed.Scheme != scheme {
					writeError(writer, 403, "origin denied")
					return
				}
			}
			if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
				writeError(writer, 415, "application/json required")
				return
			}
		}
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()
		mux.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (server *Server) require(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("signaldesk_session")
		if err != nil || server.store() == nil {
			writeError(writer, 401, "sign in required")
			return
		}
		user, csrf, err := server.store().Session(request.Context(), cookie.Value)
		if err != nil {
			writeError(writer, 401, "session expired; sign in again")
			return
		}
		allowed := false
		for _, value := range user.Permissions {
			if value == permission {
				allowed = true
			}
		}
		if !allowed {
			writeError(writer, 403, "permission denied")
			return
		}
		if request.Method != "GET" && request.Method != "HEAD" &&
			subtle.ConstantTimeCompare(
				[]byte(csrf),
				[]byte(request.Header.Get("X-CSRF-Token")),
			) != 1 {
			writeError(writer, 403, "invalid CSRF token")
			return
		}
		next.ServeHTTP(
			writer,
			request.WithContext(context.WithValue(request.Context(), identityKey{}, user)),
		)
	})
}

func (server *Server) receive(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1_100_000))
	if err != nil {
		writeError(writer, 413, "message too large")
		return
	}
	alert, err := ParseAlert(body)
	if err != nil {
		writeError(writer, 400, err.Error())
		return
	}
	id, duplicate, err := server.store().Ingest(request.Context(), alert)
	if err != nil {
		server.failure(writer, err)
		return
	}
	writeJSON(writer, 200, map[string]any{"id": id, "duplicate": duplicate})
}

func (server *Server) list(writer http.ResponseWriter, request *http.Request) {
	filters, err := ParseFilters(request.URL.Query())
	if err != nil {
		writeError(writer, 400, err.Error())
		return
	}
	issues, total, err := server.store().List(request.Context(), filters)
	if err != nil {
		server.failure(writer, err)
		return
	}
	writeJSON(
		writer,
		200,
		map[string]any{
			"issues": issues,
			"total":  total,
			"page":   filters.Page,
			limitKey: filters.Limit,
		},
	)
}

func (server *Server) detail(writer http.ResponseWriter, request *http.Request) {
	issue, events, err := server.store().Detail(request.Context(), request.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, 404, "issue not found")
		return
	}
	if err != nil {
		server.failure(writer, err)
		return
	}
	writeJSON(writer, 200, map[string]any{"issue": issue, "events": events})
}

func (server *Server) acknowledge(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Acknowledged *bool `json:"acknowledged"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Acknowledged == nil {
		writeError(writer, 400, "acknowledged boolean required")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(writer, 400, "expected one JSON object")
		return
	}
	err := server.store().
		SetAcknowledged(request.Context(), request.PathValue("id"), *body.Acknowledged, currentUser(request).Username)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, 404, "issue not found")
		return
	}
	if errors.Is(err, ErrConflict) {
		writeError(writer, 409, err.Error())
		return
	}
	if err != nil {
		server.failure(writer, err)
		return
	}
	server.detail(writer, request)
}

func (server *Server) addWorkLog(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Body   string `json:"body"`
		Status string `json:"status"`
		Format string `json:"format"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, 400, "expected a work log JSON object with a body string")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(writer, 400, "expected one JSON object")
		return
	}
	err := server.store().AddWorkLog(
		request.Context(), request.PathValue("id"), body.Body,
		currentUser(request).Username, body.Status, body.Format,
	)
	if errors.Is(err, ErrInvalidWorkLog) || errors.Is(err, ErrInvalidStatus) ||
		errors.Is(err, ErrInvalidFormat) {
		writeError(writer, 400, err.Error())
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, 404, "issue not found")
		return
	}
	if err != nil {
		server.failure(writer, err)
		return
	}
	server.detail(writer, request)
}

func (server *Server) failure(writer http.ResponseWriter, err error) {
	slog.Error("database operation failed", "error", err)
	writeError(writer, 503, "database unavailable; retry the request")
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		slog.Warn("response write failed", "error", err)
	}
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}
