package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
)

// nextTestROMs lays down the minimal ROM blobs memory.New(ModelNext)
// needs, so the test is self-contained (no dependency on the repo's
// roms/ tree or its relative path).
func nextTestROMs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{
		"48.rom", "128-0.rom", "128-1.rom",
		"plus2-0.rom", "plus2-1.rom",
		"plus3-0.rom", "plus3-1.rom", "plus3-2.rom", "plus3-3.rom",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), make([]byte, 0x4000), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestNextFullStateRoundTrip exercises the Phase-2b full-state capture/
// restore over every component a faithful Next rewind must cover: the
// 2MB pool (incl. a non-visible bank), the MMU8 slot table, a NextReg,
// and divMMC RAM. Capture, corrupt each, restore, expect the originals.
func TestNextFullStateRoundTrip(t *testing.T) {
	mem, err := memory.New(nextTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	nr := nextregs.New()
	pager := divmmc.New([]byte{0xFF})

	mem.GetPage(60)[10] = 0xC3 // non-visible pool bank
	mem.SetMMU(2, 0x29)        // MMU8 slot
	nr.Store(0x8C, 0x5A)       // a NextReg
	pager.RAMBank(2)[5] = 0x77 // divMMC RAM

	fs := captureNextFullState(mem, nr, pager)

	mem.GetPage(60)[10] = 0x00
	mem.SetMMU(2, 0xFF)
	nr.Store(0x8C, 0x00)
	pager.RAMBank(2)[5] = 0x00

	restoreNextFullState(mem, nr, pager, fs)

	if got := mem.GetPage(60)[10]; got != 0xC3 {
		t.Errorf("pool bank 60 off 10 = $%02X, want $C3", got)
	}
	if got := mem.GetMMU(2); got != 0x29 {
		t.Errorf("MMU slot 2 = $%02X, want $29", got)
	}
	if got := nr.Raw(0x8C); got != 0x5A {
		t.Errorf("NextReg $8C = $%02X, want $5A", got)
	}
	if got := pager.RAMBank(2)[5]; got != 0x77 {
		t.Errorf("divMMC RAM bank 2 off 5 = $%02X, want $77", got)
	}
}

// TestCaptureNextStateFromEmu verifies the *emulator wrapper threads
// the right components through (mem, nextRegs, the divMMC pager behind
// ula.NextDivMMC), round-trips pool + divMMC RAM, and is nil-safe.
func TestCaptureNextStateFromEmu(t *testing.T) {
	mem, err := memory.New(nextTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	u := ula.New(mem, keyboard.New())
	pager := divmmc.New([]byte{0xFF})
	u.SetNextDivMMC(pager)
	emu := &emulator{mem: mem, nextRegs: nextregs.New(), ula: u}

	mem.GetPage(70)[3] = 0x9D
	pager.RAMBank(1)[9] = 0x42

	fs := captureNextStateFromEmu(emu)
	if fs == nil {
		t.Fatal("captureNextStateFromEmu returned nil for a Next emulator")
	}

	mem.GetPage(70)[3] = 0x00
	pager.RAMBank(1)[9] = 0x00
	restoreNextStateToEmu(emu, fs)

	if got := mem.GetPage(70)[3]; got != 0x9D {
		t.Errorf("pool not restored: $%02X want $9D", got)
	}
	if got := pager.RAMBank(1)[9]; got != 0x42 {
		t.Errorf("divMMC RAM not restored: $%02X want $42", got)
	}

	// Nil-safety: must not panic or capture anything.
	if captureNextStateFromEmu(nil) != nil {
		t.Error("captureNextStateFromEmu(nil) should be nil")
	}
	restoreNextStateToEmu(nil, fs)  // no panic
	restoreNextStateToEmu(emu, nil) // no panic
}
