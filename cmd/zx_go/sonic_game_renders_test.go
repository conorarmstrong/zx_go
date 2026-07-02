package main

import (
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestSonicGameRendersIfPresent is an end-to-end check that the game
// actually renders: boot NextZXOS, load sonic.nex via the genuine loader,
// settle at the title, then press Kempston fire to start the game. It
// asserts the game frame is a real, varied level — NOT a uniform fill (an
// all-zero Layer 2 rendering as opaque palette[0] over the tilemap would
// produce a flat sky-blue screen instead of the level). Skip-if-absent:
// sonic.nex is not in the repo. Gated by SONIC_DIAG so it never runs in CI
// without the local asset.
func TestSonicGameRendersIfPresent(t *testing.T) {
	if os.Getenv("SONIC_DIAG") == "" {
		t.Skip("set SONIC_DIAG=1 (requires the local sonic.nex)")
	}
	const sdPath = "/games/Next/Sonic/sonic.nex"
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
	save := func(name string, img image.Image) {
		fp, _ := os.Create(name)
		_ = png.Encode(fp, img)
		_ = fp.Close()
	}
	// distinctColours counts unique RGB triples — a uniform fill scores 1.
	distinctColours := func(img image.Image) int {
		seen := map[uint32]struct{}{}
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				key := (r>>8)<<16 | (g>>8)<<8 | (bl >> 8)
				seen[key] = struct{}{}
			}
		}
		return len(seen)
	}

	for f := 0; f < 1450; f++ { // settle to the title
		step()
	}
	// Press Kempston fire to start the game (4x press/release).
	for rep := 0; rep < 4; rep++ {
		for f := 0; f < 20; f++ {
			emu.ula.KempstonState = 0x10
			step()
		}
		for f := 0; f < 12; f++ {
			emu.ula.KempstonState = 0x00
			step()
		}
	}
	for f := 0; f < 300; f++ {
		step()
	}
	game := emu.renderFrame()
	save("/tmp/sonic_game.png", game)

	colours := distinctColours(game)
	t.Logf("game frame distinct colours: %d (NR12=$%02X NR68=$%02X)",
		colours, emu.nextRegs.Raw(0x12), emu.nextRegs.Raw(0x68))
	// The Green Hill Zone level (sky, sea, ground, palm trees, flowers) has many
	// colours. The transparency bug rendered a near-uniform blue (a handful of
	// colours). Require a comfortably varied frame.
	if colours < 16 {
		t.Errorf("game frame has only %d distinct colours — level not rendering "+
			"(check Layer-2 palette-mapped transparency)", colours)
	}
}
