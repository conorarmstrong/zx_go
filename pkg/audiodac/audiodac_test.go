package audiodac

import "testing"

// Ports are only claimed when the matching device is enabled (so a disabled
// Covox doesn't steal the ZX Printer's port $FB, and SpecDrum/$DF is inert
// until plugged in).
func TestHandlesGating(t *testing.T) {
	d := New()
	if d.Handles(SpecDrumPort) || d.Handles(CovoxPort) {
		t.Fatal("nothing should be claimed before any device is enabled")
	}
	d.SetSpecDrum(true)
	if !d.Handles(SpecDrumPort) {
		t.Error("SpecDrum enabled: $DF should be claimed")
	}
	if d.Handles(CovoxPort) {
		t.Error("Covox disabled: $FB must NOT be claimed (ZX Printer needs it)")
	}
	d.SetCovox(true)
	if !d.Handles(CovoxPort) {
		t.Error("Covox enabled: $FB should be claimed")
	}
}

// GenerateFrame reconstructs the DAC waveform sample-accurately from the timed
// writes (box-filter, like the beeper), and carries the level across frames.
func TestGenerateFrameEventTimed(t *testing.T) {
	d := New()
	d.SetSpecDrum(true)

	// Full-scale high from t=0, then drop to zero at the half-frame point.
	d.Record(0, 0xFF)
	d.Record(4, 0x00)

	const samples, tstates = 4, 8
	got := d.GenerateFrame(samples, tstates)
	if len(got) != samples {
		t.Fatalf("len=%d, want %d", len(got), samples)
	}
	hi := int16((0xFF - 128) * amplitude) // +12192
	lo := int16((0x00 - 128) * amplitude) // -12288
	if got[0] != hi {
		t.Errorf("sample 0 = %d, want %d (level 0xFF)", got[0], hi)
	}
	if got[samples-1] != lo {
		t.Errorf("last sample = %d, want %d (level 0x00)", got[samples-1], lo)
	}

	// Next frame with no new events: the level held at 0x00 carries over.
	got2 := d.GenerateFrame(samples, tstates)
	if got2[0] != lo {
		t.Errorf("carried frame sample 0 = %d, want %d", got2[0], lo)
	}
}

// TestGenerateFrameCarriesEventAtBoundary verifies a write recorded exactly at
// (or past) the end of the frame's T-state span still lands: its level must
// carry into the next frame rather than being silently dropped because it
// fell outside every sample's [start, end) window this frame.
func TestGenerateFrameCarriesEventAtBoundary(t *testing.T) {
	d := New()
	d.SetSpecDrum(true)

	const samples, tstates = 4, 8
	d.Record(0, 0x40)
	d.Record(tstates, 0xC0) // exactly at the frame boundary

	d.GenerateFrame(samples, tstates)
	got := d.GenerateFrame(samples, tstates)

	want := (int16(0xC0) - 128) * amplitude
	if got[0] != want {
		t.Errorf("level written at the frame boundary was lost: next-frame sample 0 = %d, want %d", got[0], want)
	}
}
