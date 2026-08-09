package opus

// The Opus Discovery's parallel interface: a 6821 PIA at $3000-$3003 carrying
// the drive-select and side lines, the Centronics printer port, and the flags
// the ROM uses to remember whether its 2 KB SRAM has been initialised.
//
// The 6821's defining feature is that each side is two registers behind one
// address: control-register bit 2 chooses the peripheral register or the data
// direction register. The Opus ROM's printer routines lean on that directly,
// clearing CR bit 2 to reconfigure a line and setting it again to move data.
type pia struct {
	pr  [2]byte // peripheral output latches, A and B
	ddr [2]byte // data direction, 1 = output
	cr  [2]byte // control registers
}

// read returns register n (0 = PRA/DDRA, 1 = CRA, 2 = PRB/DDRB, 3 = CRB).
func (p *pia) read(n int) byte {
	switch n {
	case 1:
		return p.cr[0]
	case 3:
		return p.cr[1]
	}
	side := n >> 1
	if p.cr[side]&0x04 == 0 {
		return p.ddr[side]
	}
	// Output bits read their latch. Input bits read the attached line, and
	// nothing is attached: an idle Centronics BUSY reads low (ready), which
	// also keeps the printer routine's wait loop from hanging forever.
	return p.pr[side] & p.ddr[side]
}

// write stores to register n.
func (p *pia) write(n int, v byte) {
	switch n {
	case 1:
		p.cr[0] = v
		return
	case 3:
		p.cr[1] = v
		return
	}
	side := n >> 1
	if p.cr[side]&0x04 == 0 {
		p.ddr[side] = v
		return
	}
	p.pr[side] = v
}

// portA returns the port A output lines as the drive sees them.
func (p *pia) portA() byte { return p.pr[0] & p.ddr[0] }
