package debugger

import "testing"

// Pins Hint() for the RST and conditional-CALL origin classes (the
// plain-CALL and speculative arms are covered in backtrace_test.go).

func TestBacktraceHint_RstAndCallCond(t *testing.T) {
	rst := Frame{Origin: OriginRst, CallTarget: 0x38, CallSite: 0x6001}
	if got, want := rst.Hint(), "RST $38 @ $6001"; got != want {
		t.Errorf("RST Hint() = %q, want %q", got, want)
	}
	cc := Frame{Origin: OriginCallCond, CallTarget: 0x2000, CallSite: 0x100D}
	if got, want := cc.Hint(), "CALL $2000 @ $100D"; got != want {
		t.Errorf("CALL cc Hint() = %q, want %q", got, want)
	}
}
