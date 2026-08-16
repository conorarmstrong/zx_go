package multiface

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The four bits a snapshot format would think of — window paged in, red button,
// software lockout, enabled — are the small part. What actually decides what
// happens next is where in an NMI session the unit is, and the session carries
// something no guest instruction can read back: the snapshot of the host paging
// taken when the window first paged in (savedPagingState), which the OUT that
// re-arms the hardware puts back. Capture the flags and drop the session, and a
// machine restored mid-session ends that session by restoring nothing — the
// host carries on running on whatever paging the MF ROM left behind. That is
// the shape of the 128K BASIC launch corruption, so it is captured here.
//
// Deliberately NOT captured, and why:
//
//   - rom, romFile, variant: construction-time configuration. loadROM is the
//     only writer of rom and it runs once, in NewMultiface; the variant decides
//     the port decode and is what makes it a different machine, not a different
//     state. Restoring a capture into a differently-configured unit is a wiring
//     error the machinestate registry cannot see, the same way the AY does not
//     carry its clock rate.
//   - memory: live wiring to the rest of the machine, not state.
//   - memory.PagingFrozen: set and cleared by this unit, but it is a field of
//     the memory device and is captured there. Capturing it here as well would
//     give one flag two owners and make the restore order matter.
//   - The Multiface 3 $7F3F/$1F3F paging readback is NOT state of this type.
//     That path is memory.mfActive (the enable) and memory's own $7FFD/$1FFD
//     registers (the values returned), read by ula.ReadPort — see
//     pkg/ula/ula.go and pkg/ula/multiface_readback_test.go. It has no latch of
//     its own: the IN returns the live paging registers, so it is captured
//     exactly when the memory device is. There is nothing to mirror here, and
//     mirroring it would create a second copy that could disagree.
//   - Core (core.go) holds its own registered state (nmi_active, invisible,
//     mf_enable, port_io_dly). It is the FPGA transcription and is not wired
//     into any machine yet — only its golden test drives it — so it is not a
//     registered device and nothing would restore it. When it is wired up it
//     needs its own capture; see the report accompanying this change.
//
// multifaceState is the wire form. Every field is exported because gob only
// encodes exported fields, and every mutable field of the running unit is
// present: adding a field to Multiface without adding it here is the failure
// this package's tests are built to catch.
type multifaceState struct {
	RAM []byte

	Enabled   bool
	ROMPaged  bool
	RedButton bool
	Invisible bool

	SessionActive bool
	// SavedPaging is the encoded host paging snapshot taken at page-in, nil
	// when no session is holding one.
	SavedPaging []byte
}

// StateID identifies the unit in a captured machine state.
func (mf *Multiface) StateID() string { return "multiface" }

// SaveState captures the complete unit state.
func (mf *Multiface) SaveState() []byte {
	s := multifaceState{
		RAM:           mf.ram,
		Enabled:       mf.enabled,
		ROMPaged:      mf.romPaged,
		RedButton:     mf.redButton,
		Invisible:     mf.invisible,
		SessionActive: mf.sessionActive,
	}
	if mf.savedPagingState != nil {
		b, err := mf.savedPagingState.MarshalBinary()
		if err != nil {
			panic("multiface: encoding saved paging state: " + err.Error())
		}
		s.SavedPaging = b
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding a struct of a byte slice and four flags cannot fail for any
		// reason the caller could act on, and returning an error here would
		// push a dead branch into every caller.
		panic("multiface: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// Everything is decoded and checked before the first field is written, so a
// blob that is not this unit's state leaves the unit alone rather than half
// applied.
func (mf *Multiface) LoadState(b []byte) error {
	var s multifaceState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("multiface: decoding state: %w", err)
	}
	if len(s.RAM) != len(mf.ram) {
		return fmt.Errorf("multiface: state carries %d bytes of RAM, this Multiface has %d",
			len(s.RAM), len(mf.ram))
	}
	var paging *memory.PagingState
	if len(s.SavedPaging) > 0 {
		paging = &memory.PagingState{}
		if err := paging.UnmarshalBinary(s.SavedPaging); err != nil {
			return fmt.Errorf("multiface: decoding saved paging state: %w", err)
		}
	}

	copy(mf.ram, s.RAM)
	mf.enabled = s.Enabled
	mf.romPaged = s.ROMPaged
	mf.redButton = s.RedButton
	mf.invisible = s.Invisible
	mf.sessionActive = s.SessionActive
	mf.savedPagingState = paging
	return nil
}
