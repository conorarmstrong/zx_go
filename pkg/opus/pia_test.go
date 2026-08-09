package opus

import "testing"

// $3000-$3003 is a 6821 PIA, not the flat control latch it first looked like.
// The v2.2 disassembly's printer routines give it away — they walk DOWN
// through the four addresses reconfiguring directions:
//
//	181C WRITE_T   LD HL,+3003 / LD (HL),+38   Clock on for bits program
//	               DEC HL      / LD (HL),+FF   Signal: all databits = out
//	               DEC HL      / RES 2,(HL)    Clock on for BUSY program
//	               DEC HL      / RES 6,(HL)    Signal: BUSY = input
//	               INC HL      / SET 2,(HL)    Clock off for BUSY program
//
// "Clock on/off for the ... program" is the 6821's control-register bit 2
// selecting the data-direction register instead of the peripheral register,
// and "all databits = out" / "BUSY = input" are direction bits. That is the
// 6821 register file exactly: PRA/DDRA, CRA, PRB/DDRB, CRB.

// TestPIASelectsDirectionRegisterWhenControlBit2IsClear pins the 6821's
// defining quirk: one address is two registers, chosen by CR bit 2.
func TestPIASelectsDirectionRegisterWhenControlBit2IsClear(t *testing.T) {
	d := New()

	// CRA bit 2 clear -> $3000 is DDRA.
	d.Write(PIABase+1, 0x00)
	d.Write(PIABase+0, 0xFF) // DDRA := all outputs
	if got := d.Read(PIABase + 0); got != 0xFF {
		t.Errorf("DDRA read back %#02x, want 0xFF", got)
	}

	// CRA bit 2 set -> $3000 is now PRA, a different register.
	d.Write(PIABase+1, 0x04)
	d.Write(PIABase+0, 0xC0)
	if got := d.Read(PIABase + 0); got != 0xC0 {
		t.Errorf("PRA read back %#02x, want 0xC0", got)
	}

	// Going back to the DDR must still show the direction, not the data.
	d.Write(PIABase+1, 0x00)
	if got := d.Read(PIABase + 0); got != 0xFF {
		t.Errorf("DDRA read back %#02x after using PRA, want 0xFF", got)
	}
}

// TestPIAPortBHasItsOwnControlRegister pins that B is independent of A.
func TestPIAPortBHasItsOwnControlRegister(t *testing.T) {
	d := New()
	d.Write(PIABase+3, 0x00) // CRB bit 2 clear -> DDRB
	d.Write(PIABase+2, 0xFF)
	d.Write(PIABase+3, 0x04) // -> PRB
	d.Write(PIABase+2, 0x5A)
	if got := d.Read(PIABase + 2); got != 0x5A {
		t.Errorf("PRB read back %#02x, want 0x5A", got)
	}
	d.Write(PIABase+3, 0x00)
	if got := d.Read(PIABase + 2); got != 0xFF {
		t.Errorf("DDRB read back %#02x, want 0xFF", got)
	}
}

// TestControlRegistersReadBack pins the flags the ROM stores in CRA. INIT_RAM2
// ends with "(3001) := %000001b1 ... b = IC 6116 present?", and both LOOKUP_2
// ("BIT 1,(HL)") and KEY_INT ("LD A,(3001) / RRCA") read them back later.
func TestControlRegistersReadBack(t *testing.T) {
	d := New()
	for _, v := range []byte{0x00, 0x07, 0x05, 0x04, 0xFF} {
		d.Write(PIABase+1, v)
		if got := d.Read(PIABase + 1); got != v {
			t.Errorf("CRA wrote %#02x, read back %#02x", v, got)
		}
	}
}

// TestInputBitsReadTheLineNotTheLatch pins the direction bits actually doing
// something: a bit configured as an input must not read back the output latch.
// This is what lets the printer routine see BUSY after it clears DDRA bit 6.
func TestInputBitsReadTheLineNotTheLatch(t *testing.T) {
	d := New()
	d.Write(PIABase+1, 0x00)
	d.Write(PIABase+0, 0xFF) // all outputs
	d.Write(PIABase+1, 0x04)
	d.Write(PIABase+0, 0xFF) // drive them all high

	// Now make bit 6 an input, as WRITE_T does for BUSY.
	d.Write(PIABase+1, 0x00)
	d.Write(PIABase+0, 0xBF) // DDRA bit 6 clear
	d.Write(PIABase+1, 0x04)
	if got := d.Read(PIABase + 0); got&0x40 != 0 {
		t.Errorf("PRA = %#02x: bit 6 is an input and no printer is attached, so it must read 0", got)
	}
	// The output bits still read their latch.
	if got := d.Read(PIABase + 0); got&0x80 == 0 {
		t.Errorf("PRA = %#02x: bit 7 is an output driven high", got)
	}
}

// TestInitRamSequenceLeavesTheStateTheROMExpects replays INIT_RAM2's tail
// verbatim and pins the result the rest of the ROM depends on.
//
//	1585 REP_RAM  LD HL,+3001 / LD (HL),C     (3001) := %00000000
//	              DEC HL      / LD (HL),+FF   (3000) := %11111111
//	              INC HL      / LD (HL),A     (3001) := %000001b1
//	              DEC HL      / LD (HL),+C0   (3000) := %11000000
func TestInitRamSequenceLeavesTheStateTheROMExpects(t *testing.T) {
	d := New()
	d.Write(PIABase+1, 0x00) // CRA := 0  -> $3000 is DDRA
	d.Write(PIABase+0, 0xFF) // DDRA := all outputs
	d.Write(PIABase+1, 0x07) // CRA := %00000111, 6116 present, -> $3000 is PRA
	d.Write(PIABase+0, 0xC0) // PRA := %11000000

	// KEY_INT: "LD A,(3001) / RRCA" — carry set means already initialised.
	if got := d.Read(PIABase + 1); got&0x01 == 0 {
		t.Errorf("CRA = %#02x, bit 0 must be set so KEY_INT skips re-initialising", got)
	}
	// LOOKUP_2: "BIT 1,(HL)" — the 6116-present flag.
	if got := d.Read(PIABase + 1); got&0x02 == 0 {
		t.Errorf("CRA = %#02x, bit 1 must be set to signal the 6116 is present", got)
	}
	// READ_J: "LD A,(3000) / AND +80" — set means the joystick port is not
	// formatted, so the Kempston port is used. That is the power-on default.
	if got := d.Read(PIABase + 0); got&0x80 == 0 {
		t.Errorf("PRA = %#02x, bit 7 must be set (joystick port unformatted)", got)
	}
}

// TestDriveSelectFollowsPortA pins the drive lines. Subtable byte 2 of the
// disk-info table documents them: "bit 0: SET for righthand drive; bit 1: SET
// for lefthand drive", and RD_WR_M_5 writes that pattern to $3000.
func TestDriveSelectFollowsPortA(t *testing.T) {
	d := New()
	d.Write(PIABase+1, 0x00) // CRA bit 2 clear -> DDRA
	d.Write(PIABase+0, 0xFF) // port A all outputs, as INIT_RAM2 leaves it
	d.Write(PIABase+1, 0x04) // -> PRA

	d.Write(PIABase+0, 0x01) // righthand drive = drive 1
	if got := d.SelectedDrive(); got != 0 {
		t.Errorf("PRA bit 0 set: SelectedDrive = %d, want 0", got)
	}
	d.Write(PIABase+0, 0x02) // lefthand drive = drive 2
	if got := d.SelectedDrive(); got != 1 {
		t.Errorf("PRA bit 1 set: SelectedDrive = %d, want 1", got)
	}
}

// TestSideSelectIsPortABit4 pins the side line. RD_WR_M_8 does
// "LD HL,+3000 / SET 4,(HL)  Signal: handling side 2" and FORMAT_M6
// "RES 4,(HL)  Signal: side 1".
func TestSideSelectIsPortABit4(t *testing.T) {
	d := New()
	d.Write(PIABase+1, 0x00)
	d.Write(PIABase+0, 0xFF)
	d.Write(PIABase+1, 0x04)
	d.Write(PIABase+0, 0x01)
	if d.SelectedSide() != 0 {
		t.Error("side must be 0 with PRA bit 4 clear")
	}
	d.Write(PIABase+0, 0x11)
	if d.SelectedSide() != 1 {
		t.Error("side must be 1 with PRA bit 4 set")
	}
}
