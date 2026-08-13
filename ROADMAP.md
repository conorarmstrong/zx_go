# zx_go — Roadmap

## What this is

zx_go is a hardware-faithful emulator for the Sinclair 8-bit line, written
in Go: the **ZX80**, **ZX81**, the classic **48K / 128K / +2 / +2A / +3**,
the **Pentagon 128** clone, the **SAM Coupé**, and the **Spectrum Next**.

This file holds the **open-work backlog** and the **"do not regress"**
invariants. It is not a history: completed work lives in `CHANGELOG.md` and
in git. When an item is done, delete it — do not strike it through.

Every claim below cites a file (and line, where it pins a specific
behaviour). If you change one, re-check the citation.

## Format

- `[ ]` open · `[~]` partially complete · `[⊘]` catalogued, deliberately not doing
- **[correctness]** a faithfulness gap against real hardware
- **[product]** user-visible capability or confidence
- **[research]** the answer is unknown; needs evidence before it can be scoped

---

## CURRENT STATE (2026-08-13 — v1.8.18)

Every machine listed above boots and is interactive. The classic line is
mature. The Next cold-boots NextZXOS through the real FPGA chain to an
interactive desktop, and the individual hardware blocks are tested against
the FPGA VHDL.

**The honest weak point is Next game compatibility**, exactly as
`README.md` says. Blocks being faithful in isolation has not yet been
converted into "arbitrary `.NEX` titles run".

---

## Open work (ordered)

### 1. [product] Next game compatibility

`docs/compatibility.md` now holds **158 title rows: 10 Works, 14 Works
(caveat), 62 Boots (responds), 63 Boots, 1 Parses cleanly, 2 Known issue, 6
Untested.** It was 36 rows with 13 Untested before automated screening.

Screening (`TestScreenLocalTitles`, pkg/testharness) loads a title headlessly,
runs it, and measures the display window with the border cropped. A **Boots**
verdict means the guest's own code ran and drew its title or menu screen; it
does **not** mean the title was played, because no input is sent. That is the
remaining gap.

- [x] ~~Resolve the 13 Untested entries~~ — 8 resolved. The other 5 record why
  they could not be: no copy to hand (Jet Set Willy, The Hobbit, Baggers in
  Space, Lemmings +3), or no +3 disk loader in the harness yet (Where Time
  Stood Still +3).
- [x] ~~Drive input~~ — `Harness.ProbeInput` sends keys to a still screen and
  reports a material change, against a no-key control so self-animation is not
  credited to the keypress. 78 titles answer input.
- [x] ~~Bonanza Bros divergence~~ — closed, nothing to fix. Two independent
  reference emulators were checked: one blanks the screen after selecting
  Loader exactly as we do, the other never leaves the +3 menu. We match a
  reference precisely, so the earlier note that "we blank where the reference
  stays on the menu" was wrong — that reference is the outlier. The image is
  very likely not bootable. Control for the comparison: Captain Planet
  measures 27098 lit pixels in both that emulator and ours.
- [x] ~~Confirm the result-ID update at EOT~~ — settled from the published
  datasheet, and the current behaviour is right. The Result Phase Table's
  "C+1, R=01" row is qualified by its own preamble: it describes the *host*
  terminating a transfer, and "the termination must be normal". The +3 wires
  no Terminal Count, so neither holds, and ST1.EN is defined as "tried to
  access a sector beyond the final sector of the track. Will be set if TC is
  not issued after Read or Write". Leaving the ID at R+1 names the sector the
  controller tried and could not reach, which is the same statement EN makes.
  Pinned by TestDSResultIDAfterEOTIsNotTheHostTerminationCase so it is not
  re-litigated.
- [~] **Verify what the keypress did.** Partly automated, and no longer purely
  manual. A response proves a title is waiting rather than hung; it does not
  prove the title is playable. Rather than hand-write an expectation per
  title, `_tools/refdiff` (local-only) drives zx_go and a reference emulator
  through the same load-and-keypress sequence and compares the resulting
  6912-byte display file. **Six +3 disk titles verified so far**, two of them
  byte-identical.

  Known limit of the method: the reference runs in real time while we step
  frames, so titles that are still loading when the comparison is taken
  cannot be synchronised. It is reliable for titles that settle on a screen.
  Adidas Tie-Break is the one open case, where the reference sticks at 1447
  lit pixels regardless of how long it is given while a second reference
  loads the title fully, as we do.

  Remaining work is breadth: the tape and 128K classes have no equivalent
  harness yet.
- [x] ~~A +3 disk loader for the harness~~ — `Harness.InsertPlus3Disk` plus
  `.dsk`/`.edsk` screening (v1.8.4). Screening the disk class immediately
  surfaced three real loader bugs, all fixed; see the CHANGELOG.
- [x] ~~Copy-protection track layouts~~ — done. The image declares its own
  track length and the guest only ever reads by sector ID, so a track is now
  sized to what the image describes rather than to a nominal capacity. Of a
  250-image sample, 1 still fails to parse, and that one is a truncated dump.
- [x] ~~Investigate Warhawk~~ — not a fault. It calls NextZXOS at runtime, so
  bank injection cannot host it; through the genuine NEXLOAD path it launches
  and renders (`TestNexloadOSGamesIfPresent`, cmd/zx_go). The screening
  harness had recorded a working title as broken, which is now impossible:
  a blank frame from bank injection classifies as *inconclusive*, never a
  fault.

**The Next half now exists.** `TestNexloadSDGames` (cmd/zx_go) drives every
`.nex` on the SD card through the genuine NextZXOS NEXLOAD path — the only one
that can host a title calling the OS at runtime. **All 12 launch and render.**

**Every Next game on the SD card is now accounted for**: every title directory
holding a launchable program runs — 12 `.nex` through NEXLOAD, 5 NextBASIC
programs through the Command Line, and NEXTipede from tape. Pogie and THEH
hold only assets and a 48K-sized `.snx`, which cannot represent a Next game's
banked state, so there is nothing to launch.

TX-1696's own entry records why it needed its assets at the card root: it
opens `C:/common/...` by absolute path. That is unrelated to the working
directory and is not an emulator fault.

That is what items 2 and 4 were waiting on, and the answer it gives is
"no evidence either is needed": fourteen Next programs run without the zxnDMA
interrupt/match logic or exact Copper MOVE timing being modelled. Neither is
now blocked; both are simply unmotivated.

### 2. [correctness] zxnDMA interrupt / match logic and bus arbitration

**Unblocked, and unmotivated.** The transfer engine, prescaler and cycle
timing are complete and spec-checked. Not modelled: the interrupt/match logic
and DMA-vs-CPU bus arbitration (`pkg/next/dma/dma.go`).

The Next corpus now exists — 10 SD games driven through NEXLOAD, 9 rendering —
and none of them needs this. That is the evidence the item was waiting for, and
it argues for leaving it alone until a title actually demands it.

### 3. [product] Windows ARM64 has never been run

Since v1.8.1 it builds with llvm-mingw and publishes an artifact, so the
toolchain problem is solved. Nobody has launched the binary. Compiling
does not prove the OpenGL path works on Windows-on-ARM, which is why the
release matrix still marks it `experimental` and `README.md` carries the
caveat. One run on real hardware settles it.

### 4. [research] Copper cycle accuracy

**Unblocked, and unmotivated.** Since v1.6.4 the Copper is stepped in 8-pixel
segments, which is exact for `WAIT` — the hardware threshold is `x<<3 + 12`,
so 8 pixels *is* its resolution. What remains is `MOVE` landing mid-segment
(`pkg/next/copper/copper.go:19`).

The 10 Next games now screened render correctly without it, so there is still
no observed case where it matters. Leave it until one appears.

---

## Catalogued — deliberately not doing

Kept so they are not re-proposed.

- [⊘] **Real Wi-Fi / TCP networking.** The UART register interface, FIFOs
  and AT-command responder are implemented; a live network stack is out of
  scope for a reference emulator (`pkg/next/uart/doc.go`).
- [⊘] **Interface 1 RS-232 / SinclairNET.** The NET port is decoded
  (`pkg/if1/ula.go:8`); bit-banging an external serial device is out of
  scope, same rationale as the UART. The microdrive, the IF1's primary
  function, is fully modelled.
- [⊘] **NR$0B joystick I/O mode.** The register is modelled exactly
  (writable mask `$B1`, `pkg/next/wire.go:125`); repurposing joystick pins
  as GPIO for external add-ons is out of scope.
- [⊘] **SAM MIDI, clock and SD/IDE ports** — writes ignored
  (`pkg/sam/io.go`).
- [⊘] **Beta Disk density bit** (status bit 5) — TR-DOS is always MFM
  (`pkg/betadisk/interface.go`).

---

## Do not re-derive

Solved problems whose answers were expensive to find.

- **Opus Discovery ROM paging** is an **M1 address trap delayed one
  fetch**: in at `$0008`, `$0048`, `$1708`; out at `$1748`. The tell is
  placeholder bytes in the Opus ROM copying the Spectrum's at those
  addresses. Transfers are **NMI-per-byte**, not DMA, and the byte spacing
  is load-bearing. The interface is **memory-mapped, not port-mapped** — do
  not go hunting for a port map. Full record in `pkg/opus/README.md`.
- **"+3 disk copy protection we cannot represent" was never real.** Twelve
  titles were recorded as blocked by it, then five, and both were wrong: the
  cause was inferred from the images carrying unusual track layouts, never
  from evidence that the layout caused the failure. A +3 reference emulator
  loads every one. Four controller bugs were behind them: EDSK `ST1.DE` read
  as an ID-field CRC error when `ST2.DD` attributes it to the data field;
  oversized (N=6) sectors refused instead of streamed across the index hole;
  the sector ID compare ignoring the size code; and end of cylinder reported
  as a normal termination when the +3, asserting no Terminal Count, makes it
  an abnormal one. First three in v1.8.17, the fourth in v1.8.18. A fifth
  candidate, `ST3.RY` machine-wide rather than per-drive, was tried and
  **reverted** — it fixed nothing and its only evidence was an artefact of
  the reference emulator wiring a single drive. **Before blaming a disk
  format, run the image on a reference** — `ZX_GO_FDC_TRACE=1` then shows
  which command is answered wrongly.
- **The bootable SD image is FAT32.** An older FAT16 builder never booted.
- **The 128K BASIC launch bug is closed** (Multiface-3 `$7F3F`/`$1F3F`
  paging readback, Layer 2 `$123B` readback, zero-filled cold RAM). Do not
  re-chase the superseded `$2009` / IM-1 / `$5Dxx` theories.

---

## Key invariants (do not regress)

- **No hacks.** `zxnext.vhd` + the t80n VHDL are the oracle for every Next
  hardware question. Where behaviour is ambiguous, the VHDL wins over
  folklore and over other emulators.
- **Bootable SD must be FAT32** — either a real card image at
  `roms/next/sd.img`, or the FAT32 image built in memory from
  `roms/next/sd` at runtime. Case-only 8.3 aliases matter: the firmware
  resolves `menu.ini` by short name.
- **`ZX_GO_RTC_FIXED=<RFC3339>` freezes the guest clock**
  (`cmd/zx_go/next.go:835`). Required for deterministic menu-interaction
  tests — a wall-clock RTC makes the menu's clock-tick phase
  nondeterministic.
- **Never write to the real install directory from tests.** Use
  `installtest.RedirectConfig`
  (`pkg/next/install/installtest/installtest.go:30`), which redirects via
  `ZX_GO_NEXT_ROM_DIR` (`pkg/next/install/install.go:40`). A test without
  it once destroyed `roms/next/sd.img` on every run, and was the recurring
  out-of-the-box regression.
- **Test-harness chords** are `--press-key 'caps+space@N'` / `'sym+p@N'`,
  which press every named key together at frame N (`cmd/zx_go/debug.go:674`).
- **Rough real-time GUI boot timings:** splash ~5s, NextZXOS welcome ~10s.
  A large deviation means something regressed.
