package sqlglot

import (
	"math/big"
	"strconv"
	"strings"
)

// decimalPrecision is Python decimal's own default context precision --
// significant digits, not decimal places -- which is why `1 / 3.0` comes out
// to 28 threes and `20.0 / 6` to one digit before the point and 27 after.
const decimalPrecision = 28

// maxDecimalDigits bounds how large a folded result this port will write in
// plain decimal notation. The reference's own Decimal has no such limit and
// falls back to scientific notation past it -- `1E70 * 1E300` becomes
// `1E+370` there -- which this port does not write. Declining past this
// bound costs nothing the reference's own contract needs: every result in
// it is far smaller.
const maxDecimalDigits = 40

// maxDecimalExponent bounds the exponent a literal's OWN text may carry,
// checked before any big.Int operation touches it. A compact literal can
// name an enormous magnitude -- `1E70070010` is eleven characters -- and
// materialising that many digits, just to decline the result for being past
// maxDecimalDigits, is the expensive step. Every exponent this port's own
// contract ever needs is under 400 (`1E308`, the widest of them); the fuzzer
// is what finds the rest, the same way it found the chain of stars that
// once took a parse three seconds of garbage collection to refuse.
const maxDecimalExponent = 400

// bigDecimal is a SQL numeric literal the way the reference reads one:
// Python's `Decimal(text)`, not a binary float. `0.06` has no exact float64
// representation at all, so folding `0.06 + 0.01` through one answers a
// question about a different number than the one written; unscaled/scale
// keeps the value exact through Add, Sub and Mul, and Div rounds only where
// the reference's own division would.
type bigDecimal struct {
	unscaled *big.Int // may be negative
	scale    int      // value = unscaled * 10^-scale, scale >= 0
}

var pow10Cache = map[int]*big.Int{}

// pow10 memoises 10^n for the small, repeatedly-asked exponents scale
// alignment and rounding need -- never asked for more than a few dozen,
// across a call this deep in the fold loop.
func pow10(n int) *big.Int {
	if v, ok := pow10Cache[n]; ok {
		return v
	}
	v := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	pow10Cache[n] = v
	return v
}

// parseBigDecimal reads a literal's own text -- digits, an optional point,
// an optional exponent -- into unscaled/scale, the same value `Decimal(text)`
// would hold. It is not a general number parser: the text has already been
// through the tokenizer's own NUMBER rule, so it is trusted to be shaped
// like one.
func parseBigDecimal(text string) (*bigDecimal, bool) {
	s := text
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg = true
		s = s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return nil, false
		}
		if e > maxDecimalExponent || e < -maxDecimalExponent {
			return nil, false
		}
		exp = e
		s = s[:i]
	}
	intPart := s
	fracPart := ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart = s[:i]
		fracPart = s[i+1:]
	}
	if intPart == "" && fracPart == "" {
		return nil, false
	}
	digits := intPart + fracPart
	if digits == "" {
		digits = "0"
	}
	unscaled, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, false
	}
	scale := len(fracPart) - exp
	if neg {
		unscaled.Neg(unscaled)
	}
	// A negative scale means the exponent pushed the point past the last
	// digit -- `5E2` is 500, not 5 with a scale to match -- and the type
	// keeps no negative scale, so the zeros are made literal instead.
	if scale < 0 {
		unscaled = new(big.Int).Mul(unscaled, pow10(-scale))
		scale = 0
	}
	return &bigDecimal{unscaled: unscaled, scale: scale}, true
}

// decimalOf reads an operand's value the way parseBigDecimal does, seeing
// through a Neg the same way numberOf does for its own float64 operands.
func decimalOf(e *Expression) (*bigDecimal, bool) {
	if e.Class == "Neg" {
		d, ok := decimalOf(childOf(e, "this"))
		if !ok {
			return nil, false
		}
		return &bigDecimal{unscaled: new(big.Int).Neg(d.unscaled), scale: d.scale}, true
	}
	text, _ := e.Args["this"].(string)
	return parseBigDecimal(text)
}

// alignedWith scales both operands to a shared scale -- the larger of the
// two -- so their unscaled integers can be added or subtracted directly.
func (a *bigDecimal) alignedWith(b *bigDecimal) (*big.Int, *big.Int, int) {
	scale := a.scale
	if b.scale > scale {
		scale = b.scale
	}
	au := new(big.Int).Mul(a.unscaled, pow10(scale-a.scale))
	bu := new(big.Int).Mul(b.unscaled, pow10(scale-b.scale))
	return au, bu, scale
}

// Add is exact: aligning to a shared scale and adding the unscaled integers
// loses nothing, the way binary floating point would.
func (a *bigDecimal) Add(b *bigDecimal) *bigDecimal {
	au, bu, scale := a.alignedWith(b)
	return &bigDecimal{unscaled: new(big.Int).Add(au, bu), scale: scale}
}

// Sub is Add's mirror, equally exact.
func (a *bigDecimal) Sub(b *bigDecimal) *bigDecimal {
	au, bu, scale := a.alignedWith(b)
	return &bigDecimal{unscaled: new(big.Int).Sub(au, bu), scale: scale}
}

// Mul is exact too: the unscaled integers multiply directly and the scales
// add, with no rounding step at all.
func (a *bigDecimal) Mul(b *bigDecimal) *bigDecimal {
	return &bigDecimal{unscaled: new(big.Int).Mul(a.unscaled, b.unscaled), scale: a.scale + b.scale}
}

// Div is the one operation that can fail to terminate -- `1 / 3` has no
// exact decimal answer -- so it rounds to `prec` significant digits the way
// the reference's own division does: half rounds to EVEN, and a nonzero
// remainder past the rounding point always breaks a tie upward, because the
// true value is then strictly more than halfway.
func (a *bigDecimal) Div(b *bigDecimal, prec int) (*bigDecimal, bool) {
	if b.unscaled.Sign() == 0 {
		return nil, false
	}
	sign := 1
	if (a.unscaled.Sign() < 0) != (b.unscaled.Sign() < 0) {
		sign = -1
	}
	au := new(big.Int).Abs(a.unscaled)
	bu := new(big.Int).Abs(b.unscaled)
	baseExp := b.scale - a.scale // a/b = (au/bu) * 10^baseExp

	if au.Sign() == 0 {
		return &bigDecimal{unscaled: big.NewInt(0), scale: 0}, true
	}

	// Scale the numerator up until the integer quotient has more digits
	// than the precision calls for -- either it terminates exactly first,
	// or there are enough digits to round the last one correctly.
	shift := 0
	for {
		scaled := new(big.Int).Mul(au, pow10(shift))
		q, r := new(big.Int).QuoRem(scaled, bu, new(big.Int))
		qlen := len(q.String())
		if r.Sign() == 0 {
			return normalizeDecimal(q, -shift+baseExp, sign), true
		}
		if qlen > prec {
			d := qlen - prec
			rounded := roundHalfEven(q, d, r.Sign() != 0)
			return normalizeDecimal(rounded, d-shift+baseExp, sign), true
		}
		shift++
	}
}

// roundHalfEven drops the last d digits of q, rounding to the nearest value
// and to EVEN on an exact tie. inexact marks a remainder beyond what q
// itself carries -- the true value is then strictly past the halfway point,
// which always rounds up regardless of which way "to even" would go.
func roundHalfEven(q *big.Int, d int, inexact bool) *big.Int {
	if d <= 0 {
		return new(big.Int).Set(q)
	}
	divisor := pow10(d)
	half := new(big.Int).Quo(divisor, big.NewInt(2))
	q2, rem := new(big.Int).QuoRem(q, divisor, new(big.Int))
	switch cmp := rem.CmpAbs(half); {
	case cmp < 0:
		return q2
	case cmp > 0:
		return q2.Add(q2, big.NewInt(1))
	default:
		if inexact || new(big.Int).Mod(q2, big.NewInt(2)).Sign() != 0 {
			return q2.Add(q2, big.NewInt(1))
		}
		return q2
	}
}

// normalizeDecimal builds a bigDecimal from digits*10^exponent, applying the
// sign and folding a positive exponent back into the unscaled integer so
// scale never goes negative.
func normalizeDecimal(digits *big.Int, exponent int, sign int) *bigDecimal {
	unscaled := new(big.Int).Set(digits)
	scale := -exponent
	if scale < 0 {
		unscaled = new(big.Int).Mul(unscaled, pow10(-scale))
		scale = 0
	}
	if sign < 0 && unscaled.Sign() != 0 {
		unscaled.Neg(unscaled)
	}
	return &bigDecimal{unscaled: unscaled, scale: scale}
}

// digitCount is how many digits the unscaled integer has, sign aside --
// what maxDecimalDigits is measured against.
func (d *bigDecimal) digitCount() int {
	if d.unscaled.Sign() == 0 {
		return 1
	}
	return len(new(big.Int).Abs(d.unscaled).String())
}

// String writes the value in plain notation -- the point placed `scale`
// digits from the right, padded with leading zeros where the whole number
// has fewer digits than that. Every result this port folds is small enough
// that the reference would write it this way too; past maxDecimalDigits,
// where that might stop being true, the fold declines instead.
func (d *bigDecimal) String() string {
	neg := d.unscaled.Sign() < 0
	digits := new(big.Int).Abs(d.unscaled).String()
	if d.scale == 0 {
		if neg {
			return "-" + digits
		}
		return digits
	}
	for len(digits) <= d.scale {
		digits = "0" + digits
	}
	out := digits[:len(digits)-d.scale] + "." + digits[len(digits)-d.scale:]
	if neg {
		out = "-" + out
	}
	return out
}
