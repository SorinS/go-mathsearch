// Package store persists canonicalized corpus formulas in SQLite and answers
// exact and fuzzy searches over them.
//
// Exact search keys on the go-canonical signature (invariant under
// associative/commutative reordering, alpha-renaming, and free-variable
// renaming). Fuzzy search uses an FTS5 index over structural feature tokens
// (see package feature), retrieved by BM25 and re-ranked by set similarity.
//
// Derived columns (canonical, signature, features) are cached, so the schema
// records an engine "probe" — the signature of a fixed canary expression. When
// go-canonical changes its normalization or serialization, the probe changes
// and Open reports the store as stale so callers can rebuild.
package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	canonical "sorins/canonical"
	"sorins/mathsearch/feature"

	_ "modernc.org/sqlite"
)

// schemaVersion is bumped when the table layout changes.
const schemaVersion = "2"

// probeInput is a fixed expression whose signature fingerprints the engine.
const probeInput = `Integrate[E^(-a*x^2)*Cos[b*x], {x, 0, Infinity}] + Sum[k^2, {k, 1, n}]`

// Store is a handle to the formula database.
type Store struct {
	db *sql.DB
}

// Formula is one searchable side (lhs or rhs) of a corpus entry.
type Formula struct {
	EntryID   string
	Side      string // "lhs" | "rhs"
	Label     string
	Source    string
	Class     string
	LaTeX     string // whole-entry LaTeX, for display
	MMA       string // Mathematica text of this side
	Canonical string
	Signature string
	Verified  string
}

// Occurrence is one place a formula (by signature) appears in the corpus.
type Occurrence struct {
	EntryID string `json:"entry_id"`
	Side    string `json:"side"`
	Source  string `json:"source"`
	Label   string `json:"label"`
}

// Result is a search hit with a relevance score (1.0 for exact). Rows sharing
// a signature are collapsed into one Result whose Occurrences list every place
// the formula appears (its cross-corpus duplicates). IdentityMMA is the full
// identity (lhs == rhs) in Mathematica, so a hit reads sensibly even when only
// one side matched or the LaTeX is one-sided.
type Result struct {
	Formula
	Score       float64      `json:"score"`
	IdentityMMA string       `json:"identity_mma,omitempty"`
	Occurrences []Occurrence `json:"occurrences,omitempty"`
}

// fillIdentities populates IdentityMMA for each result by fetching both sides of
// its entry. The result set is small (post-collapse, post-limit), so the extra
// per-hit lookups are cheap.
func (s *Store) fillIdentities(rs []Result) {
	for i := range rs {
		lhs, rhs := s.entrySides(rs[i].EntryID)
		switch {
		case lhs != "" && rhs != "":
			rs[i].IdentityMMA = lhs + " == " + rhs
		case lhs != "":
			rs[i].IdentityMMA = lhs
		default:
			rs[i].IdentityMMA = rhs
		}
	}
}

func (s *Store) entrySides(entryID string) (lhs, rhs string) {
	rows, err := s.db.Query(`SELECT side, mma FROM formulas WHERE entry_id = ?`, entryID)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	for rows.Next() {
		var side, mma string
		if rows.Scan(&side, &mma) == nil {
			if side == "lhs" {
				lhs = mma
			} else if side == "rhs" {
				rhs = mma
			}
		}
	}
	return lhs, rhs
}

// collapse merges results that share a signature, keeping the first (highest
// scoring, since callers pass results in rank order) and attaching every row as
// an Occurrence. This is signature-based de-duplication across corpora.
func collapse(rs []Result) []Result {
	byID := map[string]int{} // signature -> index in out
	var out []Result
	for _, r := range rs {
		occ := Occurrence{EntryID: r.EntryID, Side: r.Side, Source: r.Source, Label: r.Label}
		if i, ok := byID[r.Signature]; ok {
			out[i].Occurrences = append(out[i].Occurrences, occ)
			continue
		}
		byID[r.Signature] = len(out)
		r.Occurrences = []Occurrence{occ}
		out = append(out, r)
	}
	return out
}

// Open opens or creates the database at path and ensures the schema exists.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// WAL + relaxed sync make bulk ingest and concurrent reads fast; a single
	// crash can lose only the last uncommitted transaction, which ingest simply
	// re-runs.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Ingester batches many Upserts into one transaction with prepared statements,
// so bulk ingest pays a single fsync at Commit instead of one per formula.
type Ingester struct {
	tx    *sql.Tx
	ins   *sql.Stmt
	delF  *sql.Stmt
	insF  *sql.Stmt
	delFc *sql.Stmt
	insFc *sql.Stmt
}

// NewIngester begins a batched ingest transaction.
func (s *Store) NewIngester() (*Ingester, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	ins, err := tx.Prepare(
		`INSERT INTO formulas(entry_id,side,label,source,class,latex,mma,canonical,signature,verified)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(entry_id,side) DO UPDATE SET
		   label=excluded.label, source=excluded.source, class=excluded.class,
		   latex=excluded.latex, mma=excluded.mma, canonical=excluded.canonical,
		   signature=excluded.signature, verified=excluded.verified
		 RETURNING id`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	delF, err := tx.Prepare(`DELETE FROM formula_fts WHERE rowid = ?`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	insF, err := tx.Prepare(`INSERT INTO formula_fts(rowid, features) VALUES(?, ?)`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	delFc, err := tx.Prepare(`DELETE FROM facets WHERE formula_id = ?`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	insFc, err := tx.Prepare(`INSERT OR IGNORE INTO facets(formula_id, facet) VALUES(?, ?)`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return &Ingester{tx: tx, ins: ins, delF: delF, insF: insF, delFc: delFc, insFc: insFc}, nil
}

// Add upserts one formula, its feature tokens, and its facets within the batch.
func (ing *Ingester) Add(f Formula, features string) error {
	var id int64
	if err := ing.ins.QueryRow(
		f.EntryID, f.Side, f.Label, f.Source, f.Class, f.LaTeX, f.MMA, f.Canonical, f.Signature, f.Verified,
	).Scan(&id); err != nil {
		return err
	}
	if _, err := ing.delF.Exec(id); err != nil {
		return err
	}
	if _, err := ing.insF.Exec(id, features); err != nil {
		return err
	}
	if _, err := ing.delFc.Exec(id); err != nil {
		return err
	}
	for _, fc := range feature.FacetsFrom(f.Canonical) {
		if _, err := ing.insFc.Exec(id, fc); err != nil {
			return err
		}
	}
	return nil
}

// Commit finalizes the batch.
func (ing *Ingester) Commit() error { return ing.tx.Commit() }

// Rollback discards the batch.
func (ing *Ingester) Rollback() error { return ing.tx.Rollback() }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);

CREATE TABLE IF NOT EXISTS formulas (
    id        INTEGER PRIMARY KEY,
    entry_id  TEXT NOT NULL,
    side      TEXT NOT NULL,
    label     TEXT,
    source    TEXT,
    class     TEXT,
    latex     TEXT,
    mma       TEXT,
    canonical TEXT,
    signature TEXT NOT NULL,
    verified  TEXT,
    UNIQUE(entry_id, side)
);
CREATE INDEX IF NOT EXISTS idx_signature ON formulas(signature);

-- Each corpus entry (an identity lhs = rhs) may carry a formal proof.
CREATE TABLE IF NOT EXISTS proofs (
    entry_id TEXT PRIMARY KEY,
    system   TEXT,   -- 'lean4' | 'coq' | 'rocq'
    status   TEXT,   -- 'none' | 'draft' | 'verified' | 'failed'
    proof    BLOB,
    updated  TEXT
);

-- Fuzzy index: structural feature tokens per formula (rowid = formulas.id).
CREATE VIRTUAL TABLE IF NOT EXISTS formula_fts USING fts5(features, content='');

-- Faceted filtering: coarse structural tags per formula (is-integral,
-- has-square-root, bessel, ...).
CREATE TABLE IF NOT EXISTS facets (
    formula_id INTEGER NOT NULL,
    facet      TEXT NOT NULL,
    PRIMARY KEY(formula_id, facet)
);
CREATE INDEX IF NOT EXISTS idx_facet ON facets(facet);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	// Record schema + engine probe if not already present.
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO meta(key, value) VALUES('schema_version', ?)`, schemaVersion); err != nil {
		return err
	}
	return nil
}

// EngineProbe returns the current engine fingerprint (signature of the canary).
func EngineProbe() string { return canonical.Signature(canonical.MustParse(probeInput)) }

// Stale reports whether the stored engine/schema fingerprint differs from the
// current one, meaning the derived columns must be rebuilt.
func (s *Store) Stale() (bool, string, error) {
	stored, err := s.meta("engine_probe")
	if err != nil {
		return false, "", err
	}
	schema, err := s.meta("schema_version")
	if err != nil {
		return false, "", err
	}
	if stored == "" {
		return true, "no engine fingerprint recorded (empty store)", nil
	}
	if stored != EngineProbe() {
		return true, "go-canonical engine changed since last ingest", nil
	}
	if schema != schemaVersion {
		return true, "schema version changed", nil
	}
	return false, "", nil
}

func (s *Store) meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) setMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value)
	return err
}

// Reset drops all indexed formulas (keeps proofs) and stamps the current engine
// probe. Call before a full rebuild.
func (s *Store) Reset() error {
	if _, err := s.db.Exec(`DELETE FROM formulas; DELETE FROM formula_fts; DELETE FROM facets;`); err != nil {
		return err
	}
	if err := s.setMeta("engine_probe", EngineProbe()); err != nil {
		return err
	}
	return s.setMeta("schema_version", schemaVersion)
}

// Upsert inserts or replaces a formula and its feature tokens.
func (s *Store) Upsert(f Formula, features string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(
		`INSERT INTO formulas(entry_id,side,label,source,class,latex,mma,canonical,signature,verified)
		 VALUES(?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(entry_id,side) DO UPDATE SET
		   label=excluded.label, source=excluded.source, class=excluded.class,
		   latex=excluded.latex, mma=excluded.mma, canonical=excluded.canonical,
		   signature=excluded.signature, verified=excluded.verified
		 RETURNING id`,
		f.EntryID, f.Side, f.Label, f.Source, f.Class, f.LaTeX, f.MMA, f.Canonical, f.Signature, f.Verified,
	).Scan(&id)
	if err != nil {
		return err
	}
	// Refresh the FTS row for this id.
	if _, err := tx.Exec(`DELETE FROM formula_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO formula_fts(rowid, features) VALUES(?, ?)`, id, features); err != nil {
		return err
	}
	// Refresh facets.
	if _, err := tx.Exec(`DELETE FROM facets WHERE formula_id = ?`, id); err != nil {
		return err
	}
	for _, fc := range feature.FacetsFrom(f.Canonical) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO facets(formula_id, facet) VALUES(?, ?)`, id, fc); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FacetCount is a facet and how many matching formulas carry it.
type FacetCount struct {
	Facet string `json:"facet"`
	Count int    `json:"count"`
}

// Facets runs a progressive filter: it returns the number of formulas carrying
// all selected facets, the facets available to refine further (with counts,
// excluding those already selected), and — when the match set is at or below
// listLimit — the matching formulas themselves.
func (s *Store) Facets(selected []string, listLimit int) (total int, refine []FacetCount, hits []Result, err error) {
	// The set of formula ids carrying every selected facet.
	var idFilter string
	var idArgs []any
	if len(selected) > 0 {
		ph := make([]string, len(selected))
		for i, f := range selected {
			ph[i] = "?"
			idArgs = append(idArgs, f)
		}
		idFilter = fmt.Sprintf(
			`SELECT formula_id FROM facets WHERE facet IN (%s)
			 GROUP BY formula_id HAVING COUNT(DISTINCT facet) = %d`,
			strings.Join(ph, ","), len(selected))
	} else {
		idFilter = `SELECT id FROM formulas`
	}

	if err = s.db.QueryRow(`SELECT COUNT(*) FROM (`+idFilter+`)`, idArgs...).Scan(&total); err != nil {
		return 0, nil, nil, err
	}

	// Refinement facets present in the matching set, most common first.
	sel := map[string]bool{}
	for _, f := range selected {
		sel[f] = true
	}
	rows, err := s.db.Query(
		`SELECT facet, COUNT(*) c FROM facets WHERE formula_id IN (`+idFilter+`)
		 GROUP BY facet ORDER BY c DESC, facet`, idArgs...)
	if err != nil {
		return 0, nil, nil, err
	}
	for rows.Next() {
		var fc FacetCount
		if err = rows.Scan(&fc.Facet, &fc.Count); err != nil {
			rows.Close()
			return 0, nil, nil, err
		}
		if !sel[fc.Facet] {
			refine = append(refine, fc)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, nil, nil, err
	}

	// The list, once the set is digestible.
	if total > 0 && total <= listLimit {
		frows, err := s.db.Query(
			`SELECT entry_id,side,label,source,class,latex,mma,canonical,signature,verified
			 FROM formulas WHERE id IN (`+idFilter+`) ORDER BY entry_id, side`, idArgs...)
		if err != nil {
			return 0, nil, nil, err
		}
		defer frows.Close()
		for frows.Next() {
			var f Formula
			if err := frows.Scan(&f.EntryID, &f.Side, &f.Label, &f.Source, &f.Class,
				&f.LaTeX, &f.MMA, &f.Canonical, &f.Signature, &f.Verified); err != nil {
				return 0, nil, nil, err
			}
			hits = append(hits, Result{Formula: f, Score: 1})
		}
		if err := frows.Err(); err != nil {
			return 0, nil, nil, err
		}
		hits = collapse(hits)
		s.fillIdentities(hits)
	}
	return total, refine, hits, nil
}

// DuplicateGroup is a signature shared by two or more distinct entries.
type DuplicateGroup struct {
	Signature   string
	Occurrences []Occurrence
}

// Duplicates reports signatures that appear under more than one distinct entry
// id (i.e. cross-entry, typically cross-corpus, duplicates), most-duplicated
// first, up to limit groups.
func (s *Store) Duplicates(limit int) (groups []DuplicateGroup, totalDupSigs int, err error) {
	// Count signatures with >1 distinct entry_id.
	if err = s.db.QueryRow(
		`SELECT COUNT(*) FROM (SELECT signature FROM formulas GROUP BY signature
		 HAVING COUNT(DISTINCT entry_id) > 1)`).Scan(&totalDupSigs); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(
		`SELECT signature FROM formulas GROUP BY signature
		 HAVING COUNT(DISTINCT entry_id) > 1
		 ORDER BY COUNT(DISTINCT entry_id) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, 0, err
	}
	var sigs []string
	for rows.Next() {
		var sig string
		if err = rows.Scan(&sig); err != nil {
			rows.Close()
			return nil, 0, err
		}
		sigs = append(sigs, sig)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	for _, sig := range sigs {
		occ, err := s.occurrences(sig)
		if err != nil {
			return nil, 0, err
		}
		groups = append(groups, DuplicateGroup{Signature: sig, Occurrences: occ})
	}
	return groups, totalDupSigs, nil
}

func (s *Store) occurrences(sig string) ([]Occurrence, error) {
	rows, err := s.db.Query(
		`SELECT entry_id, side, source, label FROM formulas WHERE signature = ? ORDER BY entry_id, side`, sig)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Occurrence
	for rows.Next() {
		var o Occurrence
		if err := rows.Scan(&o.EntryID, &o.Side, &o.Source, &o.Label); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Count returns the number of indexed formulas.
func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM formulas`).Scan(&n)
	return n, err
}

// Exact returns all formulas whose signature equals sig.
func (s *Store) Exact(sig string) ([]Result, error) {
	rows, err := s.db.Query(
		`SELECT entry_id,side,label,source,class,latex,mma,canonical,signature,verified
		 FROM formulas WHERE signature = ?`, sig)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var f Formula
		if err := rows.Scan(&f.EntryID, &f.Side, &f.Label, &f.Source, &f.Class,
			&f.LaTeX, &f.MMA, &f.Canonical, &f.Signature, &f.Verified); err != nil {
			return nil, err
		}
		out = append(out, Result{Formula: f, Score: 1.0})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res := collapse(out)
	s.fillIdentities(res)
	return res, nil
}

// Fuzzy retrieves candidates by BM25 over shared feature tokens and re-ranks
// them by Jaccard similarity of the full feature sets. queryTokens are the
// query's structural tokens (feature.TokensFrom). It returns up to limit hits.
func (s *Store) Fuzzy(queryTokens []string, limit, pool int) ([]Result, error) {
	match := feature.MatchQuery(queryTokens)
	if match == "" {
		return nil, nil
	}
	if pool < limit {
		pool = limit * 5
	}
	rows, err := s.db.Query(
		`SELECT f.entry_id,f.side,f.label,f.source,f.class,f.latex,f.mma,f.canonical,f.signature,f.verified
		 FROM formula_fts fts JOIN formulas f ON f.id = fts.rowid
		 WHERE formula_fts MATCH ? ORDER BY bm25(formula_fts) LIMIT ?`, match, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Result
	for rows.Next() {
		var f Formula
		if err := rows.Scan(&f.EntryID, &f.Side, &f.Label, &f.Source, &f.Class,
			&f.LaTeX, &f.MMA, &f.Canonical, &f.Signature, &f.Verified); err != nil {
			return nil, err
		}
		score := feature.Jaccard(queryTokens, feature.TokensFrom(f.Canonical))
		out = append(out, Result{Formula: f, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	out = collapse(out) // de-duplicate by signature before limiting
	if len(out) > limit {
		out = out[:limit]
	}
	s.fillIdentities(out)
	return out, nil
}

// SetProof stores a formal proof for an entry.
func (s *Store) SetProof(entryID, system, status string, proof []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO proofs(entry_id,system,status,proof,updated) VALUES(?,?,?,?,datetime('now'))
		 ON CONFLICT(entry_id) DO UPDATE SET
		   system=excluded.system, status=excluded.status, proof=excluded.proof, updated=excluded.updated`,
		entryID, system, status, proof)
	return err
}

// Proof returns the stored proof for an entry, or ok=false if none.
func (s *Store) Proof(entryID string) (system, status string, proof []byte, ok bool, err error) {
	row := s.db.QueryRow(`SELECT system,status,proof FROM proofs WHERE entry_id = ?`, entryID)
	err = row.Scan(&system, &status, &proof)
	if err == sql.ErrNoRows {
		return "", "", nil, false, nil
	}
	if err != nil {
		return "", "", nil, false, err
	}
	return system, status, proof, true, nil
}
