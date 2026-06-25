//go:build oracle

package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNBISpriteAtReadback probes the NextBASIC `% SPRITE AT (n,attr)` query —
// the operation at the heart of NextBASIC Invaders line 590
//   SPRITE %i, % SPRITE AT (a(y),0)+((x-l(y))*20), %o, ...
// which throws "Integer out of range, 590:1". If SPRITE AT returns a bad X for
// a placed/moving sprite, the computed missile X goes out of range. This boots
// NextZXOS, places sprite 0 at a known (X=200,Y=100,pat=5), then a moving
// sprite via SPRITE CONTINUE, and POKEs the SPRITE AT read-backs to RAM for
// inspection. Diagnostic (logs, does not assert). Skips if ROMs absent.
func TestNBISpriteAtReadback(t *testing.T) {
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
		t.Skip("did not reach the NextZXOS menu loop")
	}
	hold([][2]int{{7, 0x01}}, 40)
	stepN(140) // SPACE -> main menu
	for d := 0; d < 2; d++ {
		hold([][2]int{{0, 0x01}, {4, 0x10}}, 6)
		stepN(20)
	}
	hold([][2]int{{6, 0x01}}, 6)
	stepN(120) // ENTER -> editor

	// Sentinel (30010=42) proves the line ran; static sprite 0 at X=200,Y=100.
	// SPRITE MOVE flushes the NextZXOS RAM sprite-attribute cache that SPRITE
	// AT reads (nextzxos-changelog: attrs cached in RAM8 since v2.09).
	typeStr("10 poke 30010,42: sprite 0,200,100,5,1: sprite move : poke 30000,% sprite at(0,0): poke 30001,% sprite at(0,1)")
	enter()
	typeStr("run")
	enter()
	stepN(150)
	t.Logf("[ran=%d/42] static+MOVE sprite0: SPRITE AT X=%d (want 200), Y=%d (want 100)",
		emu.mem.Read(30010), emu.mem.Read(30000), emu.mem.Read(30001))

	// Read the NextZXOS sprite-attribute cache DIRECTLY out of 16K RAM bank 8
	// (8K pages 16,17). uvec8_sprite_data lives at RAM8 offset $1FFE; sprite N
	// is a 16-byte struct at uvec+N*16, +0=X +1=Y. If the cache holds 200 but
	// SPRITE AT returned 0, the bug is in the read path / banking during the
	// query; if the cache itself is 0, the SPRITE write never reached it.
	p16, p17 := emu.mem.RAM8KPage(16), emu.mem.RAM8KPage(17)
	ram8 := func(off int) byte {
		if off < 0x2000 {
			return p16[off]
		}
		return p17[off-0x2000]
	}
	uvec := int(ram8(0x1FFE)) | int(ram8(0x1FFF))<<8
	t.Logf("RAM8 uvec8_sprite_data=$%04X; cache sprite0 X=%d Y=%d (want 200,100)",
		uvec, ram8(uvec+0), ram8(uvec+1))

	// TRACE the read path: capture every RAM read at the cache's X/Y offsets
	// (uvec, uvec+1) across ALL 16K banks during a SPRITE AT query. If the
	// guest reads bank != 8 (or bank 8 but value 0) the MMU paged the wrong
	// bank for the read; the cache (bank 8) is known-correct (200,100).
	type rd struct {
		bank int
		val  byte
	}
	_ = rd{}
	// CORRELATED trace in one run: interleave NextReg MMU writes to slot 1
	// (NextReg $51 = $2000-$3FFF, where bank 8 page 16 gets mapped) with reads
	// at the cache offset ($04DF within any 8K slot). This shows whether the
	// override is live when the cache read happens.
	// Find the PC of the SPRITE AT cache read: the value SPRITE AT returns (0)
	// comes from a read at the cache offset. Capture every read at the
	// 8K-relative cache offset, with the CPU PC + the instruction bytes, so we
	// can arm a read-tape lockstep there.
	type rdpc struct {
		bank       int
		val        byte
		pc         uint16
		hl, de, bc uint16
	}
	var hits []rdpc
	var bank8reads int
	tracing := false
	emu.mem.SetRAMReadHook(func(bank int, addr uint16, val byte) {
		// Capture bank-8 reads in the sprite-cache region (uvec..uvec+$800),
		// excluding the $1700 system-var compare loop that saturated earlier.
		if tracing && bank == 8 && addr < 0x1000 && len(hits) < 40 {
			hits = append(hits, rdpc{bank, val, emu.cpu.PC, emu.cpu.HL(), emu.cpu.DE(), addr})
		}
		if tracing && bank == 8 {
			bank8reads++
		}
	})
	typeStr("20 poke 30005,% sprite at(0,0)")
	enter()
	tracing = true
	typeStr("run 20")
	enter()
	stepN(120)
	tracing = false
	emu.mem.SetRAMReadHook(nil)
	t.Logf("SPRITE AT(0,0) re-read = %d (cache 16K bank8 offset $%04X = 200); bank8 reads during query = %d", emu.mem.Read(30005), uvec, bank8reads)
	for _, h := range hits {
		t.Logf("  bank8 read: off=$%04X val=%d @PC~$%04X  HL=$%04X DE=$%04X",
			h.bc, h.val, h.pc, h.hl, h.de) // h.bc field reused to carry addr
	}
}
