package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The RAM-read hook mirrors the write hook: it must fire on every
// successful RAM read returned via Memory.Read, carrying the byte read.
// The debugger's read-watchpoint relies on this to halt on the exact
// instruction that reads a watched address.
func TestRAMReadHookSetGetAndFires(t *testing.T) {
	m := newTestMemory(t, roms.Model128K)

	if got := m.GetRAMReadHook(); got != nil {
		t.Fatalf("initial read hook = non-nil")
	}

	// $4000 is RAM on the 128K model. Seed a known byte.
	m.Write(0x4000, 0x42)

	var gotAddr uint16
	var gotVal byte
	calls := 0
	m.SetRAMReadHook(func(bank int, addr uint16, val byte) {
		calls++
		gotAddr = addr
		gotVal = val
	})
	if m.GetRAMReadHook() == nil {
		t.Fatalf("read hook after set = nil")
	}

	if v := m.Read(0x4000); v != 0x42 {
		t.Fatalf("Read($4000) = $%02X, want $42", v)
	}
	if calls == 0 {
		t.Fatalf("read hook did not fire on RAM read")
	}
	if gotVal != 0x42 {
		t.Fatalf("read hook val = $%02X, want $42", gotVal)
	}
	// $4000 is offset 0 within its 16K page.
	if gotAddr != 0x0000 {
		t.Fatalf("read hook addr = $%04X, want $0000 (page offset)", gotAddr)
	}

	// Detach: no further firing.
	m.SetRAMReadHook(nil)
	if m.GetRAMReadHook() != nil {
		t.Fatalf("read hook after nil-set = non-nil")
	}
	before := calls
	_ = m.Read(0x4000)
	if calls != before {
		t.Fatalf("read hook fired after detach")
	}
}
