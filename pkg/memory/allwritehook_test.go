package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The all-writes hook is the symmetric counterpart of SetAllReadHook: it
// fires on EVERY successful Write carrying the LOGICAL address + value,
// regardless of which dispatch path (classic page map, Next MMU override,
// divMMC window) services the write. Debug paths use it to watch a logical
// address (e.g. a game's loader-ready flag) for the write that should set
// it, without having to reconstruct the physical bank at the destination.
func TestAllWriteHookFiresWithLogicalAddress(t *testing.T) {
	m := newTestMemory(t, roms.Model128K)

	type ev struct {
		addr uint16
		val  byte
	}
	var events []ev
	m.SetAllWriteHook(func(addr uint16, val byte) {
		events = append(events, ev{addr, val})
	})

	// $4000 is RAM on the 128K model — the hook fires with the logical
	// address (not a physical bank offset) and the value.
	m.Write(0x4000, 0x42)
	if len(events) != 1 || events[0].addr != 0x4000 || events[0].val != 0x42 {
		t.Fatalf("write event = %+v, want one {$4000 $42}", events)
	}

	// A write to ROM is ignored and must NOT fire the hook (no byte
	// actually lands).
	events = events[:0]
	m.Write(0x0000, 0x99)
	if len(events) != 0 {
		t.Fatalf("all-write hook fired %d times on an ignored ROM write, want 0", len(events))
	}

	// Detach: no further firing.
	m.SetAllWriteHook(nil)
	events = events[:0]
	m.Write(0x4000, 0x01)
	if len(events) != 0 {
		t.Fatalf("all-write hook fired %d times after detach, want 0", len(events))
	}
}
