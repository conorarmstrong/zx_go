package opus

import "testing"

// The Opus Discovery is MEMORY-mapped, not port-mapped, which is why its ROM
// contains almost no IN/OUT instructions at all. Derived from the v2.15 ROM
// disassembly published at speccy4ever.speccy.org (rom/opus/opus215disas.rtf)
// cross-checked against the v2.22 ROM in roms/opus.rom:
//
//	$2800-$2803  WD1770 register file (command/status, track, sector, data)
//	$3000-$3003  drive control and status
//
// The only real port instructions in the ROM are IN A,($FE) for the keyboard
// and IN A,($1F) for its Kempston-compatible joystick port — the latter
// confirmed as genuine code by its surrounding context (it reads $3000, tests
// bit 7, then reads the joystick).

func TestAddressDecode(t *testing.T) {
	for _, tc := range []struct {
		addr uint16
		want Region
		name string
	}{
		{0x0000, RegionROM, "ROM base"},
		{0x1FFF, RegionROM, "ROM top"},
		{0x2800, RegionFDC, "FDC command/status"},
		{0x2801, RegionFDC, "FDC track"},
		{0x2802, RegionFDC, "FDC sector"},
		{0x2803, RegionFDC, "FDC data"},
		{0x3000, RegionControl, "control"},
		{0x3003, RegionControl, "control top"},
		{0x2000, RegionRAM, "interface RAM"},
		{0x4000, RegionNone, "Spectrum RAM is not ours"},
		{0xFFFF, RegionNone, "top of memory is not ours"},
	} {
		if got := Decode(tc.addr); got != tc.want {
			t.Errorf("Decode(%#04x) [%s] = %v, want %v", tc.addr, tc.name, got, tc.want)
		}
	}
}

// TestFDCRegisterIndex pins which WD1770 register each address selects.
func TestFDCRegisterIndex(t *testing.T) {
	for addr, want := range map[uint16]int{
		0x2800: 0, // command / status
		0x2801: 1, // track
		0x2802: 2, // sector
		0x2803: 3, // data
	} {
		if got := FDCRegister(addr); got != want {
			t.Errorf("FDCRegister(%#04x) = %d, want %d", addr, got, want)
		}
	}
}

// TestTrackAndSectorRoundTrip drives the register file through the device's
// memory interface, which is the only way the guest can reach it.
func TestTrackAndSectorRoundTrip(t *testing.T) {
	d := New()
	d.Write(0x2801, 0x11) // track
	d.Write(0x2802, 0x07) // sector
	if got := d.Read(0x2801); got != 0x11 {
		t.Errorf("track reads back %#02x, want 0x11", got)
	}
	if got := d.Read(0x2802); got != 0x07 {
		t.Errorf("sector reads back %#02x, want 0x07", got)
	}
}

// TestRestoreSeeksToTrackZero pins that a real WD1770 command reaches the
// controller: RESTORE must leave the track register at zero.
func TestRestoreSeeksToTrackZero(t *testing.T) {
	d := New()
	d.Write(0x2801, 0x20) // pretend the head is at track 32
	d.Write(0x2800, 0x00) // RESTORE (Type I, opcode 0x0x)
	if got := d.Read(0x2801); got != 0 {
		t.Errorf("track after RESTORE = %#02x, want 0", got)
	}
}

// TestControlSelectsDrive pins the drive lines reaching the controller
// through the PIA's port A.
func TestControlSelectsDrive(t *testing.T) {
	d := New()
	d.Write(0x3001, 0x00) // CRA bit 2 clear -> DDRA
	d.Write(0x3000, 0xFF) // all outputs
	d.Write(0x3001, 0x04) // -> PRA
	d.Write(0x3000, 0x01) // righthand drive
	if got := d.SelectedDrive(); got != 0 {
		t.Errorf("SelectedDrive = %d, want 0", got)
	}
}

// TestAddressesOutsideTheWindowAreIgnored guards the rest of the machine: the
// Opus must not answer for memory that is not its own.
func TestAddressesOutsideTheWindowAreIgnored(t *testing.T) {
	d := New()
	if _, ok := d.TryRead(0x4000); ok {
		t.Error("Opus answered a read at $4000")
	}
	if ok := d.TryWrite(0x8000, 0xFF); ok {
		t.Error("Opus answered a write at $8000")
	}
	if _, ok := d.TryRead(0x2800); !ok {
		t.Error("Opus did not answer a read at its own FDC register")
	}
}
