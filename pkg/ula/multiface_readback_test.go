package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Multiface 3 paging-register readback. Per the FPGA source
// (zxnext.vhd:2612-2616 + the mf_port_dat mux at ~line 2960, and
// multiface.vhd:43-44 "Port read 7F3F: return 7ffd / 1F3F: return 1ffd"):
// while the Multiface is active (invisible off) in MF+3 mode, an IN whose
// LOW byte is $3F returns a paging register selected by A15:12 —
//
//	$7F3F -> port $7FFD (full byte)
//	$1F3F -> port $1FFD (low nibble: bits 7-4 = 0)
//
// NextZXOS's 128K-BASIC launch fires the MF NMI; its handler reads $7F3F and
// $1F3F to snapshot paging into MF RAM ($3FCC/$3FFF), which a later routine
// tests via `cp $04; jr nz` (MF ROM $01F6) to decide whether to continue to
// the Sinclair 128 menu or abort — so these reads must return the real
// paging registers, not open bus.
func TestMultifacePagingReadback(t *testing.T) {
	mem, err := memory.New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	u := New(mem, keyboard.New())

	mem.PageMemory(0x14)      // $7FFD = $14
	mem.PageMemoryPlus3(0x05) // $1FFD = $05
	got7, _, _ := mem.GetPortState()
	if got7 != 0x14 {
		t.Fatalf("setup: $7FFD = %#x, want $14", got7)
	}

	// MF INACTIVE: $7F3F must NOT return the paging reg (open-bus float).
	mem.SetMultifaceActive(false)
	if v, handled := u.ReadPort(0x7F3F); handled && v == 0x14 {
		t.Fatalf("MF inactive: $7F3F returned paging reg %#x (want open bus, unhandled)", v)
	}

	// MF ACTIVE: $7F3F -> $7FFD full byte; $1F3F -> $1FFD low nibble.
	mem.SetMultifaceActive(true)
	if v, handled := u.ReadPort(0x7F3F); !handled || v != 0x14 {
		t.Fatalf("MF active: $7F3F = %#x handled=%v, want $14", v, handled)
	}
	if v, handled := u.ReadPort(0x1F3F); !handled || v != 0x05 {
		t.Fatalf("MF active: $1F3F = %#x handled=%v, want $05", v, handled)
	}
}

// Layer 2 port $123B readback. Per the FPGA source (zxnext.vhd:2822
// `port_123b_rd_dat <= port_123b_dat`), an IN from $123B returns the LAST
// value written to that port (a readback register, reset 0). The 128K
// launch's MF NMI handler reads $123B to snapshot the Layer 2 state, so
// this must return the real latch, not open bus (which reads as bit1=1,
// "Layer 2 visible", and would leave Layer 2 visibly enabled at the menu).
func TestLayer2PortReadback(t *testing.T) {
	mem, err := memory.New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	u := New(mem, keyboard.New())
	f := &fakeNextRegs{regs: map[byte]byte{}}
	u.SetNextRegs(f)

	// Reset value is 0 (not open-bus $FF).
	if v, handled := u.ReadPort(0x123B); !handled || v != 0x00 {
		t.Fatalf("default $123B read = %#x handled=%v, want $00", v, handled)
	}
	// After a write, the read returns that exact byte.
	u.WritePort(0x123B, 0x42)
	if v, handled := u.ReadPort(0x123B); !handled || v != 0x42 {
		t.Fatalf("$123B read after write $42 = %#x handled=%v, want $42", v, handled)
	}
}
