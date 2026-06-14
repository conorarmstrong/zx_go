package rtc

import (
	"testing"
	"time"
)

// i2cMaster drives a Bus the way NextZXOS's bit-bang routine drives
// ports $103B/$113B: SDA changes while SCL low, sampled on SCL high.
type i2cMaster struct{ b *Bus }

func (m i2cMaster) clockHigh() { m.b.WriteSCL(true) }
func (m i2cMaster) clockLow()  { m.b.WriteSCL(false) }

// start: SDA high->low while SCL high.
func (m i2cMaster) start() {
	m.b.WriteSDA(true)
	m.clockHigh()
	m.b.WriteSDA(false)
	m.clockLow()
}

// stop: SDA low->high while SCL high.
func (m i2cMaster) stop() {
	m.b.WriteSDA(false)
	m.clockHigh()
	m.b.WriteSDA(true)
}

// writeByte sends 8 bits MSB-first and samples the slave's ACK on the
// 9th clock. Returns true when the slave ACKed (SDA pulled low).
func (m i2cMaster) writeByte(v byte) bool {
	for i := 7; i >= 0; i-- {
		m.b.WriteSDA(v&(1<<i) != 0)
		m.clockHigh()
		m.clockLow()
	}
	// ACK slot: master releases SDA, slave drives.
	m.b.WriteSDA(true)
	m.clockHigh()
	ack := !m.b.ReadSDA() // low = ACK
	m.clockLow()
	return ack
}

// readByte clocks 8 bits out of the slave MSB-first, then sends the
// master's ACK (true) or NAK (false) on the 9th clock.
func (m i2cMaster) readByte(ack bool) byte {
	m.b.WriteSDA(true) // master releases the line while slave drives
	var v byte
	for i := 0; i < 8; i++ {
		m.clockHigh()
		v <<= 1
		if m.b.ReadSDA() {
			v |= 1
		}
		m.clockLow()
	}
	m.b.WriteSDA(!ack) // ACK = drive low
	m.clockHigh()
	m.clockLow()
	m.b.WriteSDA(true)
	return v
}

func fixedClockBus() *Bus {
	r := New()
	r.SetNow(func() time.Time {
		return time.Date(2026, time.June, 5, 3, 42, 17, 0, time.Local)
	})
	return NewBus(r)
}

// TestI2C_ReadTimeSequence performs the standard DS1307 read the
// NextZXOS clock routine issues: START, addr+W ($D0), reg pointer 0,
// repeated START, addr+R ($D1), sequential reads with ACK, final NAK,
// STOP. The returned bytes must be the BCD time of the fixed clock.
func TestI2C_ReadTimeSequence(t *testing.T) {
	b := fixedClockBus()
	m := i2cMaster{b}

	m.start()
	if !m.writeByte(0xD0) {
		t.Fatal("address+W $D0 not ACKed")
	}
	if !m.writeByte(0x00) {
		t.Fatal("register pointer write not ACKed")
	}
	m.start() // repeated start
	if !m.writeByte(0xD1) {
		t.Fatal("address+R $D1 not ACKed")
	}
	sec := m.readByte(true)
	min := m.readByte(true)
	hour := m.readByte(false)
	m.stop()

	if sec != 0x17 { // 17 seconds in BCD
		t.Errorf("seconds = %#02x, want 0x17", sec)
	}
	if min != 0x42 {
		t.Errorf("minutes = %#02x, want 0x42", min)
	}
	if hour != 0x03 {
		t.Errorf("hours = %#02x, want 0x03", hour)
	}
}

// TestI2C_WrongAddressNotAcked — a different slave address must float
// SDA high (no ACK), exactly what the OS sees when no RTC is fitted.
func TestI2C_WrongAddressNotAcked(t *testing.T) {
	b := fixedClockBus()
	m := i2cMaster{b}
	m.start()
	if m.writeByte(0xA0) { // some other device
		t.Error("non-$68 address must not be ACKed")
	}
	m.stop()
}

// TestI2C_RegisterPointerSelectsRegister — pointer 2 then read returns
// the hours byte first.
func TestI2C_RegisterPointerSelectsRegister(t *testing.T) {
	b := fixedClockBus()
	m := i2cMaster{b}
	m.start()
	if !m.writeByte(0xD0) {
		t.Fatal("address+W not ACKed")
	}
	if !m.writeByte(0x02) { // hours register
		t.Fatal("pointer not ACKed")
	}
	m.start()
	if !m.writeByte(0xD1) {
		t.Fatal("address+R not ACKed")
	}
	if got := m.readByte(false); got != 0x03 {
		t.Errorf("hours = %#02x, want 0x03", got)
	}
	m.stop()
}

// TestI2C_IdleSDAHigh — with no transaction the SDA read-back is high
// (the float the old stub also produced).
func TestI2C_IdleSDAHigh(t *testing.T) {
	b := fixedClockBus()
	if !b.ReadSDA() {
		t.Error("idle SDA must read high")
	}
}

// TestI2C_NVRAMWriteReadBack writes two NVRAM bytes over i2c (pointer +
// sequential data with auto-increment) and reads them back.
func TestI2C_NVRAMWriteReadBack(t *testing.T) {
	r := New()
	b := NewBus(r)
	m := i2cMaster{b}

	m.start()
	if !m.writeByte(0xD0) { // address + W
		t.Fatal("addr+W not ACKed")
	}
	if !m.writeByte(0x10) { // register pointer
		t.Fatal("pointer not ACKed")
	}
	if !m.writeByte(0x55) {
		t.Fatal("data byte 0 not ACKed")
	}
	if !m.writeByte(0x66) {
		t.Fatal("data byte 1 not ACKed")
	}
	m.stop()

	if got := r.Read(0x10); got != 0x55 {
		t.Errorf("NVRAM[0x10] = %#x, want 0x55", got)
	}
	if got := r.Read(0x11); got != 0x66 {
		t.Errorf("NVRAM[0x11] = %#x, want 0x66 (auto-increment)", got)
	}

	// Read back over i2c.
	m.start()
	m.writeByte(0xD0)
	m.writeByte(0x10)
	m.start()
	m.writeByte(0xD1) // address + R
	v0 := m.readByte(true)
	v1 := m.readByte(false)
	m.stop()
	if v0 != 0x55 || v1 != 0x66 {
		t.Errorf("i2c read-back = %#x,%#x; want 0x55,0x66", v0, v1)
	}
}
