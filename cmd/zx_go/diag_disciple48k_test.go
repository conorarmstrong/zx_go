package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Guards a 48K boot with DISCiPLE + Multiface MF3 both enabled (the
// GUI config-restore path) against a black-screen regression.
func TestDiag48KWithDisciple(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	if err := emu.peripherals.EnableDisciple("roms"); err != nil {
		t.Skipf("disciple ROM missing: %v", err)
	}
	dev := emu.peripherals.GetDisciple()
	emu.cpu.PreFetchHook = dev.PreFetchHook
	emu.cpu.PostFetchHook = dev.PostFetchHook
	if err := emu.peripherals.EnableMultiface(configStringToMultifaceVariant("MF3"), "roms"); err != nil {
		t.Logf("multiface: %v", err)
	}
	// The GUI config-restore path reboots after enabling boot-affecting
	// peripherals (peripheralNeedsReboot) — replicate it; the DISCiPLE
	// hooks are active across this reset exactly as in the GUI.
	emu.reboot()
	for i := 0; i < 300; i++ {
		runOneFrameHeadless(emu, roms.Model48K)
	}
	// 48K boot should reach the copyright screen: border/paper white,
	// bitmap mostly zero but ATTRs set to $38. Black screen = attrs $00.
	attrs := 0
	for a := uint16(0x5800); a < 0x5B00; a++ {
		if emu.mem.Read(a) == 0x38 {
			attrs++
		}
	}
	t.Logf("PC=$%04X attrs38=%d/768", emu.cpu.PC, attrs)
	if attrs < 700 {
		t.Errorf("48K+DISCiPLE boot did not reach the BASIC screen (attrs38=%d) — black screen repro", attrs)
	}
}
