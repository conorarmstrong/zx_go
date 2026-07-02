package testharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// buildTestMGT creates a standard 2-side, 80-cylinder, 10-sector,
// 512-byte MGT disk image. Each sector's first 3 bytes are (cylinder,
// head, sector_R), followed by 0xCC fill. Returns the file path.
func buildTestMGT(t *testing.T) string {
	t.Helper()
	const (
		sides   = 2
		cyls    = 80
		sectors = 10
		secSize = 512
	)
	img := make([]byte, sides*cyls*sectors*secSize)
	for logical := 0; logical < sides*cyls; logical++ {
		cyl := logical / sides
		head := logical % sides
		for s := 0; s < sectors; s++ {
			off := (logical*sectors + s) * secSize
			img[off] = byte(cyl)
			img[off+1] = byte(head)
			img[off+2] = byte(s + 1)
			for i := 3; i < secSize; i++ {
				img[off+i] = 0xCC
			}
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mgt")
	if err := os.WriteFile(path, img, 0644); err != nil {
		t.Fatalf("write test.mgt: %v", err)
	}
	return path
}

// TestDiscipleSectorReadViaZ80 boots a 48K Spectrum, enables the
// DISCiPLE, loads a test MGT disk, then runs a small Z80 program
// that reads sector 3 from track 5 side 0 via FDC port I/O and
// stores the result at 0x9000. This exercises the full path:
// Z80 IN/OUT → ULA → PeripheralManager → Disciple → plus3fdc.Track.
func TestDiscipleSectorReadViaZ80(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableDisciple(); err != nil {
		t.Fatalf("EnableDisciple: %v", err)
	}
	diskPath := buildTestMGT(t)
	if err := h.LoadDiscipleDisk(0, diskPath); err != nil {
		t.Fatalf("LoadDiscipleDisk: %v", err)
	}

	// Z80 program at 0x8000 using the DISCiPLE's FDC port addresses:
	//   0x1F control, 0xDB FDC data, 0x1B FDC cmd, 0x9B FDC sector
	code := []byte{
		0x3E, 0x01, // LD A, 0x01
		0xD3, 0x1F, // OUT (0x1F), A   ; control: drive 0, side 0
		0x3E, 0x05, // LD A, 0x05
		0xD3, 0xDB, // OUT (0xDB), A   ; FDC data reg = 5 (target track)
		0x3E, 0x10, // LD A, 0x10
		0xD3, 0x1B, // OUT (0x1B), A   ; Seek command
		0x3E, 0x03, // LD A, 0x03
		0xD3, 0x9B, // OUT (0x9B), A   ; FDC sector reg = 3
		0x3E, 0x80, // LD A, 0x80
		0xD3, 0x1B, // OUT (0x1B), A   ; Read Sector command
		0x21, 0x00, 0x90, // LD HL, 0x9000
		0x01, 0x00, 0x02, // LD BC, 0x0200 (512)
		// .loop:
		0xDB, 0xDB, // IN A, (0xDB)   ; FDC data
		0x77,       // LD (HL), A
		0x23,       // INC HL
		0x0B,       // DEC BC
		0x78,       // LD A, B
		0xB1,       // OR C
		0x20, 0xF7, // JR NZ, .loop
		0x76, // HALT
	}

	// Inject the program at 0x8000.
	for i, b := range code {
		h.WriteMemory(uint16(0x8000+i), b)
	}

	// Point PC at our program and run.
	h.CPU().PC = 0x8000
	h.RunFrames(10) // plenty of time for ~40 instructions

	// Verify: sector 3 on track 5, side 0, should have:
	//   byte 0 = 5 (cylinder)
	//   byte 1 = 0 (head)
	//   byte 2 = 3 (sector R)
	//   byte 3+ = 0xCC
	if h.Memory(0x9000) != 5 {
		t.Errorf("sector[0] = %d, want 5 (cylinder)", h.Memory(0x9000))
	}
	if h.Memory(0x9001) != 0 {
		t.Errorf("sector[1] = %d, want 0 (head)", h.Memory(0x9001))
	}
	if h.Memory(0x9002) != 3 {
		t.Errorf("sector[2] = %d, want 3 (sector R)", h.Memory(0x9002))
	}
	if h.Memory(0x9003) != 0xCC {
		t.Errorf("sector[3] = 0x%02X, want 0xCC (fill)", h.Memory(0x9003))
	}
	// Spot-check last byte of sector (offset 511).
	if h.Memory(0x91FF) != 0xCC {
		t.Errorf("sector[511] = 0x%02X, want 0xCC", h.Memory(0x91FF))
	}
	// Verify bytes past the sector are untouched (should be 0x00 or
	// whatever RAM was initialised to — certainly not 0xCC).
	if h.Memory(0x9200) == 0xCC {
		t.Error("byte past sector end is 0xCC — transfer overran")
	}
}

// TestDiscipleColdBootAndKeypress simulates a power-on with the
// DISCiPLE installed. GDOS runs its boot code, pages out, the
// Spectrum ROM boots to BASIC, and a keypress is echoed on screen.
func TestDiscipleColdBootAndKeypress(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableDiscipleColdBoot(); err != nil {
		t.Fatalf("EnableDiscipleColdBoot: %v", err)
	}
	diskPath := buildTestMGT(t)
	if err := h.LoadDiscipleDisk(0, diskPath); err != nil {
		t.Fatalf("LoadDiscipleDisk: %v", err)
	}

	h.RunFrames(500) // GDOS boot + Spectrum boot + settle

	h.TapKey(fyne.KeyP)
	h.RunFrames(20)

	text := h.ScreenText()
	if !strings.Contains(text, "PRINT") {
		t.Errorf("BASIC not responding after DISCiPLE cold boot. Screen:\n%s", text)
	}
}

// TestDiscipleAutoPageOnNMI verifies that the DISCiPLE ROM pages in
// when the CPU fetches from 0x0066 (the NMI vector). We inject a
// sentinel instruction into the GDOS ROM at 0x0066 that writes a
// known value to RAM, proving the NMI handler ran from GDOS.
func TestDiscipleAutoPageOnNMI(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableDisciple(); err != nil {
		t.Fatalf("EnableDisciple: %v", err)
	}

	disc := h.Peripherals().GetDisciple()

	// Patch the GDOS ROM at 0x0066 with a sentinel. NMI auto-pages
	// without memswap, so the CPU fetches from ROM.
	rom := disc.GetROM()
	rom[0x0066] = 0x3E // LD A, 0x42
	rom[0x0067] = 0x42
	rom[0x0068] = 0x32 // LD (0x9000), A
	rom[0x0069] = 0x00
	rom[0x006A] = 0x90
	rom[0x006B] = 0x76 // HALT

	// Verify sentinel location is clear.
	if h.Memory(0x9000) == 0x42 {
		t.Fatal("sentinel should not be set before NMI")
	}

	h.NMI()
	h.RunFrames(5)

	if h.Memory(0x9000) != 0x42 {
		t.Errorf("sentinel at 0x9000 = 0x%02X, want 0x42 (GDOS NMI handler ran)", h.Memory(0x9000))
	}
}

// TestDisciplePageViaPortBB verifies paging through the port 0xBB
// mechanism — the way GDOS's RAM hook actually triggers page-in.
func TestDisciplePageViaPortBB(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableDisciple(); err != nil {
		t.Fatalf("EnableDisciple: %v", err)
	}

	disc := h.Peripherals().GetDisciple()

	// IN A, (0xBB) pages in; OUT (0xBB), A pages out.
	// Inject: IN A,(0xBB); HALT
	h.WriteMemory(0x8000, 0xDB) // IN A, (0xBB)
	h.WriteMemory(0x8001, 0xBB)
	h.WriteMemory(0x8002, 0x76) // HALT

	h.CPU().PC = 0x8000
	h.RunFrames(2)

	if !disc.IsROMPaged() {
		t.Error("should be paged in after IN A,(0xBB)")
	}
}

// TestDiscipleROMPagingViaZ80 verifies that writing to the control
// register from Z80 code pages the DISCiPLE memory in and out, and
// that the memory contents change accordingly.
func TestDiscipleROMPagingViaZ80(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := h.EnableDisciple(); err != nil {
		t.Fatalf("EnableDisciple: %v", err)
	}

	disc := h.Peripherals().GetDisciple()
	gdosROM := disc.GetROM()

	// Pre-seed DISCiPLE RAM byte 0 so we can verify it's visible.
	disc.GetRAM()[0] = 0xAB

	// Record the normal 48K ROM byte at 0x0000.
	normalROM0 := h.Memory(0x0000)

	// Z80 program at 0x8000 — uses port 0xBB to page in/out:
	//   IN A, (0xBB)       ; page in (read = page in)
	//   LD A, (0x0000)     ; read from DISCiPLE ROM
	//   LD (0x9000), A
	//   LD A, (0x2000)     ; read from DISCiPLE RAM
	//   LD (0x9001), A
	//   OUT (0xBB), A      ; page out (write = page out)
	//   LD A, (0x0000)     ; read from 48K ROM
	//   LD (0x9002), A
	//   JR $
	code := []byte{
		0xDB, 0xBB, // IN A, (0xBB)     ; page in
		0x3A, 0x00, 0x00, // LD A, (0x0000)
		0x32, 0x00, 0x90, // LD (0x9000), A
		0x3A, 0x00, 0x20, // LD A, (0x2000)
		0x32, 0x01, 0x90, // LD (0x9001), A
		0xD3, 0xBB, // OUT (0xBB), A    ; page out
		0x3A, 0x00, 0x00, // LD A, (0x0000)
		0x32, 0x02, 0x90, // LD (0x9002), A
		0x18, 0xFE, // JR $
	}
	for i, b := range code {
		h.WriteMemory(uint16(0x8000+i), b)
	}

	h.CPU().PC = 0x8000
	h.RunFrames(5)

	// 0x9000 should contain the GDOS ROM byte at 0x0000 (paged in).
	val := h.Memory(0x9000)
	if val != gdosROM[0] {
		t.Errorf("paged-in read of 0x0000 = 0x%02X, want 0x%02X (GDOS ROM)", val, gdosROM[0])
	}

	// 0x9001 should contain the DISCiPLE RAM byte we seeded (0xAB).
	ramVal := h.Memory(0x9001)
	if ramVal != 0xAB {
		t.Errorf("paged-in read of 0x2000 = 0x%02X, want 0xAB (DISCiPLE RAM)", ramVal)
	}

	// 0x9002 should contain the normal 48K ROM byte (paged out).
	val = h.Memory(0x9002)
	if val != normalROM0 {
		t.Errorf("unpaged read of 0x0000 = 0x%02X, want 0x%02X (48K ROM)", val, normalROM0)
	}
}
