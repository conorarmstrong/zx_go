package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Covers the remoteDebugger bank-command parsers — cmdBankPeek /
// cmdBankPoke / cmdDisasmBank / cmdListBanks. These take raw arg
// slices and return protocol strings, so they test cleanly with a
// minimal emulator wrapping a real Memory.

func newRemoteFor(t *testing.T, model roms.SpectrumModel) *remoteDebugger {
	t.Helper()
	dir := newTestRomDir(t)
	mem, err := memory.New(dir, model)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	return &remoteDebugger{emu: &emulator{mem: mem}}
}

func TestCmdBankPeek(t *testing.T) {
	d := newRemoteFor(t, roms.Model128K)

	if got := d.cmdBankPeek([]string{"ram"}); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("too-few-args = %q, want usage error", got)
	}
	if got := d.cmdBankPeek([]string{"ram", "zz", "0"}); !strings.HasPrefix(got, "ERR bad bank") {
		t.Errorf("bad bank = %q", got)
	}
	if got := d.cmdBankPeek([]string{"ram", "5", "zz"}); !strings.HasPrefix(got, "ERR bad off") {
		t.Errorf("bad off = %q", got)
	}
	if got := d.cmdBankPeek([]string{"ram", "5", "0", "zz"}); !strings.HasPrefix(got, "ERR bad len") {
		t.Errorf("bad len = %q", got)
	}
	if got := d.cmdBankPeek([]string{"bogus", "0", "0"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("bad kind = %q, want ERR", got)
	}
	// Success: 4 bytes from rom bank 0. ROM test data is byte i&0xFF,
	// so offset 0 → "00 01 02 03".
	got := d.cmdBankPeek([]string{"rom", "0", "0", "4"})
	if got != "OK 00 01 02 03" {
		t.Errorf("peek rom = %q, want \"OK 00 01 02 03\"", got)
	}
	// Length clamp: request 0x400 (1024) → capped to 256 entries.
	got = d.cmdBankPeek([]string{"rom", "0", "0", "400"})
	fields := strings.Fields(strings.TrimPrefix(got, "OK "))
	if len(fields) != 256 {
		t.Errorf("len clamp produced %d bytes, want 256", len(fields))
	}
}

func TestCmdBankPoke(t *testing.T) {
	d := newRemoteFor(t, roms.Model128K)

	if got := d.cmdBankPoke([]string{"ram", "5", "0"}); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("too-few = %q, want usage", got)
	}
	if got := d.cmdBankPoke([]string{"ram", "zz", "0", "FF"}); !strings.HasPrefix(got, "ERR bad bank") {
		t.Errorf("bad bank = %q", got)
	}
	if got := d.cmdBankPoke([]string{"ram", "5", "zz", "FF"}); !strings.HasPrefix(got, "ERR bad off") {
		t.Errorf("bad off = %q", got)
	}
	if got := d.cmdBankPoke([]string{"ram", "5", "0", "ZZ"}); !strings.HasPrefix(got, "ERR bad byte") {
		t.Errorf("bad byte = %q", got)
	}
	// Success: write two bytes then read them back.
	if got := d.cmdBankPoke([]string{"ram", "5", "0", "AB", "CD"}); got != "OK wrote 2 bytes" {
		t.Errorf("poke = %q, want \"OK wrote 2 bytes\"", got)
	}
	if got := d.cmdBankPeek([]string{"ram", "5", "0", "2"}); got != "OK AB CD" {
		t.Errorf("readback = %q, want \"OK AB CD\"", got)
	}
}

func TestCmdDisasmBank(t *testing.T) {
	d := newRemoteFor(t, roms.Model128K)

	if got := d.cmdDisasmBank([]string{"ram"}); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("too-few = %q, want usage", got)
	}
	if got := d.cmdDisasmBank([]string{"ram", "zz", "0"}); !strings.HasPrefix(got, "ERR bad bank") {
		t.Errorf("bad bank = %q", got)
	}
	if got := d.cmdDisasmBank([]string{"ram", "5", "zz"}); !strings.HasPrefix(got, "ERR bad off") {
		t.Errorf("bad off = %q", got)
	}
	if got := d.cmdDisasmBank([]string{"ram", "5", "4000"}); !strings.HasPrefix(got, "ERR offset past end") {
		t.Errorf("off-past-end = %q", got)
	}
	// Poke a NOP (0x00) then disassemble it.
	d.cmdBankPoke([]string{"ram", "5", "0", "00"})
	got := d.cmdDisasmBank([]string{"ram", "5", "0", "1"})
	if !strings.HasPrefix(got, "OK") || !strings.Contains(got, "NOP") {
		t.Errorf("disasm = %q, want OK + NOP", got)
	}
}

func TestCmdListBanks(t *testing.T) {
	d := newRemoteFor(t, roms.Model128K)
	got := d.cmdListBanks()
	if got != "OK ram=128*16K rom=4*16K altrom=2*16K" {
		t.Errorf("list-banks = %q", got)
	}
}
