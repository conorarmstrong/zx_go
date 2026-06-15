package ula

import "testing"

// The tape-loading sound reconstructs the EAR signal as an audible square wave:
// a toggling (pilot-like) signal yields a varying waveform bounded by the tape
// amplitude, while a non-toggling signal is a flat (inaudible) DC level.
func TestTapeSoundWaveform(t *testing.T) {
	// Pilot-tone-like toggling every 2168 T-states across the frame.
	var events []audioEvent
	state := false
	for off := 0; off < 69888; off += 2168 {
		state = !state
		events = append(events, audioEvent{tstateOffset: off, state: state})
	}
	samples, _ := generateSquareWaveFrame(events, false, -tapeAudioAmplitude, tapeAudioAmplitude)

	var lo, hi int16 = 32767, -32768
	for _, s := range samples {
		if s < lo {
			lo = s
		}
		if s > hi {
			hi = s
		}
		if s > tapeAudioAmplitude || s < -tapeAudioAmplitude {
			t.Fatalf("sample %d outside tape amplitude ±%d", s, tapeAudioAmplitude)
		}
	}
	if hi-lo < tapeAudioAmplitude {
		t.Errorf("toggling tape signal too flat to be audible: range %d..%d", lo, hi)
	}

	// No transitions → flat DC at the low level (inaudible), not noise.
	silent, _ := generateSquareWaveFrame(nil, false, -tapeAudioAmplitude, tapeAudioAmplitude)
	for i, s := range silent {
		if s != -tapeAudioAmplitude {
			t.Fatalf("silent tape sample %d = %d, want flat %d", i, s, -tapeAudioAmplitude)
		}
	}
}

// The beeper generator still produces its full-amplitude output after the
// refactor that extracted generateSquareWaveFrame.
func TestBeeperFrameUnchangedByRefactor(t *testing.T) {
	events := []audioEvent{{tstateOffset: 0, state: true}, {tstateOffset: 34944, state: false}}
	samples, final := generateBeeperFrame(events, false)
	if len(samples) == 0 {
		t.Fatal("no beeper samples")
	}
	if final != false {
		t.Errorf("final beeper state = %v, want false", final)
	}
	// First half (state high) should sit near beeperHigh.
	if samples[10] < beeperHigh/2 {
		t.Errorf("beeper high-half sample = %d, want near %d", samples[10], beeperHigh)
	}
}
