package opus

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture of the Opus Discovery, for rewind and for replaying
// execution forward from a captured point.
//
// The four WD1770 registers the guest can read are the small part. What decides
// what happens next is the transfer buffer and its position, the track and
// sector a write was addressed to when the command started (latched, and not
// reported by anything), the 6821's two-registers-behind-one-address state, the
// 2 KB SRAM the ROM builds its tables in, both of the pager's flip-flops, and
// the DRQ byte-clock.
//
// The byte-clock matters more here than on a port-mapped interface. The Opus
// wires DRQ to the Z80's NMI and the ROM's handler moves exactly one byte per
// interrupt, so a capture that lost the pending byte-period would restore a
// transfer that never advances: BUSY stays asserted and the ROM's WAIT_I/O loop
// spins forever.

// fdcState is the wire form of the WD1770. Every field is exported because gob
// only encodes exported fields, and every field of the running controller that
// belongs to the machine is present: adding a field to fdc without adding it
// here is the failure this package's tests are built to catch.
type fdcState struct {
	Status byte
	Track  byte
	Sector byte
	Data   byte

	Drive int

	Buf          []byte
	Pos          int
	Write        bool
	TargetTrack  int
	TargetSector int

	Formatting bool
	FmtPos     int
	FmtSync    int
	FmtField   int
	FmtID      []byte
	FmtData    []byte
	FmtNeed    int
	FmtHaveID  bool

	Now       uint64
	Remaining int
	Waiting   bool
	Started   bool
}

// piaState is the wire form of the 6821. Both halves of each side are carried:
// which one an address reaches depends on control-register bit 2, so dropping
// either would change what the very next load returns.
type piaState struct {
	PR  [2]byte
	DDR [2]byte
	CR  [2]byte
}

// pagerState is the wire form of the ROM overlay's two flip-flops. `Armed` and
// `Arming` are not redundant with `Active`: the flip is delayed one M1 cycle,
// so between the trap address being recognised and the change landing there is
// a state no single boolean describes.
type pagerState struct {
	Active bool
	Armed  bool
	Arming bool
}

// opusState is the wire form of the whole interface.
type opusState struct {
	FDC   fdcState
	PIA   piaState
	Pager pagerState
	RAM   []byte
}

// Four things are deliberately absent.
//
// The mounted disks are the medium, not the machine: a rewind cannot un-write a
// floppy, and it must not un-eject or re-eject one either. Their write-protect
// tabs go with them, for the same reason and by the same rule pkg/betadisk
// follows.
//
// The 8 KB interface ROM is fixed at construction and cannot be written by the
// guest, so capturing it would copy the same bytes into every rewind frame.
//
// The NMI callback and the clock function are wiring. They are the same
// closures across a restore, and a captured function value is meaningless.

// StateID identifies the interface in a captured machine state.
func (i *Interface) StateID() string { return "opus" }

// SaveState captures the complete interface state.
func (i *Interface) SaveState() []byte {
	f := &i.Dev.fdc
	p := &i.Dev.pia
	s := opusState{
		FDC: fdcState{
			Status: f.status,
			Track:  f.track,
			Sector: f.sector,
			Data:   f.data,

			Drive: f.drive,

			// The buffer is copied, not referenced: a sector read hands out a
			// slice the image owns, so a capture that referenced it would
			// change under the guest and a restore would let later writes reach
			// back into the disk image.
			Buf:          append([]byte(nil), f.buf...),
			Pos:          f.pos,
			Write:        f.write,
			TargetTrack:  f.target.track,
			TargetSector: f.target.sector,

			Formatting: f.formatting,
			FmtPos:     f.fmtPos,
			FmtSync:    f.fmtSync,
			FmtField:   f.fmtField,
			FmtID:      append([]byte(nil), f.fmtID...),
			FmtData:    append([]byte(nil), f.fmtData...),
			FmtNeed:    f.fmtNeed,
			FmtHaveID:  f.fmtHaveID,

			Now:       f.now,
			Remaining: f.remaining,
			Waiting:   f.waiting,
			Started:   f.started,
		},
		PIA: piaState{
			PR:  p.pr,
			DDR: p.ddr,
			CR:  p.cr,
		},
		Pager: pagerState{
			Active: i.pager.active,
			Armed:  i.pager.armed,
			Arming: i.pager.arming,
		},
		RAM: append([]byte(nil), i.Dev.ram[:]...),
	}
	return encodeOpusState(s)
}

// LoadState restores a state captured by SaveState.
func (i *Interface) LoadState(b []byte) error {
	var s opusState
	if err := decodeOpusState(b, &s); err != nil {
		return err
	}
	if err := s.check(); err != nil {
		return err
	}

	f := &i.Dev.fdc
	f.status = s.FDC.Status
	f.track = s.FDC.Track
	f.sector = s.FDC.Sector
	f.data = s.FDC.Data

	f.drive = s.FDC.Drive

	f.buf = append([]byte(nil), s.FDC.Buf...)
	f.pos = s.FDC.Pos
	f.write = s.FDC.Write
	f.target.track = s.FDC.TargetTrack
	f.target.sector = s.FDC.TargetSector

	f.formatting = s.FDC.Formatting
	f.fmtPos = s.FDC.FmtPos
	f.fmtSync = s.FDC.FmtSync
	f.fmtField = s.FDC.FmtField
	f.fmtID = append([]byte(nil), s.FDC.FmtID...)
	f.fmtData = append([]byte(nil), s.FDC.FmtData...)
	f.fmtNeed = s.FDC.FmtNeed
	f.fmtHaveID = s.FDC.FmtHaveID

	f.now = s.FDC.Now
	f.remaining = s.FDC.Remaining
	f.waiting = s.FDC.Waiting
	f.started = s.FDC.Started

	p := &i.Dev.pia
	p.pr = s.PIA.PR
	p.ddr = s.PIA.DDR
	p.cr = s.PIA.CR

	i.pager.active = s.Pager.Active
	i.pager.armed = s.Pager.Armed
	i.pager.arming = s.Pager.Arming

	copy(i.Dev.ram[:], s.RAM)
	return nil
}

// check refuses a state that decodes but describes an interface that cannot
// exist. It runs before anything is applied, because a half-applied state is
// worse than a rejected one: a buffer position past its buffer would panic on
// the next byte the guest asked for.
func (s opusState) check() error {
	if s.FDC.Drive < 0 || s.FDC.Drive >= 2 {
		return fmt.Errorf("opus: state selects drive %d, which does not exist", s.FDC.Drive)
	}
	if s.FDC.Pos < 0 || s.FDC.Pos > len(s.FDC.Buf) {
		return fmt.Errorf("opus: state is %d bytes into a %d-byte transfer",
			s.FDC.Pos, len(s.FDC.Buf))
	}
	if s.FDC.Remaining < 0 {
		return fmt.Errorf("opus: state owes %d T-states before the next byte", s.FDC.Remaining)
	}
	if len(s.RAM) != RAMSize {
		return fmt.Errorf("opus: state carries %d bytes of SRAM, want %d", len(s.RAM), RAMSize)
	}
	return nil
}

// encodeOpusState is the wire encoding.
func encodeOpusState(s opusState) []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values and four byte slices cannot
		// fail for any reason the caller could act on, and returning an error
		// here would push a dead branch into every caller.
		panic("opus: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// decodeOpusState is the inverse, and reports a malformed blob rather than
// leaving the caller with a half-filled struct.
func decodeOpusState(b []byte, s *opusState) error {
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(s); err != nil {
		return fmt.Errorf("opus: decoding state: %w", err)
	}
	return nil
}
