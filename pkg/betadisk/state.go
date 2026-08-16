package betadisk

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture of the Beta Disk interface, for rewind and for
// replaying execution forward from a captured point.
//
// The four addressable registers are the small part. What decides the next
// byte the guest gets is the sector transfer buffer and its position, the
// physical head position of every drive, the direction the last Step went (a
// bare Step repeats it), and — mid-write — the sector the command was
// addressed to when it was issued. None of that is readable through the ports,
// and none of it appears in any snapshot format.

// fdcState is the wire form of the WD1793. Every field is exported because gob
// only encodes exported fields, and every field of the running controller that
// belongs to the machine is present: adding a field to FDC without adding it
// here is the failure this package's tests are built to catch.
type fdcState struct {
	StatusReg byte
	TrackReg  byte
	SectorReg byte
	DataReg   byte
	CmdType1  bool

	Side        int
	Drive       int
	Intrq       bool
	LastStepDir int

	HeadTrack [4]int

	Buf     []byte
	BufPos  int
	Writing bool

	WriteDrive int
	WriteCyl   int
	WriteSide  int
	WriteSec   int
}

// betaState is the wire form of the interface: the $FF system latch plus the
// controller behind it.
type betaState struct {
	SysReg byte
	FDC    fdcState
}

// The mounted disks and their write-protect tabs are deliberately absent.
// They are the medium, not the machine: a rewind cannot un-write a floppy, and
// it must not un-eject or re-eject one either, so the disk a drive holds and
// what is on it survive a restore untouched. Carrying a megabyte of sector
// data in every rewind frame would also bound the rewind ring at a handful of
// seconds.
//
// The TR-DOS ROM auto-paging latch is absent for a different reason: it lives
// in pkg/memory (Memory.betaActive, driven by the $3Dxx pre-fetch hook) and is
// captured there. Capturing it here as well would give two devices ownership
// of one latch, and a restore would then depend on which of them was applied
// last.

// StateID identifies the interface in a captured machine state.
func (it *Interface) StateID() string { return "betadisk" }

// SaveState captures the complete controller state.
func (it *Interface) SaveState() []byte {
	f := it.FDC
	s := betaState{
		SysReg: it.sysReg,
		FDC: fdcState{
			StatusReg:   f.statusReg,
			TrackReg:    f.trackReg,
			SectorReg:   f.sectorReg,
			DataReg:     f.dataReg,
			CmdType1:    f.cmdType1,
			Side:        f.side,
			Drive:       f.drive,
			Intrq:       f.intrq,
			LastStepDir: f.lastStepDir,
			HeadTrack:   f.headTrack,
			Buf:         f.buf,
			BufPos:      f.bufPos,
			Writing:     f.writing,
			WriteDrive:  f.wrDrive,
			WriteCyl:    f.wrCyl,
			WriteSide:   f.wrSide,
			WriteSec:    f.wrSec,
		},
	}
	return encodeBetaState(s)
}

// LoadState restores a state captured by SaveState.
func (it *Interface) LoadState(b []byte) error {
	var s betaState
	if err := decodeBetaState(b, &s); err != nil {
		return err
	}
	if err := s.check(); err != nil {
		return err
	}

	it.sysReg = s.SysReg
	f := it.FDC
	f.statusReg = s.FDC.StatusReg
	f.trackReg = s.FDC.TrackReg
	f.sectorReg = s.FDC.SectorReg
	f.dataReg = s.FDC.DataReg
	f.cmdType1 = s.FDC.CmdType1
	f.side = s.FDC.Side
	f.drive = s.FDC.Drive
	f.intrq = s.FDC.Intrq
	f.lastStepDir = s.FDC.LastStepDir
	f.headTrack = s.FDC.HeadTrack
	f.buf = s.FDC.Buf
	f.bufPos = s.FDC.BufPos
	f.writing = s.FDC.Writing
	f.wrDrive = s.FDC.WriteDrive
	f.wrCyl = s.FDC.WriteCyl
	f.wrSide = s.FDC.WriteSide
	f.wrSec = s.FDC.WriteSec
	return nil
}

// check refuses a state that decodes but describes a controller that cannot
// exist. It runs before anything is applied, because a drive index outside
// 0..3 would index the drive array out of bounds the next time the guest
// touched the disk, and a half-applied state is worse than a rejected one.
func (s betaState) check() error {
	if s.FDC.Drive < 0 || s.FDC.Drive > 3 {
		return fmt.Errorf("betadisk: state selects drive %d, which does not exist", s.FDC.Drive)
	}
	if s.FDC.Side < 0 || s.FDC.Side > 1 {
		return fmt.Errorf("betadisk: state selects side %d, which does not exist", s.FDC.Side)
	}
	if s.FDC.WriteDrive < 0 || s.FDC.WriteDrive > 3 {
		return fmt.Errorf("betadisk: state aims an in-flight write at drive %d, which does not exist",
			s.FDC.WriteDrive)
	}
	if s.FDC.BufPos < 0 || s.FDC.BufPos > len(s.FDC.Buf) {
		return fmt.Errorf("betadisk: state is %d bytes into a %d-byte transfer",
			s.FDC.BufPos, len(s.FDC.Buf))
	}
	return nil
}

// encodeBetaState is the wire encoding.
func encodeBetaState(s betaState) []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values and one byte slice cannot fail
		// for any reason the caller could act on, and returning an error here
		// would push a dead branch into every caller.
		panic("betadisk: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// decodeBetaState is the inverse, and reports a malformed blob rather than
// leaving the caller with a half-filled struct.
func decodeBetaState(b []byte, s *betaState) error {
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(s); err != nil {
		return fmt.Errorf("betadisk: decoding state: %w", err)
	}
	return nil
}
