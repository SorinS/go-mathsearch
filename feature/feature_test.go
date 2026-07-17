package feature

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseSimple(t *testing.T) {
	n, err := Parse("Plus[Times[v0,v1],v2]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Op != "Plus" || len(n.Kids) != 2 {
		t.Fatalf("got Op=%q kids=%d", n.Op, len(n.Kids))
	}
	if n.Kids[0].Op != "Times" || n.Kids[1].Leaf != "v2" {
		t.Errorf("unexpected children: %+v", n.Kids)
	}
}

func TestParseLeaves(t *testing.T) {
	n, err := Parse("Integrate[#0,$Infinity,Power[$E,Times[#-1,@0]]]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Op != "Integrate" || len(n.Kids) != 3 {
		t.Fatalf("got %q/%d", n.Op, len(n.Kids))
	}
	if n.Kids[0].Leaf != "#0" || n.Kids[1].Leaf != "$Infinity" {
		t.Errorf("leaves: %q %q", n.Kids[0].Leaf, n.Kids[1].Leaf)
	}
}

// The quote trap: a string atom may contain commas and brackets.
func TestParseQuotedString(t *testing.T) {
	n, err := Parse(`f[$"a,b[c]",v0]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Kids) != 2 {
		t.Fatalf("expected 2 args, got %d", len(n.Kids))
	}
	if n.Kids[0].Leaf != `$"a,b[c]"` {
		t.Errorf("string leaf mis-parsed: %q", n.Kids[0].Leaf)
	}
	if n.Kids[1].Leaf != "v0" {
		t.Errorf("second arg: %q", n.Kids[1].Leaf)
	}
}

func TestParseEmptyArgs(t *testing.T) {
	n, err := Parse("List[]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Op != "List" || len(n.Kids) != 0 {
		t.Errorf("List[] -> %q/%d", n.Op, len(n.Kids))
	}
}

func TestJaccard(t *testing.T) {
	a := []string{"x", "y", "z"}
	if g := Jaccard(a, a); g != 1 {
		t.Errorf("identical Jaccard = %v, want 1", g)
	}
	if g := Jaccard([]string{"a"}, []string{"b"}); g != 0 {
		t.Errorf("disjoint Jaccard = %v, want 0", g)
	}
	if g := Jaccard([]string{"a", "b"}, []string{"b", "c"}); g != 1.0/3 {
		t.Errorf("Jaccard = %v, want 1/3", g)
	}
}

func TestTokensShared(t *testing.T) {
	// Two formulas sharing a subexpression should share tokens.
	a := TokensFrom("Plus[Power[v0,#2],v1]")
	b := TokensFrom("Plus[Power[v0,#2],Sin[v1]]")
	if Jaccard(a, b) == 0 {
		t.Error("formulas sharing Power[v0,#2] should have nonzero similarity")
	}
	if Jaccard(a, b) == 1 {
		t.Error("different formulas should not be identical")
	}
}

func facetSet(canon string) map[string]bool {
	m := map[string]bool{}
	for _, f := range FacetsFrom(canon) {
		m[f] = true
	}
	return m
}

func TestFacets(t *testing.T) {
	cases := []struct {
		canon string
		want  []string
	}{
		{"Integrate[#0,$Infinity,Sin[@0]]", []string{"integral", "definite-integral", "infinite-limit", "trigonometric"}},
		{"Integrate[v0]", []string{"integral", "indefinite-integral"}},
		{"Power[v0,#1/2]", []string{"square-root"}},
		{"Power[v0,#-1]", []string{"fraction"}},
		{"Power[v0,#-1/2]", []string{"fraction", "square-root"}},
		{"BesselJ[v0,v1]", []string{"bessel"}},
		{"Plus[$Pi,$E]", []string{"has-pi", "has-e"}},
	}
	for _, c := range cases {
		got := facetSet(c.canon)
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("%s: missing facet %q (got %v)", c.canon, w, sortedKeys(got))
			}
		}
	}
}

func TestParseRat(t *testing.T) {
	cases := []struct {
		in       string
		num, den int64
		ok       bool
	}{
		{"#3", 3, 1, true},
		{"#-1/2", -1, 2, true},
		{"#1/2", 1, 2, true},
		{"v0", 0, 0, false},
		{"#", 0, 0, false},
	}
	for _, c := range cases {
		n, d, ok := parseRat(c.in)
		if ok != c.ok || (ok && (n != c.num || d != c.den)) {
			t.Errorf("parseRat(%q) = %d/%d,%v; want %d/%d,%v", c.in, n, d, ok, c.num, c.den, c.ok)
		}
	}
}

func TestDocumentAndMatchQueryAreAlnum(t *testing.T) {
	toks := TokensFrom("Integrate[#0,$Infinity,Power[$E,Times[#-1,@0]]]")
	doc := Document(toks)
	for _, r := range doc {
		if !(r == ' ' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == 't') {
			t.Fatalf("FTS document has a non-token char %q in %q", r, doc)
		}
	}
	if MatchQuery(toks) == "" {
		t.Error("empty match query for a nonempty formula")
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// exercise the unused-in-prod helper so it is covered.
var _ = reflect.DeepEqual
var _ = sortedUnique
