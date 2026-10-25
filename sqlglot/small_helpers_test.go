package sqlglot

import "testing"

// A handful of the port's own helpers, held to the Python behaviour they
// mirror.
func TestTokenizerHelpers(t *testing.T) {
	// pyBound clamps an index the way a Python slice bound does.
	if got := pyBound(-1, 10); got != 9 {
		t.Errorf("pyBound(-1, 10) is %d, want 9", got)
	}
	if got := pyBound(-20, 10); got != 0 {
		t.Errorf("pyBound(-20, 10) is %d, want 0", got)
	}
	if got := pyBound(20, 10); got != 10 {
		t.Errorf("pyBound(20, 10) is %d, want 10", got)
	}
	// utf8BytesAsLatin1 is Python's encode("utf-8").decode("latin-1").
	if got := utf8BytesAsLatin1("é"); got != "Ã©" {
		t.Errorf("utf8BytesAsLatin1 is %q, want %q", got, "Ã©")
	}
	// isAllDigits takes digits and nothing else.
	if isAllDigits("") || isAllDigits("1a") || !isAllDigits("12") {
		t.Error("isAllDigits does not read digits the way it should")
	}
	// A DataTypeParam writes the node it holds and nothing more.
	param := New("DataTypeParam", Arg{"this", New("Literal", Arg{"this", "6"}, Arg{"is_string", false})})
	if got, err := Generate(param, ""); err != nil || got != "6" {
		t.Errorf("a DataTypeParam wrote %q (%v)", got, err)
	}
}
