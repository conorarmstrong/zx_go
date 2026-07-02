package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
)

// The telnet debugger and the visual debugger operate on ONE
// breakpoint store (emulator-owned), so a breakpoint set from either
// surface is visible to the other. This pins that bidirectional
// sharing at the backend level.
func TestSharedBreakpointsTelnetToGUI(t *testing.T) {
	d := newRemoteWithCPU(t)
	// The telnet debugger's command path resolves the shared set via
	// the emulator; grab the same instance the GUI would receive.
	shared := d.emu.sharedBreakpoints()

	// Set a breakpoint through the TELNET command surface.
	if got := d.cmdSetBreakpoint([]string{"8000", "any-bank"}); !strings.Contains(got, "@$8000") {
		t.Fatalf("set-breakpoint = %q", got)
	}
	// It must be visible in the SHARED store the GUI reads.
	if _, ok := shared.Lookup(0x8000); !ok {
		t.Error("breakpoint set via telnet not visible in the shared store (GUI would not see it)")
	}

	// Now the reverse: add one as the GUI would (directly on the
	// shared store) and confirm the telnet list shows it.
	shared.Add(0x4000, debugger.BPEntry{Bank: -1})
	list := d.cmdListBreakpoints()
	if !strings.Contains(list, "4000") {
		t.Errorf("GUI-added breakpoint $4000 not in telnet list-breakpoints: %q", list)
	}

	// Clearing via telnet must remove it from the shared store too.
	d.cmdClearBreakpoint([]string{"8000"})
	if _, ok := shared.Lookup(0x8000); ok {
		t.Error("clear-breakpoint via telnet did not update the shared store")
	}
}

// newRemoteWithCPU builds a bare remoteDebugger{} (no port), so d.bps
// is nil until first use; this confirms the lazy bpset() accessor
// resolves to the emulator's shared set rather than a private one.
func TestBpsetResolvesToEmulatorShared(t *testing.T) {
	d := newRemoteWithCPU(t)
	got := d.bpset()
	if got != d.emu.sharedBreakpoints() {
		t.Error("bpset() did not resolve to the emulator's shared breakpoint set")
	}
}

// Register watchpoints share one set across telnet and GUI. A watch
// added via the telnet `watch-reg` command must appear in the
// emulator's shared RegWatchSet that the GUI Watchpoints tab renders.
func TestSharedRegWatchesTelnetToGUI(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.regWatches = d.emu.sharedRegWatches() // newRemoteDebugger wires this in production
	shared := d.emu.sharedRegWatches()

	if got := d.cmdWatchReg([]string{"a", "to", "$42"}); !strings.HasPrefix(got, "OK") {
		t.Fatalf("watch-reg = %q", got)
	}
	found := false
	for _, w := range shared.List() {
		if w.Reg == "a" {
			found = true
		}
	}
	if !found {
		t.Error("watch-reg via telnet not visible in the shared RegWatchSet (GUI tab would not show it)")
	}
}

// The time-travel ring is emulator-owned, so the GUI controller and
// the telnet tt-* commands drive the SAME buffer. (Taking an actual
// snapshot needs a full machine — ULA etc. — so this pins the
// shared-ownership wiring, not snapshot capture, which TestTTRewind*
// already covers with a real emulator.)
func TestSharedTimeTravelTelnetAndGUI(t *testing.T) {
	d := newRemoteWithCPU(t)
	ctl := ttController{emu: d.emu}

	if ctl.Enabled() {
		t.Fatal("precondition: time-travel should start off")
	}
	// GUI enables time-travel → the telnet side sees the SAME buffer.
	ctl.Enable(1000, 8)
	if d.emu.timeTravel == nil {
		t.Fatal("GUI Enable did not create the emulator time-travel buffer")
	}
	if !ctl.Enabled() {
		t.Error("controller reports disabled after Enable")
	}
	// A snapshot added directly to the shared ring (label only — no
	// machine state) is visible through the GUI controller's Rows().
	d.emu.timeTravel.entries = append(d.emu.timeTravel.entries,
		ttEntry{insn: 42, pc: 0x1234, label: "shared"})
	rows := ctl.Rows()
	if len(rows) != 1 || rows[0].Label != "shared" || rows[0].PC != 0x1234 {
		t.Errorf("Rows() = %+v, want one {42,$1234,shared}", rows)
	}
	// GUI disable tears it down for the telnet side too.
	ctl.Disable()
	if d.emu.timeTravel != nil {
		t.Error("GUI Disable did not clear the shared time-travel buffer")
	}
}
