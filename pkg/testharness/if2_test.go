package testharness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/if2"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// makeStubCartridge builds a synthetic 16KB Interface 2 cartridge
// that, when executed from PC=0x0000, writes a known magic byte
// pattern into RAM at 0x8000 and then halts. Used to verify the
// full insert → memory override → CPU execution path end-to-end.
//
// Z80 program (12 bytes at 0x0000):
//
//	LD  HL, 0x8000      21 00 80    ; destination
//	LD  (HL), 0xDE      36 DE       ; magic byte 1
//	INC HL              23
//	LD  (HL), 0xAD      36 AD       ; magic byte 2
//	INC HL              23
//	LD  (HL), 0xBE      36 BE       ; magic byte 3
//	INC HL              23
//	LD  (HL), 0xEF      36 EF       ; magic byte 4
//	HALT                76          ; stop
//
// The rest of the 16KB is padding (NOP = 0x00).
func makeStubCartridge(t *testing.T) string {
	t.Helper()
	prog := []byte{
		0x21, 0x00, 0x80, // LD HL, 0x8000
		0x36, 0xDE, //       LD (HL), 0xDE
		0x23,       //       INC HL
		0x36, 0xAD, //       LD (HL), 0xAD
		0x23,       //       INC HL
		0x36, 0xBE, //       LD (HL), 0xBE
		0x23,       //       INC HL
		0x36, 0xEF, //       LD (HL), 0xEF
		0x76, //             HALT
	}
	cart := make([]byte, if2.CartridgeSize)
	copy(cart, prog)
	path := filepath.Join(t.TempDir(), "stub.rom")
	if err := os.WriteFile(path, cart, 0o644); err != nil {
		t.Fatalf("write stub cart: %v", err)
	}
	return path
}

// TestIF2CartridgeBootsAndRuns is the end-to-end validation that
// an Interface 2 cartridge really does replace the BASIC ROM and
// get executed by the Z80. It inserts a synthetic stub cartridge
// that writes a magic byte pattern to 0x8000, runs a few frames,
// and verifies the pattern landed in RAM. A passing test proves:
//   - the peripheral manager hooked the cartridge into HandleMemoryRead
//   - reads at 0x0000..0x000C returned cartridge bytes (not BASIC ROM)
//   - the CPU fetched and executed those instructions
//   - RAM at 0x8000 was actually written to
func TestIF2CartridgeBootsAndRuns(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cartPath := makeStubCartridge(t)
	if err := h.InsertInterface2Cartridge(cartPath); err != nil {
		t.Fatalf("InsertInterface2Cartridge: %v", err)
	}
	if !h.peripherals.IsInterface2CartridgeInserted() {
		t.Fatal("cartridge not reported as inserted after Insert")
	}
	if got := h.peripherals.Interface2CartridgeName(); got != "stub" {
		t.Errorf("cartridge name = %q, want %q", got, "stub")
	}

	// Sanity check: reads at 0x0000 should now come from the
	// cartridge, not the BASIC ROM. The stub cart starts with
	// 0x21 (LD HL, nn) at offset 0; the 48K BASIC ROM starts
	// with 0xF3 (DI).
	if got := h.Memory(0x0000); got != 0x21 {
		t.Errorf("memory 0x0000 after insert = 0x%02X, want 0x21 (LD HL) — cartridge not paged in", got)
	}

	// A handful of frames is plenty — the stub runs in ~a dozen
	// T-states and then HALTs.
	h.RunFrames(2)

	// Verify the magic byte pattern landed at 0x8000.
	wantBytes := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	for i, want := range wantBytes {
		got := h.Memory(uint16(0x8000 + i))
		if got != want {
			t.Errorf("RAM 0x%04X = 0x%02X, want 0x%02X", 0x8000+i, got, want)
		}
	}

	// Eject the cartridge and verify the BASIC ROM comes back.
	h.RemoveInterface2Cartridge()
	if h.peripherals.IsInterface2CartridgeInserted() {
		t.Error("cartridge still reported as inserted after Remove")
	}
	if got := h.Memory(0x0000); got != 0xF3 {
		t.Errorf("memory 0x0000 after eject = 0x%02X, want 0xF3 (DI) — BASIC ROM not restored", got)
	}
}

// TestIF2RejectsNon48KModel confirms that inserting a cartridge on
// a non-48K machine fails cleanly with an explanatory error, rather
// than corrupting the 128K ROM paging.
func TestIF2RejectsNon48KModel(t *testing.T) {
	h, err := New(roms.Model128K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cartPath := makeStubCartridge(t)
	if err := h.InsertInterface2Cartridge(cartPath); err == nil {
		t.Error("InsertInterface2Cartridge on 128K succeeded; expected error")
	}
	if h.peripherals.IsInterface2CartridgeInserted() {
		t.Error("cartridge reported as inserted despite model rejection")
	}
}

// TestIF2RejectsWrongSize confirms the size-validation error path
// on a non-16KB file. Piggybacks on if2.Load's validation but
// exercises it through the full peripheral manager entry point.
func TestIF2RejectsWrongSize(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bogus := filepath.Join(t.TempDir(), "bogus.rom")
	if err := os.WriteFile(bogus, []byte("not a cartridge"), 0o644); err != nil {
		t.Fatalf("write bogus: %v", err)
	}
	if err := h.InsertInterface2Cartridge(bogus); err == nil {
		t.Error("InsertInterface2Cartridge on 15-byte file succeeded; expected size error")
	}
	if h.peripherals.IsInterface2CartridgeInserted() {
		t.Error("cartridge reported as inserted despite size rejection")
	}
}
