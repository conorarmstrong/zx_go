package dac

import "testing"

func TestBankStartsSilent(t *testing.T) {
	b := New()
	for c := ChannelA; c <= ChannelD; c++ {
		if b.Level(c) != 0 {
			t.Errorf("channel %d default = %d, want 0", c, b.Level(c))
		}
	}
}

func TestWritePortRoutesPerChannel(t *testing.T) {
	cases := []struct {
		name string
		port uint16
		want Channel
	}{
		{"A 0x1F", 0x1F, ChannelA},
		{"A 0xF1", 0xF1, ChannelA},
		{"B 0x0F", 0x0F, ChannelB},
		{"B 0xF3", 0xF3, ChannelB},
		{"C 0x4F", 0x4F, ChannelC},
		{"C 0xF9", 0xF9, ChannelC},
		{"D 0xFB", 0xFB, ChannelD},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := New()
			if !b.WritePort(c.port, 0xAA) {
				t.Errorf("WritePort(%#x) returned false, want true (port is DAC)", c.port)
			}
			if got := b.Level(c.want); got != 0xAA {
				t.Errorf("after write to %#x: Level(%d) = %#x, want 0xAA", c.port, c.want, got)
			}
			// Other channels unchanged.
			for ch := ChannelA; ch <= ChannelD; ch++ {
				if ch == c.want {
					continue
				}
				if got := b.Level(ch); got != 0 {
					t.Errorf("channel %d disturbed by write to %#x: %#x", ch, c.port, got)
				}
			}
		})
	}
}

func TestWritePortRejectsUnknownPort(t *testing.T) {
	b := New()
	if b.WritePort(0x42, 0xFF) {
		t.Errorf("WritePort(0x42) returned true, want false (not a DAC port)")
	}
}

func TestHighBitsOfPortIgnored(t *testing.T) {
	// Real hardware decodes DAC ports on the low byte only.
	b := New()
	b.WritePort(0xABCD&0xFF00|0x1F, 0x55) // 0xAB1F should hit channel A
	if b.Level(ChannelA) != 0x55 {
		t.Errorf("port with high bits set should still hit channel A: Level=%#x", b.Level(ChannelA))
	}
}

func TestMixedLevel(t *testing.T) {
	b := New()
	b.WritePort(0x0F, 100)
	b.WritePort(0x1F, 100)
	b.WritePort(0xF9, 100)
	b.WritePort(0xFB, 100)
	// All four at 100 -> mean 100.
	if got := b.MixedLevel(); got != 100 {
		t.Errorf("MixedLevel with all 100s = %d, want 100", got)
	}

	b.Reset()
	if got := b.MixedLevel(); got != 0 {
		t.Errorf("MixedLevel after Reset = %d, want 0", got)
	}

	// Max-out: 255 across all four => mean 255 still.
	b.WritePort(0x0F, 255)
	b.WritePort(0x1F, 255)
	b.WritePort(0xF9, 255)
	b.WritePort(0xFB, 255)
	if got := b.MixedLevel(); got != 255 {
		t.Errorf("MixedLevel saturated = %d, want 255", got)
	}
}

// TestMixIntoSilenceAtCenter verifies that level 0x80 (the midpoint)
// adds nothing to the buffer — important because a DC offset would
// produce constant speaker pressure and audible thump.
func TestMixIntoSilenceAtCenter(t *testing.T) {
	b := New()
	b.WritePort(0x0F, 0x80)
	b.WritePort(0x1F, 0x80)
	b.WritePort(0xF9, 0x80)
	b.WritePort(0xFB, 0x80)
	buf := []int16{100, 200, 300, -400}
	want := []int16{100, 200, 300, -400}
	b.MixInto(buf)
	for i := range buf {
		if buf[i] != want[i] {
			t.Errorf("MixInto at level 0x80: buf[%d] = %d, want %d (no DC offset)", i, buf[i], want[i])
		}
	}
}

// TestMixIntoPositiveContribution pins the contribution magnitude:
// all four channels at 0xFF → MixedLevel 0xFF → centred value
// (0xFF - 0x80) = 127 → contribution 127 * 64 = 8128 added per sample.
func TestMixIntoPositiveContribution(t *testing.T) {
	b := New()
	b.WritePort(0x0F, 0xFF)
	b.WritePort(0x1F, 0xFF)
	b.WritePort(0xF9, 0xFF)
	b.WritePort(0xFB, 0xFF)
	buf := []int16{0, 0, 0}
	b.MixInto(buf)
	const wantContrib int16 = 8128
	for i, v := range buf {
		if v != wantContrib {
			t.Errorf("MixInto at level 0xFF: buf[%d] = %d, want %d", i, v, wantContrib)
		}
	}
}

// TestMixIntoNegativeContribution pins the negative side: all
// channels at 0x00 → centred value (0x00 - 0x80) = -128 →
// contribution -128 * 64 = -8192.
func TestMixIntoNegativeContribution(t *testing.T) {
	b := New()
	buf := []int16{0, 0}
	b.MixInto(buf)
	const wantContrib int16 = -8192
	for i, v := range buf {
		if v != wantContrib {
			t.Errorf("MixInto at level 0x00: buf[%d] = %d, want %d", i, v, wantContrib)
		}
	}
}

// TestMixIntoAccumulates verifies MixInto adds to existing samples
// rather than overwriting them — DAC contribution layers on top of
// beeper + AY.
func TestMixIntoAccumulates(t *testing.T) {
	b := New()
	b.WritePort(0x0F, 0xC0) // raises mean toward positive
	b.WritePort(0x1F, 0xC0)
	b.WritePort(0xF9, 0xC0)
	b.WritePort(0xFB, 0xC0)
	// MixedLevel = 0xC0 = 192. Centred = 192 - 128 = 64. Contrib = 64 * 64 = 4096.
	buf := []int16{1000, -500}
	b.MixInto(buf)
	if buf[0] != 1000+4096 || buf[1] != -500+4096 {
		t.Errorf("MixInto did not accumulate: got %v, want [%d %d]", buf, 1000+4096, -500+4096)
	}
}

// TestMixIntoSaturatesAtPositiveLimit verifies the sum saturates
// rather than wrapping when DAC contribution would push a positive
// sample past int16 max. Without saturation, 30000 + 8128 = 38128
// would wrap to -27408 — an audible high-amplitude pop.
func TestMixIntoSaturatesAtPositiveLimit(t *testing.T) {
	b := New()
	b.WritePort(0x0F, 0xFF) // max positive contribution: +8128
	b.WritePort(0x1F, 0xFF)
	b.WritePort(0xF9, 0xFF)
	b.WritePort(0xFB, 0xFF)
	buf := []int16{30000, 32767, 20000}
	b.MixInto(buf)
	want := []int16{32767, 32767, 28128}
	for i := range buf {
		if buf[i] != want[i] {
			t.Errorf("buf[%d] = %d, want %d (saturating add at positive)", i, buf[i], want[i])
		}
	}
}

// TestMixIntoSaturatesAtNegativeLimit verifies the same for the
// negative side: DAC at 0x00 contributes -8192, which would wrap
// below int16 min when summed with already-low samples.
func TestMixIntoSaturatesAtNegativeLimit(t *testing.T) {
	b := New()
	// All channels default to 0 → contribution -8192.
	buf := []int16{-30000, -32768, -20000}
	b.MixInto(buf)
	want := []int16{-32768, -32768, -28192}
	for i := range buf {
		if buf[i] != want[i] {
			t.Errorf("buf[%d] = %d, want %d (saturating add at negative)", i, buf[i], want[i])
		}
	}
}
