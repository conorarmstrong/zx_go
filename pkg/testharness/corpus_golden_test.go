package testharness

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// updateCorpusGolden regenerates the golden PNGs instead of asserting
// against them: `go test ./pkg/testharness -run TestCorpus -update`.
var updateCorpusGolden = flag.Bool("update", false, "regenerate corpus golden frames")

type loadKind int

const (
	loadNEX      loadKind = iota // Next .nex via the ROM-independent LoadNEX
	loadSnapshot                 // .sna/.z80 via LoadSnapshot
	loadTAP                      // .tap: boot to 48K BASIC, LoadTAP + type LOAD""
)

// corpusProgram is one vendored, redistributable program booted as a
// faithfulness regression. See testdata/corpus/CORPUS.md for provenance and
// per-file licenses. Nothing here uses a proprietary OS: Next demos load
// through the emulator's own ROM-independent .nex loader, and the classic
// conformance snapshots run on the 48K model with the emulator's embedded
// (redistributable) 48K ROM.
type corpusProgram struct {
	name       string             // golden basename (testdata/corpus/golden/<name>.png)
	file       string             // binary under testdata/corpus/bin/
	model      roms.SpectrumModel // machine to boot
	kind       loadKind           // how to load file
	frames     int                // frames to run before capturing the screen
	minColours int                // liveness floor — catches a blanked/garbage frame
	// Optional narrow frame-INT pulse override (assert tstate + pulse width).
	// When intPulse > 0 the CPU is switched from the legacy "held-INT whole
	// frame" model to a fixed-width pulse — the faithful behaviour the
	// 128K/+3/Next use — before the program runs. Zero leaves the default.
	intAssert, intPulse uint64
}

var corpusPrograms = []corpusProgram{
	// Next hardware demos — rendered from Next hardware alone (Layer 2,
	// tilemap, hardware sprites), loaded via the ROM-independent .nex loader.
	{name: "layer2_tilemap", file: "zxnext_layer2_tilemap.nex", model: roms.ModelNext, kind: loadNEX, frames: 250, minColours: 12},
	{name: "tilemap", file: "zxnext_tilemap.nex", model: roms.ModelNext, kind: loadNEX, frames: 250, minColours: 6},
	{name: "specbong", file: "SpecBong.nex", model: roms.ModelNext, kind: loadNEX, frames: 250, minColours: 5},

	// Classic Z80/ULA conformance tests (MrKWatkins), on the 48K model. Each
	// draws a pass/fail result screen — a genuine hardware oracle. The golden
	// captures our CURRENT output; see CORPUS.md for which currently show
	// errors (int_skip, z80bltst) versus pass (ccffrm, DIHalt).
	{name: "mrk_z80bltst", file: "z80bltst.sna", model: roms.Model48K, kind: loadSnapshot, frames: 200, minColours: 4},
	{name: "mrk_ccffrm", file: "ccffrm.sna", model: roms.Model48K, kind: loadSnapshot, frames: 200, minColours: 4},
	{name: "mrk_dihalt", file: "DIHalt.sna", model: roms.Model48K, kind: loadSnapshot, frames: 200, minColours: 3},
	{name: "mrk_int_skip", file: "int_skip.sna", model: roms.Model48K, kind: loadSnapshot, frames: 200, minColours: 2},
	{name: "mrk_ulavssjs", file: "ULAvsSJS.sna", model: roms.Model48K, kind: loadSnapshot, frames: 200, minColours: 3},

	// Same int_skip test, but with a faithful ~32T narrow /INT pulse (the
	// timing the 128K/+3/Next use) instead of the 48K legacy held-INT
	// approximation. Here the DD/FD prefix blocks correctly INHIBIT interrupt
	// acceptance (result "0 !OK inhibits ISR"), where mrk_int_skip on the
	// held-INT model shows "43 !ERR! allows ISR". This golden is the
	// integration guard for the pkg/z80 frameIntPulse window fix: reverting
	// that fix flips these blocks back to the !ERR! state. assert=58/pulse=32
	// are the 48K machine's own narrow-pulse constants (next.FrameIntTiming).
	{name: "mrk_int_skip_narrowint", file: "int_skip.sna", model: roms.Model48K, kind: loadSnapshot, frames: 200, minColours: 2, intAssert: 58, intPulse: 32},

	// zxnDMA conformance (MrKWatkins ZilogDMA), tape-loaded onto the Next. The
	// Next has the zxnDMA the test drives; it boots to 48K BASIC on the
	// embedded 48K ROM (no proprietary NextZXOS), then LOAD"" pulls the tape in
	// via the LD-BYTES fast-load trap. Renders the A->B / B->A transfer grids +
	// timing rows on the real DMA engine. Output is deterministic (verified
	// stable across run lengths).
	{name: "mrk_zilogdma", file: "zilogdma.tap", model: roms.ModelNext, kind: loadTAP, frames: 600, minColours: 6},
}

// TestCorpusGoldenFrames boots each vendored program headless and asserts its
// rendered frame is pixel-identical to a committed golden. A divergence means
// the emulator's output changed — the whole point of the suite. The liveness
// floor separately guards against a golden that merely captured a blank or
// garbage screen (so a regression to "renders nothing" can't pass by matching
// an equally-blank golden).
func TestCorpusGoldenFrames(t *testing.T) {
	for _, p := range corpusPrograms {
		p := p
		t.Run(p.name, func(t *testing.T) {
			// The Next demos need a sandboxed install dir with the open
			// bottom ROM in place before New(); the 48K conformance tests
			// use the model's own embedded ROM and need no setup.
			if p.model == roms.ModelNext {
				installtest.RedirectConfig(t)
				installOpenBottomROM(t)
			}

			h, err := New(p.model)
			if err != nil {
				t.Fatalf("New(%v): %v", p.model, err)
			}
			binPath := filepath.Join("testdata", "corpus", "bin", p.file)
			switch p.kind {
			case loadNEX:
				if err := h.LoadNEX(binPath); err != nil {
					t.Fatalf("LoadNEX(%s): %v", p.file, err)
				}
			case loadSnapshot:
				if err := h.LoadSnapshot(binPath); err != nil {
					t.Fatalf("LoadSnapshot(%s): %v", p.file, err)
				}
			case loadTAP:
				h.RunFrames(200) // boot to the 48K BASIC prompt
				if err := h.LoadTAP(binPath); err != nil {
					t.Fatalf("LoadTAP(%s): %v", p.file, err)
				}
				typeLoadCommand(h) // LOAD"" — the trap injects each block
			}
			if p.intPulse > 0 {
				h.CPU().IntAssertTstate = p.intAssert
				h.CPU().IntPulseTstates = p.intPulse
			}
			h.RunFrames(p.frames)
			got := h.ScreenImage()

			if n := distinctColours(got); n < p.minColours {
				t.Fatalf("%s: only %d distinct colours after %d frames — program not rendering (want >= %d)",
					p.name, n, p.frames, p.minColours)
			}

			goldenPath := filepath.Join("testdata", "corpus", "golden", p.name+".png")
			if *updateCorpusGolden {
				writePNG(t, goldenPath, got)
				t.Logf("%s: wrote golden %s (%dx%d)", p.name, goldenPath,
					got.Bounds().Dx(), got.Bounds().Dy())
				return
			}

			want := readPNG(t, goldenPath)
			if !imagesEqual(want, got) {
				failPath := filepath.Join(t.TempDir(), p.name+"_got.png")
				writePNG(t, failPath, got)
				t.Errorf("%s: frame differs from golden %s (actual dumped to %s); "+
					"if the change is intended, regenerate with -update", p.name, goldenPath, failPath)
			}
		})
	}
}

// installOpenBottomROM places the emulator's embedded original 48K Sinclair
// ROM into the bottom-16K slot. It stands in for NextZXOS so the corpus stays
// free of proprietary OS files: the self-contained programs here render purely
// from Next hardware (verified identical output with a zero stub), and the 48K
// ROM — redistributable under Amstrad's permission and already vendored in the
// tree — supplies a working IM1/RST environment for any program that needs one.
// Requires installtest.RedirectConfig(t) first (sandbox guard).
func installOpenBottomROM(t *testing.T) {
	t.Helper()
	installtest.AssertSandboxed(t)
	rom, err := roms.ReadEmbeddedROM("48.rom")
	if err != nil {
		t.Fatalf("read embedded 48K ROM: %v", err)
	}
	dir, err := install.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, install.DistroROM), rom, 0644); err != nil {
		t.Fatal(err)
	}
}

// typeLoadCommand types `LOAD""` + ENTER at the 48K BASIC prompt (K cursor):
// J = the LOAD keyword, SymbolShift+P = ", twice, then ENTER. Each key is held
// a few frames and released with a gap so the ROM's ~50 Hz keyboard scan
// registers distinct presses (the two quotes especially need the gap).
func typeLoadCommand(h *Harness) {
	tapKey := func(k fyne.KeyName) {
		h.PressKey(k)
		h.RunFrames(4)
		h.ReleaseKey(k)
		h.RunFrames(8)
	}
	tapKey(fyne.KeyJ) // LOAD
	h.PressSymbolShift(true)
	h.RunFrames(2)
	tapKey(fyne.KeyP) // "
	tapKey(fyne.KeyP) // "
	h.PressSymbolShift(false)
	h.RunFrames(4)
	tapKey(fyne.KeyReturn)
}

func distinctColours(img *image.RGBA) int {
	seen := make(map[uint32]struct{})
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			key := (r>>8)<<24 | (g>>8)<<16 | (bl>>8)<<8 | (a >> 8)
			seen[key] = struct{}{}
		}
	}
	return len(seen)
}

func imagesEqual(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	return bytes.Equal(a.Pix, b.Pix)
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func readPNG(t *testing.T, path string) *image.RGBA {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with -update)", path, err)
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode golden %s: %v", path, err)
	}
	rgba, ok := src.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(src.Bounds())
		b := src.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				rgba.Set(x, y, src.At(x, y))
			}
		}
	}
	return rgba
}
