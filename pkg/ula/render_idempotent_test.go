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
