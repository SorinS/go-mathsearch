// Package feature turns a go-canonical canonical form into structural feature
// tokens for fuzzy search.
//
// The canonical string (from canonical.Canonical) is already normalized for
// associative/commutative reordering, alpha-renaming, and free-variable
// renaming, so features drawn from it inherit those invariances. Tokens capture
// operator presence, parent-child edges, short paths, generalized leaves, and
// subtree fingerprints (shared subexpressions).
package feature

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
)

// Node is a parsed canonical term: an operator with children, or a leaf whose
// token is one of #<rat>, v<n>, @<n>, or $<name> (a $-name may be a quoted
// string like $"FromAbove").
type Node struct {
	Op   string // "" for a leaf
	Kids []*Node
	Leaf string // leaf token, "" for an operator
	raw  string // source substring, for subtree fingerprints
}

// Parse reads a canonical string into a term tree. It is quote-aware, so string
// atoms such as $"f[x,y]" that contain commas or brackets are read correctly.
func Parse(s string) (*Node, error) {
	r := &reader{s: []rune(s)}
	n, err := r.node()
	if err != nil {
		return nil, err
	}
	r.skip()
	if r.pos != len(r.s) {
		return nil, errAt(r.pos, "trailing input")
	}
	return n, nil
}

type reader struct {
	s   []rune
	pos int
}

type parseErr struct {
	pos int
	msg string
}

func (e *parseErr) Error() string {
	return "canonical parse error at " + strconv.Itoa(e.pos) + ": " + e.msg
}
func errAt(pos int, msg string) error {
	return &parseErr{pos, msg}
}

func (r *reader) skip() {
	for r.pos < len(r.s) && r.s[r.pos] == ' ' {
		r.pos++
	}
}

func (r *reader) peek() rune {
	if r.pos < len(r.s) {
		return r.s[r.pos]
	}
	return 0
}

func isNameStart(c rune) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func (r *reader) node() (*Node, error) {
	r.skip()
	start := r.pos
	c := r.peek()
	if isNameStart(c) {
		name := r.readName()
		if r.peek() == '[' {
			r.pos++ // '['
			var kids []*Node
			r.skip()
			if r.peek() != ']' {
				for {
					k, err := r.node()
					if err != nil {
						return nil, err
					}
					kids = append(kids, k)
					r.skip()
					if r.peek() == ',' {
						r.pos++
						continue
					}
					break
				}
			}
			r.skip()
			if r.peek() != ']' {
				return nil, errAt(r.pos, "expected ']'")
			}
			r.pos++ // ']'
			return &Node{Op: name, Kids: kids, raw: string(r.s[start:r.pos])}, nil
		}
		// A bare name with no bracket is treated as a leaf.
		return &Node{Leaf: name, raw: name}, nil
	}
	// A leaf atom: # v @ $.
	leaf := r.readLeaf()
	if leaf == "" {
		return nil, errAt(r.pos, "expected a term")
	}
	return &Node{Leaf: leaf, raw: leaf}, nil
}

func (r *reader) readName() string {
	start := r.pos
	for r.pos < len(r.s) {
		c := r.s[r.pos]
		if isNameStart(c) || c >= '0' && c <= '9' {
			r.pos++
		} else {
			break
		}
	}
	return string(r.s[start:r.pos])
}

func (r *reader) readLeaf() string {
	start := r.pos
	// A string constant $"..." may contain delimiters; consume the quotes.
	if r.peek() == '$' && r.pos+1 < len(r.s) && r.s[r.pos+1] == '"' {
		r.pos += 2
		for r.pos < len(r.s) && r.s[r.pos] != '"' {
			r.pos++
		}
		if r.pos < len(r.s) {
			r.pos++ // closing quote
		}
		return string(r.s[start:r.pos])
	}
	for r.pos < len(r.s) {
		c := r.s[r.pos]
		if c == ',' || c == ']' || c == '[' || c == ' ' {
			break
		}
		r.pos++
	}
	return string(r.s[start:r.pos])
}

// Tokens returns the multiset of structural feature tokens for the term. The
// same token may appear more than once (its frequency is signal for BM25).
func Tokens(root *Node) []string {
	var toks []string
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Op == "" {
			toks = append(toks, "l:"+generalize(n.Leaf))
			return
		}
		toks = append(toks, "op:"+n.Op)
		for _, k := range n.Kids {
			toks = append(toks, "e:"+n.Op+">"+headLabel(k))
			for _, g := range k.Kids {
				toks = append(toks, "p:"+n.Op+">"+k.Op+">"+headLabel(g))
			}
		}
		// Subtree fingerprint: identical subexpressions share this token.
		toks = append(toks, "s:"+fingerprint(n.raw))
		for _, k := range n.Kids {
			walk(k)
		}
	}
	walk(root)
	return toks
}

// TokensFrom parses a canonical string and returns its feature tokens; a parse
// failure yields no tokens rather than an error (fuzzy features are best-effort).
func TokensFrom(canonical string) []string {
	n, err := Parse(canonical)
	if err != nil {
		return nil
	}
	return Tokens(n)
}

func headLabel(n *Node) string {
	if n.Op != "" {
		return n.Op
	}
	return generalize(n.Leaf)
}

// generalize maps a leaf token to a fuzzier class: numbers and De Bruijn slots
// and free variables lose their exact index, while named constants keep their
// identity (Pi differs from E) and strings collapse to one class.
func generalize(leaf string) string {
	if leaf == "" {
		return "?"
	}
	switch leaf[0] {
	case '#':
		return "#num"
	case 'v':
		return "var"
	case '@':
		return "slot"
	case '$':
		if len(leaf) > 1 && leaf[1] == '"' {
			return "$str"
		}
		return leaf // e.g. $Pi, $Infinity
	default:
		return leaf
	}
}

// opFacet maps an operator name to a coarse facet tag for progressive
// filtering. Unlisted operators contribute no facet (but still appear in
// fuzzy features).
var opFacet = map[string]string{
	"Sum": "sum", "Product": "product",
	"Exp": "exponential", "Log": "logarithm",
	"Sin": "trigonometric", "Cos": "trigonometric", "Tan": "trigonometric",
	"Cot": "trigonometric", "Sec": "trigonometric", "Csc": "trigonometric",
	"ArcSin": "inverse-trigonometric", "ArcCos": "inverse-trigonometric",
	"ArcTan": "inverse-trigonometric", "ArcCot": "inverse-trigonometric",
	"Sinh": "hyperbolic", "Cosh": "hyperbolic", "Tanh": "hyperbolic",
	"Coth": "hyperbolic", "Sech": "hyperbolic", "Csch": "hyperbolic",
	"ArcSinh": "inverse-hyperbolic", "ArcCosh": "inverse-hyperbolic", "ArcTanh": "inverse-hyperbolic",
	"Abs": "absolute-value", "Sign": "sign", "Factorial": "factorial", "Binomial": "binomial",
	"Floor": "integer-part", "Ceiling": "integer-part", "Round": "integer-part",
	"Gamma": "gamma-function", "LogGamma": "gamma-function", "PolyGamma": "gamma-function",
	"Beta": "gamma-function", "Pochhammer": "gamma-function",
	"Erf": "error-function", "Erfc": "error-function", "Erfi": "error-function",
	"BesselJ": "bessel", "BesselY": "bessel", "BesselI": "bessel", "BesselK": "bessel",
	"HankelH1": "bessel", "HankelH2": "bessel",
	"AiryAi": "airy", "AiryBi": "airy", "StruveH": "struve", "StruveL": "struve",
	"EllipticF": "elliptic", "EllipticE": "elliptic", "EllipticK": "elliptic",
	"EllipticPi": "elliptic", "EllipticTheta": "elliptic", "EllipticNomeQ": "elliptic",
	"InverseEllipticNomeQ": "elliptic", "JacobiSD": "elliptic", "JacobiCN": "elliptic",
	"JacobiSN": "elliptic", "JacobiDN": "elliptic", "WeierstrassP": "elliptic",
	"Hypergeometric2F1": "hypergeometric", "HypergeometricPFQ": "hypergeometric",
	"Hypergeometric1F1": "hypergeometric", "HypergeometricU": "hypergeometric",
	"MeijerG": "meijer-g",
	"PolyLog": "polylogarithm", "LerchPhi": "polylogarithm",
	"Zeta": "zeta", "HurwitzZeta": "zeta", "DirichletEta": "zeta",
	"LegendreP": "orthogonal-polynomial", "LegendreQ": "orthogonal-polynomial",
	"HermiteH": "orthogonal-polynomial", "LaguerreL": "orthogonal-polynomial",
	"ChebyshevT": "orthogonal-polynomial", "ChebyshevU": "orthogonal-polynomial",
	"JacobiP": "orthogonal-polynomial", "GegenbauerC": "orthogonal-polynomial",
	"SinIntegral": "integral-function", "CosIntegral": "integral-function",
	"ExpIntegralEi": "integral-function", "ExpIntegralE": "integral-function",
	"LogIntegral": "integral-function", "FresnelC": "integral-function", "FresnelS": "integral-function",
}

// Facets returns the set of structural facet tags describing a term, for
// progressive ("is an integral, has a square root, has a Bessel function, ...")
// filtering. Derived from the same canonical tree as the fuzzy features.
func Facets(root *Node) []string {
	set := map[string]bool{}
	var walk func(n *Node)
	walk = func(n *Node) {
		if n.Op == "" {
			switch n.Leaf {
			case "$Pi":
				set["has-pi"] = true
			case "$E":
				set["has-e"] = true
			case "$I":
				set["imaginary-unit"] = true
			case "$EulerGamma":
				set["euler-gamma"] = true
			case "$Infinity":
				set["infinite-limit"] = true
			}
			return
		}
		if tag, ok := opFacet[n.Op]; ok {
			set[tag] = true
		}
		switch n.Op {
		case "Integrate":
			set["integral"] = true
			if len(n.Kids) >= 3 {
				set["definite-integral"] = true
			} else {
				set["indefinite-integral"] = true
			}
		case "Construct":
			// Derivative[n][f] arrives as Construct[Construct[Derivative,..],..].
			if len(n.Kids) > 0 && n.Kids[0].Op == "Derivative" {
				set["derivative"] = true
			}
			if len(n.Kids) > 0 && n.Kids[0].Op == "Construct" &&
				len(n.Kids[0].Kids) > 0 && n.Kids[0].Kids[0].Op == "Derivative" {
				set["derivative"] = true
			}
		case "Power":
			if len(n.Kids) == 2 && n.Kids[1].Op == "" {
				if num, den, ok := parseRat(n.Kids[1].Leaf); ok {
					if num < 0 {
						set["fraction"] = true
					}
					switch {
					case den == 2 && (num == 1 || num == -1):
						set["square-root"] = true
					case den > 1:
						set["nth-root"] = true
					}
				}
			}
		}
		for _, k := range n.Kids {
			walk(k)
		}
	}
	walk(root)
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// FacetsFrom parses a canonical string and returns its facet tags.
func FacetsFrom(canonical string) []string {
	n, err := Parse(canonical)
	if err != nil {
		return nil
	}
	return Facets(n)
}

// parseRat parses a leaf like "#-1/2" or "#3" into numerator and denominator.
func parseRat(leaf string) (num, den int64, ok bool) {
	if leaf == "" || leaf[0] != '#' {
		return 0, 0, false
	}
	body := leaf[1:]
	den = 1
	if i := strings.IndexByte(body, '/'); i >= 0 {
		d, err := strconv.ParseInt(body[i+1:], 10, 64)
		if err != nil || d == 0 {
			return 0, 0, false
		}
		den = d
		body = body[:i]
	}
	n, err := strconv.ParseInt(body, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return n, den, true
}

func fingerprint(raw string) string {
	h := fnv.New64a()
	h.Write([]byte(raw))
	return strconv.FormatUint(h.Sum64(), 36)
}

// Document encodes feature tokens as a whitespace-separated string of
// alphanumeric FTS5 tokens (each feature hashed so FTS5's default tokenizer
// keeps it whole).
func Document(tokens []string) string {
	var b strings.Builder
	for i, t := range tokens {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(hashToken(t))
	}
	return b.String()
}

// MatchQuery builds an FTS5 MATCH expression (OR of the distinct hashed tokens).
func MatchQuery(tokens []string) string {
	seen := map[string]bool{}
	var parts []string
	for _, t := range tokens {
		h := hashToken(t)
		if !seen[h] {
			seen[h] = true
			parts = append(parts, h)
		}
	}
	return strings.Join(parts, " OR ")
}

func hashToken(t string) string {
	h := fnv.New64a()
	h.Write([]byte(t))
	return "t" + strconv.FormatUint(h.Sum64(), 36)
}

// Jaccard is the set similarity of two token multisets (deduplicated), in [0,1].
func Jaccard(a, b []string) float64 {
	sa, sb := toSet(a), toSet(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	inter := 0
	for t := range sa {
		if sb[t] {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func toSet(a []string) map[string]bool {
	m := make(map[string]bool, len(a))
	for _, t := range a {
		m[t] = true
	}
	return m
}

// sortedUnique is a small helper for deterministic token listings (tests).
func sortedUnique(a []string) []string {
	m := toSet(a)
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
