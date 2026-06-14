package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// snapshotOnBPDir, when non-nil, is the directory the debugger
// drops a fresh .szx file into every time any breakpoint fires.
// Files are named bp_$PCNNN.szx where NNN is a 4-digit zero-padded
// fire counter so consecutive hits don't clobber.
var snapshotOnBPDir atomic.Pointer[string]
var snapshotOnBPCounter atomic.Uint64

// cmdSnapshotOnBP implements `snapshot-on-bp DIR` (empty DIR to
// disable). The directory must already exist; the debugger refuses
// to mkdir for safety.
func (d *remoteDebugger) cmdSnapshotOnBP(args []string) string {
	if len(args) == 0 {
		snapshotOnBPDir.Store(nil)
		return "OK snapshot-on-bp disabled"
	}
	dir := strings.Join(args, " ")
	dir = strings.TrimSpace(dir)
	if dir == "" {
		snapshotOnBPDir.Store(nil)
		return "OK snapshot-on-bp disabled"
	}
	if !directoryExists(dir) {
		return "ERR directory does not exist: " + dir
	}
	snapshotOnBPDir.Store(&dir)
	snapshotOnBPCounter.Store(0)
	return "OK snapshot-on-bp dir=" + dir
}

// snapshotOnBPHit is called from BreakpointCheck after the halt
// decision is made. Writes the current emulator state to a .szx in
// the configured directory; logs but does not propagate errors so
// a write failure doesn't compound the user's debugging session.
func (d *remoteDebugger) snapshotOnBPHit(pc uint16, reason string) {
	p := snapshotOnBPDir.Load()
	if p == nil {
		return
	}
	dir := *p
	n := snapshotOnBPCounter.Add(1)
	path := filepath.Join(dir, fmt.Sprintf("bp_%04X_%04d.szx", pc, n))
	snap, err := createSnapshotFromEmulator(d.emu)
	if err != nil {
		slog.Warn("snapshot-on-bp: build failed", "err", err, "path", path)
		return
	}
	if err := snap.Save(path); err != nil {
		slog.Warn("snapshot-on-bp: save failed", "err", err, "path", path)
		return
	}
	slog.Info("snapshot-on-bp: wrote", "path", path, "pc", fmt.Sprintf("$%04X", pc), "reason", reason)
}

func directoryExists(p string) bool {
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	return st.IsDir()
}
