package next

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/uart"
)

// UARTSystem is the Next's UART register block as the machine sees it: the
// four ports, the 28 MHz clock, the soft reset, and the four interrupt request
// lines into the IM2 chain. The register block itself, and the ESP8266 module
// behind UART 0, are pkg/next/uart.
//
// The ports are decoded as zxnext.vhd:2639 has them. A15..A11 must be 00010,
// A10 xor (A9 and A8) must be 1 and the low byte $3B, which claims $133B,
// $143B, $153B and $163B and not $173B. A9:8 is the register: 0 Rx and
// prescaler, 1 select, 2 frame, 3 Tx and status. The gate is NR$83 bit 4,
// internal_port_enable(12) (:2420).
//
// Time. The block runs on the free-running 28 MHz clock (the same one as the
// CTC), and the machine advances it from the CPU's instruction boundaries:
// tstates * 8 / speed clocks per instruction, where speed is the CPU's
// multiplier. Within an instruction the order of events is therefore not
// resolved, only their order relative to the instruction. That is the same
// granularity the CTC and the IM2 chain have, and it is far finer than the
// serial timing the UART exists to keep: a bit at 115200 baud is 243 clocks,
// longer than any instruction.
//
// A register access is the exception, and is played out clock by clock: the
// strobe is held for the two T-states an I/O cycle gives it, the block is
// clocked through them, and the read is sampled on the last. Those clocks are
// counted against the instruction's own, so time does not run twice.
type UARTSystem struct {
	// Block is the register block; exported for tests and the debugger.
	Block *uart.Block

	d     *nextregs.Dispatcher
	im2   *IM2Driver
	speed func() int
	owed  int     // clocks already spent inside register accesses
	req   [4]bool // last request levels given to the chain
}

// NewUARTSystem builds the system with an ESP module on UART 0. speed returns
// the CPU speed multiplier (1, 2, 4 or 8) and is read at each access.
func NewUARTSystem(d *nextregs.Dispatcher, im2 *IM2Driver, speed func() int) *UARTSystem {
	s := &UARTSystem{Block: uart.NewBlock(), d: d, im2: im2, speed: speed}
	s.Block.SetPeer(uart.NewESP())
	s.Reset()
	// The Rx request depends on NR$C6 bit 1 (zxnext.vhd:1942-1943), so a write
	// to it can change a line without the block having moved.
	chainOnWrite(d, 0xC6, func(byte) { s.syncIM2() })
	// NR$02 bits 1:0 are the soft and hard reset triggers. i_reset_hard is tied
	// low, so both reach the block as i_reset only.
	chainOnWrite(d, 0x02, func(v byte) {
		if v&0x03 != 0 {
			s.Reset()
		}
	})
	return s
}

// Reset pulses i_reset for two clocks: the FIFOs empty, both PHYs go idle and
// the select register returns to UART 0. The baud and frame registers keep
// their values, which is the hardware's behaviour (i_reset_hard is tied low,
// zxnext.vhd:3361).
func (s *UARTSystem) Reset() {
	s.Block.SetReset(true)
	s.Block.Clock()
	s.Block.Clock()
	s.Block.SetReset(false)
	s.syncIM2()
}

// decode returns the register a port address selects, or false.
func (s *UARTSystem) decode(addr uint16) (uint8, bool) {
	if addr&0xFF != 0x3B || addr>>11 != 0b00010 {
		return 0, false
	}
	a10, a9, a8 := addr>>10&1, addr>>9&1, addr>>8&1
	if a10^(a9&a8) != 1 {
		return 0, false
	}
	if s.d.Raw(0x83)&0x10 == 0 {
		return 0, false
	}
	return uint8(addr >> 8 & 3), true
}

// ioClocks is how many 28 MHz clocks an I/O cycle holds the strobe: two
// T-states of the CPU clock.
func (s *UARTSystem) ioClocks() int {
	sp := s.speed()
	if sp < 1 {
		sp = 1
	}
	return 16 / sp
}

// ReadPort handles an IN from the UART ports.
func (s *UARTSystem) ReadPort(addr uint16) (byte, bool) {
	reg, ok := s.decode(addr)
	if !ok {
		return 0, false
	}
	s.Block.SetIO(reg, true, false, 0)
	n := s.ioClocks()
	for i := 0; i < n; i++ {
		s.Block.Clock()
	}
	v := s.Block.CPUData()
	s.Block.SetIO(reg, false, false, 0)
	s.owed += n
	s.syncIM2()
	return v, true
}

// WritePort handles an OUT to the UART ports.
func (s *UARTSystem) WritePort(addr uint16, val byte) bool {
	reg, ok := s.decode(addr)
	if !ok {
		return false
	}
	s.Block.SetIO(reg, false, true, val)
	n := s.ioClocks()
	for i := 0; i < n; i++ {
		s.Block.Clock()
		s.syncIM2()
	}
	s.Block.SetIO(reg, false, false, val)
	s.owed += n
	s.syncIM2()
	return true
}

// Advance runs the block for one instruction's worth of 28 MHz clocks.
func (s *UARTSystem) Advance(tstates, speed int) {
	if speed < 1 {
		speed = 1
	}
	n := tstates*8/speed - s.owed
	s.owed = 0
	if n < 0 {
		s.owed = -n
		n = 0
	}
	s.Block.Advance(n, nil)
	s.syncIM2()
}

// syncIM2 drives the four request lines from the block's registered outputs,
// as zxnext.vhd:1941-1943 builds im2_int_req. The Rx line is the three-quarters
// full flag or the avail flag, the avail term dropping out when NR$C6 bit 1
// (bit 5 for UART 1) selects the three-quarters-full condition alone. The Tx
// line is the Tx FIFO empty flag. These are levels, held, so the chain is
// given a change only when one occurs.
//
// It runs after every Clock of a register write, and once after a read and
// after each instruction's Advance. The chain latches a rising edge, and the
// Tx FIFO's empty flag drops for only a few clocks when a byte is written and
// the transmitter takes it, which is the edge a Tx interrupt is made of: that
// dip lies wholly inside the write access, so a write has to be watched clock
// by clock. Nothing else moves a request line down and up again inside one
// instruction. A read only lowers Rx flags, and an Advance only ever raises
// them or lets the Tx FIFO empty, so the level at the end is what the chain
// would have seen.
func (s *UARTSystem) syncIM2() {
	if s.im2 == nil {
		return
	}
	c6 := s.d.Raw(0xC6)
	b := s.Block
	lines := [4]bool{
		b.RxNearFull(0) || (b.RxAvail(0) && c6&0x02 == 0),
		b.RxNearFull(1) || (b.RxAvail(1) && c6&0x20 == 0),
		b.TxEmpty(0),
		b.TxEmpty(1),
	}
	srcs := [4]int{IM2SourceUART0R, IM2SourceUART1R, IM2SourceUART0T, IM2SourceUART1T}
	for i, on := range lines {
		if on == s.req[i] {
			continue
		}
		s.req[i] = on
		if on {
			s.im2.Raise(srcs[i])
		} else {
			s.im2.Lower(srcs[i])
		}
	}
}

type uartSysState struct {
	Block []byte
	Owed  int
	Req   [4]bool
}

// StateID identifies the system in a captured machine state.
func (s *UARTSystem) StateID() string { return "next.uartsys" }

// SaveState captures the block, the ESP behind it and the request levels.
func (s *UARTSystem) SaveState() []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(uartSysState{s.Block.SaveState(), s.owed, s.req}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a capture.
func (s *UARTSystem) LoadState(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("uartsys: empty state (the capture failed)")
	}
	var st uartSysState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&st); err != nil {
		return fmt.Errorf("uartsys: decoding state: %w", err)
	}
	if err := s.Block.LoadState(st.Block); err != nil {
		return err
	}
	s.owed = st.Owed
	s.req = st.Req
	return nil
}
