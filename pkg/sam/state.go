package sam

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/audio"
)

// State capture for the SAM Coupé, so rewind, time travel and save states work
// on it as they do on every other machine here.
//
// The SAM is five devices: this file gives each one a machinestate.Device, and
// the Machine itself carries the ASIC latches that belong to no sub-device.
// They are registered separately rather than as one blob because
// machinestate.Registry checks the device set in both directions — a machine
// that gains a second disk drive, or loses one, must be refused rather than
// half-restored — and because the pieces have genuinely different lifetimes.
//
// What is deliberately NOT captured is the disk in a drive and the ROM. Both
// are media: a rewind cannot un-write a floppy, and it must not un-insert or
// re-insert one either. That is the same rule the tape player and the +3's
// floppy controller already follow.

// decodeGob is the shared decode step. It exists so every LoadState below
// reports the same shape of error and none of them applies a partially-decoded
// value.
func decodeGob(blob []byte, into any) error {
	if len(blob) == 0 {
		return fmt.Errorf("sam: empty state (the capture failed)")
	}
	return gob.NewDecoder(bytes.NewReader(blob)).Decode(into)
}

// encodeGob is the shared encode step. A nil return is rejected by every
// LoadState, so a failed capture surfaces at restore rather than taking the
// emulator down on the goroutine that drives capture.
func encodeGob(v any) []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// The ASIC latches, on the Machine itself.
// ---------------------------------------------------------------------------

// machineState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of the running Machine that is not
// owned by a sub-device is here.
//
// The renderer's own frame buffer is not, and that is deliberate: the pixels
// already drawn are re-composed on the next flush from the memory and CLUT that
// ARE captured. Capturing a 4-plus-megabyte image per rewind step to save one
// frame of staleness is the wrong trade.
//
// RenderCursor is captured for completeness and is INERT on every path today:
// RunFrame zeroes it before executing, and every capture and restore happens at
// a frame boundary under withEmulationPaused, so the restored value is always
// overwritten before anything reads it. It is kept rather than dropped because
// it stops being inert the moment anything restores mid-frame, and a capture
// that silently omits a field is the harder failure to find.
type machineState struct {
	Border byte
	CLUT   [16]byte
	Line   byte
	LPen   byte
	HPen   byte

	FrameStart   uint64
	FrameCount   uint64
	RenderCursor int

	// The beeper's position: the live bit, the level carried in from the
	// previous frame, and any transitions recorded for the frame in flight.
	// Without the pending events a capture taken mid-frame restores a machine
	// missing every speaker toggle since the last frame boundary.
	BeeperLevel      bool
	FrameStartBeeper bool
	BeeperEvents     []beeperEventState

	// The AC-coupling filter. It is one pole, not two: the beeper is mono and
	// is filtered before it is summed with the SAA, so there is no second
	// channel to carry.
	BeeperDC      audio.DCState
	BeeperDCLimit int32
}

type beeperEventState struct {
	TState int
	High   bool
}

// StateID identifies the SAM's ASIC latches in a captured machine state.
func (m *Machine) StateID() string { return "sam.asic" }

// SaveState captures the ASIC latches and the beeper's position.
func (m *Machine) SaveState() []byte {
	s := machineState{
		Border:           m.border,
		CLUT:             m.clut,
		Line:             m.line,
		LPen:             m.lpen,
		HPen:             m.hpen,
		FrameStart:       m.frameStart,
		FrameCount:       m.frameCount,
		RenderCursor:     m.renderCursor,
		BeeperLevel:      m.beeperLevel,
		FrameStartBeeper: m.frameStartBeeper,
		BeeperEvents:     make([]beeperEventState, len(m.beeperEvents)),
		BeeperDC:         audio.CaptureDC(&m.beeperDC),
		BeeperDCLimit:    m.beeperDC.Limit(),
	}
	for i, e := range m.beeperEvents {
		s.BeeperEvents[i] = beeperEventState{TState: e.tstate, High: e.high}
	}
	return encodeGob(s)
}

// LoadState restores a state captured by SaveState.
//
// The blob is decoded whole before any of it is applied, so a malformed one
// cannot leave the machine with its palette rewound and its paging still in the
// present.
func (m *Machine) LoadState(blob []byte) error {
	var s machineState
	if err := decodeGob(blob, &s); err != nil {
		return fmt.Errorf("sam: decoding machine state: %w", err)
	}

	m.border = s.Border
	m.clut = s.CLUT
	m.lpen = s.LPen
	m.hpen = s.HPen
	m.frameStart = s.FrameStart
	m.frameCount = s.FrameCount
	m.renderCursor = s.RenderCursor
	m.beeperLevel = s.BeeperLevel
	m.frameStartBeeper = s.FrameStartBeeper
	m.beeperEvents = m.beeperEvents[:0]
	for _, e := range s.BeeperEvents {
		m.beeperEvents = append(m.beeperEvents, beeperEvent{tstate: e.TState, high: e.High})
	}
	audio.ApplyDC(&m.beeperDC, s.BeeperDC)
	m.beeperDC.SetLimit(s.BeeperDCLimit)

	// Two latches have effects outside their own byte and have to be re-applied
	// rather than just stored: SOFF changes memory contention, and the line
	// interrupt arms a Z80 timing hook. Restoring the bytes alone would leave a
	// machine whose registers read right and whose behaviour is the previous
	// guest's.
	//
	// setLineInterrupt writes m.line itself, which is why there is no
	// `m.line = s.Line` above it. There was one, and it made the restore look
	// covered while being unkillable: deleting it changed nothing, because the
	// call below put the same value back. One writer per field.
	m.Mem.SetScreenOff(s.Border&borderSOFF != 0)
	m.setLineInterrupt(s.Line)
	return nil
}

// ---------------------------------------------------------------------------
// Memory.
// ---------------------------------------------------------------------------

// memoryState is the wire form of the SAM memory map.
//
// The resolved page pointers (readPage / writePage / sectionPage /
// videoSection) are NOT captured. They are pointers into this machine's own
// arrays, so restoring them would alias the machine the capture came from —
// a write through one machine would land in the other's RAM. They are rebuilt
// from the five paging registers instead, which is what those registers are
// for.
//
// The ROM is not captured either. It is the machine's firmware, loaded from a
// file, not state a guest can move.
type memoryState struct {
	RAM    [][PageSize]byte
	ExtRAM [][PageSize]byte

	LMPR byte
	HMPR byte
	VMPR byte
	LEPR byte
	HEPR byte

	ContentionEnabled bool
	ScreenOff         bool
}

// StateID identifies the SAM memory in a captured machine state.
func (m *Memory) StateID() string { return "sam.memory" }

// SaveState captures the RAM and the paging registers.
func (m *Memory) SaveState() []byte {
	s := memoryState{
		RAM:               make([][PageSize]byte, len(m.ram)),
		ExtRAM:            make([][PageSize]byte, len(m.extRAM)),
		LMPR:              m.lmpr,
		HMPR:              m.hmpr,
		VMPR:              m.vmpr,
		LEPR:              m.lepr,
		HEPR:              m.hepr,
		ContentionEnabled: m.contentionEnabled,
		ScreenOff:         m.screenOff,
	}
	copy(s.RAM, m.ram)
	copy(s.ExtRAM, m.extRAM)
	return encodeGob(s)
}

// LoadState restores a state captured by SaveState and rebuilds the page
// mapping from the restored registers.
func (m *Memory) LoadState(blob []byte) error {
	var s memoryState
	if err := decodeGob(blob, &s); err != nil {
		return fmt.Errorf("sam: decoding memory state: %w", err)
	}
	if len(s.RAM) != len(m.ram) {
		return fmt.Errorf("sam: state has %d internal RAM pages, this machine has %d",
			len(s.RAM), len(m.ram))
	}
	if len(s.ExtRAM) != len(m.extRAM) {
		return fmt.Errorf("sam: state has %d external RAM pages, this machine has %d",
			len(s.ExtRAM), len(m.extRAM))
	}

	copy(m.ram, s.RAM)
	copy(m.extRAM, s.ExtRAM)
	m.lmpr, m.hmpr, m.vmpr = s.LMPR, s.HMPR, s.VMPR
	m.lepr, m.hepr = s.LEPR, s.HEPR
	m.contentionEnabled = s.ContentionEnabled
	m.screenOff = s.ScreenOff
	// Rebuild everything derived from the registers rather than restoring it:
	// updatePaging resolves the four section pointers (and the video overlay
	// flags) into THIS machine's arrays, and updateContention picks the delay
	// table for the restored screen mode.
	m.updatePaging()
	m.updateContention()
	return nil
}

// ---------------------------------------------------------------------------
// Keyboard.
// ---------------------------------------------------------------------------

// keyboardState is the wire form. The typed-character pulse is included
// because it is a countdown: a capture taken during one and restored without it
// would either drop the keypress or repeat it.
type keyboardState struct {
	Matrix      [9]byte
	PulseMatrix [9]byte
	PulseFrames int
}

// StateID identifies the SAM keyboard in a captured machine state.
func (k *Keyboard) StateID() string { return "sam.keyboard" }

// SaveState captures the key matrix and any typed-character pulse in flight.
func (k *Keyboard) SaveState() []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	return encodeGob(keyboardState{
		Matrix:      k.matrix,
		PulseMatrix: k.pulseMatrix,
		PulseFrames: k.pulseFrames,
	})
}

// LoadState restores a state captured by SaveState.
func (k *Keyboard) LoadState(blob []byte) error {
	var s keyboardState
	if err := decodeGob(blob, &s); err != nil {
		return fmt.Errorf("sam: decoding keyboard state: %w", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.matrix = s.Matrix
	k.pulseMatrix = s.PulseMatrix
	k.pulseFrames = s.PulseFrames
	return nil
}

// ---------------------------------------------------------------------------
// WD1772 disk controller.
// ---------------------------------------------------------------------------

// fdcState is the wire form of one drive.
//
// The four addressable registers are the small part. What decides the next byte
// the guest receives is the transfer buffer and its position, the physical head
// position (which the Track Register only follows when a command's update flag
// is set), the direction the last step went (a bare Step repeats it), and
// whether the command in flight was multi-sector. None of that is readable
// through the ports.
type fdcState struct {
	Status  byte
	Track   byte
	Sector  byte
	Data    byte
	Cyl     int
	Side    int
	LastDir int

	CmdType1    bool
	Buffer      []byte
	BufPos      int
	Writing     bool
	MultiSector bool
	DRQReads    int
	IntRQ       bool
	IdxCounter  int
}

// SetStateID names this controller for capture. The two drives must answer
// differently, or a registry holding both would restore whichever was applied
// last into both of them.
func (w *WD1772) SetStateID(id string) { w.stateID = id }

// StateID identifies this drive in a captured machine state.
func (w *WD1772) StateID() string {
	if w.stateID == "" {
		return "sam.fdc1"
	}
	return w.stateID
}

// SaveState captures the controller, but not the disk in it.
func (w *WD1772) SaveState() []byte {
	s := fdcState{
		Status:      w.status,
		Track:       w.track,
		Sector:      w.sector,
		Data:        w.data,
		Cyl:         w.cyl,
		Side:        w.side,
		LastDir:     w.lastDir,
		CmdType1:    w.cmdType1,
		Buffer:      make([]byte, len(w.buffer)),
		BufPos:      w.bufPos,
		Writing:     w.writing,
		MultiSector: w.multiSector,
		DRQReads:    w.drqReads,
		IntRQ:       w.intrq,
		IdxCounter:  w.idxCounter,
	}
	// A copy, not a view: the live buffer is refilled in place by the next
	// command, so a capture aliasing it would be rewritten by the machine
	// carrying on running.
	copy(s.Buffer, w.buffer)
	return encodeGob(s)
}

// LoadState restores a state captured by SaveState. The disk is left exactly as
// it is: it is media, and a rewind neither inserts nor ejects one.
func (w *WD1772) LoadState(blob []byte) error {
	var s fdcState
	if err := decodeGob(blob, &s); err != nil {
		return fmt.Errorf("sam: decoding fdc state: %w", err)
	}
	if s.BufPos < 0 || s.BufPos > len(s.Buffer) {
		return fmt.Errorf("sam: fdc state has position %d in a %d-byte buffer",
			s.BufPos, len(s.Buffer))
	}

	w.status = s.Status
	w.track = s.Track
	w.sector = s.Sector
	w.data = s.Data
	w.cyl = s.Cyl
	w.side = s.Side
	w.lastDir = s.LastDir
	w.cmdType1 = s.CmdType1
	w.buffer = make([]byte, len(s.Buffer))
	copy(w.buffer, s.Buffer)
	w.bufPos = s.BufPos
	w.writing = s.Writing
	w.multiSector = s.MultiSector
	w.drqReads = s.DRQReads
	w.intrq = s.IntRQ
	w.idxCounter = s.IdxCounter
	return nil
}
