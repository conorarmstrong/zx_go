//go:build nbidiag

// Standalone build tag (NOT `oracle`) so this self-contained NBI-590 probe
// builds and runs without the foreign/lockstep oracle infrastructure:
//   SDKROOT="$(xcrun --sdk macosx --show-sdk-path)" \
//     go test -tags nbidiag ./cmd/zx_go/ -run TestNBISpriteAtReadback -v

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestNBISpriteAtReadback probes the NextBASIC `% SPRITE AT (n,attr)` query —
// the operation at the heart of NextBASIC Invaders line 590
//
//	SPRITE %i, % SPRITE AT (a(y),0)+((x-l(y))*20), %o, ...
//
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

	// Capture every write of the X value (200=$C8) anywhere, + writes to the
	// cache offset $04DF, BEFORE the placement runs — to see which physical
	// bank ours writes the sprite-attribute shadow into (vs the bank 5 that
	// SPRITE AT later reads via slot 2).
	type wr struct {
		bank int
		pc   uint16
		off  uint16
		val  byte
		mmu2 byte
	}
	var cacheWrites []wr
	emu.mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
		hitOff := addr&0x1FFF == 0x04DF
		hitVal := val == 200
		if (hitOff || hitVal) && len(cacheWrites) < 80 {
			cacheWrites = append(cacheWrites, wr{bank, emu.cpu.PC, addr, val, emu.mem.GetMMU(2)})
		}
	})
	defer emu.mem.SetRAMWriteHook(nil)
	defer func() {
		t.Logf("=== writes of val=200 or to offset $04DF (where the shadow lands) ===")
		for _, w := range cacheWrites {
			mark := ""
			if w.val == 200 {
				mark = "   <== X=200"
			}
			t.Logf("  WR bank=%d off=$%04X val=%d @PC~$%04X (NR$52=%d)%s", w.bank, w.off, w.val, w.pc, w.mmu2, mark)
		}
	}()

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
		mmu        [8]byte
	}
	var hits []rdpc
	var bank8reads int
	tracing := false
	emu.mem.SetRAMReadHook(func(bank int, addr uint16, val byte) {
		// Capture bank-8 reads in the sprite-cache region (uvec..uvec+$800),
		// excluding the $1700 system-var compare loop that saturated earlier.
		if tracing && (addr == 0x04DF || addr == 0x04E0) && len(hits) < 80 {
			var m [8]byte
			for s := byte(0); s < 8; s++ {
				m[s] = emu.mem.GetMMU(s)
			}
			hits = append(hits, rdpc{bank, val, emu.cpu.PC, emu.cpu.HL(), emu.cpu.DE(), addr, m})
		}
		if tracing && bank == 8 {
			bank8reads++
		}
	})
	type mw struct {
		bank int
		pc   uint16
		src  string
	}
	var mmuWrites []mw
	var allSlotWrites []struct {
		slot byte
		bank int
		pc   uint16
		src  string
	}
	emu.mem.SetBankTracer(func(slot byte, bank int, src string) {
		if slot == 2 && len(mmuWrites) < 40 {
			mmuWrites = append(mmuWrites, mw{bank, emu.cpu.PC, src})
		}
		// Capture EVERY slot write — to see whether the SPRITE AT routine ever
		// maps bank 8 (8K page 16/17) into any slot, and via what source.
		if len(allSlotWrites) < 200 {
			allSlotWrites = append(allSlotWrites, struct {
				slot byte
				bank int
				pc   uint16
				src  string
			}{slot, bank, emu.cpu.PC, src})
		}
	})
	// Wrap the raw NR$50-57 write handlers to log EVERY slot-register write
	// (reg, value, PC) and whether ours actually applied it (GetMMU after) —
	// to catch a NEXTREG $52=16 (map bank 8 into slot 2) that ours drops.
	type nrw struct {
		reg, val, applied byte
		pc                uint16
	}
	var nrWrites []nrw
	logNR := false
	for reg := byte(0x50); reg <= 0x57; reg++ {
		r := reg
		orig := emu.nextRegs.OnWriteFn(r)
		emu.nextRegs.SetOnWrite(r, func(d *nextregs.Dispatcher, val byte) {
			if orig != nil {
				orig(d, val)
			}
			if logNR && len(nrWrites) < 60 {
				nrWrites = append(nrWrites, nrw{r, val, emu.mem.GetMMU(r - 0x50), emu.cpu.PC})
			}
		})
	}
	// Capture the executing instruction + live MMU + regs at PC=$0E5E (the
	// SPRITE AT value-read) the FIRST time, from the live slot-0 bank.
	var e5e struct {
		captured bool
		code     [10]byte
		hl, de   uint16
		mmu      [8]byte
		slot0    int
	}
	ringPC := make([]uint16, 0, 80)
	var e5eTrail []uint16
	emu.cpu.AddPreFetchHook("e5e", func(pc uint16) {
		if tracing {
			if len(ringPC) < 80 {
				ringPC = append(ringPC, pc)
			} else {
				copy(ringPC, ringPC[1:])
				ringPC[79] = pc
			}
			if pc == 0x0E5C && e5eTrail == nil {
				e5eTrail = make([]uint16, len(ringPC))
				copy(e5eTrail, ringPC)
			}
		}
		if pc != 0x0E5E || e5e.captured || !tracing {
			return
		}
		e5e.captured = true
		for i := range e5e.code {
			e5e.code[i] = emu.mem.Read(0x0E5A + uint16(i))
		}
		e5e.hl, e5e.de = emu.cpu.HL(), emu.cpu.DE()
		for s := byte(0); s < 8; s++ {
			e5e.mmu[s] = emu.mem.GetMMU(s)
		}
		e5e.slot0 = emu.mem.ResolvePage(0x0E5E)
	})
	typeStr("20 poke 30005,% sprite at(0,0)")
	enter()
	logNR = true
	tracing = true
	typeStr("run 20")
	enter()
	stepN(120)
	tracing = false
	logNR = false
	emu.mem.SetBankTracer(nil)
	t.Logf("=== raw NR$50-57 writes during query (%d) ===", len(nrWrites))
	for _, w := range nrWrites {
		mark := ""
		if (w.val == 16 || w.val == 17) && w.applied != w.val {
			mark = "   <== bank 8 written but NOT APPLIED (MMU still " + fmt.Sprintf("%d", w.applied) + ")!"
		}
		t.Logf("  NR$%02X <- %d (MMU slot%d now=%d) @PC~$%04X%s", w.reg, w.val, w.reg-0x50, w.applied, w.pc, mark)
	}
	for _, w := range mmuWrites {
		t.Logf("  slot2 write: bank=%d @PC~$%04X src=%s", w.bank, w.pc, w.src)
	}
	t.Logf("=== ALL slot writes during query (%d); looking for bank 16/17 = the cache (16K bank 8) ===", len(allSlotWrites))
	for _, w := range allSlotWrites {
		mark := ""
		if w.bank == 16 || w.bank == 17 {
			mark = "   <== BANK 8 (the cache!)"
		}
		t.Logf("  slot%d <- bank=%d @PC~$%04X src=%s%s", w.slot, w.bank, w.pc, w.src, mark)
	}
	emu.mem.SetRAMReadHook(nil)
	t.Logf("SPRITE AT(0,0) re-read = %d (cache 16K bank8 offset $%04X = 200); bank8 reads during query = %d", emu.mem.Read(30005), uvec, bank8reads)
	for _, h := range hits {
		t.Logf("  cache read: off=$%04X resolved-bank=%d val=%d @PC~$%04X HL=$%04X DE=$%04X MMU=%v",
			h.bc, h.bank, h.val, h.pc, h.hl, h.de, h.mmu) // h.bc field reused to carry addr
	}
	t.Logf("MMU at end: slot0=%d slot1=%d slot2=%d slot3=%d slot6=%d slot7=%d",
		emu.mem.GetMMU(0), emu.mem.GetMMU(1), emu.mem.GetMMU(2), emu.mem.GetMMU(3), emu.mem.GetMMU(6), emu.mem.GetMMU(7))
	emu.cpu.RemovePreFetchHook("e5e")
	t.Logf("AT PC=$0E5E (live): code[$0E5A..]=% X HL=$%04X DE=$%04X slot0-page=%d MMU=%v",
		e5e.code, e5e.hl, e5e.de, e5e.slot0, e5e.mmu)
	t.Logf("PC trail INTO the $0E5C cache LDIR: %X", e5eTrail)
}
