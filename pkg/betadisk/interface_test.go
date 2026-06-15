package betadisk

import "testing"

// The Beta interface decodes its five registers on the exact low byte
// ($1F/$3F/$5F/$7F/$FF, any high byte) and ignores everything else — notably
// $DF/$FB, so it never clashes with SpecDrum/Covox/Kempston-mouse.
func TestInterfacePortDecode(t *testing.T) {
	it := NewInterface()
	for _, p := range []uint16{0x1F, 0x3F, 0x5F, 0x7F, 0xFF, 0xFF1F, 0x40FF} {
		if !it.Handles(p) {
			t.Errorf("Handles($%04X) = false, want true", p)
		}
	}
	for _, p := range []uint16{0xDF, 0xFB, 0xFE, 0x9F, 0xBF, 0x00} {
		if it.Handles(p) {
			t.Errorf("Handles($%04X) = true, want false", p)
		}
	}
}

// The $FF system register write selects the drive (bits 0-1) and the side
// (bit 4, inverted: bit set → side 0, bit clear → side 1), per the Beta 128.
func TestInterfaceSystemRegisterSelect(t *testing.T) {
	it := NewInterface()
	// drive 2, bit4 set → side 0. (bit2 set = no reset.)
	it.WritePort(0xFF, 0x02|0x10|0x04)
	if it.FDC.drive != 2 {
		t.Errorf("drive = %d, want 2", it.FDC.drive)
	}
	if it.FDC.side != 0 {
		t.Errorf("side = %d, want 0 (bit4 set)", it.FDC.side)
	}
	// drive 1, bit4 clear → side 1.
	it.WritePort(0xFF, 0x01|0x04)
	if it.FDC.drive != 1 || it.FDC.side != 1 {
		t.Errorf("drive/side = %d/%d, want 1/1 (bit4 clear → side 1)", it.FDC.drive, it.FDC.side)
	}
}

// Reading $FF returns INTRQ in bit 7 and DRQ in bit 6.
func TestInterfaceSystemRegisterRead(t *testing.T) {
	it := NewInterface()
	it.FDC.Mount(0, NewImage(80, 2))
	it.WritePort(0xFF, 0x04)  // select drive 0, no reset
	it.FDC.WriteCommand(0x00) // Restore → raises INTRQ
	if it.ReadPort(0xFF)&0x80 == 0 {
		t.Error("INTRQ (bit 7) not reported in $FF read")
	}
	// A read sector raises DRQ.
	it.FDC.WriteSectorReg(1)
	it.FDC.WriteCommand(0x80) // Read Sector
	if it.ReadPort(0xFF)&0x40 == 0 {
		t.Error("DRQ (bit 6) not reported in $FF read")
	}
}

// End-to-end through the ports: write a command to $1F and stream a sector out
// of $7F, exactly as TR-DOS does.
func TestInterfaceReadSectorThroughPorts(t *testing.T) {
	it := NewInterface()
	img := NewImage(80, 2)
	want := make([]byte, SectorSize)
	for i := range want {
		want[i] = byte(i * 3)
	}
	if err := img.WriteSector(0, 0, 1, want); err != nil {
		t.Fatal(err)
	}
	it.FDC.Mount(0, img)
	it.WritePort(0xFF, 0x10|0x04) // drive 0, side 0, no reset
	it.WritePort(0x3F, 0)         // track register = 0 (head already at 0)
	it.WritePort(0x5F, 1)         // sector register = 1
	it.WritePort(0x1F, 0x80)      // command: Read Sector

	got := make([]byte, SectorSize)
	for i := range got {
		got[i] = it.ReadPort(0x7F)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d through ports = %02X, want %02X", i, got[i], want[i])
		}
	}
}

// A system-register write with the reset bit (bit 2) clear performs a master
// reset of the controller.
func TestInterfaceMasterReset(t *testing.T) {
	it := NewInterface()
	it.FDC.Mount(0, NewImage(80, 2))
	it.FDC.WriteSectorReg(1)
	it.FDC.WriteCommand(0x80) // start a read transfer (Busy)
	it.WritePort(0xFF, 0x00)  // bit2 clear → reset
	if it.ReadPort(0x1F)&statusBusy != 0 {
		t.Error("controller still busy after master reset")
	}
}
