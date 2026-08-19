package sam

import "testing"

// Four faults a review found in the first version of the SAM's audio path.
// Each is written as the observation that exposed it.

// The DC blocker's clamp models a 1-BIT source: a high-pass's step response is
// the step height, so a full speaker toggle overshoots to twice the level the
// cone is driven to, and the output is bounded to that level.
//
// Running the clamp over the SUMMED bus turns it into a level cap on the
// SAA1099 as well. The SAA is a six-channel chip that reaches full scale on its
// own, so an 8000 limit clipped it to a quarter of its range and pinned nearly
// half the samples in a loud frame.
func TestTheSAAIsNotClippedByTheBeepersClamp(t *testing.T) {
	loud := func(m *Machine) {
		for _, r := range []struct{ reg, val byte }{
			{0x1C, 0x02},
			{0x00, 0xFF}, {0x01, 0xFF}, {0x02, 0xFF}, {0x03, 0xFF}, {0x04, 0xFF}, {0x05, 0xFF},
			{0x08, 0x80}, {0x09, 0x80}, {0x0A, 0x80}, {0x0B, 0x80}, {0x0C, 0x80}, {0x0D, 0x80},
			{0x10, 0x33}, {0x11, 0x33}, {0x12, 0x33},
			{0x14, 0x3F}, {0x1C, 0x01},
		} {
			writeSAA(m, r.reg, r.val)
		}
	}

	bare := newTestMachine(t)
	loud(bare)
	raw := make([]int16, SamplesPerFrame*2)
	bare.SAA.GenerateStereo(raw)

	mixed := newTestMachine(t)
	loud(mixed)
	out := make([]int16, SamplesPerFrame*2)
	mixed.GenerateAudioStereo(out)

	peak := func(xs []int16) int16 {
		var p int16
		for _, v := range xs {
			if v > p {
				p = v
			}
		}
		return p
	}
	rawPeak, outPeak := peak(raw), peak(out)
	if rawPeak == 0 {
		t.Fatal("the SAA fixture produced silence")
	}
	// The mix is allowed to differ from the raw chip — the beeper is summed in
	// and both are AC-coupled — but not to lose most of its range.
	if int32(outPeak) < int32(rawPeak)/2 {
		t.Errorf("SAA peak %d became %d through the mix: the beeper's cone clamp is "+
			"capping the whole bus", rawPeak, outPeak)
	}

	pinned := 0
	for _, v := range out {
		if v == beeperAmplitude || v == -beeperAmplitude {
			pinned++
		}
	}
	if pinned > len(out)/20 {
		t.Errorf("%d of %d samples sit exactly on the beeper's clamp: the SAA is being "+
			"hard-clipped", pinned, len(out))
	}
}

// An isolated beeper toggle still clicks at the speaker's level rather than at
// twice it. Moving the clamp off the summed bus must not lose the reason it was
// there.
func TestAnIsolatedToggleIsStillClampedToTheSpeakerLevel(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, false)
	beepAt(t, m, CyclesPerFrame/2, true)

	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf)

	var peak int16
	for _, v := range buf {
		if v > peak {
			peak = v
		}
	}
	if peak > beeperAmplitude {
		t.Errorf("peak = %d, want no more than the speaker level %d", peak, beeperAmplitude)
	}
	if peak == 0 {
		t.Error("the toggle produced nothing")
	}
}

// A reset returns the speaker to rest. Clearing the BORDER latch without
// clearing the model behind it leaves the emulated speaker asserted while the
// port says otherwise, so the guest's next BEEP-high write is not an edge and
// the click is swallowed.
func TestResetReturnsTheSpeakerToRest(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, true)
	m.GenerateAudioStereo(make([]int16, SamplesPerFrame*2))

	m.Reset()

	if m.beeperLevel {
		t.Error("the speaker is still asserted after a reset")
	}
	if m.frameStartBeeper {
		t.Error("the level carried into the next frame is still high after a reset")
	}
	if len(m.beeperEvents) != 0 {
		t.Errorf("%d beeper events survived a reset", len(m.beeperEvents))
	}

	// And the guest's first post-reset click is heard.
	beepAt(t, m, 1000, true)
	if len(m.beeperEvents) != 1 {
		t.Errorf("recorded %d edges for the first click after a reset, want 1: the model "+
			"still thought the speaker was high", len(m.beeperEvents))
	}
}

// With no audio consumer the recorded events must not accumulate. --no-sound
// and every headless run leave GenerateAudioStereo uncalled for the process
// lifetime, and the capture path gob-encodes the whole list every rewind frame.
func TestTheEventListDoesNotGrowWithoutAConsumer(t *testing.T) {
	m := newTestMachine(t)
	for f := 0; f < 20; f++ {
		m.RunFrame()
		for i := 0; i < 50; i++ {
			m.frameStart = 0
			m.CPU.SetTstates(uint64(i * 100))
			v := byte(0)
			if i%2 == 0 {
				v = borderBEEP
			}
			m.WritePort(0x00FE, v)
		}
	}
	if got := len(m.beeperEvents); got > 100 {
		t.Errorf("%d events pending after 20 frames with no consumer: the list is unbounded", got)
	}
}

// A level held across a frame boundary with no consumer still carries, so the
// frame after the drop starts from the right level rather than from silence.
func TestTheHeldLevelSurvivesADroppedFrame(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, true)
	m.RunFrame() // no GenerateAudioStereo: the events are dropped

	if !m.frameStartBeeper {
		t.Error("the held level was lost along with the dropped events")
	}
}

// An edge in the frame's overshoot window still counts.
//
// ExecuteFrame runs past its budget by the length of whichever instruction
// crossed the boundary, so a write in that window lands at an offset past
// CyclesPerFrame. The sample loop cannot reach it, and truncating the list
// afterwards threw it away — leaving the speaker at the level BEFORE the write
// for the whole of the next frame.
func TestAnEdgePastTheFrameEndIsNotLost(t *testing.T) {
	m := newTestMachine(t)
	beepAt(t, m, 0, true)
	m.GenerateAudioStereo(make([]int16, SamplesPerFrame*2))

	// The guest drops the speaker in the overshoot window.
	m.frameStart = 0
	m.CPU.SetTstates(CyclesPerFrame + 10)
	m.WritePort(0x00FE, 0)

	m.GenerateAudioStereo(make([]int16, SamplesPerFrame*2))
	if m.frameStartBeeper {
		t.Error("the speaker is still high: the edge past the frame end was discarded " +
			"instead of carried into the next frame")
	}
}

// The event list stays in T-state order. A write whose timestamp falls before
// the frame start — a restore that rewinds the CPU clock, say — must not be
// filed ahead of edges already recorded, because the sample loop is a single
// forward scan that never looks back.
func TestTheEventListStaysInOrder(t *testing.T) {
	m := newTestMachine(t)
	m.frameStart = 0
	m.CPU.SetTstates(5000)
	m.WritePort(0x00FE, borderBEEP) // an edge at 5000

	// Now a write that appears to predate the frame.
	m.CPU.SetTstates(0)
	m.frameStart = 9999
	m.WritePort(0x00FE, 0)

	for i := 1; i < len(m.beeperEvents); i++ {
		if m.beeperEvents[i].tstate < m.beeperEvents[i-1].tstate {
			t.Fatalf("events out of order: %v", m.beeperEvents)
		}
	}
}
