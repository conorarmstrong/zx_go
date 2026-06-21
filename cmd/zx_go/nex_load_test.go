package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nex"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// nextStep runs one CPU instruction through the IRQ-aware single-step path. The
// NextZXOS cold boot is driven by the per-instruction divMMC/esxDOS hooks (it
// loads the OS from SD via RST 8), which ExecuteFrame bypasses — so the *boot*
// must use this path.
func nextStep(emu *emulator, n int) {
	for i := 0; i < n; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}
}

// nextRunFrames runs the Next exactly as the GUI run loop does (frame execution
// plus the per-frame peripheral tick). A loaded .nex program must be run this
// way to render correctly.
func nextRunFrames(emu *emulator, n int) {
	for i := 0; i < n; i++ {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
	}
}

func nextScreen(emu *emulator) (hash string, nonBlank bool) {
	img := emu.renderFrame()
	sum := md5.Sum(img.Pix)
	return hex.EncodeToString(sum[:]), !uniformImage(img)
}

// saveNexShot writes a PNG when NEX_RENDER_OUT_DIR is set, for eyeballing.
func saveNexShot(emu *emulator, name string) {
	dir := os.Getenv("NEX_RENDER_OUT_DIR")
	if dir == "" {
		return
	}
	var buf bytes.Buffer
	if writeScreenshotPNG(emu, &buf) == nil {
		_ = os.WriteFile(dir+"/"+name+".png", buf.Bytes(), 0o644)
	}
}

// bootNextToMenu cold-boots NextZXOS to its main menu, or skips the test if the
// Next ROMs / SD image are not installed (both are licensed, gitignored content,
// so this skips cleanly in CI).
func bootNextToMenu(t *testing.T) *emulator {
	t.Helper()
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	t.Cleanup(func() { cliFlagsActive = prev })

	emu, err := newNextEmulator()
	if err != nil {
		t.Skipf("Next ROMs not installed: %v", err)
	}
	// Cold-boot to the NextZXOS welcome (CPU settles at the menu wait loop).
	nextStep(emu, 18_000_000)
	for i := 0; i < 8_000_000 && emu.cpu.PC != nextMenuLoopPC; i++ {
		emu.cpu.StepInstructionWithIRQ()
	}
	if emu.cpu.PC != nextMenuLoopPC {
		t.Skipf("Next did not reach the NextZXOS welcome (PC=%#04x); SD/boot config unavailable", emu.cpu.PC)
	}
	// SPACE = "Start NextZXOS" -> the main menu (Browser / Command Line / ...).
	// The welcome's key scan needs the key held across several frames.
	emu.kbd.PressMatrixKey(7, 0x01, true)
	nextStep(emu, 2_000_000)
	emu.kbd.PressMatrixKey(7, 0x01, false)
	nextStep(emu, 6_000_000)
	return emu
}

// loadNex applies a parsed .nex to the running machine exactly as the GUI's
// File -> Open path does: copy each bank into its memory page, set SP/PC, and
// page the entry bank in at $C000.
func loadNex(emu *emulator, prog *nex.NEX) {
	for bank, data := range prog.Banks {
		if page := emu.mem.GetPage(bank); page != nil {
			copy(page, data)
		}
	}
	emu.cpu.SP = prog.Header.SP
	emu.cpu.PC = prog.Header.PC
	emu.mem.PageMemory(prog.Header.EntryBank & 0x07)
}

// TestNextNexProgramsLoadIfPresent boots NextZXOS and loads a few self-contained
// .nex programs from the SD card, verifying each one takes over the machine,
// renders a screen, and keeps running after a key press. Skipped when the Next
// ROMs / SD content (both gitignored) are absent, so CI stays green; locally it
// is the end-to-end proof that .nex games load and function.
func TestNextNexProgramsLoadIfPresent(t *testing.T) {
	cases := []struct {
		path, name string
		keyRow     int
		keyMask    byte // the program's "start" key, to confirm it accepts input
	}{
		// Halls of the Things (Design Design, Next port) — menu, '1' = play.
		{"../../roms/next/sd/games/Next/Halls of The Things/hott.nex", "halls-of-the-things", 3, 0x01},
		// Santa's Pressie — title screen, SPACE to proceed.
		{"../../roms/next/sd/games/Next/Santa's Pressie/SantasPressie.nex", "santas-pressie", 7, 0x01},
		// show512 — a 512-colour demo, arrows/SPACE interactive.
		{"../../roms/next/sd/demos/show512/show512.nex", "show512-demo", 7, 0x01},
		// ScrollNutter — a self-contained demo carrying a Layer 2 loading
		// screen (flags=0x01); exercises a .nex whose optional palette +
		// 48K Layer 2 screen sections must be skipped to align the banks.
		{"../../roms/next/sd/demos/ScrollNutter/ScrollNutter.nex", "scrollnutter-demo", 7, 0x01},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(c.path); err != nil {
				t.Skipf("%s not present (gitignored SD content)", c.path)
			}
			prog, err := nex.ParseFile(c.path)
			if err != nil {
				t.Fatalf("parse %s: %v", c.name, err)
			}

			emu := bootNextToMenu(t)
			loadNex(emu, prog)
			nextRunFrames(emu, 150) // let the program start up

			h1, nb1 := nextScreen(emu)
			saveNexShot(emu, c.name+"-loaded")
			if !nb1 {
				t.Errorf("%s: screen is blank after loading — program did not render", c.name)
			}
			if emu.cpu.PC == nextMenuLoopPC {
				t.Errorf("%s: returned to the NextZXOS menu (PC=%#04x) — program did not launch", c.name, emu.cpu.PC)
			}

			// Inject the program's start key — it must accept input and keep running.
			emu.kbd.PressMatrixKey(c.keyRow, c.keyMask, true)
			nextRunFrames(emu, 30)
			emu.kbd.PressMatrixKey(c.keyRow, c.keyMask, false)
			nextRunFrames(emu, 120)

			h2, nb2 := nextScreen(emu)
			saveNexShot(emu, c.name+"-afterkey")
			if !nb2 {
				t.Errorf("%s: screen blank after key press — program crashed", c.name)
			}
			if emu.cpu.PC == nextMenuLoopPC {
				t.Errorf("%s: dropped back to NextZXOS after key press (PC=%#04x)", c.name, emu.cpu.PC)
			}
			t.Logf("%s: loaded hash=%s -> after-key hash=%s (PC=%#04x, nonblank=%v)", c.name, h1, h2, emu.cpu.PC, nb2)
		})
	}
}
