//go:build nbidiag

// Standalone build tag (NOT `oracle`) so this self-contained SPRITE AT
// diagnostic probe builds and runs without the foreign/lockstep oracle
// infrastructure:
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

	// Trace EVERY NR$52 (MMU slot 2) write from boot — the SPRITE AT routine
	// reads the cache through a hardcoded slot-2 base ($40xx in $0E9B), so slot
	// 2 must hold the bank-8 cache; ours reads bank 5. This shows when slot 2 is
	// set to bank 8 (16) and where it reverts to the default 5 (10).
	type nr52e struct {
		val, applied byte
		pc           uint16
	}
	var nr52log []nr52e
	nr52on := true // capture from BOOT
	var nr52count, nr52is16 int
	orig52 := emu.nextRegs.OnWriteFn(0x52)
	emu.nextRegs.SetOnWrite(0x52, func(d *nextregs.Dispatcher, val byte) {
		if orig52 != nil {
			orig52(d, val)
		}
		if nr52on {
			nr52count++
			if val == 16 || val == 17 {
				nr52is16++
			}
			// Log only the bank-8 mappings (val 16/17) + a sample, to find if
			// ours EVER maps the cache bank into slot 2.
			if (val == 16 || val == 17 || len(nr52log) < 10) && len(nr52log) < 120 {
				nr52log = append(nr52log, nr52e{val, emu.mem.GetMMU(2), emu.cpu.PC})
			}
		}
	})
	defer func() {
		t.Logf("=== NR$52 (slot 2) writes: %d total, %d that map bank 8 (val 16/17) ===", nr52count, nr52is16)
		for _, w := range nr52log {
			mark := ""
			if w.val == 16 {
				mark = "   <== maps bank 8 (cache) into slot 2"
			}
			t.Logf("  NR$52 <- %d (slot2 now=%d) @PC~$%04X%s", w.val, w.applied, w.pc, mark)
		}
	}()

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
	var cacheWriteMMU [][8]byte
	emu.mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
		if bank == 8 && addr == 0x04DF && len(cacheWrites) < 20 {
			cacheWrites = append(cacheWrites, wr{bank, emu.cpu.PC, addr, val, emu.mem.GetMMU(2)})
			var m [8]byte
			for s := byte(0); s < 8; s++ {
				m[s] = emu.mem.GetMMU(s)
			}
			cacheWriteMMU = append(cacheWriteMMU, m)
		}
	})
	defer emu.mem.SetRAMWriteHook(nil)
	defer func() {
		t.Logf("=== cache X-byte writes to bank 8 $04DF (which slot maps bank 8 = pages 16/17?) ===")
		for i, w := range cacheWrites {
			t.Logf("  WR bank8 $04DF val=%d @PC~$%04X MMU=%v", w.val, w.pc, cacheWriteMMU[i])
		}
	}()

	// Read the cache offset $04DF in bank 8 AND bank 5 BEFORE placing the
	// sprite — to tell whether the "200" is the sprite cache (0 here, set by
	// placement) or pre-existing OS data (already 200, a red herring).
	{
		pre16 := emu.mem.RAM8KPage(16)
		pre10, pre11 := emu.mem.RAM8KPage(10), emu.mem.RAM8KPage(11)
		_ = pre11
		t.Logf("PRE-PLACEMENT: bank8[$04DF]=%d bank5(pg10)[$04DF]=%d", pre16[0x04DF], pre10[0x04DF])
	}

	// Sentinel (30010=42) proves the line ran; static sprite 0 at X=200,Y=100.
	// SPRITE MOVE flushes the NextZXOS RAM sprite-attribute cache that SPRITE
	// AT reads (NextZXOS caches sprite attrs in RAM8 since v2.09).
	typeStr("10 poke 30010,42: sprite 0,200,100,5,1: sprite move : poke 30000,% sprite at(0,0): poke 30001,% sprite at(0,1)")
	enter()
	typeStr("run")
	enter()
	stepN(150)
	t.Logf("[ran=%d/42] static+MOVE sprite0: SPRITE AT X=%d (want 200), Y=%d (want 100)",
		emu.mem.Read(30010), emu.mem.Read(30000), emu.mem.Read(30001))

	// Read the NextZXOS sprite-attribute cache directly out of 16K RAM
	// bank 8 (8K pages 16,17). uvec8_sprite_data lives at RAM8 offset
	// $1FFE; sprite N is a 16-byte struct at uvec+N*16, +0=X +1=Y — an
	// independent readback of the cache to compare against SPRITE AT's
	// own return value.
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

	// Trace the read path: capture every RAM read at the cache's X/Y
	// offsets (uvec, uvec+1) across all 16K banks during a SPRITE AT
	// query, with the resolved bank, PC, and MMU slot table — enough to
	// tell whether the query is reading bank 8 (the cache) or an MMU
	// mis-mapping is serving a different bank.
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
		// Only the cache's X/Y offsets matter here ($04DF/$04E0); every
		// other read (e.g. the system-var compare loop) is noise.
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
		// Slot 2's MMU writes are what map bank 8 in for a SPRITE AT
		// query; correlating them against the cache reads above shows
		// whether the mapping is live when the read happens.
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
	var e9b struct {
		captured       bool
		code           [24]byte
		hl, de, bc, sp uint16
		mmu            [8]byte
	}
	// Dump the read-routine code at $0D40-$0E48 (live) the first time we reach
	// $0D6E during the query — to find the conditional branch that should lead
	// to a NEXTREG $52,16 (ED 91 52 10) but which ours skips.
	var rdcode struct {
		captured bool
		d        [0x110]byte
		bc, hl   uint16
	}
	// Capture every NextReg READ via the $0D6B helper (OUT $243B=reg @ $0D6E,
	// IN $253B=value @ $0D73) during the query — if ours returns a wrong value
	// for some reg, the routine branches to skip the cache→slot-2 mapping.
	type nrread struct{ reg, val byte }
	var nrReads []nrread
	var pendingReg byte
	emu.cpu.AddPreFetchHook("rdcode", func(pc uint16) {
		if !tracing {
			return
		}
		if pc == 0x0D6E {
			pendingReg = emu.cpu.A // A = the NextReg being selected
		}
		if pc == 0x0D73 && len(nrReads) < 60 {
			nrReads = append(nrReads, nrread{pendingReg, emu.cpu.A}) // A = value read
		}
		if pc == 0x0D6E && !rdcode.captured {
			rdcode.captured = true
			for i := range rdcode.d {
				rdcode.d[i] = emu.mem.Read(0x0D40 + uint16(i))
			}
			rdcode.bc, rdcode.hl = emu.cpu.BC(), emu.cpu.HL()
		}
	})
	defer func() {
		t.Logf("=== NextReg READS by the SPRITE AT routine (reg -> value ours returns) ===")
		for _, r := range nrReads {
			t.Logf("  read NR$%02X = $%02X (%d)", r.reg, r.val, r.val)
		}
	}()
	emu.cpu.AddPreFetchHook("e5e", func(pc uint16) {
		if tracing && pc == 0x0E9B && !e9b.captured {
			e9b.captured = true
			for i := range e9b.code {
				e9b.code[i] = emu.mem.Read(0x0E9B + uint16(i))
			}
			e9b.hl, e9b.de, e9b.bc, e9b.sp = emu.cpu.HL(), emu.cpu.DE(), emu.cpu.BC(), emu.cpu.SP
			for s := byte(0); s < 8; s++ {
				e9b.mmu[s] = emu.mem.GetMMU(s)
			}
		}
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
	emu.cpu.RemovePreFetchHook("rdcode")
	if rdcode.captured {
		t.Logf("read-routine BC=$%04X HL=$%04X; code $0D40-$0E4F (look for ED 91 52 = NEXTREG $52):", rdcode.bc, rdcode.hl)
		for off := 0; off < 0x110; off += 16 {
			t.Logf("  $%04X: % X", 0x0D40+off, rdcode.d[off:off+16])
		}
	}
	t.Logf("AT PC=$0E5E (live): code[$0E5A..]=% X HL=$%04X DE=$%04X slot0-page=%d MMU=%v",
		e5e.code, e5e.hl, e5e.de, e5e.slot0, e5e.mmu)
	t.Logf("PC trail INTO the $0E5C cache LDIR: %X", e5eTrail)
	t.Logf("AT $0E9B (cache-banking setup): code=% X HL=$%04X DE=$%04X BC=$%04X SP=$%04X MMU=%v",
		e9b.code, e9b.hl, e9b.de, e9b.bc, e9b.sp, e9b.mmu)
}
