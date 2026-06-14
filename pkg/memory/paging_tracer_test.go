package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

type pagingEvent struct {
	source        string
	val           byte
	applied       bool
	specialBefore bool
	specialAfter  bool
}

// The paging tracer observes every classic-paging port write —
// including writes DROPPED by the 7FFD bit-5 lock — so the launch
// divergence hunt can see whether the guest's $1FFD special-mode
// writes are being made and whether they take effect (the development log
// D31em).
func TestPagingTracerOnPlus3SpecialModeEntryExit(t *testing.T) {
	dir := "test_roms_paging_tracer_special"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var events []pagingEvent
	mem.SetPagingTracer(func(source string, val byte, applied, specialBefore, specialAfter bool) {
		events = append(events, pagingEvent{source, val, applied, specialBefore, specialAfter})
	})

	mem.PageMemoryPlus3(0x01) // enter special mode 0 (all-RAM 0,1,2,3)
	mem.PageMemoryPlus3(0x00) // exit special mode

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	want0 := pagingEvent{"1FFD", 0x01, true, false, true}
	if events[0] != want0 {
		t.Errorf("event[0] = %+v, want %+v", events[0], want0)
	}
	want1 := pagingEvent{"1FFD", 0x00, true, true, false}
	if events[1] != want1 {
		t.Errorf("event[1] = %+v, want %+v", events[1], want1)
	}
}

func TestPagingTracerReportsLockDroppedWrites(t *testing.T) {
	dir := "test_roms_paging_tracer_locked"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var events []pagingEvent
	mem.SetPagingTracer(func(source string, val byte, applied, specialBefore, specialAfter bool) {
		events = append(events, pagingEvent{source, val, applied, specialBefore, specialAfter})
	})

	mem.PageMemory(0x20)      // set 7FFD bit 5 — lock paging
	mem.PageMemoryPlus3(0x01) // dropped by the lock on real HW too
	mem.PageMemory(0x07)      // dropped

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	if !events[0].applied || events[0].source != "7FFD" {
		t.Errorf("event[0] = %+v, want applied 7FFD write", events[0])
	}
	if events[1].applied || events[1].source != "1FFD" || events[1].specialAfter {
		t.Errorf("event[1] = %+v, want DROPPED 1FFD write, special stays off", events[1])
	}
	if events[2].applied || events[2].source != "7FFD" {
		t.Errorf("event[2] = %+v, want DROPPED 7FFD write", events[2])
	}
}

func TestPagingTracerNilIsTransparent(t *testing.T) {
	dir := "test_roms_paging_tracer_nil"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)

	mem, err := New(dir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mem.PageMemoryPlus3(0x01)
	_, _, special := mem.GetPortState()
	if !special {
		t.Errorf("special paging should engage with nil tracer")
	}
}
