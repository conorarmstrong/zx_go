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

// The FPGA's port to channel table (zxnext.vhd:2652-2655) is:
//
//	A  -   FB  DF  1F  F1  -  3F
//	B  B3  -   -   0F  F3  0F -
//	C  B3  -   -   4F  F9  4F -
//	D  -   FB  DF  5F  FB  -  5F
//
// $FB and $DF are both mono A+D ports: port_dac_A includes
// port_dac_mono_AD, which either of them asserts. Decoding $FB as
// channel D alone left a Covox or mono program's output hard-panned
// right, which is the same fault the $DF case was fixed for.
func TestWritePortDecodesEveryFPGAPortToChannelPair(t *testing.T) {
	for _, tc := range []struct {
		name string
		port uint16
		chs  []Channel
	}{
		{"$1F soundrive 1 A", 0x001F, []Channel{ChannelA}},
		{"$F1 soundrive 2 A", 0x00F1, []Channel{ChannelA}},
		{"$3F stereo A/D A", 0x003F, []Channel{ChannelA}},
		{"$0F B", 0x000F, []Channel{ChannelB}},
		{"$F3 B", 0x00F3, []Channel{ChannelB}},
		{"$4F C", 0x004F, []Channel{ChannelC}},
		{"$F9 C", 0x00F9, []Channel{ChannelC}},
		{"$5F D", 0x005F, []Channel{ChannelD}},
		{"$FB mono A+D", 0x00FB, []Channel{ChannelA, ChannelD}},
		{"$DF mono A+D", 0x00DF, []Channel{ChannelA, ChannelD}},
		{"$B3 mono B+C", 0x00B3, []Channel{ChannelB, ChannelC}},
	} {
		b := New()
		if !b.WritePort(tc.port, 0x42) {
			t.Errorf("%s: port not claimed", tc.name)
			continue
		}
		want := map[Channel]bool{}
		for _, c := range tc.chs {
			want[c] = true
		}
		for _, c := range []Channel{ChannelA, ChannelB, ChannelC, ChannelD} {
			got := b.Level(c)
			if want[c] && got != 0x42 {
				t.Errorf("%s: channel %d = %#02x, want 0x42", tc.name, c, got)
			}
			if !want[c] && got != restLevel {
				t.Errorf("%s: channel %d moved to %#02x, want rest", tc.name, c, got)
			}
		}
	}
}
