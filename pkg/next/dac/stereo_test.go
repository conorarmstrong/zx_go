package dac

import "testing"

// The four DAC channels are not four ways of saying the same thing: two of them
// are the left output and two are the right.
//
//	pcm_L_o <= ('0' & chA) + ('0' & chB);
//	pcm_R_o <= ('0' & chC) + ('0' & chD);
//	                                    soundrive.vhd:110-111
//
// and the entity's own port comments label chA/chB "-- left" and chC/chD
// "-- right" (:36-43). A program driving a stereo sample pair writes one value
// to the A/B pair and another to the C/D pair; meaning all four together turns
// that into a single centred mono signal at half the intended level.
//
// The same file also settles what silence is. Reset loads X"80" into all four
// (:71-74), which is mid-scale for a centred DAC. Level 0 is not silence, it is
// the negative rail.

// TestTheChannelsSplitLeftAndRight is the whole point of the change: a value on
// the A/B pair must reach the left output and leave the right alone.
func TestTheChannelsSplitLeftAndRight(t *testing.T) {
	for _, tc := range []struct {
		name           string
		port           uint16
		wantLeftMoved  bool
		wantRightMoved bool
	}{
		{"channel A is left", 0x1F, true, false},
		{"channel B is left", 0x0F, true, false},
		{"channel C is right", 0x4F, false, true},
		{"channel D is right", 0x5F, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			b.WritePort(tc.port, 0xFF) // full scale, away from the $80 rest level

			buf := []int16{0, 0}
			b.MixIntoStereo(buf)

			if (buf[0] != 0) != tc.wantLeftMoved {
				t.Errorf("left = %d, moved = %v, want moved = %v", buf[0], buf[0] != 0, tc.wantLeftMoved)
			}
			if (buf[1] != 0) != tc.wantRightMoved {
				t.Errorf("right = %d, moved = %v, want moved = %v", buf[1], buf[1] != 0, tc.wantRightMoved)
			}
		})
	}
}

// A stereo pair written to the two sides must come back out as a pair. This is
// the case the old mono fold destroyed outright: it averaged a hard-left and a
// hard-right sample into silence.
func TestAHardPannedPairSurvives(t *testing.T) {
	b := New()
	b.WritePort(0x1F, 0xFF) // A: left at full scale
	b.WritePort(0x0F, 0xFF) // B: left at full scale
	b.WritePort(0x4F, 0x00) // C: right at the negative rail
	b.WritePort(0x5F, 0x00) // D: right at the negative rail

	buf := []int16{0, 0}
	b.MixIntoStereo(buf)

	if buf[0] <= 0 {
		t.Errorf("left = %d, want strongly positive", buf[0])
	}
	if buf[1] >= 0 {
		t.Errorf("right = %d, want strongly negative", buf[1])
	}
	if buf[0] == buf[1] {
		t.Fatal("the two outputs are identical: the pair was folded to mono")
	}
}

// Reset is silence, and silence is $80. Resetting to 0 leaves every channel on
// the negative rail, which is a full-scale DC offset the AC-coupling downstream
// then has to remove — so it is inaudible, and wrong, and hides itself.
func TestResetLeavesTheChannelsAtMidScale(t *testing.T) {
	b := New()
	for c := ChannelA; c <= ChannelD; c++ {
		if got := b.Level(c); got != 0x80 {
			t.Errorf("new bank channel %d = %#02x, want 0x80 (soundrive.vhd:71-74)", c, got)
		}
	}

	b.WritePort(0x1F, 0x00)
	b.Reset()
	for c := ChannelA; c <= ChannelD; c++ {
		if got := b.Level(c); got != 0x80 {
			t.Errorf("after Reset channel %d = %#02x, want 0x80", c, got)
		}
	}

	// And that rest level really is silence through the mixer.
	buf := []int16{1234, -4321}
	b.MixIntoStereo(buf)
	if buf[0] != 1234 || buf[1] != -4321 {
		t.Errorf("a reset bank contributed %v to [1234 -4321]: reset is not silent", buf)
	}
}

// The event-timed frame path carries the same split. The ULA drives this one
// rather than MixIntoStereo, so a stereo MixIntoStereo alone would leave the
// production path mono.
func TestTheGeneratedFrameIsInterleavedStereo(t *testing.T) {
	const frames, tstates = 8, 800
	b := New()
	b.WritePort(0x1F, 0xFF) // A: left up
	b.WritePort(0x0F, 0xFF) // B: left up
	b.Record(0)

	out := b.GenerateFrameStereo(frames, tstates)
	if len(out) != frames*2 {
		t.Fatalf("frame length = %d, want %d interleaved slots", len(out), frames*2)
	}
	for i := 0; i < frames; i++ {
		if out[i*2] <= 0 {
			t.Errorf("frame %d left = %d, want positive", i, out[i*2])
		}
		if out[i*2+1] != 0 {
			t.Errorf("frame %d right = %d, want 0: nothing was written to C or D", i, out[i*2+1])
		}
	}
}

// The event path has to carry both sides per event, not one mixed level. A
// write to the left followed by a write to the right must show up as two
// separate steps, each on its own channel.
func TestEachSideKeepsItsOwnEventTimeline(t *testing.T) {
	const frames, tstates = 4, 400
	b := New()
	b.WritePort(0x1F, 0xFF) // left goes up at t=0
	b.Record(0)
	b.WritePort(0x4F, 0x00) // right goes down half way through
	b.Record(tstates / 2)

	out := b.GenerateFrameStereo(frames, tstates)

	// First half: left already raised, right still at its rest level.
	if out[0] <= 0 {
		t.Errorf("left at the start = %d, want positive", out[0])
	}
	if out[1] != 0 {
		t.Errorf("right at the start = %d, want 0 (nothing had moved it yet)", out[1])
	}
	// Second half: right has dropped, left is unchanged.
	last := (frames - 1) * 2
	if out[last] != out[0] {
		t.Errorf("left moved from %d to %d with no left-hand write", out[0], out[last])
	}
	if out[last+1] >= 0 {
		t.Errorf("right at the end = %d, want negative after the write to channel C", out[last+1])
	}
}

// A guest driving all four channels together is asking for a centred mono
// signal, and must get one at the same level on both sides.
func TestDrivingAllFourChannelsIsCentred(t *testing.T) {
	b := New()
	for _, p := range []uint16{0x1F, 0x0F, 0x4F, 0x5F} {
		b.WritePort(p, 0xC0)
	}
	buf := []int16{0, 0}
	b.MixIntoStereo(buf)
	if buf[0] != buf[1] {
		t.Errorf("all four channels at the same level gave (%d, %d), want a centred pair", buf[0], buf[1])
	}
	if buf[0] <= 0 {
		t.Errorf("level 0xC0 gave %d, want positive (it is above the $80 mid-scale)", buf[0])
	}
}
