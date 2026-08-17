package disciple

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture of the DISCiPLE / +D interface, for rewind and for
// replaying execution forward from a captured point.
//
// The four addressable WD1772 registers are the small part. What decides the
// next byte the guest gets is the transfer buffer and its position, the
// physical head position of each drive (the Track Register is a separate latch
// that only follows the head when a command's update flag is set), the
// direction the last step went (a bare Step repeats it), whether the in-flight
// command was multi-sector (the Sector Register auto-increments when it ends),
// and which command class last ran, because that changes what the status bits
// mean. Add to that the 8 KB of interface RAM that GDOS executes from, and the
// paging latches that decide whether the guest is running GDOS at all. None of
// it is readable through the ports, and none of it is in any snapshot format.

// discipleState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of the running interface that
// belongs to the machine is present: adding a field to Disciple without adding
// it here is the failure this package's tests are built to catch.
type discipleState struct {
	StatusReg byte
	TrackReg  byte
	SectorReg byte
	DataReg   byte

	XferBuf    []byte
	XferPos    int
	XferLen    int
	XferWrite  bool
	Formatting bool

	Busy         bool
	DRQ          bool
	INTRQ        bool
	LastCmdType1 bool
	MotorOn      bool
	StepDir      int
	MultiSector  bool

	Drive     int
	Head      int
	HeadTrack [2]int

	ControlReg byte

	Enabled   bool
	Inhibited bool
	ROMPaged  bool
	MemSwap   bool

	RAM []byte
}

// Three things are deliberately absent.
//
// The mounted disks and their paths are the medium, not the machine: a rewind
// cannot un-write a floppy, and it must not un-eject or re-eject one either, so
// what a drive holds survives a restore untouched. Carrying a megabyte of
// sector data in every rewind frame would also bound the ring at a few seconds.
//
// The 8 KB GDOS ROM is fixed at construction and cannot be written by the
// guest, so capturing it would copy the same bytes into every frame.
//
// The *memory.Memory back-pointer is wiring rather than state. It is the same
// object across a restore, and a captured pointer would be meaningless.

// StateID identifies the interface in a captured machine state.
func (d *Disciple) StateID() string { return "disciple" }

// SaveState captures the complete interface state.
func (d *Disciple) SaveState() []byte {
	s := discipleState{
		StatusReg: d.statusReg,
		TrackReg:  d.trackReg,
		SectorReg: d.sectorReg,
		DataReg:   d.dataReg,

		// The transfer buffer is copied, not referenced: a sector read hands
		// out the disk's own backing array, so a capture that referenced it
		// would change under the guest and, worse, a restore would let later
		// writes reach back into the disk image.
		XferBuf:    append([]byte(nil), d.xferBuf...),
		XferPos:    d.xferPos,
		XferLen:    d.xferLen,
		XferWrite:  d.xferWrite,
		Formatting: d.formatting,

		Busy:         d.busy,
		DRQ:          d.drq,
		INTRQ:        d.intrq,
		LastCmdType1: d.lastCmdType1,
		MotorOn:      d.motorOn,
		StepDir:      d.stepDir,
		MultiSector:  d.multiSector,

		Drive:     d.drive,
		Head:      d.head,
		HeadTrack: d.headTrack,

		ControlReg: d.controlReg,

		Enabled:   d.enabled,
		Inhibited: d.inhibited,
		ROMPaged:  d.romPaged,
		MemSwap:   d.memswap,

		RAM: append([]byte(nil), d.ram...),
	}
	return encodeDiscipleState(s)
}

// LoadState restores a state captured by SaveState.
func (d *Disciple) LoadState(b []byte) error {
	var s discipleState
	if err := decodeDiscipleState(b, &s); err != nil {
		return err
	}
	if err := s.check(len(d.ram)); err != nil {
		return err
	}

	d.statusReg = s.StatusReg
	d.trackReg = s.TrackReg
	d.sectorReg = s.SectorReg
	d.dataReg = s.DataReg

	d.xferBuf = append([]byte(nil), s.XferBuf...)
	d.xferPos = s.XferPos
	d.xferLen = s.XferLen
	d.xferWrite = s.XferWrite
	d.formatting = s.Formatting

	d.busy = s.Busy
	d.drq = s.DRQ
	d.intrq = s.INTRQ
	d.lastCmdType1 = s.LastCmdType1
	d.motorOn = s.MotorOn
	d.stepDir = s.StepDir
	d.multiSector = s.MultiSector

	d.drive = s.Drive
	d.head = s.Head
	d.headTrack = s.HeadTrack

	d.controlReg = s.ControlReg

	d.enabled = s.Enabled
	d.inhibited = s.Inhibited
	d.romPaged = s.ROMPaged
	d.memswap = s.MemSwap

	// Copy into the existing array rather than replacing it: the RAM slice is
	// handed out by GetRAM and may already be referenced elsewhere.
	copy(d.ram, s.RAM)
	return nil
}

// check refuses a state that decodes but describes an interface that cannot
// exist. It runs before anything is applied, because a drive index outside
// 0..1 would index the drive array out of bounds the next time the guest
// touched the disk, and a half-applied state is worse than a rejected one.
func (s discipleState) check(ramSize int) error {
	if s.Drive < 0 || s.Drive > 1 {
		return fmt.Errorf("disciple: state selects drive %d, which does not exist", s.Drive)
	}
	if s.Head < 0 || s.Head > 1 {
		return fmt.Errorf("disciple: state selects head %d, which does not exist", s.Head)
	}
	for i, t := range s.HeadTrack {
		if t < 0 {
			return fmt.Errorf("disciple: state parks drive %d's head on track %d", i, t)
		}
	}
	if s.XferLen < 0 || s.XferLen > len(s.XferBuf) {
		return fmt.Errorf("disciple: state describes a %d-byte transfer in a %d-byte buffer",
			s.XferLen, len(s.XferBuf))
	}
	if s.XferPos < 0 || s.XferPos > len(s.XferBuf) {
		return fmt.Errorf("disciple: state is %d bytes into a %d-byte buffer",
			s.XferPos, len(s.XferBuf))
	}
	if len(s.RAM) != ramSize {
		return fmt.Errorf("disciple: state carries %d bytes of interface RAM, want %d",
			len(s.RAM), ramSize)
	}
	return nil
}

// encodeDiscipleState is the wire encoding.
func encodeDiscipleState(s discipleState) []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values and two byte slices cannot
		// fail for any reason the caller could act on, and returning an error
		// here would push a dead branch into every caller.
		panic("disciple: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// decodeDiscipleState is the inverse, and reports a malformed blob rather than
// leaving the caller with a half-filled struct.
func decodeDiscipleState(b []byte, s *discipleState) error {
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(s); err != nil {
		return fmt.Errorf("disciple: decoding state: %w", err)
	}
	return nil
}
