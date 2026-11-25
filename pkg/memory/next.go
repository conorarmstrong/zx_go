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
		//
		// Per zxnext.vhd:3698-3702 the write CLAMPS the DFFD high nibble:
		//   if nr_wr_dat(3)='1' then port_dffd_reg(3) <= '0'; end if;
		//   if nr_wr_dat(3)='1' then port_dffd_reg(2:0) <= "00" & dat(7); end if;
		// so bits 3:1 are cleared and bit 0 = data bit 7. The $C000 bank
		// is thereby clamped to the 0-15 range NR$8E addresses — a stale
		// $DFFD extension (bank 16+) must NOT survive, or an NR$8E bit-3
		// write resolves $C000 to the wrong high bank (same stale-high-
		// bank class as the NBI-590 / tap-launch faults).
		m.portDFFD = (m.portDFFD &^ 0x0F) | ((val >> 7) & 0x01)
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
		// MMU6/7 reload suppressed when bit 3 is clear — zxnext.vhd:3814
		// (port_memory_ram_change_dly = not(nr_8e_we and not bit3)). The
		// NextZXOS sysvars trampolines (`NEXTREG $8E,n : RET`) rely on
		// this: their stack lives in an MMU-mapped bank at $C000-$FFFF,
		// and an unsuppressed reload swaps it out under the RET (the
		// issue-#10 tap-launch abort — fork proven against the FPGA
		// oracle at $5B42: RET target $0941 popped as $0000).
		//
		// KNOWN DEVIATION (paging-gate audit, rule C3/E1): the FPGA
		// applies a NR$8E write to port_1ffd_reg and the MMU reload
		// UNCONDITIONALLY — the nr_8e_we terms at zxnext.vhd:3718 and
		// :3813 carry no `and not port_7ffd_locked`, so $8E bypasses the
		// classic paging lock. pageMemoryPlus3 early-returns when
		// PagingEnabled is false (our model of that lock), so a NR$8E
		// write made WHILE paging is locked drops its 1FFD ROM-high-bit
		// update here. This is left as-is deliberately: NR$8E is a
		// Next-only register and the paging lock is a 48K/128K-BASIC
		// mechanism, so the two do not co-occur in real software (the
		// bit-3 RAM-bank + ROM-low-bit paths above are already applied
		// unconditionally). Revisit if a guest is ever found writing
		// NR$8E with 7FFD bit 5 set.
		m.pageMemoryPlus3(merged, val&0x08 != 0)
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

// SetFPGABootROMLowOnly selects the boot ROM window of core 3.02.05,
// $0000-$1FFF, with $2000-$3FFF reading the normal SRAM decode (in config
// mode the NR$04 page). The 30205 branch's zxnext.vhd changed the read mux
// from `cpu_a(15 downto 14) = "00"` to `cpu_a(15 downto 13) = "000"`; its
// boot ROM is built for that window. false is 3.02.03's $0000-$3FFF mirror.
func (m *Memory) SetFPGABootROMLowOnly(on bool) { m.fpgaBootROMLowOnly = on }

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

// SetNextMachineTiming records an NR$03 machine-timing write (bits 6:4 as
// the core stores them: 001 48K, 010 128K, 011 +3, 100 Pentagon). It takes
// effect at the next frame sync, as eff_nr_03_machine_timing does
// (zxnext.vhd:6694-6703); see LatchVideoTiming.
func (m *Memory) SetNextMachineTiming(t byte) { m.nextTimingPending = t & 0x07 }

// NextMachineTiming is the effective NR$03 machine timing.
func (m *Memory) NextMachineTiming() byte { return m.nextTiming }

// NextMachineTimingWritten is the NR$03 machine timing as last written, which
// NR$03 reads back (nr_03_machine_timing, zxnext.vhd:5894) even before the
// frame sync makes it effective.
func (m *Memory) NextMachineTimingWritten() byte { return m.nextTimingPending }

// LatchVideoTiming is the frame sync: a pending NR$03 machine timing becomes
// effective. It reports whether the timing changed, after running the
// observers registered with OnVideoTimingChange.
func (m *Memory) LatchVideoTiming() bool {
	// A pending NR$08 bit 6 write is due within its line, so by the frame
	// sync it has taken effect; settling it here keeps it clear of the
	// frame's T-state rebase.
	if m.cdHas {
		m.RAMContentionDisabled, m.cdHas = m.cdPending, false
	}
	if m.nextTimingPending == m.nextTiming {
		return false
	}
	m.nextTiming = m.nextTimingPending
	m.applyTimingCaches()
	for _, fn := range m.timingObservers {
		fn()
	}
	return true
}

// OnVideoTimingChange registers fn to run when LatchVideoTiming changes the
// Next's machine timing.
func (m *Memory) OnVideoTimingChange(fn func()) { m.timingObservers = append(m.timingObservers, fn) }

// ClearVideoTimingObservers drops every observer, for the machine leaving the
// Next: they reach into the Next's own wiring.
func (m *Memory) ClearVideoTimingObservers() { m.timingObservers = nil }

// TimingModel is the classic machine whose video and contention timing is in
// force: the model itself, except on the Next, where NR$03's machine timing
// picks it (zxnext.vhd:5758-5778: bit 2 Pentagon, "10" 128K, "11" +3, else
// 48K). pkg/roms holds each one's figures, derived from zxula_timing.vhd.
func (m *Memory) TimingModel() roms.SpectrumModel { return m.timingModelFor(m.nextTiming) }

// TimingModelAfterSync is the timing model the next frame sync will put in
// force: TimingModel with any pending NR$03 machine timing applied. The
// frame pacer arms a frame's timer before the frame latches, so it uses this.
func (m *Memory) TimingModelAfterSync() roms.SpectrumModel {
	return m.timingModelFor(m.nextTimingPending)
}

func (m *Memory) timingModelFor(t byte) roms.SpectrumModel {
	if m.currentModel != roms.ModelNext {
		return m.currentModel
	}
	switch {
	case t&0x04 != 0:
		return roms.ModelPentagon
	case t&0x03 == 0x02:
		return roms.Model128K
	case t&0x03 == 0x03:
		return roms.ModelPlus3
	default:
		return roms.Model48K
	}
}

// applyTimingCaches re-derives the contention shape from the timing in force.
// isContendedBank runs on every memory access, so it reads these caches
// rather than the model.
func (m *Memory) applyTimingCaches() {
	tm := m.TimingModel()
	m.contendTiming = contentionTimingFor(tm)
	m.contendTPerLine = uint64(roms.TStatesPerLine(tm))
	m.contendStart = uint64(roms.FirstContendedTStateFor(tm, m.sixtyHz))
}

// NoteCPUAccess records a CPU memory read or write (z80.CPU.CPUAccessHook).
// The last one to contended memory is the +3 floating bus between ULA
// fetches (p3_floating_bus_dat, zxnext.vhd:4496-4507).
func (m *Memory) NoteCPUAccess(addr uint16, val byte) {
	if m.isContendedBank(addr) {
		m.p3FloatByte = val
	}
}

// P3FloatingBus is the last CPU access to contended memory, $FF at power-on.
func (m *Memory) P3FloatingBus() byte { return m.p3FloatByte }

// SetRAMContentionDisable applies an NR$08 bit 6 write. The core latches it
// into eff_nr_08_contention_disable at the next 7 MHz column with hc(8) set
// (zxnext.vhd:5822-5824), so within the line: immediately when hc is already
// 256 or more, else when it gets there.
func (m *Memory) SetRAMContentionDisable(on bool) {
	if m.TStates == nil || m.currentModel != roms.ModelNext {
		m.RAMContentionDisabled, m.cdHas = on, false
		return
	}
	mult := uint64(1)
	if m.SpeedMultiplier != nil {
		if v := m.SpeedMultiplier(); v > 0 {
			mult = uint64(v)
		}
	}
	hcLine, intH := 456, 128
	switch m.TimingModel() {
	case roms.Model48K:
		hcLine, intH = 448, 116
	case roms.ModelPlus3:
		intH = 126
	case roms.ModelPentagon:
		hcLine, intH = 448, 439
	}
	hc := (intH + 2*int(*m.TStates/mult)) % hcLine
	wait := 0
	if hc < 256 {
		wait = (256 - hc + 1) / 2
	}
	m.cdPending, m.cdHas = on, true
	m.cdAt = *m.TStates + uint64(wait)*mult
	m.contentionDisabledNow()
}

// contentionDisabledNow settles a pending NR$08 bit 6 write whose moment has
// come and reports the effective disable.
func (m *Memory) contentionDisabledNow() bool {
	if m.cdHas && m.TStates != nil && *m.TStates >= m.cdAt {
		m.RAMContentionDisabled, m.cdHas = m.cdPending, false
	}
	return m.RAMContentionDisabled
}

// NR03Fields returns NR$03's machine type, user display-timing lock and bit 7,
// held here so a state capture keeps the register whole (the machine timing
// is SetNextMachineTiming's).
func (m *Memory) NR03Fields() (machineType, userDTLock, top byte) {
	return m.nr03Type, m.nr03Lock, m.nr03Top
}

// SetNR03Fields stores the fields NR03Fields returns.
func (m *Memory) SetNR03Fields(machineType, userDTLock, top byte) {
	m.nr03Type, m.nr03Lock, m.nr03Top = machineType, userDTLock, top
}

// SetPending60Hz records an NR$05 bit 2 write. Like the machine timing it
// takes effect at the frame sync (eff_nr_05_5060, zxnext.vhd:6699-6700); the
// machine collects it there with TakePending60Hz.
func (m *Memory) SetPending60Hz(on bool) { m.sixtyHzPending, m.sixtyHzHas = on, true }

// TakePending60Hz returns a pending 50/60 Hz selection that differs from the
// one in force, and clears it.
func (m *Memory) TakePending60Hz() (on, changed bool) {
	if !m.sixtyHzHas {
		return false, false
	}
	m.sixtyHzHas = false
	return m.sixtyHzPending, m.sixtyHzPending != m.sixtyHz
}

// FiftyHzForced reports that the timing in force is the Pentagon's, which
// zxnext.vhd:5835-5836 holds at 50 Hz whatever NR$05 says.
func (m *Memory) FiftyHzForced() bool { return m.TimingModel() == roms.ModelPentagon }

// ClearPending60Hz drops a pending 50/60 Hz write: the machine has left the
// Next whose register it belonged to.
func (m *Memory) ClearPending60Hz() { m.sixtyHzHas = false }

// SixtyHzAfterSync is the 50/60 Hz selection the next frame sync will put in
// force, for the frame pacer, which arms a frame's timer before its sync.
func (m *Memory) SixtyHzAfterSync() bool {
	if m.sixtyHzHas && m.timingModelFor(m.nextTimingPending) != roms.ModelPentagon {
		return m.sixtyHzPending
	}
	if m.timingModelFor(m.nextTimingPending) == roms.ModelPentagon {
		return false
	}
	return m.sixtyHz
}
