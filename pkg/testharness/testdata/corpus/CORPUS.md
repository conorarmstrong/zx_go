# Real-software compatibility corpus

Third-party ZX Spectrum programs, vendored here to boot in CI as a
faithfulness regression suite (see `../../corpus_golden_test.go`). Every entry
is redistributable under a verified license; the license text is in
`LICENSES/`. **No proprietary NextZXOS/ROM is used or shipped** — Next demos
load via the emulator's own ROM-independent `.nex` loader, and the classic
conformance snapshots run on the 48K model with the emulator's embedded
(redistributable) 48K ROM.

Retrieved 2026-07-08. Do not edit the binaries; if a golden changes, it must be
because emulator output changed, not because the program was altered.

## Next hardware demos (ModelNext, `LoadNEX`)

| File | Title / author | License | Source | SHA-256 |
|------|----------------|---------|--------|---------|
| `bin/zxnext_layer2_tilemap.nex` | zxnext_layer2_tilemap — Ben Baker | MIT, README-stated (`LICENSES/zxnext_layer2_tilemap.MIT.txt`) | https://github.com/benbaker76/zxnext_layer2_tilemap | `d1d3316ed8bafa1030584f164885133888f906a691164fa381ad43c692b6e9e9` |
| `bin/zxnext_tilemap.nex` | zxnext_tilemap — Ben Baker | MIT, README-stated (`LICENSES/zxnext_tilemap.MIT.txt`) | https://github.com/benbaker76/zxnext_tilemap | `bdd3086cca92f4a1c53e2cc146a3280dcd6d17060eacae935e96e9edee455b96` |
| `bin/SpecBong.nex` | SpecBong — Peter "Ped" Helcmanovsky | MIT (`LICENSES/SpecBong.MIT.txt`) | https://github.com/ped7g/SpecBong (release Part_12) | `021b85e36c9323ebe22181107f277e0347bce95ac5b06f2b95ffb583b3d7e10f` |

## Classic Z80/ULA conformance tests (Model48K, `LoadSnapshot`)

Kevin Watkins' ZXSpectrumNextTests — hardware conformance oracles that draw a
pass/fail result screen. All MIT (`LICENSES/ZXSpectrumNextTests.MIT.txt`), from
https://github.com/MrKWatkins/ZXSpectrumNextTests (`release/`).

| File | Test | SHA-256 |
|------|------|---------|
| `bin/ccffrm.sna` | CCF/SCF undocumented-flag stability per frame (Ped7g) | `0c11629c6b357c890ff073e91b05725bc00bc8d72cc36ac3bd0e228c35b7c009` |
| `bin/DIHalt.sna` | DI + HALT (halts forever with no NMI) | `644ae437cab4c426a260049487ff93e0a1983807602891273027e3d034fd1397` |
| `bin/z80bltst.sna` | Flags of IM2-interrupted block instructions (Ped7g/D. Banks) | `6d099eef1ed30f6b978cb1f49a21a7d91b66f526531b7d1058eb40f5c6bb24a7` |
| `bin/int_skip.sna` | Interrupt acceptance across long DD/FD prefix blocks (Ped7g) | `cfaeacda9a2266289da6e6f0e8d659b5c2ebef64ec915d7de9b2f3b857121065` |
| `bin/ULAvsSJS.sna` | Keyboard/joystick port read-back matrix (Ped7g) | `be48097a7cc02d4a961103df920202c5fe760558736b03e9a758fd1fe6969372` |

`bin/int_skip.sna` is booted twice: once on the default 48K held-INT model
(`mrk_int_skip`) and once with a faithful ~32T narrow /INT pulse
(`mrk_int_skip_narrowint`) — the timing the 128K/+3/Next use. The narrow-pulse
run is the integration guard for the `pkg/z80` frameIntPulse fix (see below).

## zxnDMA conformance (Next, tape-loaded)

| File | Test | SHA-256 |
|------|------|---------|
| `bin/zilogdma.tap` | Zilog/Z80 DMA transfer modes + timing (MrKWatkins) | `00fce5ea0a8331d435c67a9950b62d232ac7ade831125993107eb19881a645a8` |

`mrk_zilogdma` runs on **ModelNext** — the machine that has the zxnDMA the test
drives. The Next boots to 48K BASIC on the embedded 48K ROM (no proprietary
NextZXOS), the corpus types `LOAD""`, and `Harness.LoadTAP` injects each tape
block through a fast-load trap on the ROM's LD-BYTES ($0556). The test then
runs on the real DMA engine and draws the A->B / B->A transfer-mode grids and
the DMA timing rows. MIT (`LICENSES/ZXSpectrumNextTests.MIT.txt`).

The original golden (2026-07-09) showed **red cells**; the investigation found
three real zxnDMA gaps, all confirmed against `zxnext.vhd` and **FIXED the
same day**. The regenerated golden's readback line is byte-identical to the
test's own documented "TBBLue zxnDMA core 3.1.5" reference
(`3A3A1A03042A1A1A03D1041A1A03D508`) and the transfer-mode grids are green:

1. **Port $0B was not decoded.** The test defaults to DMA port **$0B**; our
   ULA routed the DMA only at **$6B**. The FPGA decodes BOTH
   (`port_dma_6b_io_en` / `port_dma_0b_io_en`, enables default on) and the
   accessing port latches `dma_mode` (`dma_mode <= port_0b_lsb`; `0 = zxn dma,
   1 = z80 dma`, zxnext.vhd:1778/1817; ports.txt 0x0b/0x6b). The ULA now
   routes both ports and re-latches the mode on every DMA read/write.
2. **z80 mode moves length+1 bytes.** In z80 mode LOAD/CONTINUE/auto-restart
   seed the byte counter with -1 (dma.vhd:664 "z80 dma loads -1"), and the
   transfer loop repeats while counter < block length — so a block moves
   length+1 bytes, the classic Z80 DMA convention the test expects. This was
   the "addresses one low" symptom: not a readback bug but a missing byte.
   (The same loop shows a zero length moves ONE byte, not 65536 — the old
   "0 = 65536" rule had no source and was corrected too.)
3. **$BF (Read Status Byte) was ignored.** It points the read sequence at the
   status register (dma.vhd:687), so the next port read returns the status
   byte ($3A / $1A), not whatever the cycling sequence held. The read cursor
   now mirrors the FPGA read FSM exactly, including the RD_STATUS fallback
   for an empty read mask.

Unit guards: `dma_z80mode_test.go` (z80-mode length+1 / counter-from--1 /
auto-restart / $BF / empty mask) and `TestULARoutesDMAPort0BAsZ80Mode`
(port-selects-mode routing). The pre-existing GHDL FPGA golden
(`fpga_golden_test.go`) still passes unchanged — zxn-mode behaviour is
untouched.

## 128K family (v1.6.1)

Until v1.6.1 every classic entry ran on the 48K. That is exactly how a
230-T-state error in the 128K display origin survived to v1.6.0: the renderer
and the contention model shared the same wrong origin, so a static screen
still looked right, and no vendored program ever ran on the machines that were
wrong.

| Golden | File | Model |
|--------|------|-------|
| `mrk_dihalt_128k` | `bin/DIHalt.sna` | 128K |
| `mrk_dihalt_plus3` | `bin/DIHalt.sna` | +3 |
| `mrk_ccffrm_128k` | `bin/ccffrm.sna` | 128K |
| `mrk_z80bltst_128k` | `bin/z80bltst.sna` | 128K |
| `mrk_int_skip_128k` | `bin/int_skip.sna` | 128K |

These are timing-sensitive programs and contention feeds instruction timing,
so their results move if the 128K/+2A/+3 contention window or raster geometry
changes. Treat them as **regression guards for that family, not hardware
oracles** — the goldens record what we produce, and correctness of the origin
itself rests on the FPGA derivation in `pkg/roms/timing.go`.

Two real bugs were found by adding them:

1. **A 48K snapshot on a 128K-family machine did not page the 48 BASIC ROM.**
   Only the RAM was restored, so the program ran against the 128K editor ROM
   and every ROM call (character set, print routines) produced garbage — the
   first `mrk_dihalt_128k` capture was illegible. Restoring a 48K snapshot now
   selects the 48 BASIC ROM and locks paging, as the 128K's own "48 BASIC"
   mode does (`memory.Restore48KPagingState`).

2. **The harness never applied the per-model frame-INT timing the GUI does.**
   `cmd/zx_go` configures a narrow /INT pulse for the 128K/+2A/+3/Next at
   construction; `pkg/testharness` did not, so the whole corpus ran the legacy
   held-INT approximation — a configuration no user sees, and the wrong one
   for judging INT-timing conformance. Both now read the mapping from
   `next.MachineTimingFor`, so they cannot drift.

   `mrk_int_skip_128k` is the visible payoff: it reports **`0 |OK |inhibits
   ISR`** for both the NOP and FD blocks, the correct hardware result, where
   the held-INT configuration gave `!ERR! allows ISR`. The 48K entries are
   unaffected (the 48K legitimately keeps the held-INT model, and its
   documented `!ERR!` limitation below still stands).

   One consequence to be aware of: `mrk_zilogdma` runs on the Next, so its
   golden moved when the Next harness picked up its real narrow pulse. The
   changed cell is a DMA timing measurement (`1A5705D594FE00` ->
   `1A650B0097FE00`); the documented core readback line
   (`3A3A1A03042A1A1A03D1041A1A03D508`) is unchanged. The new value is what
   the test measures under the faithful INT model; it has not been checked
   against hardware.

## Hardware exercised

- **zxnext_layer2_tilemap** — Layer 2 256x192 background composited under a
  tilemap layer (a full JRPG-style scene). The richest renderer of the set.
  Note the 2-pixel unpainted column down the left edge and the 1-pixel row
  across the top. That is the program's own NR$18 clip window, not a
  rendering fault: it writes x1=1, x2=254, y1=1, y2=254 while Layer 2 is
  still in 256x192, then switches to the 320x256 resolution, where the FPGA
  takes the X coordinates as 2-pixel units (video/layer2.vhd:129-135). x1=1
  therefore becomes column 2. The golden was regenerated on 2026-08-22 when
  the clip window was first honoured; before that Layer 2 drew full-frame
  however a program clipped it.
- **zxnext_tilemap** — tilemap layer with scrolling.
- **SpecBong** — hardware sprites + tilemap-text HUD over a Layer 2 playfield;
  captured at its pre-start frame (no input is injected in the slice).

- **ccffrm** — reads **"No error ✓"**: we pass the SCF/CCF undocumented-flag
  (bits 3/5) stability test.
- **DIHalt** — green border (the CPU stays HALTed with interrupts disabled and
  no NMI, as it should): pass.
- **z80bltst** — some pass/fail cells are red. Mixed; captured as-is.
- **int_skip** — reports **`!ERR! allows ISR`** for DD/FD prefix blocks.
  Investigated (2026-07-09): the original "interrupt accepted mid-prefix-chain"
  hypothesis was DISPROVEN — the model executes a chained DD/FD block as one
  atomic instruction (confirmed), so an interrupt cannot be taken mid-chain.
  The real cause is the classic 48K interrupt model: it uses the deliberate
  legacy "held-INT for the whole frame" approximation (`cmd/zx_go/main.go`,
  `IntPulseTstates == 0`), whereas the test expects a ~32T narrow /INT pulse
  that a long prefix block would span and lose. Making it pass would require
  migrating the classic 48K/128K INT model to a narrow pulse AND finer INT
  sampling across atomic instructions — a broad change to a proven,
  FPGA-bit-exact CPU that a conscious decision currently avoids. Left as a
  documented limitation; the golden guards the current behaviour. A related
  genuine narrow-pulse bug found during this hunt (an atomic instruction
  spanning the pulse window raised the INT late) WAS fixed in
  `pkg/z80` `frameIntPulse` — it improves the Next/128K/+3 narrow-pulse models
  but does not change this 48K golden.
- **int_skip (narrow /INT)** — the same test with a faithful ~32T narrow /INT
  pulse (`intAssert=58`, `intPulse=32`) instead of the held-INT approximation.
  Here the DD/FD/DDFD prefix blocks read **`0 !OK inhibits ISR`** — the correct
  hardware result — because the atomic prefix block spans the narrow pulse and
  the interrupt is lost, exactly as the frameIntPulse window fix models. This
  golden is the end-to-end guard for that fix: reverting it flips the blocks
  back to `43 !ERR! allows ISR` (verified). It represents the interrupt
  behaviour of the 128K/+3/Next, which the 48K memory model can host because
  int_skip is a ZX48/ZX128 program (a real 128K memory model renders 48K
  snapshots as garbage, so the memory stays 48K while only the INT timing is
  switched to the narrow pulse those machines use).
- **ULAvsSJS** — interactive keyboard test; the captured frame is the idle
  (no-key) state.

> Conformance goldens capture our CURRENT output, not a certificate of
> correctness. A red/`!ERR!` result is a real signal to investigate, and when a
> fix lands the affected golden must be regenerated with `-update`.

## Boot environment (Next demos)

The Next demos load through `Harness.LoadNEX` (parses the `.nex`, pages banks,
sets `PC`/`SP` from the header) — **not** NextZXOS's NEXLOAD. The bottom 16K
holds the emulator's embedded original 48K Sinclair ROM (redistributable under
Amstrad's permission, already vendored in `pkg/roms/data/48.rom`), standing in
for the OS so no proprietary NextZXOS file is used. These programs render
identically with a zero stub ROM — their output comes purely from Next
hardware — so the golden reflects hardware faithfulness, not ROM content.

## License notes

- SpecBong ships a standalone MIT `LICENSE`, reproduced verbatim.
- The two Ben Baker demos state MIT in their README rather than a standalone
  `LICENSE` file. `LICENSES/` reproduces the README statement, the author
  attribution, the credited contributors, and the full MIT text so the
  required notice travels with the binary.

## Adding to the corpus

1. Verify the program's license explicitly permits redistribution; add its
   text to `LICENSES/`.
2. Download the binary to `bin/`, record size + SHA-256 in the table above.
3. Add an entry to `corpusPrograms` in `../../corpus_golden_test.go` and run
   `go test ./pkg/testharness -run TestCorpus -update` to capture its golden.
4. Eyeball the generated `golden/<name>.png` to confirm it renders correctly
   (not garbage) before committing.
