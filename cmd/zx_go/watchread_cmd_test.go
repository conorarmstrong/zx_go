package main

import (
	"strings"
	"testing"
)

// watch-read arms a RAM read-watchpoint: reading a watched address halts
// the CPU at the reading instruction. This is the mirror of watch-mem
// (writes) and the piece needed to catch a routine that reads a buffer
// byte and rejects it — invisible to a write-watch.
func TestWatchRead_ParseAndFires(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.paused.Store(true) // skip the implicit dispatch pause-wait

	// Bad usage rejected.
	if got := d.handleCommand("watch-read bogus"); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("bad usage = %q, want ERR", got)
	}

	// $4000 is page 5 on the 128K model; offset 0 within that 16K page.
	if got := d.handleCommand("watch-read ram 5 0000 00FF"); !strings.HasPrefix(got, "OK watching reads") {
		t.Fatalf("arm = %q", got)
	}

	// A read outside the range must not fire.
	d.paused.Store(false)
	d.emu.mem.Write(0xC000, 0x11) // page 0, not watched
	_ = d.emu.mem.Read(0xC000)
	if d.paused.Load() {
		t.Fatal("read outside watched range wrongly halted")
	}

	// A read inside the range fires.
	d.emu.mem.Write(0x4000, 0x42)
	_ = d.emu.mem.Read(0x4000)
	if !d.paused.Load() {
		t.Fatal("read of watched address did not halt the CPU")
	}

	// Clear: no further firing.
	d.handleCommand("clear-watch-read")
	d.paused.Store(false)
	_ = d.emu.mem.Read(0x4000)
	if d.paused.Load() {
		t.Fatal("read fired after clear-watch-read")
	}
}
