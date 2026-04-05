package z80

import (
	"fmt"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// bootTracingULA tracks port I/O and provides +3 FDC responses.
type bootTracingULA struct {
	mem           *memory.Memory
	portWrites    []portWrite
	portReadCount map[uint16]int
}

type portWrite struct {
	addr uint16
	val  byte
}

func newBootTracingULA(mem *memory.Memory) *bootTracingULA {
	return &bootTracingULA{
		mem:           mem,
		portReadCount: make(map[uint16]int),
	}
}

func (u *bootTracingULA) ReadPort(addr uint16) (byte, bool) {
	if addr&0x01 == 0 {
		u.portReadCount[0x00FE]++
		return 0xBF, true // No keys pressed
	}
	if addr&0xF002 == 0x2000 { // +3 FDC status port 0x2FFD
		u.portReadCount[0x2FFD]++
		return 0x80, true
	}
	if addr&0xF002 == 0x3000 { // +3 FDC data port 0x3FFD
		u.portReadCount[0x3FFD]++
		return 0xFF, true
	}
	if addr&0xC002 == 0xC000 { // AY register read (port 0xFFFD)
		u.portReadCount[0xFFFD]++
		return 0xFF, true
	}
	u.portReadCount[addr]++
	return 0xFF, false
}

func (u *bootTracingULA) WritePort(addr uint16, val byte) {
	u.portWrites = append(u.portWrites, portWrite{addr, val})

	model := u.mem.GetCurrentModel()

	if addr&0x01 == 0 {
		return
	}

	if model == roms.ModelPlus3 || model == roms.ModelPlus2A {
		if addr&0xC002 == 0x4000 {
			u.mem.PageMemory(val)
		} else if addr&0xF002 == 0x1000 {
			u.mem.PageMemoryPlus3(val)
		}
	} else if model != roms.Model48K {
		if addr&0x8002 == 0 {
			u.mem.PageMemory(val)
		}
	}
}

// countScreenPixels counts non-zero pixels in the screen area
func countScreenPixels(mem *memory.Memory) int {
	screenPage := mem.GetPage(mem.ScreenPage)
	count := 0
	for i := 0; i < 0x1800; i++ {
		b := screenPage[i]
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) != 0 {
				count++
			}
		}
	}
	return count
}

// identifyROM reads the first few bytes at 0x0000 to identify which ROM is paged in
func identifyROM(mem *memory.Memory) string {
	b0 := mem.Read(0x0000)
	b1 := mem.Read(0x0001)
	b2 := mem.Read(0x0002)
	b3 := mem.Read(0x0003)
	return fmt.Sprintf("%02X %02X %02X %02X", b0, b1, b2, b3)
}

// realLikeULA mimics the real ULA without FDC port handling.
// Unhandled port reads return (0xFF, false) -- the floating bus value.
type realLikeULA struct {
	mem *memory.Memory
}

func newRealLikeULA(mem *memory.Memory) *realLikeULA {
	return &realLikeULA{mem: mem}
}

func (u *realLikeULA) ReadPort(addr uint16) (byte, bool) {
	if addr&0x01 == 0 {
		return 0xBF, true
	}
	return 0xFF, false
}

func (u *realLikeULA) WritePort(addr uint16, val byte) {
	model := u.mem.GetCurrentModel()

	if addr&0x01 == 0 {
		return
	}

	if model == roms.ModelPlus3 || model == roms.ModelPlus2A {
		if addr&0xC002 == 0x4000 {
			u.mem.PageMemory(val)
		} else if addr&0xF002 == 0x1000 {
			u.mem.PageMemoryPlus3(val)
		}
	} else if model != roms.Model48K {
		if addr&0x8002 == 0 {
			u.mem.PageMemory(val)
		}
	}
}

// TestPlus3BootToMenu verifies the +3 ROM boots to a menu with pixel output.
func TestPlus3BootToMenu(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	ula := newBootTracingULA(mem)
	cpu := New(mem, ula)

	const maxFrames = 500
	menuAppeared := false

	for frame := 0; frame < maxFrames; frame++ {
		cpu.ExecuteFrame(69888)
		px := countScreenPixels(mem)

		if px > 200 {
			menuAppeared = true
			t.Logf("Menu appeared at frame %d with %d pixels", frame, px)
			break
		}
	}

	if !menuAppeared {
		t.Errorf("+3 menu did not appear after %d frames", maxFrames)
	}
}

// TestPlus3BootWithUnhandledPorts verifies the +3 ROM boots even when FDC ports
// are not handled (returns 0xFF floating bus). This was the root cause of the
// original hang: IN instructions did not update registers when ReadPort returned
// ok=false, causing the ROM to misinterpret FDC status and enter an infinite loop.
func TestPlus3BootWithUnhandledPorts(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	ula := newRealLikeULA(mem)
	cpu := New(mem, ula)

	const maxFrames = 200
	menuAppeared := false

	for frame := 0; frame < maxFrames; frame++ {
		cpu.ExecuteFrame(69888)
		px := countScreenPixels(mem)

		if px > 1000 {
			menuAppeared = true
			t.Logf("Menu fully drawn at frame %d with %d pixels", frame, px)
			break
		}
	}

	if !menuAppeared {
		t.Errorf("+3 menu did not appear with unhandled FDC ports (IN instruction bug regression)")
	}
}

// TestPlus2ABootToMenu verifies the +2A also boots correctly (uses same ROMs as +3).
func TestPlus2ABootToMenu(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus2A)
	if err != nil {
		t.Skipf("Skipping: cannot load +2A ROMs: %v", err)
	}

	ula := newRealLikeULA(mem)
	cpu := New(mem, ula)

	const maxFrames = 200
	menuAppeared := false

	for frame := 0; frame < maxFrames; frame++ {
		cpu.ExecuteFrame(69888)
		px := countScreenPixels(mem)

		if px > 1000 {
			menuAppeared = true
			t.Logf("+2A menu fully drawn at frame %d with %d pixels", frame, px)
			break
		}
	}

	if !menuAppeared {
		t.Errorf("+2A menu did not appear after %d frames", maxFrames)
	}
}

// TestPlus3vs128Boot compares boot behavior between +3 and 128K
func TestPlus3vs128Boot(t *testing.T) {
	romPath := "../../roms"

	for _, model := range []struct {
		name  string
		model roms.SpectrumModel
	}{
		{"128K", roms.Model128K},
		{"+3", roms.ModelPlus3},
	} {
		t.Run(model.name, func(t *testing.T) {
			mem, err := memory.New(romPath, model.model)
			if err != nil {
				t.Skipf("Skipping: cannot load ROMs: %v", err)
			}

			ula := newBootTracingULA(mem)
			cpu := New(mem, ula)

			prevPx := 0
			for frame := 0; frame < 200; frame++ {
				cpu.ExecuteFrame(69888)
				px := countScreenPixels(mem)

				if px != prevPx || frame < 5 || frame%25 == 0 {
					t.Logf("Frame %3d: PC=0x%04X SP=0x%04X IFF1=%v Halted=%v px=%d ROM=[%s]",
						frame, cpu.PC, cpu.SP, cpu.IFF1, cpu.Halted, px, identifyROM(mem))
				}
				prevPx = px
			}
		})
	}
}
