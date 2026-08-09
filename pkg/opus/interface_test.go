package opus

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func testROM(t *testing.T) []byte {
	t.Helper()
	rom, err := roms.ReadEmbeddedROM("opus.rom")
	if err != nil {
		t.Fatalf("opus.rom is not embedded: %v", err)
	}
	return rom
}

// TestInterfaceRejectsAWrongSizedROM guards the one thing that would silently
// mis-map everything.
func TestInterfaceRejectsAWrongSizedROM(t *testing.T) {
	if _, err := NewInterface(make([]byte, 4096)); err == nil {
		t.Error("a 4 KB image was accepted as the 8 KB Opus ROM")
	}
}

// TestInterfaceServesTheROMWhenPagedIn pins the overlay. At power-on the Opus
// ROM is over the Spectrum ROM, so $0000 must read the Opus reset vector
// (F3 = DI), not the Spectrum's.
func TestInterfaceServesTheROMWhenPagedIn(t *testing.T) {
	rom := testROM(t)
	i, err := NewInterface(rom)
	if err != nil {
		t.Fatal(err)
	}
	i.Reset()
	if !i.Active() {
		t.Fatal("the Opus ROM is not paged in at reset")
	}
	for _, addr := range []uint16{0x0000, 0x0008, 0x1708, 0x1748, 0x1FFF} {
		if got, want := i.Read(addr), rom[addr]; got != want {
			t.Errorf("Read(%#04x) = %#02x, want the ROM's %#02x", addr, got, want)
		}
	}
}

// TestInterfaceIsTransparentWhenPagedOut pins the other half: once paged out
// the Opus must not answer for $0000-$3FFF at all, or the Spectrum ROM would
// be unreachable.
func TestInterfaceIsTransparentWhenPagedOut(t *testing.T) {
	i, err := NewInterface(testROM(t))
	if err != nil {
		t.Fatal(err)
	}
	i.Reset()
	i.PreFetch(PageOut)
	i.PreFetch(0x0000)
	if i.Active() {
		t.Fatal("still paged in after the RET at $1748")
	}
	for _, addr := range []uint16{0x0000, 0x2800, 0x3000, 0x3FFF} {
		if _, ok := i.TryRead(addr); ok {
			t.Errorf("the Opus answered a read at %#04x while paged out", addr)
		}
		if i.Write(addr, 0x55) {
			t.Errorf("the Opus answered a write at %#04x while paged out", addr)
		}
	}
}

// TestInterfaceHardwareIsReachableWhenPagedIn pins that the window above the
// ROM reaches the real devices, not ROM mirror.
func TestInterfaceHardwareIsReachableWhenPagedIn(t *testing.T) {
	i, err := NewInterface(testROM(t))
	if err != nil {
		t.Fatal(err)
	}
	i.Reset()

	// Interface RAM.
	if !i.Write(0x2000, 0x5A) {
		t.Fatal("the Opus did not take a write to its RAM")
	}
	if got := i.Read(0x2000); got != 0x5A {
		t.Errorf("interface RAM at $2000 = %#02x, want 0x5A", got)
	}
	// FDC track register.
	i.Write(0x2801, 0x11)
	if got := i.Read(0x2801); got != 0x11 {
		t.Errorf("FDC track register = %#02x, want 0x11", got)
	}
	// PIA control register.
	i.Write(0x3001, 0x07)
	if got := i.Read(0x3001); got != 0x07 {
		t.Errorf("PIA CRA = %#02x, want 0x07", got)
	}
}

// TestInterfaceNeverAnswersAboveTheWindow guards the rest of the address
// space: the overlay is $0000-$3FFF only.
func TestInterfaceNeverAnswersAboveTheWindow(t *testing.T) {
	i, err := NewInterface(testROM(t))
	if err != nil {
		t.Fatal(err)
	}
	i.Reset()
	for _, addr := range []uint16{0x4000, 0x8000, 0xC000, 0xFFFF} {
		if _, ok := i.TryRead(addr); ok {
			t.Errorf("the Opus answered a read at %#04x", addr)
		}
		if i.Write(addr, 0x55) {
			t.Errorf("the Opus answered a write at %#04x", addr)
		}
	}
}

// TestROMIsReadOnly pins that a stray write cannot corrupt the interface ROM.
func TestROMIsReadOnly(t *testing.T) {
	rom := testROM(t)
	i, err := NewInterface(rom)
	if err != nil {
		t.Fatal(err)
	}
	i.Reset()
	i.Write(0x0000, 0xFF)
	if got := i.Read(0x0000); got != rom[0] {
		t.Errorf("ROM byte at $0000 = %#02x after a write, want %#02x", got, rom[0])
	}
}
