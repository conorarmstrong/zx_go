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

## CURRENT STATE (2026-08-11 — v1.8.2)

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

`docs/compatibility.md` now holds **150 title rows: 9 Works, 12 Works
(caveat), 78 Boots (responds), 33 Boots, 1 Parses cleanly, 10 Known issue, 7
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
- [ ] **Verify what the keypress did.** A response proves a title is waiting
  rather than hung; it does not prove the title is playable. Closing that gap
  means per-title expectations — what screen should follow which key — which
  is genuine manual work and cannot be inferred.
- [x] ~~A +3 disk loader for the harness~~ — `Harness.InsertPlus3Disk` plus
  `.dsk`/`.edsk` screening (v1.8.4). Screening the disk class immediately
  surfaced three real loader bugs, all fixed; see the CHANGELOG.
- [x] ~~Copy-protection track layouts~~ — done. Those tracks overlap sectors:
  one oversized ID covers the region the ordinary sectors occupy. A flat
  byte-stream model cannot overlap them, but the image declares its own track
  length and the guest only ever reads by sector ID, so the track is now sized
  to what the image describes. Of a 250-image sample, 1 still fails to parse,
  and that one is a truncated dump.
- [x] ~~Investigate Warhawk~~ — not a fault. It calls NextZXOS at runtime, so
  bank injection cannot host it; through the genuine NEXLOAD path it launches
  and renders (`TestNexloadOSGamesIfPresent`, cmd/zx_go). The screening
  harness had recorded a working title as broken, which is now impossible:
  a blank frame from bank injection classifies as *inconclusive*, never a
  fault.

**The Next half now exists.** `TestNexloadSDGames` (cmd/zx_go) drives every
`.nex` on the SD card through the genuine NextZXOS NEXLOAD path — the only one
that can host a title calling the OS at runtime. **9 of 10 launch and render.**

**Every Next game on the SD card is now accounted for**: 14 of the 16 title
directories run — 9 of 10 `.nex` through NEXLOAD, all 3 NextBASIC programs
through the Command Line, and NEXTipede from tape. TX-1696 launches but
renders nothing. Pogie and THEH hold only assets and a 48K-sized `.snx`, which
cannot represent a Next game's banked state, so there is nothing to launch.

That is what items 4 and 7 were waiting on, and the answer it gives is
"no evidence either is needed": fourteen Next programs run without the zxnDMA
interrupt/match logic or exact Copper MOVE timing being modelled. Neither is
now blocked; both are simply unmotivated.

### 2. [correctness] NR$68 bit 2 — ULA half-pixel horizontal scroll

Decoded, stored and read back, but not rendered
(`pkg/ula/ulascroll.go:53`). `zxula.vhd:353` builds the shift as
`px(2 downto 0) & px(8)`, a 4-bit count in **half** pixels, so displaying
it needs the ULA layer rendered at twice the horizontal resolution; every
other path there is whole-pixel.

Bounded, but not small: it needs a 2x-wide ULA render path.

### 3. [correctness] zxnDMA interrupt / match logic and bus arbitration

**Unblocked, and unmotivated.** The transfer engine, prescaler and cycle
timing are complete and spec-checked. Not modelled: the interrupt/match logic
and DMA-vs-CPU bus arbitration (`pkg/next/dma/dma.go`).

The Next corpus now exists — 10 SD games driven through NEXLOAD, 9 rendering —
and none of them needs this. That is the evidence the item was waiting for, and
it argues for leaving it alone until a title actually demands it.

### 4. [product] GUI stability session

Still open and still **user-driven**: an agent cannot drive the windowed
app. The headless proxy passed long ago (50 000 frames, no leak, steady
frame rate, final frame still pixel-perfect), so what remains is
interactive use — menus, model switching, load/save, resize — over a
sustained session.

### 5. [product] Windows ARM64 has never been run

Since v1.8.1 it builds with llvm-mingw and publishes an artifact, so the
toolchain problem is solved. Nobody has launched the binary. Compiling
does not prove the OpenGL path works on Windows-on-ARM, which is why the
release matrix still marks it `experimental` and `README.md` carries the
caveat. One run on real hardware settles it.

### 6. [research] Copper cycle accuracy

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
