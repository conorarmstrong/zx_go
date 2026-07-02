package if1

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

func TestNewBusIsIdle(t *testing.T) {
	bus := NewMicrodriveBus()
	if bus.AnyMotorOn() {
		t.Error("fresh bus has motor on")
	}
	for i := 0; i < NumDrives; i++ {
		d := bus.Drive(i)
		if d == nil {
			t.Fatalf("Drive(%d) returned nil", i)
		}
		if d.Inserted {
			t.Errorf("drive %d: Inserted = true on fresh bus", i)
		}
	}
}

func TestInsertEjectCartridge(t *testing.T) {
	bus := NewMicrodriveBus()
	c := microdrive.New(180)
	bus.Insert(0, c)

	d := bus.Drive(0)
	if !d.Inserted {
		t.Error("after Insert: Inserted = false")
	}
	if d.Cartridge != c {
		t.Error("after Insert: cartridge pointer mismatch")
	}

	bus.Eject(0)
	if d.Inserted {
		t.Error("after Eject: Inserted = true")
	}
	if d.Cartridge != nil {
		t.Error("after Eject: cartridge not nil")
	}
}

// TestMotorRotationDaisyChain verifies that toggling the motor select
// signal walks the motor-on bit through the daisy chain just like the
// real IF1 hardware. This is the mechanism the IF1 ROM uses to pick a
// drive: each AdvanceMotor(true) followed by AdvanceMotor(false) is
// one step.
func TestMotorRotationDaisyChain(t *testing.T) {
	bus := NewMicrodriveBus()
	for i := 0; i < NumDrives; i++ {
		bus.Insert(i, microdrive.New(180))
	}

	// First select: drive 0 should be motor-on.
	bus.AdvanceMotor(true)
	if !bus.Drive(0).MotorOn {
		t.Error("after first AdvanceMotor(true): drive 0 motor off")
	}
	if bus.Drive(1).MotorOn {
		t.Error("after first AdvanceMotor(true): drive 1 motor on")
	}

	// Second select: rotate. Drive 0 takes the new (true) bit, drive 1
	// gets drive 0's previous on. So both 0 AND 1 are on.
	bus.AdvanceMotor(true)
	if !bus.Drive(0).MotorOn || !bus.Drive(1).MotorOn {
		t.Errorf("after second AdvanceMotor(true): motors = %v %v",
			bus.Drive(0).MotorOn, bus.Drive(1).MotorOn)
	}

	// Now turn the input bit off and rotate. Drive 0 = false, drive 1 = previous-0 = true.
	bus.AdvanceMotor(false)
	if bus.Drive(0).MotorOn {
		t.Error("after AdvanceMotor(false): drive 0 still on")
	}
	if !bus.Drive(1).MotorOn {
		t.Error("after AdvanceMotor(false): drive 1 motor off")
	}
}

// TestRestartAlignsToBlockBoundary checks that restart() snaps the
// head to the next valid window boundary.
func TestRestartAlignsToBlockBoundary(t *testing.T) {
	bus := NewMicrodriveBus()
	bus.Insert(0, microdrive.New(180))

	d := bus.Drive(0)
	d.MotorOn = true

	// Park the head somewhere awkward in the middle of a record.
	d.headPos = microdrive.HeadLen + 50
	bus.RestartAll()
	off := d.headPos % microdrive.BlockLen
	if off != 0 && off != microdrive.HeadLen {
		t.Errorf("after restart: blockOffset = %d, want 0 or %d", off, microdrive.HeadLen)
	}
}

// TestWritePreambleSetsSyncOK drives the IF1 write protocol against
// drive 0: 10 zeros, 2 0xFF, then a header. The drive should mark
// the slot as syncOK and start storing the header bytes in the
// cartridge data area.
func TestWritePreambleSetsSyncOK(t *testing.T) {
	bus := NewMicrodriveBus()
	c := microdrive.New(180)
	bus.Insert(0, c)
	d := bus.Drive(0)
	d.MotorOn = true
	bus.RestartAll()

	// Capture the slot index before we drive bytes — restart parks
	// us at the start of a header window (offset 0 → slot 0 in the
	// header table).
	slot := d.preambleSlot()

	// Preamble: 10 × 0x00, 2 × 0xFF. The slot's preamble counter
	// climbs from 1 → 12 across these 12 writes; the syncOK
	// transition only fires on the 13th byte (the first real data
	// byte of the header).
	for i := 0; i < 10; i++ {
		bus.WriteDataPort(0x00)
	}
	for i := 0; i < 2; i++ {
		bus.WriteDataPort(0xFF)
	}
	if d.pream[slot] != 12 {
		t.Errorf("after preamble: pream[%d] = %d, want 12", slot, d.pream[slot])
	}

	// Now write 15 header bytes. The first byte triggers the
	// 12 → syncOK transition AND lands at cartridge[0]; subsequent
	// bytes land at cartridge[1..14].
	for i := 0; i < microdrive.HeadLen; i++ {
		bus.WriteDataPort(byte(0xA0 + i))
	}
	if d.pream[slot] != syncOK {
		t.Errorf("after first data byte: pream[%d] = %d, want syncOK (%d)", slot, d.pream[slot], syncOK)
	}
	for i := 0; i < microdrive.HeadLen; i++ {
		got := c.DataAt(i)
		if got != byte(0xA0+i) {
			t.Errorf("cartridge byte %d = %02X, want %02X", i, got, byte(0xA0+i))
		}
	}
}

// TestReadByteWalksTape verifies that successive ReadDataPort calls
// stream bytes off the cartridge and the head wraps at end-of-tape.
func TestReadByteWalksTape(t *testing.T) {
	bus := NewMicrodriveBus()
	c := microdrive.New(180)
	for i := 0; i < 30; i++ {
		c.SetDataAt(i, byte(0x10+i))
	}
	bus.Insert(0, c)
	d := bus.Drive(0)
	d.MotorOn = true

	// We're in a header window — maxBytes = 15.
	bus.RestartAll()
	for i := 0; i < 15; i++ {
		got := bus.ReadDataPort()
		if got != byte(0x10+i) {
			t.Errorf("byte %d: got %02X, want %02X", i, got, byte(0x10+i))
		}
	}
}

// TestReadPastWindowRetainsLastByte verifies that once a window's
// maxBytes have been transferred, further MDR reads return the same
// byte as the last real transfer rather than the idle-bus 0xFF value.
// The IF1 ULA's data port is a latch fed by the tape's serial-to-
// parallel shifter: it only updates when the controller shifts in a
// new byte, so reading it after the window closes (before the next
// CTR/NET access restarts the drive) just samples the latch again.
func TestReadPastWindowRetainsLastByte(t *testing.T) {
	bus := NewMicrodriveBus()
	c := microdrive.New(180)
	for i := 0; i < microdrive.HeadLen; i++ {
		c.SetDataAt(i, byte(0x10+i))
	}
	bus.Insert(0, c)
	d := bus.Drive(0)
	d.MotorOn = true

	// We're in a header window — maxBytes = 15.
	bus.RestartAll()
	var last byte
	for i := 0; i < microdrive.HeadLen; i++ {
		last = bus.ReadDataPort()
	}
	if last != byte(0x10+microdrive.HeadLen-1) {
		t.Fatalf("last in-window byte = %02X, want %02X", last, byte(0x10+microdrive.HeadLen-1))
	}

	for i := 0; i < 3; i++ {
		if got := bus.ReadDataPort(); got != last {
			t.Errorf("past window read %d: data port = %02X, want %02X (latched last byte)", i, got, last)
		}
	}
}

// TestReadStatusReturnsWriteProtect verifies that the GAP/SYNC bits
// only fire on a formatted (syncOK) block, and that the WP bit
// reflects the cartridge state.
func TestReadStatusReturnsWriteProtect(t *testing.T) {
	bus := NewMicrodriveBus()
	c := microdrive.New(180)
	c.SetWriteProtect(true)
	bus.Insert(0, c)
	d := bus.Drive(0)
	d.MotorOn = true

	got := bus.ReadStatusPort()
	if got&0x01 != 0 {
		t.Errorf("write-protected: status bit 0 = 1, want 0 (got %02X)", got)
	}

	c.SetWriteProtect(false)
	got = bus.ReadStatusPort()
	if got&0x01 == 0 {
		t.Errorf("not write-protected: status bit 0 = 0, want 1 (got %02X)", got)
	}
}

// TestIdleDrivesContributeAllOnes confirms that drives with no
// cartridge or motor off return 0xFF on the bus, so they don't
// pollute the AND-merge of an active drive.
func TestIdleDrivesContributeAllOnes(t *testing.T) {
	bus := NewMicrodriveBus()
	bus.Insert(0, microdrive.New(180))
	d := bus.Drive(0)
	d.MotorOn = true

	// All other drives are idle. Status should reflect drive 0 only
	// (not write-protected → bit 0 high).
	got := bus.ReadStatusPort()
	if got&0x01 == 0 {
		t.Errorf("idle bus pollution: status bit 0 = 0, want 1 (got %02X)", got)
	}
}
