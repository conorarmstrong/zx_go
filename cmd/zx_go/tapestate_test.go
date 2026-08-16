package main

import (
	"bytes"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// The property a user actually cares about: rewind in the middle of a tape
// load, and the load carries on from where it was rather than from wherever the
// tape had reached by the time they rewound.
//
// This is an integration test on purpose. A round trip inside pkg/ula proves
// the player's fields survive a capture; it cannot prove the player is IN the
// capture, and for a long time it was not — the machine rewound around a tape
// that kept running, so the ROM's edge loop resumed against the wrong part of
// the stream and the load failed.
//
// The load here is the real one: the 48K ROM's LD-BYTES decoding real pulse
// edges off the player, with the fast-load trap deliberately NOT installed. A
// trapped load reads a block whole out of the block list and would still
// "work" with the tape position lost, which is exactly the wrong thing to
// measure.

// ldBytesPark is where the emulated LD-BYTES returns to: two bytes of JR -2, so
// the CPU parks there and the test can see the load has finished.
const ldBytesPark = 0x9000

// enterLDBYTES puts the machine at the ROM's LD-BYTES entry with its documented
// contract: A = expected flag byte, carry set for LOAD (not VERIFY), IX = the
// destination, DE = the byte count. The return address is stacked so the
// routine's RET lands somewhere the test controls.
func enterLDBYTES(emu *emulator, flag byte, dest uint16, count uint16) {
	emu.mem.Write(ldBytesPark, 0x18) // JR -2
	emu.mem.Write(ldBytesPark+1, 0xFE)

	emu.cpu.A = flag
	emu.cpu.F |= z80.FLAG_C
	emu.cpu.IX = dest
	emu.cpu.D, emu.cpu.E = byte(count>>8), byte(count)
	emu.cpu.SP -= 2
	emu.mem.Write(emu.cpu.SP, byte(ldBytesPark&0xFF))
	emu.mem.Write(emu.cpu.SP+1, byte(ldBytesPark>>8))
	emu.cpu.PC = 0x0556
}

// loadFinished reports whether LD-BYTES has returned to the parked address.
func loadFinished(emu *emulator) bool {
	return emu.cpu.PC == ldBytesPark || emu.cpu.PC == ldBytesPark+1
}

// readBack returns the bytes the loader wrote.
func readBack(emu *emulator, dest uint16, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = emu.mem.Read(dest + uint16(i))
	}
	return out
}

func TestRewindMidLoadResumesTheSameLoad(t *testing.T) {
	const (
		dest    = 0x8000
		payload = 128
		budget  = 600 // frames; the tape's pilot alone is ~100
	)
	want := make([]byte, payload)
	for i := range want {
		want[i] = byte(i*7) ^ 0x5A
	}

	emu := quietEmulator(t, roms.Model48K)
	if err := loadTapeFile(emu, writeTempTAP(t, 0xFF, want)); err != nil {
		t.Fatalf("loadTapeFile: %v", err)
	}
	tape := emu.ula.GetTapePlayer()
	enterLDBYTES(emu, 0xFF, dest, payload)

	// Run into the middle of the DATA phase. DE is LD-BYTES's byte counter, so
	// a value strictly inside the payload means the pilot and sync are behind
	// us and bytes are arriving — the state a rewind has to be able to resume.
	captureFrame := -1
	for i := 0; i < budget && captureFrame < 0; i++ {
		runOneFrameHeadless(emu, roms.Model48K)
		if de := uint16(emu.cpu.D)<<8 | uint16(emu.cpu.E); de > payload/8 && de < payload-payload/8 {
			captureFrame = i
		}
	}
	if captureFrame < 0 {
		t.Fatalf("never reached the middle of the data phase: PC=$%04X DE=%d block=%d",
			emu.cpu.PC, uint16(emu.cpu.D)<<8|uint16(emu.cpu.E), tape.CurrentBlock())
	}

	reg := emu.stateRegistry()
	mid := reg.Capture()
	midBlock, midPlaying := tape.CurrentBlock(), tape.IsPlaying()

	// Run on to the end of the load, and record everything the run produced.
	frames := 0
	for ; frames < budget && !loadFinished(emu); frames++ {
		runOneFrameHeadless(emu, roms.Model48K)
	}
	if !loadFinished(emu) {
		t.Fatalf("the load never finished, so there is nothing to compare a rewind against")
	}
	if got := readBack(emu, dest, payload); !bytes.Equal(got, want) {
		t.Fatalf("the first, un-rewound load did not read the tape correctly, so this test "+
			"is measuring a broken loader rather than a rewind (first mismatch at %d)",
			firstDiff(got, want))
	}
	if emu.cpu.F&z80.FLAG_C == 0 {
		t.Fatal("the first load returned with carry clear (R Tape loading error)")
	}
	firstRun := reg.Capture()

	// Let the tape run off the end before rewinding. Without this the player is
	// still sitting in the block's trailing pause, so "the tape went back" and
	// "the tape never moved" look the same.
	//
	// The player is advanced directly rather than by running frames: the guest
	// has parked in a DI'd loop that never reads port $FE, and the tape only
	// moves when the ULA is asked for the EAR level.
	tape.Update(5_000_000) // longer than the block's ~1s trailing pause
	if tape.IsPlaying() {
		t.Fatalf("the tape was expected to run out before the rewind; it is still on block %d",
			tape.CurrentBlock())
	}

	// Wipe the destination first, so a pass cannot be the completed load's bytes
	// still lying in RAM. The restore puts the partially-loaded bytes back (the
	// capture holds them) and the resumed load has to supply the rest.
	for i := 0; i < payload; i++ {
		emu.mem.Write(dest+uint16(i), 0)
	}

	// Rewind to the middle of the load and run the same number of frames again.
	if err := reg.Restore(mid); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := tape.CurrentBlock(); got != midBlock {
		t.Errorf("tape block after rewind = %d, want %d: the rewind left the tape where it had run to",
			got, midBlock)
	}
	if got := tape.IsPlaying(); got != midPlaying {
		t.Errorf("tape playing after rewind = %v, want %v", got, midPlaying)
	}
	for i := 0; i < frames; i++ {
		runOneFrameHeadless(emu, roms.Model48K)
	}
	if !loadFinished(emu) {
		t.Fatalf("after the rewind the load had not finished in the same %d frames: PC=$%04X DE=%d",
			frames, emu.cpu.PC, uint16(emu.cpu.D)<<8|uint16(emu.cpu.E))
	}
	if got := readBack(emu, dest, payload); !bytes.Equal(got, want) {
		t.Errorf("after rewinding into the load, the resumed load read the wrong bytes "+
			"(first mismatch at %d): the tape did not go back with the machine",
			firstDiff(got, want))
	}
	if emu.cpu.F&z80.FLAG_C == 0 {
		t.Error("the resumed load returned with carry clear (R Tape loading error)")
	}

	// The strongest form of the same claim: replaying the identical frames from
	// the capture produced the identical machine, tape included.
	if after := reg.Capture(); !bytes.Equal(after.Bytes(), firstRun.Bytes()) {
		t.Error("replaying from the mid-load capture produced a different machine than the " +
			"run it was captured from")
	}
}

func firstDiff(a, b []byte) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return len(a)
}
