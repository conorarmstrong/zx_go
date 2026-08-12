package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// The active video line (NextReg $1E/$1F) is a raster position, so it must
// stay inside the frame. A 128K-family frame is 311 lines; the counter must
// never report line 400 or 511.
//
// It did. BeamPosition masked the line to 9 bits — `(t / TStatesPerLine) &
// 0x1FF` — which bounds it at 511 rather than wrapping it at the frame, and
// the origin it measures from was only reset inside flushAudioFrame. That
// function returns early when audio is disabled, and is reached only from
// Render(), so any run that had audio off or did not render every frame let
// the offset grow without bound and turned the "raster line" into a
// free-running counter unrelated to the beam.
//
// TX-1696 polls $1E/$1F ~1700 times a frame waiting for a scanline; against a
// counter like that it waits on a line that means nothing.

// TestActiveVideoLineStaysInsideTheFrame pins the range.
func TestActiveVideoLineStaysInsideTheFrame(t *testing.T) {
	var ts uint64
	mem := &memory.Memory{}
	mem.TStates = &ts
	u := &ULA{mem: mem}

	// Sweep several frames' worth of T-states without ever resetting the
	// origin — the situation an audio-less or non-rendering run is in.
	maxLine := 0
	for ts = 0; ts < uint64(TStatesPerLine)*1200; ts += 97 {
		line := u.ActiveVideoLine()
		if line < 0 {
			t.Fatalf("t=%d: negative line %d", ts, line)
		}
		if line > maxLine {
			maxLine = line
		}
	}
	if maxLine >= LinesPerFrame {
		t.Errorf("active video line reached %d; a frame is %d lines, so it must never exceed %d",
			maxLine, LinesPerFrame, LinesPerFrame-1)
	}
}

// TestActiveVideoLineAdvancesWithoutAudio pins the property the raster-wait
// loops actually depend on: it must advance as T-states advance, with no
// audio subsystem and no Render() call in sight.
func TestActiveVideoLineAdvancesWithoutAudio(t *testing.T) {
	var ts uint64
	mem := &memory.Memory{}
	mem.TStates = &ts
	u := &ULA{mem: mem} // u.audio is nil

	seen := map[int]bool{}
	for ts = 0; ts < uint64(TStatesPerLine)*LinesPerFrame; ts += uint64(TStatesPerLine) {
		seen[u.ActiveVideoLine()] = true
	}
	if len(seen) != LinesPerFrame {
		t.Errorf("saw %d distinct lines over one frame, want %d — the counter must sweep the frame",
			len(seen), LinesPerFrame)
	}
}
