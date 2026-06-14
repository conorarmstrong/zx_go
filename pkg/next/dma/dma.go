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

// DMA is the zxnDMA controller: the configuration latched from the
// WR-register stream plus the small follow-byte state machine.
type DMA struct {
	mem MemoryBus

	portAStart uint16 // WR0: port A start address
	portBStart uint16 // WR4: port B start address
	blockLen   uint16 // WR0: block length (0 == 65536)
	aToB       bool   // WR0 bit 2: transfer port A -> port B
	aMode      byte   // WR1: port A address mode
	bMode      byte   // WR2: port B address mode
	loaded     bool   // a LOAD command has latched the addresses

	// pending holds the setters for the follow bytes the most recent
	// base byte announced; each subsequent WriteCommand consumes one.
	pending []func(byte)
}

// New returns a fresh DMA with no transfer queued.
func New(mem MemoryBus) *DMA { return &DMA{mem: mem} }

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
			if val&0x40 != 0 { // variable-timing byte follows
				d.pending = []func(byte){d.timingByte()}
			}
		case val&0x07 == 0x00: // WR2 — port B config
			d.bMode = addrMode(val)
			if val&0x40 != 0 {
				d.pending = []func(byte){d.timingByte()}
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
	case 0x03: // WR6 — command
		d.command(val)
	}
}

// timingByte consumes a WR1/WR2 variable-timing byte; if its D5 is set
// the zxnDMA expects a prescaler byte to follow as well.
func (d *DMA) timingByte() func(byte) {
	return func(v byte) {
		if v&0x20 != 0 {
			d.pending = append(d.pending, ignore()) // prescaler
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
	case 0xC3: // RESET — clear configuration + state machine
		mem := d.mem
		*d = DMA{mem: mem}
	case 0xCF: // LOAD — latch the start addresses into the counters
		d.loaded = true
	case 0x87: // ENABLE — run the configured transfer
		if d.loaded {
			d.Trigger()
		}
	default:
		// $C7/$CB reset-timing, $83 disable, $D3 continue, $BF/$B3/...
		// status commands need no state change for mem<->mem transfers.
	}
}

// Trigger runs the currently-configured transfer to completion. Block
// length 0 means 65536 per the zxnDMA convention.
func (d *DMA) Trigger() {
	length := int(d.blockLen)
	if length == 0 {
		length = 65536
	}
	src, dst := d.portAStart, d.portBStart
	sMode, dMode := d.aMode, d.bMode
	if !d.aToB { // B -> A
		src, dst = d.portBStart, d.portAStart
		sMode, dMode = d.bMode, d.aMode
	}
	if dmaTrace {
		fmt.Fprintf(os.Stderr, "DMA xfer src=%04X dst=%04X len=%d sMode=%d dMode=%d\n",
			src, dst, length, sMode, dMode)
	}
	for i := 0; i < length; i++ {
		d.mem.Write(dst, d.mem.Read(src))
		src = stepAddr(src, sMode)
		dst = stepAddr(dst, dMode)
	}
	d.loaded = false
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
