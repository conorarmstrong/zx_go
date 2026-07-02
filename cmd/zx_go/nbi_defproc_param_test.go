package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNBIDefprocIntParamBinds is the end-to-end guard for the NextBASIC
// Invaders "Integer out of range, 2550:1" fix. It boots NextZXOS, enters
// NextBASIC, and RUNs a minimal program whose DEFPROC takes an integer
// parameter:
//
//	10 let %i=200: poke 30000,99: proc p(7): stop
//	20 defproc p(%i): poke 30000,%i: endproc
//
// The body POKEs %i to address 30000. With the parameter correctly bound
// mem[30000] must be 7 (the argument); the bug left it 200 (the leaked
// global). Root cause: the Z80N SETAE opcode built a reversed pixel mask, so
// NextBASIC's DEFPROC dirty-var bitmap marked the wrong integer variable and
// the bound value never reached the var-cache the body reads. See
// pkg/z80/setae_test.go. Skips if the Next ROMs are not installed.
func TestNBIDefprocIntParamBinds(t *testing.T) {
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
	emu.reboot()

	step := func() {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
		if emu.kbd != nil {
			emu.kbd.Tick()
		}
	}
	stepN := func(n int) {
		for i := 0; i < n; i++ {
			step()
		}
	}
	digits := map[rune][][2]int{'1': {{3, 0x01}}, '2': {{3, 0x02}}, '3': {{3, 0x04}}, '4': {{3, 0x08}}, '5': {{3, 0x10}}, '6': {{4, 0x10}}, '7': {{4, 0x08}}, '8': {{4, 0x04}}, '9': {{4, 0x02}}, '0': {{4, 0x01}}}
	karr := map[rune][][2]int{'%': {{7, 0x02}, {3, 0x10}}, '(': {{7, 0x02}, {4, 0x04}}, ')': {{7, 0x02}, {4, 0x02}}, '=': {{7, 0x02}, {6, 0x02}}, ':': {{7, 0x02}, {0, 0x02}}, ',': {{7, 0x02}, {7, 0x08}}, ' ': {{7, 0x01}}}
	for r, k := range nexKeyMatrix {
		karr[r] = k
	}
	for r, k := range digits {
		karr[r] = k
	}
	hold := func(kk [][2]int, frames int) {
		for _, k := range kk {
			emu.kbd.PressMatrixKey(k[0], byte(k[1]), true)
		}
		stepN(frames)
		for row := 0; row < 8; row++ {
			emu.kbd.PressMatrixKey(row, 0xFF, false)
		}
	}
	typeStr := func(s string) {
		for _, c := range strings.ToLower(s) {
			if k, ok := karr[c]; ok {
				hold(k, 4)
				stepN(10)
			}
		}
	}
	enter := func() { hold([][2]int{{6, 0x01}}, 6); stepN(80) }

	booted := false
	for f := 0; f < 900; f++ {
		if emu.cpu.PC == nextMenuLoopPC {
			booted = true
			break
		}
		step()
	}
	if !booted {
		t.Skip("did not reach the NextZXOS menu loop (boot path differs)")
	}
	hold([][2]int{{7, 0x01}}, 40)
	stepN(140)               // SPACE -> main menu
	for d := 0; d < 2; d++ { // down x2 -> NextBASIC
		hold([][2]int{{0, 0x01}, {4, 0x10}}, 6)
		stepN(20)
	}
	hold([][2]int{{6, 0x01}}, 6)
	stepN(120) // ENTER -> editor

	typeStr("10 let %i=200: poke 30000,99: proc p(7): stop")
	enter()
	typeStr("20 defproc p(%i): poke 30000,%i: endproc")
	enter()
	typeStr("run")
	enter()
	stepN(150)

	if got := emu.mem.Read(30000); got != 7 {
		t.Fatalf("DEFPROC integer parameter did not bind: mem[30000]=%d, want 7", got)
	}
}
