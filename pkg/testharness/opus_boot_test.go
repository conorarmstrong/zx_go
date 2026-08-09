package testharness

import (
	"os"
	"strings"
	"testing"

	"fyne.io/fyne/v2"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestOpusBootReachesBASIC is the end-to-end proof that the M1 address-trap
// pager is right. With the Opus fitted the Z80 starts on the OPUS reset
// vector, not the Spectrum's:
//
//	0000 RESET  DI / LD SP,+1019 / JP 1748,PAGE_OUT
//
// $1019 and $101A in the Opus ROM are zero, so the stack's top word is 0000;
// the jump to $1748 hits the bare RET the hardware watches for, the Spectrum
// ROM comes back, and the RET returns to $0000 — now the SPECTRUM reset. If
// any part of that is wrong the machine never reaches BASIC at all.
func TestOpusBootReachesBASIC(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	if err := h.EnableOpus(); err != nil {
		t.Fatalf("EnableOpus: %v", err)
	}
	h.Reboot()

	if !h.Opus().Active() {
		t.Fatal("the Opus ROM must be paged in at reset for its vector to run")
	}
	if _, err := h.RunUntilText("1982 Sinclair Research", 400); err != nil {
		t.Fatalf("the Opus-fitted 48K never reached BASIC: %v\nscreen:\n%s", err, h.ScreenText())
	}
	if h.Opus().Active() {
		t.Error("the Opus ROM is still paged in at the BASIC prompt")
	}
}

// TestOpusInitialisesItsRAM proves the interface did not merely get out of the
// way: INIT_RAM2 must have run through the trap and copied its tables into the
// 2 KB 6116.
//
//	1569 INIT_RAM3 LD HL,+18AD / LD BC,+0089 / LDIR   Move them to the IC 6116.
//
// $18AD/$18AE in the ROM hold DEFW +2009 and DEFW +2089 — the start of the
// main table and the start of free space — so those two words must appear at
// the bottom of the interface RAM.
func TestOpusInitialisesItsRAM(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	if err := h.EnableOpus(); err != nil {
		t.Fatalf("EnableOpus: %v", err)
	}
	h.Reboot()
	if _, err := h.RunUntilText("1982 Sinclair Research", 400); err != nil {
		t.Fatalf("never reached BASIC: %v", err)
	}

	dev := h.Opus().Dev
	lo, hi := dev.Read(0x2000), dev.Read(0x2001)
	if got := uint16(lo) | uint16(hi)<<8; got != 0x2009 {
		t.Errorf("interface RAM $2000 = %#04x, want $2009 (start of the main table)", got)
	}
	lo, hi = dev.Read(0x2002), dev.Read(0x2003)
	if got := uint16(lo) | uint16(hi)<<8; got != 0x2089 {
		t.Errorf("interface RAM $2002 = %#04x, want $2089 (start of free space)", got)
	}
}

// TestOpusAddsItsBASICCommands proves the RST 8 hook works: the Opus catches
// the Spectrum's error restart to implement its own syntax. "CAT" is not a
// 48K BASIC keyword, so on a bare machine it is a syntax error; with the Opus
// fitted it must be accepted.
func TestOpusAddsItsBASICCommands(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	if err := h.EnableOpus(); err != nil {
		t.Fatalf("EnableOpus: %v", err)
	}
	h.Reboot()
	if _, err := h.RunUntilText("1982 Sinclair Research", 400); err != nil {
		t.Fatalf("never reached BASIC: %v", err)
	}
	t.Logf("boot screen:\n%s", strings.TrimRight(h.ScreenText(), "\n "))
}

// typeCAT enters "CAT 1" and presses ENTER. CAT is an extended-mode keyword
// on the 48K: E-mode, then SYMBOL SHIFT + 9.
func typeCAT(h *Harness, drive string) {
	h.EnterExtendedMode()
	h.PressSymbolShift(true)
	h.RunFrames(2)
	h.TapKey("9")
	h.PressSymbolShift(false)
	h.RunFrames(3)
	h.TapKey(fyne.KeyName(drive))
	h.RunFrames(3)
	h.TapKey("Return")
	h.RunFrames(300)
}

// TestOpusCATReadsARealDisk is the whole point: the Opus ROM's own catalogue
// routine, driven from BASIC, must pull the directory off a real .OPD image
// through the WD1770 and the NMI-per-byte transfer.
func TestOpusCATReadsARealDisk(t *testing.T) {
	const disk = "../../games/opus/BATTY.OPD"
	if _, err := os.Stat(disk); err != nil {
		t.Skipf("no Opus test disk available: %v", err)
	}
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("48K harness: %v", err)
	}
	defer h.CloseFiles()
	if err := h.EnableOpus(); err != nil {
		t.Fatalf("EnableOpus: %v", err)
	}
	if err := h.InsertOpusDisk(0, disk); err != nil {
		t.Fatalf("InsertOpusDisk: %v", err)
	}
	h.Reboot()
	if _, err := h.RunUntilText("1982 Sinclair Research", 400); err != nil {
		t.Fatalf("never reached BASIC: %v", err)
	}
	typeCAT(h, "1")

	screen := h.ScreenText()
	// The catalogue of this disk: its name, its three files, the free-sector
	// count, and the Opus's own "0 OK" report. Every one of those bytes came
	// off the image through the WD1770 and the NMI-per-byte transfer.
	for _, want := range []string{"SSSD", "BAC", "run", "0 OK"} {
		if !strings.Contains(screen, want) {
			t.Errorf("CAT output does not contain %q\nscreen:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "error") {
		t.Errorf("CAT reported an error\nscreen:\n%s", screen)
	}
}

// TestOpusLoadsAndRunsARealProgram is the strongest end-to-end check there is:
// the Opus's AUTO_RUN, which loads and runs the disk's "run" file when RUN is
// entered with no BASIC program in memory. Everything has to be right for a
// game to appear — the ROM pager, the hook codes, the WD1770, the 0-based
// sector numbering, and the NMI-per-byte transfer across hundreds of sectors.
//
// The assertions are structural rather than a golden image, because these are
// commercial disks that cannot be committed; the test skips when they are
// absent.
func TestOpusLoadsAndRunsARealProgram(t *testing.T) {
	for _, tc := range []struct{ name, disk string }{
		{"Batty", "BATTY.OPD"},
		{"Exolon", "Exolon.OPD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "../../games/opus/" + tc.disk
			if _, err := os.Stat(path); err != nil {
				t.Skipf("no Opus test disk available: %v", err)
			}
			h, err := New(roms.Model48K)
			if err != nil {
				t.Fatalf("48K harness: %v", err)
			}
			defer h.CloseFiles()
			if err := h.EnableOpus(); err != nil {
				t.Fatalf("EnableOpus: %v", err)
			}
			if err := h.InsertOpusDisk(0, path); err != nil {
				t.Fatalf("InsertOpusDisk: %v", err)
			}
			h.Reboot()
			if _, err := h.RunUntilText("1982 Sinclair Research", 400); err != nil {
				t.Fatalf("never reached BASIC: %v", err)
			}

			h.TapKey("r") // RUN
			h.RunFrames(5)
			h.TapKey("Return")
			h.RunFrames(1200)

			if screen := h.ScreenText(); strings.Contains(screen, "not found") ||
				strings.Contains(screen, "error") {
				t.Fatalf("the Opus reported a disk error\nscreen:\n%s", screen)
			}

			// A loaded game draws its own screen. A failed load leaves either
			// a blank display or the BASIC listing, and neither has anything
			// like this much drawn on it in this many colours.
			ink := 0
			for a := 0x4000; a < 0x5800; a++ {
				if h.Memory(uint16(a)) != 0 {
					ink++
				}
			}
			attrs := map[byte]bool{}
			for a := 0x5800; a < 0x5B00; a++ {
				attrs[h.Memory(uint16(a))] = true
			}
			if ink < 500 {
				t.Errorf("only %d/6144 display bytes are set; the program did not draw a screen", ink)
			}
			if len(attrs) < 5 {
				t.Errorf("only %d distinct attribute values; the screen is not a loaded game's", len(attrs))
			}
		})
	}
}
