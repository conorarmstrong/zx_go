package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// newSamEmulator installs a stand-in 48K memory as e.mem so the
// Spectrum-shaped menus have something non-nil to talk to. Every memory
// tool read that stand-in, so on the SAM the hex view showed a blank 48K
// Spectrum and a poke reported success and changed nothing.
func TestDebugMemoryReachesTheLiveSAM(t *testing.T) {
	emu, err := newSamEmulator()
	if err != nil {
		t.Skipf("newSamEmulator: %v", err)
	}
	dm := emu.debugMemory()

	// $8000 is section C, plain RAM on a default SAM.
	emu.sam.Mem.Write(0x8000, 0x5A)
	if got := dm.Read(0x8000); got != 0x5A {
		t.Errorf("debugMemory read $8000 = %#02x, want 0x5a — it is reading the stand-in", got)
	}
	if got := emu.mem.Read(0x8000); got == 0x5A {
		t.Error("the stand-in happens to hold the same value; the test cannot tell them apart")
	}

	// And a poke has to land in the machine that is running.
	dm.Write(0x8001, 0x3C)
	if got := emu.sam.Mem.Read(0x8001); got != 0x3C {
		t.Errorf("SAM RAM at $8001 = %#02x after a poke, want 0x3c", got)
	}
}

// A Spectrum keeps using its own memory.
func TestDebugMemoryIsTheSpectrumsOwnOnASpectrum(t *testing.T) {
	emu, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	emu.mem.Write(0x8000, 0x77)
	if got := emu.debugMemory().Read(0x8000); got != 0x77 {
		t.Errorf("debugMemory read $8000 = %#02x, want 0x77", got)
	}
}

// The page map has to describe the SAM's own sections, not a 48K's.
func TestDebugMemoryReportsTheSAMPageMap(t *testing.T) {
	emu, err := newSamEmulator()
	if err != nil {
		t.Skipf("newSamEmulator: %v", err)
	}
	// LMPR page 4 puts RAM page 4 in section A and 5 in section B.
	emu.sam.Mem.SetLMPR(0x24) // bit 5 = ROM0 off, so section A is RAM
	read, write := emu.debugMemory().GetPageMap()
	if read != write {
		t.Errorf("read map %v differs from write map %v: the SAM writes through the map it reads", read, write)
	}
	if read[0] != 4 || read[1] != 5 {
		t.Errorf("sections A/B map pages %d/%d, want 4/5", read[0], read[1])
	}
}

// The telnet memory commands and the poke dialog have to see the live
// machine too, not just the adapter in isolation.
func TestTelnetPeekAndPokeReachTheLiveSAM(t *testing.T) {
	emu, err := newSamEmulator()
	if err != nil {
		t.Skipf("newSamEmulator: %v", err)
	}
	d := &remoteDebugger{emu: emu}

	if got := d.cmdWriteMemory([]string{"8002", "6B"}); got != "OK" {
		t.Fatalf("write-memory: %s", got)
	}
	if got := emu.sam.Mem.Read(0x8002); got != 0x6B {
		t.Errorf("SAM RAM at $8002 = %#02x, want 0x6b", got)
	}
	if got := d.cmdReadMemory([]string{"8002"}); got != "OK $6B" {
		t.Errorf("read-memory returned %q, want \"OK $6B\"", got)
	}
}
