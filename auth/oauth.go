package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"golang.org/x/oauth2"

	"sorins/mathsearch/config"
)

// Identity is the social identity resolved from an OAuth provider.
type Identity struct {
	Provider string
	Subject  string // stable provider user id
	Email    string
}

// providerPreset holds the well-known endpoints for a provider, used when the
// config leaves them empty.
type providerPreset struct {
	authURL, tokenURL, userInfoURL string
	useIDToken                     bool // identity from the id_token instead of a userinfo call
	scopes                         []string
}

var presets = map[string]providerPreset{
	"google": {
		authURL:     "https://accounts.google.com/o/oauth2/auth",
		tokenURL:    "https://oauth2.googleapis.com/token",
		userInfoURL: "https://www.googleapis.com/oauth2/v3/userinfo",
		scopes:      []string{"openid", "email", "profile"},
	},
	"facebook": {
		authURL:     "https://www.facebook.com/v18.0/dialog/oauth",
		tokenURL:    "https://graph.facebook.com/v18.0/oauth/access_token",
		userInfoURL: "https://graph.facebook.com/me?fields=id,name,email",
		scopes:      []string{"email"},
	},
	"apple": {
		authURL:    "https://appleid.apple.com/auth/authorize",
		tokenURL:   "https://appleid.apple.com/auth/token",
		useIDToken: true,
		scopes:     []string{"name", "email"},
	},
}

// OAuth manages the configured social login providers.
type OAuth struct {
	cfgs   map[string]*oauth2.Config
	preset map[string]providerPreset
	client *http.Client
}

// NewOAuth builds the manager from the provider configs, filling endpoint URLs
// from presets where the config omits them.
func NewOAuth(providers map[string]config.OAuthProvider) *OAuth {
	o := &OAuth{
		cfgs:   map[string]*oauth2.Config{},
		preset: map[string]providerPreset{},
		client: http.DefaultClient,
	}
	for name, p := range providers {
		if p.ClientID == "" {
			continue // not configured
		}
		pre := presets[name] // zero value if unknown
		authURL := firstNonEmpty(p.AuthURL, pre.authURL)
		tokenURL := firstNonEmpty(p.TokenURL, pre.tokenURL)
		scopes := p.Scopes
		if len(scopes) == 0 {
			scopes = pre.scopes
		}
		o.cfgs[name] = &oauth2.Config{
			ClientID:     p.ClientID,
			ClientSecret: p.ClientSecret,
			RedirectURL:  p.RedirectURL,
			Scopes:       scopes,
			Endpoint:     oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL},
		}
		pre.userInfoURL = firstNonEmpty(p.UserInfoURL, pre.userInfoURL)
		o.preset[name] = pre
	}
	return o
}

// Providers lists the configured provider names, sorted.
func (o *OAuth) Providers() []string {
	out := make([]string, 0, len(o.cfgs))
	for n := range o.cfgs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Configured reports whether a provider is set up.
func (o *OAuth) Configured(name string) bool { _, ok := o.cfgs[name]; return ok }

// AuthCodeURL returns the provider's authorization URL for the given state.
func (o *OAuth) AuthCodeURL(name, state string) (string, error) {
	c, ok := o.cfgs[name]
	if !ok {
		return "", fmt.Errorf("provider %q not configured", name)
	}
	return c.AuthCodeURL(state, oauth2.AccessTypeOnline), nil
}

// Identify completes the code exchange and resolves the social identity.
func (o *OAuth) Identify(ctx context.Context, name, code string) (Identity, error) {
	c, ok := o.cfgs[name]
	if !ok {
		return Identity{}, fmt.Errorf("provider %q not configured", name)
	}
	tok, err := c.Exchange(ctx, code)
	if err != nil {
		return Identity{}, fmt.Errorf("token exchange: %w", err)
	}
	pre := o.preset[name]
	if pre.useIDToken {
		return identityFromIDToken(name, tok)
	}
	return o.identityFromUserInfo(ctx, name, pre.userInfoURL, tok)
}

func (o *OAuth) identityFromUserInfo(ctx context.Context, name, url string, tok *oauth2.Token) (Identity, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	tok.SetAuthHeader(req)
	resp, err := o.client.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	var info struct {
		Sub   string `json:"sub"`
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return Identity{}, fmt.Errorf("userinfo decode: %w", err)
	}
	sub := firstNonEmpty(info.Sub, info.ID)
	if sub == "" {
		return Identity{}, fmt.Errorf("userinfo missing subject")
	}
	return Identity{Provider: name, Subject: sub, Email: info.Email}, nil
}

// identityFromIDToken reads the claims from an OIDC id_token (used by Apple).
// The token comes directly from the provider's TLS token endpoint; production
// deployments should additionally verify its signature against the provider's
// JWKS.
func identityFromIDToken(name string, tok *oauth2.Token) (Identity, error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, fmt.Errorf("no id_token in response")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, fmt.Errorf("id_token payload: %w", err)
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, fmt.Errorf("id_token claims: %w", err)
	}
	if claims.Sub == "" {
		return Identity{}, fmt.Errorf("id_token missing subject")
	}
	return Identity{Provider: name, Subject: claims.Sub, Email: claims.Email}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
