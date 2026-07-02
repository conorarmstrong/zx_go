package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Diagnosing a watched $0000-$3FFF write needs to know WHERE it routes
// (altROM buffer vs divMMC window vs dropped-on-ROM) — the bare
// addr/val/pc line can't say. The watch-write log line therefore
// carries the routing context: rom_bank, the NR$8C alt-ROM shadow,
// and MMU0/MMU1.
func TestWatchWrite_LogsRoutingContext(t *testing.T) {
	mem, err := memory.New(nextTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	cpu := z80.New(mem, nil)
	emu := &emulator{cpu: cpu, mem: mem}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	mem.SetAltROMReg(0xC0) // make the context line carry a non-zero $8C
	installWriteWatchers(emu, "probe@4000", nil)
	mem.Write(0x4000, 0x42)

	out := buf.String()
	if !strings.Contains(out, "watch-write") {
		t.Fatalf("no watch-write line logged: %q", out)
	}
	for _, want := range []string{"rom_bank=", "alt=$C0", "mmu0=", "mmu1="} {
		if !strings.Contains(out, want) {
			t.Errorf("watch-write line missing %s: %q", want, out)
		}
	}
}
