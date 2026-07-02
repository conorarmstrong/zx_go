package trace

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestNoopEmitterDiscards(t *testing.T) {
	var n NoopEmitter
	// No way to assert "did nothing" except not panic. Calling
	// with various event types is the contract.
	n.Emit(PCEvent{PC: 0x1234})
	n.Emit(NextRegEvent{Reg: 0x12, Value: 0x34})
	n.Emit(PortEvent{Port: 0xFE})
	n.Emit(BankSwitchEvent{Source: "7FFD"})
}

func TestJSONEmitterShapesEvent(t *testing.T) {
	var buf bytes.Buffer
	e := NewJSONEmitter(&buf, nil)
	e.Emit(PCEvent{PC: 0x0D6B, Bank: 3, Insn: 42, Frame: 17})
	got := buf.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected newline-terminated line, got %q", got)
	}
	// Round-trip via json.Decode.
	var wrapper map[string]any
	if err := json.Unmarshal(buf.Bytes(), &wrapper); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if wrapper["kind"] != "pc" {
		t.Errorf("kind = %v, want %q", wrapper["kind"], "pc")
	}
	event, ok := wrapper["event"].(map[string]any)
	if !ok {
		t.Fatalf("event field not a map: %v", wrapper["event"])
	}
	if int(event["pc"].(float64)) != 0x0D6B {
		t.Errorf("event.pc = %v, want 0x0D6B", event["pc"])
	}
	if int(event["bank"].(float64)) != 3 {
		t.Errorf("event.bank = %v, want 3", event["bank"])
	}
}

func TestJSONEmitterAllKinds(t *testing.T) {
	cases := []struct {
		ev   Event
		kind string
	}{
		{PCEvent{}, "pc"},
		{NextRegEvent{}, "nextreg"},
		{PortEvent{}, "port"},
		{BankSwitchEvent{}, "bankswitch"},
		{ScreenDiffEvent{}, "screen"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		e := NewJSONEmitter(&buf, nil)
		e.Emit(c.ev)
		var wrapper map[string]any
		if err := json.Unmarshal(buf.Bytes(), &wrapper); err != nil {
			t.Errorf("%s: bad JSON: %v", c.kind, err)
			continue
		}
		if wrapper["kind"] != c.kind {
			t.Errorf("%s: kind = %v, want %q", c.kind, wrapper["kind"], c.kind)
		}
	}
}

// failingWriter returns the given error on every Write so the
// onError callback can be exercised.
type failingWriter struct{ err error }

func (f failingWriter) Write(p []byte) (int, error) { return 0, f.err }

func TestJSONEmitterStopsAfterWriteError(t *testing.T) {
	var called int
	var capturedErr error
	want := errors.New("disk full")
	e := NewJSONEmitter(failingWriter{err: want}, func(err error) {
		called++
		capturedErr = err
	})
	e.Emit(PCEvent{PC: 1})
	e.Emit(PCEvent{PC: 2})
	e.Emit(PCEvent{PC: 3})
	if called != 1 {
		t.Errorf("onError called %d times, want 1 (subsequent writes silenced)", called)
	}
	if !errors.Is(capturedErr, want) {
		t.Errorf("captured err = %v, want %v", capturedErr, want)
	}
}

func TestJSONEmitterConcurrentEmit(t *testing.T) {
	var buf bytes.Buffer
	e := NewJSONEmitter(&buf, nil)
	const goroutines = 8
	const perG = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				e.Emit(PCEvent{PC: uint16(id), Insn: uint64(j)})
			}
		}(i)
	}
	wg.Wait()
	// Every emitted line must be a complete, valid JSON object —
	// concurrent writes must not interleave bytes.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != goroutines*perG {
		t.Errorf("got %d lines, want %d", len(lines), goroutines*perG)
	}
	for i, line := range lines {
		var w map[string]any
		if err := json.Unmarshal([]byte(line), &w); err != nil {
			t.Errorf("line %d not valid JSON: %v\n  line: %q", i, err, line)
			break
		}
	}
}

func TestMultiEmitterForwardsToAllSinks(t *testing.T) {
	var b1, b2 bytes.Buffer
	m := NewMultiEmitter(
		NewJSONEmitter(&b1, nil),
		nil, // nil sinks must be silently skipped
		NewJSONEmitter(&b2, nil),
	)
	m.Emit(PCEvent{PC: 0x1234})
	if b1.Len() == 0 || b2.Len() == 0 {
		t.Errorf("both sinks should have received the event; got b1=%d b2=%d bytes",
			b1.Len(), b2.Len())
	}
	if b1.String() != b2.String() {
		t.Errorf("sinks got different output\n  b1: %q\n  b2: %q", b1.String(), b2.String())
	}
}

func TestParseChannels(t *testing.T) {
	cases := []struct {
		in     string
		want   Channels
		hasErr bool
	}{
		{"", Channels{}, false},
		{"pc", Channels{PC: true}, false},
		{"nextreg,ports", Channels{NextReg: true, Ports: true}, false},
		{"pc,nextreg,ports,bankswitch,screen",
			Channels{PC: true, NextReg: true, Ports: true, BankSwitch: true, Screen: true},
			false},
		{"port", Channels{Ports: true}, false},      // alias
		{"bank", Channels{BankSwitch: true}, false}, // alias
		{",pc,", Channels{PC: true}, false},         // empty tokens tolerated
		{"junk", Channels{}, true},
	}
	for _, c := range cases {
		got, err := ParseChannels(c.in)
		if c.hasErr {
			if err == nil {
				t.Errorf("ParseChannels(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseChannels(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseChannels(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestChannelsEmpty(t *testing.T) {
	if !(Channels{}).Empty() {
		t.Errorf("zero Channels should be Empty")
	}
	if (Channels{PC: true}).Empty() {
		t.Errorf("Channels with PC enabled should not be Empty")
	}
}
