package ay

import "testing"

// AY stereo panning, from the FPGA rather than from folklore.
//
// turbosound.vhd builds each chip's pair out of three muxes, and the whole
// panning law is in these six lines (shown for PSG 0; PSGs 1 and 2 repeat them
// verbatim at :241 and :296):
//
//	psg0_L_mux <= psg0_C when stereo_mode_i = '1' or mono_mode_i(0) = '1' else psg0_B;
//	psg0_L_sum <= ('0' & psg0_L_mux) + ('0' & psg0_A);
//	psg0_R_mux <= psg0_L_sum when mono_mode_i(0) = '1' else ('0' & psg0_C);
//	psg0_R_sum <= ('0' & psg0_R_mux) + ("00" & psg0_B);
//	psg0_L_fin <= psg0_R_sum when mono_mode_i(0) = '1' else ('0' & psg0_L_sum);
//	                                       turbosound.vhd:186-192
//
// Resolving the muxes gives three cases, and the naming only makes sense once
// you do: the middle letter is the channel that ends up in BOTH outputs.
//
//	mono: L = R = A + B + C
//	ABC:  L = A + B,  R = B + C     (B is the centre)
//	ACB:  L = A + C,  R = B + C     (C is the centre)
//
// The mode comes from NR$08 bit 5 for every chip at once (zxnext.vhd:5177),
// and NR$09 bits 7:5 force individual chips back to mono (:5186), one bit per
// PSG with bit 5 belonging to PSG 0.
//
// A plain 128K is NOT any of the stereo cases. Its AY-3-8912 drives one
// internal speaker through a single summed output, so mono is the default and
// the classic machines never leave it.

// steadyLevels programmes the three channels to constant DC levels, so the
// panning arithmetic can be checked exactly rather than statistically.
// Disabling tone AND noise for a channel holds its output high (ay.go:530),
// which leaves the volume register alone deciding the level.
func steadyLevels(t *testing.T, a *AY, volA, volB, volC byte) (int32, int32, int32) {
	t.Helper()
	a.WriteRegister(RegMixer, 0x3F) // all tone and noise disabled
	a.WriteRegister(RegVolumeA, volA)
	a.WriteRegister(RegVolumeB, volB)
	a.WriteRegister(RegVolumeC, volC)
	return int32(a.channelLevel(0)), int32(a.channelLevel(1)), int32(a.channelLevel(2))
}

// mixOnce runs one stereo sample through MixIntoStereo on a zeroed buffer.
func mixOnce(a *AY) (int16, int16) {
	buf := make([]int16, 2)
	a.MixIntoStereo(buf)
	return buf[0], buf[1]
}

// The three cases of the panning law, each checked against the arithmetic the
// VHDL performs. The fixture puts a different level on every channel so that
// swapping any two of them, or dropping one, changes the result.
func TestThePanningLawMatchesTheFPGA(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode StereoMode
		// want returns the expected (L, R) from the three channel levels.
		want func(chA, chB, chC int32) (int32, int32)
	}{
		{
			name: "mono sums all three into both channels",
			mode: StereoMono,
			want: func(a, b, c int32) (int32, int32) { return a + b + c, a + b + c },
		},
		{
			name: "ABC puts B in the centre",
			mode: StereoABC,
			want: func(a, b, c int32) (int32, int32) { return a + b, b + c },
		},
		{
			name: "ACB puts C in the centre",
			mode: StereoACB,
			want: func(a, b, c int32) (int32, int32) { return a + c, b + c },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			a.SetStereoMode(tc.mode)
			// Three distinct volumes, so no two channels are interchangeable.
			chA, chB, chC := steadyLevels(t, a, 15, 10, 5)

			gotL, gotR := mixOnce(a)
			wantL, wantR := tc.want(chA, chB, chC)
			if int32(gotL) != wantL/mixHeadroom {
				t.Errorf("L = %d, want %d", gotL, wantL/mixHeadroom)
			}
			if int32(gotR) != wantR/mixHeadroom {
				t.Errorf("R = %d, want %d", gotR, wantR/mixHeadroom)
			}
		})
	}
}

// Which channel is the centre is the whole difference between the two stereo
// modes, and it is the thing a reversed implementation would get wrong while
// still sounding like stereo. Sounding each channel alone answers it directly:
// the centre channel is the one that reaches both outputs, and the other two
// must each reach exactly one.
//
// The first version of this test asserted that ACB pans C hard right, which
// contradicts the law two paragraphs above it — in ACB, C is the centre. The
// implementation caught it. Read the resolved law, not the letters.
func TestOnlyTheCentreChannelReachesBothOutputs(t *testing.T) {
	const (
		left = iota
		centre
		right
	)
	placement := map[StereoMode][3]int{
		// channel:      A       B       C
		StereoABC: {left, centre, right},
		StereoACB: {left, right, centre},
	}
	names := [3]string{"A", "B", "C"}

	for mode, want := range placement {
		for ch := 0; ch < 3; ch++ {
			t.Run(mode.String()+"/channel "+names[ch], func(t *testing.T) {
				a := New()
				a.SetStereoMode(mode)
				var vols [3]byte
				vols[ch] = 15 // this channel alone
				steadyLevels(t, a, vols[0], vols[1], vols[2])

				l, r := mixOnce(a)
				switch want[ch] {
				case left:
					if l == 0 || r != 0 {
						t.Errorf("got (%d, %d), want left only: %s is panned hard left in %s",
							l, r, names[ch], mode)
					}
				case right:
					if r == 0 || l != 0 {
						t.Errorf("got (%d, %d), want right only: %s is panned hard right in %s",
							l, r, names[ch], mode)
					}
				case centre:
					if l == 0 || l != r {
						t.Errorf("got (%d, %d), want equal and non-zero: %s is the centre channel in %s",
							l, r, names[ch], mode)
					}
				}
			})
		}
	}
}

// A freshly-constructed chip is mono. Every classic machine that has an AY
// fitted wires it to one speaker, so anything else would be inventing a stereo
// image the hardware does not have.
func TestAFreshChipIsMono(t *testing.T) {
	a := New()
	if got := a.StereoModeSetting(); got != StereoMono {
		t.Errorf("new chip stereo mode = %v, want StereoMono", got)
	}
	steadyLevels(t, a, 15, 0, 0) // hard left in either stereo mode
	l, r := mixOnce(a)
	if l != r {
		t.Errorf("a mono chip produced (%d, %d): the channels must be identical", l, r)
	}
}

// MixIntoStereo adds to what is already in the buffer, exactly as the mono
// MixInto does, because the mixer sums every source into one buffer.
func TestMixIntoStereoAddsRatherThanReplaces(t *testing.T) {
	a := New()
	a.SetStereoMode(StereoABC)
	steadyLevels(t, a, 15, 15, 15)

	buf := []int16{100, 200}
	a.MixIntoStereo(buf)
	if buf[0] <= 100 || buf[1] <= 200 {
		t.Errorf("buffer = %v, want both slots raised above the 100/200 they started at", buf)
	}
}

// The mono MixInto keeps summing all three channels whatever the panning mode
// is set to. It is the "give me one channel" API, and a caller asking for mono
// must not silently receive one side of a stereo image.
func TestTheMonoMixIsUnaffectedByThePanningMode(t *testing.T) {
	quiet := New()
	steadyLevels(t, quiet, 15, 0, 0)
	monoBuf := make([]int16, 1)
	quiet.MixInto(monoBuf)

	panned := New()
	panned.SetStereoMode(StereoABC)
	steadyLevels(t, panned, 15, 0, 0)
	pannedBuf := make([]int16, 1)
	panned.MixInto(pannedBuf)

	if monoBuf[0] != pannedBuf[0] {
		t.Errorf("MixInto gave %d with panning set and %d without: the mono API must not pan",
			pannedBuf[0], monoBuf[0])
	}
}
