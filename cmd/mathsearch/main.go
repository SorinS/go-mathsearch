// Command mathsearch ingests a corpus of Mathematica-syntax formulas into a
// SQLite store and searches it — exactly (canonical signature) and fuzzily
// (structural features) — via a CLI or a web UI.
//
// Subcommands:
//
//	mathsearch ingest -db f.db  <corpus.json | dir> ...   build/update the index
//	mathsearch search -db f.db  "<formula>"              query from the CLI
//	mathsearch serve  -db f.db  -addr :8080              run the web UI
//	mathsearch proof  -db f.db  <entry_id> [-set file -system lean4]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	canonical "sorins/canonical"
	"sorins/mathsearch/auth"
	"sorins/mathsearch/config"
	"sorins/mathsearch/feature"
	"sorins/mathsearch/store"
)

// Build metadata, injected via -ldflags.
var (
	gitCommit = "unknown"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = cmdIngest(os.Args[2:])
	case "search":
		err = cmdSearch(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "proof":
		err = cmdProof(os.Args[2:])
	case "hashpw":
		err = cmdHashpw(os.Args[2:])
	case "dedup":
		err = cmdDedup(os.Args[2:])
	case "version":
		fmt.Printf("mathsearch %s (built %s)\n", gitCommit, buildTime)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mathsearch:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: every subcommand accepts -config conf.json (and -db overrides it)

  mathsearch ingest [-config c.json] [-db f.db] [<corpus.json | dir | glob> ...]
  mathsearch search [-config c.json] [-db f.db] "<formula>"
  mathsearch serve  [-config c.json] [-db f.db] [-addr :8080]
  mathsearch dedup  [-config c.json] [-db f.db]
  mathsearch proof  [-config c.json] [-db f.db] <entry_id> [-set file -system lean4 -status draft]
  mathsearch hashpw "<password>"
  mathsearch version

With -config and no corpus arguments, ingest uses the config's corpus.roots.`)
}

// ---------------------------------------------------------------------------
// ingest
// ---------------------------------------------------------------------------

// entry mirrors a corpus JSON object (only the fields we use).
type entry struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Source   string `json:"source"`
	Class    string `json:"class"`
	LaTeX    string `json:"latex"`
	LHS      string `json:"lhs"`
	RHS      string `json:"rhs"`
	Verified string `json:"verified"`
}

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config JSON path")
	dbPath := fs.String("db", "", "SQLite path (overrides config)")
	rebuild := fs.Bool("rebuild", false, "force a full rebuild")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *dbPath != "" {
		cfg.Database = *dbPath
	}
	// Targets: positional args, else the config's corpus roots.
	targets := fs.Args()
	if len(targets) == 0 {
		targets = cfg.Corpus.Roots
	}
	if len(targets) == 0 {
		return fmt.Errorf("ingest needs a corpus file/directory (argument or config corpus.roots)")
	}

	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()

	stale, why, err := st.Stale()
	if err != nil {
		return err
	}
	if stale || *rebuild {
		if why == "" {
			why = "forced"
		}
		fmt.Printf("rebuilding index (%s)\n", why)
		if err := st.Reset(); err != nil {
			return err
		}
	}

	paths, err := expand(targets)
	if err != nil {
		return err
	}

	const batchSize = 1000
	ing, err := st.NewIngester()
	if err != nil {
		return err
	}
	inBatch := 0
	var indexed, parseFail int
	flush := func() error {
		if inBatch == 0 {
			return nil
		}
		if err := ing.Commit(); err != nil {
			return err
		}
		fmt.Printf("  committed %d\r", indexed)
		inBatch = 0
		ing, err = st.NewIngester()
		return err
	}

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("  SKIP %s: %v\n", filepath.Base(path), err)
			continue
		}
		var entries []entry
		if err := json.Unmarshal(raw, &entries); err != nil {
			fmt.Printf("  SKIP %s: invalid JSON: %v\n", filepath.Base(path), err)
			continue
		}
		for _, e := range entries {
			for _, side := range []struct{ name, mma string }{{"lhs", e.LHS}, {"rhs", e.RHS}} {
				if strings.TrimSpace(side.mma) == "" {
					continue
				}
				expr, perr := canonical.Parse(side.mma)
				if perr != nil {
					parseFail++
					continue
				}
				canon := canonical.Canonical(expr)
				doc := feature.Document(feature.TokensFrom(canon))
				f := store.Formula{
					EntryID: e.ID, Side: side.name, Label: e.Label, Source: e.Source,
					Class: e.Class, LaTeX: e.LaTeX, MMA: side.mma,
					Canonical: canon, Signature: canonical.Signature(expr), Verified: e.Verified,
				}
				if err := ing.Add(f, doc); err != nil {
					ing.Rollback()
					return fmt.Errorf("add %s/%s: %w", e.ID, side.name, err)
				}
				indexed++
				if inBatch++; inBatch >= batchSize {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	ing.Rollback() // no-op if the last flush already committed an empty batch

	total, _ := st.Count()
	fmt.Printf("\nindexed %d formulas (%d parse failures); %d in store\n", indexed, parseFail, total)
	return nil
}

// expand turns files, directories, and globs into a sorted list of JSON file
// paths. A leading ~ is expanded to the home directory, and a target containing
// glob metacharacters (e.g. "*.verified.json") is expanded as a pattern.
func expand(targets []string) ([]string, error) {
	var out []string
	for _, t := range targets {
		t = expandHome(t)
		if strings.ContainsAny(t, "*?[") {
			m, err := filepath.Glob(t)
			if err != nil {
				return nil, err
			}
			out = append(out, m...)
			continue
		}
		info, err := os.Stat(t)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			m, err := filepath.Glob(filepath.Join(t, "*.json"))
			if err != nil {
				return nil, err
			}
			out = append(out, m...)
		} else {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config JSON path")
	dbPath := fs.String("db", "", "SQLite path (overrides config)")
	limit := fs.Int("n", 10, "max fuzzy results")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf(`search needs a formula, e.g. mathsearch search "Integrate[E^(-a x^2), {x,0,Infinity}]"`)
	}
	query := strings.Join(fs.Args(), " ")

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *dbPath != "" {
		cfg.Database = *dbPath
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()

	expr, perr := canonical.Parse(query)
	if perr != nil {
		return fmt.Errorf("parse query: %w", perr)
	}
	canon := canonical.Canonical(expr)
	sig := canonical.Signature(expr)
	fmt.Printf("query canonical: %s\n\n", canon)

	exact, err := st.Exact(sig)
	if err != nil {
		return err
	}
	fmt.Printf("EXACT (%d)\n", len(exact))
	for _, r := range exact {
		printResult(r)
	}

	fuzzy, err := st.Fuzzy(feature.TokensFrom(canon), *limit, cfg.Search.CandidatePool)
	if err != nil {
		return err
	}
	fmt.Printf("\nFUZZY (%d)\n", len(fuzzy))
	for _, r := range fuzzy {
		printResult(r)
	}
	return nil
}

// cmdDedup reports formulas that appear under more than one entry (typically
// cross-corpus duplicates), grouped by canonical signature.
func cmdDedup(args []string) error {
	fs := flag.NewFlagSet("dedup", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config JSON path")
	dbPath := fs.String("db", "", "SQLite path (overrides config)")
	limit := fs.Int("n", 25, "max duplicate groups to show")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *dbPath != "" {
		cfg.Database = *dbPath
	}
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()

	groups, totalDup, err := st.Duplicates(*limit)
	if err != nil {
		return err
	}
	total, _ := st.Count()
	fmt.Printf("%d formulas indexed; %d distinct signatures are shared across multiple entries\n\n", total, totalDup)
	for _, g := range groups {
		ids := make([]string, 0, len(g.Occurrences))
		for _, o := range g.Occurrences {
			ids = append(ids, o.EntryID+"."+o.Side)
		}
		shown := ids
		suffix := ""
		if len(shown) > 6 {
			shown = shown[:6]
			suffix = fmt.Sprintf(", … (+%d more)", len(ids)-6)
		}
		fmt.Printf("  %s…  ×%-4d %s%s\n", g.Signature[:12], len(g.Occurrences), strings.Join(shown, ", "), suffix)
	}
	fmt.Println("\nNote: the largest groups are usually trivial forms (a lone symbol or constant)")
	fmt.Println("that all canonicalize to the same atom; those are genuinely equal but not interesting.")
	return nil
}

// cmdHashpw prints a bcrypt hash for a password, for building config users.
func cmdHashpw(args []string) error {
	fs := flag.NewFlagSet("hashpw", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf(`hashpw needs one password, e.g. mathsearch hashpw "secret"`)
	}
	h, err := auth.HashPassword(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func printResult(r store.Result) {
	latex := r.LaTeX
	if len(latex) > 80 {
		latex = latex[:80] + "…"
	}
	fmt.Printf("  %.3f  %-16s %-10s %s\n", r.Score, r.EntryID+"."+r.Side, r.Class, latex)
}
