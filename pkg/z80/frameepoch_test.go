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
