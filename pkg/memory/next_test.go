package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestSetupNextWorksWithoutDistro confirms that ModelNext succeeds
// even when the user-installed NextZXOS distro ROM is absent. The
// Next mode boots the embedded 128K BASIC ROMs in that case — the
// distro is only needed for software that wants to dispatch into
// NextZXOS-specific code paths via NextReg $8E / bank-3 mapping.
func TestSetupNextWorksWithoutDistro(t *testing.T) {
	installtest.RedirectConfig(t)
	if _, err := New("", roms.ModelNext); err != nil {
		t.Fatalf("memory.New(ModelNext) without distro: %v", err)
	}
}

func TestSetupNextLoadsDistroWhenOptedIn(t *testing.T) {
	installtest.RedirectConfig(t)
	installtest.AssertSandboxed(t)
	// Install a fake 4-bank (64K) distro and opt into the NextZXOS
	// boot path. Verify the distro bytes land in ROM banks 0..3.
	// Default Next mode (fast-boot) does NOT load the distro; the
	// NextZXOS path is gated on $ZX_GO_USE_NEXTZXOS.
	t.Setenv("ZX_GO_USE_NEXTZXOS", "1")
	dir, err := install.Path()
	if err != nil {
		t.Fatalf("install.Path: %v", err)
	}
	rom := make([]byte, 4*PageSize)
	for bank := 0; bank < 4; bank++ {
		for i := 0; i < PageSize; i++ {
			rom[bank*PageSize+i] = byte(0xA0 + bank)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, install.DistroROM), rom, 0644); err != nil {
		t.Fatalf("write fake distro: %v", err)
	}

	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}

	// MMU shadow: slots 0/1 = ROM (0xFF), slots 2..7 follow the
	// Next reset state (bank 5 / 2 / 0).
	want := [8]byte{0xFF, 0xFF, 10, 11, 4, 5, 0, 1}
	for i := byte(0); i < 8; i++ {
		if got := m.GetMMU(i); got != want[i] {
			t.Errorf("ModelNext slot %d: GetMMU = %#x, want %#x", i, got, want[i])
		}
	}

	// Distro bytes should be present in every bank.
	for bank := 0; bank < 4; bank++ {
		m.SetROMBank(bank)
		want := byte(0xA0 + bank)
		if got := m.Read(0x0100); got != want {
			t.Errorf("after SetROMBank(%d): Read(0x0100) = %#x, want %#x", bank, got, want)
		}
	}
}

func TestSetupNextFastBootLoadsForwardCompatibleROMs(t *testing.T) {
	// Default Next mode (no $ZX_GO_USE_NEXTZXOS) loads 48K BASIC at
	// bank 0 so the CPU resets straight into the "© 1982" prompt.
	// 128K editor stays at bank 1 for software that pages it in.
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	// 48K BASIC starts with F3 AF 11 FF FF C3 CB 11 (DI; XOR A; LD
	// DE,$FFFF; JP $11CB). Verify F3 at $0000 and that the bytes
	// match the classic 48K reset entry.
	if got := m.Read(0x0000); got != 0xF3 {
		t.Errorf("fast-boot bank 0 $0000 = %#x, want $F3 (48K DI)", got)
	}
}

// TestAltROMWriteRedirect verifies NextReg $8C bit 7 + bit 6 set
// ("alt-rom enabled, visible during writes only") redirects writes
// to $0000-$3FFF into the alt-rom RAM without disturbing reads —
// the exact configuration NextZXOS bank-2 init uses.
func TestAltROMWriteRedirect(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	// Read a ROM byte before any alt-rom activity — should be the
	// 48K BASIC reset entry.
	orig := m.Read(0x0000)
	if orig != 0xF3 {
		t.Fatalf("baseline ROM[$0000] = %#x, want $F3", orig)
	}
	// Enable alt-rom with write-redirect (NextReg $8C = $C0).
	m.SetAltROMReg(0xC0)
	// Write to "ROM" area. Without alt-rom this would be dropped.
	m.Write(0x0001, 0x42)
	// Reads still pass through to the normal ROM (bit 6 = 1 means
	// alt-rom is write-only).
	if got := m.Read(0x0000); got != 0xF3 {
		t.Errorf("alt-rom write mode: read should still pass through, got %#x want $F3", got)
	}
	// Switch to read-redirect (NextReg $8C = $80, bit 7 set, bit 6
	// clear). Reads now come from alt-rom RAM.
	m.SetAltROMReg(0x80)
	if got := m.Read(0x0001); got != 0x42 {
		t.Errorf("alt-rom read mode: should see the byte just written, got %#x want $42", got)
	}
	// $0000 in alt-rom is still zero (no write there).
	if got := m.Read(0x0000); got != 0x00 {
		t.Errorf("alt-rom read mode: $0000 should be zero (untouched), got %#x", got)
	}
	// Disable alt-rom — reads back to normal ROM.
	m.SetAltROMReg(0x00)
	if got := m.Read(0x0000); got != 0xF3 {
		t.Errorf("alt-rom disabled: read should pass through, got %#x want $F3", got)
	}
}

// TestAltROMBankSelect verifies NextReg $8C bits 5 / 4 lock the
// alt-rom selection to Alt-ROM 1 or Alt-ROM 0.
func TestAltROMBankSelect(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	// Enable alt-rom write-redirect with ROM0 lock ($8C bit 4).
	// Writes should target Alt-ROM 0.
	m.SetAltROMReg(0xD0) // bit 7 + bit 6 + bit 4
	m.Write(0x0010, 0xAA)
	// Switch to read-redirect with ROM0 still locked.
	m.SetAltROMReg(0x90) // bit 7 + bit 4 (no bit 6)
	if got := m.Read(0x0010); got != 0xAA {
		t.Errorf("Alt-ROM 0 round-trip: got %#x want $AA", got)
	}
	// Enable alt-rom write-redirect with ROM1 lock ($8C bit 5).
	// Writes should target Alt-ROM 1 — independent of Alt-ROM 0.
	m.SetAltROMReg(0xE0) // bit 7 + bit 6 + bit 5
	m.Write(0x0010, 0xBB)
	// Read from Alt-ROM 1.
	m.SetAltROMReg(0xA0) // bit 7 + bit 5
	if got := m.Read(0x0010); got != 0xBB {
		t.Errorf("Alt-ROM 1 round-trip: got %#x want $BB", got)
	}
	// Switch back to Alt-ROM 0 — the $AA should still be there
	// (the two banks are independent).
	m.SetAltROMReg(0x90)
	if got := m.Read(0x0010); got != 0xAA {
		t.Errorf("Alt-ROM 0 persistence after touching Alt-ROM 1: got %#x want $AA", got)
	}
}

// TestConfigModeRAMPageVisibleAt0000 verifies the FPGA-bootrom /
// config-mode behaviour required for tbblue_loader.rom and the
// boot.bin firmware: when NextReg $03 (machine type) bits 2-0 are
// zero (= "configuration mode") and the FPGA bootrom is not
// currently masking $0000-$3FFF, the 16 KB window at $0000-$3FFF
// is a scratchpad mapped to a specific RAM page selected by
// NextReg $04 (REG_RAMPAGE). boot.c uses this to write each
// successive ROM image into the page that the chosen machine
// personality will later swap in via classic 7FFD / 1FFD paging.
//
// Without this behaviour the FPGA-bootrom path silently drops
// every ROM-load write boot.c issues, the chosen machine
// personality starts up with empty / wrong ROM banks, and the
// system lands at the 48 K BASIC prompt regardless of which
// menu entry the user picked. Verified against the reference Spectrum Next emulator's
// tbblue.c:5013 (machine-type write calls tbblue_set_memory_pages
// to remap $0000-$3FFF whenever bits 2-0 = 0) and TBBlue
// firmware/app/src/boot.c (every loadFile call writes
// REG_RAMPAGE then writes 16 KB into the window).
func TestConfigModeRAMPageVisibleAt0000(t *testing.T) {
	installtest.RedirectConfig(t)

	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	// Enter config mode. After ModelNext setup the personality is
	// 128K-style with ROM at bank 0; EnterConfigMode + the page
	// register simulate the post-bootrom state: NextReg $03 bits
	// 2-0 = 0 and a chosen REG_RAMPAGE value.
	m.EnterConfigMode()
	m.SetConfigModeRAMPage(0x10) // page $10 = first Spectrum-RAM page
	// Write a byte at $0123 in the window — should land in page $10.
	m.Write(0x0123, 0xAB)
	// Reading back from the window returns the just-written byte.
	if got := m.Read(0x0123); got != 0xAB {
		t.Errorf("config-mode round-trip at $0123: got %#x, want $AB", got)
	}
	// Switch the window to a different page — same address now
	// reads the OTHER page's content, not the byte we just wrote.
	m.SetConfigModeRAMPage(0x11)
	if got := m.Read(0x0123); got == 0xAB {
		t.Errorf("page switch did not take effect: still reading $AB from page $11")
	}
	// Switch back — original byte is still there.
	m.SetConfigModeRAMPage(0x10)
	if got := m.Read(0x0123); got != 0xAB {
		t.Errorf("after page-switch round-trip: got %#x, want $AB", got)
	}
}

// TestConfigModePageMapsROMBank verifies that REG_RAMPAGE values
// $00..$03 (RAMPAGE_ROMSPECCY) route the $0000-$3FFF window to the
// four Spectrum-style ROM banks the +3 paging will swap in once
// config mode exits. boot.bin's load_roms() uses this exact
// mapping to write each successive 16 KB ROM image (e.g.
// enNextZX.rom split into 4 banks) into the bank the personality
// expects to find it. Without the mapping the write goes to RAM
// bank 0 and the personality starts up with whatever bytes
// setupNext seeded into the ROM banks at construction time
// (typically 48 K BASIC — which is exactly the misleading
// "© 1982 Sinclair Research Ltd" prompt the FPGA-bootrom path
// currently lands at).
//
// Page → backing-store mapping (mirrors TBBlue hardware.h
// RAMPAGE_* layout):
//
//	$00..$03 → m.rom[0..3] (RAMPAGE_ROMSPECCY)
//	$10..$7F → m.ram[page-$10] (RAMPAGE_RAMSPECCY and beyond)
func TestConfigModePageMapsROMBank(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	m.EnterConfigMode()
	// Page $00 → ROM bank 0. Write distinctive byte; verify the
	// SAME byte is observable via m.GetROMPage(0).
	m.SetConfigModeRAMPage(0x00)
	m.Write(0x1234, 0xCD)
	if got := m.GetROMPage(0)[0x1234]; got != 0xCD {
		t.Errorf("page $00 write: rom[0][$1234] = %#x, want $CD", got)
	}
	// And reading back through the window returns the same byte.
	if got := m.Read(0x1234); got != 0xCD {
		t.Errorf("page $00 read: Read($1234) = %#x, want $CD", got)
	}
	// Page $03 → ROM bank 3.
	m.SetConfigModeRAMPage(0x03)
	m.Write(0x0001, 0xEF)
	if got := m.GetROMPage(3)[0x0001]; got != 0xEF {
		t.Errorf("page $03 write: rom[3][$0001] = %#x, want $EF", got)
	}
}

// TestConfigModePageMapsAltROM verifies REG_RAMPAGE values $06 /
// $07 (RAMPAGE_ALTROM0 / RAMPAGE_ALTROM1 per TBBlue hardware.h)
// route the $0000-$3FFF window to the Alt-ROM 0 / 1 buffers.
// NextZXOS bank-2 loads enAltZX.rom (which IS the Browser
// application binary) into Alt-ROM 0 via this path during its
// startup: set REG_RAMPAGE=$06, stream 16 KB into $0000-$3FFF,
// then NextReg $8C bit 7 enables alt-ROM read-redirect and the
// Browser code runs from there.
//
// Without this mapping (config-mode write to page $06 dropped
// silently), the alt-ROM buffer stays zero. NextReg $8C then
// switches the $0000-$3FFF read window to *empty* alt-ROM,
// CPU executes the empty buffer, and NextZXOS halts at the
// "Error reading file: c:/machines/next/enAltZX.rom" splash.
//
// Verified against TBBlue `src/firmware/hardware.h` and the
// the reference Spectrum Next emulator MMU diff at NextZXOS-Browser-running state (slot 0/1
// = $8000/$8001 = alt-ROM active per the FPGA core's MMU
// encoding).
func TestConfigModePageMapsAltROM(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	m.EnterConfigMode()
	// Page $06 → Alt-ROM 0. Write a marker, then read it back
	// through the Alt-ROM 8C interface to confirm we hit the
	// same physical buffer.
	m.SetConfigModeRAMPage(0x06)
	m.Write(0x0010, 0xC1)
	m.Write(0x3FF0, 0xC2)
	// Switch to Alt-ROM 0 read-redirect (NextReg $8C bit 7 set,
	// bit 6 clear, bit 4 set = lock ROM 0 / Alt-ROM 0). Then
	// reads from $0000-$3FFF should return the bytes we just
	// wrote via the config-mode window.
	m.ClearConfigMode()  // exit config mode so 8C path is exercised
	m.SetAltROMReg(0x90) // bit 7 + bit 4: read-redirect, Alt-ROM 0
	if got := m.Read(0x0010); got != 0xC1 {
		t.Errorf("Alt-ROM 0 read after config-mode load: $0010 = %#x, want $C1", got)
	}
	if got := m.Read(0x3FF0); got != 0xC2 {
		t.Errorf("Alt-ROM 0 read after config-mode load: $3FF0 = %#x, want $C2", got)
	}
	// Page $07 → Alt-ROM 1.
	m.EnterConfigMode()
	m.SetConfigModeRAMPage(0x07)
	m.Write(0x0020, 0xD1)
	m.ClearConfigMode()
	m.SetAltROMReg(0xA0) // bit 7 + bit 5: read-redirect, Alt-ROM 1
	if got := m.Read(0x0020); got != 0xD1 {
		t.Errorf("Alt-ROM 1 read after config-mode load: $0020 = %#x, want $D1", got)
	}
}

// fakeDivMMCRAM is a flat 64 KB buffer satisfying DivMMCAccessor.
// Used by the config-mode tests to verify byte writes through the
// $0000-$3FFF window land in divMMC RAM when NR$04 selects a divMMC
// 16 KB bank ($08-$0B).
type fakeDivMMCRAM struct {
	ram [0x10000]byte
	rom [0x2000]byte
}

func (f *fakeDivMMCRAM) ReadRAM(idx int) byte     { return f.ram[idx&0xFFFF] }
func (f *fakeDivMMCRAM) WriteRAM(idx int, v byte) { f.ram[idx&0xFFFF] = v }
func (f *fakeDivMMCRAM) ReadROMByte(addr int) byte {
	if addr < 0 || addr >= len(f.rom) {
		return 0xFF
	}
	return f.rom[addr]
}
func (f *fakeDivMMCRAM) WriteROMByte(addr int, v byte) {
	if addr < 0 || addr >= len(f.rom) {
		return
	}
	f.rom[addr] = v
}

// TestConfigModePageMapsDivMMCRAM verifies the FPGA NR$04 encoding
// for divMMC RAM: per nextreg.txt, NR$04 holds a 16 KB SRAM bank
// index, and per hardware.h $08..$0B selects the four 16 KB banks
// that span divMMC's 64 KB RAM array. boot.bin (firmware
// switchModule) writes NR$04 = $08+N while streaming each 16 KB
// firmware-module chunk into $0000-$3FFF, expecting the chunks to
// land in divMMC RAM. Without this case the writes were dropped
// silently and NextZXOS's firmware modules never reached divMMC,
// so the boot fell back to 48K BASIC.
func TestConfigModePageMapsDivMMCRAM(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	var ram fakeDivMMCRAM
	m.SetDivMMCRAM(&ram)

	m.EnterConfigMode()
	// NR$04 = $08 → divMMC 16 KB bank 0 (covers divMMC RAM offsets
	// $0000-$3FFF). Write at $0010 and $3FF0; expect the bytes in
	// divMMC RAM at the same offsets.
	m.SetConfigModeRAMPage(0x08)
	m.Write(0x0010, 0xA1)
	m.Write(0x3FF0, 0xA2)
	if got := ram.ram[0x0010]; got != 0xA1 {
		t.Errorf("NR$04=$08 write at $0010: divMMC RAM byte = %#x, want $A1", got)
	}
	if got := ram.ram[0x3FF0]; got != 0xA2 {
		t.Errorf("NR$04=$08 write at $3FF0: divMMC RAM byte = %#x, want $A2", got)
	}
	// NR$04 = $0A → divMMC 16 KB bank 2 (= 8 KB banks 4+5, offsets
	// $8000-$BFFF in the flat 64 KB RAM).
	m.SetConfigModeRAMPage(0x0A)
	m.Write(0x0010, 0xB1)
	m.Write(0x3FF0, 0xB2)
	if got := ram.ram[0x8010]; got != 0xB1 {
		t.Errorf("NR$04=$0A write at $0010: divMMC RAM[$8010] = %#x, want $B1", got)
	}
	if got := ram.ram[0xBFF0]; got != 0xB2 {
		t.Errorf("NR$04=$0A write at $3FF0: divMMC RAM[$BFF0] = %#x, want $B2", got)
	}
	// Reads through the same window return the same bytes.
	m.SetConfigModeRAMPage(0x0A)
	if got := m.Read(0x0010); got != 0xB1 {
		t.Errorf("NR$04=$0A read at $0010: %#x, want $B1", got)
	}
}

func TestSetupNextWriteToRAMRoundTrip(t *testing.T) {
	installtest.RedirectConfig(t)

	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}

	// Write into bank 5 (visible at 0x4000-0x7FFF on default layout).
	m.Write(0x4242, 0xAB)
	if got := m.Read(0x4242); got != 0xAB {
		t.Errorf("RAM round-trip: Read(0x4242) = %#x, want 0xAB", got)
	}

	// Writes to ROM area (0x0000-0x3FFF) are ignored.
	original := m.Read(0x0100)
	m.Write(0x0100, 0xFF)
	if got := m.Read(0x0100); got != original {
		t.Errorf("ROM write should be ignored: Read(0x0100) = %#x, want %#x", got, original)
	}
}

// TestResetReactivatesFPGABootROM confirms the Reboot menu path on
// ModelNext brings the FPGA bootrom back online. The previous
// session ends with boot.bin's NR$03 write clearing the bootrom flag;
// real hardware re-enters bootrom mode on every power-cycle, and the
// emulator's reboot has to mirror that — otherwise the CPU resets
// into whatever ROM bank was active (typically 48K BASIC) with the
// Next NextRegs still configured, which corrupts the display.
func TestResetReactivatesFPGABootROM(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	// Pre-install an 8K bootrom image (any non-empty buffer works
	// for this property — the actual bytes are validated by the
	// boot-path tests).
	bootrom := make([]byte, 8192)
	for i := range bootrom {
		bootrom[i] = byte(i)
	}
	m.SetFPGABootROM(bootrom)
	if !m.FPGABootROMActive() {
		t.Fatalf("SetFPGABootROM did not activate bootrom mode")
	}
	// Simulate boot.bin's NextReg $03 write: bootrom mode clears.
	m.ClearFPGABootROM()
	if m.FPGABootROMActive() {
		t.Fatalf("ClearFPGABootROM did not deactivate bootrom mode")
	}

	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if !m.FPGABootROMActive() {
		t.Fatalf("after Reset, FPGABootROMActive() = false; want true (reboot should re-enter bootrom mode)")
	}
	// And the bootrom image must be readable through the CPU bus at
	// $0000 so the next cold start runs the loader, not the residual
	// 48K BASIC ROM in m.rom[0].
	if got := m.Read(0x0000); got != bootrom[0] {
		t.Errorf("Read($0000) after Reset = %#x, want %#x (bootrom byte 0)",
			got, bootrom[0])
	}
}
