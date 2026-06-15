package dac

import "testing"

// GenerateFrame reconstructs the DAC waveform sample-accurately from timed
// writes, with the same level→amplitude mapping as the legacy MixInto.
func TestBankGenerateFrameEventTimed(t *testing.T) {
	b := New()
	// All four channels to full at t=0 (mixed level 0xFF), then silent at the
	// half-frame point.
	for _, p := range []uint16{0x0F, 0x1F, 0xF9, 0xFB} {
		b.WritePort(p, 0xFF)
	}
	b.Record(0) // mixed = 255
	for _, p := range []uint16{0x0F, 0x1F, 0xF9, 0xFB} {
		b.WritePort(p, 0x00)
	}
	b.Record(4) // mixed = 0

	const samples, tstates = 4, 8
	got := b.GenerateFrame(samples, tstates)
	hi := (int16(255) - 128) * dacMixAmplitude
	lo := (int16(0) - 128) * dacMixAmplitude
	if got[0] != hi {
		t.Errorf("sample 0 = %d, want %d (mixed 255)", got[0], hi)
	}
	if got[samples-1] != lo {
		t.Errorf("last sample = %d, want %d (mixed 0)", got[samples-1], lo)
	}
	// Next frame, no events: the held level (0) carries over.
	if g := b.GenerateFrame(samples, tstates); g[0] != lo {
		t.Errorf("carried sample 0 = %d, want %d", g[0], lo)
	}
}
