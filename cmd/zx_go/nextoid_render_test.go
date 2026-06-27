package main

import (
	"image"
	"os"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNextoidSpritesRender is the end-to-end lock-in for the Nextoid fixes:
// the game uses hardware sprites (uploaded via port $57) for its bat, ball and
// the SHIPS/SCORE HUD. Three bugs hid them all:
//   - port $57 "Sprite Attribute Upload" was unimplemented, so no sprite
//     attributes ever reached the engine;
//   - NR$15's layer-priority field was decoded from bits 1:0 instead of 4:2,
//     so Nextoid (NR$15=$03) ran as Layer2-over-Sprites and the diamond
//     background covered the bat;
//   - sprites were composited in paper-relative 256x192 coordinates, but the
//     sprite X/Y are frame-relative (320x256, paper at 32,32), so the bat
//     (Y~207) and the bottom-border HUD (Y~224) fell outside the composed area.
//
// This drives the game to its playfield (press 'S' then SPACE) and asserts the
// sprites are both uploaded AND composited (toggling the sprite layer changes
// the frame). Gated by NEXTOID_E2E because nextoid.nex is gitignored.
func TestNextoidSpritesRender(t *testing.T) {
	if os.Getenv("NEXTOID_E2E") == "" {
		t.Skip("set NEXTOID_E2E=1 (requires the local, gitignored nextoid.nex)")
	}
	const sdPath = "/games/Next/Nextoid/nextoid.nex"
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
	emu.startNexloadMacro(sdPath)

	step := func() {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
		if emu.kbd != nil {
			emu.kbd.Tick()
		}
		if emu.nexloadMacro != nil && emu.nexloadMacro.tick(emu) {
			emu.nexloadMacro = nil
		}
	}
	// Boot + load via the genuine NextZXOS loader.
	for f := 0; f < 4000 && emu.nexloadMacro != nil; f++ {
		step()
	}
	for f := 0; f < 60; f++ {
		step()
	}
	// Menu -> press 'S' (start) -> story screen -> SPACE (enter body).
	press := func(row int, mask byte) {
		for f := 0; f < 25; f++ {
			emu.kbd.PressMatrixKey(row, mask, true)
			step()
		}
		emu.kbd.PressMatrixKey(row, mask, false)
		for f := 0; f < 25; f++ {
			step()
		}
	}
	press(1, 0x02)             // 'S'
	press(7, 0x01)             // SPACE
	for f := 0; f < 360; f++ { // let the level build + sprites upload
		step()
	}

	visible := 0
	for i := 0; i < 128; i++ {
		if emu.nextSprites.Sprite(i).Visible {
			visible++
		}
	}
	if visible == 0 {
		t.Fatal("no visible sprites in gameplay — port $57 sprite-attribute upload not wired")
	}

	// Snapshot the composite, then disable the sprite layer and recompose: the
	// bat/ball/HUD sprites must visibly change the frame (i.e. they ARE drawn).
	clone := func(img *image.RGBA) *image.RGBA {
		out := image.NewRGBA(img.Bounds())
		copy(out.Pix, img.Pix)
		return out
	}
	withSprites := clone(emu.renderFrame())
	emu.nextSprites.SetEnabled(false)
	withoutSprites := clone(emu.renderFrame())
	emu.nextSprites.SetEnabled(true)

	// With sprites active the Next frame is the full 320x256 over-border frame;
	// without, it falls back to the classic 320x240. Align the common rows by
	// the vertical offset ((256-240)/2 = 8) and compare the shared band.
	diff := 0
	off := (withSprites.Bounds().Dy() - withoutSprites.Bounds().Dy()) / 2
	for r := 0; r < withoutSprites.Bounds().Dy(); r++ {
		sr := r + off
		for x := 0; x < withoutSprites.Stride; x++ {
			if withSprites.Pix[sr*withSprites.Stride+x] != withoutSprites.Pix[r*withoutSprites.Stride+x] {
				diff++
			}
		}
	}
	// A handful of sprite glyphs (bat + HUD digits) is hundreds of pixels.
	if diff < 200 {
		t.Fatalf("sprite layer barely affects the frame (%d bytes differ) — "+
			"bat/ball/HUD not composited", diff)
	}
	t.Logf("nextoid gameplay: %d visible sprites, sprite layer changes %d frame bytes",
		visible, diff)
}
