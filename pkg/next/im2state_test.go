package next

import (
	"bytes"
	"testing"
)

// Drive a driver into every interesting condition, capture, restore into a
// fresh one, and prove both behave identically afterwards.
func TestIM2DriverStateRoundTrip(t *testing.T) {
	a := NewIM2Driver()
	a.SetVectorBase(0xA0)
	a.SetMode(true)
	for _, s := range []int{IM2SourceULA, IM2SourceCTC0, IM2SourceUART0T} {
		a.SetEnabled(s, true)
	}
	a.Raise(IM2SourceCTC0)
	a.Lower(IM2SourceCTC0)
	a.Raise(IM2SourceUART0T) // level stays high
	if _, ok := a.Acknowledge(); !ok {
		t.Fatal("no ack")
	}
	a.Raise(IM2SourceULA)
	a.Lower(IM2SourceULA)

	b := NewIM2Driver()
	if err := b.LoadState(a.SaveState()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.SaveState(), b.SaveState()) {
		t.Fatal("restored driver does not re-capture identically")
	}
	step := func(name string, f func(*IM2Driver)) {
		f(a)
		f(b)
		if a.INTAsserted() != b.INTAsserted() || a.StatusBits() != b.StatusBits() {
			t.Fatalf("%s: int %v/%v status %x/%x", name, a.INTAsserted(), b.INTAsserted(), a.StatusBits(), b.StatusBits())
		}
		if !bytes.Equal(a.SaveState(), b.SaveState()) {
			t.Fatalf("%s: states differ", name)
		}
	}
	step("reti", func(d *IM2Driver) { d.RETISeen() })
	for i := 0; i < 4; i++ {
		var va, vb byte
		var oa, ob bool
		va, oa = a.Acknowledge()
		vb, ob = b.Acknowledge()
		if va != vb || oa != ob {
			t.Fatalf("ack %d: %x/%v vs %x/%v", i, va, oa, vb, ob)
		}
		step("reti", func(d *IM2Driver) { d.RETISeen() })
	}
	step("clear", func(d *IM2Driver) { d.ClearStatus(IM2SourceUART0T) })
}

func TestIM2DriverStateRejectsBadInput(t *testing.T) {
	d := NewIM2Driver()
	if err := d.LoadState([]byte{9}); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestIM2LoadStateRejectsOutOfRangeVector(t *testing.T) {
	a := NewIM2Driver()
	a.chain.vector = 16
	if err := NewIM2Driver().LoadState(a.SaveState()); err == nil {
		t.Fatal("vector 16 accepted")
	}
}

func TestIM2LoadStateDoesNotFireTheListener(t *testing.T) {
	a := NewIM2Driver()
	a.SetMode(true)
	a.SetEnabled(IM2SourceULA, true)
	a.Raise(IM2SourceULA)
	a.Lower(IM2SourceULA)
	b := NewIM2Driver()
	fired := false
	b.SetINTListener(func(bool) { fired = true })
	fired = false // installing a listener notifies once
	if err := b.LoadState(a.SaveState()); err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Fatal("listener fired on restore")
	}
}
