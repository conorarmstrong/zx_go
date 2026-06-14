package sdcard

import "testing"

// the development log/D31du: the dense-trace diff vs the reference showed its SD
// model emits TWO $FF filler bytes (Ncr) before R1 where ours
// emitted one — the only persistent hardware divergence across the
// whole boot+staging. Real-card spec allows Ncr=1-8; matching the
// reference eliminates poll-phase skew in lockstep
// comparisons. CMD17's R1 must be preceded by exactly two $FF.
func TestR1NcrIsTwoFillerBytes(t *testing.T) {
	src, _ := NewImageSource(make([]byte, 4096), false)
	c := NewCard(src)
	c.WriteCS(0x00) // selected (active low)
	// complete init handshake quickly: CMD0
	for _, b := range []byte{0x40, 0, 0, 0, 0, 0x95} {
		c.WriteData(b)
	}
	// drain until R1 ($01) appears, counting fillers
	fillers := 0
	for i := 0; i < 10; i++ {
		v := c.ReadData()
		if v == 0xFF {
			fillers++
			continue
		}
		if v != 0x01 {
			t.Fatalf("expected R1=$01 after fillers, got $%02X", v)
		}
		break
	}
	if fillers != 2 {
		t.Errorf("Ncr fillers before R1 = %d, want 2 (reference-matched)", fillers)
	}
}

// The $FE data token must be preceded by one $FF Nac (access-time)
// filler after R1 — matches the reference's model (dense-trace diff: its
// $1F4B token-wait loops once more than ours did). Verify via CMD9
// (SEND_CSD): R1 path = FF FF 00, then FF (Nac), then FE token.
func TestDataTokenNacFiller(t *testing.T) {
	src, _ := NewImageSource(make([]byte, 4096), false)
	c := NewCard(src)
	c.WriteCS(0x00)
	for _, b := range []byte{0x40 | 9, 0, 0, 0, 0, 0xFF} {
		c.WriteData(b)
	}
	seq := make([]byte, 5)
	for i := range seq {
		seq[i] = c.ReadData()
	}
	want := []byte{0xFF, 0xFF, 0x00, 0xFF, 0xFE} // Ncr×2, R1, Nac, token
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("byte %d = $%02X, want $%02X (seq % X)", i, seq[i], want[i], seq)
		}
	}
}
