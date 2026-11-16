package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// FrameID scopes the Next raster journal to one frame. It has to count video
// time, and it has to see the CPU's clock rewinds, or frames that run without
// a render pile their writes into one journal and the next replay rewinds to
// state from long ago.
func frameIDULA(t *testing.T, dir string) (*ULA, *memory.Memory) {
	t.Helper()
	createTestROMs(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })
	mem, err := memory.New(dir, roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	var ts, epoch uint64
	mem.SetTStatePtr(&ts)
	mem.SetFrameEpochPtr(&epoch)
	return New(mem, keyboard.New()), mem
}

func TestFrameIDChangesWithTheEpoch(t *testing.T) {
	u, mem := frameIDULA(t, "test_roms_ula_frameid_epoch")
	*mem.TStates = 1000
	a := u.FrameID()
	*mem.FrameEpoch++
	if b := u.FrameID(); b == a {
		t.Errorf("FrameID = %d across a clock rewind, want a new frame", b)
	}
}

func TestFrameIDCountsVideoTimeAtTurbo(t *testing.T) {
	u, mem := frameIDULA(t, "test_roms_ula_frameid_turbo")
	mem.SpeedMultiplier = func() int { return 8 }
	frame := uint64(u.FrameTStates())
	*mem.TStates = 0
	a := u.FrameID()
	*mem.TStates = frame*8 - 1
	if b := u.FrameID(); b != a {
		t.Errorf("FrameID at the frame's last CPU T-state = %d, want %d", b, a)
	}
	*mem.TStates = frame * 8
	if b := u.FrameID(); b == a {
		t.Errorf("FrameID one frame on = %d, want a new frame", b)
	}
}
