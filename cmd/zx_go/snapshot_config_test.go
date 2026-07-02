package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Covers parseWatchSpec (default + explicit + word + error branches),
// newSnapshotConfig (disabled + configured), and installBankSwitchLogger
// (tracer fires on a 128K ROM switch).

func TestParseWatchSpec(t *testing.T) {
	// Empty → canonical default set (non-empty).
	if got := parseWatchSpec(""); len(got) == 0 {
		t.Error("empty spec should return default watch set")
	}
	// Explicit byte + word + 0x/$ prefixes, plus malformed entries
	// that are skipped.
	got := parseWatchSpec("A:5C3A, B:$5C3B, C:0x4000, D:5C5D:w, ,nope, E:zz")
	want := []watchEntry{
		{"A", 0x5C3A, false},
		{"B", 0x5C3B, false},
		{"C", 0x4000, false},
		{"D", 0x5C5D, true},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestNewSnapshotConfig(t *testing.T) {
	emu := &emulator{}
	// snapshotEvery <= 0 → nil (disabled).
	if sc := newSnapshotConfig(emu, &cliFlags{snapshotEvery: 0}); sc != nil {
		t.Error("snapshotEvery=0 should return nil config")
	}
	// Configured.
	sc := newSnapshotConfig(emu, &cliFlags{
		snapshotEvery:   10,
		snapshotPrefix:  "shot",
		snapshotScreens: true,
		watchSpec:       "A:5C3A",
	})
	if sc == nil {
		t.Fatal("configured snapshotEvery should return non-nil")
	}
	if sc.every != 10 || sc.prefix != "shot" || !sc.saveScreens {
		t.Errorf("config = %+v", sc)
	}
	if len(sc.watch) != 1 || sc.watch[0].name != "A" {
		t.Errorf("watch = %+v, want one entry named A", sc.watch)
	}
	// saveScreens requires a prefix — empty prefix disables it.
	sc2 := newSnapshotConfig(emu, &cliFlags{snapshotEvery: 5, snapshotScreens: true})
	if sc2.saveScreens {
		t.Error("saveScreens should be false when prefix empty")
	}
}

func TestInstallBankSwitchLogger(t *testing.T) {
	dir := newTestRomDir(t)
	mem, err := memory.New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	// Should install a tracer without panicking; trigger a ROM-bank
	// switch via the 128K paging port and confirm no crash.
	installBankSwitchLogger(mem)
	mem.PageMemory(0x10) // ROM 1 select → fires the tracer
	mem.SetMMU(3, 0x05)  // 8K MMU slot reassignment → fires the tracer
}
