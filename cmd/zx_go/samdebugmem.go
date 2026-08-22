package main

import (
	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/sam"
)

// samDebugMemory presents the SAM Coupé's address space through the
// debugger's Memory interface.
//
// newSamEmulator installs a stand-in 48K *memory.Memory as e.mem so the
// Spectrum-shaped menus have something non-nil to talk to. Every memory
// tool read that stand-in, so on the SAM the hex view showed a blank 48K
// Spectrum whatever the machine was doing and a poke reported success and
// changed nothing. This routes them to the live machine instead.
type samDebugMemory struct{ m *sam.Machine }

func (s samDebugMemory) Read(addr uint16) byte       { return s.m.Mem.Read(addr) }
func (s samDebugMemory) Write(addr uint16, val byte) { s.m.Mem.Write(addr, val) }

func (s samDebugMemory) GetCurrentModel() roms.SpectrumModel { return roms.ModelSAM }

// GetPageMap reports the internal RAM page behind each of the SAM's four
// 16 KB sections. The SAM writes through the same map it reads, so both
// halves are identical. A section holding ROM, external RAM or a scratch
// page has no internal page: those are reported as ROM (>= 16), which is
// how the view renders a non-RAM slot.
func (s samDebugMemory) GetPageMap() ([4]int, [4]int) {
	var m [4]int
	for i, p := range s.m.Mem.SectionPages() {
		if p < 0 {
			m[i] = 16
			continue
		}
		m[i] = p
	}
	return m, m
}

// GetPortState returns the SAM's two paging latches, LMPR and HMPR. They
// occupy the slots a Spectrum fills with $7FFD and $1FFD — the same role,
// a different machine's registers.
func (s samDebugMemory) GetPortState() (byte, byte, bool) {
	return s.m.Mem.LMPR(), s.m.Mem.HMPR(), false
}

// ScreenPageIndex is the RAM page the display is being read from (VMPR).
func (s samDebugMemory) ScreenPageIndex() int { return int(s.m.Mem.VisibleScreenPage()) }

// debugMemory is the address space the memory tools should inspect: the
// live machine's, which is e.mem on every machine except the SAM.
func (e *emulator) debugMemory() debugger.Memory {
	if e.sam != nil {
		return samDebugMemory{m: e.sam}
	}
	return e.mem
}
