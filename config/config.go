// Package config defines the JSON configuration that drives every part of the
// application: the database, the HTTP server, corpus locations, search tuning,
// and authentication.
//
// Load starts from built-in defaults and overlays a JSON file (if given). A few
// security-sensitive values may also come from the environment so secrets need
// not live in the file: MATHSEARCH_JWT_SECRET overrides auth.jwt_secret.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config is the root configuration object.
type Config struct {
	Database string `json:"database"`
	Server   Server `json:"server"`
	Corpus   Corpus `json:"corpus"`
	Search   Search `json:"search"`
	Auth     Auth   `json:"auth"`
}

// Server holds HTTP server settings.
type Server struct {
	Addr            string `json:"addr"`
	ReadTimeoutSec  int    `json:"read_timeout_sec"`
	WriteTimeoutSec int    `json:"write_timeout_sec"`
}

// Corpus lists corpus files/directories to ingest by default.
type Corpus struct {
	Roots []string `json:"roots"`
}

// Search tunes retrieval.
type Search struct {
	FuzzyLimit    int `json:"fuzzy_limit"`    // results returned
	CandidatePool int `json:"candidate_pool"` // BM25 candidates before re-rank
}

// Auth configures authentication. Privileged users authenticate with a
// username/password against Users and receive a "privileged" token; social
// users authenticate via an OAuth provider and receive a rate-limited "social"
// token. Both are the same signed JWT, distinguished by a role claim.
type Auth struct {
	// Enabled gates the authenticated endpoints. When false, write endpoints
	// are refused entirely (read/search stay public).
	Enabled     bool                     `json:"enabled"`
	JWTSecret   string                   `json:"jwt_secret"`
	TokenTTLMin int                      `json:"token_ttl_minutes"`
	Users       []User                   `json:"users"`
	OAuth       map[string]OAuthProvider `json:"oauth"` // keyed by "google"|"facebook"|"apple"
	RateLimit   RateLimit                `json:"rate_limit"`
}

// User is a privileged login credential; PasswordHash is a bcrypt hash.
type User struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

// OAuthProvider configures one social login provider. The endpoint URLs default
// to the provider's well-known values when left empty (see auth.Presets).
type OAuthProvider struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	UserInfoURL  string   `json:"user_info_url"`
}

// RateLimit tunes the token-bucket limiter applied to API requests. Privileged
// tokens are exempt; social tokens and anonymous callers are limited.
type RateLimit struct {
	RequestsPerMinute int  `json:"requests_per_minute"`
	Burst             int  `json:"burst"`
	PrivilegedExempt  bool `json:"privileged_exempt"`
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Database: "mathsearch.db",
		Server: Server{
			Addr:            ":8080",
			ReadTimeoutSec:  15,
			WriteTimeoutSec: 30,
		},
		Search: Search{
			FuzzyLimit:    25,
			CandidatePool: 200,
		},
		Auth: Auth{
			Enabled:     false,
			TokenTTLMin: 60,
			RateLimit: RateLimit{
				RequestsPerMinute: 60,
				Burst:             20,
				PrivilegedExempt:  true,
			},
		},
	}
}

// Load returns Default overlaid with the JSON at path (empty path = defaults
// only) and environment overrides applied.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if v := os.Getenv("MATHSEARCH_JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.Search.FuzzyLimit <= 0 {
		c.Search.FuzzyLimit = 25
	}
	if c.Search.CandidatePool < c.Search.FuzzyLimit {
		c.Search.CandidatePool = c.Search.FuzzyLimit * 5
	}
	if c.Auth.Enabled && c.Auth.JWTSecret == "" {
		return fmt.Errorf("auth.enabled is true but no jwt_secret is set (config or MATHSEARCH_JWT_SECRET)")
	}
	if c.Auth.TokenTTLMin <= 0 {
		c.Auth.TokenTTLMin = 60
	}
	return nil
}
