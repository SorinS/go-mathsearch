package latexfmt

import (
	"testing"

	canonical "sorins/canonical"
)

func tex(t *testing.T, mma string) string {
	t.Helper()
	e, err := canonical.Parse(mma)
	if err != nil {
		t.Fatalf("parse %q: %v", mma, err)
	}
	return LaTeX(e)
}

func TestLaTeX(t *testing.T) {
	cases := []struct{ mma, want string }{
		{"a + b", `a + b`},
		{"a - b", `a - b`},
		{"a*b", `a b`},
		{"a/b", `\frac{a}{b}`},
		{"x^2", `x^{2}`},
		{"Sqrt[x]", `\sqrt{x}`},
		{"x^(1/2)", `x^{\frac{1}{2}}`}, // 1/2 is division (parser rule), not Sqrt
		{"x^Rational[1,2]", `\sqrt{x}`},
		{"(q/p)^n", `\left(\frac{q}{p}\right)^{n}`},
		{"E^x", `e^{x}`},
		{"Exp[x]", `e^{x}`},
		{"Sin[x]", `\sin\!\left(x\right)`},
		{"n!", `n!`},
		{"Pi", `\pi`},
		{"Infinity", `\infty`},
		{"-Infinity", `-\infty`},
		{"tau", `\tau`},
		{"Integrate[f[x], {x, 0, t}]", `\int_{0}^{t} \operatorname{f}\!\left(x\right)\,dx`},
		{"Integrate[gg, x]", `\int \mathrm{gg}\,dx`},
		{"Sum[1/k^2, {k, 1, Infinity}]", `\sum_{k=1}^{\infty} \frac{1}{k^{2}}`},
		{"BesselJ[n, x]", `J_{n}\!\left(x\right)`},
		{"Abs[x]", `\left|x\right|`},
		{"Subscript[a, n]", `a_{n}`},
	}
	for _, c := range cases {
		if got := tex(t, c.mma); got != c.want {
			t.Errorf("LaTeX(%q) =\n  %s\nwant\n  %s", c.mma, got, c.want)
		}
	}
}

func TestIdentity(t *testing.T) {
	lhs, _ := canonical.Parse("Integrate[Sin[x]/x, {x, 0, Infinity}]")
	rhs, _ := canonical.Parse("Pi/2")
	got := Identity(lhs, rhs)
	want := `\int_{0}^{\infty} \frac{\sin\!\left(x\right)}{x}\,dx = \frac{\pi}{2}`
	if got != want {
		t.Errorf("Identity =\n  %s\nwant\n  %s", got, want)
	}
	// One-sided still renders.
	if Identity(nil, rhs) != `\frac{\pi}{2}` {
		t.Errorf("one-sided identity = %s", Identity(nil, rhs))
	}
}
