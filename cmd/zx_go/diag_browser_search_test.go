package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"testing"
)

// TestDiagBrowserSearch reproduces the NextZXOS Browser "search" crash
// (issues #9/#10). It boots to the menu, opens the Browser, then drives
// the incremental search (press H, then a letter) while tracing port
// $FF (Timex SCLD video-mode register) writes. Screenshots are saved
// when NEX_RENDER_OUT_DIR is set so the corruption can be eyeballed.
//
//	ZX_GO_DIAG=1 NEX_RENDER_OUT_DIR=/tmp/shots \
//	  go test ./cmd/zx_go -run TestDiagBrowserSearch -v -count=1 -timeout 600s
func TestDiagBrowserSearch(t *testing.T) {
	if os.Getenv("ZX_GO_DIAG") == "" {
		t.Skip("diagnostic — set ZX_GO_DIAG=1 to run")
	}
	emu := bootNextToMenu(t)

	// Trace every port $FF write (the Timex SCLD video-mode register).
	ffWrites := map[byte]int{}
	var lastFF byte
	emu.ula.SetPortTracer(func(addr uint16, val byte, write, handled bool) {
		if write && (addr&0xFF) == 0xFF {
			ffWrites[val]++
			lastFF = val
		}
	})

	shot := func(name string) {
		dir := os.Getenv("NEX_RENDER_OUT_DIR")
		if dir == "" {
			return
		}
		var buf bytes.Buffer
		if writeScreenshotPNG(emu, &buf) == nil {
			_ = os.WriteFile(dir+"/"+name+".png", buf.Bytes(), 0o644)
		}
	}

	rebooted := func() bool {
		// A reboot blanks the screen and cascades through low ROM before
		// settling back at the welcome loop. Detect the blank screen.
		img := emu.renderFrame()
		return uniformImage(img)
	}

	// Open the Browser: cursor is on "Browser" at the menu; ENTER opens it.
	nexPressCombo(emu, [][2]int{{6, 0x01}}, 6, 14) // ENTER
	nexRunFrames(emu, 200)
	shot("01-browser-open")
	t.Logf("browser-open: PC=%#04x timexMode(last $FF)=%#02x blank=%v", emu.cpu.PC, lastFF, rebooted())

	press := func(label string, row int, mask byte, frames int) {
		emu.kbd.PressMatrixKey(row, mask, true)
		nextRunFrames(emu, 12)
		emu.kbd.PressMatrixKey(row, mask, false)
		nextRunFrames(emu, frames)
		shot(label)
		t.Logf("%s: PC=%#04x lastFF=%#02x blank=%v", label, emu.cpu.PC, lastFF, rebooted())
	}

	// Isolate the trigger:
	press("02-nav-down", 0, 0x01, 40) // CAPS = part of DOWN; harmless probe of a modifier
	press("03-jump-s", 1, 0x02, 40)   // 's' in normal browser = jump highlight to SRC
	press("04-search-h", 6, 0x10, 40) // 'h' = enter incremental search

	// Catch whoever corrupts the ULA attribute region ($5800-$5AFF =
	// offset $1800-$1AFF in the screen bank) during the search char.
	type wr struct {
		pc, addr uint16
		val      byte
		bank     int
	}
	var writes []wr
	emu.mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
		if bank == emu.mem.ScreenPage && addr >= 0x1800 && addr < 0x1B00 {
			if len(writes) < 400 {
				writes = append(writes, wr{emu.cpu.PC, addr, val, bank})
			}
		}
	})
	press("05-search-s", 1, 0x02, 60) // 's' = first search char (trigger)
	emu.mem.SetRAMWriteHook(nil)

	writerPCs := map[uint16]int{}
	for _, w := range writes {
		writerPCs[w.pc]++
	}
	t.Logf("screen-attr writes during search-s: %d total, ScreenPage=%d", len(writes), emu.mem.ScreenPage)
	for pc, n := range writerPCs {
		t.Logf("  writer PC=%#04x x%d", pc, n)
	}
	// Show the first several raw writes for the address/value pattern.
	for i := 0; i < len(writes) && i < 24; i++ {
		t.Logf("  wr[%d] PC=%#04x bank=%d addr=%#04x val=%#02x", i, writes[i].pc, writes[i].bank, writes[i].addr, writes[i].val)
	}

	// Summarise the $FF video-mode values observed.
	var vals []int
	for v := range ffWrites {
		vals = append(vals, int(v))
	}
	sort.Ints(vals)
	var sb []string
	for _, v := range vals {
		sb = append(sb, fmt.Sprintf("$%02X(mode%03b)x%d", v, v&0x07, ffWrites[byte(v)]))
	}
	t.Logf("distinct port $FF writes: %v", sb)
}
