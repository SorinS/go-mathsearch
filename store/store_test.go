package store

import (
	"testing"

	canonical "sorins/canonical"
	"sorins/mathsearch/feature"
)

// add ingests one formula built from a Mathematica string.
func add(t *testing.T, s *Store, id, side, mma string) {
	t.Helper()
	e, err := canonical.Parse(mma)
	if err != nil {
		t.Fatalf("parse %q: %v", mma, err)
	}
	canon := canonical.Canonical(e)
	f := Formula{
		EntryID: id, Side: side, Source: "test", Class: "identity",
		LaTeX: mma, MMA: mma, Canonical: canon, Signature: canonical.Signature(e),
	}
	if err := s.Upsert(f, feature.Document(feature.TokensFrom(canon))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestExactAndDedup(t *testing.T) {
	s := open(t)
	// Two different entries whose formulas are canonically equal (operand order
	// + variable renaming) must share a signature and collapse.
	add(t, s, "a", "rhs", "a*b + c")
	add(t, s, "b", "rhs", "z*y + x") // same modulo AC + renaming

	e := canonical.MustParse("c + a*b")
	res, err := s.Exact(canonical.Signature(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 collapsed exact result, got %d", len(res))
	}
	if len(res[0].Occurrences) != 2 {
		t.Errorf("expected 2 occurrences (a, b), got %d", len(res[0].Occurrences))
	}

	groups, total, err := s.Duplicates(10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 || len(groups[0].Occurrences) != 2 {
		t.Errorf("duplicates: total=%d groups=%d", total, len(groups))
	}
}

func TestFuzzyRanks(t *testing.T) {
	s := open(t)
	add(t, s, "close", "rhs", "Integrate[E^(-a*x^2)*Cos[b*x], {x, 0, Infinity}]")
	add(t, s, "far", "rhs", "Sum[1/k^2, {k, 1, Infinity}]")

	q := canonical.MustParse("Integrate[E^(-a*x^2), {x, 0, Infinity}]")
	res, err := s.Fuzzy(feature.TokensFrom(canonical.Canonical(q)), 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no fuzzy results")
	}
	if res[0].EntryID != "close" {
		t.Errorf("expected 'close' ranked first, got %q", res[0].EntryID)
	}
}

func TestFacets(t *testing.T) {
	s := open(t)
	add(t, s, "i1", "rhs", "Integrate[BesselJ[0, x]/x, {x, 0, Infinity}]")
	add(t, s, "i2", "rhs", "Integrate[Sin[x]/x, {x, 0, Infinity}]")
	add(t, s, "s1", "rhs", "Sum[1/k^2, {k, 1, Infinity}]")

	// All three share nothing that makes them one; filter by integral.
	total, refine, hits, err := s.Facets([]string{"integral"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("integral count = %d, want 2", total)
	}
	// bessel should appear as a refinement of the integral set.
	found := false
	for _, fc := range refine {
		if fc.Facet == "bessel" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'bessel' among refinements of 'integral'")
	}
	if len(hits) != 2 { // total <= listLimit, so hits are returned
		t.Errorf("expected 2 listed hits, got %d", len(hits))
	}

	// Narrow to bessel integrals -> just i1.
	total, _, hits, err = s.Facets([]string{"integral", "bessel"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(hits) != 1 || hits[0].EntryID != "i1" {
		t.Errorf("integral+bessel: total=%d hits=%d", total, len(hits))
	}
}

func TestStaleOnEngineChange(t *testing.T) {
	s := open(t)
	if stale, _, err := s.Stale(); err != nil || stale {
		t.Errorf("fresh store should not be stale (stale=%v err=%v)", stale, err)
	}
}

func TestProofRoundTrip(t *testing.T) {
	s := open(t)
	if err := s.SetProof("thm-1", "lean4", "verified", []byte("theorem foo : 1 = 1 := rfl")); err != nil {
		t.Fatal(err)
	}
	sys, status, proof, ok, err := s.Proof("thm-1")
	if err != nil || !ok {
		t.Fatalf("proof missing: ok=%v err=%v", ok, err)
	}
	if sys != "lean4" || status != "verified" || string(proof) == "" {
		t.Errorf("proof mismatch: %s/%s/%q", sys, status, proof)
	}
}
