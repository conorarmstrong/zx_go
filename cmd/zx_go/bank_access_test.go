package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Covers emuBanks.Bank / BankPeek / BankPoke for the memory-only
// kinds (ram, rom, altrom). The divmmc-ram path requires an
// *emulator and is tested separately at integration level; here we
// just confirm it returns ErrUnknownKind when emu is nil (the safe
// default).

func newTestRomDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"48.rom", "128-0.rom", "128-1.rom"} {
		data := make([]byte, 16384)
		for i := range data {
			data[i] = byte(i & 0xFF)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newBanksFor(t *testing.T, model roms.SpectrumModel) *emuBanks {
	t.Helper()
	dir := newTestRomDir(t)
	mem, err := memory.New(dir, model)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	return &emuBanks{mem: mem, emu: nil}
}

func TestEmuBanks_RAM_ValidBank(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	page, err := b.Bank("ram", 5) // bank 5 (screen RAM)
	if err != nil {
		t.Fatalf("ram bank 5: %v", err)
	}
	if len(page) != 16384 {
		t.Errorf("ram bank len = %d, want 16384", len(page))
	}
}

func TestEmuBanks_RAM_OutOfRange(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	_, err := b.Bank("ram", 9999)
	if !errors.Is(err, debugger.ErrBankOutOfRange) {
		t.Errorf("ram OOB = %v, want ErrBankOutOfRange", err)
	}
}

func TestEmuBanks_ROM_ValidBank(t *testing.T) {
	b := newBanksFor(t, roms.Model128K)
	rom, err := b.Bank("rom", 0)
	if err != nil {
		t.Fatalf("rom bank 0: %v", err)
	}
	if len(rom) != 16384 {
		t.Errorf("rom bank len = %d, want 16384", len(rom))
	}
}

func TestEmuBanks_ROM_OutOfRange(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	_, err := b.Bank("rom", 99)
	if !errors.Is(err, debugger.ErrBankOutOfRange) {
		t.Errorf("rom OOB = %v, want ErrBankOutOfRange", err)
	}
}

func TestEmuBanks_AltROM_ValidBank(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	// AltROM buffers 0 and 1 are always allocated; both should
	// return non-nil slices on any model.
	for _, bank := range []int{0, 1} {
		if _, err := b.Bank("altrom", bank); err != nil {
			t.Errorf("altrom bank %d = %v, want nil err", bank, err)
		}
	}
}

func TestEmuBanks_AltROM_OutOfRange(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	// Banks > 1 are out of range.
	if _, err := b.Bank("altrom", 2); !errors.Is(err, debugger.ErrBankOutOfRange) {
		t.Errorf("altrom bank 2 = %v, want ErrBankOutOfRange", err)
	}
}

func TestEmuBanks_DivmmcRAM_NoEmuReturnsUnknown(t *testing.T) {
	b := newBanksFor(t, roms.Model48K) // emu intentionally nil
	_, err := b.Bank("divmmc-ram", 0)
	if !errors.Is(err, debugger.ErrUnknownKind) {
		t.Errorf("divmmc-ram with nil emu = %v, want ErrUnknownKind", err)
	}
}

func TestEmuBanks_UnknownKind(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	_, err := b.Bank("nonsense", 0)
	if !errors.Is(err, debugger.ErrUnknownKind) {
		t.Errorf("bogus kind = %v, want ErrUnknownKind", err)
	}
}

func TestEmuBanks_BankPeekAndPoke(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	// Peek bank 5 at offset 0 — initial test bytes are i & 0xFF.
	bytes, err := b.BankPeek("ram", 5, 0, 4)
	if err != nil {
		t.Fatalf("BankPeek: %v", err)
	}
	if len(bytes) != 4 {
		t.Errorf("BankPeek length = %d, want 4", len(bytes))
	}
	// Now poke and read back.
	if err := b.BankPoke("ram", 5, 0x100, []byte{0x42, 0x43}); err != nil {
		t.Fatalf("BankPoke: %v", err)
	}
	got, err := b.BankPeek("ram", 5, 0x100, 2)
	if err != nil {
		t.Fatalf("BankPeek after poke: %v", err)
	}
	if got[0] != 0x42 || got[1] != 0x43 {
		t.Errorf("BankPeek = %v, want [0x42 0x43]", got)
	}
}

// TestPoolScanFindsPattern — pool-scan locates a byte pattern across
// physical RAM banks regardless of CPU mapping.
func TestPoolScanFindsPattern(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	page, err := b.Bank("ram", 5)
	if err != nil {
		t.Fatal(err)
	}
	copy(page[0x123:], []byte{0xDE, 0xAD, 0xBE, 0xEF})

	hits := poolScan(b, "ram", []byte{0xDE, 0xAD, 0xBE, 0xEF}, 8)
	if len(hits) != 1 {
		t.Fatalf("hits = %v, want exactly 1", hits)
	}
	if hits[0].bank != 5 || hits[0].off != 0x123 {
		t.Errorf("hit = bank %d off $%04X, want bank 5 off $0123", hits[0].bank, hits[0].off)
	}
}

// TestPoolScanNoMatch returns empty without error.
func TestPoolScanNoMatch(t *testing.T) {
	b := newBanksFor(t, roms.Model48K)
	if hits := poolScan(b, "ram", []byte{0x01, 0x02, 0x03, 0x99, 0x98}, 8); len(hits) != 0 {
		t.Errorf("hits = %v, want none", hits)
	}
}
