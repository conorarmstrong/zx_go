package main

import (
	"strings"
	"testing"
	"time"
)

// The pause-ack wait defaults to 2s, which is too short to reach a deep
// breakpoint in a long boot (e.g. NextZXOS at insn ~170M, several
// seconds of wall-clock) — the state command gives up before the CPU
// reaches the BP. `set-pause-timeout` makes the wait configurable so a
// `continue` to a far breakpoint can be awaited.

func TestSetPauseTimeout_DefaultAndOverride(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.paused.Store(true) // skip the implicit pause-ack wait for dispatch

	// Default is 2s before any override.
	if got := d.pauseAckTimeout(); got != 2*time.Second {
		t.Fatalf("default pauseAckTimeout = %v, want 2s", got)
	}

	// Query form reports the current value.
	if got := d.handleCommand("set-pause-timeout"); !strings.HasPrefix(got, "OK pause-timeout=2") {
		t.Fatalf("set-pause-timeout (query) = %q, want prefix \"OK pause-timeout=2\"", got)
	}

	// Override to 30s.
	if got := d.handleCommand("set-pause-timeout 30"); !strings.HasPrefix(got, "OK pause-timeout=30") {
		t.Fatalf("set-pause-timeout 30 = %q, want prefix \"OK pause-timeout=30\"", got)
	}
	if got := d.pauseAckTimeout(); got != 30*time.Second {
		t.Fatalf("after override pauseAckTimeout = %v, want 30s", got)
	}

	// Bad arg is rejected, value unchanged.
	if got := d.handleCommand("set-pause-timeout nope"); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("set-pause-timeout nope = %q, want ERR", got)
	}
	if got := d.pauseAckTimeout(); got != 30*time.Second {
		t.Fatalf("after bad arg pauseAckTimeout = %v, want still 30s", got)
	}
}
