package uart

import (
	"fmt"
	"reflect"
	"testing"
)

// A capture taken mid-traffic, restored into a fresh Block, must be the same
// machine: every field equal (reflect.DeepEqual sees the unexported ones, so a
// field the capture forgot shows up here), and the two must then run on
// identically. The traffic is chosen to leave something in every part: bytes
// in flight both ways, FIFOs part full, an error latched, a stale cell, a
// non-default prescaler and frame.
// firstDiff names the first field at which two values differ, or returns ""
// when they are the same machine. It is reflect.DeepEqual without the
// distinction between a nil slice and an empty one, which no consumer can see.
func firstDiff(path string, a, b reflect.Value) string {
	if a.Kind() == reflect.Pointer || a.Kind() == reflect.Interface {
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return path + " nil mismatch"
			}
			return ""
		}
		return firstDiff(path, a.Elem(), b.Elem())
	}
	switch a.Kind() {
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if d := firstDiff(path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i)); d != "" {
				return d
			}
		}
	case reflect.Array, reflect.Slice:
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s len %d vs %d", path, a.Len(), b.Len())
		}
		for i := 0; i < a.Len(); i++ {
			if d := firstDiff(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); d != "" {
				return d
			}
		}
	default:
		if fmt.Sprint(a) != fmt.Sprint(b) {
			return fmt.Sprintf("%s: %v vs %v", path, a, b)
		}
	}
	return ""
}

func TestBlockStateRoundTrip(t *testing.T) {
	a, ioA := newBlockWithESP()
	ioA.send("AT\r") // the module is mid-reply at the capture
	a.Advance(8000, nil)
	ioA.wr(0, 0x20) // prescaler lsb
	ioA.wr(0, 0x80) // prescaler bits 13:7
	ioA.wr(2, 0x1C) // 8E1
	ioA.wr(1, 0x40) // select uart 1
	ioA.wr(2, 0x1E)
	ioA.wr(1, 0x00)
	a.SetLoopback(1, false)
	a.SetCTSn(1, true)
	for i := 0; i < 20; i++ {
		ioA.wr(3, byte(0x30+i))
	}
	a.Advance(3000, nil) // mid-frame
	a.SetRx(1, false)    // line 1 pulled low: break / framing
	a.Advance(700, nil)

	state := a.SaveState()
	if len(state) == 0 {
		t.Fatal("SaveState returned nothing")
	}
	b := NewBlock()
	b.SetPeer(NewESP())
	if err := b.LoadState(state); err != nil {
		t.Fatal(err)
	}
	if firstDiff("block", reflect.ValueOf(a), reflect.ValueOf(b)) != "" {
		t.Fatalf("restored Block differs from the one captured: %s", firstDiff("block", reflect.ValueOf(a), reflect.ValueOf(b)))
	}

	// Run both on, with the same traffic, and require the same behaviour.
	ioB := io{b}
	for round := 0; round < 40; round++ {
		a.Advance(500, nil)
		b.Advance(500, nil)
		ioA.wr(3, byte(round))
		ioB.wr(3, byte(round))
		if x, y := ioA.rd(3), ioB.rd(3); x != y {
			t.Fatalf("round %d: status %02X vs %02X", round, x, y)
		}
		if x, y := ioA.rd(0), ioB.rd(0); x != y {
			t.Fatalf("round %d: data %02X vs %02X", round, x, y)
		}
	}
	if firstDiff("block", reflect.ValueOf(a), reflect.ValueOf(b)) != "" {
		t.Fatalf("the two Blocks diverged after the restore: %s", firstDiff("block", reflect.ValueOf(a), reflect.ValueOf(b)))
	}
}

func TestBlockStateRefusesAPeerMismatch(t *testing.T) {
	a, _ := newBlockWithESP()
	if err := NewBlock().LoadState(a.SaveState()); err == nil {
		t.Error("a state captured with a peer loaded into a Block with none")
	}
	if err := a.LoadState(NewBlock().SaveState()); err == nil {
		t.Error("a state captured without a peer loaded into a Block with one")
	}
	if err := NewBlock().LoadState(nil); err == nil {
		t.Error("an empty state loaded without error")
	}
}

// A capture of a Block that has settled to idle must keep it settled: the
// skip count is part of the machine, and a restore that lost it would run a
// few Clocks it need not, which is harmless, while one that invented it would
// skip Clocks that matter.
func TestBlockStateRoundTripWhenQuiescent(t *testing.T) {
	a, _ := newBlockWithESP()
	a.Advance(200, nil)
	if a.quiet < quiescentSettle {
		t.Fatalf("setup: Block not quiescent (quiet=%d)", a.quiet)
	}
	b := NewBlock()
	b.SetPeer(NewESP())
	if err := b.LoadState(a.SaveState()); err != nil {
		t.Fatal(err)
	}
	if d := firstDiff("block", reflect.ValueOf(a), reflect.ValueOf(b)); d != "" {
		t.Errorf("restored quiescent Block differs: %s", d)
	}
}
