// Package dma implements the Spectrum Next's zxnDMA controller driven
// through its Z80-DMA-compatible command protocol on I/O port 0x6B.
//
// The controller is programmed by a stream of bytes written to port
// 0x6B. Each byte is either a base register byte (WR0..WR6) — whose bit
// pattern both selects the register group and flags which extra
// "follow" bytes come next — or one of those announced follow bytes, or
// (for WR6) a command byte: RESET / LOAD / ENABLE / .... A memory
// transfer runs when an ENABLE command arrives after a LOAD has latched
// the configured addresses.
//
// This replaces an earlier stub that assumed a fixed seven-byte command
// (src, length, dst, mode). That assumption is wrong: NextZXOS dot
// commands — e.g. the NextGuide ".guide" viewer — program the DMA with
// the real variable-length WR-register stream, so the stub mis-read the
// register bytes as an address and triggered a multi-kilobyte garbage
// transfer that corrupted RAM and crashed the viewer.
//
// Supported (the subset NextZXOS actually drives):
//
//   - WR0: transfer direction (A->B / B->A), port A start address,
//     block length
//   - WR1/WR2: port A/B address mode (increment / decrement / fixed),
//     with the optional variable-timing (+ prescaler) follow bytes
//     parsed and skipped
//   - WR4: port B start address (+ transfer-mode bits)
//   - WR6 commands: RESET ($C3), reset port A/B timing ($C7/$CB),
//     LOAD ($CF), ENABLE ($87); other status/continue commands are
//     accepted as no-ops
//
// Memory<->memory transfers run synchronously to completion. Not
// modelled: per-byte prescaler timing, I/O-port endpoints, the
// interrupt/match logic, and DMA-vs-CPU bus contention.
package dma

import (
	"fmt"
	"os"
)

// dmaTrace logs every port-0x6B byte to stderr when ZX_GO_DMA_TRACE is
// set — the diagnostic that pinned the command-stream mis-parse.
var dmaTrace = os.Getenv("ZX_GO_DMA_TRACE") != ""

func dmaLog(val byte) { fmt.Fprintf(os.Stderr, "DMA<-%02X\n", val) }

// Port address modes, decoded from a WR1/WR2 byte's bits 5..4.
const (
	addrDecrement byte = iota
	addrIncrement
	addrFixed
)

// MemoryBus is the contract DMA needs to move bytes between RAM
// locations. pkg/memory.Memory satisfies it.
type MemoryBus interface {
	Read(addr uint16) byte
	Write(addr uint16, val byte)
}

// IOBus is the contract DMA needs when a port is configured as an IO endpoint
// (WR1/WR2 D3 = 1) — e.g. DMA uploads to the sprite-image ($5B), Layer 2
// ($123B / $253B) or DAC ports. The 16-bit port number is the port's address
// register. pkg/ula.ULA satisfies it. May be nil; an IO endpoint with no bus
// degrades to a no-op read/write rather than corrupting memory.
type IOBus interface {
	ReadPort(port uint16) byte
	WritePort(port uint16, val byte)
}

// DMA is the zxnDMA controller: the configuration latched from the
// WR-register stream plus the small follow-byte state machine.
type DMA struct {
	mem MemoryBus
	io  IOBus

	portAStart uint16 // WR0: port A start address
	portBStart uint16 // WR4: port B start address
	blockLen   uint16 // WR0: block length (0 == 65536)
	aToB       bool   // WR0 bit 2: transfer port A -> port B
	aMode      byte   // WR1: port A address mode
	bMode      byte   // WR2: port B address mode
	aIsIO      bool   // WR1 D3: port A is an IO endpoint (else memory)
	bIsIO      bool   // WR2 D3: port B is an IO endpoint (else memory)
	loaded     bool   // a LOAD command has latched the addresses

	// Internal counters the chip exposes via the read mask. LOAD copies the
	// start addresses into curA/curB and zeroes counter; a transfer advances
	// them; Continue zeroes the counter without touching the pointers.
	curA, curB uint16
	counter    uint16

	// Timing (Slice 2 fills these in): per-port cycle length (2..4) and the
	// zxnDMA fixed-time prescaler, plus the transfer mode (continuous/burst).
	aCycleLen byte
	bCycleLen byte
	prescaler byte
	mode      byte

	autoRestart bool // WR5 D5: reload + repeat at end of block

	// lastDuration is the T-state cost of the most recent transfer, derived
	// from the per-byte cycle cost (read + write cycle lengths, or the
	// prescaler if larger). The emulator charges it to the CPU clock.
	lastDuration uint64

	// Read-back: a WR6 "read mask follows" ($BB) selects which of the seven
	// internal registers (status, byte-counter lo/hi, port A lo/hi, port B
	// lo/hi — bits 0..6) appear in the read sequence; IO reads of port 0x6B
	// return them in order, cycling. Power-up mask is 0x7F.
	readMask byte
	readIdx  int

	// pending holds the setters for the follow bytes the most recent
	// base byte announced; each subsequent WriteCommand consumes one.
	pending []func(byte)
}

// Transfer modes (WR4 D6:D5).
const (
	modeContinuous byte = iota
	modeBurst
)

// New returns a fresh DMA with no transfer queued.
func New(mem MemoryBus) *DMA {
	return &DMA{mem: mem, aCycleLen: 2, bCycleLen: 2, readMask: 0x7F}
}

// SetIOBus attaches the port bus used for IO endpoints. Optional.
func (d *DMA) SetIOBus(io IOBus) { d.io = io }

// WriteCommand accepts one byte of the port-0x6B command stream. Wired
// via ULA.SetNextDMA / the routing in ULA.WritePort.
func (d *DMA) WriteCommand(val byte) {
	if dmaTrace {
		dmaLog(val)
	}
	if len(d.pending) > 0 {
		f := d.pending[0]
		d.pending = d.pending[1:]
		f(val)
		return
	}
	d.decodeBase(val)
}

func setLow(p *uint16) func(byte)  { return func(v byte) { *p = (*p &^ 0x00FF) | uint16(v) } }
func setHigh(p *uint16) func(byte) { return func(v byte) { *p = (*p &^ 0xFF00) | uint16(v)<<8 } }
func ignore() func(byte)           { return func(byte) {} }

// addrMode decodes a WR1/WR2 byte's address-mode bits (D5 D4): bit 5 set
// = fixed; else bit 4 set = increment; else decrement.
func addrMode(val byte) byte {
	switch {
	case val&0x20 != 0:
		return addrFixed
	case val&0x10 != 0:
		return addrIncrement
	default:
		return addrDecrement
	}
}

// decodeBase interprets a base register byte and queues its follow
// bytes (or, for WR6, executes the command immediately).
func (d *DMA) decodeBase(val byte) {
	if val&0x80 == 0 {
		switch {
		case val&0x03 != 0: // WR0 — transfer setup
			d.aToB = val&0x04 != 0
			var p []func(byte)
			if val&0x08 != 0 {
				p = append(p, setLow(&d.portAStart))
			}
			if val&0x10 != 0 {
				p = append(p, setHigh(&d.portAStart))
			}
			if val&0x20 != 0 {
				p = append(p, setLow(&d.blockLen))
			}
			if val&0x40 != 0 {
				p = append(p, setHigh(&d.blockLen))
			}
			d.pending = p
		case val&0x07 == 0x04: // WR1 — port A config
			d.aMode = addrMode(val)
			d.aIsIO = val&0x08 != 0
			if val&0x40 != 0 { // variable-timing byte follows
				d.pending = []func(byte){d.aTimingByte()}
			}
		case val&0x07 == 0x00: // WR2 — port B config
			d.bMode = addrMode(val)
			d.bIsIO = val&0x08 != 0
			if val&0x40 != 0 {
				d.pending = []func(byte){d.bTimingByte()}
			}
		}
		return
	}
	switch val & 0x03 {
	case 0x00: // WR3 — match/mask (accepted; follow bytes skipped)
		var p []func(byte)
		if val&0x08 != 0 {
			p = append(p, ignore())
		}
		if val&0x10 != 0 {
			p = append(p, ignore())
		}
		d.pending = p
	case 0x01: // WR4 — port B address + transfer mode
		switch (val >> 5) & 0x03 {
		case 0x02: // 10 = burst
			d.mode = modeBurst
		default: // 01 continuous; 00/11 "do not use" behave continuous
			d.mode = modeContinuous
		}
		var p []func(byte)
		if val&0x04 != 0 {
			p = append(p, setLow(&d.portBStart))
		}
		if val&0x08 != 0 {
			p = append(p, setHigh(&d.portBStart))
		}
		if val&0x10 != 0 { // interrupt-control byte (with its own follows)
			p = append(p, d.interruptControl())
		}
		d.pending = p
	case 0x02: // WR5 — ready/wait/auto-restart (no follow bytes)
		d.autoRestart = val&0x20 != 0 // D5: 0 = stop on end, 1 = auto-restart
	case 0x03: // WR6 — command
		d.command(val)
	}
}

// cycleLen decodes a timing byte's D1:D0 into a read/write cycle count:
// 00=4, 01=3, 10=2 (11 "do not use" → treated as 2).
func cycleLen(v byte) byte {
	switch v & 0x03 {
	case 0x00:
		return 4
	case 0x01:
		return 3
	default:
		return 2
	}
}

// aTimingByte consumes the WR1 (port A) variable-timing byte, latching port A's
// cycle length. Port A has no prescaler.
func (d *DMA) aTimingByte() func(byte) {
	return func(v byte) { d.aCycleLen = cycleLen(v) }
}

// bTimingByte consumes the WR2 (port B) variable-timing byte, latching port B's
// cycle length; if its D5 is set, the zxnDMA fixed-time prescaler byte follows.
func (d *DMA) bTimingByte() func(byte) {
	return func(v byte) {
		d.bCycleLen = cycleLen(v)
		if v&0x20 != 0 {
			d.pending = append(d.pending, func(p byte) { d.prescaler = p })
		}
	}
}

// interruptControl consumes a WR4 interrupt-control byte and its
// optional pulse-offset / vector follow bytes (D3 / D4).
func (d *DMA) interruptControl() func(byte) {
	return func(v byte) {
		if v&0x08 != 0 {
			d.pending = append(d.pending, ignore()) // pulse offset
		}
		if v&0x10 != 0 {
			d.pending = append(d.pending, ignore()) // interrupt vector
		}
	}
}

// command executes a WR6 command byte.
func (d *DMA) command(val byte) {
	switch val {
	case 0xC3: // RESET — clear configuration + state machine (keep the buses)
		*d = DMA{mem: d.mem, io: d.io, aCycleLen: 2, bCycleLen: 2, readMask: 0x7F}
	case 0xCF: // LOAD — latch the start addresses into the internal pointers
		d.loaded = true
		d.curA = d.portAStart
		d.curB = d.portBStart
		d.counter = 0
	case 0xD3: // CONTINUE — zero the byte counter; a following ENABLE repeats
		// the block from the CURRENT pointers (not the start addresses).
		d.loaded = true
		d.counter = 0
	case 0x87: // ENABLE — run the configured transfer
		if d.loaded {
			d.Trigger()
		}
	case 0xBB: // READ MASK FOLLOWS — next byte sets the read mask
		d.pending = []func(byte){func(m byte) {
			d.readMask = m & 0x7F
			d.readIdx = 0
		}}
	case 0xA7: // INITIATE READ SEQUENCE — reset the read cursor
		d.readIdx = 0
	default:
		// $C7/$CB reset-timing, $83 disable, status-reinit commands need no
		// state change for the transfers the zxnDMA actually runs.
	}
}

// Trigger runs the configured transfer to completion from the current internal
// pointers. Block length 0 means 65536 per the zxnDMA convention. Each byte is
// read from the source endpoint (memory or IO) and written to the destination
// endpoint, advancing each port's pointer per its address mode. On end of block
// the DMA either auto-restarts (reload start addresses, stay armed) or clears
// its loaded flag.
func (d *DMA) Trigger() {
	length := int(d.blockLen)
	if length == 0 {
		length = 65536
	}
	srcIsA := d.aToB // port A is the source when transferring A -> B
	if dmaTrace {
		fmt.Fprintf(os.Stderr, "DMA xfer A=%04X B=%04X len=%d aIO=%v bIO=%v mode=%d presc=%d\n",
			d.curA, d.curB, length, d.aIsIO, d.bIsIO, d.mode, d.prescaler)
	}
	moved := length - int(d.counter)
	for remaining := moved; remaining > 0; remaining-- {
		b := d.portRead(srcIsA)
		d.portWrite(!srcIsA, b)
		d.counter++
	}
	d.lastDuration = uint64(moved) * d.perByteCycles()
	if d.autoRestart {
		d.curA = d.portAStart
		d.curB = d.portBStart
		d.counter = 0
	} else {
		d.loaded = false
	}
}

// portRead reads one byte from port A (a=true) or port B, from memory or IO per
// that port's configuration, then advances the port's pointer.
func (d *DMA) portRead(a bool) byte {
	if a {
		v := d.endpointRead(d.aIsIO, d.curA)
		d.curA = stepAddr(d.curA, d.aMode)
		return v
	}
	v := d.endpointRead(d.bIsIO, d.curB)
	d.curB = stepAddr(d.curB, d.bMode)
	return v
}

// portWrite writes one byte to port A (a=true) or port B, then advances its
// pointer.
func (d *DMA) portWrite(a bool, val byte) {
	if a {
		d.endpointWrite(d.aIsIO, d.curA, val)
		d.curA = stepAddr(d.curA, d.aMode)
		return
	}
	d.endpointWrite(d.bIsIO, d.curB, val)
	d.curB = stepAddr(d.curB, d.bMode)
}

func (d *DMA) endpointRead(isIO bool, addr uint16) byte {
	if isIO {
		if d.io != nil {
			return d.io.ReadPort(addr)
		}
		return 0xFF
	}
	return d.mem.Read(addr)
}

func (d *DMA) endpointWrite(isIO bool, addr uint16, val byte) {
	if isIO {
		if d.io != nil {
			d.io.WritePort(addr, val)
		}
		return
	}
	d.mem.Write(addr, val)
}

// perByteCycles is the T-state cost of moving one byte: the source read cycle
// length plus the destination write cycle length, or the fixed-time prescaler
// if it is larger (zxnDMA "the transfer takes at least <prescaler> cycles per
// byte" — the sampled-audio feature).
func (d *DMA) perByteCycles() uint64 {
	srcCyc, dstCyc := d.aCycleLen, d.bCycleLen
	if !d.aToB { // B is the source
		srcCyc, dstCyc = d.bCycleLen, d.aCycleLen
	}
	per := uint64(srcCyc) + uint64(dstCyc)
	if uint64(d.prescaler) > per {
		per = uint64(d.prescaler)
	}
	return per
}

func stepAddr(a uint16, mode byte) uint16 {
	switch mode {
	case addrIncrement:
		return a + 1
	case addrDecrement:
		return a - 1
	default: // addrFixed
		return a
	}
}

// Source / Destination / Length — accessors for tests. Source is the
// port A start address, Destination the port B start address.
func (d *DMA) Source() uint16      { return d.portAStart }
func (d *DMA) Destination() uint16 { return d.portBStart }
func (d *DMA) Length() uint16      { return d.blockLen }

// ByteCounter / CurrentA / CurrentB expose the chip's internal counters (the
// values the read mask returns): bytes transferred in the current operation and
// the live port A / port B pointers.
func (d *DMA) ByteCounter() uint16 { return d.counter }
func (d *DMA) CurrentA() uint16    { return d.curA }
func (d *DMA) CurrentB() uint16    { return d.curB }

// Duration returns the T-state cost of the most recent transfer (per-byte cycle
// cost × bytes moved). The emulator charges this to the CPU clock so a
// continuous-mode transfer consumes the right amount of time.
func (d *DMA) Duration() uint64 { return d.lastDuration }

// Mode returns the transfer mode (continuous or burst) from the last WR4 write.
func (d *DMA) Mode() byte { return d.mode }

// ReadCommand returns the next register value in the read sequence (an IO read
// of port 0x6B), advancing and wrapping the cursor. The read mask selects which
// of the seven registers participate. If the mask is empty it returns 0xFF.
func (d *DMA) ReadCommand() byte {
	regs := d.readSequence()
	if len(regs) == 0 {
		return 0xFF
	}
	if d.readIdx >= len(regs) {
		d.readIdx = 0
	}
	v := d.regValue(regs[d.readIdx])
	d.readIdx++
	if d.readIdx >= len(regs) {
		d.readIdx = 0
	}
	return v
}

// readSequence returns the register indices (0..6) selected by the read mask,
// in ascending order.
func (d *DMA) readSequence() []int {
	var seq []int
	for bit := 0; bit < 7; bit++ {
		if d.readMask&(1<<bit) != 0 {
			seq = append(seq, bit)
		}
	}
	return seq
}

// regValue returns the current value of read-mask register reg:
// 0=status, 1/2=byte counter lo/hi, 3/4=port A addr lo/hi, 5/6=port B addr lo/hi.
func (d *DMA) regValue(reg int) byte {
	switch reg {
	case 1:
		return byte(d.counter)
	case 2:
		return byte(d.counter >> 8)
	case 3:
		return byte(d.curA)
	case 4:
		return byte(d.curA >> 8)
	case 5:
		return byte(d.curB)
	case 6:
		return byte(d.curB >> 8)
	default: // 0 = status byte (not implemented on the zxnDMA per its docs)
		return 0
	}
}
