package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The all-reads hook (--read-tape-all) must fire on EVERY Read — ROM
// reads included — carrying the LOGICAL address + value. This is the
// load-bearing property for bank-agnostic first-divergence lockstep vs a
// reference emulator: ROM reads match by construction, so once both tapes
// log every read in order, the first divergent VALUE is the root cause
// even when code is banked through a window. Contrast SetRAMReadHook,
// which fires on RAM reads only (and so would mis-align against a
// reference that logs all reads).
func TestAllReadHookFiresOnROMAndRAM(t *testing.T) {
	m := newTestMemory(t, roms.Model128K)

	type ev struct {
		addr uint16
		val  byte
	}
	var events []ev
	m.SetAllReadHook(func(addr uint16, val byte) {
		events = append(events, ev{addr, val})
	})

	// $0000 is ROM on the 128K model — the RAM hook would NOT fire here,
	// but the all-reads hook MUST.
	romVal := m.Read(0x0000)
	if len(events) != 1 {
		t.Fatalf("all-read hook fired %d times on a ROM read, want 1", len(events))
	}
	if events[0].addr != 0x0000 || events[0].val != romVal {
		t.Fatalf("ROM read event = {addr:$%04X val:$%02X}, want {$0000 $%02X}",
			events[0].addr, events[0].val, romVal)
	}

	// $4000 is RAM — the hook fires with the logical address (not a
	// physical bank offset) and the value.
	m.Write(0x4000, 0x42)
	events = events[:0]
	if v := m.Read(0x4000); v != 0x42 {
		t.Fatalf("Read($4000) = $%02X, want $42", v)
	}
	if len(events) != 1 || events[0].addr != 0x4000 || events[0].val != 0x42 {
		t.Fatalf("RAM read event = %+v, want one {$4000 $42}", events)
	}

	// Detach: no further firing.
	m.SetAllReadHook(nil)
	events = events[:0]
	_ = m.Read(0x4000)
	if len(events) != 0 {
		t.Fatalf("all-read hook fired %d times after detach, want 0", len(events))
	}
}
