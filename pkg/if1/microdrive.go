// Package if1 implements the Sinclair ZX Interface 1 — the Spectrum
// 48K's external bus expansion that adds 8 microdrive slots, an RS-232
// serial port, and the ZX Net network interface.
//
// The architecture mirrors the FUSE emulator's peripherals/if1.c
// almost line-for-line so the behaviour interoperates with the same
// .mdr cartridge images and the same shadow ROM. The package is
// internally split into:
//
//   - microdrive.go: per-drive state and the tape-loop simulation
//   - ula.go:        the IF1 ULA's three I/O port handlers
//   - if1.go:        the top-level Interface 1 type, ROM paging,
//     integration with the existing peripheral manager
package if1

import (
	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

// NumDrives is the number of microdrive slots in a real Interface 1
// configuration. The IF1 hardware allows up to 8 daisy-chained drives;
// most home setups had 1 or 2 attached.
const NumDrives = 8

// preamble byte values used by the IF1 ROM to mark a block start.
// Block writes lay down 10 × 0x00 followed by 2 × 0xFF; the controller
// recognises this pattern in the per-block "pream" tracking array as
// the "this block is now formatted" sentinel.
const (
	syncNo = 0    // not yet formatted
	syncOK = 0xFF // 12-byte preamble seen — block is formatted
)

// Drive is the per-microdrive state. Mirrors microdrive_t in
// fuse-1.6.0/peripherals/if1.c:66-82.
//
// At the hardware level a microdrive is just a tape loop: there's a
// single read/write head, and bytes stream past it as the tape moves.
// We model the tape with `headPos` (a byte offset into the cartridge,
// modulo the cartridge length) and bump it on every read/write.
type Drive struct {
	Cartridge *microdrive.Cartridge

	Inserted bool // a cartridge is in the slot
	Modified bool // the cartridge has been written to since insert
	MotorOn  bool // motor active — only one drive should be motorOn at a time

	headPos     int  // absolute byte offset into the cartridge
	transferred int  // bytes consumed/produced since the last block boundary
	maxBytes    int  // size of the current "window" — 15 (header) or 528 (record + data)
	gap         byte // GAP countdown (read by CTR port)
	sync        byte // SYNC countdown (read by CTR port)

	// lastByte is the MDR data port's read latch: the last byte
	// actually shifted in from the tape. It is only updated while
	// transferred < maxBytes; reads past the end of the window keep
	// returning this latched value instead of going idle, since the
	// real ULA's parallel latch isn't cleared between transfers.
	lastByte byte

	// pream tracks formatting state for each of the 512 possible
	// (header + record) slots in the cartridge. Index 0..255 covers
	// the block headers; 256..511 covers the record headers. Values:
	//
	//	syncNo (0)   — not formatted
	//	1..11        — preamble bytes seen so far during a write
	//	syncOK (255) — full 12-byte preamble seen, block is "real"
	//
	// The IF1 ROM walks the tape looking for syncOK to know which
	// blocks contain real data; un-syncOK blocks read as 0xFF garbage.
	pream [512]byte
}

// currentBlock returns the index of the block under the head.
func (d *Drive) currentBlock() int {
	if d.Cartridge == nil || d.Cartridge.Length() == 0 {
		return 0
	}
	return d.headPos / microdrive.BlockLen
}

// preambleSlot returns the index into d.pream for the current head
// position. Slots 0..MaxBlocks-1 cover block headers; slots
// MaxBlocks..2*MaxBlocks-1 cover record (data) headers.
//
// FUSE uses `head_pos / 543 + (max_bytes == 15 ? 0 : 256)` — the
// "is this a header window" condition is whether maxBytes equals 15
// (i.e. we're in a header window, not a data window).
func (d *Drive) preambleSlot() int {
	base := d.currentBlock()
	if d.maxBytes != microdrive.HeadLen {
		base += 256
	}
	return base
}

// incrementHead moves the head one byte forward, wrapping around at
// the end of the cartridge (it's a tape loop). Mirrors increment_head
// at fuse-1.6.0/peripherals/if1.c:1037-1045.
func (d *Drive) incrementHead() {
	if d.Cartridge == nil || d.Cartridge.Length() == 0 {
		return
	}
	d.headPos++
	if d.headPos >= d.Cartridge.Length()*microdrive.BlockLen {
		d.headPos = 0
	}
}

// restart aligns the head to the next block boundary or header
// boundary, then resets the transferred counter and recomputes the
// window size. Called by the IF1 ULA after every CTR or NET access
// (i.e. between operations on the tape) — see microdrives_restart
// at fuse-1.6.0/peripherals/if1.c:1047-1065.
//
// IMPORTANT: this does NOT reset the GAP / SYNC countdowns. The
// IF1 ROM polls the CTR port hundreds of times waiting for the
// GAP→SYNC→GAP pulse pattern; if restart() reset the counters on
// every poll they'd never reach zero and the IF1 ROM would report
// "Microdrive not present" even with a valid formatted cartridge.
// FUSE's microdrives_restart deliberately omits the reset for the
// same reason.
//
// The IF1 ROM "scans" the tape by reading short bursts in CTR mode
// and then asking the controller to advance to a usable position.
// The next valid window after any head position is one of:
//
//   - the same position, if it's already at offset 0 (header window)
//   - the same position, if it's already at offset 15 (record window)
//   - offset 15 inside the same block, if we're between 1 and 14
//   - offset 0 of the NEXT block, if we're past offset 15
//
// We compute that directly with arithmetic instead of looping
// incrementHead() up to ~543 times.
func (d *Drive) restart() {
	if d.Cartridge == nil || d.Cartridge.Length() == 0 {
		return
	}
	off := d.headPos % microdrive.BlockLen
	switch {
	case off == 0:
		// Already at start of a header window — nothing to do.
	case off < microdrive.HeadLen:
		// In the middle of a header — advance to the start of
		// the record window in the same block.
		d.headPos += microdrive.HeadLen - off
	case off == microdrive.HeadLen:
		// Already at start of a record window — nothing to do.
	default:
		// Past the record window — wrap to the next block start.
		d.headPos += microdrive.BlockLen - off
		if d.headPos >= d.Cartridge.Length()*microdrive.BlockLen {
			d.headPos = 0
		}
	}
	d.transferred = 0
	if d.headPos%microdrive.BlockLen == 0 {
		d.maxBytes = microdrive.HeadLen
	} else {
		d.maxBytes = microdrive.HeadLen + microdrive.DataLen + 1
	}
}

// reset returns the drive to the initial post-power-on state. The
// cartridge stays inserted; only the transient runtime state is
// cleared. Mirrors microdrives_reset at fuse-1.6.0/peripherals/if1.c:516-542.
func (d *Drive) reset() {
	d.headPos = 0
	d.MotorOn = false
	d.gap = 15
	d.sync = 15
	d.transferred = 0
}

// readByte reads one byte from the tape head and advances the head.
// Returns the byte; the controller's port_mdr_in code combines this
// with the bytes from any other motor-on drives via AND (negative
// logic on the bus). Returns 0xFF when the drive isn't streaming at
// all (uninserted or motor off — the idle bus value, the AND
// identity). Once the current window's maxBytes have been
// transferred, further reads return the latched lastByte rather than
// going idle — see the lastByte field comment.
func (d *Drive) readByte() byte {
	if !d.Inserted || !d.MotorOn || d.Cartridge == nil {
		return 0xFF
	}
	if d.transferred < d.maxBytes {
		d.lastByte = d.Cartridge.DataAt(d.headPos)
		d.incrementHead()
	}
	d.transferred++
	return d.lastByte
}

// writeByte processes one byte coming in from the CPU. The IF1 ROM
// writes the 12-byte preamble (10 × 0x00 + 2 × 0xFF) before each
// header or record, then the actual header / data bytes. We track the
// preamble pattern in d.pream[block] so the controller knows which
// blocks have been "formatted" by the ROM. Mirrors port_mdr_out at
// fuse-1.6.0/peripherals/if1.c:803-839.
func (d *Drive) writeByte(b byte) {
	if !d.Inserted || !d.MotorOn || d.Cartridge == nil {
		return
	}
	slot := d.preambleSlot()

	switch {
	case d.transferred == 0 && b == 0x00:
		d.pream[slot] = 1
	case d.transferred > 0 && d.transferred < 10 && b == 0x00:
		d.pream[slot]++
	case d.transferred > 9 && d.transferred < 12 && b == 0xFF:
		d.pream[slot]++
	case d.transferred == 12 && d.pream[slot] == 12:
		d.pream[slot] = syncOK
	}

	if d.transferred > 11 && d.transferred < d.maxBytes+12 {
		d.Cartridge.SetDataAt(d.headPos, b)
		d.incrementHead()
		d.Modified = true
	}
	d.transferred++
}

// statusBits returns the bits this drive contributes to the CTR port
// status read. The IF1 ULA ANDs all motor-on drives' contributions
// together (active-low bus convention).
//
// Layout (from fuse-1.6.0/peripherals/if1.c:114-117):
//
//	bit 0 — write protect (1 = NOT protected)
//	bit 1 — SYNC (low for the SYNC portion of a block, high otherwise)
//	bit 2 — GAP  (low for the GAP portion of a block, high otherwise)
//
// The drive walks GAP/SYNC counters down on each CTR read so the IF1
// ROM sees alternating GAP and SYNC patterns as it polls.
//
// Returns 0xFF when the drive is idle (motor off, no cartridge); the
// AND-merge in the controller leaves those bits high.
func (d *Drive) statusBits() byte {
	if !d.MotorOn || !d.Inserted || d.Cartridge == nil {
		return 0xFF
	}
	ret := byte(0xFF)
	slot := d.preambleSlot()
	if d.pream[slot] == syncOK {
		// Formatted block — produce GAP and SYNC pulses for the IF1
		// ROM's tape-scanning state machine.
		if d.gap > 0 {
			d.gap--
			// In GAP region — both GAP and SYNC bits high.
		} else {
			ret &= 0xF9 // bits 1 and 2 low: GAP and SYNC
			if d.sync > 0 {
				d.sync--
			} else {
				d.gap = 15
				d.sync = 15
			}
		}
	}
	if d.Cartridge.WriteProtect() {
		ret &^= 0x01 // bit 0 low when write-protected
	}
	return ret
}

// MicrodriveBus owns the 8 daisy-chained drives plus the rotation
// logic the IF1 ULA uses to walk the motor signal through them. It is
// the "controller" half of the IF1; the ULA half lives in ula.go.
type MicrodriveBus struct {
	drives [NumDrives]Drive
}

// NewMicrodriveBus creates a fresh bus with no cartridges inserted
// and all drives idle. Each slot starts with a freshly-allocated
// blank Cartridge so the per-drive state has a place to point — the
// real cartridge is swapped in by Insert.
func NewMicrodriveBus() *MicrodriveBus {
	bus := &MicrodriveBus{}
	for i := range bus.drives {
		bus.drives[i].reset()
	}
	return bus
}

// Drive returns a pointer to the i-th drive (0-based). Out-of-range
// indices return nil.
func (b *MicrodriveBus) Drive(i int) *Drive {
	if i < 0 || i >= NumDrives {
		return nil
	}
	return &b.drives[i]
}

// Insert places the supplied cartridge into drive i, ejecting any
// existing cartridge first. Calls restart() at the end so the drive
// is in a consistent state (maxBytes set, head aligned) even before
// the host issues its first CTR access.
//
// The per-block pream[] markers are seeded with syncOK for every
// block in the cartridge — "we assume formatted cartridges", in
// FUSE's words at if1.c:1168. Without this, the IF1 ROM walks the
// tape looking for syncOK markers (the "this block was formatted
// by the microdrive" sentinel), doesn't find any, and reports
// "Microdrive not present" — even though the cartridge data is
// all there.
func (b *MicrodriveBus) Insert(i int, c *microdrive.Cartridge) {
	d := b.Drive(i)
	if d == nil || c == nil {
		return
	}
	d.Cartridge = c
	d.Inserted = true
	d.Modified = false
	d.headPos = 0
	d.pream = [512]byte{}
	// Assume formatted: mark every block's header and record
	// window as syncOK so the IF1 ROM's tape-scanning state
	// machine reports "drive present" on the CTR port.
	for k := 0; k < c.Length(); k++ {
		d.pream[k] = syncOK     // block-header window
		d.pream[256+k] = syncOK // record window
	}
	d.restart()
}

// Eject removes the cartridge from drive i. Subsequent reads from
// the slot return 0xFF.
func (b *MicrodriveBus) Eject(i int) {
	d := b.Drive(i)
	if d == nil {
		return
	}
	d.Cartridge = nil
	d.Inserted = false
	d.Modified = false
	d.MotorOn = false
}

// Reset clears transient state on all drives but leaves cartridges
// in place. Called from the system reset path.
func (b *MicrodriveBus) Reset() {
	for i := range b.drives {
		b.drives[i].reset()
	}
}

// AdvanceMotor rotates the motor-on bit through the daisy chain.
// Called by the IF1 ULA when it sees a falling edge on the comms
// clock with comms-data low (the IF1's "select next drive" signal).
// Mirrors the motor-rotation loop in port_ctr_out at
// fuse-1.6.0/peripherals/if1.c:846-867.
func (b *MicrodriveBus) AdvanceMotor(motorOn bool) {
	for i := NumDrives - 1; i > 0; i-- {
		b.drives[i].MotorOn = b.drives[i-1].MotorOn
	}
	b.drives[0].MotorOn = motorOn
}

// AnyMotorOn reports whether any drive is currently spinning. Used
// by the UI to flash a "drive active" indicator.
func (b *MicrodriveBus) AnyMotorOn() bool {
	for i := range b.drives {
		if b.drives[i].MotorOn {
			return true
		}
	}
	return false
}

// RestartAll calls restart() on every drive. The IF1 ULA does this
// after every CTR or NET port access — once the host has finished a
// command, the drives prepare for the next block boundary.
func (b *MicrodriveBus) RestartAll() {
	for i := range b.drives {
		b.drives[i].restart()
	}
}

// ReadDataPort reads one byte from the MDR data port. The IF1 ULA
// returns the AND of all motor-on drives' bytes (active-low bus).
// Mirrors port_mdr_in at fuse-1.6.0/peripherals/if1.c:555-580.
func (b *MicrodriveBus) ReadDataPort() byte {
	ret := byte(0xFF)
	for i := range b.drives {
		ret &= b.drives[i].readByte()
	}
	return ret
}

// WriteDataPort writes one byte to the MDR data port. Every motor-on
// drive sees the write. Mirrors port_mdr_out at if1.c:803-839.
func (b *MicrodriveBus) WriteDataPort(val byte) {
	for i := range b.drives {
		b.drives[i].writeByte(val)
	}
}

// ReadStatusPort returns the AND of all drives' status bits, plus the
// caller-supplied bus-wide bits (DTR, BUSY) ORed in by the ULA.
// Mirrors the drive loop in port_ctr_in at if1.c:585-612.
func (b *MicrodriveBus) ReadStatusPort() byte {
	ret := byte(0xFF)
	for i := range b.drives {
		ret &= b.drives[i].statusBits()
	}
	return ret
}
