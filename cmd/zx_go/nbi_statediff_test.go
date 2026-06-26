//go:build nbidiag

// Standalone (nbidiag tag) — the zx_go half of the post-load snapshot
// comparison. Boots the SAME FAT32 SD image MAME uses ($ZX_GO_NEXT_SD_IMG),
// drives the SAME keystrokes (SPACE -> Command Line -> .cd -> load) to the
// NextBASIC Invaders DIFFICULTY MENU (post-LOAD, pre-digit), then exports the
// full PHYSICAL state in the same "FX|" format as nbi_state_dump.lua so the two
// can be byte-diffed. Run:
//
//	SDKROOT="$(xcrun --sdk macosx --show-sdk-path)" \
//	  ZX_GO_NEXT_SD_IMG=$HOME/development/CSpect/app/zxgo-next-fat32.img \
//	  ZXFX_OUT=/tmp/zxgo_nbi_menu.fx \
//	  go test -tags nbidiag ./cmd/zx_go/ -run TestNBIStateDumpAtMenu -v -timeout 600s
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func TestNBIStateDumpAtMenu(t *testing.T) {
	if os.Getenv("ZX_GO_NEXT_SD_IMG") == "" {
		t.Skip("set ZX_GO_NEXT_SD_IMG to the FAT32 card to match the MAME oracle")
	}
	out := os.Getenv("ZXFX_OUT")
	if out == "" {
		out = "/tmp/zxgo_nbi_menu.fx"
	}

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

	// Keymap: NextZXOS command-line characters -> matrix (row,mask) presses.
	// nexKeyMatrix has . / - ' space letters digits; add " (SYM+P) for load.
	km := map[rune][][2]int{}
	for r, k := range nexKeyMatrix {
		km[r] = k
	}
	km['"'] = [][2]int{{7, 0x02}, {5, 0x01}} // SYMBOL SHIFT + P

	release := func() {
		for row := 0; row < 8; row++ {
			emu.kbd.PressMatrixKey(row, 0xFF, false)
		}
	}
	hold := func(keys [][2]int, frames int) {
		for _, k := range keys {
			emu.kbd.PressMatrixKey(k[0], byte(k[1]), true)
		}
		stepN(frames)
		release()
	}
	typeStr := func(s string) {
		for _, c := range strings.ToLower(s) {
			if k, ok := km[c]; ok {
				hold(k, 4)
				stepN(10)
			}
		}
	}
	enter := func() { hold([][2]int{{6, 0x01}}, 6); stepN(92) }

	// Boot to the welcome/menu wait loop.
	booted := false
	for f := 0; f < 1200; f++ {
		if emu.cpu.PC == nextMenuLoopPC {
			booted = true
			break
		}
		step()
	}
	if !booted {
		t.Skip("did not reach the NextZXOS menu loop")
	}

	// SPACE -> main menu; DOWN (CAPS+6) -> Command Line; ENTER -> prompt.
	hold([][2]int{{7, 0x01}}, 40)
	stepN(140)
	hold([][2]int{{0, 0x01}, {4, 0x10}}, 6)
	stepN(10)
	enter()

	// cd into the game folder, then LOAD (auto-RUNs to the difficulty menu).
	typeStr(`.cd "/games/next/nextbasic invaders"`)
	enter()
	typeStr(`load "basicinvaders.bas"`)
	enter()
	stepN(1500) // let the OS load + auto-run to the settled difficulty menu

	// ZXFX_PHASE=game: press difficulty '1' and run a few seconds into the live
	// invader field — where the SPRITE AT / missile path (line 590) executes —
	// then dump. Default (menu) dumps at the quiescent pre-digit menu.
	if os.Getenv("ZXFX_PHASE") == "game" {
		hold([][2]int{{3, 0x01}}, 6) // '1'
		gf := 360
		if v := os.Getenv("ZXFX_GAMEFRAMES"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				gf = n
			}
		}
		// Step frame-by-frame, recording when ours first returns to the NextZXOS
		// menu loop ($0C90) — the signature of the 590 error aborting the program.
		pclog := os.Getenv("ZXFX_PCLOG") != ""
		threwAt := -1
		for i := 0; i < gf; i++ {
			step()
			if pclog && i < 60 {
				t.Logf("  game frame %2d: PC=$%04X", i, emu.cpu.PC)
			}
			if threwAt < 0 && emu.cpu.PC == nextMenuLoopPC && i >= 1 {
				threwAt = i
			}
		}
		// threwAt>=0 means ours returned to the NextZXOS menu loop — the 590
		// error aborted the program. A pre-throw dump needs gf < threwAt.
		t.Logf("game phase: %d frames; first return-to-menu($0C90) at frame %d", gf, threwAt)
	}

	// ZXFX_PHASE=errtrap: press difficulty '1', then per-instruction watch
	// ERR_NR ($5C3A) — the Sinclair error-number sysvar. When it flips to an
	// error code (the "Integer out of range" raise), capture the instruction
	// trail (where it was raised = provenance), the CPU file, and the
	// calculator-stack top (STKEND-5.. = the value being range-checked). This
	// lands directly on the offending value + the routine that raised 590.
	if os.Getenv("ZXFX_PHASE") == "errtrap" {
		hold([][2]int{{3, 0x01}}, 6) // '1'
		gf := 30
		if v := os.Getenv("ZXFX_GAMEFRAMES"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				gf = n
			}
		}
		const ringN = 48
		ring := make([]uint16, 0, ringN)
		pushRing := func(pc uint16) {
			if len(ring) < ringN {
				ring = append(ring, pc)
			} else {
				copy(ring, ring[1:])
				ring[ringN-1] = pc
			}
		}
		type errEvt struct {
			frame              int
			pc                 uint16
			oldNR, newNR, a    byte
			bc, de, hl, sp, sk uint16
			calc               [10]byte
			trail              []uint16
		}
		var events []errEvt
		curFrame := 0
		lastNR := emu.mem.Read(0x5C3A)
		// Capture register + memory context at the raise site (the instruction
		// just before RST 8 in the trail). Default $1FF9; override via env.
		trapPC := uint16(0x1FF9)
		if v := os.Getenv("ZXFX_TRAPPC"); v != "" {
			if n, err := strconv.ParseUint(v, 16, 16); err == nil {
				trapPC = uint16(n)
			}
		}
		type rgw struct {
			frame                   int
			pc                      uint16
			a                       byte
			bc, de, hl, ix, iy, sp  uint16
			atHL, atDE, calc, stkfp [12]byte
		}
		var raises []rgw
		win := func(base uint16) [12]byte {
			var b [12]byte
			for i := range b {
				b[i] = emu.mem.Read(base - 5 + uint16(i))
			}
			return b
		}
		emu.cpu.AddPreFetchHook("errtrap", func(pc uint16) {
			pushRing(pc)
			if pc == trapPC && curFrame >= 20 && len(raises) < 8 {
				sk := uint16(emu.mem.Read(0x5C65)) | uint16(emu.mem.Read(0x5C66))<<8
				raises = append(raises, rgw{curFrame, pc, emu.cpu.A,
					emu.cpu.BC(), emu.cpu.DE(), emu.cpu.HL(), emu.cpu.IX, emu.cpu.IY, emu.cpu.SP,
					win(emu.cpu.HL()), win(emu.cpu.DE()), win(sk), win(uint16(0x5C92 + 5))})
			}
			nr := emu.mem.Read(0x5C3A)
			if nr != lastNR {
				if nr != 0xFF && len(events) < 16 {
					sk := uint16(emu.mem.Read(0x5C65)) | uint16(emu.mem.Read(0x5C66))<<8
					var calc [10]byte
					for i := range calc {
						calc[i] = emu.mem.Read(sk - 5 + uint16(i))
					}
					tr := make([]uint16, len(ring))
					copy(tr, ring)
					events = append(events, errEvt{curFrame, pc, lastNR, nr, emu.cpu.A,
						emu.cpu.BC(), emu.cpu.DE(), emu.cpu.HL(), emu.cpu.SP, sk, calc, tr})
				}
				lastNR = nr
			}
		})
		for i := 0; i < gf; i++ {
			curFrame = i
			step()
			if emu.cpu.PC == nextMenuLoopPC && i >= 1 {
				break
			}
		}
		emu.cpu.RemovePreFetchHook("errtrap")
		t.Logf("captured %d ERR_NR transitions, %d raise-site hits", len(events), len(raises))
		for _, ev := range events {
			t.Logf("ERR_NR $%02X->$%02X @f%d PC=$%04X A=$%02X BC=$%04X DE=$%04X HL=$%04X SP=$%04X STKEND=$%04X calc(stkend-5..)=% X",
				ev.oldNR, ev.newNR, ev.frame, ev.pc, ev.a, ev.bc, ev.de, ev.hl, ev.sp, ev.sk, ev.calc)
			t.Logf("  trail(last %d PCs): %X", len(ev.trail), ev.trail)
		}
		for _, r := range raises {
			t.Logf("RAISE @f%d PC=$%04X A=$%02X BC=$%04X DE=$%04X HL=$%04X IX=$%04X IY=$%04X SP=$%04X",
				r.frame, r.pc, r.a, r.bc, r.de, r.hl, r.ix, r.iy, r.sp)
			t.Logf("  (HL-5..)=% X  (DE-5..)=% X  calc(STKEND-5..)=% X  FP-MEM=% X",
				r.atHL, r.atDE, r.calc, r.stkfp)
		}
		return
	}

	// ZXFX_PHASE=trace: press difficulty '1', then frame-by-frame watch the
	// NextZXOS sprite-attribute cache (RAM bank 8) — the data SPRITE AT reads.
	// Logs each frame's PC + the per-sprite X (uvec+s*16+0) for sprites 0..15,
	// flagging any X>319 (out of range — the seed of the 590 error). This pins
	// the exact frame a cache entry goes garbage, and whether it's a sudden
	// structural break (cache bug) or gradual drift (RNG).
	if os.Getenv("ZXFX_PHASE") == "trace" {
		hold([][2]int{{3, 0x01}}, 6) // '1'
		gf := 30
		if v := os.Getenv("ZXFX_GAMEFRAMES"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				gf = n
			}
		}
		// Capture every read at the cache 8K-offset $04DF, WHATEVER slot it is
		// mapped through (addr&$1FFF==$04DF) — i.e. the actual SPRITE AT cache
		// read, at the instant it happens, with the resolved physical bank + the
		// full MMU. If ours reads bank!=8 here (cache lives in bank 8), the
		// SPRITE AT routine paged the wrong bank → garbage X → 590.
		type cacheRead struct {
			frame, slot, bank int
			pc, addr          uint16
			val               byte
			mmu               [8]byte
		}
		var reads []cacheRead
		curFrame := 0
		// Filter on the SPRITE AT cache-read PC ($0E5E) — captures EVERY SPRITE
		// AT read across all frames (incl. zap()'s read for the firing alien),
		// with the resolved physical bank + offset + value + MMU at the instant.
		// Capture window: by default only the frames around the throw (>=23),
		// since a frame-0 NextZXOS memory-scan loop saturates earlier. Capture
		// reads that resolve to the cache bank (8) OR carry a large value (a
		// candidate out-of-range sprite coord), with the bank/offset/MMU/PC.
		fromFrame := 23
		if v := os.Getenv("ZXFX_FROMFRAME"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				fromFrame = n
			}
		}
		emu.mem.SetRAMReadHook(func(bank int, addr uint16, val byte) {
			if curFrame < fromFrame || len(reads) >= 400 {
				return
			}
			if bank != 8 && val < 0x40 {
				return // keep cache-bank reads + any large value
			}
			var m [8]byte
			for s := byte(0); s < 8; s++ {
				m[s] = emu.mem.GetMMU(s)
			}
			reads = append(reads, cacheRead{curFrame, 0, bank, emu.cpu.PC, addr, val, m})
		})
		defer emu.mem.SetRAMReadHook(nil)
		defer func() {
			t.Logf("captured %d candidate reads (frame>=%d, bank8 or val>=$40)", len(reads), fromFrame)
			for _, r := range reads {
				t.Logf("  READ f%d PC=$%04X bank=%d off=$%04X val=%d(=$%02X) MMU=%v",
					r.frame, r.pc, r.bank, r.addr, r.val, r.val, r.mmu)
			}
		}()
		ram8 := func() (func(int) byte, int) {
			p16, p17 := emu.mem.RAM8KPage(16), emu.mem.RAM8KPage(17)
			rd := func(off int) byte {
				if off < 0x2000 {
					return p16[off]
				}
				return p17[off-0x2000]
			}
			uvec := int(rd(0x1FFE)) | int(rd(0x1FFF))<<8
			return rd, uvec
		}
		for i := 0; i < gf; i++ {
			curFrame = i
			step()
			rd, uvec := ram8()
			var sb strings.Builder
			bad := false
			for s := 0; s < 16; s++ {
				lo := int(rd((uvec + s*16) & 0x3FFF))
				hi := int(rd((uvec + s*16 + 1) & 0x3FFF)) // NextZXOS X is 9-bit (lo + bit8 in +1?)
				x := lo | (hi&1)<<8
				fmt.Fprintf(&sb, " s%d=%d", s, x)
				if x > 319 {
					bad = true
				}
			}
			flag := ""
			if bad {
				flag = "  <-- X>319 OUT OF RANGE"
			}
			thrown := ""
			if emu.cpu.PC == nextMenuLoopPC {
				thrown = "  [THROWN -> $0C90]"
			}
			t.Logf("trace f%2d PC=$%04X uvec=$%04X MMU2=%d:%d X[%s ]%s%s",
				i, emu.cpu.PC, uvec, emu.mem.GetMMU(2), emu.mem.GetMMU(3), sb.String(), flag, thrown)
			if emu.cpu.PC == nextMenuLoopPC && i >= 1 {
				break
			}
		}
		return
	}

	dumpFXState(t, emu, out)
	t.Logf("FX dump written to %s (PC=$%04X) phase=%s", out, emu.cpu.PC, os.Getenv("ZXFX_PHASE"))
}

// dumpFXState writes the canonical "FX|" physical-state snapshot: 256 NextRegs
// (read-back path), the CPU file, and physical RAM 8K banks 0..31 — the same
// format nbi_state_dump.lua emits for MAME, for a byte-level diff.
func dumpFXState(t *testing.T, emu *emulator, path string) {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = fh.Close() }()

	var nr strings.Builder
	for r := 0; r < 256; r++ {
		fmt.Fprintf(&nr, "%02X", emu.nextRegs.ReadReg(byte(r)))
	}
	fmt.Fprintf(fh, "FX|NR|%s\n", nr.String())

	c := emu.cpu
	af := uint16(c.A)<<8 | uint16(c.F)
	afp := uint16(c.A_)<<8 | uint16(c.F_)
	bcp := uint16(c.B_)<<8 | uint16(c.C_)
	dep := uint16(c.D_)<<8 | uint16(c.E_)
	hlp := uint16(c.H_)<<8 | uint16(c.L_)
	fmt.Fprintf(fh,
		"FX|CPU|af=%04X bc=%04X de=%04X hl=%04X af_=%04X bc_=%04X de_=%04X hl_=%04X ix=%04X iy=%04X sp=%04X pc=%04X i=%02X im=%d\n",
		af, c.BC(), c.DE(), c.HL(), afp, bcp, dep, hlp, c.IX, c.IY, c.SP, c.PC, c.I, c.IM)

	hexbuf := make([]byte, 0, 16384)
	const hexd = "0123456789ABCDEF"
	for b := 0; b < 32; b++ {
		page := emu.mem.RAM8KPage(b)
		hexbuf = hexbuf[:0]
		for _, by := range page {
			hexbuf = append(hexbuf, hexd[by>>4], hexd[by&15])
		}
		fmt.Fprintf(fh, "FX|RAM|%03d|%s\n", b, string(hexbuf))
	}
	fmt.Fprint(fh, "FX|END\n")
}
