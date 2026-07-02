package plus3fdc

import (
	"fmt"
	"sync"
)

// UPD765 is a NEC µPD765A floppy disk controller emulation covering the
// command set Spectrum +3 / +2A +3DOS and CP/M software use to read and
// write DSK-family disk images: READ/WRITE DATA, READ/WRITE DELETED
// DATA, READ ID, READ DIAGNOSTIC (READ TRACK), WRITE ID (FORMAT TRACK),
// SCAN EQUAL/LOW-OR-EQUAL/HIGH-OR-EQUAL, SEEK, RECALIBRATE, SENSE
// INTERRUPT STATUS, SENSE DRIVE STATUS, and SPECIFY. Simplifications
// against the real chip:
//
//   - No spin-cycle accurate timing — every status read returns "ready",
//     so the FDC never enters a wait state. +3DOS tolerates this.
//   - SEEK/RECALIBRATE move the head immediately rather than over time.
//   - SCAN commands compare a single sector; real hardware continues
//     across consecutive sectors up to EOT the way READ/WRITE DATA do.
//   - Drive 2/3 alias drive 0/1 (the +3 only wires the US0 line).
//
// Weak / copy-protected sectors are modelled with a per-byte "weak" bit
// (each read of a weak byte returns fresh random data) plus an opt-in
// Speedlock retry heuristic — see SetSpeedlockEnabled.
//
// The state machine has three phases:
//
//   CMD: collecting the command byte and its parameter bytes
//   EXE: per-byte data transfer (reads, writes, and scan comparisons)
//   RES: delivering the per-command result bytes
//
// External interaction is through three port functions:
//
//   ReadStatus()      → main status register byte
//   ReadData()        → next data byte from EXE or RES
//   WriteData(b)      → command/parameter byte from the CPU

// Main Status Register bits.
const (
	msrRQM byte = 0x80 // Request for master: ready to transfer a byte
	msrDIO byte = 0x40 // Data direction: 1 = FDC→CPU, 0 = CPU→FDC
	msrEXM byte = 0x20 // Execution mode (non-DMA)
	msrCB  byte = 0x10 // Controller busy
	// Bits 0..3 are D3B..D0B (per-drive seek-in-progress); we don't model
	// overlapped seeks so they're always zero.
)

// Status register 0 bits.
const (
	st0US0 byte = 0x01
	st0US1 byte = 0x02
	st0HD  byte = 0x04
	st0NR  byte = 0x08
	st0SE  byte = 0x20
	st0IC0 byte = 0x40 // IC = bits 7..6 (00=normal, 01=abnormal, 10=invalid)
	st0IC1 byte = 0x80
)

// Status register 1 bits.
const (
	st1MA byte = 0x01 // Missing address mark
	st1NW byte = 0x02 // Not writable (write protect)
	st1ND byte = 0x04 // No data
	st1DE byte = 0x20 // Data error in ID/data field (CRC)
	st1EN byte = 0x80 // End of cylinder
)

// Status register 2 bits.
const (
	st2SN byte = 0x04 // Scan Not satisfied
	st2SH byte = 0x08 // Scan Equal Hit
	st2WC byte = 0x10 // Wrong Cylinder
	st2DD byte = 0x20 // Data error in Data field (CRC)
	st2CM byte = 0x40 // Control Mark (deleted DAM encountered)
)

// Status register 3 bits.
const (
	st3HD byte = 0x04
	st3TS byte = 0x08 // Two-sided
	st3T0 byte = 0x10 // Track 0
	st3RY byte = 0x20 // Ready
	st3WP byte = 0x40 // Write protect
)

// fdcPhase is the current FDC state.
type fdcPhase int

const (
	phaseCommand fdcPhase = iota
	phaseExecution
	phaseResult
)

// commandID identifies a decoded µPD765 command.
type commandID int

const (
	cmdInvalid commandID = iota
	cmdReadData
	cmdReadDelData
	cmdReadDiag
	cmdReadID
	cmdRecalibrate
	cmdSeek
	cmdSenseInt
	cmdSenseDrive
	cmdSpecify
	cmdVersion
	cmdWriteData
	cmdWriteDelData
	cmdWriteID
	cmdScanEq
	cmdScanLE
	cmdScanGE
)

// consumesFromCPU reports whether the given command reads bytes from the
// CPU during execution phase (direction CPU→FDC, MSR.DIO = 0). The SCAN
// commands are included because even though they don't write to the
// disk, they take the comparison data from the CPU side.
func (id commandID) consumesFromCPU() bool {
	switch id {
	case cmdWriteData, cmdWriteDelData, cmdWriteID,
		cmdScanEq, cmdScanLE, cmdScanGE:
		return true
	}
	return false
}

// commandSpec describes how a command is decoded and how many parameter
// bytes follow the opcode. paramBytes is the number of bytes that
// follow the first byte. A command matches when (opcode & mask) == value.
type commandSpec struct {
	id         commandID
	mask       byte
	value      byte
	paramBytes int
}

// commandTable is scanned linearly on opcode receipt — first match wins.
// Mask/value/paramBytes numbers follow the µPD765A command encoding
// table: each command occupies bits 0..4 (or fewer) of the opcode byte,
// with the remaining high bits carrying MT/MF/SK flags the mask treats
// as don't-care.
var commandTable = []commandSpec{
	{cmdReadData, 0x1F, 0x06, 8},
	{cmdReadDelData, 0x1F, 0x0C, 8},
	{cmdReadDiag, 0x9F, 0x02, 8},
	{cmdWriteData, 0x3F, 0x05, 8},
	{cmdWriteDelData, 0x3F, 0x09, 8},
	{cmdWriteID, 0xBF, 0x0D, 5},
	{cmdScanEq, 0x1F, 0x11, 8},
	{cmdScanLE, 0x1F, 0x19, 8},
	{cmdScanGE, 0x1F, 0x1D, 8},
	{cmdReadID, 0xBF, 0x0A, 1},
	{cmdRecalibrate, 0xFF, 0x07, 1},
	{cmdSeek, 0xFF, 0x0F, 2},
	{cmdSenseInt, 0xFF, 0x08, 0},
	{cmdSpecify, 0xFF, 0x03, 2},
	{cmdSenseDrive, 0xFF, 0x04, 1},
	{cmdVersion, 0x1F, 0x10, 0},
}

// invalidCommandSpec is returned by lookupCommand for unrecognised
// opcodes — package-level so the lookup is allocation-free.
var invalidCommandSpec = commandSpec{id: cmdInvalid}

// UPD765 is the FDC instance state.
type UPD765 struct {
	// mu serialises access between the CPU goroutine (which calls
	// ReadStatus/ReadData/WriteData via port I/O) and the UI goroutine
	// (which calls AttachDisk / Reset when the user mounts a new disk).
	mu sync.Mutex

	// Two physical drives. The +3 only wires US0 of the µPD765, so the
	// 4 logical drive selects collapse onto these two — see
	// physicalDrive(). disks[0] = drive A, disks[1] = drive B.
	disks [2]*Disk
	// Per-drive write-protect flag, checked by WRITE DATA / WRITE ID
	// before entering execution phase.
	wp [2]bool

	phase  fdcPhase
	cmd    *commandSpec
	cmdBuf [9]byte // command opcode + up to 8 parameter bytes
	cmdLen int

	resultBuf [7]byte
	resultLen int
	resultIdx int

	// Per-byte execution state — used by both read and write commands.
	// The direction (FDC↔CPU) is implicit in f.cmd.id and surfaces in
	// the MSR DIO bit via ReadStatus.
	ioTrack      *Track
	ioPos        int    // index of the next byte to read or write
	ioEnd        int    // index immediately after the last data byte
	ioCRC        uint16 // accumulated CRC over the data field
	ioDataOffset int    // bytes transferred so far in the current sector

	// READ DATA / READ DELETED DATA / SCAN result-phase flags. Set
	// during the transfer, surfaced in ST0/ST1/ST2 when the result
	// phase is built.
	readCM       bool // DAM type on disk didn't match the commanded type
	readDataCRC  bool // data CRC verification failed
	readWrongCyl bool // IDAM C didn't match the requested C

	// Read/scan direction flags derived from the current command's
	// opcode.
	readWantDeleted bool // true for READ DELETED DATA (wants F8 DAM)
	readSK          bool // SK bit of the command (bit 5): skip wrong-DAM sectors

	// SCAN state — tracks whether every byte compared so far has met
	// the scan condition. Starts true, cleared on the first mismatch.
	scanMatch bool

	// Speedlock retry-detection state. lastSectorID is a packed
	// encoding of the last (C,H,R) that READ DATA was issued for;
	// speedlockCounter increments each time the same sector is read
	// in succession, resetting on any other sector read. execRead
	// uses this counter to return "random" data on the retried reads
	// that Speedlock's copy-protection relies on. speedlockEnabled
	// gates the whole mechanism — disabled by default, the user
	// opts in via SetSpeedlockEnabled (from the UI).
	lastSectorID     uint32
	speedlockCounter int
	speedlockEnabled bool

	// WRITE ID / FORMAT TRACK state. The CPU sends 4 bytes (CHRN) per
	// sector during execution; we accumulate them and rebuild the
	// track once all sectors have been described. The new track is
	// built into a staging buffer (formatStaging) and only swapped
	// into formatDisk.Tracks[formatHead][formatCyl] if the build
	// succeeds — a failed or aborted FORMAT never damages the
	// previously-present track.
	formatStaging *Track
	formatDisk    *Disk
	formatCyl     int
	formatHead    int
	formatN       byte
	formatNumSec  byte
	formatFiller  byte
	formatCHRN    []byte // 4*formatNumSec bytes of CHRN data
	formatFail    bool   // track builder overflowed during format

	// Per-drive head position on the current track. Reset to 0 by
	// SEEK/RECALIBRATE; advanced past each sector that's read so the
	// next READ DATA / READ ID picks up where the previous one left
	// off, mimicking a real spinning disk.
	headPos [4]int

	// Per-drive PCN (present cylinder number) for SEEK/SENSE INTERRUPT.
	pcn [4]byte
	// Per-drive pending "seek complete" status. SENSE INTERRUPT picks
	// the lowest-indexed drive whose flag is set, returns its ST0+PCN
	// and clears the flag. seekResult[d] holds the ST0 byte that was
	// latched when drive d's seek finished.
	seekDone   [4]bool
	seekResult [4]byte

	// Last drive selected by a command (US bits) and HD (head address).
	us byte
	hd byte
}

// NewUPD765 creates an idle FDC with no disk loaded.
func NewUPD765() *UPD765 {
	return &UPD765{}
}

// physicalDrive returns the physical drive index (0 = A, 1 = B) for a
// µPD765 unit-select value. The +3 only wires US0, so of the two US
// bits only bit 0 is the drive selector and bit 1 (US1) is ignored —
// drives 0/2 alias to A and 1/3 alias to B.
func physicalDrive(us byte) int {
	return int(us & 1)
}

// AttachDisk loads a parsed Disk into the given physical drive (0 = A,
// 1 = B). Pass nil to eject. Calling this from the UI thread while the
// CPU goroutine is mid-command is safe — the FDC's state machine is
// reset to idle so a torn read can't return a half-old / half-new
// sector.
func (f *UPD765) AttachDisk(drive int, d *Disk) {
	if drive < 0 || drive >= len(f.disks) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disks[drive] = d
	f.resetLocked()
}

// SetWriteProtect toggles the per-drive write-protect flag.
func (f *UPD765) SetWriteProtect(drive int, wp bool) {
	if drive < 0 || drive >= len(f.wp) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wp[drive] = wp
}

// SetSpeedlockEnabled turns the Speedlock copy-protection workaround on
// or off. When enabled, repeated READ DATA commands on the same sector
// return progressively "randomised" data after a few repetitions — a
// heuristic that lets some Speedlock-protected games believe they're
// reading a weak/copy-protected sector. Default: disabled.
func (f *UPD765) SetSpeedlockEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.speedlockEnabled = enabled
	if !enabled {
		f.speedlockCounter = 0
		f.lastSectorID = 0
	}
}

// serializeDisk returns the EDSK bytes for the disk mounted in the given
// drive. The FDC lock is held for the whole walk of the track data so
// this can't race a concurrent WriteData / FormatTrack call mutating the
// same tracks from the CPU goroutine.
func (f *UPD765) serializeDisk(drive int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var d *Disk
	if drive >= 0 && drive < len(f.disks) {
		d = f.disks[drive]
	}
	if d == nil {
		return nil, fmt.Errorf("plus3fdc: drive %d has no disk", drive)
	}
	return d.SerializeDSK()
}

// Reset returns the FDC to idle, dropping any in-flight command. The disk
// pointer is preserved.
func (f *UPD765) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetLocked()
}

func (f *UPD765) resetLocked() {
	f.phase = phaseCommand
	f.cmd = nil
	f.cmdLen = 0
	f.resultLen = 0
	f.resultIdx = 0

	// Read/scan/write execution scratch — all cleared so a fresh
	// command starts from a known state rather than inheriting flags
	// from whatever was in flight when the reset hit.
	f.ioTrack = nil
	f.ioPos = 0
	f.ioEnd = 0
	f.ioCRC = 0
	f.ioDataOffset = 0
	f.readCM = false
	f.readDataCRC = false
	f.readWrongCyl = false
	f.readWantDeleted = false
	f.readSK = false
	f.scanMatch = false

	// FORMAT TRACK scratch — dropping the staging reference lets the
	// garbage collector reclaim the partially-built track.
	f.formatStaging = nil
	f.formatDisk = nil
	f.formatCHRN = nil
	f.formatFail = false
	f.formatN = 0
	f.formatNumSec = 0
	f.formatFiller = 0
	f.formatCyl = 0
	f.formatHead = 0

	for i := range f.pcn {
		f.pcn[i] = 0
		f.headPos[i] = 0
	}
	for i := range f.seekDone {
		f.seekDone[i] = false
	}
	// Speedlock state clears on reset / disk change. The enable flag
	// itself is a user preference and is NOT cleared here.
	f.lastSectorID = 0
	f.speedlockCounter = 0
}

// ReadStatus returns the Main Status Register byte. The CPU reads this
// from port 0x2FFD on the +3.
func (f *UPD765) ReadStatus() byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.phase {
	case phaseCommand:
		if f.cmdLen == 0 {
			return msrRQM
		}
		return msrRQM | msrCB
	case phaseExecution:
		// DIO=1 for reads (FDC→CPU), DIO=0 for writes (CPU→FDC).
		st := byte(msrRQM | msrEXM | msrCB)
		if f.cmd == nil || !f.cmd.id.consumesFromCPU() {
			st |= msrDIO
		}
		return st
	case phaseResult:
		return msrRQM | msrDIO | msrCB
	}
	return msrRQM
}

// ReadData returns the next byte from the FDC. The CPU reads this from
// port 0x3FFD on the +3.
func (f *UPD765) ReadData() byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.phase {
	case phaseExecution:
		return f.execRead()
	case phaseResult:
		if f.resultIdx >= f.resultLen {
			return 0xFF
		}
		b := f.resultBuf[f.resultIdx]
		f.resultIdx++
		if f.resultIdx >= f.resultLen {
			// Result phase done — back to idle.
			f.phase = phaseCommand
			f.cmdLen = 0
			f.cmd = nil
		}
		return b
	}
	// Read from CMD phase / unexpected: undefined behaviour. Return 0xFF.
	return 0xFF
}

// WriteData accepts a command, parameter, or (during write execution)
// data byte from the CPU. The CPU writes this to port 0x3FFD on the +3.
func (f *UPD765) WriteData(b byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.phase == phaseExecution && f.cmd != nil && f.cmd.id.consumesFromCPU() {
		switch f.cmd.id {
		case cmdWriteData, cmdWriteDelData:
			f.execWrite(b)
		case cmdWriteID:
			f.execWriteIDByte(b)
		case cmdScanEq, cmdScanLE, cmdScanGE:
			f.execScanByte(b)
		}
		return
	}
	if f.phase != phaseCommand {
		// Result phase or mismatched write — ignore silently.
		return
	}

	if f.cmdLen == 0 {
		// First byte: decode the command.
		spec := lookupCommand(b)
		f.cmd = spec
		f.cmdBuf[0] = b
		f.cmdLen = 1
		if spec.id == cmdInvalid {
			f.executeInvalid()
			return
		}
		if spec.paramBytes == 0 {
			f.execute()
		}
		return
	}

	// Subsequent bytes: parameters.
	f.cmdBuf[f.cmdLen] = b
	f.cmdLen++
	if f.cmdLen-1 >= f.cmd.paramBytes {
		f.execute()
	}
}

// lookupCommand walks the dispatch table and returns the first matching
// spec. invalidCommandSpec is returned if nothing matches.
func lookupCommand(b byte) *commandSpec {
	for i := range commandTable {
		s := &commandTable[i]
		if b&s.mask == s.value {
			return s
		}
	}
	return &invalidCommandSpec
}

// execute runs the command once all bytes are received and either:
//   - prepares the result phase (commands without execution data transfer)
//   - prepares the execution phase (READ DATA)
//   - returns to idle (SPECIFY, SEEK, RECALIBRATE)
func (f *UPD765) execute() {
	switch f.cmd.id {
	case cmdSpecify:
		f.completeNoResult()
	case cmdSenseDrive:
		f.execSenseDrive()
	case cmdRecalibrate:
		f.execRecalibrate()
	case cmdSeek:
		f.execSeek()
	case cmdSenseInt:
		f.execSenseInt()
	case cmdReadID:
		f.execReadID()
	case cmdReadData, cmdReadDelData:
		f.execReadData()
	case cmdReadDiag:
		f.execReadDiag()
	case cmdVersion:
		f.beginResult([]byte{0x80}) // µPD765A reports 0x80 for VERSION
	case cmdWriteData:
		f.execWriteData(false)
	case cmdWriteDelData:
		f.execWriteData(true)
	case cmdWriteID:
		f.execWriteID()
	case cmdScanEq, cmdScanLE, cmdScanGE:
		f.execScanData()
	default:
		f.executeInvalid()
	}
}

// completeNoResult finishes a command that has no result phase, returning
// the FDC to the idle command-accepting state.
func (f *UPD765) completeNoResult() {
	f.phase = phaseCommand
	f.cmdLen = 0
	f.cmd = nil
}

// beginResult enters the result phase with the supplied bytes.
func (f *UPD765) beginResult(bytes []byte) {
	n := copy(f.resultBuf[:], bytes)
	f.resultLen = n
	f.resultIdx = 0
	f.phase = phaseResult
}

// icCode is the IC (interrupt code) field of ST0, encoding how the
// command terminated. Bits 7..6 of ST0.
type icCode byte

const (
	icNormal   icCode = 0    // 00 — completed without error
	icAbnormal icCode = 0x40 // 01 — abnormal termination (st0IC0)
	icInvalid  icCode = 0x80 // 10 — invalid command issued (st0IC1)
)

// makeST0 builds a Status Register 0 byte with the current US/HD bits
// filled in and the supplied IC code applied. NR is set automatically when
// the selected drive has no disk.
func (f *UPD765) makeST0(ic icCode) byte {
	st0 := usBits(f.us)
	if f.hd != 0 {
		st0 |= st0HD
	}
	if !f.driveReady(int(f.us)) {
		st0 |= st0NR
	}
	st0 |= byte(ic)
	return st0
}

// driveReady reports whether the given logical drive (0..3) currently
// has a disk in its physical slot.
func (f *UPD765) driveReady(drive int) bool {
	return f.disks[physicalDrive(byte(drive))] != nil
}

// currentDisk returns the disk currently selected by the f.us field.
func (f *UPD765) currentDisk() *Disk {
	return f.disks[physicalDrive(f.us)]
}

// currentTwoSide reports whether the currently-selected drive's disk is
// double-sided (sets ST3.TS).
func (f *UPD765) currentTwoSide() bool {
	d := f.currentDisk()
	return d != nil && d.Sides == 2
}

// executeInvalid handles the INVALID command — single ST0 byte with IC=10.
func (f *UPD765) executeInvalid() {
	f.beginResult([]byte{byte(icInvalid)})
}

// execSenseDrive returns one ST3 byte for the addressed drive.
func (f *UPD765) execSenseDrive() {
	hduds := f.cmdBuf[1]
	f.us = hduds & 0x03
	f.hd = (hduds >> 2) & 0x01
	st3 := usBits(f.us)
	if f.hd != 0 {
		st3 |= st3HD
	}
	if f.currentTwoSide() {
		st3 |= st3TS
	}
	if f.driveReady(int(f.us)) {
		st3 |= st3RY
		if f.pcn[f.us&3] == 0 {
			st3 |= st3T0
		}
	}
	if f.wp[physicalDrive(f.us)] {
		st3 |= st3WP
	}
	f.beginResult([]byte{st3})
}

// execRecalibrate moves the head to track 0 on the addressed drive.
func (f *UPD765) execRecalibrate() {
	us := f.cmdBuf[1] & 0x03
	f.us = us
	f.pcn[us] = 0
	f.headPos[us] = 0
	f.completeNoResult()
	f.markSeekDone(int(us))
}

// execSeek moves the head to the specified cylinder.
func (f *UPD765) execSeek() {
	us := f.cmdBuf[1] & 0x03
	f.hd = (f.cmdBuf[1] >> 2) & 0x01
	f.us = us
	f.pcn[us] = f.cmdBuf[2]
	f.headPos[us] = 0
	f.completeNoResult()
	f.markSeekDone(int(us))
}

// markSeekDone latches a "seek complete" status for the given drive so a
// later SENSE INTERRUPT can pick it up.
func (f *UPD765) markSeekDone(drive int) {
	f.seekDone[drive] = true
	st0 := byte(st0SE) | usBits(byte(drive))
	if !f.driveReady(drive) {
		st0 |= st0NR | byte(icAbnormal)
	}
	f.seekResult[drive] = st0
}

// usBits encodes a 0..3 drive index into the US0/US1 bits used by ST0/ST3.
func usBits(drive byte) byte {
	return drive & 0x03
}

// execSenseInt returns the latched seek-complete status for the
// lowest-indexed drive that has one pending. With no seek pending the
// command behaves like INVALID and returns a single-byte ST0=0x80
// result, per the 82078 datasheet's SENSE INTERRUPT STATUS description.
func (f *UPD765) execSenseInt() {
	for d := 0; d < 4; d++ {
		if f.seekDone[d] {
			f.seekDone[d] = false
			f.beginResult([]byte{f.seekResult[d], f.pcn[d]})
			return
		}
	}
	f.beginResult([]byte{0x80})
}

// execReadID returns the next sector header at or after the current
// head position on the current track. The head position is advanced
// past the returned IDAM so successive READ IDs walk through every
// sector in physical order.
func (f *UPD765) execReadID() {
	hduds := f.cmdBuf[1]
	f.us = hduds & 0x03
	f.hd = (hduds >> 2) & 0x01
	track := f.currentTrack()
	if track == nil {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{st0, st1MA | st1ND, 0, 0, 0, 0, 0})
		return
	}
	c, h, r, n, idEnd, ok := track.idAt(f.headPos[f.us&3])
	if !ok {
		// Walked to end of track without finding an IDAM — try once
		// from the start to handle wrap-around.
		c, h, r, n, idEnd, ok = track.idAt(0)
	}
	if !ok {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{st0, st1MA | st1ND, 0, 0, 0, 0, 0})
		return
	}
	f.headPos[f.us&3] = idEnd
	st0 := f.makeST0(icNormal)
	f.beginResult([]byte{st0, 0, 0, c, h, r, n})
}

// currentTrack returns the track at the current US / HD / PCN, or nil
// if the selected drive has no disk in it.
func (f *UPD765) currentTrack() *Track {
	d := f.currentDisk()
	if d == nil {
		return nil
	}
	cyl := int(f.pcn[f.us&3])
	return d.Track(int(f.hd), cyl)
}

// READ DATA / WRITE DATA / SCAN command parameter byte offsets within
// cmdBuf. These commands all share the same 8-byte parameter layout.
const (
	rdParamHDS = 1 // head/drive select
	rdParamC   = 2 // expected cylinder
	rdParamH   = 3 // expected head
	rdParamR   = 4 // start sector ID (mutated during multi-sector transfers)
	rdParamN   = 5 // size code
	rdParamEOT = 6 // final sector ID for multi-sector transfers
	rdParamGPL = 7 // gap length (ignored)
	rdParamDTL = 8 // data length when N=0 (ignored — we always use N)
)

// execReadData walks the current track looking for an IDAM matching the
// requested R, then queues that sector's data for streaming through the
// data port. CRC bytes are pulled from the track stream as well so the
// FDC can verify them at the end of the transfer and report ST2.DD on
// mismatch — that's how EDSK-recorded data CRC errors propagate to the
// running program.
func (f *UPD765) execReadData() {
	f.readWantDeleted = (f.cmd.id == cmdReadDelData)
	// SK bit (bit 5 of the command byte) controls whether the FDC
	// skips sectors whose DAM type doesn't match the commanded type
	// (SK=1) or reads them anyway and flags ST2.CM (SK=0).
	f.readSK = f.cmdBuf[0]&0x20 != 0

	hduds := f.cmdBuf[rdParamHDS]
	f.us = hduds & 0x03
	f.hd = (hduds >> 2) & 0x01

	f.trackSpeedlockRetry()

	if !f.startSectorRead() {
		// startSectorRead has already populated the result phase.
		return
	}
	f.phase = phaseExecution
}

// trackSpeedlockRetry updates the Speedlock repetition counter, but
// ONLY for the very specific READ DATA pattern Speedlock actually uses
// for its copy-protection check: a single-sector read of sector 2 on
// track 0, head 0 (or any even head). Broadening the detection would
// cause legitimate BDOS retries of arbitrary sectors to get
// progressively corrupted and turn recoverable errors into hard
// failures.
func (f *UPD765) trackSpeedlockRetry() {
	if !f.speedlockEnabled {
		return
	}
	c := f.cmdBuf[rdParamC]
	h := f.cmdBuf[rdParamH]
	r := f.cmdBuf[rdParamR]
	eot := f.cmdBuf[rdParamEOT]
	u := uint32(h&1) | uint32(c)<<1 | uint32(r)<<8
	if r == eot && u == 0x200 {
		if u == f.lastSectorID {
			f.speedlockCounter++
		} else {
			f.speedlockCounter = 0
			f.lastSectorID = u
		}
	} else {
		f.lastSectorID = 0
		f.speedlockCounter = 0
	}
}

// sectorLocation is the result of walking the track for a matching
// IDAM. err describes why the search failed; on success the fields
// describe where the sector data lives and what its DAM / ID state
// was, but don't include any command-specific interpretation.
type sectorLocation struct {
	dataStart int  // first byte of the sector data field
	length    int  // number of data bytes (before the trailing CRC)
	deleted   bool // sector has a deleted DAM (F8)
	wrongCyl  bool // IDAM C didn't match wantC
	nextPos   int  // position immediately after the ID CRC (for retries)
}

// findSector searches the given track for an IDAM with the requested
// record ID, starting from startPos and walking at most one full
// revolution. It handles wrap-around, skips sectors with bad ID CRCs,
// and (when sk=true) skips sectors whose DAM type doesn't match
// wantDeleted — that's the µPD765 SK bit applied during the walk so
// retries can't false-match the same wrong-DAM sector again.
//
// This is the single source of truth for the "walk the track looking
// for sector R" logic that READ DATA, WRITE DATA, and SCAN all need.
// Returns (_, st1, false) when the sector can't be found; st1 is the
// ST1 bits the caller should set in the error result phase.
func (f *UPD765) findSector(track *Track, wantC, wantR byte, startPos int, wantDeleted, sk bool) (loc sectorLocation, st1 byte, ok bool) {
	pos := startPos
	wrapped := false
	for {
		c, _, r, n, idEnd, idCRCOK, walkOK := track.idAtCRC(pos)
		if !walkOK {
			if wrapped {
				return sectorLocation{}, st1ND, false
			}
			pos = 0
			wrapped = true
			continue
		}
		idamPos := idEnd - 7
		if wrapped && idamPos >= startPos {
			return sectorLocation{}, st1ND, false
		}
		if !idCRCOK || r != wantR {
			pos = idEnd
			continue
		}
		dataStart, deleted, dok := track.dataAt(idEnd)
		if !dok {
			return sectorLocation{}, st1MA, false
		}
		length := sectorLength(n)
		if dataStart+length > track.bpt {
			return sectorLocation{}, st1ND, false
		}
		// SK=1 + wrong DAM type → keep walking. The wrap detection
		// above guarantees we make at most one revolution before
		// giving up.
		if sk && deleted != wantDeleted {
			pos = idEnd
			continue
		}
		return sectorLocation{
			dataStart: dataStart,
			length:    length,
			deleted:   deleted,
			wrongCyl:  c != wantC,
			nextPos:   idEnd,
		}, 0, true
	}
}

// startSectorRead searches the current track for a sector matching
// cmdBuf[rdParamR] and primes the execution phase to stream its data.
// Returns false if the sector wasn't found (the result phase has
// already been populated with the error bits).
func (f *UPD765) startSectorRead() bool {
	wantC := f.cmdBuf[rdParamC]
	wantR := f.cmdBuf[rdParamR]
	track := f.currentTrack()
	if track == nil {
		f.readDataError(st1MA | st1ND)
		return false
	}
	loc, st1, ok := f.findSector(track, wantC, wantR, f.headPos[f.us&3], f.readWantDeleted, f.readSK)
	if !ok {
		f.readDataError(st1)
		return false
	}
	f.ioTrack = track
	f.ioPos = loc.dataStart
	f.ioEnd = loc.dataStart + loc.length
	f.ioCRC = initialDataCRC(loc.deleted)
	f.ioDataOffset = 0
	f.readCM = loc.deleted != f.readWantDeleted
	f.readDataCRC = false
	f.readWrongCyl = loc.wrongCyl
	return true
}

// initialDataCRC returns the CRC accumulator state after the implied
// 3×A1 sync prefix and the data address mark (FB for normal data, F8
// for deleted), matching what the µPD765 uses when verifying the data
// field CRC.
func initialDataCRC(deleted bool) uint16 {
	mark := byte(dataMark)
	if deleted {
		mark = delDataMrk
	}
	crc := uint16(0xFFFF)
	crc = crcUpdate(crc, a1Mark)
	crc = crcUpdate(crc, a1Mark)
	crc = crcUpdate(crc, a1Mark)
	crc = crcUpdate(crc, mark)
	return crc
}

// readDataError finishes a failed READ DATA without entering execution
// phase. The CHRN result fields echo the requested values from cmdBuf.
func (f *UPD765) readDataError(st1 byte) {
	st0 := f.makeST0(icAbnormal)
	f.beginResult([]byte{
		st0, st1, 0,
		f.cmdBuf[rdParamC], f.cmdBuf[rdParamH], f.cmdBuf[rdParamR], f.cmdBuf[rdParamN],
	})
}

// execRead returns the next byte of the sector currently being read.
// Each byte is pulled from the track via Track.readByte (which honours
// the weak-data bitmap), accumulated into the data CRC, and the position
// is advanced. When the data field is exhausted the FDC pulls the
// trailing CRC bytes from the track, compares them to the running CRC,
// and transitions to the result phase — flagging ST2.DD if they don't
// match. If R is still below EOT and no error occurred, R is advanced
// and the next sector is queued within the same execution phase
// (multi-sector transfer); see the tail of this function.
func (f *UPD765) execRead() byte {
	if f.ioTrack == nil {
		return 0xFF
	}
	if f.ioPos >= f.ioEnd {
		return 0xFF
	}
	// READ DIAGNOSTIC is a straight track dump — no CRC verification,
	// no multi-sector loop, just stream bytes until ioEnd and emit a
	// minimal result phase.
	if f.cmd != nil && f.cmd.id == cmdReadDiag {
		b := f.ioTrack.data[f.ioPos]
		f.ioPos++
		if f.ioPos >= f.ioEnd {
			st0 := f.makeST0(icNormal)
			// Echo the first IDAM found on the track as the result CHRN.
			var rc, rh, rr, rn byte
			if c, h, r, n, _, ok := f.ioTrack.idAt(0); ok {
				rc, rh, rr, rn = c, h, r, n
			}
			f.beginResult([]byte{st0, 0, 0, rc, rh, rr, rn})
			f.ioTrack = nil
		}
		return b
	}

	b := f.ioTrack.readByte(f.ioPos)
	f.ioPos++
	// Accumulate CRC with the original (unmangled) byte first, before
	// any Speedlock corruption below — the CRC must reflect what was
	// actually read off the track.
	f.ioCRC = crcUpdate(f.ioCRC, b)

	// Speedlock heuristic: on repeated reads of the same sector,
	// progressively corrupt the returned bytes so the program believes
	// it's hitting a weak copy-protected sector. The offset counter is
	// incremented before this byte is classified, so the first byte of
	// the sector has offset == 1, not 0 — that shift is what makes the
	// mangling land on the byte positions Speedlock's check expects.
	f.ioDataOffset++
	offset := f.ioDataOffset
	if f.speedlockEnabled && f.speedlockCounter > 0 {
		if offset < 64 && b != 0xE5 {
			f.speedlockCounter = 2
		} else if (f.speedlockCounter > 1 || offset < 64) && offset%29 == 0 {
			b ^= byte(offset)
			// Accumulate the CRC a second time with the mangled byte,
			// so the running CRC ends up in the same inconsistent state
			// Speedlock's check expects to see on a genuinely weak
			// sector.
			f.ioCRC = crcUpdate(f.ioCRC, b)
		}
	}
	if f.ioPos >= f.ioEnd {
		// Last byte just consumed — verify the on-track CRC and decide
		// whether to start the next sector (multi-sector transfer) or
		// finish with the result phase.
		if f.ioPos+2 <= f.ioTrack.bpt {
			storedHi := f.ioTrack.data[f.ioPos]
			storedLo := f.ioTrack.data[f.ioPos+1]
			if storedHi != byte(f.ioCRC>>8) || storedLo != byte(f.ioCRC&0xFF) {
				f.readDataCRC = true
			}
			f.headPos[f.us&3] = f.ioPos + 2
		} else {
			f.headPos[f.us&3] = f.ioPos
		}

		// Multi-sector transfer: if the current R is below EOT and we
		// didn't hit an abnormal condition, advance R and start the
		// next sector. Otherwise finish with the result phase.
		abnormal := f.readDataCRC || f.readWrongCyl || f.readCM
		if !abnormal && f.cmdBuf[rdParamR] < f.cmdBuf[rdParamEOT] {
			f.cmdBuf[rdParamR]++
			if f.startSectorRead() {
				return b
			}
			// startSectorRead already populated the result phase on
			// failure.
			return b
		}

		f.finishReadResult()
	}
	return b
}

// execReadDiag implements the READ DIAGNOSTIC (READ TRACK) command.
// Unlike READ DATA, this reads every byte of the physical track in
// order starting from the index hole, returning gaps, sync fields,
// address marks, data fields, and CRCs. The transfer length is
// (128 << N) * EOT bytes, matching what the µPD765 datasheet calls
// "reads a complete track".
//
// Because the FDC exposes the raw spinning-disk byte stream, this is
// the escape hatch programs use to inspect unusual track layouts.
func (f *UPD765) execReadDiag() {
	hduds := f.cmdBuf[rdParamHDS]
	f.us = hduds & 0x03
	f.hd = (hduds >> 2) & 0x01

	track := f.currentTrack()
	if track == nil {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{st0, st1MA | st1ND, 0, 0, 0, 0, 0})
		return
	}
	n := f.cmdBuf[rdParamN]
	eot := f.cmdBuf[rdParamEOT]
	length := sectorLength(n) * int(eot)
	if length > track.bpt {
		length = track.bpt
	}
	if length == 0 {
		length = track.bpt
	}

	f.ioTrack = track
	f.ioPos = 0
	f.ioEnd = length
	f.ioCRC = 0
	f.readCM = false
	f.readDataCRC = false
	f.readWrongCyl = false
	f.phase = phaseExecution
}

// finishReadResult builds the result phase for a READ DATA / READ DEL
// DATA transfer, capturing CRC / wrong-cyl / deleted-DAM state in the
// three status bytes.
func (f *UPD765) finishReadResult() {
	st0 := f.makeST0(icNormal)
	st1 := byte(0)
	st2 := byte(0)
	if f.readDataCRC {
		st0 = f.makeST0(icAbnormal)
		st1 |= st1DE
		st2 |= st2DD
	}
	if f.readCM {
		st2 |= st2CM
	}
	if f.readWrongCyl {
		st0 = f.makeST0(icAbnormal)
		st2 |= st2WC
	}
	// Multi-sector transfers that reach EOT set ST1.EN.
	if f.cmdBuf[rdParamR] >= f.cmdBuf[rdParamEOT] {
		st1 |= st1EN
	}
	f.beginResult([]byte{
		st0, st1, st2,
		f.cmdBuf[rdParamC], f.cmdBuf[rdParamH], f.cmdBuf[rdParamR] + 1, f.cmdBuf[rdParamN],
	})
	f.ioTrack = nil
}

// execWriteData walks the current track for an IDAM matching R, then
// prepares the execution phase to receive sector bytes from the CPU.
// When the user has write-protected the drive the command fails with
// ST1.NW before entering execution phase. The deleted parameter selects
// WRITE DELETED DATA (F8 DAM) vs plain WRITE DATA (FB DAM).
func (f *UPD765) execWriteData(deleted bool) {
	hduds := f.cmdBuf[rdParamHDS]
	f.us = hduds & 0x03
	f.hd = (hduds >> 2) & 0x01
	wantC := f.cmdBuf[rdParamC]
	wantR := f.cmdBuf[rdParamR]

	if f.wp[physicalDrive(f.us)] {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{
			st0, st1NW, 0,
			f.cmdBuf[rdParamC], f.cmdBuf[rdParamH], f.cmdBuf[rdParamR], f.cmdBuf[rdParamN],
		})
		return
	}

	track := f.currentTrack()
	if track == nil {
		f.readDataError(st1MA | st1ND)
		return
	}
	loc, st1, ok := f.findSector(track, wantC, wantR, f.headPos[f.us&3], false, false)
	if !ok {
		f.readDataError(st1)
		return
	}
	if loc.dataStart+loc.length+2 > track.bpt {
		f.readDataError(st1ND)
		return
	}
	// Overwrite the DAM byte (1 byte before dataStart) so WRITE DATA
	// on a previously-deleted sector flips it back to FB, and vice
	// versa for WRITE DELETED DATA.
	damPos := loc.dataStart - 1
	if deleted {
		track.data[damPos] = delDataMrk
	} else {
		track.data[damPos] = dataMark
	}
	// Clear the weak bitmap for the data region — a fresh write
	// replaces the stored bytes with known-good data.
	for i := loc.dataStart; i < loc.dataStart+loc.length; i++ {
		bitClear(track.weak, i)
	}
	f.ioTrack = track
	f.ioPos = loc.dataStart
	f.ioEnd = loc.dataStart + loc.length
	f.ioCRC = initialDataCRC(deleted)
	f.phase = phaseExecution
}

// execWrite accepts one sector data byte from the CPU during a WRITE
// DATA / WRITE DELETED DATA execution phase. When the sector is full
// the FDC writes the running CRC into the track and transitions to
// result phase.
func (f *UPD765) execWrite(b byte) {
	if f.ioTrack == nil {
		return
	}
	if f.ioPos >= f.ioEnd {
		return
	}
	f.ioTrack.data[f.ioPos] = b
	f.ioPos++
	f.ioCRC = crcUpdate(f.ioCRC, b)
	if f.ioPos >= f.ioEnd {
		// Last data byte — write the CRC bytes into the track.
		if f.ioPos+2 <= f.ioTrack.bpt {
			f.ioTrack.data[f.ioPos] = byte(f.ioCRC >> 8)
			f.ioTrack.data[f.ioPos+1] = byte(f.ioCRC & 0xFF)
			f.headPos[f.us&3] = f.ioPos + 2
		} else {
			f.headPos[f.us&3] = f.ioPos
		}
		st0 := f.makeST0(icNormal)
		f.beginResult([]byte{
			st0, 0, 0,
			f.cmdBuf[rdParamC], f.cmdBuf[rdParamH], f.cmdBuf[rdParamR] + 1, f.cmdBuf[rdParamN],
		})
		f.ioTrack = nil
	}
}

// execWriteID starts a FORMAT TRACK operation. The CPU has already sent
// HDS|US, N, SC (sectors per track), GPL, and filler byte. The CPU will
// now send 4 bytes (C, H, R, N) per sector; execWriteIDByte accumulates
// them and once all are received we rebuild the track from scratch.
func (f *UPD765) execWriteID() {
	hduds := f.cmdBuf[1]
	f.us = hduds & 0x03
	f.hd = (hduds >> 2) & 0x01
	f.formatN = f.cmdBuf[2]
	f.formatNumSec = f.cmdBuf[3]
	// cmdBuf[4] = GPL (gap length) is accepted from the CPU but
	// ignored — we use the MFM gap layout from gapSpecs.
	f.formatFiller = f.cmdBuf[5]

	if f.wp[physicalDrive(f.us)] {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{st0, st1NW, 0, 0, 0, 0, 0})
		return
	}
	// Reject sector-size codes > 8 (would mean sectors ≥ 32KB, which
	// can't fit on a track anyway).
	if f.formatN > 8 {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{st0, st1ND, 0, 0, 0, 0, f.formatN})
		return
	}
	d := f.currentDisk()
	if d == nil {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{st0, st1MA | st1ND, 0, 0, 0, 0, 0})
		return
	}

	// Record the FORMAT target (disk + head + cyl) so the final swap
	// in execWriteIDByte can commit — or skip — without tearing the
	// existing track.
	cyl := int(f.pcn[f.us&3])
	if int(f.hd) >= d.Sides || cyl >= d.Cylinders {
		st0 := f.makeST0(icAbnormal)
		f.beginResult([]byte{st0, st1ND, 0, 0, 0, 0, 0})
		return
	}
	f.formatDisk = d
	f.formatCyl = cyl
	f.formatHead = int(f.hd)
	f.formatStaging = newTrack(bytesPerTrackDD, f.formatFiller, byte(cyl), f.hd, f.formatN)

	if f.formatNumSec == 0 {
		// Zero-sector format produces an empty track. Build in the
		// staging buffer and commit unconditionally — no CPU input
		// needed.
		b := newTrackBuilder(f.formatStaging, gapMFM)
		b.preindexAdd()
		b.postindexAdd()
		b.gap4Add()
		d.Tracks[f.hd][cyl] = f.formatStaging
		f.formatStaging = nil
		f.formatDisk = nil
		st0 := f.makeST0(icNormal)
		f.beginResult([]byte{st0, 0, 0, 0, 0, 0, f.formatN})
		return
	}

	f.formatCHRN = make([]byte, 0, 4*int(f.formatNumSec))
	f.formatFail = false
	f.phase = phaseExecution
}

// execWriteIDByte accepts one CHRN byte from the CPU during FORMAT TRACK
// execution. Once all sectors have been described the track byte stream
// is built into the staging buffer and only swapped into the disk if
// the build succeeds — a too-small layout leaves the previous track
// untouched and reports ST1.ND.
func (f *UPD765) execWriteIDByte(b byte) {
	f.formatCHRN = append(f.formatCHRN, b)
	if len(f.formatCHRN) < 4*int(f.formatNumSec) {
		return
	}
	// All CHRN bytes received — build the track in the staging buffer.
	staging := f.formatStaging
	tb := newTrackBuilder(staging, gapMFM)
	if !tb.preindexAdd() || !tb.postindexAdd() {
		f.formatFail = true
	}
	data := make([]byte, sectorLength(f.formatN))
	for i := range data {
		data[i] = f.formatFiller
	}
	var lastC, lastH, lastR, lastN byte
	for j := 0; j < int(f.formatNumSec) && !f.formatFail; j++ {
		c := f.formatCHRN[j*4+0]
		h := f.formatCHRN[j*4+1]
		r := f.formatCHRN[j*4+2]
		n := f.formatCHRN[j*4+3]
		lastC, lastH, lastR, lastN = c, h, r, n
		if !tb.idAdd(c, h, r, n, false) {
			f.formatFail = true
			break
		}
		if _, ok := tb.dataAdd(data, false, false); !ok {
			f.formatFail = true
			break
		}
	}
	tb.gap4Add()

	// Commit only on success. A failed format leaves the disk's
	// Tracks[head][cyl] pointing at whatever was there before.
	if !f.formatFail && f.formatDisk != nil {
		f.formatDisk.Tracks[f.formatHead][f.formatCyl] = staging
	}

	st1 := byte(0)
	ic := icNormal
	if f.formatFail {
		st1 = st1ND
		ic = icAbnormal
	}
	st0 := f.makeST0(ic)
	f.beginResult([]byte{st0, st1, 0, lastC, lastH, lastR, lastN})
	f.formatStaging = nil
	f.formatDisk = nil
	f.formatCHRN = nil
	f.formatFail = false
}

// execScanData prepares a SCAN execution phase. The scan is set up just
// like READ DATA — find the target IDAM, locate the data field, enter
// execution phase — but bytes arrive from the CPU (DIO=0) and are
// compared against the disk data via execScanByte. scanMatch is
// initialised to true and cleared on the first byte that fails the
// scan condition.
func (f *UPD765) execScanData() {
	hduds := f.cmdBuf[rdParamHDS]
	f.us = hduds & 0x03
	f.hd = (hduds >> 2) & 0x01
	wantC := f.cmdBuf[rdParamC]
	wantR := f.cmdBuf[rdParamR]

	track := f.currentTrack()
	if track == nil {
		f.readDataError(st1MA | st1ND)
		return
	}
	loc, st1, ok := f.findSector(track, wantC, wantR, f.headPos[f.us&3], false, false)
	if !ok {
		f.readDataError(st1)
		return
	}
	f.ioTrack = track
	f.ioPos = loc.dataStart
	f.ioEnd = loc.dataStart + loc.length
	f.ioCRC = initialDataCRC(loc.deleted)
	f.readCM = loc.deleted // scan treats a deleted DAM as a control-mark event
	f.readDataCRC = false
	f.readWrongCyl = loc.wrongCyl
	f.scanMatch = true
	f.phase = phaseExecution
}

// execScanByte compares the next CPU byte against the next disk byte
// per the scan condition, and advances the position. When the full
// sector has been compared the result phase is built with ST2.SH (hit)
// or ST2.SN (not satisfied) set appropriately.
func (f *UPD765) execScanByte(b byte) {
	if f.ioTrack == nil {
		return
	}
	if f.ioPos >= f.ioEnd {
		return
	}
	diskByte := f.ioTrack.readByte(f.ioPos)
	f.ioPos++
	f.ioCRC = crcUpdate(f.ioCRC, diskByte)

	// 0xFF from the CPU is a "don't care" byte per the µPD765 spec.
	if b != 0xFF {
		switch f.cmd.id {
		case cmdScanEq:
			if b != diskByte {
				f.scanMatch = false
			}
		case cmdScanLE:
			if b > diskByte {
				f.scanMatch = false
			}
		case cmdScanGE:
			if b < diskByte {
				f.scanMatch = false
			}
		}
	}

	if f.ioPos >= f.ioEnd {
		// Done with the sector. Verify the data CRC, then build the
		// result with SH / SN based on the match outcome.
		if f.ioPos+2 <= f.ioTrack.bpt {
			storedHi := f.ioTrack.data[f.ioPos]
			storedLo := f.ioTrack.data[f.ioPos+1]
			if storedHi != byte(f.ioCRC>>8) || storedLo != byte(f.ioCRC&0xFF) {
				f.readDataCRC = true
			}
			f.headPos[f.us&3] = f.ioPos + 2
		} else {
			f.headPos[f.us&3] = f.ioPos
		}

		st0 := f.makeST0(icNormal)
		st1 := byte(0)
		st2 := byte(0)
		if f.readDataCRC {
			st0 = f.makeST0(icAbnormal)
			st1 |= st1DE
			st2 |= st2DD
		}
		if f.readCM {
			st2 |= st2CM
		}
		if f.readWrongCyl {
			st0 = f.makeST0(icAbnormal)
			st2 |= st2WC
		}
		if f.scanMatch {
			st2 |= st2SH
		} else {
			st2 |= st2SN
		}
		f.beginResult([]byte{
			st0, st1, st2,
			f.cmdBuf[rdParamC], f.cmdBuf[rdParamH], f.cmdBuf[rdParamR] + 1, f.cmdBuf[rdParamN],
		})
		f.ioTrack = nil
	}
}
