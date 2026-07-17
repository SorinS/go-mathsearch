// Package auth provides authentication for the write endpoints. There are two
// classes of user, both carrying the same signed JWT distinguished by a role
// claim:
//
//   - "privileged" — a username/password login against configured users.
//   - "social"     — an OAuth login via Google/Facebook/Apple (see oauth.go).
//
// Privileged tokens are exempt from rate limiting; social tokens are not.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"sorins/mathsearch/config"
)

// Roles.
const (
	RolePrivileged = "privileged"
	RoleSocial     = "social"
)

// ErrUnauthorized is returned when credentials or tokens are invalid.
var ErrUnauthorized = errors.New("unauthorized")

// Claims are the JWT claims: the standard set plus our role.
type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// Authenticator issues and validates tokens against configured users.
type Authenticator struct {
	enabled bool
	secret  []byte
	ttl     time.Duration
	users   map[string]string // username -> bcrypt hash
	now     func() time.Time
}

// New builds an Authenticator from the auth config.
func New(a config.Auth) *Authenticator {
	users := make(map[string]string, len(a.Users))
	for _, u := range a.Users {
		users[u.Username] = u.PasswordHash
	}
	return &Authenticator{
		enabled: a.Enabled,
		secret:  []byte(a.JWTSecret),
		ttl:     time.Duration(a.TokenTTLMin) * time.Minute,
		users:   users,
		now:     time.Now,
	}
}

// Enabled reports whether authentication is configured on.
func (a *Authenticator) Enabled() bool { return a.enabled }

// Login verifies a username/password and returns a privileged JWT.
func (a *Authenticator) Login(username, password string) (string, error) {
	hash, ok := a.users[username]
	if !ok {
		// Compare against a dummy hash to blunt user-enumeration timing.
		bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidin"), []byte(password))
		return "", ErrUnauthorized
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return "", ErrUnauthorized
	}
	return a.Issue(username, RolePrivileged)
}

// Issue signs a token for the given subject and role.
func (a *Authenticator) Issue(subject, role string) (string, error) {
	now := a.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.ttl)),
		},
	})
	return tok.SignedString(a.secret)
}

// Parse validates a token string and returns its claims.
func (a *Authenticator) Parse(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrUnauthorized
		}
		return a.secret, nil
	})
	if err != nil {
		return nil, ErrUnauthorized
	}
	return claims, nil
}

type ctxKey int

const claimsKey ctxKey = 0

// principal carries the authenticated identity through the request context.
type principal struct {
	Subject string
	Role    string
}

// PrincipalFrom returns the authenticated subject and role, if any.
func PrincipalFrom(ctx context.Context) (subject, role string, ok bool) {
	p, ok := ctx.Value(claimsKey).(principal)
	return p.Subject, p.Role, ok
}

// Require wraps h so it runs only for requests carrying a valid Bearer token.
// When auth is disabled, every request is refused (write endpoints are closed).
func (a *Authenticator) Require(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.enabled {
			http.Error(w, "authentication is not enabled on this server", http.StatusForbidden)
			return
		}
		token := bearer(r)
		if token == "" {
			http.Error(w, "missing Bearer token", http.StatusUnauthorized)
			return
		}
		claims, err := a.Parse(token)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, principal{claims.Subject, claims.Role})
		h(w, r.WithContext(ctx))
	}
}

// Annotate attaches the caller's identity (if a valid token is present) without
// requiring it, so downstream middleware such as the rate limiter can treat
// privileged callers differently. Anonymous and invalid tokens pass through.
func (a *Authenticator) Annotate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.enabled {
			if token := bearer(r); token != "" {
				if claims, err := a.Parse(token); err == nil {
					r = r.WithContext(context.WithValue(r.Context(), claimsKey, principal{claims.Subject, claims.Role}))
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return ""
}

// HashPassword returns a bcrypt hash for building config users.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}
