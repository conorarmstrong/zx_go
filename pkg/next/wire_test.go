package next

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// wireTestULA is a minimal z80.ULA stub for tests that just need
// a CPU instance without real port traffic.
type wireTestULA struct{}

func (wireTestULA) ReadPort(_ uint16) (byte, bool) { return 0xFF, false }
func (wireTestULA) WritePort(_ uint16, _ byte)     {}

func wireTestROMs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"48.rom", "128-0.rom", "128-1.rom"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 0x4000), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestWireConfigModeRAMPageRoutesToMemory verifies WireConfigModeRAMPage
// installs an OnWrite handler that latches NextReg $04 (REG_RAMPAGE)
// writes into memory.SetConfigModeRAMPage. The boot.bin firmware
// running at $6000 issues a stream of REG_RAMPAGE writes during
// its ROM-load loop (boot.c:loadFile) — without this wiring, the
// rampage value stays at zero and every loaded ROM smashes the
// same page.
func TestWireConfigModeRAMPageRoutesToMemory(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireConfigModeRAMPage(disp, mem)

	// Config-mode active/inactive state is owned by NR$03 (per
	// zxnext.vhd:1102 — nr_03_config_mode is the sole gate). NR$04
	// only selects the page register. The OnWrite handler must
	// store the page through to memory unconditionally; it must
	// not touch config_mode.
	disp.Select(0x04)
	disp.WriteData(0x10) // RAMPAGE_RAMSPECCY

	if got := mem.ConfigModeRAMPage(); got != 0x10 {
		t.Errorf("after NextReg $04=$10: ConfigModeRAMPage = %#x, want $10", got)
	}
	if got := disp.Raw(0x04); got != 0x10 {
		t.Errorf("dispatcher storage: Raw($04) = %#x, want $10", got)
	}
}

// TestWireConfigModeRAMPageDoesNotEnterConfigMode locks in the
// "NR$04 must not flip config_mode" contract — a regression of
// disp.Reset() walking NR$04=0 silently re-entering config mode
// (caught during the post-soft-reset boot debug; the page selector
// must never be conflated with the mode latch).
func TestWireConfigModeRAMPageDoesNotEnterConfigMode(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	mem.ClearConfigMode()
	disp := nextregs.New()
	WireConfigModeRAMPage(disp, mem)
	disp.Select(0x04)
	disp.WriteData(0x10)
	if mem.ConfigModeActive() {
		t.Errorf("NR$04 write must not enter config mode; only NR$03 may")
	}
}

// TestWireMachineTypeOnceExitedStays verifies the reference Spectrum Next emulator-faithful
// semantics around the config-mode transition: once guest code has
// written a real machine personality (bits 2-0 != 0), subsequent
// writes with bits 2-0 = 0 must NOT re-enter config mode. The
// `$0000-$3FFF` window stays mapped per the personality's normal
// paging (e.g. +3 ROM bank 0).
//
// Without this guard, NextZXOS bank-0 init's "NEXTREG $03,$B0"
// (which sets +3 display timing without changing the personality
// from +3) accidentally re-routes $0000-$3FFF to whichever RAM
// page REG_RAMPAGE was last latched to (boot.bin's last loaded
// ROM bank). NextZXOS then executes the wrong bytes, eventually
// reaches an "ED 91 02 01" (NEXTREG $02,$01) and soft-resets in
// an infinite loop.
//
// Mirrors `tbblue.c:5013-5020`: tbblue_set_memory_pages() only
// runs when previous_machine_type==0, bootrom was active, or the
// special value & 7 == 7 case.
func TestWireMachineTypeOnceExitedStays(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireMachineType(disp, mem)

	// Power-on path: bootrom active → first $03 write transitions
	// out of bootrom. the reference Spectrum Next emulator clears bootrom on this write.
	mem.SetFPGABootROM(make([]byte, 8192))
	disp.Select(0x03)
	disp.WriteData(0xB0) // bits 2-0 = 0 — still config-mode-ish
	if !mem.ConfigModeActive() {
		t.Errorf("first $03 write from bootrom: config mode should be active")
	}

	// Boot.bin's final write: $03 = $B3 (bits 2-0 = 3) — exits
	// config and sets +3 personality.
	disp.WriteData(0xB3)
	if mem.ConfigModeActive() {
		t.Errorf("after $03 = $B3: config mode must be cleared")
	}

	// NextZXOS bank-0 $0125 writes $03 = $B0 with previous = $B3.
	// Per the reference Spectrum Next emulator semantics this is a no-op for memory paging —
	// config mode must NOT re-activate.
	disp.WriteData(0xB0)
	if mem.ConfigModeActive() {
		t.Errorf("after $03 = $B0 (from non-config personality): config mode must STAY off")
	}
}

// TestWireMachineTypeControlsConfigMode verifies WireMachineType
// installs a NextReg $03 OnWrite handler that:
// - keeps config mode active across writes with bits 2-0 = 0
// while the FPGA bootrom is still in play (boot.bin loads ROMs
// in this window), AND
// - exits config mode the moment guest code installs a real
// personality (bits 2-0 != 0).
//
// Mirrors the reference Spectrum Next emulator's tbblue.c:4999-5020 machine-type handler: the
// memory map is reapplied only on transitions out of bootrom or
// while bits 2-0 are still zero.
func TestWireMachineTypeControlsConfigMode(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	mem.SetConfigModeRAMPage(0x10) // pretend boot.bin set RAMPAGE_RAMSPECCY
	mem.SetFPGABootROM(make([]byte, 8192))
	disp := nextregs.New()
	WireMachineType(disp, mem)

	// Write $03 = $B0: Next/+3 display timing, machine bits 2-0 = 0.
	// With bootrom still active, config mode should remain active.
	disp.Select(0x03)
	disp.WriteData(0xB0)
	if !mem.ConfigModeActive() {
		t.Errorf("after NextReg $03 = $B0 (bootrom path, bits 2-0 = 0): config mode should remain active")
	}

	// Write $03 = $B3: bits 2-0 = 3 = +2A/+3 personality.
	// Config mode must exit so $0000-$3FFF reverts to the
	// personality's normal ROM paging.
	disp.WriteData(0xB3)
	if mem.ConfigModeActive() {
		t.Errorf("after NextReg $03 = $B3 (bits 2-0 = 3): config mode should be cleared")
	}
}

func TestWireMMURoutesWritesToMemory(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireMMU(disp, mem)

	// Write to NextReg 0x52 via the dispatcher's data port path:
	// select 0x52, then write 0x42 to data.
	disp.Select(0x52)
	disp.WriteData(0x42)

	if got := mem.GetMMU(2); got != 0x42 {
		t.Errorf("after dispatcher write to 0x52: mem.GetMMU(2) = %#x, want 0x42", got)
	}
	if !mem.IsMMUOverridden(2) {
		t.Errorf("after dispatcher write: mmuOverride(2) should be true")
	}
	// Dispatcher's storage was also updated by Store.
	if got := disp.Raw(0x52); got != 0x42 {
		t.Errorf("dispatcher storage: Raw(0x52) = %#x, want 0x42", got)
	}
}

func TestWireMMURoutesReadsFromMemory(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireMMU(disp, mem)

	// On power-on slot 5 = bank 2 hi = 5.
	disp.Select(0x55)
	if got := disp.ReadData(); got != 5 {
		t.Errorf("disp read of 0x55: got %#x, want 5 (bank 2 hi)", got)
	}

	// Mutate memory directly; read should reflect.
	mem.SetMMU(5, 0xCC)
	if got := disp.ReadData(); got != 0xCC {
		t.Errorf("disp read of 0x55 after SetMMU: got %#x, want 0xCC", got)
	}
}

func TestWireMMUEveryRegisterIndependent(t *testing.T) {
	// Confirm the loop-capture is correct: writing each NextReg
	// 0x50..0x57 must affect ONLY the corresponding MMU slot.
	mem, err := memory.New(wireTestROMs(t), roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireMMU(disp, mem)

	for slot := byte(0); slot < 8; slot++ {
		disp.Select(0x50 + slot)
		disp.WriteData(0xA0 + slot)
	}
	for slot := byte(0); slot < 8; slot++ {
		if got := mem.GetMMU(slot); got != 0xA0+slot {
			t.Errorf("slot %d: GetMMU = %#x, want %#x", slot, got, 0xA0+slot)
		}
	}
}

// TestWireAltROMRoutesWritesToMemory verifies WireAltROM installs
// the NextReg $8C handler that calls Memory.SetAltROMReg, and that
// guest software reading $8C via the dispatcher sees the same
// value it just wrote.
func TestWireAltROMRoutesWritesToMemory(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireAltROM(disp, mem)

	// Write $C0 to NextReg $8C (NextZXOS's "enable alt-rom +
	// write-only" config).
	disp.Select(0x8C)
	disp.WriteData(0xC0)

	if got := mem.AltROMReg(); got != 0xC0 {
		t.Errorf("after dispatcher write to $8C: AltROMReg() = %#x, want $C0", got)
	}
	// OnRead returns the memory state, not the dispatcher's
	// stored byte.
	disp.Select(0x8C)
	if got := disp.ReadData(); got != 0xC0 {
		t.Errorf("dispatcher read of $8C: got %#x, want $C0", got)
	}
}

// TestWireResetSoftFiresOnEveryWrite locks in the no-edge-detection
// semantics: the FPGA bootrom polls NR$02 after a write to check
// "did the reset fire?". With NR$02 default $01 (= "last reset was
// soft"), a subsequent write of $01 must STILL trigger a soft reset
// — the OUT is a separate write-strobe pulse on the bus, regardless
// of stored value.
func TestWireResetSoftFiresOnEveryWrite(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	WireReset(disp, mem, cpu, nil)

	// Sanity: direct-boot cold default is $02 — the reset_type
	// shift-history seeded at "010" (the FPGA bootrom's single soft
	// reset already applied; zxnext.vhd:1736).
	if got := disp.Raw(0x02); got != 0x02 {
		t.Fatalf("cold NR$02 default = %#x, want $02", got)
	}
	cpu.PC = 0x1234 // arbitrary non-zero
	cpu.IFF1 = true

	// Write $01 — bit 0 set → soft reset must fire.
	disp.Select(0x02)
	disp.WriteData(0x01)

	if cpu.PC != 0 {
		t.Errorf("after NR$02=$01: PC = %#04x, want 0 (soft reset didn't fire)", cpu.PC)
	}
	if cpu.IFF1 {
		t.Errorf("after NR$02=$01: IFF1 still set, want cleared (soft reset didn't fire)")
	}
	if got := disp.Raw(0x02); got&0x01 == 0 {
		t.Errorf("after soft reset: NR$02 = %#x, want bit 0 set (sticky-soft)", got)
	}

	// Set up state again and write $01 AGAIN — must fire again.
	// Edge-detection would suppress this because bit 0 is still
	// set in the stored value.
	cpu.PC = 0x5678
	cpu.IFF1 = true
	disp.Select(0x02)
	disp.WriteData(0x01)
	if cpu.PC != 0 {
		t.Errorf("second NR$02=$01 write: PC = %#04x, want 0 (no-edge-detection contract broken)", cpu.PC)
	}
}

// TestWireResetHardClearsRegisters verifies a write that sets
// bit 1 (= hard-reset trigger) fully resets CPU state including
// SP and the alternate registers, matching real /RESET semantics.
// Hard reset takes precedence over soft when both bits set.
func TestWireResetHardClearsRegisters(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	WireReset(disp, mem, cpu, nil)

	cpu.PC = 0x1234
	cpu.SP = 0x5678
	cpu.IFF1 = true

	disp.Select(0x02)
	disp.WriteData(0x02) // bit 1 = hard reset trigger

	if cpu.PC != 0 {
		t.Errorf("after NR$02=$02: PC = %#04x, want 0 (hard reset didn't fire)", cpu.PC)
	}
}

// spiResetSpy records ResetSPI calls so a reset test can assert the
// divMMC slave-select is deasserted on reset.
type spiResetSpy struct{ resets int }

func (s *spiResetSpy) ResetSPI() { s.resets++ }

// TestWireResetDeassertsDivMMCCS locks in that BOTH a soft and a hard
// NR$02 reset deassert the divMMC SPI chip-select (zxnext.vhd:3308:
// `if reset='1' then port_e7_reg <= (others => '1')`, reset = hard OR
// soft). NextZXOS soft-resets right after mounting the FAT32 card
// mid-CMD18; deasserting CS aborts that transaction so the post-reset
// SD re-init starts clean instead of spinning forever on stale stream
// bytes.
func TestWireResetDeassertsDivMMCCS(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	spy := &spiResetSpy{}
	WireReset(disp, mem, cpu, spy)

	disp.Select(0x02)
	disp.WriteData(0x01) // soft reset
	if spy.resets != 1 {
		t.Errorf("after soft reset: ResetSPI called %d times, want 1", spy.resets)
	}

	disp.Select(0x02)
	disp.WriteData(0x02) // hard reset
	if spy.resets != 2 {
		t.Errorf("after hard reset: ResetSPI called %d times, want 2", spy.resets)
	}
}

// TestWireResetSoftPreservesNRFile locks in the FPGA-faithful
// contract that a SOFT reset (NR$02 bit 0) preserves the entire
// NextReg file. On real hardware the 200+ NR `if reset = '1' then
// … default` blocks gate on the global `reset` signal
// (= `reset <= i_RESET`, zxnext.vhd:1730 — the CORE/hard reset),
// while nr_02_soft_reset is a separate output (o_RESET_SOFT,
// zxnext.vhd:1577) that resets only the CPU + classic paging.
// So NR$03/$04/$0A and the rest of the file must survive a soft
// reset unchanged. Wiping them caused the post-FAT32-mount
// infinite reset loop at bank-0 $6D0E.
func TestWireResetSoftPreservesNRFile(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	WireReset(disp, mem, cpu, nil)

	// Establish non-default OS-configured state across a spread of
	// registers the soft reset must NOT clobber.
	disp.Store(0x03, 0xB3) // machine type / config-mode exited
	disp.Store(0x04, 0x03) // RAMPAGE
	disp.Store(0x0A, 0x10) // divMMC automap config
	disp.Store(0x07, 0x03) // CPU speed
	disp.Store(0x50, 0x07) // MMU8 slot 0

	// Trigger a soft reset.
	disp.Select(0x02)
	disp.WriteData(0x01)

	// NR$02 latches sticky-soft.
	if got := disp.Raw(0x02); got&0x01 == 0 {
		t.Errorf("post-soft-reset NR$02 = %#x, want bit 0 set (sticky-soft)", got)
	}
	// Machine personality (NR$03) and general config survive — NR$03
	// is NOT in the FPGA reset block (zxnext.vhd), so it is preserved.
	for _, c := range []struct {
		reg  byte
		want byte
	}{
		{0x03, 0xB3}, {0x04, 0x03}, {0x0A, 0x10}, {0x07, 0x03}, {0x50, 0x07},
	} {
		if got := disp.Raw(c.reg); got != c.want {
			t.Errorf("soft reset clobbered NR$%02X = %#x, want %#x (NR$03/config must be preserved)", c.reg, got, c.want)
		}
	}
}

// TestWireResetSoftReArmsDivMMCEntryPoints locks in the FPGA-faithful
// behaviour that a soft reset re-arms the divMMC entry-point registers
// to their power-on defaults. Per zxnext.vhd:5087-5090 NR$B8/$B9/$BA/
// $BB are in the UNCONDITIONAL reset block, and the board asserts the
// core `reset` on a soft reset too (zxnext_top_issue5.vhd:880
// `reset <= reset_hard or reset_soft`). The load-bearing bit is
// NR$B8 bit 0 ($83): it re-traps the $0000 RST-0 automap entry so the
// post-reset divMMC/esxDOS RST $00 vector pages in the divMMC ROM
// instead of falling through to whatever ROM bank sits at $0000.
func TestWireResetSoftReArmsDivMMCEntryPoints(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	cpu := z80.New(mem, wireTestULA{})
	// Wire the divMMC entry-point handlers onto a real pager so the
	// reset's WriteReg fires through to it.
	pager := divmmc.New(make([]byte, divmmc.ROMSize))
	WireDivMMCEntryPoints(disp, pager)
	WireReset(disp, mem, cpu, nil)

	// NextZXOS clears NR$B8 bit 0 during operation (e.g. $82).
	disp.WriteReg(0xB8, 0x82)
	disp.Store(0x03, 0xB3) // personality must survive
	if pager.EntryPoints0() != 0x82 {
		t.Fatalf("setup: pager EP0 = %#x, want $82", pager.EntryPoints0())
	}

	disp.Select(0x02)
	disp.WriteData(0x01) // soft reset

	if got := disp.Raw(0xB8); got != 0x83 {
		t.Errorf("post-soft-reset NR$B8 = %#x, want $83 (re-armed)", got)
	}
	if got := pager.EntryPoints0(); got != 0x83 {
		t.Errorf("post-soft-reset pager EP0 = %#x, want $83 (OnWrite must fire)", got)
	}
	if got := disp.Raw(0x03); got != 0xB3 {
		t.Errorf("soft reset clobbered NR$03 = %#x, want $B3 (personality preserved)", got)
	}
}

// TestWireResetPromotesAltROMStagedNibble locks in zxnext.vhd:2255 —
// on the core `reset` signal (asserted for BOTH hard and soft reset,
// zxnext_top_issue5.vhd:880) the NR$8C Alt-ROM register promotes its
// staged low nibble into the active high nibble:
//
//	if reset = '1' then nr_8c_altrom(7 downto 4) <= nr_8c_altrom(3 downto 0);
//
// The low (staged) nibble is preserved. NextZXOS uses this to stage an
// after-reset Alt-ROM configuration before its one legitimate boot
// soft-reset ($6D31) — losing the promote leaves NR$8C's active nibble
// stale, which kills the (altrom_en AND alt_128_n) arm of the divMMC
// rom3-automap gate (zxnext.vhd:3138) and with it the menu-era $0038
// IM1 automap.
func TestWireResetPromotesAltROMStagedNibble(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger byte // NR$02 write value
		pre     byte // NR$8C before reset
		want    byte // NR$8C after reset
	}{
		{"soft promotes staged nibble", 0x01, 0x0C, 0xCC},
		{"soft replaces active nibble", 0x01, 0xC5, 0x55},
		{"hard promotes staged nibble", 0x02, 0x0C, 0xCC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
			if err != nil {
				t.Fatal(err)
			}
			disp := nextregs.New()
			cpu := z80.New(mem, wireTestULA{})
			WireAltROM(disp, mem)
			WireReset(disp, mem, cpu, nil)

			disp.Select(0x8C)
			disp.WriteData(tc.pre)
			disp.Select(0x02)
			disp.WriteData(tc.trigger)

			disp.Select(0x8C)
			if got := disp.ReadData(); got != tc.want {
				t.Errorf("NR$8C after reset = %#02x, want %#02x (zxnext.vhd:2255 nibble promote)", got, tc.want)
			}
			if got := mem.AltROMReg(); got != tc.want {
				t.Errorf("memory altROMReg after reset = %#02x, want %#02x", got, tc.want)
			}
		})
	}
}
