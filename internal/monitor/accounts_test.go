package monitor

import (
	"net/http"
	"testing"
)

func TestAccountValidationAndPermissions(t *testing.T) {
	input := AccountInput{
		Username: " ADMIN ",
		Name:     "Administrator",
		Password: "a-long-test-password",
		Role:     "admin",
	}
	if err := validateAccount(&input, true); err != nil || input.Username != "admin" {
		t.Fatalf("normalization: %+v %v", input, err)
	}
	input.Password = "short"
	if validateAccount(&input, true) == nil {
		t.Fatal("short password accepted")
	}
	input.Password = ""
	input.Role = "owner"
	if validateAccount(&input, false) == nil {
		t.Fatal("unknown role accepted")
	}
	if len(permissions("viewer")) != 1 || len(permissions("operator")) != 3 ||
		len(permissions("admin")) != 5 {
		t.Fatal("role permissions incorrect")
	}
	token := randomToken()
	if len(token) != 64 || tokenHash(token) == token || randomToken() == token {
		t.Fatal("session token generation failed")
	}
}

func TestSessionCookieSecurity(t *testing.T) {
	for _, local := range []bool{false, true} {
		server := &Server{LocalHTTP: local}
		for _, maxAge := range []int{43200, -1} {
			cookie := server.sessionCookie("test-token", maxAge)
			if cookie.Secure != !local || !cookie.HttpOnly ||
				cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("cookie security: local=%t maxAge=%d cookie=%+v", local, maxAge, cookie)
			}
			if cookie.Name != "signaldesk_session" || cookie.Value != "test-token" ||
				cookie.Path != "/" ||
				cookie.MaxAge != maxAge {
				t.Fatalf("cookie attributes changed: %+v", cookie)
			}
		}
	}
}
