package ula

import (
	"bytes"
	"testing"
)

// Render must be a pure observation of the machine: asking twice for the same
// frame has to give the same picture.
//
// It did not. The compose walk steps the Copper and replays the raster
// journal, so a second Render ran the Copper program a second time and left
// the NextRegs somewhere else entirely. On TX-1696 the first render produced
// its title screen in 20 colours and every render after it produced a black
// frame, from identical machine state with no CPU time in between.
//
// That is not a corner case. Anything that renders alongside a render — a
// screenshot taken next to a frame, a measurement, a debugger view — got a
// picture the machine was never showing.
// This covers the classic path, which was always idempotent. The Next
// compositor path is the one that broke; it is guarded at the integration
// level, where a Copper and a real program exist to break it.
func TestRenderIsIdempotentForAFrame(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	page := u.mem.GetPage(5)
	for i := 0; i < 0x1800; i++ {
		page[i] = byte(i * 7)
	}
	for i := 0x1800; i < 0x1B00; i++ {
		page[i] = 0x47
	}

	first := u.Render()
	a := make([]byte, len(first.Pix))
	copy(a, first.Pix)

	for i := 0; i < 3; i++ {
		again := u.Render()
		if !bytes.Equal(a, again.Pix) {
			t.Fatalf("render %d differs from the first for the same frame", i+2)
		}
	}
}

// TestLastFrameDoesNotStepTheCopper is the Next-path guard, and the bug that
// actually bit.
//
// The compose walk steps the Copper as it goes, because a MOVE has to affect
// the segments after it within the same frame. That coupling is correct, so
// Render() is legitimately a mutation — but then merely LOOKING at the screen
// must not go through it. It did: screenshots and measurements called Render()
// again, running the Copper program a second time for the same frame. On
// TX-1696 the first render produced its title screen in 20 colours and every
// render after it a black frame, from identical state with no CPU time in
// between.
func TestLastFrameDoesNotStepTheCopper(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &recordingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.Render()
	first := len(c.steps)
	if first == 0 {
		t.Fatal("precondition: the Copper was never stepped by a render")
	}

	for i := 0; i < 3; i++ {
		u.LastFrame()
		if len(c.steps) != first {
			t.Fatalf("LastFrame call %d stepped the Copper %d extra times; looking at the screen must not run it",
				i+1, len(c.steps)-first)
		}
	}
}

// TestLastFrameComposesLazily pins that asking for the screen before anything
// has run still gives a picture, not a blank one.
func TestLastFrameComposesLazily(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	c := &recordingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.LastFrame() // nothing rendered yet
	if len(c.steps) == 0 {
		t.Error("LastFrame on a fresh ULA composed nothing")
	}
}

// TestComposeWalkRunsAgainOnANewFrame pins the other half: the guard must not
// freeze the picture. When the frame advances, the walk runs again.
func TestComposeWalkRunsAgainOnANewFrame(t *testing.T) {
	u, mem := newFloatingBusULA(t)
	c := &recordingCopper{}
	u.SetNextCopper(c)
	u.SetNextCompositor(stubCompositor{})

	u.Render()
	first := len(c.steps)

	// Advance the machine by a whole frame and render again.
	*mem.TStates += uint64(TStatesPerLine * LinesPerFrame)
	u.Render()

	if len(c.steps) <= first {
		t.Error("the Copper was not stepped on the next frame; the guard is freezing the render")
	}
}

// TestLastFrameReturnsTheFrameRenderReturned pins a bug introduced with
// LastFrame itself.
//
// Render has several exits: the 320-pixel base image, the 640-pixel frame the
// 80-column tilemap path builds, and the hi-res Layer 2 frame. LastFrame
// originally handed back u.img unconditionally, so in those modes it returned
// the base the frame was BUILT from rather than the frame itself — the
// NextZXOS Guide, which renders 80-column, came out as vertical stripes
// instead of its text.
func TestLastFrameReturnsTheFrameRenderReturned(t *testing.T) {
	u, _ := newFloatingBusULA(t)
	u.SetNextCompositor(wide80Compositor{})

	rendered := u.Render()
	last := u.LastFrame()
	if rendered != last {
		t.Fatalf("LastFrame returned a different image from Render (%dx%d vs %dx%d)",
			last.Bounds().Dx(), last.Bounds().Dy(),
			rendered.Bounds().Dx(), rendered.Bounds().Dy())
	}
	if got := last.Bounds().Dx(); got != 2*TotalWidth {
		t.Errorf("80-column frame is %d pixels wide, want %d", got, 2*TotalWidth)
	}
}

// wide80Compositor is a stub compositor that reports 80-column tilemap mode,
// which is what routes Render through the 640-pixel path.
type wide80Compositor struct{ stubCompositor }

func (wide80Compositor) TilemapIs80Col() bool { return true }
