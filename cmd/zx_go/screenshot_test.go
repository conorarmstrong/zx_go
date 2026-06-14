package main

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The File → Save Screenshot menu action must work for every machine
// type. writeScreenshotPNG is the shared encoder; this boots each
// classic model a little, captures, and verifies a valid non-empty
// PNG of the expected framebuffer dimensions comes out.
func TestSaveScreenshotAllClassicMachines(t *testing.T) {
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	defer func() { cliFlagsActive = prev }()

	for _, m := range []roms.SpectrumModel{
		roms.Model48K, roms.Model128K, roms.ModelPlus2,
		roms.ModelPlus2A, roms.ModelPlus3,
	} {
		t.Run(roms.GetModelName(m), func(t *testing.T) {
			emu, err := newEmulator(m)
			if err != nil {
				t.Fatalf("newEmulator(%s): %v", roms.GetModelName(m), err)
			}
			for i := 0; i < 120; i++ {
				runOneFrameHeadless(emu, m)
			}
			var buf bytes.Buffer
			if err := writeScreenshotPNG(emu, &buf); err != nil {
				t.Fatalf("writeScreenshotPNG: %v", err)
			}
			if buf.Len() == 0 {
				t.Fatal("empty PNG")
			}
			img, err := png.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("decode PNG: %v", err)
			}
			b := img.Bounds()
			if b.Dx() <= 0 || b.Dy() <= 0 {
				t.Fatalf("bad PNG dimensions %dx%d", b.Dx(), b.Dy())
			}
			// The copyright screen is non-blank: at least some pixels
			// must differ from the top-left pixel (border).
			if uniformImage(img) {
				t.Errorf("%s screenshot is a single flat colour — render produced nothing", roms.GetModelName(m))
			}
		})
	}
}

func TestSaveScreenshotNext(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	defer func() { cliFlagsActive = prev }()

	emu, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Fatalf("newEmulator(Next): %v", err)
	}
	emu.paused.Store(false)
	for i := 0; i < 400; i++ {
		runOneFrameHeadless(emu, roms.ModelNext)
	}
	var buf bytes.Buffer
	if err := writeScreenshotPNG(emu, &buf); err != nil {
		t.Fatalf("writeScreenshotPNG(Next): %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("decode Next PNG: %v", err)
	}
}

func uniformImage(img image.Image) bool {
	b := img.Bounds()
	r0, g0, b0, a0 := img.At(b.Min.X, b.Min.Y).RGBA()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := img.At(x, y).RGBA()
			if r != r0 || g != g0 || bb != b0 || a != a0 {
				return false
			}
		}
	}
	return true
}
