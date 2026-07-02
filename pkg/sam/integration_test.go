package sam

import (
	"crypto/md5"
	"encoding/hex"
	"image/png"
	"os"
	"testing"
)

// screenHash returns a digest of the rendered frame, so a test can detect that
// the display changed (e.g. after typing or running a program).
func screenHash(m *Machine) string {
	img := m.Render()
	sum := md5.Sum(img.Pix)
	return hex.EncodeToString(sum[:])
}

// distinctDisplayBytes counts distinct bytes in the visible display page (a
// proxy for "has the ROM drawn content here").
func distinctDisplayBytes(m *Machine) int {
	seen := map[byte]bool{}
	n := mode34BytesPerLine * samActiveHeight
	for off := 0; off < n; off++ {
		seen[m.Mem.VideoMemByte(off)] = true
	}
	return len(seen)
}

// dumpScreen writes the current frame to a PNG if SAM_RENDER_OUT_DIR is set.
func dumpScreen(m *Machine, name string) {
	dir := os.Getenv("SAM_RENDER_OUT_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(dir + "/" + name + ".png")
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_ = png.Encode(f, m.Render())
}

func runFrames(m *Machine, n int) {
	for i := 0; i < n; i++ {
		m.RunFrame()
	}
}

// tapKey presses a SAM matrix key (row, bit), holds it for a few frames so the
// 50 Hz ROM keyboard scan registers it, then releases it with a gap.
func tapKey(m *Machine, row, bit int) {
	m.Kbd.SetKey(row, bit, true)
	runFrames(m, 10)
	m.Kbd.SetKey(row, bit, false)
	runFrames(m, 10)
}

// TestSAMBootToBasicAndType is the end-to-end test: boot the real ROM to the
// copyright screen, dismiss it to reach SAM BASIC, type a command and confirm
// keyboard input reaches the ROM and the line executes.
func TestSAMBootToBasicAndType(t *testing.T) {
	m, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}

	// 1) Boot to the copyright screen (the SAM waits there for a key).
	runFrames(m, 600)
	if !m.CPU.IFF1 && m.CPU.PC < 0xC000 {
		t.Fatalf("SAM did not reach the ROM main loop (PC=%#04x)", m.CPU.PC)
	}
	copyright := screenHash(m)
	dumpScreen(m, "1_copyright")
	if distinctDisplayBytes(m) < 2 {
		t.Error("copyright screen should have drawn content")
	}

	// 2) Press a key to drop into SAM BASIC; let it draw the prompt.
	tapKey(m, 7, 0) // SPACE
	runFrames(m, 400)
	t.Logf("after dismiss: PC=%#04x mode=%d page=%d distinctBytes=%d",
		m.CPU.PC, m.Mem.ScreenMode(), m.Mem.VisibleScreenPage(), distinctDisplayBytes(m))
	dumpScreen(m, "2_basic")
	basic := screenHash(m)
	if basic == copyright {
		t.Error("pressing a key should leave the copyright screen for SAM BASIC")
	}

	// 3) Type "PRINT 65" (SAM BASIC keywords are typed in full). Confirm the
	// keystrokes reach the running ROM (the display changes).
	for _, k := range []struct{ row, bit int }{
		{5, 0}, {2, 3}, {5, 2}, {7, 3}, {2, 4}, // P R I N T
		{7, 0},         // SPACE
		{4, 4}, {3, 4}, // 6 5
	} {
		tapKey(m, k.row, k.bit)
	}
	dumpScreen(m, "3_typed")
	typed := screenHash(m)
	if typed == basic {
		t.Error("typing should change the display (keyboard not reaching the ROM)")
	}

	// 4) ENTER → the line executes (display changes again).
	tapKey(m, 6, 0)
	runFrames(m, 80)
	dumpScreen(m, "4_executed")
	if screenHash(m) == typed {
		t.Error("pressing ENTER should execute the line (display did not change)")
	}
}

// TestSAMBootsGameDiskIfPresent loads a real SAM disk image (gitignored under
// games/) and drives the BOOT command, asserting the title screen actually
// renders. Skipped when no disk is present, so CI stays green; locally it is
// the end-to-end proof that disk games load, exercising the WD1772 spin-up
// detection and FORCE INTERRUPT status handling a real boot sequence depends
// on.
func TestSAMBootsGameDiskIfPresent(t *testing.T) {
	for _, name := range []string{"ManicMiner.dsk", "Tetris.dsk"} {
		path := "../../games/" + name
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("no game disk at %s (games/ is gitignored): %v", path, err)
		}
		t.Run(name, func(t *testing.T) {
			disk, err := LoadDisk(data)
			if err != nil {
				t.Fatalf("LoadDisk(%s): %v", name, err)
			}
			m, err := NewDefault()
			if err != nil {
				t.Fatal(err)
			}
			m.InsertDisk(0, disk)
			runFrames(m, 600)
			tapKey(m, 7, 0) // SPACE → BASIC
			runFrames(m, 300)
			for _, k := range []struct{ row, bit int }{{7, 4}, {5, 1}, {5, 1}, {2, 4}} { // BOOT
				tapKey(m, k.row, k.bit)
			}
			tapKey(m, 6, 0) // ENTER
			runFrames(m, 1500)
			if db := distinctDisplayBytes(m); db < 30 {
				t.Errorf("%s did not load — display has only %d distinct bytes (an error screen), expected a title screen", name, db)
			}
		})
	}
}

// typeRune injects a typed symbol via the SAM keyboard's typed-character layer
// (TypeRune + the per-frame pulse), holding long enough for the ROM to register.
func typeRune(m *Machine, r rune) {
	m.Kbd.TypeRune(r)
	runFrames(m, samRunePulseFrames+6)
}

// TestSAMTypedSymbolsReachBasic exercises the typed-character layer end to end:
// the double-quote (a SAM-specific key) typed through TypeRune must echo at the
// BASIC prompt, proving symbol input reaches the running ROM.
func TestSAMTypedSymbolsReachBasic(t *testing.T) {
	m, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	runFrames(m, 600)
	tapKey(m, 7, 0) // SPACE → BASIC
	runFrames(m, 300)
	prompt := screenHash(m)

	// A double-quote is not a plain matrix key in our host map — it must come
	// through the typed-character layer.
	typeRune(m, '"')
	dumpScreen(m, "5_quote")
	if screenHash(m) == prompt {
		t.Fatal(`typing '"' did not change the display — typed-character layer not reaching the ROM`)
	}
}
