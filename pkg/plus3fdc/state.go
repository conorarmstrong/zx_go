package plus3fdc

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete controller state capture, for rewind and for replaying execution
// forward from a captured point.
//
// The main status register is four bits wide and the result bytes are gone the
// moment the CPU has taken them. What actually decides the next byte the guest
// gets is none of that: it is the phase, the command buffer with its R already
// stepped part way through a multi-sector transfer, the position inside the
// data field, the running data CRC, the per-drive head positions and PCNs, the
// seek-complete results latched for a later SENSE INTERRUPT, and the Speedlock
// retry counter. A capture that stopped at what the ports expose would restore
// a controller that runs but does not resume: the next READ DATA would answer
// from a different place on the disk.
//
// Where the line is drawn between machine state and media:
//
//   - The mounted images (f.disks) are NOT captured. They are media, the same
//     way the cassette in the deck is: rewinding emulated time does not put a
//     different disk in the drive. The consequence is deliberate — a rewind
//     across an eject sees the disk gone, exactly as a real machine would.
//   - The write-protect flags (f.wp) are NOT captured, for the same reason.
//     The tab on the disk is set by hand, and rewinding does not unset it. This
//     is also where the existing code already draws the line: resetLocked
//     clears every controller field but leaves wp, the mounted disks and the
//     Speedlock enable alone.
//   - speedlockEnabled is NOT captured; it is a user preference from the UI,
//     and resetLocked deliberately leaves it alone for that reason. The
//     counters it drives (speedlockCounter, lastSectorID) ARE captured, because
//     they change which bytes a retry returns and reset does clear them.
//   - Media the guest has already written is not captured either: a WRITE or a
//     FORMAT that completed before the capture belongs to the disk, not to the
//     controller. Rewinding does not un-write a sector, in the same way it does
//     not un-eject a disk.
//   - The FORMAT staging track IS captured in full. It is not on the disk yet —
//     the commit into Tracks[head][cyl] happens only when the last CHRN byte
//     arrives — so a format caught mid-stream is controller state, and nothing
//     else in the machine holds those bytes.
//
// Two live pointers cannot be encoded and are re-resolved on restore instead:
// ioTrack and formatDisk. Both are derived at command time from the selected
// unit, head and PCN (currentTrack / currentDisk), all three of which are
// captured, so re-resolving reproduces the same pointer against the same media.
// It cannot be faked past that: if the disk was ejected between capture and
// restore, the transfer resumes against no disk, which is what the hardware
// does when the disk leaves mid-command.
//
// One thing genuinely does not reproduce, and is not faked: weak bytes.
// Track.readByte returns fresh randomness for every byte flagged weak, drawn
// from the package-global generator, and that generator is not ours to capture.
// Replaying across a weak sector therefore returns different bytes — which is
// what a real weak sector does on every revolution, so the capture is not what
// makes it unreproducible.

// Sentinel values for the decoded-command pointer, which is either nil, the
// package-level invalid spec, or an entry in commandTable. The pointer itself
// cannot be encoded; the index into that table can.
const (
	cmdIndexNone    = -1
	cmdIndexInvalid = -2
)

// fdcState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running controller is present:
// adding a field to UPD765 without adding it here is the failure this
// package's tests are built to catch.
type fdcState struct {
	Phase  int
	Cmd    int // index into commandTable, or one of the cmdIndex sentinels
	CmdBuf [9]byte
	CmdLen int

	ResultBuf [7]byte
	ResultLen int
	ResultIdx int

	// IOActive says whether a transfer was in flight; the track pointer
	// itself is re-resolved from US/HD/PCN on restore.
	IOActive     bool
	IOPos        int
	IOEnd        int
	IOCRC        uint16
	IODataOffset int

	ReadCM          bool
	ReadDataCRC     bool
	ReadWrongCyl    bool
	ReadDiagNoID    bool
	ReadWantDeleted bool
	ReadSK          bool
	ScanMatch       bool

	LastSectorID     uint32
	SpeedlockCounter int

	// FormatActive says whether a FORMAT TRACK was mid-stream; the disk
	// pointer is re-resolved from US on restore, the staging track is
	// carried in full.
	FormatActive  bool
	FormatStaging trackState
	FormatCyl     int
	FormatHead    int
	FormatN       byte
	FormatNumSec  byte
	FormatFiller  byte
	FormatCHRN    []byte

	HeadPos    [4]int
	PCN        [4]byte
	SeekDone   [4]bool
	SeekResult [4]byte

	US byte
	HD byte
}

// trackState is a Track flattened for encoding, used only for the FORMAT
// staging buffer. The bitmaps travel with it because a format writes address
// marks, and an address mark is a clock-bitmap bit as much as it is a byte.
type trackState struct {
	C      byte
	H      byte
	N      byte
	Filler byte
	BPT    int
	Data   []byte
	Clocks []byte
	Weak   []byte
}

// formatFail is deliberately NOT captured. It is cleared on entry to
// execWriteIDByte, set while the track builder runs, read once to choose
// ST1/IC, and cleared again before that function returns, so it never outlives
// the single port write that delivers the final CHRN byte. Captures are taken
// at instruction boundaries, so every reachable capture point sees false and
// restoring it is a no-op.
//
// An independent mutation audit is what found it: it was the one field whose
// restore could be deleted with every test still passing, and no fixture could
// have moved it. What the guest can actually observe about a failed format is
// ST1.ND in the result phase, and resultBuf IS captured.
// TestFormatFailNeverOutlivesTheCommandThatSetsIt defends the removal, and
// fails if a future change lets the flag survive a command.

// StateID identifies the controller in a captured machine state.
func (p *Plus3FDC) StateID() string { return "plus3fdc" }

// SaveState captures the complete controller state.
func (p *Plus3FDC) SaveState() []byte { return p.fdc.saveState() }

// LoadState restores a state captured by SaveState.
func (p *Plus3FDC) LoadState(b []byte) error { return p.fdc.loadState(b) }

func (f *UPD765) saveState() []byte {
	f.mu.Lock()
	s := fdcState{
		Phase:  int(f.phase),
		Cmd:    commandIndex(f.cmd),
		CmdBuf: f.cmdBuf,
		CmdLen: f.cmdLen,

		ResultBuf: f.resultBuf,
		ResultLen: f.resultLen,
		ResultIdx: f.resultIdx,

		IOActive:     f.ioTrack != nil,
		IOPos:        f.ioPos,
		IOEnd:        f.ioEnd,
		IOCRC:        f.ioCRC,
		IODataOffset: f.ioDataOffset,

		ReadCM:          f.readCM,
		ReadDataCRC:     f.readDataCRC,
		ReadWrongCyl:    f.readWrongCyl,
		ReadDiagNoID:    f.readDiagNoID,
		ReadWantDeleted: f.readWantDeleted,
		ReadSK:          f.readSK,
		ScanMatch:       f.scanMatch,

		LastSectorID:     f.lastSectorID,
		SpeedlockCounter: f.speedlockCounter,

		FormatActive: f.formatStaging != nil || f.formatDisk != nil,
		// The copy is load-bearing: the encode below runs after the lock is
		// dropped, so handing gob the live slice would let a concurrent
		// execWriteIDByte rewrite it mid-encode.
		FormatStaging: trackToState(f.formatStaging),
		FormatCyl:     f.formatCyl,
		FormatHead:    f.formatHead,
		FormatN:       f.formatN,
		FormatNumSec:  f.formatNumSec,
		FormatFiller:  f.formatFiller,
		FormatCHRN:    append([]byte(nil), f.formatCHRN...),

		HeadPos:    f.headPos,
		PCN:        f.pcn,
		SeekDone:   f.seekDone,
		SeekResult: f.seekResult,

		US: f.us,
		HD: f.hd,
	}
	f.mu.Unlock()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values and byte slices cannot fail
		// for any reason the caller could act on, and returning an error here
		// would push a dead branch into every caller.
		panic("plus3fdc: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

func (f *UPD765) loadState(b []byte) error {
	var s fdcState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("plus3fdc: decoding state: %w", err)
	}
	// A state blob is external input, so the fields that are used as indices or
	// as an enum are checked before anything is applied. Out of range they
	// would index past a buffer or dereference a command spec that isn't
	// there, and a controller that panics on the next port read is a worse
	// answer than a refused restore.
	if s.Phase < int(phaseCommand) || s.Phase > int(phaseResult) {
		return fmt.Errorf("plus3fdc: decoding state: phase %d is not a controller phase", s.Phase)
	}
	if s.Cmd < cmdIndexInvalid || s.Cmd >= len(commandTable) {
		return fmt.Errorf("plus3fdc: decoding state: command index %d is not a command", s.Cmd)
	}
	if s.CmdLen < 0 || s.CmdLen > len(s.CmdBuf) {
		return fmt.Errorf("plus3fdc: decoding state: command length %d does not fit the command buffer", s.CmdLen)
	}
	if s.ResultLen < 0 || s.ResultLen > len(s.ResultBuf) || s.ResultIdx < 0 || s.ResultIdx > s.ResultLen {
		return fmt.Errorf("plus3fdc: decoding state: result %d/%d does not fit the result buffer",
			s.ResultIdx, s.ResultLen)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.phase = fdcPhase(s.Phase)
	f.cmd = commandSpecAt(s.Cmd)
	f.cmdBuf = s.CmdBuf
	f.cmdLen = s.CmdLen

	f.resultBuf = s.ResultBuf
	f.resultLen = s.ResultLen
	f.resultIdx = s.ResultIdx

	f.ioPos = s.IOPos
	f.ioEnd = s.IOEnd
	f.ioCRC = s.IOCRC
	f.ioDataOffset = s.IODataOffset

	f.readCM = s.ReadCM
	f.readDataCRC = s.ReadDataCRC
	f.readWrongCyl = s.ReadWrongCyl
	f.readDiagNoID = s.ReadDiagNoID
	f.readWantDeleted = s.ReadWantDeleted
	f.readSK = s.ReadSK
	f.scanMatch = s.ScanMatch

	f.lastSectorID = s.LastSectorID
	f.speedlockCounter = s.SpeedlockCounter

	f.formatCyl = s.FormatCyl
	f.formatHead = s.FormatHead
	f.formatN = s.FormatN
	f.formatNumSec = s.FormatNumSec
	f.formatFiller = s.FormatFiller
	f.formatCHRN = s.FormatCHRN

	f.headPos = s.HeadPos
	f.pcn = s.PCN
	f.seekDone = s.SeekDone
	f.seekResult = s.SeekResult

	f.us = s.US
	f.hd = s.HD

	// The two pointers, re-resolved against whatever media is mounted now.
	// Both must come after US/HD/PCN are in place, because that is what they
	// are derived from.
	f.ioTrack = nil
	if s.IOActive {
		f.ioTrack = f.currentTrack()
	}
	f.formatStaging = nil
	f.formatDisk = nil
	if s.FormatActive {
		f.formatStaging = s.FormatStaging.toTrack()
		f.formatDisk = f.currentDisk()
	}
	return nil
}

// commandIndex encodes the decoded-command pointer as a table index.
func commandIndex(spec *commandSpec) int {
	switch {
	case spec == nil:
		return cmdIndexNone
	case spec == &invalidCommandSpec:
		return cmdIndexInvalid
	}
	for i := range commandTable {
		if spec == &commandTable[i] {
			return i
		}
	}
	// lookupCommand is the only thing that sets f.cmd, and it returns either a
	// commandTable entry or invalidCommandSpec. A pointer to anything else
	// means the two have drifted apart, which no caller could act on.
	panic("plus3fdc: encoding state: decoded command is not in the command table")
}

// commandSpecAt is the inverse of commandIndex. The index has already been
// range-checked by loadState.
func commandSpecAt(i int) *commandSpec {
	switch i {
	case cmdIndexNone:
		return nil
	case cmdIndexInvalid:
		return &invalidCommandSpec
	}
	return &commandTable[i]
}

// trackToState flattens the FORMAT staging track. A nil track (the usual case,
// with no format in flight) flattens to the zero value, which gob omits.
func trackToState(t *Track) trackState {
	if t == nil {
		return trackState{}
	}
	return trackState{
		C:      t.C,
		H:      t.H,
		N:      t.N,
		Filler: t.Filler,
		BPT:    t.bpt,
		Data:   append([]byte(nil), t.data...),
		Clocks: append([]byte(nil), t.clocks...),
		Weak:   append([]byte(nil), t.weak...),
	}
}

// toTrack rebuilds the staging track. The slices come straight from the
// decoder, so they are already private to this controller.
func (ts trackState) toTrack() *Track {
	return &Track{
		C:      ts.C,
		H:      ts.H,
		N:      ts.N,
		Filler: ts.Filler,
		bpt:    ts.BPT,
		data:   ts.Data,
		clocks: ts.Clocks,
		weak:   ts.Weak,
	}
}
