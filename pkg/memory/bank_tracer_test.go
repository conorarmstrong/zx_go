package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

type bankSwitchEvent struct {
	slot   byte
	bank   int
	source string
}

func TestBankTracerOnSetMMU(t *testing.T) {
	dir := "test_roms_bank_tracer_mmu"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var events []bankSwitchEvent
	mem.SetBankTracer(func(slot byte, bank int, source string) {
		events = append(events, bankSwitchEvent{slot, bank, source})
	})
	mem.SetMMU(3, 0x07)
	mem.SetMMU(5, 0x12)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0] != (bankSwitchEvent{3, 0x07, "NEXTREG_50+slot"}) {
		t.Errorf("event[0] = %+v", events[0])
	}
	if events[1] != (bankSwitchEvent{5, 0x12, "NEXTREG_50+slot"}) {
		t.Errorf("event[1] = %+v", events[1])
	}
}

func TestBankTracerOnPageMemory(t *testing.T) {
	dir := "test_roms_bank_tracer_page"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var events []bankSwitchEvent
	mem.SetBankTracer(func(slot byte, bank int, source string) {
		events = append(events, bankSwitchEvent{slot, bank, source})
	})
	// Toggle ROM bank via 7FFD bit 4.
	mem.PageMemory(0x10) // ROM 1
	// Only one event expected: ROM bank changed from 0 to 1.
	if len(events) != 1 {
		t.Fatalf("PageMemory(0x10): got %d events, want 1", len(events))
	}
	if events[0].slot != 0xFF || events[0].bank != 1 || events[0].source != "7FFD" {
		t.Errorf("event = %+v, want slot=0xFF bank=1 source=\"7FFD\"", events[0])
	}
}

func TestBankTracerNoSpuriousEventOnSameBank(t *testing.T) {
	dir := "test_roms_bank_tracer_idem"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var events []bankSwitchEvent
	mem.SetBankTracer(func(slot byte, bank int, source string) {
		events = append(events, bankSwitchEvent{slot, bank, source})
	})
	// Writing the same value twice should produce ONE event
	// (or zero — currently we only fire on actual change).
	mem.PageMemory(0x10)
	mem.PageMemory(0x10)
	if len(events) != 1 {
		t.Errorf("Repeated PageMemory should yield 1 event, got %d", len(events))
	}
}

func TestBankTracerNilIsTransparent(t *testing.T) {
	dir := "test_roms_bank_tracer_nil"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Tracer not set — paging must still work.
	mem.PageMemory(0x10)
	mem.SetMMU(3, 5)
	if mem.GetMMU(3) != 5 {
		t.Errorf("MMU broken with nil tracer")
	}
}
