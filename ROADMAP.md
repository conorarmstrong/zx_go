# zx_go — Roadmap

## What this is

zx_go is a hardware-faithful emulator for the Sinclair 8-bit line, written
in Go: the **ZX80**, **ZX81**, the classic **48K / 128K / +2 / +2A / +3**,
the **Pentagon 128** clone, the **SAM Coupé**, and the **Spectrum Next**.

This file holds the **open-work backlog** and the **"do not regress"
invariants**. It is not a history: completed work lives in `CHANGELOG.md`
and git. If an item here is done, delete it rather than striking it through.

## Format

- `[ ]` open · `[~]` partially complete · `[⊘]` catalogued, deliberately not doing
- **[correctness]** a faithfulness gap against real hardware
- **[product]** user-visible capability or confidence
- **[research]** unknown answer, needs investigation before it can be scoped

---

## CURRENT STATE (2026-08-11 — v1.8.2)

Every machine above boots and is interactive. The classic line is mature;
the Next cold-boots NextZXOS through the real FPGA chain to an interactive
desktop, and the individual hardware blocks are tested against the FPGA
VHDL.

**The honest weak point is Next game compatibility**, exactly as
`README.md` says. Blocks being faithful in isolation has not yet been
converted into "arbitrary `.NEX` titles run".

---

## Open work (ordered)

### 1. [product] Next game compatibility

The single most valuable thing to work on, and the project's own stated
gap. `docs/compatibility.md` currently holds **25 entries: 9 Works, 13
Untested, 3 Known issue**. That is too thin to support a compatibility
claim either way.

The machinery already exists — redistributable `.nex`, headless boot,
pixel-golden assertion — so this scales by adding titles, not by building
infrastructure. What is missing is volume and a systematic
divergence-hunting loop rather than per-title debugging.

- [ ] Grow the tested corpus by an order of magnitude.
- [ ] Turn the 13 Untested entries into a verdict either way.
- [ ] Triage the 3 Known issues to root cause.

### 2. [correctness] Sprite per-line bandwidth limit

Real hardware runs out of sprite bandwidth per scanline, drops the
overflow, and latches bit 1 of the `$303B` status port. We model neither:
the limit is unenforced and the status bit always reads 0
(`pkg/next/sprite/sprite.go:250`).

This is compatibility-relevant, not cosmetic. Software that reads the flag
to throttle its own sprite use sees a machine that never saturates, and
scenes that should visibly drop sprites render complete.

### 3. [correctness] NR$68 bit 2 — ULA half-pixel horizontal scroll

Decoded, stored and read back, but not rendered
(`pkg/ula/ulascroll.go:53`). `zxula.vhd:353` builds the shift as
`px(2 downto 0) & px(8)`, a 4-bit count in **half** pixels, so displaying
it needs the ULA layer rendered at twice the horizontal resolution.
Everything else in that path is whole-pixel.

Bounded but not small: it needs a 2x-wide ULA render path.

### 4. [correctness] zxnDMA interrupt / match logic and bus arbitration

The transfer engine, prescaler and cycle timing are complete and
spec-checked. Not modelled: the interrupt/match logic, and DMA-vs-CPU bus
arbitration (`pkg/next/dma/dma.go`). No traced software has exercised
either, which is why it was deferred; revisit if a title needs it.

### 5. [product] GUI stability session

Carried over, still open, and still **user-driven** — an agent cannot
drive the windowed app. The headless proxy passed long ago (50 000 frames,
≈17 min, no leak, steady frame rate, final frame still pixel-perfect), so
this is about interactive use: menus, model switching, load/save, resize,
over a sustained session.

### 6. [product] Windows ARM64 has never been run

Since v1.8.1 it builds with llvm-mingw and publishes an artifact, so the
toolchain problem is solved. Nobody has launched the binary. Compiling
does not prove the OpenGL path works on Windows-on-ARM, which is why the
release matrix still marks it `experimental` and the README carries the
caveat. One run on real hardware resolves it either way.

### 7. [research] Copper cycle accuracy

Since v1.6.4 the Copper is stepped in 8-pixel segments, which is exact for
`WAIT` (the hardware threshold is `x<<3 + 12`, so 8 pixels *is* its
resolution). What remains is `MOVE` landing mid-segment
(`pkg/next/copper/copper.go:19`). Whether that is observable at all needs
evidence from real software before it is worth scoping.

---

## Catalogued — deliberately not doing

Kept so they are not re-proposed. Each has a rationale in the code or the
git history.

- [⊘] **Real Wi-Fi / TCP networking.** The UART register interface,
  FIFOs and AT-command responder are implemented; a live network stack is
  out of scope for a reference emulator (`pkg/next/uart/doc.go`).
- [⊘] **Interface 1 RS-232 / SinclairNET** and **NR$0B joystick I/O
  mode** — bit-banged external serial devices, same rationale.
- [⊘] **SAM MIDI, clock and SD/IDE ports** — writes ignored
  (`pkg/sam/io.go`).
- [⊘] **Beta Disk density bit** (status bit 5) — TR-DOS is always MFM.
- [⊘] **Debugger bisect UX**, **direct-boot path polish**, **GHDL
  gate-level testbench** — the CPU-conformance work already made the
  point.

---

## Do not re-derive

Solved problems whose answers are expensive to rediscover.

- **Opus Discovery ROM paging** is an **M1 address trap delayed one
  fetch**: in at `$0008`, `$0048`, `$1708`; out at `$1748`. The tell is
  placeholder bytes in the Opus ROM copying the Spectrum's at those
  addresses. Transfers are **NMI-per-byte**, not DMA, and the byte spacing
  is load-bearing. The interface is **memory-mapped, not port-mapped**;
  do not go hunting for a port map. Full record in `pkg/opus/README.md`.
- **The bootable SD image is FAT32.** The old FAT16 builder output never
  booted.
- **The 128K BASIC launch bug is closed** (Multiface-3 `$7F3F`/`$1F3F`
  paging readback, Layer 2 `$123B` readback, zero-filled cold RAM). Do not
  re-chase the superseded `$2009` / IM-1 / `$5Dxx` theories.

---

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
