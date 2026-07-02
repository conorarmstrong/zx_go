package debugger

import (
	stdbytes "bytes"
	"testing"
)

func TestParseUserHexHexDefault(t *testing.T) {
	v, err := parseUserHex("$1A")
	if err != nil || v != 26 {
		t.Fatalf("$1A → %d err=%v, want 26 nil", v, err)
	}
	v, err = parseUserHex("0x1a")
	if err != nil || v != 26 {
		t.Fatalf("0x1a → %d err=%v, want 26 nil", v, err)
	}
	v, err = parseUserHex("10")
	if err != nil || v != 16 {
		t.Fatalf("bare 10 → %d err=%v, want 16 nil (hex default)", v, err)
	}
}

func TestParseUserHexDecimalSuffix(t *testing.T) {
	v, err := parseUserHex("10d")
	if err != nil || v != 10 {
		t.Fatalf("10d → %d err=%v, want 10 nil", v, err)
	}
}

// TestParseUserHexDollarPrefixEndingInD guards against the decimal
// "...d" suffix rule shadowing an explicit $-prefixed hex literal
// whose last digit happens to be D — $1D and $FD are valid hex
// (should be treated as such, not as "1" / "F" with a decimal
// suffix marker).
func TestParseUserHexDollarPrefixEndingInD(t *testing.T) {
	v, err := parseUserHex("$1D")
	if err != nil || v != 0x1D {
		t.Fatalf("$1D → %d err=%v, want %d nil", v, err, 0x1D)
	}
	v, err = parseUserHex("$FD")
	if err != nil || v != 0xFD {
		t.Fatalf("$FD → %d err=%v, want %d nil", v, err, 0xFD)
	}
}

func TestParseUserHexUppercase0XPrefix(t *testing.T) {
	v, err := parseUserHex("0X1A")
	if err != nil || v != 0x1A {
		t.Fatalf("0X1A → %d err=%v, want %d nil", v, err, 0x1A)
	}
}

func TestParseUserHexEmpty(t *testing.T) {
	if _, err := parseUserHex(""); err == nil {
		t.Fatalf("empty input must error")
	}
}

func TestParseByteListSpaceSeparated(t *testing.T) {
	got, err := parseByteList("AA BB CC")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xAA, 0xBB, 0xCC}
	if !stdbytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
}

func TestParseByteListCommaSeparated(t *testing.T) {
	got, err := parseByteList("$10,$20;$30")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x10, 0x20, 0x30}
	if !stdbytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
}

func TestParseByteListEmpty(t *testing.T) {
	got, err := parseByteList("   ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %x", got)
	}
}

func TestHexDumpRendersTwoRows(t *testing.T) {
	d := make([]byte, 32)
	for i := range d {
		d[i] = byte(i)
	}
	got := hexDump(0x4000, d)
	// Expect two rows starting at 004000 and 004010.
	if !stdbytes.Contains([]byte(got), []byte("004000:")) {
		t.Errorf("missing first row prefix in:\n%s", got)
	}
	if !stdbytes.Contains([]byte(got), []byte("004010:")) {
		t.Errorf("missing second row prefix in:\n%s", got)
	}
	// Trailing ASCII column for printable bytes.
	if !stdbytes.Contains([]byte(got), []byte("..........")) {
		t.Errorf("expected dots for non-printable bytes:\n%s", got)
	}
}

func TestHexDumpShortRowPadsHex(t *testing.T) {
	got := hexDump(0, []byte{0xAA, 0xBB, 0xCC})
	// The unfilled hex columns should be blank-padded so the ASCII
	// gutter stays at a fixed column.
	if stdbytes.Count([]byte(got), []byte("   ")) < 13 {
		t.Errorf("expected padding for short row:\n%s", got)
	}
}
