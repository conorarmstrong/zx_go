package if1

import (
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

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
	// We don't have a real IF1 ROM here so we don't bother loading
	// one — Phase 9 only exercises the controller and ULA paths.
	i.ULA.Bus.Insert(0, cart)

	// Bit-bang the motor-select sequence: clock high+data high,
	// then clock low+data low → falling edge with dta low →
	// engage drive 0.
	i.HandlePortWrite(0xEF, 0x03)
	i.HandlePortWrite(0xEF, 0x00)

	if !i.ULA.Bus.Drive(0).MotorOn {
		t.Fatal("setup failed: drive 0 motor still off after select pulse")
	}

	for offset := 0; offset < microdrive.HeadLen; offset++ {
		want := cart.DataAt(offset)
		got, ok := i.HandlePortRead(0xE7)
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
	i.HandlePortWrite(0xEF, 0x03)
	i.HandlePortWrite(0xEF, 0x00)

	// Read the 15-byte header window.
	for offset := 0; offset < microdrive.HeadLen; offset++ {
		_, _ = i.HandlePortRead(0xE7)
	}
	// CTR access advances the window — restart() runs as a side
	// effect of HandlePortRead(CTR). The next MDR reads should
	// stream the record header + data, starting at cartridge
	// offset HeadLen (15).
	_, _ = i.HandlePortRead(0xEF)

	// Now read 15 more bytes — the record header. They should
	// match cart.DataAt(15..29).
	for offset := 0; offset < microdrive.HeadLen; offset++ {
		want := cart.DataAt(microdrive.HeadLen + offset)
		got, _ := i.HandlePortRead(0xE7)
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
	i.HandlePortWrite(0xEF, 0x03)
	i.HandlePortWrite(0xEF, 0x00)

	// 12-byte preamble + 15-byte header. The preamble bytes are
	// consumed by the controller's syncing logic and don't reach
	// the cartridge; the 15 header bytes do.
	for k := 0; k < 10; k++ {
		i.HandlePortWrite(0xE7, 0x00)
	}
	for k := 0; k < 2; k++ {
		i.HandlePortWrite(0xE7, 0xFF)
	}
	for k := 0; k < microdrive.HeadLen; k++ {
		i.HandlePortWrite(0xE7, byte(0x80+k))
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
