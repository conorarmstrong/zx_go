package testharness

import "testing"

// Covers the unexported helpers (textTimeoutError.Error, quote,
// itoa) used by RunUntilText timeout reporting. They're pure
// formatters with simple inputs, but the cover tool flags them as
// uncovered without a direct call.

func TestQuote(t *testing.T) {
	if got := quote("hello"); got != "\"hello\"" {
		t.Errorf("quote(hello) = %q, want \"hello\"", got)
	}
	if got := quote(""); got != "\"\"" {
		t.Errorf("quote(empty) = %q, want \"\"", got)
	}
}

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{1234567, "1234567"},
		{-1, "-1"},
		{-12345, "-12345"},
	}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Errorf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTextTimeoutError_Error(t *testing.T) {
	e := &textTimeoutError{want: "READY", frames: 250}
	got := e.Error()
	want := "testharness: text \"READY\" not seen within 250 frames"
	if got != want {
		t.Errorf("textTimeoutError.Error() = %q, want %q", got, want)
	}
}
