package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"sorins/mathsearch/config"
)

func testAuth(t *testing.T) *Authenticator {
	t.Helper()
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	return New(config.Auth{
		Enabled:     true,
		JWTSecret:   "test-secret",
		TokenTTLMin: 60,
		Users:       []config.User{{Username: "admin", PasswordHash: hash}},
	})
}

func TestLoginAndParse(t *testing.T) {
	a := testAuth(t)
	if _, err := a.Login("admin", "wrong"); err == nil {
		t.Error("expected error for wrong password")
	}
	if _, err := a.Login("nobody", "secret"); err == nil {
		t.Error("expected error for unknown user")
	}
	token, err := a.Login("admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := a.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "admin" || claims.Role != RolePrivileged {
		t.Errorf("claims = %s/%s", claims.Subject, claims.Role)
	}
}

func TestParseRejectsForgedToken(t *testing.T) {
	a := testAuth(t)
	other := New(config.Auth{Enabled: true, JWTSecret: "different", TokenTTLMin: 60})
	tok, _ := other.Issue("attacker", RolePrivileged)
	if _, err := a.Parse(tok); err == nil {
		t.Error("token signed with a different secret must be rejected")
	}
}

func TestRequireGatesOnToken(t *testing.T) {
	a := testAuth(t)
	h := a.Require(func(w http.ResponseWriter, r *http.Request) {
		sub, role, ok := PrincipalFrom(r.Context())
		if !ok || sub != "admin" || role != RolePrivileged {
			t.Errorf("principal not set: %s/%s/%v", sub, role, ok)
		}
		w.WriteHeader(http.StatusOK)
	})

	// No token -> 401.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/api/formulas", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}

	// Valid token -> handler runs.
	token, _ := a.Login("admin", "secret")
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/formulas", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("valid token: got %d, want 200", rec.Code)
	}
}

func TestRateLimiterExemptsPrivileged(t *testing.T) {
	rl := NewRateLimiter(config.RateLimit{RequestsPerMinute: 60, Burst: 2, PrivilegedExempt: true}, "/api/")
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	// Identity is annotated before the limiter runs (outer wrapper).
	// Social identity: limited to burst=2, third request 429.
	social := withPrincipal(rl.Middleware(final), "u1", RoleSocial)
	codes := hitN(social, 3, "1.2.3.4")
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Errorf("social codes = %v, want [200 200 429]", codes)
	}

	// Privileged identity: exempt, never limited.
	priv := withPrincipal(rl.Middleware(final), "admin", RolePrivileged)
	pcodes := hitN(priv, 5, "5.6.7.8")
	for i, c := range pcodes {
		if c != 200 {
			t.Errorf("privileged req %d = %d, want 200", i, c)
		}
	}
}

func hitN(h http.Handler, n int, ip string) []int {
	var codes []int
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/search", nil)
		req.RemoteAddr = ip + ":1234"
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	return codes
}

func withPrincipal(next http.Handler, sub, role string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), claimsKey, principal{sub, role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
