package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	canonical "sorins/canonical"
	"sorins/mathsearch/auth"
	"sorins/mathsearch/config"
	"sorins/mathsearch/feature"
	"sorins/mathsearch/store"
)

//go:embed index.html
var indexHTML []byte

//go:embed docs.html
var docsHTML []byte

//go:embed openapi.json
var openapiJSON []byte

// server bundles the dependencies the HTTP handlers need.
type server struct {
	st    *store.Store
	cfg   *config.Config
	auth  *auth.Authenticator
	oauth *auth.OAuth
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config JSON path")
	dbPath := fs.String("db", "", "SQLite path (overrides config)")
	addr := fs.String("addr", "", "listen address (overrides config)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *dbPath != "" {
		cfg.Database = *dbPath
	}
	if *addr != "" {
		cfg.Server.Addr = *addr
	}

	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	if stale, why, _ := st.Stale(); stale {
		fmt.Fprintf(os.Stderr, "warning: index is stale (%s); run `mathsearch ingest -rebuild`\n", why)
	}

	srv := &server{st: st, cfg: cfg, auth: auth.New(cfg.Auth), oauth: auth.NewOAuth(cfg.Auth.OAuth)}

	mux := http.NewServeMux()
	// Public.
	mux.HandleFunc("GET /{$}", srv.handleIndex)
	mux.HandleFunc("GET /docs", srv.handleDocs)
	mux.HandleFunc("GET /api/openapi.json", srv.handleOpenAPI)
	mux.HandleFunc("GET /api/stats", srv.handleStats)
	mux.HandleFunc("GET /api/search", srv.handleSearch)
	mux.HandleFunc("GET /api/facets", srv.handleFacets)
	mux.HandleFunc("GET /api/proofs/{id}", srv.handleGetProof)
	mux.HandleFunc("POST /api/login", srv.handleLogin)
	// Social login (issues a rate-limited "social" token).
	mux.HandleFunc("GET /auth/{provider}", srv.handleOAuthStart)
	mux.HandleFunc("GET /auth/{provider}/callback", srv.handleOAuthCallback)
	// Authenticated (write).
	mux.HandleFunc("POST /api/formulas", srv.auth.Require(srv.handleAddFormula))
	mux.HandleFunc("PUT /api/proofs/{id}", srv.auth.Require(srv.handlePutProof))

	// Middleware: annotate identity first (outer), then rate-limit /api/ using
	// that identity (privileged callers are exempt).
	rl := auth.NewRateLimiter(cfg.Auth.RateLimit, "/api/")
	handler := srv.auth.Annotate(rl.Middleware(mux))

	httpSrv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      handler,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeoutSec) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeoutSec) * time.Second,
	}
	n, _ := st.Count()
	fmt.Printf("mathsearch serving %s (%d formulas, auth=%v, social=%v) on http://localhost%s\n",
		cfg.Database, n, srv.auth.Enabled(), srv.oauth.Providers(), cfg.Server.Addr)
	fmt.Printf("  UI: http://localhost%s/   API docs: http://localhost%s/docs\n", cfg.Server.Addr, cfg.Server.Addr)
	return httpSrv.ListenAndServe()
}

// ---------------------------------------------------------------------------
// OAuth (social login)
// ---------------------------------------------------------------------------

func (s *server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !s.oauth.Configured(provider) {
		http.Error(w, "unknown or unconfigured provider", http.StatusNotFound)
		return
	}
	state, err := randomState()
	if err != nil {
		http.Error(w, "state error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_state", Value: state, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 300,
	})
	url, err := s.oauth.AuthCodeURL(provider, state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

func (s *server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	c, err := r.Cookie("oauth_state")
	if err != nil || c.Value == "" || c.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	id, err := s.oauth.Identify(r.Context(), provider, code)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	subject := id.Provider + ":" + id.Subject
	token, err := s.auth.Issue(subject, auth.RoleSocial)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"role":       auth.RoleSocial,
		"provider":   id.Provider,
		"email":      id.Email,
		"expires_in": s.cfg.Auth.TokenTTLMin * 60,
	})
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(docsHTML)
}

func (s *server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(openapiJSON)
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	n, _ := s.st.Count()
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing query ?q="})
		return
	}
	expr, perr := canonical.Parse(q)
	if perr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": "parse error: " + perr.Error()})
		return
	}
	canon := canonical.Canonical(expr)
	exact, err := s.st.Exact(canonical.Signature(expr))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	fuzzy, err := s.st.Fuzzy(feature.TokensFrom(canon), s.cfg.Search.FuzzyLimit, s.cfg.Search.CandidatePool)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query_canonical": canon,
		"exact":           toHits(exact),
		"fuzzy":           toHits(fuzzy),
	})
}

func (s *server) handleFacets(w http.ResponseWriter, r *http.Request) {
	selected := r.URL.Query()["f"]
	total, refine, hits, err := s.st.Facets(selected, s.cfg.Search.FuzzyLimit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"selected": selected,
		"total":    total,
		"refine":   refine,
		"limit":    s.cfg.Search.FuzzyLimit,
	}
	if len(hits) > 0 {
		resp["hits"] = toHits(hits)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	token, err := s.auth.Login(cred.Username, cred.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid credentials"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_in": s.cfg.Auth.TokenTTLMin * 60,
	})
}

func (s *server) handleAddFormula(w http.ResponseWriter, r *http.Request) {
	var in entry
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if in.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id is required"})
		return
	}
	sigs := map[string]string{}
	indexed := 0
	for _, side := range []struct{ name, mma string }{{"lhs", in.LHS}, {"rhs", in.RHS}} {
		if side.mma == "" {
			continue
		}
		expr, perr := canonical.Parse(side.mma)
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": side.name + " parse error: " + perr.Error()})
			return
		}
		canon := canonical.Canonical(expr)
		sig := canonical.Signature(expr)
		f := store.Formula{
			EntryID: in.ID, Side: side.name, Label: in.Label, Source: in.Source,
			Class: in.Class, LaTeX: in.LaTeX, MMA: side.mma,
			Canonical: canon, Signature: sig, Verified: in.Verified,
		}
		if err := s.st.Upsert(f, feature.Document(feature.TokensFrom(canon))); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		sigs[side.name] = sig
		indexed++
	}
	if indexed == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "at least one of lhs/rhs is required"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexed": indexed, "signatures": sigs})
}

func (s *server) handleGetProof(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sys, status, proof, ok, err := s.st.Proof(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no proof for " + id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entry_id": id, "system": sys, "status": status, "proof": string(proof),
	})
}

func (s *server) handlePutProof(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		System string `json:"system"`
		Status string `json:"status"`
		Proof  string `json:"proof"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if in.System == "" || in.Proof == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "system and proof are required"})
		return
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	if err := s.st.SetProof(id, in.System, in.Status, []byte(in.Proof)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entry_id": id, "system": in.System, "status": in.Status})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type hitJSON struct {
	EntryID     string             `json:"entry_id"`
	Side        string             `json:"side"`
	Label       string             `json:"label"`
	Source      string             `json:"source"`
	Class       string             `json:"class"`
	LaTeX       string             `json:"latex"`
	MMA         string             `json:"mma"`
	Signature   string             `json:"signature"`
	Score       float64            `json:"score"`
	Occurrences []store.Occurrence `json:"occurrences,omitempty"`
}

func toHits(rs []store.Result) []hitJSON {
	out := make([]hitJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, hitJSON{
			EntryID: r.EntryID, Side: r.Side, Label: r.Label, Source: r.Source,
			Class: r.Class, LaTeX: r.LaTeX, MMA: r.MMA, Signature: r.Signature,
			Score: r.Score, Occurrences: r.Occurrences,
		})
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// proof (CLI)
// ---------------------------------------------------------------------------

func cmdProof(args []string) error {
	fs := flag.NewFlagSet("proof", flag.ExitOnError)
	dbPath := fs.String("db", "mathsearch.db", "SQLite database path")
	set := fs.String("set", "", "path to a proof file to attach (else print the stored proof)")
	system := fs.String("system", "lean4", "proof system: lean4 | coq | rocq")
	status := fs.String("status", "draft", "proof status: none | draft | verified | failed")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("proof needs exactly one entry id")
	}
	entryID := fs.Arg(0)

	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if *set != "" {
		data, err := os.ReadFile(*set)
		if err != nil {
			return err
		}
		if err := st.SetProof(entryID, *system, *status, data); err != nil {
			return err
		}
		fmt.Printf("stored %s proof (%s) for %s (%d bytes)\n", *system, *status, entryID, len(data))
		return nil
	}
	sys, stat, proof, ok, err := st.Proof(entryID)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Printf("no proof for %s\n", entryID)
		return nil
	}
	fmt.Printf("# %s proof for %s (%s)\n%s\n", sys, entryID, stat, proof)
	return nil
}
