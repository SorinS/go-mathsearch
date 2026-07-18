// Package latexfmt renders a parsed Mathematica expression (a go-canonical
// *Expr, before normalization) as LaTeX. It is used to display the full
// identity for a corpus entry — both sides, always complete and correct — since
// the corpus's own LaTeX field is sometimes one-sided or OCR-damaged.
//
// The printer is precedence-aware for the core algebra and covers the common
// calculus, elementary, and special functions; anything unrecognized falls back
// to \operatorname{Name}(args), so output is always valid rather than pretty.
package latexfmt

import (
	"math/big"
	"strings"

	canonical "sorins/canonical"
)

// Precedence levels; higher binds tighter.
const (
	precRel   = 10
	precPlus  = 20
	precTimes = 40
	precPow   = 60
	precAtom  = 100
)

// LaTeX renders e as a LaTeX fragment (no surrounding math delimiters).
func LaTeX(e *canonical.Expr) string {
	s, _ := render(e)
	return s
}

// Identity renders "lhs = rhs" (or a single side if the other is nil).
func Identity(lhs, rhs *canonical.Expr) string {
	switch {
	case lhs != nil && rhs != nil:
		return LaTeX(lhs) + " = " + LaTeX(rhs)
	case lhs != nil:
		return LaTeX(lhs)
	case rhs != nil:
		return LaTeX(rhs)
	default:
		return ""
	}
}

// render returns the LaTeX for e and its precedence.
func render(e *canonical.Expr) (string, int) {
	switch e.Kind {
	case canonical.KConst:
		return renderRat(e.Val)
	case canonical.KFree:
		return symbol(e.Name), precAtom
	case canonical.KSym:
		return constName(e.Name), precAtom
	case canonical.KOp:
		return renderOp(e)
	default:
		return "", precAtom
	}
}

// wrap parenthesizes s if its precedence is below need.
func wrap(s string, prec, need int) string {
	if prec < need {
		return `\left(` + s + `\right)`
	}
	return s
}

func arg(e *canonical.Expr, need int) string {
	s, p := render(e)
	return wrap(s, p, need)
}

func renderOp(e *canonical.Expr) (string, int) {
	k := e.Kids
	switch e.Op {
	case "Plus":
		return renderPlus(k), precPlus
	case "Subtract":
		if len(k) == 2 {
			return arg(k[0], precPlus) + " - " + arg(k[1], precPlus+1), precPlus
		}
	case "Minus":
		if len(k) == 1 {
			return "-" + arg(k[0], precPlus+1), precPlus
		}
	case "Times":
		return renderTimes(k), precTimes
	case "Divide":
		if len(k) == 2 {
			return `\frac{` + LaTeX(k[0]) + `}{` + LaTeX(k[1]) + `}`, precAtom
		}
	case "Power":
		if len(k) == 2 {
			return renderPower(k[0], k[1])
		}
	case "Sqrt":
		if len(k) == 1 {
			return `\sqrt{` + LaTeX(k[0]) + `}`, precAtom
		}
	case "Exp":
		if len(k) == 1 {
			return `e^{` + LaTeX(k[0]) + `}`, precPow
		}
	case "Abs":
		if len(k) == 1 {
			return `\left|` + LaTeX(k[0]) + `\right|`, precAtom
		}
	case "Factorial":
		if len(k) == 1 {
			return arg(k[0], precPow) + "!", precPow
		}
	case "Integrate", "Sum", "Product":
		return renderBinder(e), precTimes
	case "Subscript":
		if len(k) == 2 {
			return arg(k[0], precAtom) + "_{" + LaTeX(k[1]) + "}", precPow
		}
	case "Binomial":
		if len(k) == 2 {
			return `\binom{` + LaTeX(k[0]) + `}{` + LaTeX(k[1]) + `}`, precAtom
		}
	case "Floor":
		if len(k) == 1 {
			return `\left\lfloor ` + LaTeX(k[0]) + ` \right\rfloor`, precAtom
		}
	case "Ceiling":
		if len(k) == 1 {
			return `\left\lceil ` + LaTeX(k[0]) + ` \right\rceil`, precAtom
		}
	case "Equal":
		return joinRel(k, " = "), precRel
	case "Unequal":
		return joinRel(k, `\neq `), precRel
	case "Less":
		return joinRel(k, " < "), precRel
	case "LessEqual":
		return joinRel(k, `\leq `), precRel
	case "Greater":
		return joinRel(k, " > "), precRel
	case "GreaterEqual":
		return joinRel(k, `\geq `), precRel
	case "Rule":
		if len(k) == 2 {
			return LaTeX(k[0]) + `\to ` + LaTeX(k[1]), precRel
		}
	case "Log":
		if len(k) == 2 { // Log[b, x] = log_b x
			return `\log_{` + LaTeX(k[0]) + `}` + arg(k[1], precPow), precPow
		}
	}
	// Named functions with standard notation.
	if tex, ok := funcName(e.Op); ok {
		return tex + `\!\left(` + joinArgs(k) + `\right)`, precPow
	}
	// Bessel-family and similar: X_{first}(rest).
	if sub, ok := subscriptFunc(e.Op); ok && len(k) >= 1 {
		return sub + "_{" + LaTeX(k[0]) + `}\!\left(` + joinArgs(k[1:]) + `\right)`, precPow
	}
	// Generic fallback: \operatorname{Name}(args).
	return `\operatorname{` + e.Op + `}\!\left(` + joinArgs(k) + `\right)`, precPow
}

func renderPlus(k []*canonical.Expr) string {
	var b strings.Builder
	for i, t := range k {
		s, sign := signedTerm(t)
		if i == 0 {
			if sign < 0 {
				b.WriteString("-")
			}
			b.WriteString(s)
			continue
		}
		if sign < 0 {
			b.WriteString(" - ")
		} else {
			b.WriteString(" + ")
		}
		b.WriteString(s)
	}
	return b.String()
}

// signedTerm renders a summand, returning its body (unsigned) and sign so Plus
// can join with + / -.
func signedTerm(e *canonical.Expr) (string, int) {
	switch {
	case e.Kind == canonical.KConst && e.Val.Sign() < 0:
		neg := new(big.Rat).Neg(e.Val)
		s, _ := renderRat(neg)
		return s, -1
	case e.Kind == canonical.KOp && e.Op == "Minus" && len(e.Kids) == 1:
		return arg(e.Kids[0], precPlus+1), -1
	}
	return arg(e, precPlus), 1
}

func renderTimes(k []*canonical.Expr) string {
	sign := 1
	var num, den []string
	for _, f := range k {
		// Pull any unary minus out as an overall sign.
		for f.Kind == canonical.KOp && f.Op == "Minus" && len(f.Kids) == 1 {
			sign = -sign
			f = f.Kids[0]
		}
		// Pull a negative constant's sign out (drop a bare -1).
		if f.Kind == canonical.KConst && f.Val.Sign() < 0 {
			sign = -sign
			if f.Val.Cmp(big.NewRat(-1, 1)) == 0 {
				continue
			}
			f = &canonical.Expr{Kind: canonical.KConst, Val: new(big.Rat).Neg(f.Val)}
		}
		// A reciprocal power goes to the denominator.
		if f.Kind == canonical.KOp && f.Op == "Power" && len(f.Kids) == 2 &&
			f.Kids[1].Kind == canonical.KConst && f.Kids[1].Val.Sign() < 0 {
			posExp := new(big.Rat).Neg(f.Kids[1].Val)
			if posExp.Cmp(big.NewRat(1, 1)) == 0 {
				den = append(den, arg(f.Kids[0], precPow))
			} else {
				s, _ := renderPower(f.Kids[0], &canonical.Expr{Kind: canonical.KConst, Val: posExp})
				den = append(den, s)
			}
			continue
		}
		num = append(num, arg(f, precTimes))
	}
	numStr := joinProduct(num)
	if numStr == "" {
		numStr = "1"
	}
	var out string
	if len(den) > 0 {
		out = `\frac{` + numStr + `}{` + joinProduct(den) + `}`
	} else {
		out = numStr
	}
	if sign < 0 {
		out = "-" + out
	}
	return out
}

// joinProduct concatenates factors, inserting \cdot only between two that would
// otherwise merge ambiguously (digit next to digit).
func joinProduct(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			prev := parts[i-1]
			if endsDigit(prev) && startsDigit(p) {
				b.WriteString(` \cdot `)
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteString(p)
	}
	return b.String()
}

func renderPower(base, exp *canonical.Expr) (string, int) {
	// Square roots and reciprocal square roots.
	if exp.Kind == canonical.KConst {
		if exp.Val.Cmp(big.NewRat(1, 2)) == 0 {
			return `\sqrt{` + LaTeX(base) + `}`, precAtom
		}
		if exp.Val.Cmp(big.NewRat(-1, 2)) == 0 {
			return `\frac{1}{\sqrt{` + LaTeX(base) + `}}`, precAtom
		}
		if exp.Val.Cmp(big.NewRat(-1, 1)) == 0 {
			return `\frac{1}{` + arg(base, precPow) + `}`, precAtom
		}
	}
	baseStr := arg(base, precPow)
	// A fraction as a power base needs explicit parentheses: (q/p)^n.
	if isFractionBase(base) {
		baseStr = `\left(` + LaTeX(base) + `\right)`
	}
	return baseStr + "^{" + LaTeX(exp) + "}", precPow
}

func isFractionBase(e *canonical.Expr) bool {
	if e.Kind == canonical.KConst {
		return !e.Val.IsInt()
	}
	return e.Kind == canonical.KOp && e.Op == "Divide"
}

func renderBinder(e *canonical.Expr) string {
	var op, dsym string
	switch e.Op {
	case "Integrate":
		op, dsym = `\int`, `\,d`
	case "Sum":
		op = `\sum`
	case "Product":
		op = `\prod`
	}
	v := symbol(e.Bind)
	k := e.Kids
	// Definite: [lower, upper, body]; indefinite: [body].
	if len(k) == 3 {
		lo, hi, body := LaTeX(k[0]), LaTeX(k[1]), arg(k[2], precTimes)
		if e.Op == "Integrate" {
			return op + `_{` + lo + `}^{` + hi + `} ` + body + dsym + v
		}
		return op + `_{` + v + `=` + lo + `}^{` + hi + `} ` + body
	}
	if len(k) == 1 {
		if e.Op == "Integrate" {
			return op + ` ` + arg(k[0], precTimes) + dsym + v
		}
		return op + `_{` + v + `} ` + arg(k[0], precTimes)
	}
	return op
}

func joinArgs(k []*canonical.Expr) string {
	parts := make([]string, len(k))
	for i, a := range k {
		parts[i] = LaTeX(a)
	}
	return strings.Join(parts, ", ")
}

func joinRel(k []*canonical.Expr, rel string) string {
	parts := make([]string, len(k))
	for i, a := range k {
		parts[i] = arg(a, precRel+1)
	}
	return strings.Join(parts, rel)
}

func renderRat(r *big.Rat) (string, int) {
	if r == nil {
		return "0", precAtom
	}
	if r.IsInt() {
		return r.Num().String(), precAtom
	}
	num := r.Num()
	sign := ""
	if num.Sign() < 0 {
		sign = "-"
		num = new(big.Int).Neg(num)
	}
	return sign + `\frac{` + num.String() + `}{` + r.Denom().String() + `}`, precAtom
}

func endsDigit(s string) bool {
	if s == "" {
		return false
	}
	c := s[len(s)-1]
	return c >= '0' && c <= '9'
}

func startsDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}
