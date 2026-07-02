package memory

import (
	"errors"
	"fmt"
	"os"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// setupNext brings up the ZX Spectrum Next memory layout.
//
// The Spectrum Next is hardware-compatible with the 128K Spectrum
// and exposes a "Machine Type" personality switch (NextReg $03)
// that lets it boot as a 48K, 128K, +2A/+3, or Next.
//
// Bank 0 starts as the 48K BASIC ROM and bank 1 as the 128K editor
// ROM, so the CPU resets straight into the classic "© 1982 Sinclair
// Research Ltd" prompt with all Next hardware (Z80N opcodes,
// NextRegs, MMU, Layer 2, sprites, Copper, zxnDMA, DAC, RTC) wired
// and available from BASIC. If the user has installed the NextZXOS
// distro ROM (`enNextZX.rom`), its four 16K banks overwrite
// m.rom[0..3] below so the real NextZXOS shell boots instead; this
// is gated purely on the file's presence (ZX_GO_SKIP_DISTRO_PRELOAD=1
// disables the overlay even when the file is present).
// SetFPGABootROM separately arms the FPGA loader ROM at $0000, which
// reads TBBLUE.FW from the SD card instead of using either embedded
// image.
//
// NextReg $8E and the legacy 7FFD/1FFD port writes swap among
// whichever four 16K banks ended up in m.rom[0..3] regardless of
// which of the above populated them.
func (m *Memory) setupNext() error {
	// Load the 128K editor / 48K BASIC ROMs through the ROM manager.
	// These are the canonical fallback ROMs; software that does
	// `NEXTREG $8E,$01` to select the 128K editor or `$03` for the
	// 48K-compatible bank reads from these images.
	if err := m.romManager.LoadROM(roms.ROM128K_0, "128-0.rom"); err != nil {
		return fmt.Errorf("ZX Spectrum Next: load 128K ROM 0: %w", err)
	}
	if err := m.romManager.LoadROM(roms.ROM128K_1, "128-1.rom"); err != nil {
		return fmt.Errorf("ZX Spectrum Next: load 128K ROM 1: %w", err)
	}
	if err := m.romManager.LoadROM(roms.ROM48K, "48.rom"); err != nil {
		return fmt.Errorf("ZX Spectrum Next: load 48K BASIC ROM: %w", err)
	}
	rom0, _ := m.romManager.GetROM(roms.ROM128K_0)
	rom48, _ := m.romManager.GetROM(roms.ROM48K)

	// Bank 0 = 48K BASIC ROM, bank 1 = 128K editor ROM: the CPU resets
	// straight into the classic "© 1982 Sinclair Research Ltd" prompt.
	// If the NextZXOS distro ROM is installed, the load below
	// overwrites all four banks with it.
	copy(m.rom[0], rom48)
	copy(m.rom[1], rom0) // 128K editor — page 1
	// Bank 2 / bank 3 stay zero-filled until the distro load (if any)
	// populates them.

	// NextZXOS distro ROM overlay: if `roms/next/enNextZX.rom` is
	// installed, populate m.rom[0..3] with it so the CPU boots the
	// real NextZXOS shell instead of the embedded 128K BASIC ROMs.
	// This is a scaffold needed because this emulator treats m.rom[]
	// (legacy ROM banks 0-3) and m.ram[] (MMU8-paged main RAM) as
	// separate arrays, whereas real Spectrum Next hardware has them
	// in a single SRAM: the FPGA bootrom's INIR writes that load
	// enNextZX.rom from SD land only in m.ram[], so without this
	// overlay the post-soft-reset switch to legacy ROM-bank mapping
	// would read from an empty m.rom[].
	//
	// Gated purely on file presence (the standard install action
	// puts enNextZX.rom there); ZX_GO_SKIP_DISTRO_PRELOAD=1 disables it.
	if os.Getenv("ZX_GO_SKIP_DISTRO_PRELOAD") == "" {
		if distro, err := install.LoadROM(install.DistroROM); err == nil {
			if len(distro) >= 4*PageSize {
				copy(m.rom[0], distro[0:PageSize])
				copy(m.rom[1], distro[PageSize:2*PageSize])
				copy(m.rom[2], distro[2*PageSize:3*PageSize])
				copy(m.rom[3], distro[3*PageSize:4*PageSize])
			} else if len(distro) >= 2*PageSize {
				copy(m.rom[0], distro[0:PageSize])
				copy(m.rom[1], distro[PageSize:2*PageSize])
			}
		} else if !errors.Is(err, install.ErrROMNotInstalled) {
			return fmt.Errorf("ZX Spectrum Next: distro ROM load: %w", err)
		}
	}

	// 128K-style page map: ROM 0 at 0x0000, RAM 5 at 0x4000,
	// RAM 2 at 0x8000, RAM 0 at 0xC000. NextReg $8E and the
	// legacy 7FFD/1FFD port writes swap among ROM banks 16..19.
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0}
	m.PagingEnabled = true
	m.ScreenPage = 5

	// Reboot path: if the FPGA bootrom image is still resident from
	// a prior boot, re-activate it so the next cold start runs the
	// loader again. Real hardware re-enters bootrom mode on every
	// power-cycle / hard reset; without this, Reboot from the menu
	// would leave the bootrom inactive and the CPU would drop into
	// the 48K BASIC ROM with the Next NextRegs (tilemap, etc.) still
	// configured from the previous session — manifesting as bank-5
	// tilemap data corrupted by BASIC's screen writes.
	if len(m.fpgaBootROM) > 0 {
		m.fpgaBootROMActive = true
	}

	// Per zxnext.vhd:1102 `signal nr_03_config_mode : std_logic := '1'`
	// the FPGA powers on with config_mode = 1. boot.bin relies on
	// this: when it writes NR$04=$00..$03 to map a ROM bank into
	// the config window then streams 16K of writes through the CPU
	// $0000-$3FFF area, those writes only land in m.rom[N] if config
	// mode is already active when the first NR$04 write fires.
	//
	// We only activate it when the FPGA bootrom is also armed — the
	// fast-boot path (no bootrom, jump straight into 48K BASIC) is
	// not a faithful power-on cycle and the classic-ROM tests expect
	// writes to $0000-$3FFF to be dropped by the read-only ROM map.
	// On real hardware boot.bin's first NR$03 write exits config mode
	// before classic software gets a chance to write to $0000-$3FFF,
	// so this distinction is invisible to guests.
	if m.fpgaBootROMActive {
		m.configModeActive = true
	}
	return nil
}

// SetROMBank changes which of the four 16K ROM banks is visible
// at 0x0000-0x3FFF. Called from the NextReg 0x8E handler. Writes
// through to port7FFD bit 4 (low bit of ROM index) and port1FFD
// bit 2 (high bit) so selectROM remains the single source of
// truth and classic-port readers see consistent state.
func (m *Memory) SetROMBank(bank int) {
	if bank < 0 || bank > 3 {
		return
	}
	// Low bit of bank → port7FFD bit 4.
	if bank&0x01 != 0 {
		m.port7FFD |= 0x10
	} else {
		m.port7FFD &^= 0x10
	}
	// High bit of bank → port1FFD bit 2.
	if bank&0x02 != 0 {
		m.port1FFD |= 0x04
	} else {
		m.port1FFD &^= 0x04
	}
	m.selectROM("NEXTREG_8E")
}

// SetROMBankExtended honours the full byte written to NextReg $8E
// per the FPGA paging logic. Bits 1:0 select ROM bank as before
// (delegated to SetROMBank, which writes through to port_7FFD bit 4
// + port_1FFD bit 2 so selectROM stays canonical). When bit 3 of
// the value is set, the write ALSO updates the classic-paging
// shadows: bits 6:4 → port_7FFD[2:0] (RAM bank at $C000-$FFFF),
// and bit 7 → port_1FFD[0] (special-paging mode toggle).
//
// Bits 2 and 5 are reserved here. The reference FPGA gates the
// port_7FFD bit-4 write on bit 2 = 0 but no NextZXOS write we've
// traced exercises that path, so it's deferred until evidence
// shows we need it. Mis-handling would risk overwriting the ROM
// select that SetROMBank already manages.
//
// The RAM-bank propagation matters because NextZXOS chains NR$8E
// with bit 3 set during boot — values $7A / $08 captured under the
// reference. Without bit 3 honoured, the wrong physical bank stays
// at $C000-$FFFF after the OS thinks it has switched. Boot then
// runs against stale paging state and diverges.
func (m *Memory) SetROMBankExtended(val byte) {
	// Per zxnext.vhd NR$8E write logic — has TWO blocks:
	//
	// Block 1 (lines 3662-3672) updates port_7FFD:
	//   if nr_wr_dat(3) = '1' then
	//      port_7ffd_reg(2 downto 0) <= nr_wr_dat(6 downto 4);
	//   end if;
	//   if nr_wr_dat(2) = '0' then
	//      port_7ffd_reg(4) <= nr_wr_dat(0);
	//   end if;
	//
	// Block 2 (lines 3726-3735) updates port_1FFD:
	//   port_1ffd_reg(2) <= nr_wr_dat(1);
	//   port_1ffd_reg(1) <= nr_wr_dat(0);
	//   port_1ffd_reg(0) <= nr_wr_dat(2);
	//
	// ROM bank is determined by port_1FFD[2] : port_7FFD[4]:
	//   port_1ffd_rom <= port_1ffd_reg(2) & port_7ffd_reg(4);  (line 3772)
	//
	// So NR$8E=$02 = `0000 0010`:
	//   bit 1=1 → port_1FFD[2] = 1
	//   bit 0=0 → port_1FFD[1] = 0, port_7FFD[4] (via bit 2=0 path) = 0
	// → ROM bank = 10 binary = 2. Which is what NextZXOS expects when
	// its RAM-resident trampoline at $5B48 does NEXTREG $8E,$02; RET
	// to dispatch to bank-2 $1661.
	//
	// bit 7 also updates port_DFFD[0] (high RAM-bank extension).
	if val&0x08 != 0 {
		// bit 3 set → write bits 6:4 to port_7FFD[2:0] (RAM bank).
		// We update port_7FFD directly without calling PageMemory
		// — PageMemory would clobber any MMU8 ($50-$57) bindings
		// that bank-0 init established for slot 3 ($C000-$FFFF).
		// Per FPGA, the NR$8E writeback into port_7FFD is the source
		// of truth, but MMU8 takes PRECEDENCE in slot mapping. The
		// classic-paging slot 3 only re-syncs when MMU8 is at its
		// default $FF page.
		m.port7FFD = (m.port7FFD &^ 0x07) | ((val >> 4) & 0x07)
		// bit 7 → port_DFFD[0] (high RAM-bank bit). Per nextreg.txt $8E:
		//   bit 7    = port 0xdffd bit 0  \  RAM bank 0-15
		//   bits 6:4 = port 0x7ffd bits 2:0 /
		// so the $C000 RAM bank is the combined 4-bit value. ours
		// PREVIOUSLY routed bit 7 to port_1FFD[0] (the paging-MODE bit)
		// and dropped it from the bank, so NEXTREG $8E,$88 mapped bank 0
		// instead of bank 8 — breaking NextZXOS's inter-bank RST $00 calls
		// into the high banks (e.g. the SPRITE AT sprite-attribute cache,
		// the NBI "590 Integer out of range" root cause).
		m.portDFFD = (m.portDFFD &^ 0x01) | ((val >> 7) & 0x01)
		// Per zxnext.vhd: a NR$8E write with bit 3 set asserts
		// port_memory_ram_change_dly (= NOT(nr_8e_we AND NOT bit3),
		// line 3814), and the paging process then reloads MMU6/MMU7
		// from port_7ffd_bank UNCONDITIONALLY (lines 4677-4680). The
		// FPGA's MMU registers are plain last-write-wins: a 7FFD-bank
		// change clobbers any prior NR$50-$57 binding on slots 6/7.
		// syncMMUFromPage(3) reproduces that — it rewrites slotBank[6/7]
		// AND clears their mmuOverride flags. (Matches the classic
		// PageMemory path; see TestMMUClassicClearsOverride.) The bank is
		// the full $DFFD-extended value so banks 8-15 map correctly.
		ramPage := int(m.portDFFD)<<3 | int(m.port7FFD&0x07)
		m.memoryPageReadMap[3] = ramPage
		m.memoryPageWriteMap[3] = ramPage
		m.syncMMUFromPage(3)
	}
	if val&0x04 == 0 {
		// bit 2 clear → write bit 0 to port_7FFD[4] (ROM select
		// low bit). Update slot 0 ROM bank without disturbing MMU8.
		m.port7FFD = (m.port7FFD &^ 0x10) | ((val & 0x01) << 4)
		if !m.mmuOverride[0] && !m.mmuOverride[1] {
			m.selectROM("7FFD")
		}
	}
	// Update port_1FFD per VHDL lines 3732-3735:
	//   port_1FFD[2] = bit 1, port_1FFD[1] = bit 0, port_1FFD[0] = bit 2
	// This is the ROM-bank-high-bit path that NextZXOS's trampolines
	// rely on (NR$8E=$02 sets port_1FFD[2]=1 → ROM bank 2 selected).
	new1FFD := byte(0)
	new1FFD |= (val >> 1) & 0x01 << 2 // bit 1 of val → port_1FFD[2]
	new1FFD |= (val & 0x01) << 1      // bit 0 of val → port_1FFD[1]
	new1FFD |= (val >> 2) & 0x01      // bit 2 of val → port_1FFD[0]
	// Preserve any bits we don't model in port_1FFD.
	merged := (m.port1FFD &^ 0x07) | new1FFD
	if merged != m.port1FFD {
		m.PageMemoryPlus3(merged)
	}
}

// ROMMappingNR8E reconstructs the NextReg $8E ("Spectrum 128K Memory
// Mapping") READ value from the live paging ports. Per nextreg.txt
// lines 870-885 and zxnext.vhd, the read is a pure function of the
// current paging state — NOT the last byte written to $8E:
//
//	bit 7    = port_DFFD[0]   (high RAM-bank bit; DFFD not modelled
//	                           yet → 0, see TestSetROMBankExtended_
//	                           Bit7_TargetsDFFD_NotModelled)
//	bits 6:4 = port_7FFD[2:0] (RAM bank low 3 bits)
//	bit 3    = 1              (always set on read)
//	bit 2    = port_1FFD[0]   (paging mode: 0 normal, 1 special allRAM)
//	bit 1    = port_1FFD[2]   (ROM/allRAM select high)
//	bit 0    = port_7FFD[4]   when paging mode 0 (normal ROM select)
//	           port_1FFD[1]   when paging mode 1 (special allRAM)
//
// Matches the reference emulator (NextReg case 0x8e), which
// reports $78 at the NextZXOS 128K-menu idle state (7FFD bank 7 + the
// always-1 bit 3). Returning the stored byte instead gives a stale
// $00 whenever paging was last driven through the legacy ports, which
// breaks any NextZXOS code that read-modify-writes $8E to preserve the
// current map.
func (m *Memory) ROMMappingNR8E() byte {
	v := (m.portDFFD & 0x01) << 7        // bit 7 = port_DFFD[0] (high RAM-bank bit)
	v |= (m.port7FFD & 0x07) << 4        // bits 6:4 = port_7FFD[2:0]
	v |= 0x08                            // bit 3 always reads 1
	v |= (m.port1FFD & 0x01) << 2        // bit 2 = port_1FFD[0] (paging mode)
	v |= ((m.port1FFD >> 2) & 0x01) << 1 // bit 1 = port_1FFD[2]
	if m.port1FFD&0x01 == 0 {
		v |= (m.port7FFD >> 4) & 0x01 // normal: bit 0 = port_7FFD[4]
	} else {
		v |= (m.port1FFD >> 1) & 0x01 // special: bit 0 = port_1FFD[1]
	}
	return v
}

// ResetPaging zeroes the classic paging shadows (port_7FFD,
// port_1FFD and port_DFFD). Mirrors the FPGA reset block at
// zxnext.vhd:3647-3648 (`port_7ffd_reg <= (others => '0')`), :3715
// (`port_1ffd_reg <= (others => '0')`), and the matching
// port_dffd_reg clear, all driven by the same `reset` signal —
// which fires via NR$02 bit 0 soft reset as well as a hard reset.
// Without this, RAM-bank-in-slot-3 / shadow-screen / ROM-high-bit
// state would survive a soft reset and let the post-reset path
// resume with a non-default classic page map.
func (m *Memory) ResetPaging() {
	m.port7FFD = 0
	m.port1FFD = 0
	m.portDFFD = 0
	// The FPGA clears port_7ffd_reg on EVERY reset (zxnext.vhd:
	// 3646-3648; reset = hard OR soft), which clears bit 5 and so
	// unlocks 128K paging. NextZXOS's staging soft-reset depends on
	// coming back up unlocked.
	m.PagingEnabled = true
	// Default 128K read/write map for 7FFD = 1FFD = 0 and no special
	// paging: RAM5 @ $4000, RAM2 @ $8000, RAM0 @ $C000. Pages 1/2 are
	// fixed; the caller sets the ROM for page 0 via SetROMBank.
	m.specialPaging = false
	m.memoryPageReadMap[3] = 0
	m.memoryPageWriteMap[3] = 0
	// zxnext.vhd:4610-4618: a reset — hard OR soft — resets the 8K
	// MMU to FF FF 0A 0B 04 05 00 01 with NO overrides. syncAllMMU
	// re-derives every slot from the (now-default) classic page map
	// and clears mmuOverride. Without this, a soft reset would leave a
	// prior program's MMU overrides in place — e.g. a dot command's
	// high RAM banks in slots 4-6 — so NextZXOS's post-reset init would
	// run over the wrong memory map.
	m.syncAllMMU()
}

// GetROMBank returns the currently-active ROM bank at
// 0x0000-0x3FFF (0..3 for the four Next ROM banks, or -1 if
// the active slot 0 is RAM rather than ROM).
func (m *Memory) GetROMBank() int {
	idx := m.memoryPageReadMap[0]
	if idx >= 16 && idx <= 19 {
		return idx - 16
	}
	return -1
}

// SetFPGABootROM installs the Spectrum Next FPGA bootrom image
// and enables bootrom mode. While active, reads at $0000-$3FFF
// come from the supplied 8 KB image (mirrored), bypassing all
// other ROM/RAM/divMMC dispatch. Writes to $0000-$3FFF are
// dropped. Cleared by ClearFPGABootROM, typically wired to the
// NextReg $03 OnWrite handler. Passing rom=nil clears the mode.
//
// This models the real Spectrum Next behaviour: at FPGA power-
// on, the bootrom is the only thing visible at $0000; the
// loader runs, reads TBBLUE.FW from SD, writes machine-type to
// NextReg $03, which clears bootrom mode and lets the freshly-
// loaded firmware take over.
func (m *Memory) SetFPGABootROM(rom []byte) {
	if len(rom) == 0 {
		m.fpgaBootROM = nil
		m.fpgaBootROMActive = false
		return
	}
	m.fpgaBootROM = rom
	m.fpgaBootROMActive = true
	// Power-on cycle: config_mode latches to 1 (zxnext.vhd:1102).
	// Activating the bootrom is logically equivalent — boot.bin
	// expects to find itself in config mode so its $00-$03 NR$04
	// writes can route 16K of CPU writes into each ROM bank.
	m.configModeActive = true
}

// ClearFPGABootROM disables bootrom mode but keeps the loaded
// image around (cheap to re-enable for a soft reset back into
// the loader if a peripheral wants to). Idempotent.
func (m *Memory) ClearFPGABootROM() {
	m.fpgaBootROMActive = false
}

// RearmFPGABootROM re-enables bootrom mode using the retained
// image. Models zxnext.vhd:5108-5110: the NR reset block (hard OR
// soft reset) re-asserts bootrom_en when nr_03_config_mode is
// active — the mechanism behind the firmware config menu's machine
// selection (re-enter/stay in config mode, soft reset, bootrom
// reloads TBBLUE.FW, the chosen personality boots). No-op when no
// image is installed.
func (m *Memory) RearmFPGABootROM() {
	if len(m.fpgaBootROM) > 0 {
		m.fpgaBootROMActive = true
	}
}

// FPGABootROMActive reports whether the bootrom is currently
// masking the low ROM area. Exposed for debug snapshots and tests.
func (m *Memory) FPGABootROMActive() bool { return m.fpgaBootROMActive }

// SetConfigModeRAMPage selects which 16 KB RAM page is visible at
// $0000-$3FFF when the Spectrum Next "configuration mode" memory
// map is active. The active/inactive state of config mode itself
// is owned by NR$03 (see WireMachineType + ClearConfigMode), per
// zxnext.vhd:1102 where `nr_03_config_mode` is the sole gate.
// NR$04 only picks which page is visible while that state is on.
//
// Power-on activates config mode in setupNext when the FPGA
// bootrom is armed (mirroring the FPGA's `'1'` power-on default
// for nr_03_config_mode); boot.bin's first NR$03 write with bits
// 2-0 != 0 turns it off.
//
// The window's priority ordering when config mode IS active:
//
//	FPGA bootrom (reads only) > Alt-ROM redirect > config-mode RAM
//
// — bootrom masks reads, but writes still hit the selected page;
// boot.bin running at $6000 leans on this to load each personality
// ROM into the page that classic 7FFD / 1FFD paging will swap in
// once the personality is selected.
func (m *Memory) SetConfigModeRAMPage(page byte) {
	m.configModeRAMPage = page
}

// ClearConfigMode exits the Next config-mode window. Called from
// the NextReg $03 OnWrite handler when guest code installs a real
// machine personality (bits 2-0 != 0).
func (m *Memory) ClearConfigMode() { m.configModeActive = false }

// EnterConfigMode is the symmetric counterpart to ClearConfigMode.
// Called from the NextReg $03 OnWrite handler when guest code
// requests bits 2-0 = 111 (the "re-enter config mode" pattern).
// Also exposed for tests that need to drive the config-mode read /
// write paths without standing up the full NextReg wire layer.
func (m *Memory) EnterConfigMode() { m.configModeActive = true }

// DivMMCAccessor lets pkg/memory route config-mode writes into the
// divMMC RAM that lives in pkg/next/divmmc, without taking a direct
// dependency on that package. divMMC has 128 KB of RAM (16 × 8 KB
// banks), so idx ranges 0..0x1FFFF. The eight 16 KB banks visible
// through NR$04 = $08..$0F (RAMPAGE_RAMDIVMMC per FPGA hardware.h)
// map directly onto this flat space.
// ReadROMByte / WriteROMByte address the divMMC ROM image
// (RAMPAGE_DIVMMC_ROM = NR$04 = $04 per FPGA hardware.h); without
// this hook NextZXOS reads the install-time enNxtmmc.rom and the
// SD's enNxtmmc.rom never gets a chance to overwrite it, producing
// the "enNxtmmc.rom invalid" abort during NextZXOS bank-0 init.
type DivMMCAccessor interface {
	ReadRAM(idx int) byte
	WriteRAM(idx int, val byte)
	ReadROMByte(addr int) byte
	WriteROMByte(addr int, val byte)
}

// SetDivMMCRAM wires a divMMC RAM accessor. Without this, NR$04
// values $08..$0F (the divMMC RAM 16 KB banks per FPGA nextreg.txt)
// behave as if the bank were unbacked: writes are silently dropped,
// reads return 0. boot.bin streams its firmware modules (boot.bin
// itself, editor.bin, etc.) into divMMC RAM via this path, so the
// accessor MUST be wired before NextZXOS boot.
func (m *Memory) SetDivMMCRAM(d DivMMCAccessor) { m.divMMCRAM = d }

// ConfigModeActive reports whether the config-mode window is
// currently routing $0000-$3FFF to a RAM page. Exposed for tests
// and debug snapshots.
func (m *Memory) ConfigModeActive() bool { return m.configModeActive }

// ConfigModeRAMPage returns the latched REG_RAMPAGE value. Exposed
// for tests; production callers should not need it.
func (m *Memory) ConfigModeRAMPage() byte { return m.configModeRAMPage }

// configModeReadByte returns one byte from the config-mode window
// at $0000-$3FFF. ok=false when no backing is wired for the current
// REG_RAMPAGE value (caller falls through to normal paging). This
// consolidates the per-page dispatch so divMMC RAM ($08..$0F per
// FPGA nextreg.txt) can be routed without needing a contiguous
// 16 KB slice.
func (m *Memory) configModeReadByte(addr uint16) (byte, bool) {
	p := m.configModeRAMPage
	if p == 0x04 && m.divMMCRAM != nil {
		// RAMPAGE_DIVMMC_ROM is 8 KB; reads past $1FFF return $FF.
		v := m.divMMCRAM.ReadROMByte(int(addr & 0x3FFF))
		m.fireRAMReadHook(int(p), addr&0x3FFF, v)
		return v, true
	}
	if p >= 0x08 && p <= 0x0F && m.divMMCRAM != nil {
		idx := int(p-0x08)*0x4000 + int(addr&0x3FFF)
		v := m.divMMCRAM.ReadRAM(idx)
		m.fireRAMReadHook(int(p), addr&0x3FFF, v)
		return v, true
	}
	if buf := m.configModePageBacking(p); buf != nil {
		v := buf[addr]
		// The read-tape (SetRAMReadHook) must see config-window reads
		// too — boot.bin/NextZXOS run RAM-presence checks through this
		// window, and an untapped read makes value tapes under-report
		// against oracle emulators (the development log).
		m.fireRAMReadHook(int(p), addr&0x3FFF, v)
		return v, true
	}
	return 0, false
}

// fireRAMReadHook invokes the RAM-read hook if installed. bank is the
// caller's reporting namespace: the 16K bank for pool reads, or the
// RAMPAGE page number for config-window reads.
func (m *Memory) fireRAMReadHook(bank int, off uint16, val byte) {
	if m.ramReadHook != nil {
		m.ramReadHook(bank, off, val)
	}
}

// configModeWriteByte is the dual of configModeReadByte. Returns
// true if the write landed in a backing store (drop-it-silently
// otherwise, matching the prior nil-buf behaviour).
func (m *Memory) configModeWriteByte(addr uint16, val byte) bool {
	p := m.configModeRAMPage
	if p == 0x04 && m.divMMCRAM != nil {
		// boot.bin streams enNxtmmc.rom (the SD-side divMMC ROM) into
		// this 8 KB window. NextZXOS later reads it back and aborts
		// with "enNxtmmc.rom invalid" if the bytes don't match the
		// version it expects.
		m.divMMCRAM.WriteROMByte(int(addr&0x3FFF), val)
		if m.configWriteHook != nil {
			m.configWriteHook(p, addr, val)
		}
		return true
	}
	if p >= 0x08 && p <= 0x0F && m.divMMCRAM != nil {
		idx := int(p-0x08)*0x4000 + int(addr&0x3FFF)
		m.divMMCRAM.WriteRAM(idx, val)
		return true
	}
	if buf := m.configModePageBacking(p); buf != nil {
		buf[addr] = val
		if m.configWriteHook != nil {
			m.configWriteHook(p, addr, val)
		}
		return true
	}
	return false
}

// SetConfigWriteHook installs a debug callback fired on every
// successful config-mode write. Called from headless test paths
// hunting load-path corruption (e.g. why m.rom[3]'s first 18
// bytes don't match enNextZX.rom). Returns nil from the hook to
// skip; the write goes through either way.
func (m *Memory) SetConfigWriteHook(fn func(page byte, addr uint16, val byte)) {
	m.configWriteHook = fn
}

// configModePageBacking returns the 16 KB backing slice that
// REG_RAMPAGE page `p` maps to when config mode is active. Returns
// nil when the page isn't backed by this dispatch — the caller
// treats nil as "drop write / return 0".
//
// Mapping per TBBlue `src/firmware/hardware.h`:
//
//	$00..$03 RAMPAGE_ROMSPECCY -> m.rom[0..3]
//	$06      RAMPAGE_ALTROM0   -> m.altROM0
//	$07      RAMPAGE_ALTROM1   -> m.altROM1
//	$10..$7F RAMPAGE_RAMSPECCY -> m.ram[p-$10] (banks 0..111)
//
// $04 (RAMPAGE_ROMDIVMMC) and $08..$0F (RAMPAGE_RAMDIVMMC) are handled
// by the caller (configModeReadByte/configModeWriteByte) before
// reaching this function, since they route through the divMMCRAM
// accessor rather than a contiguous slice. $05 (RAMPAGE_ROMMF, the
// Multiface ROM) is not routed through config mode at all — the
// Multiface ROM is installed directly via SetMultifaceROM.
func (m *Memory) configModePageBacking(p byte) []byte {
	switch {
	case p <= 0x03:
		return m.rom[p][:]
	case p == 0x06:
		// RAMPAGE_ALTROM0: NextZXOS Browser uses this to stream
		// enAltZX.rom (which IS the Browser application binary)
		// into the Alt-ROM 0 buffer. NextReg $8C bit 7 then
		// enables the read-redirect so the Browser executes
		// from this buffer.
		return m.altROM0[:]
	case p == 0x07:
		// RAMPAGE_ALTROM1: 48K-style alt-ROM bank.
		return m.altROM1[:]
	case p >= 0x10 && p <= 0x7F:
		idx := int(p) - 0x10
		if idx < len(m.ram) && m.ram[idx] != nil {
			return m.ram[idx]
		}
	}
	return nil
}
