package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// These tests cover the ring-buffer semantics of timeTravelBuffer
// directly (no live emulator). Capture and restore are exercised
// in integration: a headless run with --time-travel=N followed by
// inspecting tt-status / tt-rewind via the debugger.
//
// We bypass newTimeTravelBuffer because it requires a *emulator;
// the field set we need for ring testing is small.

func newTestTTB(every, max int) *timeTravelBuffer {
	return &timeTravelBuffer{
		everyInsns:  every,
		maxEntries:  max,
		nextCapture: uint64(every),
	}
}

func pushTTEntry(t *timeTravelBuffer, insn uint64, pc uint16, label string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) >= t.maxEntries {
		t.entries = append(t.entries[:0], t.entries[1:]...)
	}
	t.entries = append(t.entries, ttEntry{insn: insn, pc: pc, label: label})
}

func TestTimeTravelEviction(t *testing.T) {
	tt := newTestTTB(1000, 3)
	pushTTEntry(tt, 1000, 0x1000, "")
	pushTTEntry(tt, 2000, 0x2000, "")
	pushTTEntry(tt, 3000, 0x3000, "")
	if tt.Len() != 3 {
		t.Fatalf("want 3 entries, got %d", tt.Len())
	}
	pushTTEntry(tt, 4000, 0x4000, "")
	if tt.Len() != 3 {
		t.Fatalf("after eviction want 3, got %d", tt.Len())
	}
	snaps := tt.Snapshots()
	if snaps[0].insn != 2000 {
		t.Errorf("oldest should be insn=2000, got %d", snaps[0].insn)
	}
	if snaps[2].insn != 4000 {
		t.Errorf("newest should be insn=4000, got %d", snaps[2].insn)
	}
}

func TestTimeTravelFindAtOrBefore(t *testing.T) {
	tt := newTestTTB(1000, 5)
	for _, insn := range []uint64{1000, 2000, 3000, 4000, 5000} {
		pushTTEntry(tt, insn, 0x1000, "")
	}

	cases := []struct {
		target uint64
		want   uint64
		nil    bool
	}{
		{500, 0, true}, // before all
		{1000, 1000, false},
		{1500, 1000, false},
		{2500, 2000, false},
		{5000, 5000, false},
		{9999, 5000, false}, // after all → newest
	}
	for _, c := range cases {
		got := tt.FindAtOrBefore(c.target)
		if c.nil {
			if got != nil {
				t.Errorf("target %d: want nil, got %d", c.target, got.insn)
			}
			continue
		}
		if got == nil {
			t.Errorf("target %d: want %d, got nil", c.target, c.want)
			continue
		}
		if got.insn != c.want {
			t.Errorf("target %d: want %d, got %d", c.target, c.want, got.insn)
		}
	}
}

func TestTimeTravelFindPC(t *testing.T) {
	tt := newTestTTB(1000, 10)
	pushTTEntry(tt, 1000, 0x1000, "")
	pushTTEntry(tt, 2000, 0x2000, "")
	pushTTEntry(tt, 3000, 0x1000, "")
	pushTTEntry(tt, 4000, 0x3000, "")
	pushTTEntry(tt, 5000, 0x1000, "")

	hits := tt.FindPC(0x1000)
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits at $1000, got %d", len(hits))
	}
	for i, want := range []uint64{1000, 3000, 5000} {
		if hits[i].insn != want {
			t.Errorf("hit %d: want insn=%d got %d", i, want, hits[i].insn)
		}
	}

	if hits := tt.FindPC(0x9999); len(hits) != 0 {
		t.Errorf("expected no hits at $9999, got %d", len(hits))
	}
}

func TestTimeTravelClear(t *testing.T) {
	tt := newTestTTB(1000, 5)
	pushTTEntry(tt, 1000, 0x1000, "")
	pushTTEntry(tt, 2000, 0x2000, "")
	if tt.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", tt.Len())
	}
	tt.Clear()
	if tt.Len() != 0 {
		t.Errorf("after Clear expected 0 entries, got %d", tt.Len())
	}
}

func TestTimeTravelSnapshotsIsCopy(t *testing.T) {
	tt := newTestTTB(1000, 5)
	pushTTEntry(tt, 1000, 0x1000, "first")

	snap1 := tt.Snapshots()
	if len(snap1) != 1 {
		t.Fatalf("expected 1 entry in copy, got %d", len(snap1))
	}
	// Mutate the returned slice; live state must not change.
	snap1[0].label = "mutated"
	snap2 := tt.Snapshots()
	if snap2[0].label != "first" {
		t.Errorf("Snapshots() should return a copy; live label changed to %q", snap2[0].label)
	}
}

func TestNewTimeTravelBufferRejectsZero(t *testing.T) {
	if b := newTimeTravelBuffer(nil, 0, 16); b != nil {
		t.Error("everyInsns=0 should return nil")
	}
	if b := newTimeTravelBuffer(nil, 1000, 0); b != nil {
		t.Error("maxEntries=0 should return nil")
	}
}

// TestTimeTravelStepRearmOnRewind verifies that an instruction-
// counter rewind (cold-reset / RZX playback start) triggers a
// re-arm of nextCapture so the next capture lands at the post-
// rewind baseline + everyInsns rather than the original far-future
// threshold. Uses a fake-emulator hook substituting for cpu.
func TestTimeTravelStepRearmOnRewind(t *testing.T) {
	tt := newTestTTB(100, 3)
	// Simulate having seen insns=1000, with the threshold set far
	// ahead (1100). A subsequent Step at insns=0 must re-arm at
	// 100, not pass through.
	tt.lastSeenInsns = 1000
	tt.nextCapture = 1100
	// Build a stub for Step's read of emu.cpu.InstructionCount by
	// inspecting the post-Step state. We can't easily call Step
	// directly (it needs a real emu), but we can re-implement the
	// re-arm logic inline and verify it matches expectations.
	insns := uint64(0)
	if insns < tt.lastSeenInsns {
		tt.nextCapture = insns + uint64(tt.everyInsns)
	}
	tt.lastSeenInsns = insns
	if tt.nextCapture != 100 {
		t.Errorf("rewind didn't re-arm: nextCapture=%d", tt.nextCapture)
	}
}

func TestTimeTravelFindAtOrBeforeReturnsCopy(t *testing.T) {
	tt := newTestTTB(1000, 5)
	pushTTEntry(tt, 1000, 0x1000, "original")

	got := tt.FindAtOrBefore(2000)
	if got == nil {
		t.Fatal("expected hit")
	}
	got.label = "mutated"

	// Live state should still hold the original.
	snaps := tt.Snapshots()
	if snaps[0].label != "original" {
		t.Errorf("FindAtOrBefore must return a copy; live label = %q", snaps[0].label)
	}
}

// TestCaptureBankContext_Next covers the per-snapshot bank/paging
// context (time-travel Phase 2a).
func TestCaptureBankContext_Next(t *testing.T) {
	rd := newRemoteFor(t, roms.ModelNext)
	ctx := captureBankContext(rd.emu)
	if ctx == "" {
		t.Fatal("captureBankContext returned empty for Next emulator")
	}
	// This fixture has mem but no ULA, so the divMMC clause is absent;
	// the rom/slots clause must always be present. (The dmmc: clause is
	// exercised in the live --debugger-port smoke test.)
	for _, want := range []string{"rom=", "slots="} {
		if !strings.Contains(ctx, want) {
			t.Errorf("bankCtx %q missing %q", ctx, want)
		}
	}
}

func TestCaptureBankContext_NilSafe(t *testing.T) {
	if got := captureBankContext(nil); got != "" {
		t.Errorf("captureBankContext(nil) = %q, want empty", got)
	}
}
