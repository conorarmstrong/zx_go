package main

import (
	"strings"
	"testing"
)

// Drives handleCommand across its read-only / formatting dispatch
// arms. Setting paused=true up front skips the pause-ack channel wait
// (which would otherwise block with no headless loop running), so the
// switch routing itself is exercised. Channel-driven commands
// (continue/step) are covered elsewhere.

func TestHandleCommand_Dispatch(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.paused.Store(true) // skip the implicit pause-ack wait

	cases := []struct {
		line       string
		wantPrefix string
	}{
		{"", "ERR empty"},
		{"help", "OK "},
		{"?", "OK "},
		{"get-registers", "OK PC="},
		{"regs", "OK PC="},
		{"get-stack", "OK sp="},
		{"stack", "OK sp="},
		{"get-mmu", "OK rom_bank="},
		{"mmu", "OK rom_bank="},
		{"get-divmmc", "OK no-ula"}, // emu.ula is nil
		{"divmmc", "OK no-ula"},
		{"read-memory 8000", "OK $"},
		{"peek 4000", "OK $"},
		{"get-memory 8000 4", "OK "},
		{"write-memory 8000 AB", "OK"},
		{"poke 8000 CD", "OK"},
		{"set-reg A 42", "OK"},
		{"list-breakpoints", "OK "},
		{"lbp", "OK "},
		{"set-bp 9000 any-bank", "OK breakpoint"},
		{"clear-bp 9000", "OK cleared"},
		{"list-tp", "OK "},
		{"tp 8000", "OK tp @"},
		{"clear-tp", "OK"},
		{"watch-zero", "OK watch-zero="},
		{"watch-zero on", "OK watch-zero=on"},
		{"cont-until off", "OK cont-until cleared"},
		{"crash-detect", "OK crash-detect off"},
		{"bp-first-entry", "OK (no bp-first-entry armed)"},
		{"snapshot-on-bp", "OK snapshot-on-bp disabled"},
		{"list-banks", "OK ram="},
		{"definitely-not-a-command", "ERR"},
	}
	for _, c := range cases {
		got := d.handleCommand(c.line)
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("handleCommand(%q) = %q, want prefix %q", c.line, got, c.wantPrefix)
		}
	}
}
