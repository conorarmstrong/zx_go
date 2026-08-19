package ula

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"image/color"

	"github.com/conorarmstrong/zx_go/pkg/audio"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The guest-writable registers are the small part. What decides the next frame
// is where the ULA is INSIDE the frame: the border changes recorded but not
// yet painted, the speaker toggles queued for a waveform not yet synthesised,
// the position in the 16-frame flash cycle, the frame origin every T-state
// offset is measured against, and the one pole of memory in the output
// filter. None of that is readable by the guest and none of it appears in any
// .SNA or .Z80 snapshot. A capture that stopped at the registers would restore
// a ULA that runs but does not resume: same configuration, wrong scanline,
// wrong beeper phase, wrong frame.
//
// What is NOT here, and why:
//
//   - mem, kbd, audio, ay, peripherals, tape, beta, speccyDAC and every next*
//     field are wiring to other devices. Each of those devices captures itself
//     (the machinestate registry names them separately); copying a pointer to
//     one into this blob would restore a reference, not a state.
//   - borderTracer, portTracer, rzxPlaybackHook and rzxRecordHook are debugger
//     and RZX observers installed from outside. They are the observer's
//     property, not the machine's, and a rewind must not silently reattach or
//     detach one.
//   - img, wideImg, wideRow, nextFullImg and the compositor* scratch buffers
//     are derived: every pixel in them is recomputed from scratch by the next
//     Render from the state that IS captured here. Carrying ~600 KB of pixels
//     per rewind point to hold a value that is about to be overwritten would
//     bound the rewind ring at a handful of frames.
//   - lastImg is the same, with one honest consequence: it is the image
//     Render most recently returned, so between a restore and the next Render
//     a LastFrame call reports the frame the machine was rewound FROM. It is
//     not cleared on restore, because clearing it would make LastFrame compose
//     a frame instead, and composing is not free of side effects — on the Next
//     it steps the Copper (see the LastFrame comment). A stale picture for one
//     frame is the smaller error.
//
// The ULA has no lock of its own; like every other access to it (port I/O,
// Render), SaveState and LoadState belong on the emulation goroutine.

// ulaState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running ULA is present: adding a
// field to ULA without adding it here is the failure this package's tests are
// built to catch.
type ulaState struct {
	Flash      bool
	FlashCount int

	TimexVideoMode byte

	BorderColour           byte
	FrameStartBorderColour byte
	BorderChanges          []stateBorderChange

	Mic                 bool
	TapeIn              bool
	LastTapeTstate      uint64
	TapeAudioEvents     []stateAudioEvent
	FrameStartTapeState bool

	Speaker                bool
	FrameStartSpeakerState bool
	AudioEvents            []stateAudioEvent
	FrameStartTstate       uint64

	KempstonEnabled bool
	KempstonState   byte

	ULAOutputDisabled bool
	ULAScrollX        byte
	ULAScrollY        byte
	ULAFineScrollX    bool

	HasRendered bool

	// The DC blocker's fields are unexported, so they are mirrored rather
	// than embedded: gob would silently drop them, which is exactly the kind
	// of silent omission this file exists to prevent.
	// The DC-blocking filter is one pole PER CHANNEL, so both positions are
	// captured. Restoring only the left would leave the right channel's filter
	// running on from the present, which on a hard-panned Next program is an
	// audible step at the rewind point on one side only.
	DCLeft    audio.DCState
	DCRight   audio.DCState
	DCLimit   int32
	DCEnabled bool

	FastLoad    bool
	FEReadCount uint64
	Port123BVal byte

	Palette [16]color.RGBA
}

// stateBorderChange and stateAudioEvent mirror the unexported per-frame event
// records for the same reason.
type stateBorderChange struct {
	Scanline int
	Colour   byte
}

type stateAudioEvent struct {
	TstateOffset int
	State        bool
}

// StateID identifies the ULA in a captured machine state.
func (u *ULA) StateID() string { return "ula" }

// SaveState captures the complete ULA state.
func (u *ULA) SaveState() []byte {
	s := ulaState{
		Flash:      u.flash,
		FlashCount: u.flashCount,

		TimexVideoMode: u.timexVideoMode,

		BorderColour:           u.BorderColour,
		FrameStartBorderColour: u.frameStartBorderColour,
		BorderChanges:          saveBorderChanges(u.borderChanges),

		Mic:                 u.Mic,
		TapeIn:              u.TapeIn,
		LastTapeTstate:      u.lastTapeTstate,
		TapeAudioEvents:     saveAudioEvents(u.tapeAudioEvents),
		FrameStartTapeState: u.frameStartTapeState,

		Speaker:                u.Speaker,
		FrameStartSpeakerState: u.frameStartSpeakerState,
		AudioEvents:            saveAudioEvents(u.audioEvents),
		FrameStartTstate:       u.frameStartTstate,

		KempstonEnabled: u.KempstonEnabled,
		KempstonState:   u.KempstonState,

		ULAOutputDisabled: u.ulaOutputDisabled,
		ULAScrollX:        u.ulaScrollX,
		ULAScrollY:        u.ulaScrollY,
		ULAFineScrollX:    u.ulaFineScrollX,

		HasRendered: u.hasRendered,

		DCLeft:    audio.CaptureDC(u.dc.Left()),
		DCRight:   audio.CaptureDC(u.dc.Right()),
		DCLimit:   u.dc.Limit(),
		DCEnabled: u.dcEnabled,

		FastLoad:    u.fastLoad,
		FEReadCount: u.feReadCount,
		Port123BVal: u.port123BVal,

		Palette: u.palette,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of fixed-size values and slices of them cannot
		// fail for any reason the caller could act on, and returning an error
		// here would push a dead branch into every caller.
		panic("ula: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded before any of it is applied, so a malformed state
// leaves the ULA untouched rather than half-rewound.
func (u *ULA) LoadState(b []byte) error {
	var s ulaState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("ula: decoding state: %w", err)
	}

	u.flash = s.Flash
	u.flashCount = s.FlashCount

	u.timexVideoMode = s.TimexVideoMode

	u.BorderColour = s.BorderColour
	u.frameStartBorderColour = s.FrameStartBorderColour
	u.borderChanges = loadBorderChanges(s.BorderChanges)

	u.Mic = s.Mic
	u.TapeIn = s.TapeIn
	u.lastTapeTstate = s.LastTapeTstate
	u.tapeAudioEvents = loadAudioEvents(s.TapeAudioEvents)
	u.frameStartTapeState = s.FrameStartTapeState

	u.Speaker = s.Speaker
	u.frameStartSpeakerState = s.FrameStartSpeakerState
	u.audioEvents = loadAudioEvents(s.AudioEvents)
	u.frameStartTstate = s.FrameStartTstate

	u.KempstonEnabled = s.KempstonEnabled
	u.KempstonState = s.KempstonState

	u.ulaOutputDisabled = s.ULAOutputDisabled
	u.ulaScrollX = s.ULAScrollX
	u.ulaScrollY = s.ULAScrollY
	u.ulaFineScrollX = s.ULAFineScrollX

	u.hasRendered = s.HasRendered

	audio.ApplyDC(u.dc.Left(), s.DCLeft)
	audio.ApplyDC(u.dc.Right(), s.DCRight)
	u.dc.SetLimit(s.DCLimit)
	u.dcEnabled = s.DCEnabled

	u.fastLoad = s.FastLoad
	u.feReadCount = s.FEReadCount
	u.port123BVal = s.Port123BVal

	u.palette = s.Palette
	return nil
}

// The event lists are converted element by element rather than reinterpreted,
// so a capture never shares a backing array with the running ULA — which goes
// on appending to and truncating both lists every frame.

func saveBorderChanges(in []borderChange) []stateBorderChange {
	if len(in) == 0 {
		return nil
	}
	out := make([]stateBorderChange, len(in))
	for i, c := range in {
		out[i] = stateBorderChange{Scanline: c.scanline, Colour: c.colour}
	}
	return out
}

func loadBorderChanges(in []stateBorderChange) []borderChange {
	out := make([]borderChange, len(in))
	for i, c := range in {
		out[i] = borderChange{scanline: c.Scanline, colour: c.Colour}
	}
	return out
}

func saveAudioEvents(in []audioEvent) []stateAudioEvent {
	if len(in) == 0 {
		return nil
	}
	out := make([]stateAudioEvent, len(in))
	for i, e := range in {
		out[i] = stateAudioEvent{TstateOffset: e.tstateOffset, State: e.state}
	}
	return out
}

func loadAudioEvents(in []stateAudioEvent) []audioEvent {
	out := make([]audioEvent, len(in))
	for i, e := range in {
		out[i] = audioEvent{tstateOffset: e.TstateOffset, state: e.State}
	}
	return out
}
