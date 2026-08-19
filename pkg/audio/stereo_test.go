package audio

import "testing"

// The audio path carries interleaved stereo from the producer to the oto sink.
//
// The reason it has to is that three sources on these machines are genuinely
// two-channel and were having it thrown away at the last step: the SAM's
// SAA1099, the Next's SounDrive DAC bank (soundrive.vhd sums chA+chB into the
// left output and chC+chD into the right), and the Next's three AYs, which
// pan per NR$08 bit 5 and NR$09 bits 7:5. The FPGA keeps them apart the whole
// way through a dedicated mixer entity (audio_mixer.vhd), whose ports are
// pcm_L_o and pcm_R_o.
//
// The beeper is the exception and stays mono on purpose: it is one bit driving
// one speaker, so PushBeeperSamples widens it to both channels rather than
// pretending it has a stereo image.

// A stereo push must keep the two channels apart all the way through the ring
// buffer. Before the queue was widened it held one value per sample, so a
// stereo producer had to average its channels before pushing and the
// separation was gone before the buffer ever saw it.
func TestAStereoPushKeepsTheChannelsApart(t *testing.T) {
	as := fakeSystem()

	// Two frames: left rising, right falling, so a swap or an average is
	// visible in the result rather than symmetric.
	as.PushStereoSamples([]int16{100, -100, 200, -200})

	out := make([]int16, 4)
	as.popStereoSamples(out)

	want := []int16{100, -100, 200, -200}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("drained %v, want %v: the channels did not survive the queue", out, want)
		}
	}
}

// The beeper is one bit driving one speaker, so it must arrive equally on both
// channels. This is the property that lets every existing mono producer keep
// pushing exactly as it did.
func TestABeeperPushArrivesOnBothChannels(t *testing.T) {
	as := fakeSystem()

	as.PushBeeperSamples([]int16{1000, -2000})

	out := make([]int16, 4)
	as.popStereoSamples(out)

	if out[0] != 1000 || out[1] != 1000 {
		t.Errorf("frame 0 = (%d, %d), want (1000, 1000)", out[0], out[1])
	}
	if out[2] != -2000 || out[3] != -2000 {
		t.Errorf("frame 1 = (%d, %d), want (-2000, -2000)", out[2], out[3])
	}
}

// An underrun has to hold and decay each channel separately. Holding a single
// value for both would collapse the image to mono for the duration of every
// buffer starve, which is exactly when the artefact is least welcome.
func TestAnUnderrunDecaysEachChannelSeparately(t *testing.T) {
	as := fakeSystem()
	as.PushStereoSamples([]int16{8000, -8000})

	out := make([]int16, 8)
	as.popStereoSamples(out)

	if out[0] != 8000 || out[1] != -8000 {
		t.Fatalf("real frame = (%d, %d), want (8000, -8000)", out[0], out[1])
	}
	// The first starved frame continues from the last real one, per channel.
	if out[2] != 8000 || out[3] != -8000 {
		t.Errorf("first starved frame = (%d, %d), want (8000, -8000): the hold is not per channel",
			out[2], out[3])
	}
	// Then each channel decays toward silence from its own side.
	if out[4] >= out[2] {
		t.Errorf("left did not decay: %d then %d", out[2], out[4])
	}
	if out[5] <= out[3] {
		t.Errorf("right did not decay toward zero from below: %d then %d", out[3], out[5])
	}
}

// Overflow drops whole frames. Dropping a single slot would rotate every later
// frame by one channel, which turns a dropout into a permanent left/right swap
// rather than a glitch.
func TestOverflowDropsWholeFramesNotSingleChannels(t *testing.T) {
	as := fakeSystem()

	// Push two ring buffers' worth of frames whose left channel is always
	// even and right always odd, so a half-frame slip is unmistakable.
	in := make([]int16, queueCapacity*2)
	for i := 0; i < len(in); i += 2 {
		in[i] = 2000
		in[i+1] = 2001
	}
	as.PushStereoSamples(in)

	out := make([]int16, queueCapacity)
	as.popStereoSamples(out)

	for i := 0; i < len(out); i += 2 {
		if out[i] != 2000 || out[i+1] != 2001 {
			t.Fatalf("frame %d = (%d, %d), want (2000, 2001): overflow slipped a half frame",
				i/2, out[i], out[i+1])
		}
	}
}
