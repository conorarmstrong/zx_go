package ay

import "testing"

// nonSilent reports whether any sample in buf is non-zero.
func nonSilent(buf []int16) bool {
	for _, s := range buf {
		if s != 0 {
			return true
		}
	}
	return false
}

// The engine mixes its chips' output into the buffer — this is what makes
// 128K/AY music audible on the Next (the audio mixer pulls from the engine).
// A tone programmed on chip 0 must produce non-silent output through the engine.
func TestEngineMixIntoSoundsChip0(t *testing.T) {
	e := NewEngine()
	ch := e.Active() // chip 0
	// Programme a steady tone on channel A at full volume.
	ch.WriteRegister(0, 0x00) // tone A fine
	ch.WriteRegister(1, 0x02) // tone A coarse → audible pitch
	ch.WriteRegister(7, 0x3E) // mixer: enable tone A (bit0=0), disable noise
	ch.WriteRegister(8, 0x0F) // channel A volume = max

	buf := make([]int16, 1024)
	e.MixInto(buf)
	if !nonSilent(buf) {
		t.Error("engine produced silence for a chip-0 tone — AY music would be inaudible")
	}
}

// NextReg 0x06 bit 2 (Disabled) silences the engine entirely.
func TestEngineMixIntoDisabledIsSilent(t *testing.T) {
	e := NewEngine()
	ch := e.Active()
	ch.WriteRegister(1, 0x02)
	ch.WriteRegister(7, 0x3E)
	ch.WriteRegister(8, 0x0F)

	e.Select(0x03) // bits 1-0 = 11 → hold all AY in reset (disabled)
	buf := make([]int16, 1024)
	e.MixInto(buf)
	if nonSilent(buf) {
		t.Error("disabled engine should mix nothing")
	}
}
