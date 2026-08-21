package dac

import "testing"

// The FPGA's port_dac_A..D decode (zxnext.vhd:2658-2664) routes $5F to
// channel D (SounDrive mode 1 and stereo A/D) and $DF to channels A and
// D together (the SpecDrum mono pair). Neither reached the bank, so a
// program panning a DAC hard right left the right-hand pair sitting at
// mid-scale and silent.
func TestWritePortDecodesChannelD(t *testing.T) {
	b := New()
	if !b.WritePort(0x005F, 0x20) {
		t.Fatal("port $5F was not claimed as a DAC port")
	}
	if got := b.Level(ChannelD); got != 0x20 {
		t.Errorf("channel D = %#02x, want %#02x", got, 0x20)
	}
}

func TestWritePortDecodesTheMonoADPair(t *testing.T) {
	b := New()
	if !b.WritePort(0x00DF, 0x30) {
		t.Fatal("port $DF was not claimed as a DAC port")
	}
	if got := b.Level(ChannelA); got != 0x30 {
		t.Errorf("channel A = %#02x, want %#02x", got, 0x30)
	}
	if got := b.Level(ChannelD); got != 0x30 {
		t.Errorf("channel D = %#02x, want %#02x", got, 0x30)
	}
	// $DF is the mono A+D pair, so B and C stay at rest.
	if got := b.Level(ChannelB); got != restLevel {
		t.Errorf("channel B moved to %#02x", got)
	}
	if got := b.Level(ChannelC); got != restLevel {
		t.Errorf("channel C moved to %#02x", got)
	}
}

// The existing decode must not regress.
func TestWritePortKeepsTheDocumentedMap(t *testing.T) {
	for _, tc := range []struct {
		port uint16
		ch   Channel
	}{
		{0x001F, ChannelA}, {0x00F1, ChannelA},
		{0x000F, ChannelB}, {0x00F3, ChannelB},
		{0x004F, ChannelC}, {0x00F9, ChannelC},
		{0x00FB, ChannelD},
	} {
		b := New()
		if !b.WritePort(tc.port, 0x40) {
			t.Fatalf("port %#04x was not claimed", tc.port)
		}
		if got := b.Level(tc.ch); got != 0x40 {
			t.Errorf("port %#04x: channel %d = %#02x, want %#02x", tc.port, tc.ch, got, 0x40)
		}
	}
}

// A port with no DAC decode must fall through so the rest of the ULA
// dispatch still sees it.
func TestWritePortIgnoresNonDACPorts(t *testing.T) {
	b := New()
	if b.WritePort(0x00FE, 0x40) {
		t.Error("port $FE was claimed as a DAC port")
	}
}
