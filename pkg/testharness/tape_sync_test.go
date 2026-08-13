package testharness

import (
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// corpusTape is the one tape vendored in the tree, so these tests need no
// local title collection.
func corpusTape() string {
	return filepath.Join("testdata", "corpus", "bin", "zilogdma.tap")
}

// standardBlocks counts the blocks a ROM LD-BYTES load would consume: the
// header/data pairs, ignoring the TZX-only pure-tone/turbo kinds.
func standardBlocks(tp *ula.TapePlayer) int {
	n := 0
	for _, b := range tp.Blocks() {
		if b.Type == "Header" || b.Type == "Data" {
			n++
		}
	}
	return n
}

// TestTapeBlocksConsumedCountsTrapFires pins the counter refdiff uses to prove
// both machines consumed the same tape through the same ROM entry point. A
// pixel diff between two machines that loaded different amounts of a tape is
// noise dressed as a finding, so the count has to be observable.
func TestTapeBlocksConsumedCountsTrapFires(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	h.RunFrames(200) // reach the K prompt
	if err := h.LoadTAP(corpusTape()); err != nil {
		t.Fatalf("LoadTAP: %v", err)
	}
	want := standardBlocks(h.ULA().GetTapePlayer())
	if want == 0 {
		t.Fatal("corpus tape has no standard blocks — wrong fixture")
	}
	if got := h.TapeBlocksConsumed(); got != 0 {
		t.Fatalf("TapeBlocksConsumed before load = %d, want 0", got)
	}
	h.TypeLoadCommand()
	h.RunFrames(300)
	if got := h.TapeBlocksConsumed(); got != want {
		t.Fatalf("TapeBlocksConsumed = %d, want %d", got, want)
	}
	if h.TapeLastLoadTstate() == 0 {
		t.Fatal("TapeLastLoadTstate = 0 after a load")
	}
}

// TestTapeTrapGatedOnBasicROM asserts the fast-load trap only fires while the
// 48 BASIC ROM — the one that actually holds LD-BYTES at $0556 — is paged at
// $0000. On the 128K the machine sits in the menu ROM (bank 0) until "Tape
// Loader" is selected, and $0556 there is unrelated code: trapping it consumes
// a block and scribbles it over RAM using whatever IX/DE happened to hold.
//
// The GUI has always gated on this (cmd/zx_go's tapeTrapROMActive); the
// harness did not, which made every headless 128K tape run diverge from the
// session a user gets.
func TestTapeTrapGatedOnBasicROM(t *testing.T) {
	h, err := New(roms.Model128K)
	if err != nil {
		t.Fatalf("New(128K): %v", err)
	}
	if err := h.LoadTAP(corpusTape()); err != nil {
		t.Fatalf("LoadTAP: %v", err)
	}
	h.RunFrames(200) // sit in the 128 menu
	if bank := h.MemoryBus().GetROMBank(); bank != 0 {
		t.Fatalf("128 menu ROM bank = %d, want 0 — fixture assumption broken", bank)
	}
	// Enter $0556 with the menu ROM paged. There is no LD-BYTES there, so the
	// trap must decline; the LOAD contract registers are set as the real
	// routine would see them so a firing trap would write a whole block over
	// the screen.
	cpu := h.CPU()
	cpu.PC = 0x0556
	cpu.A = 0x00
	cpu.F |= z80.FLAG_C
	cpu.IX = 0x4000
	cpu.D, cpu.E = 0x10, 0x00
	h.RunFrames(1)
	if got := h.TapeBlocksConsumed(); got != 0 {
		t.Fatalf("menu ROM consumed %d tape blocks — trap not gated on the 48 BASIC ROM", got)
	}

	// Back to the menu for the positive half.
	h.Reboot()
	h.RunFrames(200)
	// Selecting Tape Loader pages ROM bank 1 in and runs LOAD"", which must
	// then load normally.
	h.TapKey("Return")
	h.RunFrames(300)
	if got := h.TapeBlocksConsumed(); got == 0 {
		t.Fatal("Tape Loader consumed no blocks — the gate blocks the real loader too")
	}
}

// TestDisplayFileFollowsShadowScreen pins DisplayFile to the page the machine
// is actually displaying. Reading $4000 always reads page 5, so a 128K title
// that switched to the shadow screen in page 7 would be compared on a screen
// nobody was looking at.
func TestDisplayFileFollowsShadowScreen(t *testing.T) {
	h, err := New(roms.Model128K)
	if err != nil {
		t.Fatalf("New(128K): %v", err)
	}
	// $7FFD bit 3 selects page 7 as the display; bits 0-2 page it at $C000 so
	// the marker can be written through the CPU's own address space.
	h.MemoryBus().PageMemory(0x0F)
	const marker = 0xA5
	h.WriteMemory(0xC000, marker)

	got := h.DisplayFile()
	if len(got) != 6912 {
		t.Fatalf("DisplayFile length = %d, want 6912", len(got))
	}
	if got[0] != marker {
		t.Fatalf("DisplayFile()[0] = %#02x, want %#02x — reading page 5, not the displayed page", got[0], marker)
	}
	// The returned slice must be a copy: refdiff holds a before/after pair.
	got[0] ^= 0xFF
	if again := h.DisplayFile(); again[0] != marker {
		t.Fatal("DisplayFile returned a live view of RAM, not a copy")
	}
}

// TestRunUntilTstatesReachesTarget pins the T-state clock as the unit both
// sides of a differential run measure their settle windows in. Frames are the
// wrong unit for that: the harness runs 69888 T-states per frame on every
// model, so "n frames" means a different amount of guest time here than on a
// machine using its own line count.
func TestRunUntilTstatesReachesTarget(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	target := h.GuestTstates() + 5_000_000
	h.RunUntilTstates(target)
	got := h.GuestTstates()
	if got < target {
		t.Fatalf("Tstates = %d, want >= %d", got, target)
	}
	// One frame of overshoot at most: the loop steps whole frames.
	if got > target+TstatesPerFrame {
		t.Fatalf("Tstates = %d overshot %d by more than a frame", got, target)
	}
}

// TestRunUntilTapeIdleWaitsForTheLastBlock pins the event-driven wait that
// replaces "sleep for a fixed number of frames". Loading is over when the ROM
// loader stops being entered, which is a property of the guest, not of a
// number picked in advance.
func TestRunUntilTapeIdleWaitsForTheLastBlock(t *testing.T) {
	const quietT = 3_500_000 // 1 s of guest time with no block loaded
	const deadlineT = 200_000_000

	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	h.RunFrames(200)
	if err := h.LoadTAP(corpusTape()); err != nil {
		t.Fatalf("LoadTAP: %v", err)
	}
	h.StartTapeLoad()
	if !h.RunUntilTapeIdle(quietT, deadlineT) {
		t.Fatal("RunUntilTapeIdle hit the deadline on a tape that loads")
	}
	if h.TapeBlocksConsumed() != standardBlocks(h.ULA().GetTapePlayer()) {
		t.Fatalf("went idle after %d of %d blocks",
			h.TapeBlocksConsumed(), standardBlocks(h.ULA().GetTapePlayer()))
	}
	if h.GuestTstates()-h.TapeLastLoadTstate() < quietT {
		t.Fatal("went idle before the quiet window elapsed")
	}
}

// TestRunUntilTapeIdleReportsDeadline is the other half: a tape that is never
// loaded must be reported as "still loading", not silently treated as done.
// Reporting an unsynchronised pair as a divergence is the failure mode this
// guards against.
func TestRunUntilTapeIdleReportsDeadline(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	h.RunFrames(200)
	if err := h.LoadTAP(corpusTape()); err != nil {
		t.Fatalf("LoadTAP: %v", err)
	}
	// No StartTapeLoad: the guest sits at the BASIC prompt and never enters
	// LD-BYTES, so no block is ever consumed.
	if h.RunUntilTapeIdle(3_500_000, 20*TstatesPerFrame) {
		t.Fatal("RunUntilTapeIdle reported idle without a single block loaded")
	}
	if h.TapeBlocksConsumed() != 0 {
		t.Fatalf("TapeBlocksConsumed = %d without a load command", h.TapeBlocksConsumed())
	}
}

// TestStartTapeLoadIsModelAware pins the one difference between the models: a
// 48K needs LOAD"" typed at the K prompt, a 128K needs ENTER on the ROM menu's
// pre-highlighted Tape Loader. Getting this wrong is silent — the 48K sequence
// happens to work on a 128K only because J and SYMBOL SHIFT+P are no-ops at
// the menu and the trailing ENTER selects Tape Loader by accident.
func TestStartTapeLoadIsModelAware(t *testing.T) {
	for _, model := range []roms.SpectrumModel{roms.Model48K, roms.Model128K} {
		model := model
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			h, err := New(model)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			h.RunFrames(200)
			if err := h.LoadTAP(corpusTape()); err != nil {
				t.Fatalf("LoadTAP: %v", err)
			}
			h.StartTapeLoad()
			h.RunFrames(300)
			if h.TapeBlocksConsumed() == 0 {
				t.Fatal("StartTapeLoad consumed no tape blocks")
			}
		})
	}
}
