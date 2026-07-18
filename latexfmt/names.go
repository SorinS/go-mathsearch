package latexfmt

import "strings"

var greek = map[string]string{
	"alpha": `\alpha`, "beta": `\beta`, "gamma": `\gamma`, "delta": `\delta`,
	"epsilon": `\epsilon`, "varepsilon": `\varepsilon`, "zeta": `\zeta`, "eta": `\eta`,
	"theta": `\theta`, "vartheta": `\vartheta`, "iota": `\iota`, "kappa": `\kappa`,
	"lambda": `\lambda`, "mu": `\mu`, "nu": `\nu`, "xi": `\xi`, "omicron": `o`,
	"pi": `\pi`, "varpi": `\varpi`, "rho": `\rho`, "sigma": `\sigma`, "tau": `\tau`,
	"upsilon": `\upsilon`, "phi": `\phi`, "varphi": `\varphi`, "chi": `\chi`,
	"psi": `\psi`, "omega": `\omega`,
}

var greekCapital = map[string]string{
	"gamma": `\Gamma`, "delta": `\Delta`, "theta": `\Theta`, "lambda": `\Lambda`,
	"xi": `\Xi`, "pi": `\Pi`, "sigma": `\Sigma`, "upsilon": `\Upsilon`,
	"phi": `\Phi`, "psi": `\Psi`, "omega": `\Omega`,
}

// symbol renders a variable name: Greek names become Greek letters, single
// letters pass through, named-character escapes (\[Lambda]) resolve, and
// anything else is set upright.
func symbol(name string) string {
	if name == "" {
		return ""
	}
	// Named-character escape: \[Lambda], \[CapitalOmega], ...
	if strings.HasPrefix(name, `\[`) && strings.HasSuffix(name, "]") {
		inner := name[2 : len(name)-1]
		if strings.HasPrefix(inner, "Capital") {
			if g, ok := greekCapital[strings.ToLower(inner[len("Capital"):])]; ok {
				return g
			}
		}
		if g, ok := greek[strings.ToLower(inner)]; ok {
			return g
		}
		return `\mathrm{` + inner + `}`
	}
	if g, ok := greek[name]; ok {
		return g
	}
	if len(name) == 1 {
		return name
	}
	// Multi-letter, non-Greek: upright to avoid looking like a product.
	return `\mathrm{` + name + `}`
}

// constName renders a symbolic constant.
func constName(name string) string {
	if strings.HasPrefix(name, `"`) { // a string literal atom
		return `\text{` + strings.Trim(name, `"`) + `}`
	}
	switch name {
	case "Pi":
		return `\pi`
	case "Infinity":
		return `\infty`
	case "ComplexInfinity":
		return `\tilde{\infty}`
	case "E":
		return `e`
	case "I":
		return `i`
	case "EulerGamma":
		return `\gamma`
	case "Degree":
		return `{}^{\circ}`
	case "GoldenRatio":
		return `\varphi`
	case "Catalan":
		return `G`
	case "Glaisher":
		return `A`
	case "Khinchin":
		return `K`
	default:
		return `\mathrm{` + name + `}`
	}
}

// funcName maps a function head to its LaTeX command when standard notation
// exists; the caller applies it to parenthesized arguments.
var funcNames = map[string]string{
	"Sin": `\sin`, "Cos": `\cos`, "Tan": `\tan`, "Cot": `\cot`, "Sec": `\sec`, "Csc": `\csc`,
	"Sinh": `\sinh`, "Cosh": `\cosh`, "Tanh": `\tanh`, "Coth": `\coth`,
	"ArcSin": `\arcsin`, "ArcCos": `\arccos`, "ArcTan": `\arctan`,
	"Log": `\ln`, "Gamma": `\Gamma`,
	"Sech": `\operatorname{sech}`, "Csch": `\operatorname{csch}`,
	"ArcCot": `\operatorname{arccot}`, "ArcSinh": `\operatorname{arcsinh}`,
	"ArcCosh": `\operatorname{arccosh}`, "ArcTanh": `\operatorname{arctanh}`,
	"Erf": `\operatorname{erf}`, "Erfc": `\operatorname{erfc}`, "Erfi": `\operatorname{erfi}`,
	"Sign": `\operatorname{sgn}`, "Sinc": `\operatorname{sinc}`,
}

func funcName(op string) (string, bool) {
	t, ok := funcNames[op]
	return t, ok
}

// subscriptFunc maps a function head whose first argument is a subscript order
// (Bessel, orthogonal polynomials, ...) to its base letter, so BesselJ[n,x]
// renders as J_{n}(x).
var subscriptFuncs = map[string]string{
	"BesselJ": "J", "BesselY": "Y", "BesselI": "I", "BesselK": "K",
	"LegendreP": "P", "LegendreQ": "Q", "HermiteH": "H", "LaguerreL": "L",
	"ChebyshevT": "T", "ChebyshevU": "U", "GegenbauerC": "C",
}

func subscriptFunc(op string) (string, bool) {
	t, ok := subscriptFuncs[op]
	return t, ok
}
