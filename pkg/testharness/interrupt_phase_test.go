package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The raster geometry that v1.6.0 corrected is only ever observable as a PHASE
// against the frame interrupt: how long after the INT the ULA first stalls a
// contended access, and how long until it first fetches paper. Every existing
// test checks the contention function or the constants in isolation, which is
// how a 230-T-state error in the whole 128K family went unnoticed — the
// renderer and the contention model shared the same wrong origin, so nothing
// that compared them to each other could see it.
//
// These measure the phase through the emulator's own memory and ULA, sweeping
// the frame rather than asserting at a point.
//
// The expected figures are written out as LITERALS on purpose. Comparing the
// measurement against roms.DisplayStartTState would be tautological: the
// emulator derives its behaviour from that same constant, so changing the
// constant moves both sides and the test passes regardless. Verified by
// reverting the origin to the old flat 64*lineLength — with the constant as
// the expectation the tests still passed; with these literals they fail.

// firstStalledTState sweeps the frame and reports the first T-state at which a
// contended-bank access is charged, or -1 if none is.
func firstStalledTState(h *Harness, limit int) int {
	mem := h.MemoryBus()
	ts := mem.TStates
	for t := 0; t < limit; t++ {
		*ts = uint64(t)
		before := *ts
		mem.ContendMemory(0x4000) // bank 5: contended on every classic model
		if *ts != before {
			return t
		}
	}
	return -1
}

// firstPaperTState sweeps the frame and reports the first T-state at which the
// floating bus returns screen data rather than 0xFF.
func firstPaperTState(h *Harness, limit int) int {
	ts := h.MemoryBus().TStates
	for t := 0; t < limit; t++ {
		*ts = uint64(t)
		if v, _ := h.ULA().ReadPort(0xFF); v != 0xFF {
			return t
		}
	}
	return -1
}

// TestFirstContendedTStateMatchesTheModel measures the observable contention
// phase and asserts it is the per-model figure derived from the FPGA.
func TestFirstContendedTStateMatchesTheModel(t *testing.T) {
	for _, tc := range []struct {
		model roms.SpectrumModel
		want  int
	}{
		{roms.Model48K, 14335},
		{roms.Model128K, 14361},
		{roms.ModelPlus2, 14361},
		{roms.ModelPlus2A, 14362},
		{roms.ModelPlus3, 14362},
	} {
		model, want := tc.model, tc.want
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			h, err := New(model)
			if err != nil {
				t.Fatalf("New(%v): %v", model, err)
			}
			// Contention needs a live T-state counter.
			var ts uint64
			h.MemoryBus().SetTStatePtr(&ts)

			got := firstStalledTState(h, 30000)
			if got != want {
				t.Errorf("first contended T-state = %d, want %d (a %+d T-state phase error against the interrupt)",
					got, want, got-want)
			}
		})
	}
}

// TestFirstPaperFetchMatchesTheModel does the same through a completely
// different surface — the floating bus, which reports what the ULA is
// fetching — so the two consumers of the origin are cross-checked against the
// derived figure rather than against each other.
func TestFirstPaperFetchMatchesTheModel(t *testing.T) {
	// The +2A/+3 have no floating bus, and the 48K/128K origin is what this
	// measures.
	for _, tc := range []struct {
		model  roms.SpectrumModel
		origin int
	}{
		{roms.Model48K, 14336},
		{roms.Model128K, 14362},
		{roms.ModelPlus2, 14362},
	} {
		model, origin := tc.model, tc.origin
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			h, err := New(model)
			if err != nil {
				t.Fatalf("New(%v): %v", model, err)
			}
			var ts uint64
			h.MemoryBus().SetTStatePtr(&ts)

			// Fill the screen so a paper fetch is distinguishable from 0xFF.
			page := h.MemoryBus().GetPage(5)
			for i := range page {
				page[i] = 0xA5
			}

			got := firstPaperTState(h, 30000)
			// Offsets 0 and 1 of the fetch pattern are idle cycles, so the
			// first non-0xFF T-state is the origin plus two.
			if got != origin+2 {
				t.Errorf("first paper fetch = %d, want %d (origin %d + 2 idle cycles)",
					got, origin+2, origin)
			}
		})
	}
}

// TestContentionAndPaperShareOnePhase is the assertion that would have caught
// the v1.6.0 bug directly: whatever the origin is, the ULA must start stalling
// exactly one T-state before it starts fetching. Two independent measurements,
// one relationship.
func TestContentionAndPaperShareOnePhase(t *testing.T) {
	for _, model := range []roms.SpectrumModel{roms.Model48K, roms.Model128K, roms.ModelPlus2} {
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			h, err := New(model)
			if err != nil {
				t.Fatalf("New(%v): %v", model, err)
			}
			var ts uint64
			h.MemoryBus().SetTStatePtr(&ts)
			page := h.MemoryBus().GetPage(5)
			for i := range page {
				page[i] = 0xA5
			}

			stall := firstStalledTState(h, 30000)
			paper := firstPaperTState(h, 30000)
			if stall < 0 || paper < 0 {
				t.Fatalf("no contention (%d) or no paper fetch (%d) found in the frame", stall, paper)
			}
			// Stall opens one T-state before the fetch; the fetch pattern's
			// first two cycles are idle, so paper reads two later again.
			if got := paper - stall; got != 3 {
				t.Errorf("paper fetch is %d T-states after the first stall, want 3 "+
					"(1 prefetch + 2 idle cycles) — the two consumers disagree on the origin", got)
			}
		})
	}
}
