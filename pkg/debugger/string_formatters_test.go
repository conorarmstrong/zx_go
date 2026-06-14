package debugger

import (
	"strings"
	"testing"
)

// iter 310: cover pure-formatter Stringers — DisassembledLine and
// PCSource. They're used by every debugger surface that renders a
// history view or a disassembly listing.

func TestDisassembledLine_String_NoOperand(t *testing.T) {
	d := DisassembledLine{Addr: 0x8000, Bytes: []byte{0xC9}, Mnem: "RET"}
	got := d.String()
	if !strings.Contains(got, "8000") || !strings.Contains(got, "C9") || !strings.Contains(got, "RET") {
		t.Errorf("DisassembledLine.String() = %q, missing addr/bytes/mnem", got)
	}
	if strings.Contains(got, "  ") == false {
		t.Errorf("DisassembledLine.String() = %q, want padded bytes column", got)
	}
}

func TestDisassembledLine_String_WithOperand(t *testing.T) {
	d := DisassembledLine{Addr: 0x4000, Bytes: []byte{0x21, 0x34, 0x12}, Mnem: "LD HL,", Operand: "$1234"}
	got := d.String()
	for _, want := range []string{"4000", "21", "34", "12", "LD HL,", "$1234"} {
		if !strings.Contains(got, want) {
			t.Errorf("DisassembledLine.String() = %q, missing %q", got, want)
		}
	}
}

func TestPCSource_String(t *testing.T) {
	cases := []struct {
		src  PCSource
		want string
	}{
		{SourceJP, "JP"},
		{SourceJR, "JR"},
		{SourceCall, "CALL"},
		{SourceRet, "RET"},
		{SourceRst, "RST"},
		{SourceInt, "INT"},
		{SourceNmi, "NMI"},
		{SourceReset, "RESET"},
		{PCSource(99), "seq"}, // default arm
	}
	for _, c := range cases {
		if got := c.src.String(); got != c.want {
			t.Errorf("PCSource(%d).String() = %q, want %q", int(c.src), got, c.want)
		}
	}
}
