package z80

import "testing"

// ExecuteFrame rewinds the T-state clock by the frame budget at the end of
// every frame, so the clock alone cannot tell one frame from the next. The
// frame epoch counts those rewinds, and the other discontinuities (reset, state
// load), so a consumer keyed on "which frame is this" sees each one as new.

type epochMem struct {
	fiMem
	epoch *uint64
}

func (m *epochMem) SetFrameEpochPtr(p *uint64) { m.epoch = p }

func TestFrameEpochIsSharedWithMemory(t *testing.T) {
	m := &epochMem{fiMem: fiMem{b: make([]byte, 0x10000)}}
	New(m, fiULA{})
	if m.epoch == nil {
		t.Fatal("New did not hand memory the frame epoch")
	}
}

func TestFrameEpochAdvancesAtEveryClockRewind(t *testing.T) {
	m := &epochMem{fiMem: fiMem{b: make([]byte, 0x10000)}}
	c := New(m, fiULA{})
	c.PC = 0x8000
	start := *m.epoch
	c.ExecuteFrame(1000)
	if got := *m.epoch; got != start+1 {
		t.Errorf("epoch after one frame = %d, want %d", got, start+1)
	}
	c.ExecuteFrame(1000)
	if got := *m.epoch; got != start+2 {
		t.Errorf("epoch after two frames = %d, want %d", got, start+2)
	}
}

func TestFrameEpochAdvancesOnResetAndLoad(t *testing.T) {
	m := &epochMem{fiMem: fiMem{b: make([]byte, 0x10000)}}
	c := New(m, fiULA{})
	snap := c.SaveState()
	e := *m.epoch
	c.Reset()
	if *m.epoch == e {
		t.Error("Reset left the epoch unchanged")
	}
	e = *m.epoch
	if err := c.LoadState(snap); err != nil {
		t.Fatal(err)
	}
	if *m.epoch == e {
		t.Error("LoadState left the epoch unchanged")
	}
}

// Clock runs on across the frame rewind; ClockGeneration moves only where it
// really jumps.
func TestClockIsUnbrokenAcrossFrames(t *testing.T) {
	m := &epochMem{fiMem: fiMem{b: make([]byte, 0x10000)}}
	c := New(m, fiULA{})
	c.PC = 0x8000
	g := c.ClockGeneration()
	before := c.Clock()
	c.ExecuteFrame(1000)
	mid := c.Clock()
	c.ExecuteFrame(1000)
	after := c.Clock()
	if mid < before+1000 || after < mid+1000 {
		t.Errorf("Clock %d -> %d -> %d across two 1000 T frames", before, mid, after)
	}
	if c.ClockGeneration() != g {
		t.Error("a frame rewind changed the clock generation")
	}
	c.Reset()
	if c.ClockGeneration() == g {
		t.Error("Reset left the clock generation unchanged")
	}
}
