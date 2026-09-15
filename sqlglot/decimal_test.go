package sqlglot

import (
	"math/big"
	"testing"
)

// TestBigDecimalArithmetic pins bigDecimal's four operations against Python's
// own `decimal.Decimal` at its default context (28 significant digits,
// ROUND_HALF_EVEN) -- the values here were read out of that module directly,
// not hand-computed.
func TestBigDecimalArithmetic(t *testing.T) {
	for _, tc := range []struct{ a, b, op, want string }{
		{"0.06", "0.01", "+", "0.07"},
		{"0.06", "0.01", "-", "0.05"},
		{"0.06", "1", "+", "1.06"},
		{"3.0", "9", "*", "27.0"},
		{"0.03", "0.73", "*", "0.0219"},
		{"1", "3.0", "/", "0.3333333333333333333333333333"},
		{"20.0", "6", "/", "3.333333333333333333333333333"},
		// Exact division terminates before precision is ever reached.
		{"1", "8", "/", "0.125"},
		{"5", "2", "/", "2.5"},
		{"25", "100", "/", "0.25"},
		// Scientific notation in the source text normalises the same way a
		// plain decimal would.
		{"1.2E+1", "15E-3", "+", "12.015"},
		{"5E2", "1", "+", "501"},
		// Negative operands.
		{"-0.06", "0.01", "+", "-0.05"},
		{"0.06", "-0.01", "-", "0.07"},
		{"-3.0", "-9", "*", "27.0"},
		{"-1", "3.0", "/", "-0.3333333333333333333333333333"},
		// A leading "+" on the text itself (as opposed to on an exponent,
		// already covered above) and a plain negative integer -- scale 0,
		// no decimal point at all -- both take a corner String() does not
		// share with the fractional case.
		{"+5", "3", "-", "2"},
		{"-5", "0", "+", "-5"},
	} {
		t.Run(tc.a+tc.op+tc.b, func(t *testing.T) {
			a, ok := parseBigDecimal(tc.a)
			if !ok {
				t.Fatalf("parseBigDecimal(%q) failed", tc.a)
			}
			b, ok := parseBigDecimal(tc.b)
			if !ok {
				t.Fatalf("parseBigDecimal(%q) failed", tc.b)
			}
			var got string
			switch tc.op {
			case "+":
				got = a.Add(b).String()
			case "-":
				got = a.Sub(b).String()
			case "*":
				got = a.Mul(b).String()
			case "/":
				r, ok := a.Div(b, decimalPrecision)
				if !ok {
					t.Fatalf("Div(%q, %q) refused", tc.a, tc.b)
				}
				got = r.String()
			}
			if got != tc.want {
				t.Errorf("%s %s %s = %s, want %s", tc.a, tc.op, tc.b, got, tc.want)
			}
		})
	}
}

// TestBigDecimalDivByZero covers the one way Div can fail outright.
func TestBigDecimalDivByZero(t *testing.T) {
	a, _ := parseBigDecimal("1")
	b, _ := parseBigDecimal("0")
	if _, ok := a.Div(b, decimalPrecision); ok {
		t.Error("Div by zero was not refused")
	}
	// Zero divided by anything nonzero is exactly zero, no rounding needed.
	zero, _ := parseBigDecimal("0")
	five, _ := parseBigDecimal("5")
	r, ok := zero.Div(five, decimalPrecision)
	if !ok || r.String() != "0" {
		t.Errorf("0 / 5 = %v (ok=%v), want 0", r, ok)
	}
}

// TestParseBigDecimalRejects covers text the tokenizer would never actually
// produce, so parseBigDecimal's own refusal is exercised directly rather
// than left as a branch nothing calls.
func TestParseBigDecimalRejects(t *testing.T) {
	for _, text := range []string{"", ".", "1.2.3", "abc", "1E"} {
		if _, ok := parseBigDecimal(text); ok {
			t.Errorf("parseBigDecimal(%q) should have been refused", text)
		}
	}
}

// TestParseBigDecimalBoundsTheExponent is the fix for a crash the fuzzer
// found: a compact literal can name an enormous magnitude -- `1E70070010`
// is eleven characters -- and turning that into an actual big.Int, just to
// decline the fold a moment later for being past maxDecimalDigits, took
// the reported case from milliseconds to over ten seconds. The bound is
// checked before any big.Int is built at all.
func TestParseBigDecimalBoundsTheExponent(t *testing.T) {
	for _, text := range []string{"1E70070010", "1E-70070010"} {
		if _, ok := parseBigDecimal(text); ok {
			t.Errorf("parseBigDecimal(%q) should have been refused as too large an exponent", text)
		}
	}
	// The widest exponent this port's own contract actually needs is well
	// inside the bound, and still parses.
	if _, ok := parseBigDecimal("1E308"); !ok {
		t.Error("parseBigDecimal(\"1E308\") was refused; it is within the bound")
	}
}

// TestDecimalOfThroughNeg covers decimalOf seeing through a Neg the same way
// numberOf does for its own float64 operands.
func TestDecimalOfThroughNeg(t *testing.T) {
	neg := New("Neg", Arg{"this", New("Literal", Arg{"this", "0.06"}, Arg{"is_string", false})})
	d, ok := decimalOf(neg)
	if !ok {
		t.Fatal("decimalOf(Neg(0.06)) refused")
	}
	if got := d.String(); got != "-0.06" {
		t.Errorf("decimalOf(Neg(0.06)) = %s, want -0.06", got)
	}
	// And a Neg wrapping something that is not a number at all still
	// refuses cleanly rather than panicking.
	negOfColumn := New("Neg", Arg{"this", New("Column", Arg{"this", New("Identifier", Arg{"this", "x"}, Arg{"quoted", false})})})
	if _, ok := decimalOf(negOfColumn); ok {
		t.Error("decimalOf(Neg(column)) should have been refused")
	}
}

// TestDigitCountAndMaxDecimalDigits covers the guard foldDecimalArithmetic
// uses to decline a result too large for this port's plain-notation writer,
// including the zero case digitCount special-cases.
func TestDigitCountAndMaxDecimalDigits(t *testing.T) {
	zero := &bigDecimal{unscaled: big.NewInt(0), scale: 0}
	if got := zero.digitCount(); got != 1 {
		t.Errorf("digitCount(0) = %d, want 1", got)
	}
	hundred, _ := parseBigDecimal("100")
	if got := hundred.digitCount(); got != 3 {
		t.Errorf("digitCount(100) = %d, want 3", got)
	}
	negHundred, _ := parseBigDecimal("-100")
	if got := negHundred.digitCount(); got != 3 {
		t.Errorf("digitCount(-100) = %d (sign should not count), want 3", got)
	}
}

// TestFoldDecimalArithmeticDeclinesPastTheDigitLimit is the same property
// TestArithmeticThatOverflows checks end to end, pinned directly at the
// function that owns the decision: a result the reference itself would
// write in scientific notation is left unfolded rather than spelled out in
// forty-odd plain digits.
func TestFoldDecimalArithmeticDeclinesPastTheDigitLimit(t *testing.T) {
	huge := New("Literal", Arg{"this", "1E70"}, Arg{"is_string", false})
	huge2 := New("Literal", Arg{"this", "1E300"}, Arg{"is_string", false})
	if out := foldDecimalArithmetic("Mul", huge, huge2); out != nil {
		t.Errorf("foldDecimalArithmetic(1E70 * 1E300) = %v, want nil (declined)", out)
	}
	// And a result well within the limit still folds.
	small := New("Literal", Arg{"this", "0.06"}, Arg{"is_string", false})
	smallToo := New("Literal", Arg{"this", "0.01"}, Arg{"is_string", false})
	out := foldDecimalArithmetic("Add", small, smallToo)
	if out == nil {
		t.Fatal("foldDecimalArithmetic(0.06 + 0.01) declined; want 0.07")
	}
	got, err := Generate(out, "")
	if err != nil || got != "0.07" {
		t.Errorf("foldDecimalArithmetic(0.06 + 0.01) wrote %q (err=%v), want 0.07", got, err)
	}
}

// TestDecimalLitNegative covers decimalLit's own Neg-wrapping, the mirror of
// numberLit's: a negative result is Neg(Literal(positive text)), never a
// literal whose text begins with a minus sign.
func TestDecimalLitNegative(t *testing.T) {
	neg, _ := parseBigDecimal("-0.5")
	lit := decimalLit(neg)
	if lit.Class != "Neg" {
		t.Fatalf("decimalLit(-0.5).Class = %s, want Neg", lit.Class)
	}
	inner, _ := lit.Args["this"].(*Expression)
	if inner == nil || inner.Name() != "0.5" {
		t.Errorf("decimalLit(-0.5) wraps %v, want Literal(0.5)", inner)
	}
	// A whole-number decimal result still carries a point: it came from a
	// fractional literal, and the reference marks it as one for that reason
	// alone, the same way numberLit does for a float result.
	whole, _ := parseBigDecimal("5E2") // parses to unscaled=500, scale=0
	wholeLit := decimalLit(whole)
	if wholeLit.Name() != "500.0" {
		t.Errorf("decimalLit(500) = %q, want \"500.0\"", wholeLit.Name())
	}
}

// TestRoundHalfEven pins the rounding rule Div uses once a division does not
// terminate within precision: nearest, ties to EVEN, and a nonzero remainder
// beyond the dropped digits always breaks a tie upward, because the true
// value is then strictly past the halfway point rather than sitting on it.
func TestRoundHalfEven(t *testing.T) {
	for _, tc := range []struct {
		q       int64
		d       int
		inexact bool
		want    int64
	}{
		{124, 1, false, 12},  // drop "4": rounds down
		{126, 1, false, 13},  // drop "6": rounds up
		{125, 1, false, 12},  // exact tie: 12 is even, stays
		{135, 1, false, 14},  // exact tie: 13 is odd, rounds up to even 14
		{125, 1, true, 13},   // an inexact tail breaks the tie upward regardless of parity
		{1200, 2, false, 12}, // dropping two digits at once, no tie
	} {
		got := roundHalfEven(big.NewInt(tc.q), tc.d, tc.inexact).Int64()
		if got != tc.want {
			t.Errorf("roundHalfEven(%d, drop %d, inexact=%v) = %d, want %d",
				tc.q, tc.d, tc.inexact, got, tc.want)
		}
	}
	// d <= 0 drops nothing at all.
	if got := roundHalfEven(big.NewInt(123), 0, false).Int64(); got != 123 {
		t.Errorf("roundHalfEven(123, drop 0) = %d, want 123", got)
	}
}
