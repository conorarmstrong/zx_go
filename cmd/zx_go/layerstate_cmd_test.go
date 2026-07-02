package main

import (
	"strings"
	"testing"
)

// TestFormatLayerState verifies the decoded video-layer panel: the
// SLU layer-priority order from NR$15 bits 4-2, sprite enable/over-
// border/priority, ULA enable (NR$68), Layer2 resolution (NR$70) and
// tilemap enable (NR$6B). Reader injected so it unit-tests without an
// emulator.
func TestFormatLayerState(t *testing.T) {
	regs := map[byte]byte{
		0x15: 0x03, // prio bits 4-2 = 000 (S L U); bit1 over-border, bit0 sprites on
		0x68: 0x00, // ULA enabled (bit7=0)
		0x70: 0x00, // Layer2 256x192
		0x6B: 0x80, // tilemap enabled (bit7=1)
	}
	out := formatLayerState(func(r byte) byte { return regs[r] })

	for _, want := range []string{
		"Layers",            // header
		"priority", "S>L>U", // decoded SLU order
		"sprites", "on", // sprites visible
		"ULA", "Layer2", "256x192", // L2 resolution decoded
		"tilemap", "enabled",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("layer panel missing %q\n---\n%s", want, out)
		}
	}
}

// TestFormatLayerState_ULADisabled checks the ULA-disabled bit and a
// non-default priority order decode correctly.
func TestFormatLayerState_ULADisabled(t *testing.T) {
	regs := map[byte]byte{
		0x15: 0x14, // bits 4-2 = 101 → U L S
		0x68: 0x80, // ULA disabled
	}
	out := formatLayerState(func(r byte) byte { return regs[r] })
	if !strings.Contains(out, "U>L>S") {
		t.Errorf("expected U>L>S priority:\n%s", out)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("expected ULA disabled:\n%s", out)
	}
}

// TestFormatLayerState_SpriteZeroOnTop checks NR$15 bit 6 decodes with
// the correct polarity: bit set means sprite 0 renders in front of
// higher-numbered overlapping sprites (sprite.Engine.SetZeroOnTop).
func TestFormatLayerState_SpriteZeroOnTop(t *testing.T) {
	out := formatLayerState(func(r byte) byte {
		if r == 0x15 {
			return 0x40 // bit 6 set
		}
		return 0
	})
	if !strings.Contains(out, "sprite0-on-top: yes") {
		t.Errorf("bit6 set should report sprite0-on-top: yes:\n%s", out)
	}

	out = formatLayerState(func(r byte) byte { return 0 }) // bit 6 clear
	if !strings.Contains(out, "sprite0-on-top: no") {
		t.Errorf("bit6 clear should report sprite0-on-top: no:\n%s", out)
	}
}
