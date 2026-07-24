package testharness

import (
	"math/rand"
	"runtime/debug"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestGuestPortStress runs randomised guest code through the real CPU on every
// model: OUT / IN across the whole 16-bit port space, Z80N NEXTREG writes over
// the whole register space, and reads and writes across the whole address
// space, then renders. Guest code is untrusted — any value a program can put on
// the bus has to be survivable — so a panic here is a crash a real program
// could trigger, and there is no recover() anywhere in the emulator to soften
// it.
//
// This is what caught the $DFFD extended-RAM-bank fault: a bank >= 16 in the
// $C000 slot was read through the classic ROM dispatch, silently returning ROM
// bytes for banks 16-19 and indexing past the four-entry ROM array for 20+.
// See pkg/memory/dffd_highbank_test.go.
func TestGuestPortStress(t *testing.T) {
	models := []roms.SpectrumModel{
		roms.Model48K, roms.Model128K, roms.ModelPlus2,
		roms.ModelPlus2A, roms.ModelPlus3, roms.ModelNext, roms.ModelPentagon,
	}
	for _, model := range models {
		for seed := 0; seed < 60; seed++ {
			runGuestPortStress(t, model, int64(seed))
		}
	}
}

func runGuestPortStress(t *testing.T, model roms.SpectrumModel, seed int64) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("model=%v seed=%d PANIC: %v\n%s", model, seed, r, debug.Stack())
		}
	}()
	h, err := New(model)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(h.CloseFiles)
	rng := rand.New(rand.NewSource(seed))

	h.RunFrames(2) // let the machine reach a realistic state first

	// Assemble the random program into upper RAM, ending in a spin so the
	// CPU stays put once the stream is exhausted.
	const base = 0xC000
	addr := uint16(base)
	emit := func(bs ...byte) {
		for _, b := range bs {
			h.WriteMemory(addr, b)
			addr++
		}
	}
	for i := 0; i < 220 && addr < 0xFF00; i++ {
		port := uint16(rng.Intn(0x10000))
		val := byte(rng.Intn(256))
		switch rng.Intn(6) {
		case 0: // LD BC,nn / LD A,n / OUT (C),A
			emit(0x01, byte(port), byte(port>>8), 0x3E, val, 0xED, 0x79)
		case 1: // LD BC,nn / IN A,(C)
			emit(0x01, byte(port), byte(port>>8), 0xED, 0x78)
		case 2: // NEXTREG reg,val  (Z80N: ED 91 rr vv)
			emit(0xED, 0x91, val, byte(rng.Intn(256)))
		case 3: // LD A,n / NEXTREG reg,A  (Z80N: ED 92 rr)
			emit(0x3E, val, 0xED, 0x92, byte(rng.Intn(256)))
		case 4: // LD A,n / LD (nn),A — exercise the write dispatch
			w := uint16(rng.Intn(0x10000))
			emit(0x3E, val, 0x32, byte(w), byte(w>>8))
		case 5: // LD A,(nn) — exercise the read dispatch
			w := uint16(rng.Intn(0x10000))
			emit(0x3A, byte(w), byte(w>>8))
		}
	}
	emit(0x18, 0xFE) // JR -2

	h.CPU().PC = base
	h.CPU().SP = 0xBF00
	// Render between runs so the video stack sees the registers the stream
	// left behind — sprites, Layer 2, tilemap, copper and the palette all
	// index off guest-written values.
	for i := 0; i < 4; i++ {
		h.RunFrames(2)
		h.ScreenImage()
	}
}
