# zx_go — Roadmap

## What this is

zx_go is a hardware-faithful Sinclair emulator written in Go that
supports the **ZX80**, **ZX81**, the classic 48K / 128K / +2 / +2A / +3
Spectrums, and the **Spectrum Next**.

**v1.2.0** added the ZX80 and ZX81 (`pkg/zx8x`): faithful CPU-generated
video (A15 NOP-substitution video fetches, R-bit-6 interrupt with
refresh-during-HALT, the ZX81 SLOW-mode NMI border generator), the ZX8x
keyboard, and `.P` / `.O` program loading; plus the **Pentagon 128**, the
**TR-DOS / Beta Disk** interface (`pkg/betadisk`), **quick save/load** state
slots, and the completed **zxnDMA** (IO endpoints, prescaler timing, read-back).

This is the project roadmap: the **current state**, the ordered
**open-work** backlog, the hardware-feature **catalogue**, and the
**"do not regress" invariants**. Day-to-day completed work also lives in
git history, `CHANGELOG.md`, and the development log.

## Format

- `[ ]` = open work · `[~]` = partially complete
- **[blocker]** — must ship before v1.0
- **[should-do]** — strong pride-of-product item
- **[nice-to-have]** / **[v1.1]** — catalogued / deferred

---

## CURRENT STATE (2026-06-14 — v1.0 RC1)

**Working end-to-end (GUI + headless):** FPGA bootrom → TBBLUE splash →
NextZXOS welcome → menu → menu-item launch (Browser C:/ listing,
NextBASIC + acceptance), and the firmware config-menu machine selection
boots every personality. Classic 48K…+3 feature-complete.

**128K BASIC launch — FIXED (2026-06-14).** NextZXOS More…→128K BASIC now
renders the Sinclair "128" menu pixel-identical to the reference emulator
(was: black screen / NextZXOS welcome). Three faithful VHDL-backed fixes
found via the ours-vs-reference first-divergence audit — zero-fill cold RAM,
Multiface-3 paging readback ($7F3F/$1F3F), Layer 2 port $123B readback.
The last open *functional* Next bug; see the Open-work entry + the development log.
`./bin/zx_go --next` works out of the box; with no ROMs installed,
picking Next offers a download from the official Spectrum Next distro.

**NextZXOS "Guide" — FULLY WORKING (2026-06-09).** The Guide now renders
pixel-perfectly (matching the reference emulator — title, intro, full menu)
AND is interactive (DOWN/UP scroll; a down-then-up round-trip returns to
the byte-identical index). Five root causes, all fixed (committed, TDD'd,
no regression): (1) the **zxnDMA** was a fixed-7-step stub, but NextGuide
drives the real variable-length **Z80-DMA WR-group** protocol on IO port
`$6B` → the stub ran a 3709-byte garbage transfer that corrupted RAM →
the `$D700` slide; implemented the faithful protocol. (2) tilemap
**textmode** (NR$6B bit 3, 1bpp tiles, pixel `(attr&0xFE)|bit`) was
unimplemented → garbage tiles. (3) the Guide runs **80-column** mode =
640px wide; built the **640px display path** (the tilemap renders native
at 640px over the doubled 320px base). (4) the tilemap **transparency
nibble** was hardcoded `$0F` but NR$4C=`$08` → text was wrongly
transparent (ULA bled through); wired the live NR$4C. (5) **NR$1E/$1F
active video line** was unimplemented → after rendering, NextGuide (DI'd)
spun in a raster-wait loop polling NR$1F forever (a 7-instruction hang);
implemented `ULA.ActiveVideoLine()` from the T-state position and wired
the live NR$1E/$1F reads → the wait exits and the Guide is interactive.
The long crash-hunt's divMMC-bank-walk / chain-pointer theories were
downstream symptoms — see the development log.

---

## Open work (ordered)

- [x] ~~**[bug] 128K BASIC launch → black screen / welcome**~~ **DONE
  (2026-06-14).** Resolved via a rigorous ours-vs-reference first-divergence
  audit (shared clock = guest FRAMES $5C78; ZX_GO_TDIFF_AT + frame_dump.lua
  + cmp_fd.py; launch-phase PC-stream diff from the MF NMI $0066). Earlier
  $2009/IM-1 and D62/$5Dxx theories were downstream symptoms. Three faithful,
  VHDL-backed, TDD'd fixes: (1) **cold RAM now ZERO-fills** (was a $C0FFEE
  pseudo-random hack masking a since-fixed banking bug — diverged all 256
  banks; memory.go + cold_ram_test.go). (2) **Multiface-3 paging readback**:
  IN $7F3F→$7FFD, $1F3F→$1FFD when the MF is active (multiface.vhd:43-44,
  zxnext.vhd mf_port_dat mux) — ours returned open-bus $FF, flipping a
  `cp $04; jr nz` at MF-ROM $01F6 into the abort path; pkg/ula/ula.go +
  multiface_readback_test.go. **This made the Sinclair 128 menu render**
  (AltROM revealed NR$8C=$80, 128-ROM keywait $3683). (3) **Layer 2 port
  $123B readback** (zxnext.vhd:2822 port_123b_rd_dat) — open bus left
  NR$69=$C0 (Layer 2 visible) bleeding striping into the top border; now
  NR$69=$00 and the menu is PIXEL-IDENTICAL to the reference emulator, stable over a
  3000-frame post-launch soak. Boot / Browser / NextBASIC / 48K all
  unaffected; full suite + golangci-lint v2 + vet green. See the development log.

> Status (2026-06-09): the **hardware-feature sweep is complete** — every
> tractable Next video/audio/NextReg gap has been implemented (see the
> catalogue below; the large/subtle/out-of-scope ones carry a [⊘]
> decision + rationale). What remains here is the **dev-tooling /
> research / user-driven backlog** — not emulation-correctness gaps:

- [~] **[blocker] 30-minute GUI stability session** — HEADLESS PROXY
  PASSED: a 50 000-frame headless soak (≈17 min real-time, 3.57 B
  instructions) ran clean — no crash/panic/leak, steady ≈720 fps, final
  frame still the pixel-perfect Guide (md5 8c1eaaedf47e). The interactive
  GUI clicking part is still USER-DRIVEN (I can't drive the windowed app),
  but the underlying emulation is verified stable over a long run.
- [x] ~~#108 NR spec audit remainder~~ DONE — audited the NR read-back
  shapes against the FPGA; found + fixed a real bug (NR$05 re-encoded to a
  fictional layout citing a non-existent VHDL line — the only real read is
  zxnext.vhd:5897 = the input layout, so the mask is now `val&0xFA`).
  TestSpec_NR05_BitReorderingOnRead corrected.
- [⊘] **[nice-to-have] #243/#245 compare-foreign bisect UX** + #244
  the reference emulator DZRP RunToInstruction polish — debugger ergonomics.
- [⊘] **[v1.1] #237/#239 GHDL gate-level oracle testbench** — research;
  the CPU-conformance piece already proved its point.
- [⊘] **[v1.1] #229 direct-boot (no-firmware) path polish**; #226
  direct-core boot default; #240 provenance phase 2b — boot-path polish.

## Unimplemented hardware features (catalogued — 2026-06-09 code scan)

Deferred / stubbed / approximated hardware behaviours found in the
source. Most are [v1.1] / [nice-to-have]; the 640px display (above) is
the active one. Line refs are approximate (re-grep before starting).

### Video
- [x] ~~Layer 2 320×256 / 640×256 hi-res modes~~ DONE (all TDD'd, Guide
  byte-identical). Render: column-major addressing (320×256 8bpp
  `renderColumnMajor`; 640×256 4bpp `renderColumnMajor4bpp`, high nibble =
  left pixel) + LineWidth/LineHeight. Registers: NR$70 wiring moved into
  WireLayer2 (bits 5:4 → SetResolution, bits 3:0 → SetPaletteOffset, offset
  added mod-16 to each pixel's high nibble per layer2.vhd:203, offset 0 =
  identity). Compositor: ComposeScanline skips a hi-res L2 in the 256-wide
  pass; new ComposeWideLayer2Row colours the 320/640 L2 → RGBA;
  HiResLayer2Active/Layer2Width drive the dispatch; ULA.renderHiResLayer2
  overlays the wide L2 over the base frame (pixel-doubling for 640),
  mirroring renderWide. Additive — resolution 0 keeps the verified path.
  Visible window stays 240 rows (top-aligned, the same window every mode
  uses; the bottom 16 of the 256 hi-res lines are off-window overscan).
  TDD: TestLayer2_640ColumnMajor4bpp, TestLayer2_PaletteOffset640,
  TestComposeWideLayer2Row320, TestRenderHiResLayer2DispatchesWidePass.
  Follow-up (nice-to-have): end-to-end screenshot-oracle check vs
  the reference emulator once a hi-res-L2 test program is to hand (layer2.go,
  compositor.go, ula.go).
- [x] ~~NextReg transparency colour~~ DONE — global transparency is
  **NR$14** (FPGA nr_14, default `$E3`), wired live into
  `compositor.SetTransparency`. **NR$4A fallback DONE too**: the
  RGB332-expanded fallback (next.go) is shown where every layer is
  transparent (compositor `paintBase`); TDD via
  `TestComposeScanlineSULStencilAndFallback`.
- [x] ~~Layer priority + SUL per-pixel "below" stencil~~ DONE — the ULA's
  16-colour palette is threaded to the compositor (`SetULAPalette`) so it
  resolves the ULA transparency colour (u.palette[NR$14] when NR$14 < 16;
  inert for the default $E3, so the verified compositing is byte-identical).
  With that: the **SUL per-pixel stencil** (Layer 2 shows through a
  transparent ULA pixel, `paintULAStencil`) and the **per-pixel Layer 2
  priority bit** (NR$44 bit 7 captured in the palette, `HasPriority`,
  promotes L2 above ULA+TM in SUL via `paintL2Priority`; also fixes the
  NR$44 priority read-back). Tilemap-over-ULA priority (on_top + the
  ulatm_rgb mix) was already modelled. TDD:
  `TestComposeScanlineSULStencilAndFallback`,
  `TestComposeScanlineSULLayer2Priority`, `TestPalettePriority`
  (compositor.go, palette.go, ula.go, next.go).
- [x] ~~Tilemap advanced features~~ DONE — per-tile **mirror X/Y +
  rotate**, **pixel scroll** (NR$2F/$30/$31), and **clip window** (NR$1B,
  X doubled in 80-col) all FPGA-faithful, TDD `TestTilemapMirrorRotate` /
  `TestTilemapScroll` / `TestTilemapClip` (tilemap.go).
- [x] ~~Sprite rendering~~ DONE — mirror/rotate, scale (1/2/4/8×), 8bpp,
  N6, Y-MSB, NR$75-$79 auto-increment (FPGA sprites.vhd:437/813/968), the
  **$303B status port** (collision + max-per-line, clear-on-read), and
  **anchor groups** (composite/unified relative sprites). TDD: TestSprite*,
  TestSpriteCollision, TestNextSpritePortRouting, TestSpriteAnchorRelative.
- [x] ~~Sprite 8bpp pattern mode~~ DONE — byte-4 bit 7 selects 8bpp (256
  bytes/pattern, 1 byte/pixel); palette offset added to the high nibble
  per FPGA sprites.vhd:968. Also fixed the 4bpp palette offset (was
  pixel+offset, is offset<<4|pixel). TDD `TestSprite8bpp` /
  `TestSprite4bppPaletteOffset`.
- [x] ~~Sprite auto-increment register aliases $75-$79~~ DONE — NR$75-$79
  apply attribute bytes 0-4 (shared with $35-$39) and auto-increment the
  sprite index, TDD `TestSpriteAttrAutoIncrement` (wire.go).

### Copper
- [x] ~~Per-T-state raster-precise Copper execution~~ DONE — added the
  per-T-state beam-position model (ULA.BeamPosition, TDD) and stepped the
  Copper at end-of-line hpos so WAITs release on the correct scanline
  (TestEndOfLineHposReleasesScanlineWaits). Scanline-precise — the
  achievable precision for a per-scanline renderer (full per-pixel hpos
  would need per-pixel rendering).
- [x] ~~Copper VBL auto-restart timing~~ DONE — StartOnVBL resets the
  program counter to 0 at the top of each frame (raster wrap), TDD
  `TestStartOnVBLRestartsEachFrame` (copper.go).

### CPU / memory / timing
- [x] ~~Per-instruction contention at turbo speeds (7/14/28 MHz)~~ DONE —
  the contention stall magnitude is now scaled by the speed multiplier
  (a 6-ULA-cycle hold = 6*N CPU T-states at N×), TDD
  TestTurboContentionScaling; ×1 at 3.5 MHz so the boot is byte-identical
  (memory.go).
- [x] ~~NR$8E port-7FFD bit-4 write gate in config mode~~ DONE (non-gap) —
  verified against the FPGA: port $7FFD writes are gated ONLY by
  port_7ffd_locked (= bit 5; zxnext.vhd:3650-3652 `port_7ffd_wr = '1' and
  port_7ffd_locked = '0'`), NOT by config mode. OUR code already implements
  the bit-5 lock-drop (PageMemory + PagingEnabled, tested by
  TestPagingTracerReportsLockDroppedWrites). No config-mode-specific gate
  exists on real hardware; the earlier deferral note misread it.
- [x] ~~NextReg $02 bit 0 soft-reset latching~~ DONE — the NR$02 soft
  reset is fully implemented and load-bearing (WireReset, wire.go:1479+):
  Z80 /RESET semantics (PC/I/R/IFF/IM/HALT cleared, SP/IX/IY survive), the
  3-bit reset_type shift-history (zxnext.vhd:1736), the Alt-ROM
  staged-nibble promote (the "latching", :2255), paging reset to ROM0/RAM0,
  divMMC SPI + entry-point re-arm, and the config-mode FPGA-bootrom re-arm
  the firmware's config-menu machine selection depends on. All FPGA-derived
  and lockstep-verified vs the reference emulator. The one documented simplification —
  keeping NR$82-$89 as-is rather than a per-register reset-type-conditional
  reset — is hardware-faithful for the boot (preserves nr_03 for free) and
  unused by application software.
- [x] ~~ULA per-scanline render refactor~~ DONE (moot) — the copper-MOVE
  timing gap doesn't apply: the ULA inner screen is built from the fixed
  classic palette, screen RAM, and the already-per-scanline border (port
  $FE), none copper-changeable via a NextReg MOVE; the compositor layers
  are already per-scanline. A refactor would only matter if the ULA
  honoured the Next ULA palette (a separate, unimplemented feature, not a
  timing bug) (ula.go).

### Peripherals
- [x] ~~Joystick I/O mode (NR$0B)~~ DONE — the NR$0B register is modelled
  exactly (writable mask $B1 = FPGA bits 7,5:4,0; TestNR0BJoyIOModeMask).
  The I/O-mode BEHAVIOUR (repurposing joy pins as GPIO/UART for hardware
  add-ons) is out of scope, like the UART (wire.go:117).
- [x] ~~DISCiPLE FDC Read Track / Write Track ($E0 / $F0)~~ DONE — Read
  Track streams the raw track image (Track.Bytes); Write Track parses the
  format stream into sectors and rebuilds the track (Disk.FormatTrack). TDD
  TestFormatTrackAndBytes / TestParseFormatStream / TestDisciple_ReadWriteTrack.
- [x] ~~Interface 1 RS-232 + SinclairNET~~ DONE (scope) — the microdrive
  (IF1's primary function) is fully modelled; RS-232/SinclairNET bit-bang
  an external serial device (out of scope like the UART) and the CTR WAT
  CPU-stall is unnecessary with emulator-driven microdrive timing (if1/ula.go).
- [x] ~~UART / ESP Wi-Fi networking~~ DONE (scope) — the register interface
  (NR$A8 data / NR$A9 status, TX/RX FIFOs, AT-command responder) is fully
  implemented + wired; real Wi-Fi networking (live TCP/IP) is out of scope
  for a reference emulator (uart/doc.go).
- [x] ~~RTC battery-backed persistence~~ DONE — the DS1307's 56-byte NVRAM
  (regs 0x08-0x3F) is modelled + persisted across runs via SetPersistPath;
  TDD TestI2C_NVRAMWriteReadBack / TestRTCNVRAMPersists (rtc/rtc.go).

### Audio / misc
- [x] ~~AY volume curve~~ DONE — replaced the uniform 3 dB/step
  approximation with the measured AY-3-8912 levels (FUSE table, scaled to
  the mixing headroom); TDD `TestVolumeCurveIsMeasured` (pkg/ay/ay.go).
- [x] ~~RZX competition-mode DSA signing~~ DONE — Sign/Verify compute a DSA
  signature over the recording (SHA-1 digest, sign-end block 0x21) per the
  RZX security model; tamper-detection verified (TestRZXSignVerify). DSA +
  SHA-1 are mandated by the format (rzx/sign.go).

## Key invariants (do not regress)

- No hacks: zxnext.vhd + t80n VHDL = the oracle for every Next
  hardware question; the reference emulator = the behavioural oracle
  (NOT a clean NR read-back oracle).
- Bootable SD = FAT32: roms/next/sd.img (any real card image) OR the
  in-memory FAT32 image built from roms/next/sd (the runtime
  fallback + _tools/mksd, #227 — case-only 8.3 aliases matter, the
  firmware resolves menu.ini by short name). The OLD FAT16 builder
  output never booted.
- ZX_GO_RTC_FIXED=<RFC3339> freezes the guest clock — REQUIRED for
  deterministic menu-interaction tests (wall-clock RTC makes the
  menu clock-tick phase nondeterministic).
- NEVER write to the real install dir from tests — always
  installtest.RedirectConfig / ZX_GO_NEXT_ROM_DIR=t.TempDir().
  (A test without it destroyed roms/next/sd.img on every run —
  the recurring OOTB-regression root, fixed D31en.)
- Boot timings (real-time GUI): splash ~5s, NextZXOS welcome ~10s.
- Test harness: chords via --press-key 'caps+space@N'; fast menu
  card /tmp/menu_card.img (autoexec.1st dirent erased) boots to
  menu by frame ~1500.
