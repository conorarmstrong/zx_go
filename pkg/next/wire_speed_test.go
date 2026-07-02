package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// minimalMem satisfies z80.Memory for the wire_speed test without
// pulling in pkg/memory.
type minimalMem struct{}

func (minimalMem) Read(_ uint16) byte     { return 0 }
func (minimalMem) Write(_ uint16, _ byte) {}
func (minimalMem) ContendPort(_ uint16)   {}

type minimalULA struct{}

func (minimalULA) ReadPort(_ uint16) (byte, bool) { return 0xFF, false }
func (minimalULA) WritePort(_ uint16, _ byte)     {}

func TestWireCPUSpeedRoutesWritesToCPU(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)

	// Guest writes 0x02 (14 MHz) to NextReg 0x07 via port 0x253B.
	disp.Select(0x07)
	disp.WriteData(0x02)

	if cpu.SpeedSelect() != 0x02 {
		t.Errorf("after dispatcher write to 0x07: cpu.SpeedSelect = %#x, want 0x02", cpu.SpeedSelect())
	}
	if cpu.SpeedMultiplier() != 4 {
		t.Errorf("SpeedMultiplier = %d, want 4", cpu.SpeedMultiplier())
	}
}

func TestWireCPUSpeedReadBack(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)

	// Per zxnext.vhd:5902 the read shape is:
	//   bit 7:6 = 0
	//   bit 5:4 = current cpu_speed (dynamic, == target for us)
	//   bit 3:2 = 0
	//   bit 1:0 = target nr_07_cpu_speed
	// So sel=$01 reads as $01 | ($01 << 4) = $11.
	cpu.SetSpeedSelect(0x01) // 7 MHz
	disp.Select(0x07)
	if got := disp.ReadData(); got != 0x11 {
		t.Errorf("disp read of 0x07 = $%02X, want $11 (bits 5:4 current + 1:0 programmed)", got)
	}
}

// TestWireCPUSpeed_ReadShapeAllSpeeds verifies the zxnext.vhd:5902
// read shape: `port_253b_dat <= "00" & cpu_speed & "00" &
// nr_07_cpu_speed` — bits 7:6 = 00, bits 5:4 = CURRENT speed,
// bits 3:2 = 00, bits 1:0 = programmed.
func TestWireCPUSpeed_ReadShapeAllSpeeds(t *testing.T) {
	cpu := z80.New(minimalMem{}, minimalULA{})
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)

	for sel := byte(0); sel < 4; sel++ {
		cpu.SetSpeedSelect(sel)
		want := (sel << 4) | sel
		disp.Select(0x07)
		if got := disp.ReadData(); got != want {
			t.Errorf("speed sel=%d: read $%02X, want $%02X", sel, got, want)
		}
	}
}
