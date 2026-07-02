package if1

import (
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// loadRealIF1ROM returns the embedded if1-2.rom bytes from pkg/roms.
// Used by the real-ROM integration tests below to validate the
// page-in/page-out triggers against actual Interface 1 firmware.
func loadRealIF1ROM(t *testing.T) []byte {
	t.Helper()
	rm := roms.NewROMManager("")
	if err := rm.LoadROM(roms.ROMINTERFACE1, "if1-2.rom"); err != nil {
		t.Skipf("real IF1 ROM not available: %v", err)
	}
	rom, ok := rm.GetROM(roms.ROMINTERFACE1)
	if !ok {
		t.Skip("real IF1 ROM not loaded")
	}
	return rom
}

// TestRealROMByteAtRSTAddr verifies the byte at 0x0008 in the
// embedded Interface 1 ROM is the LD HL,(nn) opcode the IF1 ROM uses
// to enter its RST 8 handler. This is the canonical fingerprint of
// a real IF1 ROM (see Geoff Wearmouth's IF1 disassembly), so failing
// this test means we either loaded the wrong ROM or the embedded
// file is corrupted.
func TestRealROMByteAtRSTAddr(t *testing.T) {
	rom := loadRealIF1ROM(t)
	if len(rom) != ROMSize {
		t.Fatalf("ROM size: got %d, want %d", len(rom), ROMSize)
	}
	if rom[0x0008] != 0x2A {
		t.Errorf("byte at 0x0008 = %02X, want 0x2A (LD HL,(nn) — IF1 RST 8 handler)", rom[0x0008])
	}
	if rom[0x0005] != 0xC3 || rom[0x0006] != 0x00 || rom[0x0007] != 0x07 {
		t.Errorf("bytes at 0x0005..0x0007 = %02X %02X %02X, want C3 00 07 (JP 0x0700 — IF1 exit)",
			rom[0x0005], rom[0x0006], rom[0x0007])
	}
}

// TestRealROMPageInThenMemoryRead drives the full page-in path
// against the embedded IF1 ROM: install the ROM, simulate a Z80
// fetch at PC=0x0008 (the RST 8 trigger), then verify subsequent
// memory reads come from the IF1 ROM rather than the host ROM.
//
// This is the closest thing to "BASIC issued an error and the IF1
// ROM took over" we can do without a full Z80 + 48K ROM setup, and
// it validates that the trigger constant (PageInRST8 = 0x0008) and
// the MemoryRead overlay actually work end-to-end against real
// firmware bytes.
func TestRealROMPageInThenMemoryRead(t *testing.T) {
	rom := loadRealIF1ROM(t)
	i := New()
	if err := i.LoadROMBytes(rom); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}

	// Before page-in: MemoryRead returns nothing.
	if _, ok := i.MemoryRead(0x0008); ok {
		t.Fatal("inactive IF1 claimed memory at 0x0008")
	}

	// Simulate a Z80 fetch at the RST 8 trigger.
	i.PreFetchHook(PageInRST8)
	if !i.Active() {
		t.Fatal("PreFetchHook(PageInRST8) did not activate the IF1")
	}

	// MemoryRead at 0x0008 must now return the IF1 ROM byte (0x2A,
	// the LD HL,(nn) opcode), not whatever the host ROM has there.
	val, ok := i.MemoryRead(0x0008)
	if !ok {
		t.Fatal("active IF1 didn't claim 0x0008 in memory read")
	}
	if val != 0x2A {
		t.Errorf("MemoryRead(0x0008) = %02X, want 0x2A (real IF1 ROM byte)", val)
	}

	// The 16 KB ROM area is mirrored, so 0x2008 should also see
	// the IF1 ROM byte (the same 8 KB chunk repeats).
	val, ok = i.MemoryRead(0x2008)
	if !ok || val != 0x2A {
		t.Errorf("MemoryRead(0x2008) = %02X (ok=%v), want 0x2A (mirrored IF1 ROM)", val, ok)
	}

	// Simulate the page-out fetch at 0x0700 and confirm the
	// overlay drops.
	i.PostFetchHook(PageOutAddr)
	if i.Active() {
		t.Error("PostFetchHook(PageOutAddr) did not deactivate the IF1")
	}
	if _, ok := i.MemoryRead(0x0008); ok {
		t.Error("after page-out: IF1 still claims 0x0008")
	}
}

// TestRealROMOtherTriggerAddress verifies the close-channel page-in
// trigger (0x1708) also fires when the real ROM is loaded. The IF1
// ROM uses this entry point during RS-232 stream cleanup; the byte
// at 0x1708 in the v2 ROM is part of the close-channel handler.
func TestRealROMOtherTriggerAddress(t *testing.T) {
	rom := loadRealIF1ROM(t)
	i := New()
	if err := i.LoadROMBytes(rom); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}

	i.PreFetchHook(PageInCloseChannel)
	if !i.Active() {
		t.Errorf("PreFetchHook(PageInCloseChannel) did not activate the IF1")
	}
}

// TestEndToEndCartridgeRead exercises the full data path: load
// FUSE's bundled success.mdr cartridge, insert it into drive 0,
// engage the motor via the CTR port, then read the first header
// window (15 bytes) of the cartridge through the MDR port. The
// bytes should match the cartridge contents byte-for-byte.
//
// 15 bytes is the maxBytes for a header window — the drive
// "saturates" after that and the IF1 ROM has to issue a CTR read to
// advance to the next window. We test that path separately below.
func TestEndToEndCartridgeRead(t *testing.T) {
	// FUSE's test cartridge lives next door in pkg/microdrive/testdata.
	cart, err := microdrive.ReadFile(filepath.Join("..", "microdrive", "testdata", "success.mdr"))
	if err != nil {
		t.Fatalf("load cartridge: %v", err)
	}

	i := New()
	// No ROM loaded — this test exercises only the controller and
	// ULA data path, not ROM paging.
	i.ULA.Bus.Insert(0, cart)

	// Bit-bang the motor-select sequence: clock high+data high,
	// then clock low+data low → falling edge with dta low →
	// engage drive 0.
	i.HandlePortWrite(PortCTR, 0x03)
	i.HandlePortWrite(PortCTR, 0x00)

	if !i.ULA.Bus.Drive(0).MotorOn {
		t.Fatal("setup failed: drive 0 motor still off after select pulse")
	}

	for offset := 0; offset < microdrive.HeadLen; offset++ {
		want := cart.DataAt(offset)
		got, ok := i.HandlePortRead(PortMDR)
		if !ok {
			t.Fatalf("MDR port read at offset %d: not handled", offset)
		}
		if got != want {
			t.Errorf("offset %d: got %02X, want %02X", offset, got, want)
		}
	}
}

// TestEndToEndCartridgeReadAcrossWindows confirms that the IF1
// controller exposes BOTH the header window AND the data window of
// each block when the host issues a CTR access between them. Real
// IF1 software does exactly this — read header, check it, then read
// the data record.
func TestEndToEndCartridgeReadAcrossWindows(t *testing.T) {
	cart, err := microdrive.ReadFile(filepath.Join("..", "microdrive", "testdata", "success.mdr"))
	if err != nil {
		t.Fatalf("load cartridge: %v", err)
	}
	i := New()
	i.ULA.Bus.Insert(0, cart)

	// Engage drive 0.
	i.HandlePortWrite(PortCTR, 0x03)
	i.HandlePortWrite(PortCTR, 0x00)

	// Read the 15-byte header window.
	for offset := 0; offset < microdrive.HeadLen; offset++ {
		_, _ = i.HandlePortRead(PortMDR)
	}
	// CTR access advances the window — restart() runs as a side
	// effect of HandlePortRead(CTR). The next MDR reads should
	// stream the record header + data, starting at cartridge
	// offset HeadLen (15).
	_, _ = i.HandlePortRead(PortCTR)

	// Now read 15 more bytes — the record header. They should
	// match cart.DataAt(15..29).
	for offset := 0; offset < microdrive.HeadLen; offset++ {
		want := cart.DataAt(microdrive.HeadLen + offset)
		got, _ := i.HandlePortRead(PortMDR)
		if got != want {
			t.Errorf("after CTR-restart: offset %d (cart byte %d): got %02X, want %02X",
				offset, microdrive.HeadLen+offset, got, want)
		}
	}
}

// TestEndToEndWriteThenReadBack writes a known byte pattern through
// the MDR port and verifies it lands in the cartridge AND can be
// read back. Exercises the preamble-collapse logic in the write
// path: preamble bytes don't go to the cartridge, real data does.
func TestEndToEndWriteThenReadBack(t *testing.T) {
	cart := microdrive.New(180)
	i := New()
	i.ULA.Bus.Insert(0, cart)

	// Engage drive 0.
	i.HandlePortWrite(PortCTR, 0x03)
	i.HandlePortWrite(PortCTR, 0x00)

	// 12-byte preamble + 15-byte header. The preamble bytes are
	// consumed by the controller's syncing logic and don't reach
	// the cartridge; the 15 header bytes do.
	for k := 0; k < 10; k++ {
		i.HandlePortWrite(PortMDR, 0x00)
	}
	for k := 0; k < 2; k++ {
		i.HandlePortWrite(PortMDR, 0xFF)
	}
	for k := 0; k < microdrive.HeadLen; k++ {
		i.HandlePortWrite(PortMDR, byte(0x80+k))
	}

	// The 15 header bytes should now be at cartridge offsets 0..14.
	for k := 0; k < microdrive.HeadLen; k++ {
		want := byte(0x80 + k)
		got := cart.DataAt(k)
		if got != want {
			t.Errorf("cartridge[%d]: got %02X, want %02X", k, got, want)
		}
	}

	// The drive's modified flag should be set.
	if !i.ULA.Bus.Drive(0).Modified {
		t.Error("after write: Drive.Modified = false")
	}
}

// TestPageHooksRoundTripWithROM verifies that PageIn and PageOut work
// correctly when paired with the real Z80 fetch hooks: install the
// hooks, simulate fetches at the trigger addresses, and confirm the
// memory overlay activates and deactivates as expected.
func TestPageHooksRoundTripWithROM(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}

	// Initially inactive.
	if i.Active() {
		t.Fatal("freshly-loaded IF1 is already active")
	}
	if _, ok := i.MemoryRead(0x0008); ok {
		t.Fatal("inactive IF1 claimed memory at 0x0008")
	}

	// Simulate a Z80 fetch at one of the page-in triggers.
	i.PreFetchHook(0x0008)
	if !i.Active() {
		t.Errorf("PreFetchHook(0x0008): IF1 not active")
	}
	val, ok := i.MemoryRead(0x0008)
	if !ok {
		t.Fatal("active IF1 didn't claim 0x0008")
	}
	if val != 0x08 { // fakeROM byte at offset 0x08 is 0x08
		t.Errorf("MemoryRead(0x0008) = %02X, want 0x08 (fake ROM byte)", val)
	}

	// Simulate a fetch at the page-out trigger.
	i.PostFetchHook(0x0700)
	if i.Active() {
		t.Errorf("PostFetchHook(0x0700): IF1 still active")
	}
	if _, ok := i.MemoryRead(0x0008); ok {
		t.Error("after page-out: IF1 still claiming memory")
	}
}
