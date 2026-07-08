package testharness

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// updateCorpusGolden regenerates the golden PNGs instead of asserting
// against them: `go test ./pkg/testharness -run TestCorpus -update`.
var updateCorpusGolden = flag.Bool("update", false, "regenerate corpus golden frames")

// corpusProgram is one vendored, redistributable Next program booted as a
// faithfulness regression. See testdata/corpus/CORPUS.md for provenance and
// per-file licenses. Binaries load through the emulator's own ROM-independent
// .nex loader, so no proprietary NextZXOS/ROM is involved.
type corpusProgram struct {
	name       string // golden basename (testdata/corpus/golden/<name>.png)
	file       string // binary under testdata/corpus/bin/
	frames     int    // frames to run before capturing the screen
	minColours int    // liveness floor — a real render has many distinct colours
}

var corpusPrograms = []corpusProgram{
	{name: "layer2_tilemap", file: "zxnext_layer2_tilemap.nex", frames: 250, minColours: 12},
	{name: "tilemap", file: "zxnext_tilemap.nex", frames: 250, minColours: 6},
	{name: "specbong", file: "SpecBong.nex", frames: 250, minColours: 5},
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
			installtest.RedirectConfig(t)
			installOpenBottomROM(t)

			h, err := New(roms.ModelNext)
			if err != nil {
				t.Fatalf("New(ModelNext): %v", err)
			}
			binPath := filepath.Join("testdata", "corpus", "bin", p.file)
			if err := h.LoadNEX(binPath); err != nil {
				t.Fatalf("LoadNEX(%s): %v", p.file, err)
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
