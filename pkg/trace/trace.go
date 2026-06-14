// Package trace is the structured-event emitter that powers the
// `--trace=...` CLI flag.
//
// Each hookable subsystem (CPU pre-fetch, NextReg accesses, port
// I/O, memory bank-switch) emits typed events to an Emitter. The
// default Emitter is a no-op so the tracing infrastructure has
// zero runtime cost when disabled. Enabling tracing via
// `cmd/zx_go --trace=...` installs a JSONEmitter that writes one
// JSON object per line to a file or stderr.
//
// The package is intentionally tiny and dependency-free: callers
// pass event values, the emitter serialises them. Adding new event
// types is a matter of declaring a struct with a `Kind() string`
// method.
//
// JSON-lines format was chosen over the alternatives (binary, raw
// text, slog records) because:
//
//   - jq makes filtering trivial: `jq 'select(.kind=="nextreg")'`
//   - diff between two runs is line-by-line, well-supported by
//     standard tools
//   - schema evolution is forgiving (extra fields are ignored by
//     old consumers)
//   - the existing slog handler can already write structured records
//     in this shape if we want to bridge later
package trace

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Event is the marker interface implemented by every emittable
// event type. Kind returns a short tag the emitter uses as the
// `"kind"` field in JSON output so consumers can filter by type.
type Event interface {
	Kind() string
}

// Emitter accepts trace events. Implementations must be safe for
// concurrent calls from the emulation goroutine and (occasionally)
// the Fyne UI goroutine. NoopEmitter is the zero-value default.
type Emitter interface {
	Emit(e Event)
}

// NoopEmitter discards every event. Used when tracing is disabled
// (the common case). Zero cost beyond an interface method call.
type NoopEmitter struct{}

// Emit implements Emitter.
func (NoopEmitter) Emit(_ Event) {}

// JSONEmitter writes each event as a single line of JSON to w.
// Safe for concurrent use. Errors from w are logged once and
// subsequent writes silently dropped — the emulator must not stop
// because a trace file has trouble.
type JSONEmitter struct {
	mu      sync.Mutex
	w       io.Writer
	enc     *json.Encoder
	errored bool
	onError func(error) // called once on first write error
}

// NewJSONEmitter wraps w. The encoder is configured with no
// indentation (so each event is exactly one line). onError is
// invoked once on the first write failure (typically to surface
// a `slog.Warn` to the user); pass nil to ignore failures.
func NewJSONEmitter(w io.Writer, onError func(error)) *JSONEmitter {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "")
	return &JSONEmitter{w: w, enc: enc, onError: onError}
}

// Emit serialises e and writes one JSON line. The output object
// contains the event's struct fields plus a `"kind"` field set to
// e.Kind(). Concurrent calls are serialised; the lock is held only
// for the duration of one Encode call.
func (j *JSONEmitter) Emit(e Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.errored {
		return
	}
	// Wrap the event in a small struct so the kind tag appears
	// alongside the event fields without each event type having
	// to embed it.
	wrapper := struct {
		Kind  string `json:"kind"`
		Event Event  `json:"event"`
	}{
		Kind:  e.Kind(),
		Event: e,
	}
	if err := j.enc.Encode(wrapper); err != nil {
		j.errored = true
		if j.onError != nil {
			j.onError(err)
		}
	}
}

// PCEvent is emitted on every M1 fetch when the `pc` channel is
// enabled. Optional Range filter narrows what gets captured;
// without a filter the volume is impractical.
type PCEvent struct {
	PC    uint16 `json:"pc"`
	Bank  int    `json:"bank"`  // ROM bank visible at slot 0 (0–3 for Next, -1 for classic)
	Insn  uint64 `json:"insn"`  // instruction counter
	Frame int    `json:"frame"` // simulated frame counter
}

// Kind implements Event.
func (PCEvent) Kind() string { return "pc" }

// NextRegEvent is emitted on every NextReg read or write when the
// `nextreg` channel is enabled.
type NextRegEvent struct {
	PC    uint16 `json:"pc"`
	Reg   uint8  `json:"reg"`
	Value uint8  `json:"value"`
	Write bool   `json:"write"`
}

// Kind implements Event.
func (NextRegEvent) Kind() string { return "nextreg" }

// PortEvent is emitted on every interesting I/O port access when
// the `ports` channel is enabled. "Interesting" is anything our
// emulation routes specially — \$E3/\$E7/\$EB (divMMC), \$7FFD/
// \$1FFD (paging), \$FE (border/key), \$243B/\$253B (NextReg).
type PortEvent struct {
	PC    uint16 `json:"pc"`
	Port  uint16 `json:"port"`
	Value uint8  `json:"value"`
	Write bool   `json:"write"`
}

// Kind implements Event.
func (PortEvent) Kind() string { return "port" }

// BankSwitchEvent is emitted whenever the slot-0 ROM bank changes
// (via port 7FFD bit 4, port 1FFD bit 2, or NextReg \$8E) or any
// MMU slot is reassigned (NextReg \$50–\$57). The Source field
// names the trigger so consumers can correlate with port/nextreg
// events.
type BankSwitchEvent struct {
	PC      uint16 `json:"pc"`
	Slot    uint8  `json:"slot"` // 8K slot index (0–7) for MMU, or 0xFF for ROM-bank toggle
	NewBank int    `json:"new_bank"`
	Source  string `json:"source"` // "7FFD", "1FFD", "NEXTREG_8E", "NEXTREG_50+slot"
}

// Kind implements Event.
func (BankSwitchEvent) Kind() string { return "bankswitch" }

// ScreenDiffEvent is emitted when the framebuffer content hash
// changes (only if the `screen` channel is enabled in a future
// iteration). Captured here so the schema is stable.
type ScreenDiffEvent struct {
	Frame      int    `json:"frame"`
	BitmapHash string `json:"bitmap_hash"`
	AttrHash   string `json:"attr_hash"`
}

// Kind implements Event.
func (ScreenDiffEvent) Kind() string { return "screen" }

// MultiEmitter fans the same event out to multiple sinks. Useful
// when one channel writes to stderr while another writes to a
// file, for example.
type MultiEmitter struct {
	sinks []Emitter
}

// NewMultiEmitter returns an emitter that forwards to all sinks
// in order. Nil sinks are skipped.
func NewMultiEmitter(sinks ...Emitter) *MultiEmitter {
	out := make([]Emitter, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			out = append(out, s)
		}
	}
	return &MultiEmitter{sinks: out}
}

// Emit forwards e to every sink.
func (m *MultiEmitter) Emit(e Event) {
	for _, s := range m.sinks {
		s.Emit(e)
	}
}

// Channels is the parsed form of the `--trace=` CLI flag. Each
// field gates whether the corresponding hook installs its emitter
// call at startup.
type Channels struct {
	PC         bool
	NextReg    bool
	Ports      bool
	BankSwitch bool
	Screen     bool
}

// Empty reports whether any channel is enabled. When false, the
// CLI can skip every hook-install step and the program runs at
// full speed.
func (c Channels) Empty() bool {
	return c == Channels{}
}

// ParseChannels decodes a comma-separated list as used in the
// `--trace=` flag. Unknown channels return an error so typos
// don't silently disable a request. Empty input returns an empty
// Channels struct, no error.
func ParseChannels(s string) (Channels, error) {
	var c Channels
	if s == "" {
		return c, nil
	}
	// Walk the comma-separated list manually rather than pulling
	// in strings.Split — this stays dependency-free and works on
	// any well-formed input.
	for len(s) > 0 {
		var tok string
		if i := indexByte(s, ','); i >= 0 {
			tok, s = s[:i], s[i+1:]
		} else {
			tok, s = s, ""
		}
		switch tok {
		case "":
			// empty token between commas — ignore
		case "pc":
			c.PC = true
		case "nextreg":
			c.NextReg = true
		case "ports", "port":
			c.Ports = true
		case "bankswitch", "bank":
			c.BankSwitch = true
		case "screen":
			c.Screen = true
		default:
			return Channels{}, fmt.Errorf("trace: unknown channel %q (valid: pc, nextreg, ports, bankswitch, screen)", tok)
		}
	}
	return c, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
