package monitor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	roleAdmin         = "admin"
	roleOperator      = "operator"
	roleViewer        = "viewer"
	permissionWorkLog = "issues.worklog"
)

type User struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Enabled     bool     `json:"enabled"`
	Permissions []string `json:"permissions"`
}

type AccountInput struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
}

var (
	usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+\-]{2,79}$`)
	ErrDenied       = errors.New("invalid credentials or permission denied")
	ErrAccount      = errors.New(
		"use a unique username (3-80 characters), a name, a valid role and a password of 12-72 bytes",
	)
)

func permissions(role string) []string {
	result := []string{"issues.read"}
	if role == roleOperator || role == roleAdmin {
		result = append(result, "issues.acknowledge", permissionWorkLog)
	}
	if role == roleAdmin {
		result = append(result, "users.manage", "settings.manage")
	}
	return result
}

func randomToken() string {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func validateAccount(input *AccountInput, creating bool) error {
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	input.Name = strings.TrimSpace(input.Name)
	if !usernamePattern.MatchString(input.Username) || input.Name == "" || len(input.Name) > 120 ||
		(input.Role != roleAdmin && input.Role != roleOperator && input.Role != roleViewer) {
		return ErrAccount
	}
	if (creating || input.Password != "") &&
		(len(input.Password) < 12 || len(input.Password) > 72) {
		return ErrAccount
	}
	return nil
}

func (store *Store) UserCount(ctx context.Context) (int, error) {
	var count int
	err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM app_users").Scan(&count)
	return count, err
}

func (store *Store) SaveUser(
	ctx context.Context,
	id string,
	input AccountInput,
	actor string,
	first bool,
) (User, error) {
	if err := validateAccount(&input, id == ""); err != nil {
		return User{}, err
	}
	var hash []byte
	var err error
	if input.Password != "" {
		hash, err = bcrypt.GenerateFromPassword([]byte(input.Password), 12)
		if err != nil {
			return User{}, err
		}
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73519343)"); err != nil {
		return User{}, err
	}
	if first {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM app_users").Scan(&count); err != nil {
			return User{}, err
		}
		if count != 0 {
			return User{}, ErrConflict
		}
		input.Role, input.Enabled = roleAdmin, true
	} else {
		var role string
		if err = tx.QueryRow(ctx, "SELECT role FROM app_users WHERE id=$1 AND enabled=true", actor).
			Scan(&role); err != nil ||
			role != roleAdmin {
			return User{}, ErrDenied
		}
	}
	if id == "" {
		id = randomToken()
		_, err = tx.Exec(
			ctx,
			"INSERT INTO app_users (id,username,name,password_hash,role,enabled) VALUES ($1,$2,$3,$4,$5,$6)",
			id,
			input.Username,
			input.Name,
			hash,
			input.Role,
			input.Enabled,
		)
	} else {
		var oldRole string
		var oldEnabled bool
		if err = tx.QueryRow(ctx, "SELECT role,enabled FROM app_users WHERE id=$1 FOR UPDATE", id).
			Scan(&oldRole, &oldEnabled); err != nil {
			return User{}, err
		}
		if oldRole == roleAdmin && oldEnabled && (input.Role != roleAdmin || !input.Enabled) {
			var remaining int
			if err = tx.QueryRow(ctx, "SELECT count(*) FROM app_users WHERE role='admin' AND enabled AND id<>$1", id).
				Scan(&remaining); err != nil {
				return User{}, err
			}
			if remaining == 0 {
				return User{}, ErrConflict
			}
		}
		_, err = tx.Exec(
			ctx,
			"UPDATE app_users SET username=$2,name=$3,role=$4,enabled=$5,password_hash=COALESCE($6,password_hash) WHERE id=$1",
			id,
			input.Username,
			input.Name,
			input.Role,
			input.Enabled,
			hash,
		)
		if err == nil {
			_, err = tx.Exec(ctx, "DELETE FROM app_sessions WHERE user_id=$1", id)
		}
	}
	if err != nil {
		return User{}, ErrAccount
	}
	if _, err = tx.Exec(
		ctx,
		"INSERT INTO app_audit(actor,action,target) VALUES ($1,'user.saved',$2)",
		actor,
		id,
	); err != nil {
		return User{}, err
	}
	if _, err = tx.Exec(
		ctx,
		"INSERT INTO app_settings(id,workspace_id) VALUES (true,$1) ON CONFLICT DO NOTHING",
		randomToken(),
	); err != nil {
		return User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return User{
		ID:          id,
		Username:    input.Username,
		Name:        input.Name,
		Role:        input.Role,
		Enabled:     input.Enabled,
		Permissions: permissions(input.Role),
	}, nil
}

func (store *Store) Login(
	ctx context.Context,
	username, password string,
) (string, string, User, error) {
	var attempts int
	err := store.Pool.QueryRow(ctx, `INSERT INTO app_login_rate(id,window_start,attempts) VALUES(true,now(),1)
	ON CONFLICT(id) DO UPDATE SET attempts=CASE
		WHEN app_login_rate.window_start < now()-interval '1 minute' THEN 1
		ELSE app_login_rate.attempts+1 END,
	window_start=CASE
		WHEN app_login_rate.window_start < now()-interval '1 minute' THEN now()
		ELSE app_login_rate.window_start END RETURNING attempts`).
		Scan(&attempts)
	if err != nil {
		return "", "", User{}, err
	}
	if attempts > 30 {
		return "", "", User{}, errors.New("too many login attempts; try again in one minute")
	}
	var user User
	var hash []byte
	err = store.Pool.QueryRow(ctx,
		"SELECT id,username,name,role,enabled,password_hash FROM app_users WHERE username=$1",
		strings.ToLower(strings.TrimSpace(username))).
		Scan(&user.ID, &user.Username, &user.Name, &user.Role, &user.Enabled, &hash)
	if err != nil {
		hash = []byte("$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW")
	}
	valid := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if err != nil || !valid || !user.Enabled || len(password) > 72 {
		return "", "", User{}, ErrDenied
	}
	token, csrf := randomToken(), randomToken()
	_, err = store.Pool.Exec(
		ctx,
		"INSERT INTO app_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,$4)",
		tokenHash(token),
		user.ID,
		csrf,
		time.Now().Add(12*time.Hour),
	)
	user.Permissions = permissions(user.Role)
	return token, csrf, user, err
}

func (store *Store) Session(ctx context.Context, token string) (User, string, error) {
	var user User
	var csrf string
	if len(token) != 64 {
		return user, csrf, ErrDenied
	}
	err := store.Pool.QueryRow(ctx, `SELECT u.id,u.username,u.name,u.role,u.enabled,s.csrf
		FROM app_sessions s JOIN app_users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires_at>now() AND u.enabled`, tokenHash(token)).
		Scan(&user.ID, &user.Username, &user.Name, &user.Role, &user.Enabled, &csrf)
	user.Permissions = permissions(user.Role)
	return user, csrf, err
}

func (store *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := store.Pool.Query(
		ctx,
		"SELECT id,username,name,role,enabled FROM app_users ORDER BY created_at,id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		if err := rows.Scan(
			&user.ID,
			&user.Username,
			&user.Name,
			&user.Role,
			&user.Enabled,
		); err != nil {
			return nil, err
		}
		user.Permissions = permissions(user.Role)
		users = append(users, user)
	}
	return users, rows.Err()
}

func accountNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
