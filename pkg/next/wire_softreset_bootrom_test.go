package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

func bootromHarness(t *testing.T) (*nextregs.Dispatcher, *memory.Memory, *z80.CPU) {
	t.Helper()
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	WireMachineType(disp, mem)
	WireReset(disp, mem, cpu, nil)
	return disp, mem, cpu
}

// TestSoftResetInConfigModeRearmsBootrom — per zxnext.vhd:5108-5110,
// the NR reset block (reset = hard OR soft) re-asserts bootrom_en
// when nr_03_config_mode is active:
//
//	if reset = '1' then
//	   ...
//	   if nr_03_config_mode = '1' then
//	      bootrom_en <= '1';
//	   end if;
//
// This is THE mechanism behind the firmware config menu's machine
// selection: the firmware re-enters config mode (NR$03 bits 2:0 = 0),
// then issues an NR$02 soft reset — the FPGA comes back up in the
// bootrom, TBBLUE.FW reloads, and the chosen personality boots.
// Without the re-arm the CPU restarts at $0000 in the stale memory
// map and spins in firmware leftovers (the config-menu freeze).
func TestSoftResetInConfigModeRearmsBootrom(t *testing.T) {
	d, mem, _ := bootromHarness(t)

	// Power-on: bootrom armed, config mode active (zxnext.vhd:1101-1103).
	mem.SetFPGABootROM(make([]byte, 8192))
	if !mem.ConfigModeActive() {
		t.Fatal("precondition: power-on must start in config mode")
	}
	// boot.bin's first NR$03 write (a timing set, bits 2:0 = 000)
	// clears bootrom_en (any NR$03 write does) but PRESERVES config
	// mode (zxnext.vhd:5147-5151: 000 = no change). This is the
	// splash / config-editor era state.
	d.WriteReg(0x03, 0xB0)
	if mem.FPGABootROMActive() {
		t.Fatal("precondition: any NR$03 write must clear bootrom mode")
	}
	if !mem.ConfigModeActive() {
		t.Fatal("precondition: NR$03 bits 2:0 = 000 must preserve config mode")
	}

	// The config menu's machine selection ends in an NR$02 soft
	// reset issued while still in config mode.
	d.WriteReg(0x02, 0x01) // soft reset
	if !mem.FPGABootROMActive() {
		t.Error("soft reset with config_mode active must re-arm the FPGA bootrom (zxnext.vhd:5108-5110)")
	}
}

// TestSoftResetOutsideConfigModeKeepsBootromOff — the same VHDL block
// is guarded on nr_03_config_mode: a soft reset with a personality
// installed must NOT drop back into the bootrom (NextZXOS's staging
// soft-reset relies on resuming its installed ROMs).
func TestSoftResetOutsideConfigModeKeepsBootromOff(t *testing.T) {
	d, mem, _ := bootromHarness(t)

	mem.SetFPGABootROM(make([]byte, 8192))
	d.WriteReg(0x03, 0xB3) // personality installed, config off
	d.WriteReg(0x02, 0x01) // soft reset
	if mem.FPGABootROMActive() {
		t.Error("soft reset outside config mode must NOT re-arm the bootrom")
	}
}
