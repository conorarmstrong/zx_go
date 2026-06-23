package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNextKempstonPort1FIdle pins that on the Spectrum Next, port $1F is always
// the Kempston joystick (the FPGA decodes it; the TBBLUE firmware polls it at
// boot). An idle read must return $00 — NOT the floating bus ($FF). Sonic the
// Hedgehog reads $1F during its option-menu setup (IN A,($1F); XOR $FF; ...;
// SWAPNIB; MIRROR -> a $D200-block flag); a floating-bus $FF there flipped the
// flag and sent it down a blank-screen path instead of the menu.
func TestNextKempstonPort1FIdle(t *testing.T) {
	mem, err := memory.New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	u := New(mem, keyboard.New())

	// No joystick configured (KempstonEnabled stays false) and nothing pressed.
	val, ok := u.ReadPort(0x001F)
	if !ok || val != 0x00 {
		t.Fatalf("Next port $1F = ($%02X, %v); want ($00, true) — idle Kempston, not floating bus", val, ok)
	}
}
