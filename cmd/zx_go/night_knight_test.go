package main

import (
	"os"
	"testing"
)

// Night-Knight-Demo.nex used to hang permanently after NEXLOAD: bank 5's
// interrupt handler reasserts the same classic RAM bank (4) into $C000 every
// frame via port $7FFD, but Memory.PageMemory only re-synced the Next's MMU
// slots 6/7 from that bank when the encoded bank number changed from the
// previous write — not what real hardware does (zxnext.vhd re-syncs on every
// $7FFD-family write). Since the value never changed, $C000 could never
// reclaim itself from an earlier, unrelated NextReg $56/$57 override, so the
// CPU executed leftover data as code, leaking ~308 bytes of stack every
// frame until the game livelocked. Fixed by making the sync unconditional.
// These tests pin both the externally-visible symptom (the game reaches and
// stays at the correct post-launch resting state) and the underlying
// mechanism (the stack no longer drains frame over frame).

// TestNightKnightRepro boots real NextZXOS, drives the actual NEXLOAD dot
// command, and verifies Night-Knight settles at the same PC/SP real hardware
// does (confirmed against MAME): PC=$1F3E, SP=$5FDF.
func TestNightKnightRepro(t *testing.T) {
	path := "../../roms/next/sd/games/Next/Night-Knight/Night-Knight-Demo.nex"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}
	emu := bootNextToMenu(t)
	nexloadFromMenu(emu, "/games/Next/Night-Knight/Night-Knight-Demo.nex", 0)

	const wantPC, wantSP = 0x1f3e, 0x5fdf
	reached := false
	for i := 0; i < 600; i++ {
		nextRunFrames(emu, 1)
		if emu.cpu.PC == wantPC && emu.cpu.SP == wantSP {
			reached = true
			break
		}
	}
	if !reached {
		t.Fatalf("Night-Knight never settled at PC=%#04x SP=%#04x (real hardware's resting state); got PC=%#04x SP=%#04x IFF1=%v",
			wantPC, wantSP, emu.cpu.PC, emu.cpu.SP, emu.cpu.IFF1)
	}

	// Confirm it stays there (no residual leak) and is actually rendering
	// the title screen, not stuck on a blank/uniform frame.
	for i := 0; i < 20; i++ {
		nextRunFrames(emu, 1)
		if emu.cpu.PC != wantPC || emu.cpu.SP != wantSP {
			t.Fatalf("drifted away from the resting state at frame %d: PC=%#04x SP=%#04x", i, emu.cpu.PC, emu.cpu.SP)
		}
	}
	saveNexShot(emu, "night-knight-settled")
	if _, nonBlank := nextScreen(emu); !nonBlank {
		t.Fatalf("screen is blank/uniform at the resting state; expected the title screen to be rendered")
	}
}

// TestNightKnightNoStackLeak is a narrower regression test for the exact
// mechanism that was broken: once bank 5 is running and taking its
// once-per-frame interrupt, SP must not drift downward frame after frame.
func TestNightKnightNoStackLeak(t *testing.T) {
	path := "../../roms/next/sd/games/Next/Night-Knight/Night-Knight-Demo.nex"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not present", path)
	}
	emu := bootNextToMenu(t)
	nexloadFromMenu(emu, "/games/Next/Night-Knight/Night-Knight-Demo.nex", 0)

	reachedBank5 := false
	for i := 0; i < 600; i++ {
		nextRunFrames(emu, 1)
		if emu.cpu.PC >= 0x4000 && emu.cpu.PC < 0x8000 {
			reachedBank5 = true
			break
		}
	}
	if !reachedBank5 {
		t.Fatalf("never reached bank5, PC=%#04x SP=%#04x", emu.cpu.PC, emu.cpu.SP)
	}

	// Run well past the point the leak used to accumulate (previously
	// -308 bytes/frame) and confirm SP settles rather than draining.
	var spSamples []uint16
	for i := 0; i < 40; i++ {
		nextRunFrames(emu, 1)
		spSamples = append(spSamples, emu.cpu.SP)
	}
	last := spSamples[len(spSamples)-1]
	for i := len(spSamples) - 10; i < len(spSamples); i++ {
		if spSamples[i] != last {
			t.Fatalf("SP still drifting near the end of the run (frame %d: SP=%#04x, frame %d: SP=%#04x) — stack leak regressed",
				i, spSamples[i], len(spSamples)-1, last)
		}
	}
}
