package rzx

import (
	"errors"
	"fmt"
)

// ErrPlaybackFinished is returned by Playback.Frame when the recording
// has been fully consumed and there are no more input blocks. Drivers
// should treat this as a clean end-of-playback signal, not a fault.
var ErrPlaybackFinished = errors.New("rzx: playback finished")

// ErrPlaybackUnderflow is returned by Playback.NextByte when the CPU
// asks for more IN bytes during a frame than the recording stored.
// Indicates the emulator desynced from the recording (typically a CPU
// bug or a divergent peripheral side-effect that produced an extra
// IN read).
var ErrPlaybackUnderflow = errors.New("rzx: playback underflow — CPU read more INs than recorded")

// Playback drives a single RZX playback session. It owns the cursor
// state into a File and exposes the per-frame and per-IN-read entry
// points the emulator's main loop and ULA hook will call.
//
// Lifecycle (mirroring FUSE rzx.c):
//
//  1. NewPlayback(file) — wrap a parsed File
//  2. Start(which) — position at the `which`-th input recording block
//     and surface the snapshot the recorder captured
//  3. For each emulator frame: read Instructions(), let the CPU run
//     that many instructions, then call Frame() to advance the cursor
//  4. Inside the frame, NextByte() supplies each IN-port read
//  5. Stop() releases the file
type Playback struct {
	file *File

	// currentBlock points at the active input block in file.Blocks
	// (-1 when nothing is active). currentFrame is the frame index
	// inside that block; dataFrame is the frame whose InBytes we're
	// consuming, which differs from frames[currentFrame] when the
	// current frame is RepeatLast. inIndex tracks consumption.
	currentBlock int
	currentFrame int
	dataFrame    *Frame
	inIndex      int

	finished bool
}

// NewPlayback wraps a parsed File for playback. Start must be called
// before any other method does useful work.
func NewPlayback(file *File) *Playback {
	return &Playback{file: file, currentBlock: -1}
}

// Start positions the cursor at the `which`-th input recording block
// (0-indexed, ignoring non-IRB blocks) and returns the snapshot block
// that immediately precedes it, if any. The driver applies that
// snapshot to the live emulator state before calling Frame() the first
// time.
//
// Mirrors libspectrum_rzx_start_playback (libspectrum/rzx.c:481).
func (p *Playback) Start(which int) (*SnapshotBlock, error) {
	skip := which
	var prev *SnapshotBlock
	for i, block := range p.file.Blocks {
		switch block.Type {
		case BlockSnapshot:
			prev = block.Snapshot
		case BlockInput:
			if skip > 0 {
				skip--
				continue
			}
			if block.Input == nil || len(block.Input.Frames) == 0 {
				return nil, fmt.Errorf("rzx: input block %d is empty", i)
			}
			p.currentBlock = i
			p.currentFrame = 0
			p.dataFrame = &block.Input.Frames[0]
			p.inIndex = 0
			p.finished = false
			return prev, nil
		}
	}
	return nil, fmt.Errorf("rzx: no input recording block at index %d", which)
}

// Stop tears down the playback cursor. Idempotent.
func (p *Playback) Stop() {
	p.currentBlock = -1
	p.dataFrame = nil
	p.finished = true
}

// Finished reports whether the playback has run off the end of the
// file. After Frame returns ErrPlaybackFinished, this method returns
// true and all other entry points are no-ops.
func (p *Playback) Finished() bool {
	return p.finished
}

// Instructions returns the instruction count the CPU should execute
// during the current frame. The driver passes this to
// CPU.ExecuteRZXFrame. Mirrors libspectrum_rzx_instructions
// (libspectrum/rzx.c:625).
//
// dataFrame may point at an earlier frame (when the current frame is
// RepeatLast); the instruction count belongs to the CURRENT frame, not
// the source of the IN bytes — so we look up frames[currentFrame]
// directly rather than going through dataFrame.
func (p *Playback) Instructions() uint16 {
	if p.currentBlock < 0 {
		return 0
	}
	frames := p.file.Blocks[p.currentBlock].Input.Frames
	if p.currentFrame >= len(frames) {
		return 0
	}
	return frames[p.currentFrame].Instructions
}

// Tstates returns the T-state offset at which the active input block
// began recording. Used by the driver to seed CPU.tstates so memory
// contention timings line up.
//
// Mirrors libspectrum_rzx_tstates (libspectrum/rzx.c:619).
func (p *Playback) Tstates() uint32 {
	if p.currentBlock < 0 {
		return 0
	}
	return p.file.Blocks[p.currentBlock].Input.Tstates
}

// NextByte returns the next IN-port byte for the current frame. The
// ULA's RZX playback hook calls this for every CPU IN instruction.
// Returns ErrPlaybackUnderflow if the CPU asks for more bytes than
// the recording captured for this frame — that's the canonical
// "emulator desynced" signal.
//
// Mirrors libspectrum_rzx_playback (libspectrum/rzx.c:592).
func (p *Playback) NextByte() (byte, error) {
	if p.dataFrame == nil {
		return 0, ErrPlaybackFinished
	}
	if p.inIndex >= len(p.dataFrame.InBytes) {
		return 0, fmt.Errorf("%w: frame %d wanted byte #%d, recorded %d",
			ErrPlaybackUnderflow, p.currentFrame, p.inIndex, len(p.dataFrame.InBytes))
	}
	b := p.dataFrame.InBytes[p.inIndex]
	p.inIndex++
	return b, nil
}

// Frame finalises the current frame and advances the cursor to the
// next one. The driver calls this once per emulator frame, AFTER
// running the CPU for Instructions() instructions.
//
// Returns:
//   - snap != nil: an intermediate snapshot block sits between the
//     just-finished input block and the next one. The driver should
//     apply it to the running emulator before continuing playback.
//     This is how RZX rollback / autosave files work — the recording
//     embeds periodic snapshots so playback can resync.
//   - ErrPlaybackFinished: the recording is exhausted. The driver
//     should call Stop and clean up.
//
// Validates that the CPU consumed exactly the number of IN reads the
// recording captured for this frame — any mismatch indicates desync
// and is reported as an error (matching libspectrum_rzx_playback_frame
// at libspectrum/rzx.c:529).
func (p *Playback) Frame() (snap *SnapshotBlock, err error) {
	if p.dataFrame == nil {
		return nil, ErrPlaybackFinished
	}

	// Validate we consumed every recorded IN byte. The CPU is
	// supposed to do exactly the same IN reads it did when the
	// recording was made; any mismatch means the emulator desynced.
	want := len(p.dataFrame.InBytes)
	if p.inIndex != want {
		return nil, fmt.Errorf("rzx: frame %d: CPU read %d INs, recording stored %d",
			p.currentFrame, p.inIndex, want)
	}

	// Advance to the next frame inside the current block.
	p.currentFrame++
	input := p.file.Blocks[p.currentBlock].Input
	if p.currentFrame < len(input.Frames) {
		// Update dataFrame for the new frame. RepeatLast frames
		// keep the previous frame's IN bytes.
		newFrame := &input.Frames[p.currentFrame]
		if !newFrame.RepeatLast {
			p.dataFrame = newFrame
		}
		p.inIndex = 0
		return nil, nil
	}

	// End of this input block — walk forward to the next IRB,
	// returning any snapshot block we pass over (which the driver
	// will apply before resuming).
	for i := p.currentBlock + 1; i < len(p.file.Blocks); i++ {
		switch p.file.Blocks[i].Type {
		case BlockSnapshot:
			snap = p.file.Blocks[i].Snapshot
		case BlockInput:
			if p.file.Blocks[i].Input == nil || len(p.file.Blocks[i].Input.Frames) == 0 {
				continue
			}
			p.currentBlock = i
			p.currentFrame = 0
			p.dataFrame = &p.file.Blocks[i].Input.Frames[0]
			p.inIndex = 0
			return snap, nil
		}
	}

	// No more input blocks — playback is done.
	p.dataFrame = nil
	p.finished = true
	return snap, ErrPlaybackFinished
}
