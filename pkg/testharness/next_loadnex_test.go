package testharness

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/next/nex"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// installFakeDistroForLoad writes a 16K distro ROM so memory.New
// can succeed in tests that need a constructed harness.
//
// MUST be preceded by installtest.RedirectConfig(t) — the
// AssertSandboxed guard fails the test loudly otherwise, since
// without the redirect this would write into the developer's real
// install directory.
func installFakeDistroForLoad(t *testing.T) {
	t.Helper()
	installtest.AssertSandboxed(t)
	dir, err := install.Path()
	if err != nil {
		t.Fatal(err)
	}
	rom := make([]byte, 0x4000)
	if err := os.WriteFile(filepath.Join(dir, install.DistroROM), rom, 0644); err != nil {
		t.Fatal(err)
	}
}

// buildTinyNEX writes a one-bank .NEX file with a known pattern in
// bank 5 and a specific entry PC/SP/border so the test can verify
// LoadNEX places everything correctly.
func buildTinyNEX(t *testing.T) string {
	t.Helper()
	hdr := make([]byte, nex.HeaderSize)
	copy(hdr[0:4], nex.Magic[:])
	copy(hdr[4:8], []byte("V1.2"))
	hdr[11] = 5                                       // border = magenta
	binary.LittleEndian.PutUint16(hdr[12:14], 0xFEDC) // SP
	binary.LittleEndian.PutUint16(hdr[14:16], 0xBEEF) // PC
	binary.LittleEndian.PutUint16(hdr[16:18], 1)      // 1 bank
	hdr[18+5] = 1                                     // bank 5 present
	hdr[139] = 5                                      // entry bank = 5

	bank5 := bytes.Repeat([]byte{0xA5}, nex.BankSize)

	path := filepath.Join(t.TempDir(), "tiny.nex")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bank5); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadNEXAppliesHeaderAndBanks(t *testing.T) {
	installtest.RedirectConfig(t)
	installFakeDistroForLoad(t)

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	nexPath := buildTinyNEX(t)
	if err := h.LoadNEX(nexPath); err != nil {
		t.Fatalf("LoadNEX: %v", err)
	}

	if h.CPU().PC != 0xBEEF {
		t.Errorf("PC = %#x after LoadNEX, want 0xBEEF", h.CPU().PC)
	}
	if h.CPU().SP != 0xFEDC {
		t.Errorf("SP = %#x after LoadNEX, want 0xFEDC", h.CPU().SP)
	}

	// Bank 5 maps at 0x4000–0x7FFF on the 48K-equivalent default
	// layout. Verify the bytes landed.
	for _, off := range []uint16{0x4000, 0x4123, 0x7FFF} {
		if got := h.MemoryBus().Read(off); got != 0xA5 {
			t.Errorf("mem[%#x] = %#x, want 0xA5", off, got)
		}
	}

	// Entry bank 5: the .NEX header asks for bank 5 at 0xC000.
	// On a 128K-style 7FFD write with bits 0-2=5, bank 5 maps at
	// 0xC000. So reads at 0xC000-0xFFFF should also yield 0xA5.
	for _, off := range []uint16{0xC000, 0xCDEF, 0xFFFF} {
		if got := h.MemoryBus().Read(off); got != 0xA5 {
			t.Errorf("entry-bank mem[%#x] = %#x, want 0xA5", off, got)
		}
	}
}

// buildExtendedBankNEX writes a .NEX with one classic bank (5) and
// one extended bank (50, which is well above the 0..7 range that
// classic 7FFD paging can address). Used to verify banks 8+ are
// accepted by the loader now that memory.New allocates 128 banks
// for ModelNext.
func buildExtendedBankNEX(t *testing.T) string {
	t.Helper()
	hdr := make([]byte, nex.HeaderSize)
	copy(hdr[0:4], nex.Magic[:])
	copy(hdr[4:8], []byte("V1.2"))
	hdr[11] = 0                                       // border
	binary.LittleEndian.PutUint16(hdr[12:14], 0xFFFF) // SP
	binary.LittleEndian.PutUint16(hdr[14:16], 0x8000) // PC
	binary.LittleEndian.PutUint16(hdr[16:18], 2)      // 2 banks
	hdr[18+5] = 1                                     // bank 5 present
	hdr[18+50] = 1                                    // bank 50 present
	hdr[139] = 5                                      // entry bank = 5 (classic-addressable)

	bank5 := bytes.Repeat([]byte{0xC5}, nex.BankSize)  // 0xC5 = "bank 5"
	bank50 := bytes.Repeat([]byte{0x32}, nex.BankSize) // 0x32 = 50

	path := filepath.Join(t.TempDir(), "extended.nex")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(hdr); err != nil {
		t.Fatal(err)
	}
	// Banks appear in LoadOrder which puts low banks first then
	// 8..111 ascending. So bank 5 comes before bank 50 in the file.
	if _, err := f.Write(bank5); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bank50); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadNEXAcceptsExtendedBanks verifies .NEX banks 8+ load
// correctly: ModelNext allocates 128 banks (2 MB) and the loader
// writes the full payload. We verify both a classic bank (5) and an
// extended bank (50) land correctly. Bank 50 isn't addressable via
// classic paging — we read it back via the raw GetPage accessor.
func TestLoadNEXAcceptsExtendedBanks(t *testing.T) {
	installtest.RedirectConfig(t)
	installFakeDistroForLoad(t)

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	nexPath := buildExtendedBankNEX(t)
	if err := h.LoadNEX(nexPath); err != nil {
		t.Fatalf("LoadNEX: %v", err)
	}

	// Classic bank 5: addressable at 0x4000 on default layout.
	if got := h.MemoryBus().Read(0x4123); got != 0xC5 {
		t.Errorf("bank 5 via classic paging mem[0x4123] = %#x, want 0xC5", got)
	}

	// Extended bank 50: not addressable via 7FFD. Read directly
	// via GetPage to prove the data landed in the underlying RAM.
	page50 := h.MemoryBus().GetPage(50)
	if page50 == nil {
		t.Fatalf("GetPage(50) returned nil — extended banks not allocated for ModelNext")
	}
	for _, off := range []int{0, 0x1234, 0x3FFF} {
		if page50[off] != 0x32 {
			t.Errorf("bank 50 page[%#x] = %#x, want 0x32", off, page50[off])
		}
	}

	// Sanity: a bank we didn't load must remain at the cold-boot
	// zero-fill — memory.New defaults Next banks to zero (see
	// memory.TestColdRAMZeroByDefault). This verifies the loader
	// didn't accidentally write to unrelated banks.
	page75 := h.MemoryBus().GetPage(75)
	if page75 == nil {
		t.Fatalf("GetPage(75) returned nil — extended banks not allocated for ModelNext")
	}
	for _, off := range []int{0, 0x1234, 0x3FFF} {
		if got := page75[off]; got != 0x00 {
			t.Errorf("unused bank 75 page[%#x] = %#x, want 0x00 (zero-fill)", off, got)
		}
	}
}

// TestExtendedBankExecutableViaMMU verifies banks 8+ are reachable
// from CPU execution, not just allocated. It pokes a tiny program
// (LD A,n; LD (nn),A; HALT)
// into 16K bank 50, configures the 8K MMU so bank 50's lower
// half maps to 0x6000-0x7FFF, runs from there, and verifies the
// program both fetched correctly (LD A took 0xAB) and the store
// landed in classic-paged RAM (bank 5, mapped at 0x4000).
//
// Without the MMU-aware Read path in pkg/memory the CPU would
// fetch zeros from the classic-paged bank that 0x6000 maps to,
// and the LD A,n / LD (nn),A pair would never execute.
func TestExtendedBankExecutableViaMMU(t *testing.T) {
	installtest.RedirectConfig(t)
	installFakeDistroForLoad(t)

	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Tiny program in 16K bank 50 at bank-offset 0x0000 (which is
	// also 8K bank 100 — bank50_lower):
	//   3E AB        LD A, 0xAB
	//   32 23 41     LD (0x4123), A  -- stores into classic-paged RAM
	//   76           HALT
	page50 := h.MemoryBus().GetPage(50)
	if page50 == nil {
		t.Fatalf("bank 50 not allocated — extended banks broken")
	}
	prog := []byte{0x3E, 0xAB, 0x32, 0x23, 0x41, 0x76}
	copy(page50, prog)

	// Set 8K MMU slot 3 (covers 0x6000-0x7FFF) → 8K bank 100
	// (= 16K bank 50 lower half). NextReg 0x53 is slot 3.
	h.MemoryBus().SetMMU(3, 100)

	// Set classic paging so bank 5 sits at 0x4000 (default 48K
	// layout). The LD (0x4123),A targets this bank — we'll read
	// back from bank 5 to confirm the program ran.
	h.MemoryBus().PageMemory(0)

	h.CPU().PC = 0x6000
	h.CPU().SP = 0xFFFF
	// Step until HALT or budget.
	for i := 0; i < 100; i++ {
		h.CPU().StepInstruction()
		if h.CPU().Halted {
			break
		}
	}
	if !h.CPU().Halted {
		t.Fatalf("program did not HALT — PC=%#x A=%#x", h.CPU().PC, h.CPU().A)
	}

	// LD (0x4123),A → bank 5 at offset 0x0123.
	bank5 := h.MemoryBus().GetPage(5)
	if bank5 == nil {
		t.Fatal("bank 5 not allocated")
	}
	if bank5[0x0123] != 0xAB {
		t.Errorf("after MMU-dispatched execution: bank 5 [0x0123] = %#x, want 0xAB", bank5[0x0123])
	}
	// And A should hold 0xAB.
	if h.CPU().A != 0xAB {
		t.Errorf("A = %#x, want 0xAB", h.CPU().A)
	}
}

// TestLoadNEXRejectsNonNextModel verifies the model gate.
func TestLoadNEXRejectsNonNextModel(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	err = h.LoadNEX("/dev/null")
	if err == nil {
		t.Errorf("LoadNEX on a 48K harness should fail")
	}
}

func TestMountSDCardWiresFOpen(t *testing.T) {
	installtest.RedirectConfig(t)
	installFakeDistroForLoad(t)
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}

	// Stage a file the guest can F_OPEN.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "test.dat"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := h.MountSDCard(root); err != nil {
		t.Fatalf("MountSDCard: %v", err)
	}
	// Close the esxDOS handle the F_OPEN below leaves open before root's
	// t.TempDir RemoveAll runs (LIFO) — Windows can't delete an open file.
	t.Cleanup(h.CloseFiles)

	mem := h.MemoryBus()
	cpu := h.CPU()

	// Write the null-terminated path into RAM bank 5 (visible at
	// 0x4000 on the default 48K-equivalent layout for ModelNext).
	const pathAddr = 0x4000
	const returnAddr = 0x4100
	path := "/test.dat"
	for i, b := range []byte(path) {
		mem.Write(pathAddr+uint16(i), b)
	}
	mem.Write(pathAddr+uint16(len(path)), 0)
	// F_OPEN = 0x9A inline at the return-after-RST address. Park
	// a NOP at returnAddr+1: after the esxDOS hook dispatches it
	// updates PC to returnAddr+1 and the same StepInstruction()
	// call continues into a normal M1 fetch there. Without an
	// explicit NOP that fetch lands on the cold-RAM fill ($FF for
	// ModelNext — see memory.New) which decodes as RST $38 and
	// trashes the SP/PC assertions below.
	mem.Write(returnAddr, 0x9A)
	mem.Write(returnAddr+1, 0x00)

	// Simulate the state immediately after a RST 8 fired: PC at
	// the trap (0x0008), SP holding the return address (which
	// points at the inline API byte).
	cpu.SP = 0xFEFE
	mem.Write(cpu.SP, byte(returnAddr&0xFF))
	mem.Write(cpu.SP+1, byte(returnAddr>>8))
	cpu.SetHL(pathAddr)
	cpu.B = 0x01 // read access
	cpu.F &^= 0x01
	cpu.PC = 0x0008

	// One instruction is enough: the esxDOS pre-fetch hook fires
	// at PC=0x0008, performs the dispatch (open file, set A,
	// advance PC to returnAddr+1, pop stack), then the CPU
	// fetches from the new PC.
	cpu.StepInstruction()

	if cpu.F&0x01 != 0 {
		t.Fatalf("F_OPEN: CF set, error in A=%#x", cpu.A)
	}
	if cpu.A == 0 {
		t.Errorf("F_OPEN: returned handle 0, expected a valid handle")
	}
	if cpu.PC < returnAddr+1 {
		t.Errorf("PC = %#x, want >= %#x (past the API parameter byte)", cpu.PC, returnAddr+1)
	}
	if cpu.SP != 0xFF00 {
		t.Errorf("SP = %#x, want 0xFF00 (stack popped past return addr)", cpu.SP)
	}
}

func TestMountSDCardRejectsNonNextModel(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.MountSDCard(t.TempDir()); err == nil {
		t.Errorf("MountSDCard on a 48K harness should fail")
	}
}
