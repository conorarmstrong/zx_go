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
		"NextReg",                           // header
		"$07 CPU Speed", "programmed=28MHz", // decoded CPU speed
		"MMU8", "$50", "$57", "DF", // slot table incl. slot 7 value
		"$8E 128K Memory Mapping", // paging reg present
	} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q\n---\n%s", want, out)
		}
	}
	line := func(reg string) string {
		for _, l := range strings.Split(out, "\r\n") {
			if strings.Contains(l, reg+" ") {
				return l
			}
		}
		return ""
	}
	// $0A bit 4 is the divMMC automap gate; $06 bit 4 is only the DRIVE-button
	// NMI enable.
	if l := line("$0A"); !strings.Contains(l, " automap") {
		t.Errorf("$0A line %q does not decode automap", l)
	}
	if l := line("$06"); !strings.Contains(l, "drive-nmi") || strings.Contains(l, "automap") {
		t.Errorf("$06 line %q: want drive-nmi and no automap", l)
	}
}

func TestFormatNextRegListNamesEveryRegister(t *testing.T) {
	out := formatNextRegList(func(byte) byte { return 0 }, nil)
	for _, want := range []string{"$00 Machine ID", "$CE DMA INT EN 2", "$A9 ESP GPIO0"} {
		if !strings.Contains(out, want) {
			t.Errorf("nr-list missing %q", want)
		}
	}
	if strings.Contains(out, "$E0 ") {
		t.Error("nr-list printed a reserved register")
	}
	one := formatNextRegList(func(byte) byte { return 0xAD }, []byte{0xC0})
	if !strings.Contains(one, "vector=5 stackless im=2 hw-im2") {
		t.Errorf("nr-list C0: %q", one)
	}
}
