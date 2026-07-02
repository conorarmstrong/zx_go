package rzx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

// ErrNoActiveInput is returned when StoreFrame is called without a
// preceding StartInput. Mirrors libspectrum_rzx_store_frame's behaviour
// at libspectrum/rzx.c:423.
var ErrNoActiveInput = errors.New("rzx: StoreFrame called with no active input block")

// AutosaveInterval is the number of emulator frames between automatic
// snapshot insertions when autosaves are enabled. Matches FUSE's
// AUTOSAVE_INTERVAL constant at rzx.c:105 (5 seconds × 50 Hz).
const AutosaveInterval = 5 * 50

// CompetitionSpeedTolerance is the maximum permitted deviation from
// 100% emulation speed (in percentage points) during a competition-mode
// recording. Matches FUSE's SPEED_TOLERANCE at rzx.c:102.
const CompetitionSpeedTolerance = 5.0

// ErrCompetitionSpeedExceeded is returned by RecordCompetitionFrame
// when the emulator has drifted further than CompetitionSpeedTolerance
// from real time. Recording drivers should treat this as fatal —
// FUSE stops the recording and surfaces a UI error (rzx.c:670).
var ErrCompetitionSpeedExceeded = errors.New("rzx: competition mode: emulation speed outside tolerance")

// ErrCompetitionRollbackForbidden is returned when Rollback or
// RollbackTo is called on a competition-mode recording. FUSE disables
// the rollback menu items at start_recording time (rzx.c:441) — we
// enforce it explicitly so misuse from the API surface fails loudly.
var ErrCompetitionRollbackForbidden = errors.New("rzx: competition mode: rollback is not allowed")

// Recording drives an in-progress RZX recording session. It owns a
// File that gets blocks appended as the user records, and exposes the
// per-frame and per-IN-read entry points the emulator's main loop and
// ULA hook will call.
//
// Lifecycle (mirroring FUSE rzx.c):
//
//  1. NewRecording() — fresh, empty File
//  2. Optionally AddSnap(snap, false) — embed the initial snapshot
//  3. StartInput(tstates) — open an input recording block at the
//     given T-state offset
//  4. For each emulator frame:
//     a. RecordIN(b) is called by the ULA hook for every IN read
//     b. StoreFrame(instructions) closes the frame, capturing the
//     per-frame instruction count and the IN bytes accumulated
//     since the previous StoreFrame
//  5. Optionally AddSnap(snap, true) — insert an autosave, which
//     Rollback/RollbackTo can later restore
//  6. Stop() / Bytes() — serialise the file and write it out
type Recording struct {
	file *File

	// Active input block — points into file.Blocks for the most
	// recent BlockInput. nil between StartInput and the next call.
	activeInput *InputBlock

	// inBuf accumulates IN bytes for the current frame between
	// StoreFrame calls. Cleared after each StoreFrame. Allocated
	// once and reused to keep the per-frame allocation cost flat.
	inBuf bytes.Buffer

	// autosaveFrameCount counts frames since the last automatic
	// snapshot. Reset by AddAutosave, advanced by StoreFrame when
	// autosaves are enabled. Mirrors FUSE's autosave_frame_count
	// at rzx.c:63.
	autosaveFrameCount int

	// AutosavesEnabled toggles the automatic-snapshot path.
	// Recording drivers set this from a user preference.
	AutosavesEnabled bool

	// CompetitionMode flags this recording as a competition entry.
	// In competition mode autosaves and rollback are disabled
	// (FUSE rzx.c:430-444), the emulator must run within
	// CompetitionSpeedTolerance percent of real time, and the file
	// header is marked Signed. DSA signing/verification is implemented
	// in sign.go (Sign/Verify); the caller supplies the DSA key. As in
	// FUSE, a recording may still be written unsigned (rzx.c:432).
	CompetitionMode bool
}

// NewRecording creates a fresh, empty recording.
func NewRecording() *Recording {
	return &Recording{file: &File{}}
}

// AddSnap appends a snapshot block to the recording. The automatic
// flag distinguishes intermediate snapshots (autosaves that Rollback /
// RollbackTo can restore) from the user-driven initial / final snapshot.
//
// libspectrum_rzx_add_snap implicitly stops any active input block
// before inserting the snapshot (rzx.c:297). We do the same so the
// frames stay properly bracketed.
func (r *Recording) AddSnap(snap *SnapshotBlock, automatic bool) {
	r.stopInput()
	snap.Automatic = automatic
	r.file.Blocks = append(r.file.Blocks, Block{Type: BlockSnapshot, Snapshot: snap})
}

// AddCreator appends a creator block — typically called once at the
// start of recording so playback tools can identify the producer.
func (r *Recording) AddCreator(c *CreatorBlock) {
	r.file.Blocks = append(r.file.Blocks, Block{Type: BlockCreator, Creator: c})
}

// StartInput opens a fresh input recording block at the given T-state
// offset and makes it the active target for subsequent StoreFrame
// calls. Mirrors libspectrum_rzx_start_input (libspectrum/rzx.c:268).
func (r *Recording) StartInput(tstates uint32) {
	block := &InputBlock{Tstates: tstates}
	r.file.Blocks = append(r.file.Blocks, Block{Type: BlockInput, Input: block})
	r.activeInput = block
	r.inBuf.Reset()
}

// stopInput closes the active input block. Idempotent. Called
// automatically by AddSnap and Stop.
func (r *Recording) stopInput() {
	r.activeInput = nil
	r.inBuf.Reset()
}

// RecordIN is the ULA-hook entry point. The ULA calls this for every
// IN-port read so the byte gets appended to the current frame's
// recorded sequence.
func (r *Recording) RecordIN(b byte) {
	if r.activeInput == nil {
		// IN reads outside an active recording window are
		// dropped silently — matches FUSE which only stores
		// bytes when rzx_recording is set.
		return
	}
	r.inBuf.WriteByte(b)
}

// StoreFrame closes the in-progress frame, recording the supplied
// instruction count alongside the IN bytes captured since the previous
// StoreFrame. The byte buffer is reset for the next frame.
//
// Repeat-last collapsing matches libspectrum_rzx_store_frame at
// libspectrum/rzx.c:436: if the new frame's IN sequence is identical
// to the last non-repeated frame's, the new frame is encoded as
// RepeatLast (which the writer turns into the 0xFFFF marker).
func (r *Recording) StoreFrame(instructions uint16) error {
	if r.activeInput == nil {
		return ErrNoActiveInput
	}

	inBytes := r.inBuf.Bytes()
	frame := Frame{Instructions: instructions}

	// Repeat-last collapsing: if this frame's IN bytes exactly
	// match the last *non-repeated* frame's bytes, encode it as
	// RepeatLast (the writer turns this into the 0xFFFF marker).
	frames := r.activeInput.Frames
	if len(frames) > 0 && len(inBytes) > 0 {
		last := &frames[r.activeInput.nonRepeat]
		if !last.RepeatLast && bytes.Equal(last.InBytes, inBytes) {
			frame.RepeatLast = true
		}
	}

	if !frame.RepeatLast {
		frame.InBytes = append([]byte(nil), inBytes...)
		r.activeInput.nonRepeat = len(frames)
	}

	r.activeInput.Frames = append(r.activeInput.Frames, frame)
	r.inBuf.Reset()

	if r.AutosavesEnabled {
		r.autosaveFrameCount++
	}
	return nil
}

// AutosaveDue reports whether enough frames have elapsed since the
// last automatic snapshot to warrant a new one. Drivers call this
// after each StoreFrame; on true, the driver captures a fresh
// snapshot from the live emulator state and passes it to AddAutosave.
//
// Returns false if autosaves are disabled.
func (r *Recording) AutosaveDue() bool {
	return r.AutosavesEnabled && r.autosaveFrameCount >= AutosaveInterval
}

// AddAutosave inserts an automatic snapshot, opens a fresh input
// recording block at the supplied T-state offset, and runs the
// pruning policy. The supplied snapshot block is marked Automatic so
// Finalise can drop it later.
//
// Mirrors FUSE's autosave_frame at rzx.c:609.
func (r *Recording) AddAutosave(snap *SnapshotBlock, tstates uint32) {
	r.AddSnap(snap, true) // also closes the active input
	r.StartInput(tstates)
	r.autosaveFrameCount = 0
	r.pruneAutosaves()
}

// pruneAutosaves implements FUSE's tiered autosave decay policy
// (rzx.c:557). Each automatic snapshot has a "frames before now"
// distance; if that distance falls EXACTLY on one of the tier
// boundaries (15s / 60s / 300s ago) AND the previous autosave is
// less than 2× as old, the snapshot is dropped. Net effect: history
// stays dense for the recent past and exponentially sparse further
// back, capping the autosave list at ~log scale instead of linear.
func (r *Recording) pruneAutosaves() {
	// Collect the cumulative frame count at each automatic
	// snapshot, plus its index in the block list.
	type point struct {
		blockIdx int
		ageAtNow int // distance from "now" in frames, filled in below
	}
	var autosaves []point
	frames := 0
	for i, b := range r.file.Blocks {
		switch b.Type {
		case BlockInput:
			frames += len(b.Input.Frames)
		case BlockSnapshot:
			if b.Snapshot.Automatic {
				autosaves = append(autosaves, point{blockIdx: i, ageAtNow: frames})
			}
		}
	}
	// Convert "frames at the moment of capture" → "frames before now".
	for i := range autosaves {
		autosaves[i].ageAtNow = frames - autosaves[i].ageAtNow
	}

	// Walk newest-to-oldest. Same predicate as FUSE rzx.c:597:
	//   age == 750 || age == 3000 || age == 15000
	//   AND previous autosave age < 2× this age
	const (
		tier15s  = 15 * 50
		tier60s  = 60 * 50
		tier300s = 300 * 50
	)
	for i := len(autosaves) - 1; i > 0; i-- {
		this := autosaves[i]
		prev := autosaves[i-1]
		isTier := this.ageAtNow == tier15s || this.ageAtNow == tier60s || this.ageAtNow == tier300s
		if !isTier {
			continue
		}
		if prev.ageAtNow >= 2*this.ageAtNow {
			continue
		}
		// Delete the block at this.blockIdx.
		r.file.Blocks = append(r.file.Blocks[:this.blockIdx], r.file.Blocks[this.blockIdx+1:]...)
		// Indices shift — adjust subsequent autosave entries
		// (we're walking backwards so they've already been
		// processed and won't be touched again, but be safe).
	}
}

// Bytes serialises the recording to a fresh byte slice using the
// supplied options. Closes any active input block first so the file
// is in a consistent state. Competition-mode recordings force the
// Signed header flag; the caller is responsible for adding the
// sign-start/sign-end blocks and calling Sign (see sign.go) before
// serialising, since a recording may exist unsigned in the interim.
func (r *Recording) Bytes(opts WriteOptions) ([]byte, error) {
	r.stopInput()
	if r.CompetitionMode {
		opts.Signed = true
	}
	return r.file.Write(opts)
}

// WriteFile serialises the recording and writes it to disk in one shot.
// Convenience wrapper around Bytes + os.WriteFile so the caller doesn't
// have to manage the buffer.
func (r *Recording) WriteFile(path string, opts WriteOptions) error {
	data, err := r.Bytes(opts)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Rollback removes everything after the LAST snapshot block in the
// recording, returning that snapshot. The recording is left ready to
// resume from that point — the caller should call StartInput again
// after applying the snapshot to the live emulator.
//
// Mirrors libspectrum_rzx_rollback (libspectrum/rzx.c:309). Returns
// ErrCompetitionRollbackForbidden if the recording is in competition
// mode — FUSE disables the rollback UI in that case (rzx.c:441).
func (r *Recording) Rollback() (*SnapshotBlock, error) {
	if r.CompetitionMode {
		return nil, ErrCompetitionRollbackForbidden
	}
	r.stopInput()
	for i := len(r.file.Blocks) - 1; i >= 0; i-- {
		if r.file.Blocks[i].Type == BlockSnapshot {
			snap := r.file.Blocks[i].Snapshot
			r.file.Blocks = r.file.Blocks[:i+1]
			return snap, nil
		}
	}
	return nil, fmt.Errorf("rzx: rollback: no snapshot block in recording")
}

// RollbackTo removes everything after the `which`-th snapshot block,
// returning that snapshot. Used to implement multi-step undo from a
// list of available rollback points.
//
// Mirrors libspectrum_rzx_rollback_to (libspectrum/rzx.c:351). Returns
// ErrCompetitionRollbackForbidden in competition mode.
func (r *Recording) RollbackTo(which int) (*SnapshotBlock, error) {
	if r.CompetitionMode {
		return nil, ErrCompetitionRollbackForbidden
	}
	r.stopInput()
	seen := -1
	for i, block := range r.file.Blocks {
		if block.Type != BlockSnapshot {
			continue
		}
		seen++
		if seen == which {
			snap := block.Snapshot
			r.file.Blocks = r.file.Blocks[:i+1]
			return snap, nil
		}
	}
	return nil, fmt.Errorf("rzx: rollback: snapshot block %d not found", which)
}

// CheckCompetitionSpeed validates that the supplied emulator speed
// (as a percentage; 100 = real-time) is within
// CompetitionSpeedTolerance of 100%. Returns
// ErrCompetitionSpeedExceeded otherwise. Drivers call this once per
// frame during competition recording — on error, the recording must
// be stopped immediately. Mirrors FUSE's check at rzx.c:670.
//
// Always succeeds when CompetitionMode is false.
func (r *Recording) CheckCompetitionSpeed(percent float64) error {
	if !r.CompetitionMode {
		return nil
	}
	if percent < 100-CompetitionSpeedTolerance || percent > 100+CompetitionSpeedTolerance {
		return fmt.Errorf("%w: speed %.1f%%", ErrCompetitionSpeedExceeded, percent)
	}
	return nil
}

// SnapshotPoints returns the cumulative frame count at each snapshot
// block in the recording. The driver passes this to the UI so the user
// can pick a rollback target. Mirrors FUSE's get_rollback_list at
// rzx.c:773.
func (r *Recording) SnapshotPoints() []int {
	var points []int
	frames := 0
	for _, block := range r.file.Blocks {
		switch block.Type {
		case BlockInput:
			frames += len(block.Input.Frames)
		case BlockSnapshot:
			points = append(points, frames)
		}
	}
	return points
}

// Finalise consolidates the recording for distribution: removes any
// intermediate (non-first) snapshot blocks and merges adjacent input
// recording blocks into one. Mirrors libspectrum_rzx_finalise at
// libspectrum/rzx.c:1706. Used by the "Finalise Recording" menu item
// in FUSE which prepares an in-progress autosave file for sharing.
func (r *Recording) Finalise() {
	r.stopInput()

	// Pass 1: drop interspersed snapshots, keeping only the first.
	pruned := make([]Block, 0, len(r.file.Blocks))
	seenSnap := false
	for _, b := range r.file.Blocks {
		if b.Type == BlockSnapshot {
			if seenSnap {
				continue
			}
			seenSnap = true
		}
		pruned = append(pruned, b)
	}

	// Pass 2: merge adjacent input blocks. Walking forward, if we
	// see two BlockInput in a row, append the second's frames to
	// the first and drop the second. The non_repeat index needs to
	// be re-anchored to the merged block — but only when the second
	// block actually has frames; a block left empty (StartInput with
	// no StoreFrame before the next block) has no meaningful
	// nonRepeat, and re-anchoring it anyway would point one past the
	// end of the survivor's Frames.
	merged := make([]Block, 0, len(pruned))
	for _, b := range pruned {
		if b.Type == BlockInput && len(merged) > 0 && merged[len(merged)-1].Type == BlockInput {
			prev := merged[len(merged)-1].Input
			if len(b.Input.Frames) > 0 {
				offset := len(prev.Frames)
				prev.Frames = append(prev.Frames, b.Input.Frames...)
				prev.nonRepeat = offset + b.Input.nonRepeat
			}
			continue
		}
		merged = append(merged, b)
	}
	r.file.Blocks = merged
}
