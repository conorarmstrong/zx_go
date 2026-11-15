package ctc

import (
	"bytes"
	"encoding/gob"
	"testing"
)

// A restored bank must continue exactly as the original does.
func TestBankStateRoundTripContinuesIdentically(t *testing.T) {
	a := NewBank()
	// Program channel 0 as a timer with interrupt, prescaler 16, TC 5,
	// channel 1 as a counter chained from it, and leave channel 2 mid control word.
	a.WritePort(0x183B, 0x95)
	a.WritePort(0x183B, 0x05)
	a.WritePort(0x193B, 0x95)
	a.WritePort(0x193B, 0x03)
	a.WritePort(0x1A3B, 0x07)
	for i := 0; i < 37; i++ {
		a.TickChained()
	}
	a.Channel(3).SetTrigger(true)

	b := NewBank()
	if err := b.LoadState(a.SaveState()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.SaveState(), b.SaveState()) {
		t.Fatal("restored bank does not re-capture identically")
	}
	for i := 0; i < 5000; i++ {
		za, zb := a.TickChained(), b.TickChained()
		if za != zb {
			t.Fatalf("tick %d: zc %v vs %v", i, za, zb)
		}
		for ch := 0; ch < NumChannels; ch++ {
			if a.Channel(ch).Count() != b.Channel(ch).Count() {
				t.Fatalf("tick %d ch %d: count %d vs %d", i, ch, a.Channel(ch).Count(), b.Channel(ch).Count())
			}
		}
	}
	if !bytes.Equal(a.SaveState(), b.SaveState()) {
		t.Fatal("banks diverged")
	}
}

func TestBankLoadStateRejectsBadInput(t *testing.T) {
	b := NewBank()
	if err := b.LoadState([]byte{1, 2, 3}); err == nil {
		t.Fatal("garbage accepted")
	}
	s := bankState{}
	s.Ch[2].State = 99
	var buf bytes.Buffer
	_ = encodeForTest(&buf, s)
	if err := b.LoadState(buf.Bytes()); err == nil {
		t.Fatal("out of range channel state accepted")
	}
}

func encodeForTest(buf *bytes.Buffer, s bankState) error {
	return gob.NewEncoder(buf).Encode(s)
}
