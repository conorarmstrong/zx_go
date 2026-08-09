package opus

// ROM paging.
//
// The Opus Discovery overlays its 8 KB ROM on the Spectrum ROM, and switches
// on an M1 address trap: the hardware watches the address bus during an
// opcode fetch and flips the overlay. The flip is DELAYED BY ONE M1 CYCLE —
// two flip-flops, the first recognising the address and the second clocking
// the state on the following fetch — so the instruction sitting AT a trap
// address always executes out of whichever ROM was already paged.
//
// The v2.2 ROM depends on that delay in both directions:
//
//	$0008  RST 8. The Spectrum's LD HL,(CH_ADD) runs, then the Opus ROM is in
//	       for $000B (ENTRY_1).
//	$1708  The Spectrum's INC HL runs, then the Opus ROM is in for $1709,
//	       which holds a compensating DEC HL. The Opus byte at $1708 is a NOP
//	       standing in for the Spectrum instruction that really executes.
//	$1748  Page out. The Opus ROM holds a bare RET here; the RET is fetched
//	       from the Opus ROM and the Spectrum ROM is back for the return
//	       address.
const (
	// RST8 is the error restart the Opus hooks to catch hook codes.
	RST8 = 0x0008
	// KeyInt is the Spectrum's KEY-INT, reached from the frame interrupt
	// handler. The Opus hooks it to initialise its 2 KB SRAM on the first
	// interrupt after power-on and to run its per-frame housekeeping, then
	// returns to $0049 in the Spectrum ROM.
	//
	// It carries the same placeholder signature as $1708: the Opus byte at
	// $0048 is C5, identical to the Spectrum's PUSH BC there, because the
	// delay means the SPECTRUM's instruction is the one that executes. The
	// Opus ROM only diverges from $0049 (PUSH HL) onwards.
	KeyInt = 0x0048
	// PageIn is the interface's entry point, also reached from the Spectrum
	// ROM's CLOSE_2.
	PageIn = 0x1708
	// PageOut is the bare RET whose address the hardware detects.
	PageOut = 0x1748
)

// Pager is the ROM overlay state machine. The zero value is paged out; call
// Reset for the power-on state.
type Pager struct {
	active bool
	armed  bool
	arming bool // what active becomes when the armed change lands
}

// Reset returns the pager to power-on: the Opus ROM is paged in, so the Z80
// starts on the Opus reset vector at $0000, which sets a stack whose top word
// is zero and jumps to PageOut — paging itself out and returning to $0000 in
// the Spectrum ROM.
func (p *Pager) Reset() {
	p.active, p.armed, p.arming = true, false, false
}

// Active reports whether the Opus ROM is currently over the Spectrum ROM.
func (p *Pager) Active() bool { return p.active }

// SetActive forces the overlay state, for save-state restore.
func (p *Pager) SetActive(a bool) { p.active, p.armed = a, false }

// PreFetch advances the trap for an opcode fetch at pc. Call it from the
// CPU's pre-fetch hook, before the byte at pc is read, so that any change
// armed by the PREVIOUS fetch is in force for this one.
func (p *Pager) PreFetch(pc uint16) {
	if p.armed {
		p.active, p.armed = p.arming, false
	}
	switch pc {
	case RST8, KeyInt, PageIn:
		p.armed, p.arming = true, true
	case PageOut:
		p.armed, p.arming = true, false
	}
}
