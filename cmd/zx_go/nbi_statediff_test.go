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

	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"image/png"
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

	// Trace every NR$8C (Alt-ROM control) write across boot+load+game to find
	// where ours arms the Alt-ROM read redirect ($80) that MAME never sets
	// (MAME's NR$8C stays $00 during NBI). PC isn't available here, so log the
	// value sequence; a single $80 with no following $00 is the leak.
	if os.Getenv("ZXFX_TRACE8C") != "" {
		orig8C := emu.nextRegs.OnWriteFn(0x8C)
		emu.nextRegs.SetOnWrite(0x8C, func(d *nextregs.Dispatcher, val byte) {
			if orig8C != nil {
				orig8C(d, val)
			}
			t.Logf("  NR$8C <- $%02X (PC=$%04X)", val, emu.cpu.PC)
		})
	}

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
		// At the ERR_NR flip, snapshot the NextBASIC variable area (VARS=$5C4B)
		// so we can walk it offline and read array a()/l() + scalars — to see
		// the corrupt element feeding the out-of-range sprite-number.
		var varsBuf []byte
		var varsAddr uint16
		grabVars := func() {
			varsAddr = uint16(emu.mem.Read(0x5C4B)) | uint16(emu.mem.Read(0x5C4C))<<8
			varsBuf = make([]byte, 2048)
			for i := range varsBuf {
				varsBuf[i] = emu.mem.Read(varsAddr + uint16(i))
			}
		}
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
		type seedRec struct {
			frame    int
			pc       uint16
			old, new uint16
		}
		var seedLog []seedRec
		// Read-ring: the last reads before the conversion. If ours pages the
		// wrong bank for the a()/l() array element read, the garbage source
		// shows here (bank + offset + value + PC at each read).
		type rdrec struct {
			bank int
			off  uint16
			val  byte
			pc   uint16
		}
		var rrDump []rdrec
		_ = rdrec{}
		// Capture reads to BANKS OTHER than the sysvar/calc banks (0,2,5) in the
		// throw frame — i.e. the banked-variable/array (a(),l()) reads — until
		// the conversion at $1EA0. These reveal the array element values feeding
		// the out-of-range result.
		emu.mem.SetRAMReadHook(func(bank int, addr uint16, val byte) {
			if curFrame < 24 || len(rrDump) >= 400 {
				return
			}
			if bank == 0 || bank == 2 || bank == 5 {
				return
			}
			rrDump = append(rrDump, rdrec{bank, addr, val, emu.cpu.PC})
		})
		defer emu.mem.SetRAMReadHook(nil)
		defer func() {
			banks := map[int]int{}
			for _, r := range rrDump {
				banks[r.bank]++
			}
			t.Logf("  non-sysvar reads in throw frame: %d (by bank: %v)", len(rrDump), banks)
			for i, r := range rrDump {
				if i < 120 {
					t.Logf("    rd PC=$%04X bank=%d off=$%04X val=%d($%02X)", r.pc, r.bank, r.off, r.val, r.val)
				}
			}
		}()
		win := func(base uint16) [12]byte {
			var b [12]byte
			for i := range b {
				b[i] = emu.mem.Read(base - 5 + uint16(i))
			}
			return b
		}
		type e9bctx struct {
			frame      int
			b          byte
			hl, de, bc uint16
		}
		var e9bHits []e9bctx
		// Single-step trace: starting at the FIRST $0E9B with B=$00 (the shared
		// anchor MAME also reaches), record the next N instructions (PC + HL +
		// regs) so we can diff against MAME's single-step from the same anchor.
		type stepRec struct {
			pc, hl, de, bc uint16
			a, f           byte
		}
		var stepTrace []stepRec
		var div925 string
		capturing := false
		// Anchor at the RND range-reduction helper $2A83 (reached during every
		// % RND call); capture the next N instructions with AF/BC/DE/HL so we can
		// diff ours' RND computation against MAME's $2A83 trace line-by-line. Arm
		// in the fire frame so we catch the throwing RND.
		emu.cpu.AddPreFetchHook("e9b", func(pc uint16) {
			// Anchor when the cache routine starts handling leader 21 (X-low write
			// $0920 with HL in $262C..$2636 = the leader-21 16-byte struct in slot 1).
			if pc == 0x0920 && emu.cpu.HL() >= 0x262C && emu.cpu.HL() <= 0x2636 &&
				!capturing && len(stepTrace) == 0 {
				capturing = true
			}
			if capturing && len(stepTrace) < 90 {
				stepTrace = append(stepTrace, stepRec{pc, emu.cpu.HL(), emu.cpu.DE(), emu.cpu.BC(), emu.cpu.A, emu.cpu.F})
			} else if capturing {
				capturing = false
			}
			if pc == 0x0925 && emu.cpu.HL() == 0x2631 && div925 == "" {
				p16 := emu.mem.RAM8KPage(16)
				nr8c := emu.nextRegs.ReadReg(0x8C)
				dmInfo := "no-divmmc"
				if pg, ok := emu.ula.NextDivMMC().(*divmmc.Pager); ok && pg != nil {
					hv, hok := pg.HandleRead(0x2631)
					dmInfo = fmt.Sprintf("divMMC pagedIn=%v mapram=%v HandleRead($2631)=($%02X,%v)",
						pg.IsPagedIn(), pg.MAPRAM(), hv, hok)
				}
				div925 = fmt.Sprintf("slot1 bank=%d  Read($2631)=$%02X  bank8[$0631]=$%02X  NR$8C=$%02X  %s",
					emu.mem.GetMMU(1), emu.mem.Read(0x2631), p16[0x0631], nr8c, dmInfo)
			}
		})
		defer func() {
			if div925 != "" {
				t.Logf("  DIVERGENCE $0925: %s", div925)
			}
			t.Logf("=== ours' single-step from cache-routine $0920 (leader 21) ===")
			for i, s := range stepTrace {
				t.Logf("  [%2d] PC=$%04X af=%02X%02X bc=%04X de=%04X hl=%04X", i, s.pc, s.a, s.f, s.bc, s.de, s.hl)
			}
		}()
		defer func() {
			t.Logf("=== ours' $0E9B (cache-addr-from-B) hits in the game: %d (MAME game had B=$00) ===", len(e9bHits))
			for _, h := range e9bHits {
				t.Logf("  $0E9B @f%d B=$%02X HL=$%04X DE=$%04X BC=$%04X", h.frame, h.b, h.hl, h.de, h.bc)
			}
		}()
		var lastSeed uint16 = 0xFFFF
		emu.cpu.AddPreFetchHook("errtrap", func(pc uint16) {
			pushRing(pc)
			if sd := uint16(emu.mem.Read(0x5C76)) | uint16(emu.mem.Read(0x5C77))<<8; sd != lastSeed {
				if lastSeed != 0xFFFF && len(seedLog) < 64 {
					seedLog = append(seedLog, seedRec{curFrame, pc, lastSeed, sd})
				}
				lastSeed = sd
			}
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
		// Watch writes into bank-8 cache bytes for alien leader 21 (uvec $04DF +
		// 21*16 = $062F..$0632): +0=X-low +2=X8/flags. Capture PC + value to find
		// who sets X[8] (the +256 that makes SPRITE AT(21,0)=288 not 32).
		type cwr struct {
			frame              int
			pc                 uint16
			addr               uint16
			val                byte
			a                  byte
			bc, de, hl, ix, iy uint16
		}
		var cacheWr []cwr
		var disasm962 []byte
		emu.mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
			if bank == 8 && addr >= 0x062F && addr <= 0x0632 && len(cacheWr) < 40 {
				cacheWr = append(cacheWr, cwr{curFrame, emu.cpu.PC, addr, val,
					emu.cpu.A, emu.cpu.BC(), emu.cpu.DE(), emu.cpu.HL(), emu.cpu.IX, emu.cpu.IY})
				if emu.cpu.PC == 0x0962 && disasm962 == nil {
					disasm962 = make([]byte, 40)
					for i := range disasm962 {
						disasm962[i] = emu.mem.Read(0x0920 + uint16(i)) // window $0920..$0947
					}
				}
			}
		})
		defer emu.mem.SetRAMWriteHook(nil)
		defer func() {
			t.Logf("=== bank-8 writes to leader-21 cache ($062F..$0632) ===")
			for _, w := range cacheWr {
				t.Logf("  @f%d PC=$%04X bank8[$%04X]=$%02X (%d)  A=%02X BC=%04X DE=%04X HL=%04X IX=%04X IY=%04X",
					w.frame, w.pc, w.addr, w.val, w.val, w.a, w.bc, w.de, w.hl, w.ix, w.iy)
			}
			if disasm962 != nil {
				t.Logf("  code bytes $0920..$0947: % X", disasm962)
			}
		}()
		// Optional: force SEED ($5C76/77) + FRAMES ($5C78-7A) to a fixed value
		// at frame 1, to test whether the throw is seed/RNG-dependent (changes)
		// or deterministic-structural (unchanged). ZXFX_SEED=<decimal>.
		forceSeed, seedOn := -1, false
		if v := os.Getenv("ZXFX_SEED"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				forceSeed, seedOn = n, true
			}
		}
		for i := 0; i < gf; i++ {
			curFrame = i
			step()
			if i <= 30 {
				nr15 := emu.nextRegs.ReadReg(0x15)
				t.Logf("  @f%d NR$15=$%02X overBorder(bit1)=%d  shipCacheY=%d",
					i, nr15, (nr15>>1)&1, func() int {
						p16, p17 := emu.mem.RAM8KPage(16), emu.mem.RAM8KPage(17)
						r8 := func(o int) byte {
							if o < 0x2000 {
								return p16[o&0x1FFF]
							}
							return p17[(o-0x2000)&0x1FFF]
						}
						uv := int(r8(0x1FFE)) | int(r8(0x1FFF))<<8
						return int(r8(uv + 64*16 + 1))
					}())
			}
			if i == 23 && varsBuf == nil {
				grabVars() // program context active (banking maps the vars)
			}
			if i >= 18 && i <= 27 {
				p16, p17 := emu.mem.RAM8KPage(16), emu.mem.RAM8KPage(17)
				ram8 := func(off int) byte {
					if off < 0x2000 {
						return p16[off&0x1FFF]
					}
					return p17[(off-0x2000)&0x1FFF]
				}
				uvec := int(ram8(0x1FFE)) | int(ram8(0x1FFF))<<8
				catX := func(n int) int { // SPRITE AT(n,0): 9-bit X from cache
					b0 := int(ram8(uvec + n*16 + 0))
					b2 := int(ram8(uvec + n*16 + 2))
					return b0 | (b2&1)<<8
				}
				catY := func(n int) int { return int(ram8(uvec + n*16 + 1)) }
				var sb strings.Builder
				for n := 0; n <= 35; n++ {
					fmt.Fprintf(&sb, " %d:%d", n, catX(n))
				}
				t.Logf("  @f%d CACHE SPRITE-AT(n,0) uvec=$%04X:%s", i, uvec, sb.String())
				// Player ship = sprite 64: cache X/Y + live engine X/Y/visible.
				if emu.nextSprites != nil {
					a := emu.nextSprites.Sprite(64)
					vis, ex, ey := false, int16(0), int16(0)
					if a != nil {
						vis, ex, ey = a.Visible, a.X, a.Y
					}
					t.Logf("  @f%d SHIP sprite64: cache X=%d Y=%d  | engine X=%d Y=%d vis=%v",
						i, catX(64), catY(64), ex, ey, vis)
					if i == 26 {
						x1, x2, y1, y2, set := emu.nextSprites.Clip()
						t.Logf("  sprite clip: x1=%d x2=%d y1=%d y2=%d set=%v", x1, x2, y1, y2, set)
						scan := make([]byte, 320)
						var rows []int
						for fy := 0; fy < 256; fy++ {
							for k := range scan {
								scan[k] = 0
							}
							emu.nextSprites.RenderScanline(fy, scan, 320)
							for x := 0; x < 16; x++ {
								if scan[x] != 0 {
									rows = append(rows, fy)
									break
								}
							}
						}
						t.Logf("  ship-column (X=0..15) covered at frame-Y rows: %v (Y=240 => expect ~240-255)", rows)
						for s := 0; s < 128; s++ {
							a := emu.nextSprites.Sprite(s)
							if a != nil && a.Visible && a.Y < 40 {
								t.Logf("  TOP-AREA visible sprite %d: X=%d Y=%d pat=%d ext=%v byte4=$%02X",
									s, a.X, a.Y, a.Pattern, a.Extended, a.Byte4)
							}
						}
					}
				}
				if i == 24 {
					for _, n := range []int{0, 7, 14, 21, 28} {
						var st strings.Builder
						for b := 0; b < 16; b++ {
							fmt.Fprintf(&st, " %02X", ram8(uvec+n*16+b))
						}
						t.Logf("  STRUCT sprite %d:%s", n, st.String())
					}
				}
			}
			if (i == 23 || i == 24 || i == 25) && emu.nextSprites != nil {
				var sb strings.Builder
				maxX := int16(-9999)
				for s := 0; s < 80; s++ {
					a := emu.nextSprites.Sprite(s)
					if a == nil {
						continue
					}
					if a.X > maxX {
						maxX = a.X
					}
					if s < 48 {
						fmt.Fprintf(&sb, " %d:%d", s, a.X)
					}
				}
				t.Logf("  @f%d sprite X (0..47):%s  [maxX over 0..79 = %d]", i, sb.String(), maxX)
			}
			if seedOn && i == 23 {
				emu.mem.Write(0x5C76, byte(forceSeed))
				emu.mem.Write(0x5C77, byte(forceSeed>>8))
			}
			if fv := os.Getenv("ZXFX_FRAMES"); fv != "" && i == 1 {
				if n, err := strconv.Atoi(fv); err == nil {
					emu.mem.Write(0x5C78, byte(n))
					emu.mem.Write(0x5C79, byte(n>>8))
					emu.mem.Write(0x5C7A, byte(n>>16))
				}
			}
			if emu.cpu.PC == nextMenuLoopPC && i >= 1 {
				break
			}
		}
		emu.cpu.RemovePreFetchHook("errtrap")
		if os.Getenv("ZXFX_PNG") != "" {
			img := emu.renderFrame()
			if fp, err := os.Create(os.Getenv("ZXFX_PNG")); err == nil {
				_ = png.Encode(fp, img)
				_ = fp.Close()
				b := img.Bounds()
				t.Logf("wrote frame PNG %s (%dx%d)", os.Getenv("ZXFX_PNG"), b.Dx(), b.Dy())
			}
		}
		// Walk the captured VARS buffer: dump number-array a()/l() elements +
		// scalars, decoding the Sinclair small-integer FP form [00,sign,lo,hi,00].
		if varsBuf != nil {
			decSI := func(b []byte) (int, bool) { // small-int FP -> value
				if len(b) < 5 || b[0] != 0 || b[4] != 0 {
					return 0, false
				}
				v := int(b[2]) | int(b[3])<<8
				if b[1] == 0xFF {
					v -= 65536
				}
				return v, true
			}
			t.Logf("VARS=$%04X (walking %d bytes)", varsAddr, len(varsBuf))
			t.Logf("  raw[0:64]=% X", varsBuf[0:64])
			t.Logf("  raw[64:128]=% X", varsBuf[64:128])
			for i := 0; i < len(varsBuf); {
				c := varsBuf[i]
				if c == 0x80 {
					break // end of vars
				}
				typ := c >> 5
				name := rune(c&0x1F) + 0x60
				switch typ {
				case 0b100: // number array
					if i+3 >= len(varsBuf) {
						i = len(varsBuf)
						break
					}
					total := int(varsBuf[i+1]) | int(varsBuf[i+2])<<8
					ndim := int(varsBuf[i+3])
					dims := []int{}
					p := i + 4
					for d := 0; d < ndim && p+1 < len(varsBuf); d++ {
						dims = append(dims, int(varsBuf[p])|int(varsBuf[p+1])<<8)
						p += 2
					}
					// elements start at p; dump first ~24, flag any > 255
					var sb strings.Builder
					n := 0
					for e := p; e+4 < i+3+total && n < 30; e += 5 {
						if v, ok := decSI(varsBuf[e : e+5]); ok {
							flag := ""
							if v > 255 || v < 0 {
								flag = "!"
							}
							fmt.Fprintf(&sb, " [%d]=%d%s", n, v, flag)
						} else {
							fmt.Fprintf(&sb, " [%d]=<fp %X>", n, varsBuf[e:e+5])
						}
						n++
					}
					t.Logf("  array %c() dims=%v:%s", name, dims, sb.String())
					i = i + 3 + total
				case 0b101: // long-name number — skip name chars then 5 bytes
					j := i + 1
					for j < len(varsBuf) && varsBuf[j]&0x80 == 0 {
						j++
					}
					i = j + 1 + 5
				case 0b011: // single-char number scalar: name + 5 bytes
					if v, ok := decSI(varsBuf[i+1 : i+6]); ok {
						t.Logf("  scalar %c = %d", name, v)
					}
					i += 6
				case 0b010: // string
					if i+2 >= len(varsBuf) {
						i = len(varsBuf)
						break
					}
					ln := int(varsBuf[i+1]) | int(varsBuf[i+2])<<8
					i += 3 + ln
				case 0b111: // FOR control var: name + 18 bytes
					i += 19
				default:
					i++
				}
			}
		}
		// Locate where the out-of-range value 379 ($017B) lives: scan the var
		// region (logical $4000-$7FFF = banks 5/2, plus sysvars $5B00-$5FFF) for
		// the raw 16-bit pair 7B 01 and the small-integer-FP form 00 00 7B 01 00.
		// X-PTR ($5C5F) marks the tokenised error position (which SPRITE param).
		xptr := uint16(emu.mem.Read(0x5C5F)) | uint16(emu.mem.Read(0x5C60))<<8
		var ctx [24]byte
		for i := range ctx {
			ctx[i] = emu.mem.Read(xptr - 8 + uint16(i))
		}
		t.Logf("X-PTR=$%04X tokens(xptr-8..)=% X", xptr, ctx)
		for a := 0x4000; a <= 0x7FFE; a++ {
			if emu.mem.Read(uint16(a)) == 0x7B && emu.mem.Read(uint16(a+1)) == 0x01 {
				t.Logf("  found 7B 01 (=379) @ logical $%04X bank=%d", a, emu.mem.GetMMU(byte(a>>13)))
			}
		}
		t.Logf("=== SEED mutations (PC = the RND routine writing $5C76/77) ===")
		for _, s := range seedLog {
			t.Logf("  @f%d PC=$%04X SEED $%04X -> $%04X", s.frame, s.pc, s.old, s.new)
		}
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
	nbanks := 32 // default covers banks 0-15 (16K); set ZXFX_BANKS=224 for high banks
	if v := os.Getenv("ZXFX_BANKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			nbanks = n
		}
	}
	for b := 0; b < nbanks; b++ {
		page := emu.mem.RAM8KPage(b)
		if page == nil {
			continue
		}
		hexbuf = hexbuf[:0]
		for _, by := range page {
			hexbuf = append(hexbuf, hexd[by>>4], hexd[by&15])
		}
		fmt.Fprintf(fh, "FX|RAM|%03d|%s\n", b, string(hexbuf))
	}
	fmt.Fprint(fh, "FX|END\n")
}
