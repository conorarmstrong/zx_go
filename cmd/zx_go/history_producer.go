package main

import (
	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Building one history entry, shared by both debugger surfaces.
//
// The two surfaces write into ONE ring: whichever opens first creates it and
// the other reuses it. So an entry's meaning must not depend on which surface
// built it. It did: the GUI producer omitted the ROM bank, so a trace recorded
// with the GUI open reported bank 0 for its whole length while the reverse
// views advertised the recorded bank as trustworthy. Both now go through here.
//
// Hot path. This runs on every M1 fetch, roughly a million times a second at
// 28 MHz, so it allocates nothing and the wide-only work is genuinely skipped
// rather than computed and discarded.

// historyStackWindow reads the top debugger.StackWindow words at SP.
//
// The return address a backwards step through a CALL needs is here, not in a
// register, so without it a reverse walk cannot explain where control came
// from. It is wide-only: eight extra memory reads per instruction is real cost
// on this path, and someone running a narrow ring did not ask to pay it.
//
// Reads go through the normal memory path, so paging is respected and the
// words are what the guest would have seen. A read near $FFFF wraps, which is
// what the hardware does.
func historyStackWindow(mem memReader, sp uint16) [debugger.StackWindow]uint16 {
	var out [debugger.StackWindow]uint16
	for i := 0; i < debugger.StackWindow; i++ {
		addr := sp + uint16(i*2)
		lo := uint16(mem.Read(addr))
		hi := uint16(mem.Read(addr + 1))
		out[i] = hi<<8 | lo
	}
	return out
}

// memReader is the slice of memory this file needs, kept narrow so the
// producer can be tested without a whole machine.
type memReader interface {
	Read(addr uint16) byte
}

// fillHistoryEntry builds an entry from the CPU's current state.
//
// romBank is passed rather than derived because the two surfaces reach it
// differently, and wide selects whether the pair registers, the shadow
// registers and the stack window are recorded.
func fillHistoryEntry(c *z80.CPU, mem memReader, pc uint16, romBank byte, wide bool) debugger.HistoryEntry {
	e := debugger.HistoryEntry{
		PC:         pc,
		SP:         c.SP,
		A:          c.A,
		F:          c.F,
		IFFIM:      debugger.PackIFFIM(c.IFF1, c.IFF2, c.Halted, int(c.IM)),
		Insns:      c.InstructionCount(),
		ROMBnk:     romBank,
		Source:     debugger.PCSource(c.BranchSource),
		SourceFrom: c.BranchFrom,
	}
	if wide {
		e.BC = c.BC()
		e.DE = c.DE()
		e.HL = c.HL()
		e.IX = c.IX
		e.IY = c.IY
		e.AltAF = uint16(c.A_)<<8 | uint16(c.F_)
		e.AltBC = uint16(c.B_)<<8 | uint16(c.C_)
		e.AltDE = uint16(c.D_)<<8 | uint16(c.E_)
		e.AltHL = uint16(c.H_)<<8 | uint16(c.L_)
		if mem != nil {
			e.Stack = historyStackWindow(mem, c.SP)
		}
	}
	return e
}

// buildHistoryEntry is the telnet surface's producer.
func (d *remoteDebugger) buildHistoryEntry(pc uint16, wide bool) debugger.HistoryEntry {
	return fillHistoryEntry(d.emu.cpu, d.emu.mem, pc, byte(d.currentROMBank()), wide)
}

// buildGUIHistoryEntry is the visual surface's producer. It resolves the ROM
// bank the same way the telnet surface does, so the two agree.
func buildGUIHistoryEntry(emu *emulator, pc uint16, wide bool) debugger.HistoryEntry {
	return fillHistoryEntry(emu.cpu, emu.mem, pc, emuROMBank(emu), wide)
}

// emuROMBank is currentROMBank without a debugger attached: the bank index
// (0-3) mapped at $0000-$3FFF. Both producers must derive it identically, so
// there is one implementation and remoteDebugger.currentROMBank defers to it.
func emuROMBank(emu *emulator) byte {
	port7FFD, port1FFD, _ := emu.mem.GetPortState()
	return byte(int((port7FFD>>4)&1) | int((port1FFD>>1)&2))
}
