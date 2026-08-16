package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// The history ring carries shadow registers, a stack window and the active ROM
// bank. A field no producer fills is worse than an absent one: the reverse
// views render it, so a user reads a confident zero as the machine's real
// state. The ring's own tests only prove a value survives a Push, which is
// equally true of a field nothing ever writes.
//
// So these test the PRODUCER. Both surfaces build entries, and they must build
// the same entry: a trace recorded with the GUI open and one recorded over
// telnet describing the same instruction differently would be its own bug.

// cpuWithShadows parks known values in every field an entry should carry.
func cpuWithShadows(t *testing.T) *remoteDebugger {
	t.Helper()
	d := newRemoteWithCPU(t)
	c := d.emu.cpu
	c.PC, c.SP = 0x8000, 0x9000
	c.A, c.F = 0xAA, 0x55
	c.SetBC(0x1111)
	c.SetDE(0x2222)
	c.SetHL(0x3333)
	c.IX, c.IY = 0x4444, 0x5555
	c.A_, c.F_ = 0x12, 0x34
	c.B_, c.C_ = 0x56, 0x78
	c.D_, c.E_ = 0x9A, 0xBC
	c.H_, c.L_ = 0xDE, 0xF0
	for i := 0; i < debugger.StackWindow; i++ {
		d.emu.mem.Write(uint16(0x9000+i*2), byte(0x10+i))
		d.emu.mem.Write(uint16(0x9000+i*2+1), 0xAB)
	}
	return d
}

func TestHistoryEntryCarriesTheShadowRegisters(t *testing.T) {
	d := cpuWithShadows(t)

	e := d.buildHistoryEntry(0x8000, true)

	if e.AltAF != 0x1234 {
		t.Errorf("AltAF = %04X, want 1234: the reverse register view reads A'/F' from this field", e.AltAF)
	}
	if e.AltBC != 0x5678 || e.AltDE != 0x9ABC || e.AltHL != 0xDEF0 {
		t.Errorf("shadow pairs = %04X/%04X/%04X, want 5678/9ABC/DEF0", e.AltBC, e.AltDE, e.AltHL)
	}
}

// The return address a backwards step through a CALL needs is on the stack,
// not in a register.
func TestHistoryEntryCarriesTheStackWindow(t *testing.T) {
	d := cpuWithShadows(t)

	e := d.buildHistoryEntry(0x8000, true)

	for i := 0; i < debugger.StackWindow; i++ {
		want := uint16(0xAB00) | uint16(0x10+i)
		if e.Stack[i] != want {
			t.Fatalf("Stack[%d] = %04X, want %04X (little-endian word at SP+%d)", i, e.Stack[i], want, i*2)
		}
	}
}

// A narrow ring is the cheap mode. Eight extra memory reads on every M1 fetch
// is real cost on a path that runs about a million times a second, so it must
// not be paid by someone who did not ask for the wide ring.
func TestANarrowRingPaysForNeitherShadowsNorStack(t *testing.T) {
	d := cpuWithShadows(t)

	e := d.buildHistoryEntry(0x8000, false)

	if e.AltAF != 0 || e.AltBC != 0 {
		t.Error("a narrow ring must not record the shadow registers")
	}
	if e.Stack[0] != 0 {
		t.Error("a narrow ring must not read the stack: that is eight memory reads per instruction")
	}
	// ...but the base fields are recorded even when narrow.
	if e.PC != 0x8000 || e.SP != 0x9000 || e.A != 0xAA {
		t.Error("a narrow ring still records PC, SP and AF")
	}
}

// The ROM bank is recorded so reverse debugging can say which bank an address
// executed in. The GUI producer omitted it, so a trace recorded with the GUI
// open reported bank 0 for its whole length while the docs advertised the
// recorded bank as trustworthy.
func TestBothProducersRecordTheROMBank(t *testing.T) {
	d := cpuWithShadows(t)

	e := d.buildHistoryEntry(0x8000, true)
	if e.ROMBnk != byte(d.currentROMBank()) {
		t.Errorf("telnet producer ROMBnk = %d, want %d", e.ROMBnk, d.currentROMBank())
	}

	g := buildGUIHistoryEntry(d.emu, 0x8000, true)
	if g.ROMBnk != e.ROMBnk {
		t.Errorf("GUI producer ROMBnk = %d but telnet producer = %d: the same instruction "+
			"must be recorded the same way whichever surface opened the ring", g.ROMBnk, e.ROMBnk)
	}
}

// Both surfaces write into ONE shared ring, so an entry's meaning must not
// depend on which of them created it.
func TestBothProducersBuildTheSameEntry(t *testing.T) {
	d := cpuWithShadows(t)

	telnet := d.buildHistoryEntry(0x8000, true)
	gui := buildGUIHistoryEntry(d.emu, 0x8000, true)

	// BranchSource is consumed by whichever producer runs first, so compare
	// everything else.
	telnet.Source, gui.Source = 0, 0
	telnet.SourceFrom, gui.SourceFrom = 0, 0

	if telnet != gui {
		t.Errorf("producers disagree:\n telnet = %+v\n gui    = %+v", telnet, gui)
	}
}
