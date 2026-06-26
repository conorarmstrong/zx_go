package memory

import (
	"os"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Memory routing priority matrix tests (iter 188 — task #109).
//
// Verifies the documented priority ordering of read/write dispatch at
// $0000-$3FFF on ModelNext. Per `pkg/memory/memory.go::Read()` and
// `Write()` and `zxnext.vhd`'s priority cascade, the rules are:
//
// READ priority at $0000-$3FFF:
//
//   1. FPGA bootrom active → return bootrom byte (mirrored)
//   2. Alt-ROM read redirect (NR$8C bits 7=1, 6=0) → altROM buffer
//   3. Config-mode active → configModeReadByte (NR$04 RAMPAGE)
//   4. MMU8 slot overridden → m.ram[bank] (or m.rom for $FF)
//   5. Classic dispatch → m.rom[active ROM bank]
//
// WRITE priority at $0000-$3FFF:
//
//   1. FPGA bootrom active → drop (read-only)
//   2. Alt-ROM write redirect (NR$8C bits 7=1, 6=1) → altROM buffer
//   3. Config-mode active → configModeWriteByte (NR$04 RAMPAGE)
//   4. MMU8 slot overridden + bank != $FF → m.ram[bank]
//   5. MMU8 slot overridden + bank == $FF → drop (ROM half)
//   6. Classic dispatch → drop (ROM area)
//
// Each test asserts the priority by configuring multiple overlapping
// flags and verifying which one wins.

// routingTestSetup creates a fresh Memory in ModelNext + a known
// pattern in each routing destination so we can identify which one
// was hit on a read.
//
// Sentinel byte values per destination:
//
//	$A1 = FPGA bootrom byte 0
//	$A2 = altROM0 byte 0
//	$A3 = altROM1 byte 0
//	$A4 = m.rom[0] byte 0  (= ROM bank 0 / RAMPAGE_ROMSPECCY=0)
//	$A5 = m.rom[1] byte 0
//	$A6 = m.ram[5] byte 0  (= MMU8 bank value 10 = bank16k 5)
//	$A7 = m.ram[7] byte 0  (= MMU8 bank value 14 = bank16k 7)
func routingTestSetup(t *testing.T) *Memory {
	t.Helper()
	mem := newRoutingTestMemory(t)

	mem.fpgaBootROM = []byte{0xA1}
	mem.altROM0[0] = 0xA2
	mem.altROM1[0] = 0xA3
	mem.rom[0][0] = 0xA4
	mem.rom[1][0] = 0xA5
	if mem.ram[5] != nil {
		mem.ram[5][0] = 0xA6
	}
	if mem.ram[7] != nil {
		mem.ram[7][0] = 0xA7
	}
	return mem
}

func newRoutingTestMemory(t *testing.T) *Memory {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"48.rom", "128-0.rom", "128-1.rom"} {
		if err := writeBlank(dir+"/"+name, PageSize); err != nil {
			t.Fatal(err)
		}
	}
	mem, err := New(dir, roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	// Default clean state — clear any per-test transient flags.
	// NOTE: leave MMU8 overrides at false (= legacy classic
	// dispatch). Calling SetMMU(slot, $FF) would set the override
	// flag and route through the MMU8 path with the "ROM half" sub-
	// branch (consulting PeripheralRead) — that's the OPPOSITE of
	// "passthrough" despite the surface-similarity of $FF.
	mem.ClearFPGABootROM()
	mem.ClearConfigMode()
	mem.SetAltROMReg(0x00)
	for slot := range mem.mmuOverride {
		mem.mmuOverride[slot] = false
	}
	return mem
}

func writeBlank(path string, size int) error {
	return os.WriteFile(path, make([]byte, size), 0644)
}

// TestRouting_FPGABootRomBeatsAll verifies that when fpgaBootROMActive
// is true, $0000-$3FFF reads come from the bootrom regardless of
// every other overlay. Per VHDL the bootrom mask is the highest-
// priority read source while it's active.
func TestRouting_FPGABootRomBeatsAll(t *testing.T) {
	mem := routingTestSetup(t)
	mem.fpgaBootROMActive = true
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x10) // would route to m.ram[0]
	mem.SetAltROMReg(0x80)         // would redirect reads to altROM
	mem.SetMMU(0, 0x0A)            // would override slot 0 to bank 5

	if got := mem.Read(0x0000); got != 0xA1 {
		t.Errorf("read at $0000 with bootrom active: $%02X, want $A1 (bootrom)", got)
	}
}

// TestRouting_FPGABootRom_WriteReachesConfigPage pins the VHDL contract:
// bootrom_en gates ONLY the read mux (zxnext.vhd:1856); the SRAM write path
// (sram_req, 3154) is bootrom-independent. At power-on bootrom_en AND
// config_mode are both '1' (1101/1102), so a $0000-$3FFF write MUST land in
// the config-mode RAMPAGE target — not be dropped. boot.bin streams its ROM
// images (and $2000-$3FFF divMMC-window content) through here while the
// bootrom is still active; dropping these writes left the under-populated
// NOP-slide that stalled the cold boot at $2401. Found by the VHDL
// conformance audit (memory-paging axis, Rank 1).
func TestRouting_FPGABootRom_WriteReachesConfigPage(t *testing.T) {
	mem := routingTestSetup(t)
	mem.fpgaBootROMActive = true
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x10) // routes to m.ram[0]
	mem.ram[0][0] = 0xBB

	mem.Write(0x0000, 0x99)

	if mem.ram[0][0] != 0x99 {
		t.Errorf("bootrom-active config-mode write dropped: ram[0][0]=$%02X, want $99 "+
			"(bootrom_en gates reads only; writes reach the config page)", mem.ram[0][0])
	}
}

// TestRouting_FPGABootRom_NonConfigRomWriteIgnored: with bootrom active and
// config-mode NOT active, a write to a ROM-covered $0000-$3FFF slot is still
// ignored (it targets ROM, not RAM) — matching the write mux's ROM no-op.
func TestRouting_FPGABootRom_NonConfigRomWriteIgnored(t *testing.T) {
	mem := routingTestSetup(t)
	mem.fpgaBootROMActive = true
	// config-mode OFF, no MMU override → $0000 covers a classic ROM page.
	mem.rom[0][0] = 0xDD
	mem.Write(0x0000, 0x99)
	if mem.rom[0][0] != 0xDD {
		t.Errorf("ROM write leaked: rom[0][0]=$%02X, want $DD (ROM is read-only)", mem.rom[0][0])
	}
}

// TestRouting_DivMMC_Read_BeatsConfigAndMMU pins the VHDL divMMC-first READ
// priority at $0000-$3FFF: zxnext.vhd's final mux tests divMMC (3084/3092)
// with override(2)='1' in BOTH the MMU-RAM (3043) and config-mode (3050)
// branches, so a paged-in divMMC overlay wins over config-mode AND an
// MMU8-mapped RAM bank for reads. Previously our Read consulted divMMC only
// for ROM-mapped ($FF) slots, so a $2000-$3FFF read under an MMU RAM override
// (or config-mode) bypassed the divMMC RAM bank → the cold boot fetched
// MMU-RAM zeros and NOP-slid at $2401. (VHDL conformance audit, Rank 3.)
func TestRouting_DivMMC_Read_BeatsConfigAndMMU(t *testing.T) {
	mem := routingTestSetup(t)
	// divMMC overlay paged in: claims $2000-$3FFF, returns sentinel $D0.
	mem.PeripheralRead = func(a uint16) (byte, bool) {
		if a >= 0x2000 && a < 0x4000 {
			return 0xD0, true
		}
		return 0, false
	}
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x10) // config page would otherwise serve $2000
	mem.SetMMU(1, 0x0A)            // slot1 ($2000-$3FFF) → RAM bank 5
	mem.ram[5][0] = 0xA6

	if got := mem.Read(0x2000); got != 0xD0 {
		t.Errorf("Read($2000) = $%02X, want $D0 (divMMC overlay must beat config + MMU8-RAM on read)", got)
	}
}

// TestRouting_AltROMRead_BeatsConfig verifies the altROM read redirect
// (NR$8C bit 7=1, 6=0) beats config-mode for reads of a ROM-region slot
// (no MMU override) — altROM is a ROM-region overlay above config-mode.
func TestRouting_AltROMRead_BeatsConfig(t *testing.T) {
	mem := routingTestSetup(t)
	// altROM read redirect = NR$8C = 1000_0000 (bit 7=1, bit 6=0).
	// bits 5:4 control which alt bank; default 0 selects altROM0
	// when port 7FFD bit 4 = 0 (= ROM bank 0).
	mem.SetAltROMReg(0x80)
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x10) // would route to m.ram[0]
	// Slot 0 left as ROM (no MMU override) → altROM redirect applies.

	if got := mem.Read(0x0000); got != 0xA2 {
		t.Errorf("read at $0000 with alt-ROM active (slot=ROM): $%02X, want $A2 (altROM0)", got)
	}
}

// TestRouting_MMURAM_BeatsAltROMRead locks in that an MMU8-mapped RAM bank
// at $0000-$3FFF outranks the Alt-ROM READ redirect. On the FPGA
// sram_altrom_en is gated by sram_pre_override(slot) (zxnext.vhd:3037-3043 /
// 3138), which is 0 once the slot maps a real RAM bank — the same term
// TestDivMMCRom3GateSuppressedByMMURAM verifies. This is the NextBASIC-
// Invaders 590 fix: NextZXOS reads its bank-8 sprite-attribute cache via
// NR$51=16 while the reveal ISR ($007x) has the Alt-ROM read redirect armed;
// the read must serve the RAM bank (the cache), not Alt-ROM bytes (whose
// stray X[8] made the missile X out of range -> "Integer out of range" 590).
func TestRouting_MMURAM_BeatsAltROMRead(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetAltROMReg(0x80) // Alt-ROM read redirect armed (bit7=1, bit6=0)
	mem.SetMMU(0, 0x0A)    // map RAM bank 5 into slot 0 ($0000-$1FFF)
	mem.ram[5][0] = 0xA6   // sentinel distinct from altROM0 ($A2)

	if got := mem.Read(0x0000); got != 0xA6 {
		t.Errorf("read at $0000 with MMU RAM bank 5 + Alt-ROM read redirect: $%02X, want $A6 (RAM bank beats altROM; sram_pre_override)", got)
	}
	// With the slot returned to ROM the Alt-ROM redirect serves the read again.
	mem.SetMMU(0, 0xFF)
	if got := mem.Read(0x0000); got != 0xA2 {
		t.Errorf("read at $0000 with slot=ROM: $%02X, want $A2 (altROM redirect applies when slot is ROM)", got)
	}
}

// TestRouting_AltROMWriteRedirect_LandsInAltROM verifies that when
// NR$8C bits 7=1, 6=1, writes to $0000-$3FFF land in altROM (instead
// of being dropped). Reads with this combination DON'T redirect (bit
// 6 set means writes-only).
func TestRouting_AltROMWriteRedirect_LandsInAltROM(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetAltROMReg(0xC0) // bit 7=1, bit 6=1 — write-redirect mode

	mem.altROM0[0] = 0x00 // clear sentinel — we want to see the new write
	mem.Write(0x0000, 0x99)

	if mem.altROM0[0] != 0x99 {
		t.Errorf("altROM write redirect: altROM0[0]=$%02X, want $99",
			mem.altROM0[0])
	}
}

// TestRouting_AltROMWriteRedirect_ReadsPassThrough verifies that when
// NR$8C bits 7=1, 6=1 (write-redirect), reads at $0000-$3FFF go
// through the normal ROM/RAM dispatch — they do NOT come from altROM.
func TestRouting_AltROMWriteRedirect_ReadsPassThrough(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetAltROMReg(0xC0)
	// Sentinel: altROM0 has $A2; classic ROM bank 0 has $A4.
	// In write-redirect mode, the read should NOT come from altROM,
	// so it must return $A4 from the normal ROM path.
	if got := mem.Read(0x0000); got != 0xA4 {
		t.Errorf("alt-ROM write-redirect mode read: $%02X, want $A4 (rom[0])",
			got)
	}
}

// TestRouting_ConfigMode_BeatsMMUAndClassic verifies that
// configModeActive routes reads through configModeReadByte
// (= m.rom[NR$04] when RAMPAGE is in $00-$03 range, or m.ram[N] when
// $10-$7F) regardless of MMU8 / classic state.
func TestRouting_ConfigMode_BeatsMMUAndClassic(t *testing.T) {
	mem := routingTestSetup(t)
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x01) // RAMPAGE = 1 → m.rom[1]
	mem.SetMMU(0, 0x0A)            // would route to bank 5

	if got := mem.Read(0x0000); got != 0xA5 {
		t.Errorf("config-mode RAMPAGE=$01 read at $0000: $%02X, want $A5 (rom[1])",
			got)
	}
}

// TestRouting_ConfigMode_WriteLandsInRAMPAGE_Target verifies that
// when config-mode is active, $0000-$3FFF writes go through
// configModeWriteByte to the RAMPAGE-selected backing.
func TestRouting_ConfigMode_WriteLandsInRAMPAGE_Target(t *testing.T) {
	mem := routingTestSetup(t)
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x02) // RAMPAGE = 2 → m.rom[2]
	mem.rom[2][0] = 0x00
	mem.Write(0x0000, 0x99)

	if mem.rom[2][0] != 0x99 {
		t.Errorf("config-mode write at $0000 with RAMPAGE=$02: m.rom[2][0]=$%02X, want $99",
			mem.rom[2][0])
	}
}

// TestRouting_MMU8_BeatsClassic verifies that an MMU8 slot override
// (bank != $FF) wins over classic ROM dispatch at $0000.
func TestRouting_MMU8_BeatsClassic(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetMMU(0, 0x0A) // bank8k = $0A → bank16k = 5

	if got := mem.Read(0x0000); got != 0xA6 {
		t.Errorf("MMU8 slot 0 = $0A read at $0000: $%02X, want $A6 (ram[5])",
			got)
	}
}

// TestRouting_MMU8_PassthroughOnFF verifies that an MMU8 slot value
// of $FF means "use legacy paging" — i.e. read comes from the
// classic ROM dispatch (= rom[active ROM bank]).
func TestRouting_MMU8_PassthroughOnFF(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetMMU(0, 0xFF) // $FF = legacy passthrough

	if got := mem.Read(0x0000); got != 0xA4 {
		t.Errorf("MMU8 slot 0 = $FF read at $0000: $%02X, want $A4 (rom[0])",
			got)
	}
}

// TestRouting_ClassicDropsROMWrite verifies that with no overlays
// active (no bootrom, no altROM, no configMode, MMU8 = $FF), writes
// to $0000-$3FFF are dropped (ROM is read-only in classic mode).
func TestRouting_ClassicDropsROMWrite(t *testing.T) {
	mem := routingTestSetup(t)
	mem.rom[0][0] = 0x55
	mem.Write(0x0000, 0x99)
	if mem.rom[0][0] != 0x55 {
		t.Errorf("classic write at $0000 (ROM): rom[0][0]=$%02X, want $55 (dropped)",
			mem.rom[0][0])
	}
}

// TestRouting_MMU8FFDropsWrite verifies that an MMU8-overridden slot
// with bank value $FF drops writes (= it means "ROM half").
func TestRouting_MMU8FFDropsWrite(t *testing.T) {
	mem := routingTestSetup(t)
	// Force MMU8 override on slot 0 by writing a non-default value
	// first, then change to $FF. (Setting $FF on a never-overridden
	// slot may or may not set the override flag — depends on impl.)
	mem.SetMMU(0, 0x0A)
	mem.SetMMU(0, 0xFF)
	mem.rom[0][0] = 0x55
	mem.Write(0x0000, 0x99)
	if mem.rom[0][0] != 0x55 {
		t.Errorf("MMU8 slot 0 = $FF write at $0000: rom[0][0]=$%02X, want $55 (dropped)",
			mem.rom[0][0])
	}
}

// TestRouting_ConfigModeOverridesAltROM verifies the priority: when
// BOTH altROM read-redirect (bit 7=1, 6=0) AND config-mode are active,
// the **altROM redirect WINS** per the Write/Read priority in our
// memory.go (line 798-802 for reads). This is a spec-cross-check:
// per zxnext.vhd, the altROM read priority is HIGHER than config-mode.
func TestRouting_AltROMReadPriority_OverConfigMode(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetAltROMReg(0x80) // altROM read redirect
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x10) // would route to m.ram[0]
	if got := mem.Read(0x0000); got != 0xA2 {
		t.Errorf("altROM + config-mode read at $0000: $%02X, want $A2 (altROM wins)",
			got)
	}
}

// TestRouting_AltROMSelect_Follows7FFDBit4 verifies that when neither
// $8C bit 5 nor bit 4 (lock bits) is set, altROM follows port 7FFD
// bit 4 — selecting altROM1 when 7FFD bit 4 = 1, altROM0 otherwise.
func TestRouting_AltROMSelect_Follows7FFDBit4(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetAltROMReg(0x80) // bit 7 alone — no lock bits

	mem.port7FFD = 0x00 // bit 4 = 0 → altROM0
	if got := mem.Read(0x0000); got != 0xA2 {
		t.Errorf("altROM with 7FFD bit 4 = 0: $%02X, want $A2 (altROM0)", got)
	}

	mem.port7FFD = 0x10 // bit 4 = 1 → altROM1
	if got := mem.Read(0x0000); got != 0xA3 {
		t.Errorf("altROM with 7FFD bit 4 = 1: $%02X, want $A3 (altROM1)", got)
	}
}

// TestRouting_AltROMSelect_LockBit5_Forces48K verifies that when
// NR$8C bit 5 is set, altROM1 (48K-style) is selected regardless of
// 7FFD bit 4.
func TestRouting_AltROMSelect_LockBit5_Forces48K(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetAltROMReg(0xA0) // bit 7 + bit 5 — lock to altROM1
	mem.port7FFD = 0x00    // bit 4 = 0 — would normally select altROM0
	if got := mem.Read(0x0000); got != 0xA3 {
		t.Errorf("altROM lock-bit-5 + 7FFD bit 4 = 0: $%02X, want $A3 (altROM1 forced)",
			got)
	}
}

// TestRouting_AltROMSelect_LockBit4_Forces128K verifies that when
// NR$8C bit 4 is set (and bit 5 isn't), altROM0 (128K-style) is
// selected regardless of 7FFD bit 4.
func TestRouting_AltROMSelect_LockBit4_Forces128K(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetAltROMReg(0x90) // bit 7 + bit 4 — lock to altROM0
	mem.port7FFD = 0x10    // bit 4 = 1 — would normally select altROM1
	if got := mem.Read(0x0000); got != 0xA2 {
		t.Errorf("altROM lock-bit-4 + 7FFD bit 4 = 1: $%02X, want $A2 (altROM0 forced)",
			got)
	}
}

// peripheralStub simulates a peripheral (e.g. divMMC) that
// intercepts reads/writes when its `handled` flag is true.
type peripheralStub struct {
	handleRead  bool
	handleWrite bool
	lastReadAt  uint16
	lastWriteAt uint16
	lastWriteV  byte
	returnVal   byte
}

func (p *peripheralStub) Read(addr uint16) (byte, bool) {
	if !p.handleRead {
		return 0, false
	}
	p.lastReadAt = addr
	return p.returnVal, true
}

func (p *peripheralStub) Write(addr uint16, val byte) bool {
	if !p.handleWrite {
		return false
	}
	p.lastWriteAt = addr
	p.lastWriteV = val
	return true
}

// TestRouting_PeripheralReadConsulted_InMMU8FF verifies that when
// MMU8 slot 0 is overridden with bank $FF (= "ROM half"), the
// PeripheralRead hook is consulted BEFORE the classic ROM dispatch.
// This is the divMMC integration point: divMMC sets the MMU8
// slot to $FF then registers itself as the peripheral.
func TestRouting_PeripheralReadConsulted_InMMU8FF(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleRead: true, returnVal: 0xCC}
	mem.PeripheralRead = stub.Read
	mem.SetMMU(0, 0xFF) // bank $FF = "ROM half" path

	if got := mem.Read(0x0000); got != 0xCC {
		t.Errorf("MMU8=$FF + peripheral handled: $%02X, want $CC (peripheral wins)",
			got)
	}
	if stub.lastReadAt != 0x0000 {
		t.Errorf("peripheral.Read called at $%04X, want $0000", stub.lastReadAt)
	}
}

// TestRouting_PeripheralWriteConsulted_InModelNext verifies that
// the PeripheralWrite hook gets consulted on writes to $0000-$3FFF
// when no higher-priority overlay (bootrom / alt-ROM / config-mode)
// claims the write.
func TestRouting_PeripheralWriteConsulted_InModelNext(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleWrite: true}
	mem.PeripheralWrite = stub.Write

	mem.Write(0x0000, 0x99)

	if stub.lastWriteAt != 0x0000 || stub.lastWriteV != 0x99 {
		t.Errorf("PeripheralWrite not consulted: lastWriteAt=$%04X val=$%02X",
			stub.lastWriteAt, stub.lastWriteV)
	}
}

// TestRouting_ConfigModeBeatsPeripheralWrite verifies that config-
// mode writes route via configModeWriteByte INSTEAD of the
// PeripheralWrite hook. Documents the current priority where
// config-mode wins (= what we observed during the iter 180 cold-
// boot investigation).
func TestRouting_DivMMCBeatsConfigModeWrite(t *testing.T) {
	// Per zxnext.vhd's final memory mux (3084-3130) the divMMC override
	// is tested FIRST — sram_pre_override(2)='1' is set in the
	// config-mode branch (3050) too, so a PAGED-IN overlay (a claiming
	// PeripheralWrite) beats config-mode for writes exactly as the read
	// path (with the same citation) already encodes. The previous
	// expectation (config-mode first) was unfaithful and hid the
	// stolen-seek-write bug (the development log).
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleWrite: true}
	mem.PeripheralWrite = stub.Write
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x02) // RAMPAGE = m.rom[2]

	mem.Write(0x0000, 0x99)

	if stub.lastWriteAt != 0x0000 || stub.lastWriteV != 0x99 {
		t.Errorf("claiming PeripheralWrite (paged-in overlay) must win: lastWriteAt=$%04X val=$%02X",
			stub.lastWriteAt, stub.lastWriteV)
	}
	if mem.rom[2][0] == 0x99 {
		t.Errorf("config-mode page received a write the overlay claimed")
	}
}

func TestRouting_ConfigModeGetsDeclinedWrites(t *testing.T) {
	// When the overlay is NOT paged in (PeripheralWrite declines), the
	// config-mode window receives the write as before.
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleWrite: false}
	mem.PeripheralWrite = stub.Write
	mem.EnterConfigMode()
	mem.SetConfigModeRAMPage(0x02)

	mem.Write(0x0000, 0x99)

	if mem.rom[2][0] != 0x99 {
		t.Errorf("config-mode write: m.rom[2][0]=$%02X, want $99", mem.rom[2][0])
	}
}

// TestRouting_FPGABootRomBeatsPeripheralRead verifies that the
// FPGA bootrom mask returns its byte BEFORE consulting the
// peripheral hook for reads.
func TestRouting_FPGABootRomBeatsPeripheralRead(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleRead: true, returnVal: 0xCC}
	mem.PeripheralRead = stub.Read
	mem.fpgaBootROMActive = true

	if got := mem.Read(0x0000); got != 0xA1 {
		t.Errorf("bootrom + peripheral: $%02X, want $A1 (bootrom wins)", got)
	}
	if stub.lastReadAt != 0 {
		t.Errorf("PeripheralRead consulted while bootrom active: lastReadAt=$%04X",
			stub.lastReadAt)
	}
}

// TestRouting_AddressRange_PeripheralOnlyBelow4000 verifies that
// the PeripheralRead hook is NOT consulted for addresses ≥ $4000
// (= peripheral integration is limited to the ROM area on Next).
func TestRouting_AddressRange_PeripheralOnlyBelow4000(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleRead: true, returnVal: 0xCC}
	mem.PeripheralRead = stub.Read

	// Read at $4000 — should NOT consult peripheral.
	stub.lastReadAt = 0xFFFF // sentinel
	_ = mem.Read(0x4000)
	if stub.lastReadAt != 0xFFFF {
		t.Errorf("PeripheralRead consulted at $4000: lastReadAt=$%04X (should be untouched)",
			stub.lastReadAt)
	}
}

// =========================================================================
// divMMC × MMU8 cross-product tests (iter 219).
// =========================================================================

// TestRouting_DivMMCPaged_BeatsMMU8RAM pins the VHDL-correct priority: a
// paged-in divMMC overlay beats a non-$FF MMU8 RAM bank at $0000-$3FFF on
// READ, mirroring the write path. zxnext.vhd's final mux tests divMMC first
// (3084/3092) with override(2)='1' in the MMU-RAM branch (3043). Previously
// our Read consulted divMMC only for $FF (ROM-half) slots, so a real RAM bank
// in slot 0/1 bypassed divMMC — the inverted precedence behind the $2401
// cold-boot NOP-slide (VHDL conformance audit, Rank 3). (Was
// TestRouting_DivMMCPaged_MMU8RAM_MMUWins, which enshrined the bug.)
func TestRouting_DivMMCPaged_BeatsMMU8RAM(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleRead: true, returnVal: 0xCC}
	mem.PeripheralRead = stub.Read
	mem.SetMMU(0, 0x0A) // bank 5 (= $A6) — non-$FF RAM bank in slot 0

	if got := mem.Read(0x0000); got != 0xCC {
		t.Errorf("divMMC overlay vs MMU8 RAM bank: $%02X, want $CC (divMMC wins per VHDL 3084/3092)",
			got)
	}
	if stub.lastReadAt != 0x0000 {
		t.Errorf("divMMC peripheral not consulted at $0000: lastReadAt=$%04X", stub.lastReadAt)
	}
}

// TestRouting_DivMMCPaged_MMU8FF_PeripheralWins verifies that when
// divMMC pages in (sets its MMU8 slots to $FF), the PeripheralRead
// hook IS consulted instead of the classic ROM path.
func TestRouting_DivMMCPaged_MMU8FF_PeripheralWins(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleRead: true, returnVal: 0xCC}
	mem.PeripheralRead = stub.Read
	mem.SetMMU(0, 0xFF) // ROM half — peripheral consulted

	if got := mem.Read(0x0000); got != 0xCC {
		t.Errorf("MMU8 $FF + peripheral handled: $%02X, want $CC (peripheral wins)",
			got)
	}
	if stub.lastReadAt != 0x0000 {
		t.Errorf("peripheral not consulted at $0000: lastReadAt=$%04X",
			stub.lastReadAt)
	}
}

// TestRouting_DivMMCPaged_MMU8FF_NoPeripheral_FallsToROM verifies the
// $FF MMU8 path falls through to the classic ROM dispatch when the
// peripheral declines.
func TestRouting_DivMMCPaged_MMU8FF_NoPeripheral_FallsToROM(t *testing.T) {
	mem := routingTestSetup(t)
	// No PeripheralRead handler installed.
	mem.SetMMU(0, 0xFF)

	if got := mem.Read(0x0000); got != 0xA4 {
		t.Errorf("MMU8 $FF, no peripheral: $%02X, want $A4 (rom[0])", got)
	}
}

// TestRouting_DivMMCPaged_Slot1_PeripheralAtRSTAddr verifies the
// $2000-$3FFF half (slot 2/3 8K) also routes via peripheral when
// MMU8 set to $FF. The divMMC overlay at $2000 hosts RAM bank
// content during paged-in mode.
func TestRouting_DivMMCPaged_Slot1_PeripheralAtRSTAddr(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleRead: true, returnVal: 0xDD}
	mem.PeripheralRead = stub.Read
	mem.SetMMU(2, 0xFF) // slot 2 = $2000-$3FFF half

	if got := mem.Read(0x2008); got != 0xDD {
		t.Errorf("MMU8 slot 2 $FF, peripheral at $2008: $%02X, want $DD",
			got)
	}
	if stub.lastReadAt != 0x2008 {
		t.Errorf("peripheral lastReadAt=$%04X, want $2008", stub.lastReadAt)
	}
}

// TestRouting_DivMMCWrite_MMU8FF_DroppedByDefault verifies that
// writes to a slot with MMU8 = $FF and no PeripheralWrite handler
// are silently dropped (the ROM-half path is read-only).
func TestRouting_DivMMCWrite_MMU8FF_DroppedByDefault(t *testing.T) {
	mem := routingTestSetup(t)
	mem.SetMMU(0, 0xFF)
	mem.rom[0][0] = 0xA4
	mem.Write(0x0000, 0x99)
	if mem.rom[0][0] != 0xA4 {
		t.Errorf("MMU8 $FF write leaked into rom[0][0]: $%02X, want $A4 (write dropped)",
			mem.rom[0][0])
	}
}

// TestRouting_DivMMCWrite_MMU8FF_PeripheralReceives verifies that a
// PeripheralWrite handler does claim writes in the MMU8-$FF path.
func TestRouting_DivMMCWrite_MMU8FF_PeripheralReceives(t *testing.T) {
	mem := routingTestSetup(t)
	stub := &peripheralStub{handleWrite: true}
	mem.PeripheralWrite = stub.Write
	mem.SetMMU(0, 0xFF)

	mem.Write(0x0010, 0xEE)

	if stub.lastWriteAt != 0x0010 || stub.lastWriteV != 0xEE {
		t.Errorf("MMU8 $FF write: peripheral got lastWriteAt=$%04X val=$%02X, want $0010 $EE",
			stub.lastWriteAt, stub.lastWriteV)
	}
}

// TestWriteDivMMCWindowBeatsMMUMappedSlot — the FPGA's final SRAM
// mux (zxnext.vhd:3084-3092) tests `sram_pre_override(2) = '1' and
// divmmc_ram_en = '1'` BEFORE any other mapping, and the MMU arm
// (vhd:3038-3044) sets override = "110" — bit 2 ('1') marks the
// slot as divmmc-overridable. So while the divMMC overlay is paged
// in, bottom-16K writes land in divMMC RAM even when NR$50/$51 map
// a main-RAM bank there. (An earlier reading inverted this and
// broke boot — the development log.)
func TestWriteDivMMCWindowBeatsMMUMappedSlot(t *testing.T) {
	m := newRoutingTestMemory(t)
	claimed := 0
	m.PeripheralWrite = func(addr uint16, val byte) bool {
		claimed++
		return true // paged-in overlay claims the window
	}

	// MMU slot 1 ($2000-$3FFF) → 8K bank 32 (16K bank 16 low half).
	m.SetMMU(1, 32)
	m.Write(0x2005, 0xAB)

	if claimed != 1 {
		t.Errorf("peripheral claims = %d, want 1 (divmmc beats MMU per the final mux)", claimed)
	}
	if got := m.ram[16][0x0005]; got == 0xAB {
		t.Error("bank16[0005] must NOT receive the write while the overlay claims it")
	}
}
