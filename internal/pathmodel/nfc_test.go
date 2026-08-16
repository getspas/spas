package pathmodel

import "testing"

// Managed paths are normalized to NFC at the single parsing funnel, matching
// the precomposed form Git uses on macOS: exclude patterns are not normalized
// by Git, so a decomposed spelling would never match its own exclusion rule.
func TestParseNormalizesToNFC(t *testing.T) {
	t.Parallel()

	decomposed := "secrets/re\u0301sume\u0301.txt" // NFD: e + combining acute
	composed := "secrets/r\u00e9sum\u00e9.txt"     // NFC
	got, err := Parse(decomposed)
	if err != nil {
		t.Fatalf("Parse(NFD) error = %v", err)
	}
	want, err := Parse(composed)
	if err != nil {
		t.Fatalf("Parse(NFC) error = %v", err)
	}
	if got != want {
		t.Fatalf("Parse(NFD) = %q, want the NFC spelling %q", got, want)
	}
	if got.String() != string(want) {
		t.Fatalf("stored spelling = %q, want %q", got.String(), want)
	}
}
