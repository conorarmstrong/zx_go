package rtc

// Bus models the Spectrum Next's bit-banged i2c bus with a DS1307
// slave at address $68, as wired to I/O ports $103B (SCL) and $113B
// (SDA) — zxnext.vhd:2630-2631 (decode: A15-A8 = $10/$11, A7-A0 = $3B,
// gated by port_i2c_io_en) and :3234-3250 (write latches bit 0 of the
// data byte into the open-drain SCL/SDA outputs; both reset high).
//
// The lines are open-drain wired-AND: the observable SDA level is the
// AND of the master latch and the slave's drive. NextZXOS bit-bangs
// the standard DS1307 transaction (START, $D0, reg ptr, repeated
// START, $D1, sequential reads) to render the menu's date/time line;
// without a slave that ACKs and drives SDA, the read-back floats high
// and the clock fetch fails every frame.
type Bus struct {
	rtc *RTC

	scl  bool // master SCL latch (reset high)
	sdaM bool // master SDA latch (reset high)
	sdaS bool // slave SDA drive (true = released / high)

	started bool // between START and STOP

	phase  busPhase
	bitCnt int
	shift  byte

	reading  bool // current transfer direction (R/W̄ bit of the address)
	regPtr   byte
	wrotePtr bool // in a write transfer, has the register pointer arrived yet
	readData byte // shift register for slave→master bits
}

type busPhase int

const (
	phaseAddr     busPhase = iota // receiving the address byte
	phaseAddrAck                  // driving the address ACK clock
	phaseWrite                    // receiving a data byte (reg ptr / reg write)
	phaseWriteAck                 // driving the data ACK clock
	phaseRead                     // transmitting a data byte
	phaseReadAck                  // sampling the master's ACK/NAK
	phaseIdleNak                  // addressed elsewhere — ignore until STOP
)

const ds1307Addr = 0x68

// NewBus wraps an RTC register model with the bit-banged i2c slave.
func NewBus(r *RTC) *Bus {
	return &Bus{rtc: r, scl: true, sdaM: true, sdaS: true}
}

// sdaLine is the wired-AND bus level both parties observe.
func (b *Bus) sdaLine() bool { return b.sdaM && b.sdaS }

// ReadSDA returns the SDA line as seen on port $113B bit 0.
func (b *Bus) ReadSDA() bool { return b.sdaLine() }

// ReadSCL returns the SCL latch as seen on port $103B bit 0.
func (b *Bus) ReadSCL() bool { return b.scl }

// WriteSDA drives the master SDA latch (port $113B write, bit 0).
// START/STOP conditions are SDA transitions while SCL is high.
func (b *Bus) WriteSDA(bit bool) {
	prev := b.sdaLine()
	b.sdaM = bit
	now := b.sdaLine()
	if !b.scl || prev == now {
		return
	}
	if !now { // falling edge while SCL high = START (or repeated START)
		b.started = true
		b.phase = phaseAddr
		b.bitCnt = 0
		b.shift = 0
		b.sdaS = true
		return
	}
	// rising edge while SCL high = STOP
	b.started = false
	b.sdaS = true
}

// WriteSCL drives the master SCL latch (port $103B write, bit 0).
// The slave samples master bits on the rising edge and updates its
// own output after the falling edge (standard i2c timing).
func (b *Bus) WriteSCL(bit bool) {
	if bit == b.scl {
		return
	}
	b.scl = bit
	if !b.started {
		return
	}
	if bit {
		b.onRisingSCL()
	} else {
		b.onFallingSCL()
	}
}

func (b *Bus) onRisingSCL() {
	switch b.phase {
	case phaseAddr, phaseWrite:
		b.shift <<= 1
		if b.sdaLine() {
			b.shift |= 1
		}
		b.bitCnt++
	case phaseRead:
		b.bitCnt++ // master samples our bit; nothing to capture
	case phaseReadAck:
		// Master's ACK (low) asks for another byte; NAK (high) ends
		// the read. Record by switching phase now; output updates on
		// the next falling edge.
		if b.sdaLine() {
			b.phase = phaseIdleNak
		} else {
			b.regPtr = (b.regPtr + 1) & 0x3F // DS1307 pointer wraps at $40
			b.phase = phaseRead
			b.bitCnt = -1 // falling edge below loads the next byte
		}
	}
}

func (b *Bus) onFallingSCL() {
	switch b.phase {
	case phaseAddr:
		if b.bitCnt < 8 {
			return
		}
		if b.shift>>1 == ds1307Addr {
			b.reading = b.shift&1 != 0
			b.sdaS = false // ACK
			b.phase = phaseAddrAck
		} else {
			b.sdaS = true // not us — stay silent until STOP
			b.phase = phaseIdleNak
		}
	case phaseAddrAck:
		b.sdaS = true // release after the ACK clock
		b.bitCnt = 0
		b.shift = 0
		if b.reading {
			b.readData = b.rtc.Read(b.regPtr)
			b.phase = phaseRead
			b.driveReadBit()
		} else {
			b.phase = phaseWrite
			b.wrotePtr = false
		}
	case phaseWrite:
		if b.bitCnt < 8 {
			return
		}
		// The first written byte is the register pointer; subsequent
		// bytes are register data written at the pointer, which then
		// auto-increments (DS1307 sequential write). Clock registers
		// are ignored by the RTC model; the NVRAM is stored + persisted.
		if !b.wrotePtr {
			b.regPtr = b.shift & 0x3F
			b.wrotePtr = true
		} else {
			b.rtc.Write(b.regPtr, b.shift)
			b.regPtr = (b.regPtr + 1) & 0x3F
		}
		b.sdaS = false // ACK
		b.phase = phaseWriteAck
	case phaseWriteAck:
		b.sdaS = true
		b.bitCnt = 0
		b.shift = 0
		b.phase = phaseWrite
	case phaseRead:
		if b.bitCnt == -1 {
			// Continuing after the master's ACK: load and present
			// the next register byte.
			b.readData = b.rtc.Read(b.regPtr)
			b.bitCnt = 0
			b.driveReadBit()
			return
		}
		if b.bitCnt < 8 {
			b.driveReadBit()
			return
		}
		b.sdaS = true // release for the master's ACK/NAK clock
		b.phase = phaseReadAck
	case phaseIdleNak:
		b.sdaS = true
	}
}

// driveReadBit presents the current MSB of readData on SDA.
func (b *Bus) driveReadBit() {
	b.sdaS = b.readData&0x80 != 0
	b.readData <<= 1
}
