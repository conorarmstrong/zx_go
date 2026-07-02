package main

import (
	"strings"
	"testing"
)

// TestFormatNextRegPanel verifies the decoded NextReg dashboard renders
// the key registers with human-readable meaning (jnext-parity: its GUI
// has a NextReg panel; we surface the same as a scriptable command).
// The formatter takes a reader func so it unit-tests without an emulator.
func TestFormatNextRegPanel(t *testing.T) {
	regs := map[byte]byte{
		0x03: 0xB3, // machine type / timing
		0x06: 0x10, // peripheral 2: bit4 = drive-button divMMC NMI enable
		0x07: 0x03, // CPU speed = 28 MHz
		0x0A: 0x10, // peripheral 1: bit4 = divMMC automap enable
		0x8C: 0x00,
		0x8E: 0x08, // paging
		0x50: 0xFF, 0x51: 0xFF, 0x52: 0x0A, 0x53: 0x0B,
		0x54: 0x04, 0x55: 0x05, 0x56: 0xDE, 0x57: 0xDF, // MMU8 slots
	}
	out := formatNextRegPanel(func(r byte) byte { return regs[r] })

	for _, want := range []string{
		"NextReg",                // header
		"$07", "CPU speed", "28", // decoded CPU speed
		"MMU8", "$50", "$57", "DF", // slot table incl. slot 7 value
		"$8E",            // paging reg present
		"automap en=1",   // $0A bit4 is the real divMMC-automap gate
		"drive-nmi en=1", // $06 bit4 is only the DRIVE-button NMI enable
	} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q\n---\n%s", want, out)
		}
	}
	// $06 bit4 must NOT be mislabelled as the divMMC enable — that gate
	// lives at $0A bit4 (see the automap en= check above).
	if strings.Contains(out, "divMMC en=") {
		t.Errorf("panel mislabels NR$06 bit4 as \"divMMC en\"; that gate is NR$0A bit4:\n%s", out)
	}
}
