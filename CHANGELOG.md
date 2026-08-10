# Changelog

All notable changes to this project are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the
project targets [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.8.0]

### Added

- **The Opus Discovery now works.** v1.7.0 shipped the disk layer with the ROM
  paging trigger unknown; it is known now, and the interface boots, hooks
  itself into BASIC, catalogues a disk, and loads and runs real games off real
  `.opd` images.

  **Paging is an M1 address trap, delayed by one cycle.** Three addresses page
  the ROM in — `$0008` (the error restart, for hook codes), `$0048` (KEY-INT,
  where it initialises its RAM), `$1708` (its entry point) — and `$1748` pages
  it out. The change lands on the *next* opcode fetch, so the instruction at a
  trap address always executes from whichever ROM was already paged. What
  confirmed it: the Opus ROM carries **placeholder bytes copying the
  Spectrum's** at exactly those addresses — `NOP` at `$1708` where the 48K ROM
  has `INC HL`, `C5` at `$0048` where it has `PUSH BC`. Those bytes never
  execute. At power-on the ROM is paged in, so the Z80 runs the Opus reset
  vector and pages itself out into the Spectrum's.

  **Transfers are NMI-driven, not DMA.** DRQ is wired to the Z80's NMI and the
  handler moves one byte per interrupt. The byte spacing is load-bearing: a
  real WD1770 delivers a byte every ~32 µs (112 T-states) and the ROM's
  handler needs ~83, so asserting DRQ the instant the previous byte is taken
  re-enters the handler halfway through itself and the transfer never advances.
  The pacing counts down from a delta rather than against a deadline, because
  the CPU's T-state counter is rebased every frame and an absolute deadline
  stalls mid-sector at the first frame boundary.

  Also corrected from v1.7.0: `$3000-$3003` is a **6821 PIA** (PRA/DDRA, CRA,
  PRB/DDRB, CRB — one address is two registers, selected by control-register
  bit 2), not the flat latch it first looked like; interface RAM is 2 KB at
  `$2000-$27FF`; and **sectors are numbered from 0**, which the ROM's own
  drive table settles with "(03) no extra sectors".

- **Opus disks in the emulator.** File → Load Opus Disk 1/2, `.opd` on the
  command line and in the unified Open File dialog. Fitting the interface
  resets the machine, as the real hardware requires. Guest writes land in the
  mounted image rather than the file, with an explicit Save Opus Disk As...,
  so running a game disk cannot damage it; ejecting with unsaved changes asks
  first.

- **Formatting, via the WD1770's WRITE TRACK.** `FORMAT 1;"name"` works. The
  controller is handed a whole raw track a byte at a time — gaps, sync,
  address marks, ID fields and sector data — and recovers the sectors from it,
  which is what the real chip does; the ROM builds that stream from a
  run-length table at `$1BDB` with the track, side, sector and size
  substituted into its `$F0-$F4` placeholders. WRITE TRACK carries no length,
  so the controller ends the command itself after a track's worth of bytes, as
  the index pulse does on real hardware.

  Verified end to end against the ROM's own commands: a blank image formatted
  by `FORMAT` and then read back by `CAT`, showing the new disk name and a
  full 178 free blocks; and a copy of a real game disk formatted while the
  source file is checked byte for byte and left untouched.

### Known gaps

- 48K only: the trap addresses are 48K ROM addresses and mean something else
  under any other ROM, so the emulator refuses to fit the interface elsewhere.

## [v1.7.0]

### Added

- **Opus Discovery disk interface (`pkg/opus`).** The 8 KB v2.22 interface ROM
  is vendored at `roms/opus.rom`, and the hardware is modelled where it
  actually lives: the Opus is memory-mapped, not port-mapped, with the WD1770
  register file at `$2800-$2803` and drive control at `$3000-$3003` inside the
  window it pages in alongside its ROM. That is why earlier port-map hunting
  found nothing — the ROM contains almost no `IN`/`OUT` at all.

  `.opd` images are 40 cylinders of 18 256-byte sectors on one side, a flat
  184320 bytes with no header, confirmed against three unrelated real images.
  The Opus geometry is why the controller is its own rather than
  `pkg/betadisk`'s, which is built around TR-DOS's 16 sectors per track.

  Working and test-covered: Type I restore/seek/step, Type II read and write
  sector, drive selection, write protection, and record-not-found on an empty
  drive — all driven through the register file the guest itself uses, and the
  read path verified byte-for-byte against real `.OPD` disk images.

### Known gaps

- **Opus ROM auto-paging is not implemented.** The trigger that pages the
  interface over the Spectrum ROM is unknown. Executing the ROM standalone
  from reset polls `$3001` a few hundred times and then runs into RAM;
  sweeping every single-bit status value changes how it fails but never
  reaches the controller. The published disassembly has no port map to find,
  and its `page-in`/`page-out` labels turn out to be BASIC-editor routines.
  Until this is known the package is a working disk subsystem rather than a
  boot device, so it is deliberately not wired into the machine or the GUI.
  `pkg/opus/README.md` records what has been ruled out so it is not re-derived.

## [v1.6.4]

### Added

- **Copper intra-line raster precision.** The Copper was stepped once per
  scanline at end-of-line hcount, so every MOVE on a line landed before the
  row was composited and a WAIT for a mid-line column had no effect at all.

  Previous notes here called this unfixable without a per-pixel renderer, and
  a segment-based approach an approximation. Both were wrong. The WAIT column
  field is 6 bits taken as 8-pixel units — the release threshold is
  `hcount >= (X<<3)+12`, `copper.vhd:94` — so **8 pixels is the Copper's own
  horizontal resolution**. Stepping and compositing in 8-pixel segments is
  therefore exact at the granularity the hardware itself resolves, not an
  approximation of anything.

  The compositor gained `ComposeScanlineRange`, and the row loop now walks the
  line in 8-pixel segments, stepping the Copper at each boundary and finishing
  off-screen so a WAIT for a column beyond the visible area still releases
  within its own line. The whole line still shares one instruction budget, so
  segmenting cannot let the Copper outrun the hardware. **Every pixel-golden
  corpus frame is unchanged**, so this is a pure gain with no regression.

### Verified

- **Compatibility manifest: 20 Untested down to 13, 16 entries now carry
  evidence.** Manic Miner verified into gameplay (Central Cavern with the AIR
  bar and score panel); Elite, Chuckie Egg, Driller, Total Eclipse, Jet Pac
  and Pssst to their title or options screens, each with a note on exactly
  what was seen and what was not. Driller and Total Eclipse were checked on
  their 48K tape releases rather than the +3 disk reissues the rows name, and
  Jet Pac and Pssst from snapshots rather than through the Interface 2
  cartridge slot — both said plainly in the notes rather than glossed.

- **Where Time Stood Still stays Untested, with the reason recorded.** The
  `.z80` to hand is a v1 (48K-format) snapshot of a 128K game, so it drops to
  BASIC on the 48K and the 128K alike. Checked on both models specifically to
  rule out the v1.6.1 48K-snapshot paging change as the cause; it is not.

### Known limitations

- **NR$68 bit 2 (ULA half-pixel scroll)** is decoded, stored and read back but
  cannot render. The shift it contributes is a HALF pixel
  (`zxula.vhd:353` builds it as `px(2 downto 0) & px(8)`), and the ULA layer is
  sampled at one pixel per output pixel. Showing it needs the layer rendered at
  twice the horizontal resolution. Rounding to the nearest whole pixel was
  considered and rejected: it would move the picture by a full pixel when the
  guest asked for half, which is a different wrong answer rather than a better
  one.
- **Opus Discovery** remains unimplemented. Confirmed there is no ROM image
  anywhere on this machine, and the obvious reference implementation is GPL
  against this project's MIT licence, so it cannot be written or tested here.

## [v1.6.3]

### Added

- **NR$26 / NR$27 ULA scroll.** The ULA's own horizontal and vertical scroll
  was entirely unimplemented, which is also why NR$68 bit 2 had nothing to
  attach to. Transcribed from `zxula.vhd:192-216`: the horizontal register
  splits into a whole-character offset (bits 7:3) applied to the fetch and a
  pixel shift within the character (bits 2:0) that straddles two cells; the
  vertical is a 9-bit sum folded back onto the 192-line screen by the core's
  two special cases, not a modulo. Both reset to zero, where they are exactly
  a no-op, so no classic model changes.

  NR$68 bit 2 is now carried into the ULA but still does not render: the shift
  it contributes is a HALF pixel (`zxula.vhd:353` builds it as
  `px(2 downto 0) & px(8)`), which needs the ULA layer rendered at twice the
  horizontal resolution to show.

### Fixed

- **The raster journal grew without bound.** It was only ever cleared by the
  compositor's `EndReplay`, so any path that executes frames WITHOUT rendering
  one — headless stepping, fast tape turbo, the debugger, RZX playback — left
  entries piling up forever. Measured on a real Next boot it reached **113,766
  entries and was still climbing**. That is a memory leak, and worse a
  correctness bug: the next rewind would undo state from thousands of frames
  earlier. Shipped in v1.6.0, so it affected the palette and Layer 2 scroll
  journalling too, not just the registers added in v1.6.1.

  Entries are now scoped to a frame identity taken from the T-state counter
  (`roms.FrameTStates`), which advances whether or not anything renders, and
  a frame that is never rendered has its entries discarded — its writes are
  already live. A hard cap of 8192 entries per frame is the backstop. The same
  boot now peaks in the hundreds.

- **NR$14 is journalled after all.** v1.6.1 excluded it, reasoning that
  transparency compares against a whole-frame ULA layer so a per-row
  comparison would be inconsistent. That diagnosis was wrong. show512 writes
  NR$14 **zero** times in 150 frames — it was never the demo's own writes that
  broke it, but the accumulated journal being rewound. With the leak fixed,
  NR$14 journals correctly and every Next demo passes.

## [v1.6.2]

No functional change. Corrects a claim in the v1.6.1 notes and syncs the
`release-public` branch, which had fallen 152 commits behind `main`.

### Changed

- **Corrected the v1.6.1 note on how the NR$14 regression was found.** It said
  a Next demo test "the full-package run had been silently skipping" caught
  it. That was wrong, and checking it properly disproved it: re-introducing
  the NR$14 journal entry fails the test in a full-package run and a targeted
  run alike. There is no order-dependent skipping and `go test ./...` catches
  it. The regression was missed because the verification command in use piped
  through `head -20` and truncated the output above the failure — a mistake in
  how the suite was being read, not a property of the suite.

## [v1.6.1]

Closing the testing gap that let the v1.6.0 bug through, rather than adding
features. Two more real bugs fell out of doing it.

### Fixed

- **A 48K snapshot on a 128K-family machine did not page the 48 BASIC ROM.**
  Only the RAM was restored, so the program ran against the 128K editor ROM
  and every ROM call — the character set, the print routines — produced
  garbage. Restoring a 48K snapshot now selects the 48 BASIC ROM and locks
  paging, exactly as the 128K's own "48 BASIC" mode does. Affects the +2, +2A
  and +3 equally (the +2A/+3 need ROM 3, reached through `$1FFD` bit 2).

- **The test harness never applied the per-model frame-INT timing the GUI
  does.** `cmd/zx_go` gives the 128K/+2A/+3/Next a narrow /INT pulse at
  construction; `pkg/testharness` did not, so the entire golden corpus ran the
  legacy held-INT approximation — a configuration no user sees, and the wrong
  one for judging INT-timing conformance. Both now read the mapping from
  `next.MachineTimingFor` so they cannot drift.

  The payoff is visible: the `int_skip` conformance test on the 128K now
  reports `0 |OK |inhibits ISR` for both the NOP and FD prefix blocks — the
  correct hardware result — where held-INT gave `!ERR! allows ISR`.

### Added

- **The golden corpus covers the 128K family.** Every classic entry was 48K,
  which is precisely how a 230-T-state error in the 128K display origin
  survived: the renderer and the contention model shared the same wrong
  origin, so a static screen still looked right, and no vendored program ever
  ran on the machines that were wrong. Five entries added across the 128K and
  +3. They are regression guards for that family, not hardware oracles.

- **An observable interrupt-phase assertion.** The existing tests checked the
  contention function and the constants in isolation, which could not see a
  phase error. These sweep the frame and measure the first stalled T-state and
  the first paper fetch through the emulator's own memory and ULA. The
  expected figures are written as literals on purpose: comparing against
  `roms.DisplayStartTState` is tautological, since the emulator derives its
  behaviour from that same constant. Verified by reverting the origin — with
  the constant as the expectation the tests still passed; with literals they
  fail with a `+230 T-state phase error`.

- **The raster journal covers the remaining visual registers** — NR$15, NR$2F,
  NR$30, NR$4A, NR$4C, NR$6E and NR$6F — via a whitelist that wraps their write
  handlers. It stays a whitelist deliberately: replay re-runs the handler, so
  anything that auto-increments a cursor, uploads data or repages memory must
  never be added. A test pins the idempotence that makes replay safe.

  **NR$14 was excluded here on a mistaken diagnosis; see v1.6.3.** It was
  attributed to a per-row/per-frame mismatch in how transparency is compared.
  That was wrong — the real cause was the journal growing without bound.

### Verified

- **The 128K raster origin is confirmed by simulation, not just by reading the
  VHDL.** A GHDL testbench drives the real `zxula_timing.vhd`, waits for its
  `o_int_ula` pulse and counts 7 MHz ticks to the first paper fetch. It
  returns 14336 / 14362 / 14363 / 17982 for the 48K / 128K / +3 / Pentagon,
  matching `pkg/roms/timing.go` exactly. (MAME was the original plan and is
  the wrong oracle here: its `tbblue` screen is the Next's own output rather
  than the classic raster, and `spec128` needs proprietary ROMs.)

- **Atic Atac and Renegade are no longer "Untested".** Atic Atac boots, accepts
  keyboard input through its menu and plays. Renegade loads from `.tap`
  through the 128 Tape Loader to its title screen. `docs/compatibility.md`
  now documents how to verify a title so the manifest can be worked down
  without vendoring anything.

## [v1.6.0]

Clears the remaining valid items from the external review, plus one open
question from v1.5.0 that turned out to be a real bug. Everything is derived
from the FPGA core sources, with the VHDL line references in the code.

### Fixed

- **The whole 128K family was a scanline late against the interrupt.** v1.5.0
  left this flagged as an open question; the FPGA settles it. `zxula_timing.vhd`
  derives the ULA-relative counters from the raw ones — `hc_ula` resets at
  `c_min_hactive - 12`, `vc_ula` on the line where `vc = c_min_vactive` — and
  the interrupt fires at `(c_int_v, c_int_h)`. Working the gap out per
  personality gives a first paper fetch of **14336** on the 48K, **14362** on
  the 128K/+2 and **14363** on the +2A/+3, not the flat `64 * lineLength`
  every model was using. The 48K and 128K results reproduce the documented
  14336 / 14362 exactly, which is what validates the derivation; the +2A/+3
  differs from the commonly-quoted figure by one T-state because the core
  gives it its own `c_int_h`.

  The 230-T-state error was invisible to the test corpus because the renderer
  and the contention model shared the same wrong origin, so a static screen
  still looked right — what was wrong was the phase against the interrupt,
  which is exactly what timing-sensitive software depends on. The per-model
  geometry now lives in one place (`pkg/roms/timing.go`) and the contention
  window, the floating bus and the mid-frame border tracking all key off it.

  Two consequences fell out. The floating bus was placing the paper 24
  T-states into each line, a left border that does not exist once the origin
  IS the fetch. And the border tracker's `frameScanline` was hardcoded to 64
  lines with an "approximate" comment; it now follows the model.

- **NextReg 0x68 only decoded bit 7.** The blend selection (bits 6:5) and the
  stencil bit were dropped, so NR$15's two blend orderings could only ever see
  the reset blend source. All the fields are decoded now, the blend source and
  stencil reach the compositor, and the read-back masks reserved bit 1 per
  `zxnext.vhd:6093`. Bit 4 (cancel extended keys) and bit 2 (ULA half-pixel
  scroll) are decoded and read back but not acted on — neither subsystem
  models them.

- **CPU mid-frame writes now take effect at their own scanline.** The Next
  composites at the end of a frame, so a CPU write partway down the screen
  applied retroactively to the whole frame and raster splits driven from an
  interrupt handler or a raster-wait loop did not appear at all. (The Copper
  was never affected — it is stepped inside the compositor's row loop.)

  A new journal (`pkg/next/rasterlog`) records each visual change against the
  display row it was made on; the compositor rewinds to the frame-start state,
  walks down applying each change as it reaches its row, then restores the
  live state. Changes are recorded as undo/redo closures rather than register
  writes on purpose: replaying raw NextReg writes through the dispatcher would
  re-trigger palette auto-increment, sprite uploads and MMU paging. Wired for
  palette entry writes (the classic rainbow split) and Layer 2 scroll (per-line
  parallax); other visual registers still latch once per frame.

- **TZX 0x18 and 0x19 are decoded.** Both were skipped via their length field,
  silently dropping content. 0x18 (CSW recording) now decodes both the RLE and
  Z-RLE streams; 0x19 (generalised data block) decodes its symbol alphabets,
  run-length pilot stream and bit-packed data stream, honouring the per-symbol
  level flags. A malformed stream block is skipped with a warning rather than
  failing the load.

- **The rest of the UI paths that mutate live state are guarded.** v1.5.1
  fixed the machine switch; reboot and snapshot restore had the same
  weakness, reachable from menus, drag-and-drop and the telnet debugger. Both
  now take `coreMu`, each with a `Locked` variant for the callers that already
  hold it — the machine switch reboots, and RZX playback restores intermediate
  snapshots from inside the frame loop.

### Known limitations

- **The Copper still resolves a WAIT to a whole scanline.** Intra-line raster
  precision would need a per-pixel renderer. The per-scanline instruction
  budget is hardware-correct as of v1.5.0.
- **Opus Discovery is not emulated.** Microdrive, TR-DOS/Beta and DISCiPLE
  are.

## [v1.5.1]

Self-review of the v1.5.0 changes. Three defects found in that release's own
new code, plus one hot-path cost.

### Fixed

- **The fast-load trap could hang the emulator on a cyclic tape.** v1.5.0 made
  `NextBlock` walk past the non-data blocks it had just taught the player to
  understand, but that walk was unbounded — and TZX flow control can move the
  cursor backwards. A tape whose jump or loop cycles over blocks carrying no
  data (a jump of -1 over a pause, say) spun forever. Both tape walks now share
  one bound.
- **Long same-level runs were split into a toggling chain.** Pulse durations
  were `uint16`, so anything past 65535 T-states had to be emitted as several
  pulses — and the player toggles the EAR line at every pulse boundary, so a
  run came back as alternating levels instead of the one level recorded. This
  hit the new direct-recording block (0x15) and, pre-existing, every trailing
  pause: a one-second gap was carrying ~53 spurious edges. Durations are now
  `uint32` and a run is one pulse.
- **The machine switch could swap the core out mid-frame.** A data race that
  predates v1.5.0 (the frame-pacing work only added another read of it, which
  is how it was noticed). The machine-switch menu runs on the UI goroutine and
  replaces `cpu`, `mem`, `ula`, `kbd`, `peripherals`, `model` and the Next
  device set wholesale — or, on the in-place path, has `mem.SwitchModel`
  reshuffle bank allocations under the running CPU — while the emulation
  goroutine is reading all of it every frame. Pausing first was not enough:
  `paused` is only tested at the top of a loop iteration, so setting it never
  waited for an in-flight frame to finish. A new `coreMu` is held by the
  emulation goroutine for the whole of each frame and across both switch
  paths' mutations, so a switch can only land between frames. It is taken
  after the remote debugger's `WaitIfPaused`, so a debugger pause cannot
  freeze a machine switch.

  Not covered: the other UI actions that mutate live emulator state
  (snapshot load, quick-load, reboot, tape insertion) rely on the same
  advisory pause and have the same weakness. They mutate contents rather than
  replacing the core objects, so the consequences are milder, but they are the
  same class of bug and want their own pass.

- **A stale contention shape after a machine switch.** Deciding contention by
  the paged bank put a model lookup on every memory access, so the timing
  personality and line length are now cached — and the cache is refreshed by
  `setupModel`, which both `New` and `SwitchModel` call. Pinned by a test, as
  a stale cache would leave a switched-to machine contending like the old one.

## [v1.5.0]

A fidelity pass driven by an external review. Several of the reported items
turned out to be already implemented (zexall and zexdoc both pass in CI, the
debugger has had conditional breakpoints and a NextReg/palette/sprite widget
set for some time, Microdrive and TR-DOS are first-class packages), but four
were real and are fixed here. Everything below is derived from the FPGA core
sources rather than from folklore, with the VHDL line references in the code.

### Fixed

- **Memory contention was wrong on every model.** `contentionDelay` hardcoded
  a 228 T-state scanline and a single start T-state for all machines, and
  decided contention purely on the address `0x4000-0x7FFF`. Three separate
  faults, all now fixed against `zxnext.vhd:4489-4493` and `zxula.vhd:582-583`:
  - The 48K ULA runs a **224 T-state line**, not 228. Keying it off 228 drifted
    the contention window 4 T-states per line, so from display line 1 onward
    most lines were charged at the wrong offsets — line 1 was charged nothing
    at all where hardware charges 6. The window now advances by the model's
    own line length.
  - **Contention is decided by the paged bank, not the address.** The 48K
    contends bank 5, the 128K family the odd banks (1/3/5/7), and the +2A/+3
    banks >= 4; banks above 7 and ROM never contend. A contended bank paged
    into `0xC000` was previously charged nothing (the old code's own comment
    described the rule it did not implement), and the +3's all-RAM special
    paging mode now correctly contends all four slots.
  - **The +2A/+3 have their own delay pattern.** `zxula.vhd:583` ORs in a
    second contended term for `i_timing_p3`, taking the line from 6 contended
    T-states of every 8 to 7. They were using the 48K/128K table.

- **Four of the eight NR$15 layer-priority modes did nothing.** The register
  decodes bits 4:2 as a 3-bit field, but the live compositor only had cases
  for 0-3. Modes 4 (USL), 5 (ULS) and the two additive blend orderings (6, 7)
  fell through the switch with no case at all, so **Layer 2 and the sprite
  layer vanished entirely** and the frame showed bare ULA. All eight now
  render, matching the orderings in the FPGA-golden `Mix()` reference that
  was already in the tree but unwired. The blend modes use NR$68's reset
  blend selection; NR$68 itself is still not wired.

- **TZX blocks were parsed and silently discarded.** Pure tone (0x12), pulse
  sequence (0x13) and direct recording (0x15) never reached the pulse stream,
  and 0x20 / 0x23 / 0x24 / 0x25 / 0x2A / 0x2B were consumed as no-ops so a
  tape's **flow control was ignored** — jumps, counted loops and stops all had
  no effect, and multi-load or protected tapes played their blocks in raw file
  order. All of these are now played or executed. Note that parsing never
  failed here; the content was dropped after a successful parse, which is why
  it went unnoticed. **Still not implemented: 0x18 (CSW) and 0x19 (generalised
  data block)** — both are separate compressed-stream formats, and they remain
  skipped via their length field.

- **The Copper's per-scanline instruction budget was derived from the CPU
  clock.** It was a flat 64, reasoned from "one instruction per 4 CPU
  T-states". The Copper is clocked from `i_CLK_28` (`zxnext.vhd:3942-3944`),
  so its throughput is set by the video clock and does not move with the CPU
  speed at all. A list with more than 64 instructions on one line was being
  starved. The budget is now `copper.InstructionsPerScanline`, derived from
  the line length: 912 instructions on a 456-column line.

### Changed

- **Frame pacing follows the machine, and no longer drifts.** The emulation
  loop ran on a flat 20 ms `time.Ticker` for every model. No Sinclair machine
  runs at 50.000 Hz — the 48K is 19.968 ms, the 128K family 19.992 ms, and the
  Pentagon 20.480 ms — and a tick the host was too slow to service was
  silently lost with nothing to correct for it. Frames are now scheduled
  against a fixed wall-clock grid derived from the model's frame length and
  CPU clock, so service time cannot accumulate as drift, an overrun drops the
  frames it missed instead of running them back-to-back, and switching machine
  takes effect on the next frame. Rendering follows the same period.

### Known limitations

- **Mid-frame NextReg writes from the CPU are not latched per scanline.** The
  Next layers composite at end of frame, so a CPU write partway down the
  screen applies retroactively to the whole frame. Copper-driven raster splits
  are unaffected — the Copper is stepped inside the row loop. See ROADMAP.md
  for the design sketch; it wants its own change with the pixel-golden corpus
  green before and after.
- **The Copper resolves WAITs to a whole scanline.** Intra-line raster
  precision would need a per-pixel renderer.

## [v1.4.2]

Reported as "the border is now black". The emulated frame was never wrong:
the emulator's own screenshot is a correct 320x240 with a white border, and
every model renders byte-identically to v1.4.0. The window around it was.

### Fixed

- **The View presets sized the window, not the content box.** They exist to
  give a whole-number pixel multiple — at 300% every ZX pixel should be a
  crisp 3x3 block — but fyne pads the content and puts the menu above it, so
  asking for a 960x720 window left a 952x742 box, and 952/320 = 2.975 floors
  to 2. Picking "300%" silently gave 200%, and the lost third of the window
  became surround, which is what read as a thick black frame. The presets now
  add the chrome so the box is exact. Measured on a real run: container
  952x742 → 960x750, factor 2 → 3, image 640x480 → **960x720**.
- **An exact multiple could be lost to float32 rounding.** `integerScaleFactor`
  floored a float32 division, so a 960/320 landing on 2.9999997 dropped a whole
  step and halved the image. Guarded with an epsilon tight enough that it can
  only rescue an already-exact fit, never promote a genuinely fractional one.

### Changed

- **The surround is the emulated border colour, not black.** A freely-resized
  window can never be an exact multiple, so some gap always remains, and it
  should not read as a black picture frame. This is also the faithful answer:
  the 320x240 we render is a crop of the full ULA frame, the bulk of which is
  border, so continuing the border out to the window edge is what a Spectrum
  puts on a TV. Sampled from the un-filtered frame, since the CRT scratch
  buffer would tint it.

  Known limitation: the surround is one flat colour taken from the frame's
  corner, so a rainbow-border effect will not continue its stripes outward.

## [v1.4.1]

A robustness sweep over the parts of the emulator that untrusted input
reaches: guest code on the bus, and the file formats we parse. Found by
driving randomised guest programs through the real CPU on every model and
by fuzzing the format readers.

### Fixed

- **`$DFFD` extended RAM banks in the `$C000` slot were read as ROM.** On
  the Next the `$C000` bank is `port_7ffd_bank = port_dffd_reg(3:0) &
  port_7ffd_reg(2:0)`, a 7-bit value, but the classic read dispatch applied
  the page-map's ">= 16 means ROM index" encoding to all four 16K slots.
  Banks 16-19 silently returned ROM 0 bytes while the *write* path, which
  flags ROM with `-1`, correctly put the data in RAM — so reads and writes
  disagreed. Banks 20-127 indexed past the four-entry ROM array and
  panicked, taking the whole emulator down; there is no `recover()` in the
  tree. Three guest instructions reach it: `ld bc,$DFFD : ld a,3 : out
  (c),a` then a read of `$C000`. The 8K MMU "ROM half" path had the same
  fault, reachable via `NR$56=$FF` after a `$DFFD` write. The `$0000-$3FFF`
  ROM slot and the ZX80/ZX81 ROM mirror at slot 2 are unaffected and now
  covered by their own regression.
- **`LD A,R` after a `HALT` returned a frozen value on every Spectrum and
  Next model.** A HALTed Z80 does not stop the clock: it keeps fetching the
  HALT opcode internally, one M1 every 4 T-states, and each M1 refresh bumps
  R (Sean Young §5). The emulator has four execution loops and they had
  drifted apart — `ExecuteFrame`, the production loop, advanced 1 T-state
  per halted tick and never touched R, and ignored the `RefreshDuringHalt`
  opt-in entirely. All four now share one `haltTick`, so the 48K keyboard
  scan's PRNG seed and anything else sampling R sees real entropy. The
  `RefreshDuringHalt` flag is gone: the behaviour is unconditional, which is
  what the hardware does.
- **A zxnDMA IO endpoint aimed at the DMA's own command port crashed the
  process.** IO-endpoint accesses go to the ULA's port dispatch, which
  routes `$6B`/`$0B` straight back to `WriteCommand`, so a transferred byte
  that happened to be ENABLE (`$87`) started a nested `Trigger` without
  bound: `fatal error: stack overflow`, which no `recover()` can catch. The
  FPGA has no such hazard — it is a state machine already sitting in its
  transfer state, so an ENABLE arriving mid-block is not a second transfer.
  Modelled with a re-entrancy guard covering both the synchronous transfer
  and the interleaved burst pump.
- **RZX input-recording blocks inflated without bound.** The frame stream
  carries no declared uncompressed length, so a small hostile zlib stream
  could expand to arbitrary memory before `readFrames` rejected the frame
  count. The read is now bounded by what the declared frame count can hold
  (4 + `0xFFFF` bytes per frame) under an absolute ceiling. The snapshot
  block was already bounded this way.

### Added

- **`TestGuestPortStress`** — randomised guest programs run through the real
  CPU on all seven models: `OUT`/`IN` across the whole 16-bit port space,
  Z80N `NEXTREG` writes over the whole register space, reads and writes
  across the whole address space, then a render so the video stack sees the
  registers the stream left behind. This is what surfaced the `$DFFD` fault.
- **Fuzz targets for the disk-image and RZX readers** — `FuzzParseDiskImage`
  (DSK / EDSK / UDI / SAD, including the sector walk the FDC performs on
  every read) and `FuzzRead` (the RZX block walker). Both clean at 60s.
- **`make race` and a CI race-detector job.** The race detector's ~10x cost
  puts a bare `go test -race ./...` past `go test`'s 10-minute *default*
  per-package timeout: it aborted mid-package and reported a timeout instead
  of a result, which is how the emulator loop's concurrency went unchecked.
  Two things fix that — an explicit timeout for `cmd/zx_go`, ~20 minutes
  under `-race` once the ROM-backed boot tests unskip, and `-short` to drop
  the Cringle Z80 exerciser, which pushes past even an hour while being
  single-goroutine, so the race detector has nothing to find in it. It keeps
  its own CI job, at full speed and without `-short`. The resulting run is
  clean: 50 packages, no data races anywhere.

## [v1.4.0]

Crisp, square pixels at every zoom, plus a 400% view. Resolves
[#11](https://github.com/conorarmstrong/zx_go/issues/11): at fractional
window sizes or on HiDPI displays with a non-integer OS display-scale the
image was stretched to fill, softening the ZX pixel grid.

### Added

- **Integer scaling (crisp pixels)** — a checkable `View` menu item, on by
  default. The emulator image is drawn at the largest whole-number multiple
  of its native 320x240 grid that fits, centred, with black bars for any
  remainder, so every source pixel maps to an exact square block of
  physical pixels. The multiple is computed in physical pixels via the
  canvas density, so it stays pixel-exact on HiDPI and fractionally-scaled
  (e.g. Windows 125%) displays. Turn it off to stretch-to-fill as before;
  the choice is persisted (`integer_scale` in config.json).
- **400% (1280x960)** view preset in the `View` menu.

### Notes

- 100% is 320x240, not 256x192: that is the full native ULA frame — the
  256x192 display plus the authentic border a real Spectrum drew. The
  standard 100/200/300/400% presets are exact integer multiples of it.

## [v1.3.9]

zxnDMA: the three gaps the compatibility-corpus ZilogDMA conformance test
exposed, fixed against `zxnext.vhd` / `device/dma.vhd`. The regenerated
corpus golden's register readback is now byte-identical to the test's
documented "TBBLue zxnDMA core 3.1.5" hardware capture
(`3A3A1A03042A1A1A03D1041A1A03D508`), and the transfer-mode grids pass.

### Fixed

- **DMA port `$0B` is decoded, and the accessing port selects the DMA
  mode** (`ports.txt` `0x0b`/`0x6b`; `zxnext.vhd:1817`
  `dma_mode <= port_0b_lsb` on any DMA read or write): `$6B` = zxn dma,
  `$0B` = Z80-DMA compatible. Reads and writes at `$0B` previously fell
  through to the floating bus, so software driving the documented Z80-DMA
  port saw `$FF` and no transfers.
- **z80 mode moves length+1 bytes.** LOAD/CONTINUE/auto-restart seed the
  byte counter with -1 in z80 mode (`dma.vhd:664` "z80 dma loads -1"), and
  the transfer loop repeats while counter < block length — reproducing the
  classic Z80 DMA length+1 convention, the raw-counter readback, and the
  final port addresses exactly. zxn mode (`$6B`) is unchanged: the
  pre-existing GHDL FPGA golden passes untouched.
- **`$BF` (Read Status Byte) is implemented** (`dma.vhd:687`): the next
  port read returns the status register wherever the read sequence stood.
  The read cursor now mirrors the FPGA read FSM state machine exactly,
  including its RD_STATUS fallback for an empty read mask (previously a
  synthetic `$FF`).
- **A zero block length moves one byte, not 65536.** The FPGA FSM always
  transfers once before testing the counter (`dma.vhd`
  `TRANSFERING_WRITE_4`); the old "0 = 65536" rule had no source in the
  zxnDMA documentation and did not match the hardware.

## [v1.3.8]

Follow-up hardening after the v1.3.7 NextZXOS Browser fixes: a systematic
audit of every paging/MMU gate condition in `zxnext.vhd`, cross-checked
against `pkg/memory`. One more real bug of the same class, the rest pinned
with FPGA-derived tests.

### Fixed

- **NextReg `$8E` with bit 3 set now clamps the `$DFFD` high-bank nibble**
  (`zxnext.vhd:3698-3702`: `port_dffd_reg(3)<='0'`,
  `port_dffd_reg(2:0)<="00"&dat(7)`). The handler previously cleared only
  `$DFFD` bit 0, leaving a stale high nibble — so after a program paged a
  RAM bank ≥16 via `$DFFD`, a subsequent `$8E` bit-3 write (e.g. NextZXOS's
  `NEXTREG $8E,$08` RAM-sizing exit) resolved `$C000` to `staleNibble<<3`
  instead of the intended 0-15 bank. Same stale-high-bank class as the
  NBI-590 and tap-launch faults. Found by the gate audit, proven by both a
  unit test and the FPGA golden (`W8E 40 → $C000 bank 4`).

### Added

- **The FPGA-derived paging golden now exercises the NR`$8E`/`$EFF7` gate
  class.** `_tools/paging-vhdl-test` (the GHDL extract of the real MMU
  decode) gained an `$EFF7` stimulus input and testbench sequences for the
  `$8E` bit-3 suppression, the bit-3-set reload, the `$DFFD` clamp, and the
  `$EFF7` bit-3 RAM-at-`$0000` behaviour. Regenerated
  `testdata/paging_golden.txt` (144 mappings) and taught the replay the
  `WEFF7` op. Confirmed the golden has teeth: reverting the `$DFFD`-clamp
  fix makes it fail (`$C000` 228 vs 4).
- Unit-level `TestNR8E_GateMatrix` mirroring the same suppress/reload/clamp
  cases beside the unconditional-port matrix.
- `docs/internal/hardware-audit/paging-gate-catalogue.md` — the full
  catalogue of paging/MMU gates with per-rule status (fixed / modelled /
  deliberately deferred), so the exception rules are documented rather than
  rediscovered bug-by-bug. Two rules are recorded as conscious deviations
  (NR`$8E` under a locked pager; the SounDrive DAC F1/F9 port-conflict
  suppression and Pentagon-1024 `$8F` mode), both unreachable on a stock
  Next personality.

## [v1.3.7]

### Fixed

Resolved GitHub issues #9 and #10 (NextZXOS Browser: missing directories,
search crash, `.tap` files not launching). Two root causes.

- **NextReg `$8E` writes with bit 3 clear no longer reload MMU6/7** — the
  one paging write whose MMU reload real hardware suppresses
  (`zxnext.vhd:3814`: `port_memory_ram_change_dly <= not (nr_8e_we and not
  nr_wr_dat(3))`). NextZXOS's ROM-swap trampolines in sysvars RAM
  (`NEXTREG $8E,n : RET` at `$5B3E-$5B53`) run with the OS stack in an
  MMU-mapped bank at `$C000-$FFFF`; our `$8E` handler routed its
  `port_1FFD` update through `PageMemoryPlus3`, whose (v1.3.6,
  correct-for-real-`$1FFD`-writes) unconditional MMU6/7 re-sync swapped
  the stack bank out mid-trampoline — the `RET` popped `$0000` from the
  wrong bank and the OS crashed into its error handler. Root-caused by
  instruction-level comparison against a reference at the exact fork
  (`$5B42`: `RET` to `$0941` vs `$0000`). This single fix cures BOTH the
  Browser search crash (issues #9/#10 — any typed search character
  aborted or corrupted the screen) and the `.tap` launch abort (issue #10
  — `tapload.bas` died at its `.$ metadata` statement before showing the
  TAP Loader menu). Verified end-to-end: Browser search matches and
  highlights correctly in both image and folder modes, and ENTER on a
  `.tap` now reaches the mode menu and loads + runs the tape (128K mode).
- **Folder mode ("files instead of an image") now serves the user's whole
  SD directory** (issue #9) — the boot-card `NextBootFilter()` was wrongly
  applied to user-configured folders, hiding all but 6 root entries; the
  image is also sized to the folder's contents instead of a fixed 256 MB.
- New regression tests: `pkg/memory` NR`$8E`-bit3/MMU6-7 suppression matrix
  + genuine-`$1FFD`-still-reloads guard; `cmd/zx_go` full-tree folder-mode
  card build; env-gated end-to-end reproductions for the Browser search
  and `.tap` launch (`ZX_GO_DIAG=1`, need local ROMs/SD content).

## [v1.3.6]

### Fixed

Closed out the rest of the MMU-sync bug class the v1.3.5 fix belonged to —
an audit found the identical pattern in two more places, plus an entirely
unimplemented paging port, all cross-checked against `zxnext.vhd`.

- **`Memory.SetDFFD` had the identical "only re-sync if the bank changed"
  bug as v1.3.5's `PageMemory` fix** — a repeated `$DFFD` write reasserting
  the same high-bank nibble could never reclaim MMU slots 6/7 from an
  earlier NextReg `$56`/`$57` override. Fixed the same way: the re-sync is
  now unconditional.
- **`Memory.PageMemoryPlus3` never re-synced MMU6/7 from the classic bank on
  an ordinary `$1FFD` write at all** — only on the special-paging-exit
  transition. Real hardware re-syncs MMU6/7 on every `$1FFD` write
  regardless (the fixed defaults for MMU2-5 genuinely *are* transition-only,
  per `zxnext.vhd:4653-4667` — that narrower rule is unchanged and now has
  its own regression test). Added a test recreating the specific historical
  NextZXOS boot-stack incident this file's transition-only logic was
  originally protecting, to prove the wider re-sync doesn't reopen it.
- **Port `$EFF7` was entirely unimplemented** — a classic Pentagon/Scorpion-
  style incompletely-decoded port ($E0F7-$EFF7 all alias it) that reveals
  RAM bank 0 instead of ROM at `$0000-$3FFF` when its bit 3 is set, and
  (like the other paging ports) re-syncs MMU6/7 on every write. Implemented
  `Memory.SetEFF7`/`EFF7Value` and wired the port decode into
  `ULA.WritePort`.
- Added a systematic test matrix (`pkg/memory/mmu_sync_matrix_test.go`)
  covering every paging-port write source (`$7FFD`, `$1FFD` normal-mode,
  `$DFFD`, `$EFF7`) against both a changed-value and a repeated-value write —
  the category of test that was missing and would have caught all of the
  above before they shipped.

## [v1.3.5]

### Fixed

- **Spectrum Next classic-paging → MMU6/7 re-sync was conditional on the RAM
  bank number changing** — `Memory.PageMemory` only re-synced the Next's 8K MMU
  slots 6/7 from the classic `$7FFD` RAM bank when the encoded bank actually
  differed from the previous write. Real hardware (`zxnext.vhd`) re-syncs on
  every `$7FFD`-family port write regardless of whether the value changed; the
  narrow exception that *does* exist is unrelated (a NextReg `$8E` write with
  bit 3 clear), and was already modelled correctly and separately. Because the
  old check was too broad, a game that re-asserts the same classic bank every
  frame (Night-Knight does, via its interrupt handler) could never reclaim
  `$C000-$DFFF` back from an earlier, unrelated NextReg `$56`/`$57` override —
  the CPU ended up executing stale data as code, leaking stack every frame
  until the game livelocked. Fixed by making the re-sync unconditional,
  matching the FPGA source exactly.

## [v1.3.4]

### Fixed

Codebase-wide bug-hunt and hardware-faithfulness sweep. Each fix below was
cross-checked against the FPGA VHDL, a chip datasheet, the reference driver
source, or the relevant file-format spec, and is covered by a regression test.

- **Pentagon / 48K frame-interrupt timing was swapped** — the maskable-interrupt
  assert position for the Pentagon and 48K models used each other's raster
  coordinates, firing the Pentagon INT near the top of the frame instead of the
  bottom. Corrected to match `zxula_timing.vhd`.
- **Spectrum Next Layer 2 read paging (`$123B` bit 2) was never applied** — the
  read-enable flag was decoded but the read redirect was missing from the memory
  read path, so software mapping Layer 2 for readback saw the wrong bytes. Also,
  Layer 2 map mode no longer redirects `$C000-$FFFF` in all-segments mode (the
  FPGA forces that region to the normal page).
- **`$DFFD` high-bank latch** — now cleared on reset (hard and NR$02 soft) and
  ignored while paging is locked, matching `port_dffd_reg` in the core.
- **ULA border colour** — a mid-frame border write no longer retroactively
  repaints the scanlines above it, and the border-change scanline is now timed
  with the per-model line length (224 T on 48K vs 228 on 128K).
- **Spectrum Next DAC channel routing** — three of the four SounDrive/Covox DAC
  port aliases were mapped to the wrong channel (A/B swapped, C on a stray port);
  corrected against the port table.
- **Next compositor LUS priority mode** was missing its ULA+tilemap pass, so ULA
  did not sit above sprites as the mode requires.
- **DAC writes at a frame boundary** were dropped instead of carrying into the
  next frame (audible on longer 128K/Pentagon frames).
- **ZX Printer** mid-print speed changes were ignored (the running-motor write
  never read the speed bit).
- **+3 floppy controller** — a data race between disk-save and the CPU thread,
  and a swallowed error that could commit a corrupt formatted track.
- **DISCiPLE / SAM Coupé WD177x** — Force-Interrupt during a busy transfer no
  longer mis-reports the status class; SAM multi-sector writes now advance past
  the first sector.
- **Interface 1 / Microdrive** idle-read values corrected to match the hardware.
- **Snapshot & RZX loaders hardened** against malformed/hostile files — bounded
  the allocations and zlib decompression driven by untrusted length fields,
  rejected truncated headers instead of silently misloading, and fixed the `.z80`
  v3 `+3` hardware-mode constant and an out-of-bounds panic in RZX recording.
- **FAT16 image builder** — nested-subdirectory `..` entries now point at the
  real parent (not root), and `.`/`..` names are written correctly.
- **Machine-switch fixes** — switching models via the menu now updates the
  per-frame timing budget; DAC and TR-DOS state no longer goes stale when
  crossing to a ZX80/ZX81/SAM machine and back.
- **Debugger** — fixed a refresh-goroutine leak on window close, stale
  disassembly after an in-place memory edit, a hex-parse failure on `$`-prefixed
  values ending in `d`/`D`, a data race on the `cont-until` condition, missing
  implicit-pause on several hook-installing commands, and assorted diagnostic
  read-out corrections.
- **Test harness** — the Next test harness now wires the sprite port and RTC I²C
  bus it was silently omitting, closing a coverage gap.
- ROM Info now names Interface 1 / ZX81 / ZX80 ROMs instead of "Unknown ROM".

## [v1.3.3]

### Added

- **`--tape` command-line flag** — load a `.tap`/`.tzx` into the deck at startup
  and start it playing, in **headless** mode as well as GUI. Headless runs
  previously had no way to feed a standard tape (only `--trd` disks); the guest
  reads it with `LOAD ""` (48K) or the 128 Tape Loader, with the fast-load trap
  installed automatically. (GitHub issue #5.)

### Fixed

- **Spectrum Next Nextoid no longer resets to the NextZXOS welcome on load** —
  the Copper's two-byte (`NR$60`) instruction-write phase was not reset by the
  `NR$61`/`NR$62` cursor writes, so a stray staged byte paired NextZXOS's Copper
  list off-by-one and decoded as garbage `MOVE` writes across the whole NextReg
  config every frame. The cursor writes now reset the byte phase, matching the
  FPGA.
- **Spectrum Next over-border sprites are visible** — the Next sprite frame is
  320×256 (32px over-border) but the renderer cropped to the classic 320×240,
  hiding sprites parked in the bottom strip (e.g. the NextBASIC Invaders player
  ship at sprite Y=240). The full-height path now renders when the Next sprite
  layer is active.
- **Spectrum Next "Integer out of range" in NextBASIC sprite reads** — an
  Alt-ROM read redirect (`NR$8C`) took precedence over an 8K MMU slot mapping a
  real RAM bank, so `SPRITE AT` read Alt-ROM bytes (with a stray high bit) for
  the sprite cache. The MMU RAM mapping now wins, matching the FPGA's
  `sram_pre_override`.

### Changed

- **Honest project-status documentation** (GitHub issue #4) — the README now
  carries an explicit status note distinguishing the mature classic line from
  the young Spectrum-Next *game* compatibility, the absolute "`.NEX` games load
  and run" claim is qualified, and the compatibility manifest's Next section
  lists real per-title statuses (working, caveated, and known-broken) instead of
  a single placeholder.

## [v1.3.2]

### Fixed

- **Spectrum Next divMMC overlay no longer leaks under CONMEM** — the divMMC
  automap-held latch was kept paged-in across a page-out (the `$1FF8-$1FFF`
  off-area or RETN) whenever CONMEM (port `$E3` bit 7) was set, contradicting
  the FPGA core where the latch clears regardless of CONMEM (an orthogonal
  force-in). The stale latch left the divMMC RAM overlay masking ROM after the
  firmware cleared CONMEM, so the CPU ran divMMC RAM as code — a NextBASIC
  program (e.g. NextBASIC Invaders) derailed and reset on start-up. The overlay
  now stays mapped while CONMEM is held and drops once CONMEM clears.

## [v1.3.1]

### Fixed

- **Spectrum Next text viewer & 64/85-column modes** — viewing a text file in
  the NextZXOS Browser (and the editor / `.bas`↔`.txt` views) rendered as
  garbled noise. These use the Timex 512×192 8x1 hi-res display mode (port
  `$FF` mode 110), which was unimplemented and fell through to a plain-ULA
  render. Implemented it (two-display-file interleave + hi-res colour); 85-column
  text now renders correctly.

## [v1.3.0]

**More Spectrum Next games run correctly** — hardware-sprite games, games that
gate on the core version, and games that need a slower CPU.

### Added

- **CPU speed control** (Machine → CPU Speed: Auto / 3.5 / 7 / 14 / 28 MHz).
  NextZXOS runs the Next at 28 MHz by default, which makes some games (e.g.
  RustHawk) run far too fast; pinning a slower speed is the emulator equivalent
  of the Next's on-screen speed selector / F8 hotkey. "Auto" follows the game/OS.
- **Sprite Attribute Upload port `$57`** — the auto-incrementing attribute
  stream many games (e.g. Nextoid) use to upload all their sprites each frame.

### Fixed

- **Hardware sprites now render for games like Nextoid** — the bat, ball and
  HUD were invisible. Five fixes: the `$57` attribute-upload port (above);
  4-byte sprites now default to 8bpp (per the FPGA); sprites composite in
  frame coordinates (320×256, paper at 32,32) with a border pass so HUD
  sprites in the border show; NextReg `$15` layer-priority decoded from bits
  4:2 (not 1:0) — the bug that hid sprites behind Layer 2; and NextReg `$4B`
  sprite transparency is honoured so 8bpp sprites' see-through cells work.
- **"Core x.xx.xx needed" abort / reboot to the NextZXOS welcome screen** — the
  read-only NextReg core-version registers (`$01`/`$0E`) are no longer
  corruptible by guest pokes; reports a stable core 3.02.03. The Machine ID
  (`$00`) stays writable so games' emulator/hardware probes still work.

## [v1.2.2]

Fixes **AY music on the Spectrum Next** — 128K games (e.g. Renegade) run under
the Next's 128K persona now play their music.

### Fixed
- **AY music was silent on the Spectrum Next** (e.g. a 128K game like Renegade
  run under the Next's 128K persona). Two bugs:
  1. **NextReg `$06` was misread.** `$06` ("Peripheral 2") bits 1-0 are the
     audio chip mode (00 YM / 01 AY / 10 ZXN-8950 / **11 = hold all AY in
     reset**) and **bit 2 is PS/2 mode** — but `engine.Select` read bit 2 as
     "AY disable" and bits 1-0 as a chip index. NextZXOS sets bit 2 (PS/2)
     during boot, which then muted all AY. Now only bits 1-0 == 11 silences the
     engine, and the TurboSound chip select moves to the `$FFFD` protocol
     (write `$FF`/`$FE`/`$FD` to select chip 0/1/2, new `Engine.SelectChip`).
  2. **The engine was never mixed into audio.** `$FFFD`/`$BFFD` writes route to
     the engine's active chip, but the mixer was still fed the classic single
     `u.ay`, so the generated music was never heard. The engine now satisfies
     the audio AY source (new `Engine.MixInto` sums its TurboSound chips) and is
     wired into the mixer on the Next (and re-wired across reset).

  Classic 128K AY is unchanged.

## [v1.2.1]

Adds the **test/evidence layer**: an automated real-software compatibility
corpus and loader fuzzing — the regression guard the mechanism-level unit
tests can't provide (the Renegade-128K tape failure passed every test).

### Added
- **Compatibility corpus** (`cmd/zx_go` `TestCompatibilityCorpus`) — loads real
  software headless, drives it to its title/menu screen, and asserts the screen
  matches a recorded golden over a settle window (robust to the odd transient
  frame). It catches "a real game silently stopped loading" — exactly the class
  of bug the Renegade-128K tape failure was. Game files are copyrighted and not
  committed; point it at a folder with `ZX_GO_CORPUS` (titles whose files are
  absent skip, so CI stays green), and record goldens with
  `ZX_GO_CORPUS_UPDATE=1`. Seeded with Renegade 128K. See `docs/compatibility.md`.
- **Loader fuzzing** — Go native fuzz targets for the snapshot (`FuzzLoadBytes`
  — .sna/.z80/.szx), TR-DOS image (`FuzzLoadImage`), and tape (`FuzzLoadTAP` /
  `FuzzLoadTZX`) parsers. Their seed corpora run in the normal suite; extended
  fuzzing (`-fuzz`) found no panics across millions of executions, so the
  parsers reject hostile/corrupt input cleanly rather than crashing.

## [v1.2.0]

Adds the **Sinclair ZX80 and ZX81** and the **Pentagon 128** as supported
machines, the **TR-DOS / Beta Disk** interface, **quick save/load** state
slots, a **user manual**, and completes the **zxnDMA** (IO endpoints + prescaler
timing + read-back).

### Added
- **Tape-loading sound** — the EAR signal is now mixed into the audio output
  while a tape plays, so you hear the authentic pilot whistle + data screech (a
  real 48K plays it through the beeper, a 128K through the TV; either way the
  tape signal reaches the speaker). Reconstructed with the same box-filter as
  the beeper at a lower amplitude. With *Fast Tape Loading* on it's a brief
  accelerated chirp; turn that off for the full real-time loading sound.
- **Fast tape loading** (Tape menu → *Fast Tape Loading*, on by default) — while
  a tape is actively loading, the emulation runs a burst of frames per tick, so
  a game whose code loads through the real-time / edge-timed loader (custom
  turbo loaders can't be trap-accelerated) finishes in seconds rather than the
  several real-time minutes a full tape takes. Toggle off for authentic
  real-time loading.
- **Pentagon 128** (`roms.ModelPentagon`) — the Soviet ZX Spectrum 128 clone:
  128K paging and AY like the 128K, but with **no memory contention**
  (`setupModel` disables it; `SetTStatePtr`/`SwitchModel` honour the flag) and
  the Pentagon **71680-T-state frame**. Bank 0 is the Pentagon editor ROM,
  bank 1 the standard 48 BASIC. Boots to the 128 menu and runs 128/48 BASIC.
  `Machine → Pentagon 128` / `--pentagon`.
- **TR-DOS / Beta Disk interface** (`pkg/betadisk`) — the disk standard for the
  Pentagon and other 128K clones. A WD1793 floppy controller (Restore/Seek/
  Step/Read-Sector/Write-Sector/Read-Address/Force-Interrupt with DRQ/INTRQ),
  raw `.TRD` sector images with 80/40-track single/double-sided geometry
  inference, and the Beta port decode ($1F/$3F/$5F/$7F/$FF — exact low byte, so
  no clash with SpecDrum/Covox/Kempston-mouse) and $FF system register (drive,
  side-inverted bit 4, active-low reset bit 2), cross-checked against Fuse's
  `beta.c`. The TR-DOS ROM auto-pages over $0000-$3FFF on a $3Dxx instruction
  fetch (gated to the 48 BASIC ROM) and pages out at $4000+, driven by a CPU
  pre-fetch hook. Mount with `File → Load TR-DOS Disk A/B (.TRD)` or `--trd`
  (48K/128K/+2/Pentagon).
- **zxnDMA completion** (`pkg/next/dma`) — the Spectrum Next DMA now handles
  IO-port endpoints (WR1/WR2 D3 — DMA uploads to the sprite-image, Layer 2 and
  DAC ports, routed through the ULA port dispatch, instead of corrupting
  memory), the per-byte prescaler + cycle-length + burst/continuous mode (a
  continuous-mode transfer's T-state duration is charged to the CPU clock),
  Continue (`$D3`) and WR5 auto-restart, and read-mask register read-back
  (`$BB`/`$A7` + port-0x6B reads return the status / byte-counter / port-address
  registers). Burst-mode + prescaler transfers interleave with the CPU (one byte
  per prescaler T-states, pumped from a per-instruction Step), so DMA-streamed
  sampled audio is paced across the CPU timeline while the CPU runs in the gaps.
  Cross-checked against the official `zxndma.txt`.
- **SpecDrum & Covox** (`pkg/audiodac`) — classic-Spectrum 8-bit DAC sound
  add-ons: Cheetah SpecDrum (OUT $DF) and a mono Covox (OUT $FB), opt-in from
  the Peripherals menu and persisted in config. Event-timed like the beeper —
  each write is recorded with its T-state offset and reconstructed per
  audio-sample (box-filter), then mixed into the beeper output — so PCM drum
  playback is sample-accurate rather than a per-frame snapshot. Enabling Covox
  claims port $FB (so it and the ZX Printer are mutually exclusive, as on real
  hardware).
- **ZX80 and ZX81 emulation** (`pkg/zx8x`). Faithful CPU-generated
  display: the Z80 itself produces the picture via A15 video fetches
  that are forced to NOP while the latched byte indexes a character
  bitmap through the I register and a scanline counter (matching the
  MAME/ZEsarUX references and the hardware write-ups). The maskable
  interrupt is driven off R-register bit 6 (with refresh continuing
  during HALT), and the ZX81 SLOW-mode NMI generator (ports $FE/$FD)
  paces the top/bottom borders. ZX80 uses the 4 KB ROM with the
  character set at $0E00; ZX81 the 8 KB ROM at $1E00.
- New `roms.ModelZX80` / `roms.ModelZX81`, their 4 KB / 8 KB ROMs
  (embedded), and mirrored memory maps (ROM mirrored to fill the 16 KB
  page; RAM mirrored into the upper 32 KB).
- ZX8x keyboard matrix; `.P` / `.81` (ZX81) and `.O` / `.80` (ZX80)
  program loading via `File → Open File…`.
- `Machine → Sinclair ZX81 / ZX80` menu entries and `--zx81` / `--zx80`
  command-line flags.
- Z80 core: opt-in `RefreshDuringHalt` (advances R during HALT, as real
  hardware does) and an `M1FetchHook` for opcode substitution — both
  used by the ZX8x video / interrupt path; classic Spectrum behaviour
  is unchanged (both off by default).
- **Quick save / quick load state slots** — `F2` snapshots the running
  machine to a single SZX slot under the user config dir; `F4` restores
  it. Also in the **File** menu, and run under `withEmulationPaused` so
  it can't race the emulation goroutine. Gated to the machines with an
  SZX representation (48K…+3 and Pentagon); the ZX80/ZX81 and the Next
  are excluded.
- **Floating bus** — `IN` from an unattached even port returns the byte
  the ULA is fetching for the current display position (Ramsoft/FUSE
  model), so floating-bus timing tricks read correctly.
- **Event-timed Spectrum Next DAC** — the four-channel Next DAC bank is
  now reconstructed sample-accurately (per-write T-state offsets,
  box-filter) and mixed alongside the beeper/AY, matching the SpecDrum/
  Covox path, instead of a per-audio-pull snapshot.
- **User manual** ([`docs/manual.md`](docs/manual.md)) — an everyday
  user guide: machines, loading software, save states, peripherals,
  sound, the keyboard, and troubleshooting. Linked from the README.

### Fixed
- **Tape loading on the 128K / +2 / Pentagon (and custom-loader games on every
  model).** Two bugs combined to stop a game like Renegade loading from the 128
  menu's Tape Loader: (1) the fast-load `LD-BYTES` trap was gated to the 48K, so
  on the 128-family it never fired (and the comment's claim that other models
  "fall back to the slow loader" was false); it now fires whenever the 48 BASIC
  ROM — which holds `LD-BYTES` at `$0556` — is the ROM paged at `$0000` (bank 1
  on the 128/+2/Pentagon, bank 3 on the +2A/+3). (2) The tape EAR bit was
  advanced only **once per frame**, freezing the level for a whole 69888-T
  frame, so edge-timed loaders saw no pulses; the tape is now advanced on every
  port-`$FE` read against the live CPU T-state, so both the ROM loader and
  games' custom (turbo) loaders sample real pulses. Renegade now loads all nine
  blocks end-to-end on the 128K (regression-tested).
- **Tape fast-load trap / real-time player desync.** The trap's block
  consumption (`NextBlock`) and the real-time pulse player (`Update`) shared no
  pulse state, so after the trap loaded a block, the first real-time `Update`
  replayed the previous block's pulses (or skipped a block) — feeding garbage to
  any custom turbo loader that took over and producing "R Tape loading error".
  `Update` now tracks which block its pulses belong to and regenerates from the
  current block's pilot when the trap moved the index.
- Spectrum Next NextReg reset-default conformance (vs the FPGA `zxnext.vhd`):
  NR$06 = `$A0` (CPU-speed + 50/60 Hz hotkey enables) and NR$98/$99 = `$FF`/`$01`
  (Pi GPIO) now match the VHDL reset vector (were `$00`). Documented in
  `VHDL_CONFORMANCE.md`; the NR$68 "gap" was a misread (our `$00` is already
  correct — the VHDL inverts bit 7 on write) and the NR$18–$1B clip-window
  resets were verified conformant.

## [v1.0 RC1]

First release candidate. The Spectrum Next boots NextZXOS end-to-end
through the authentic FPGA-bootrom → TBBLUE → NextZXOS chain; menu
items launch (Browser, NextBASIC), the firmware config menu boots
every machine personality, and the classic 48K…+3 models are
feature-complete.

### Added
- **Spectrum Next menu-item launch** — ENTER on Browser opens the
  SD card's `C:/` listing; NextBASIC runs interactive programs.
- **Firmware config-menu machine selection** boots the chosen
  personality (soft reset re-arms the FPGA bootrom in config mode).
- **FAT32-LBA SD-image builder** (#227) — boot out-of-box from the
  distro tree with no card image; `--sd-writeback` persists guest
  writes (with a `.bak` backup).
- **On-demand NextZXOS ROM download** from the official Spectrum
  Next distribution — the licensed ROMs are not bundled.
- **Save Screenshot** works across every machine type and Next
  video mode.
- Diagnostic instruments: `ZX_GO_PAGING_TRACE`, divMMC conmem
  page-events, `ZX_GO_RTC_TRACE`.

### Fixed
- **#255 menu-launch stall** — divMMC overlay now beats the Alt-ROM
  read redirect (zxnext.vhd memory-mux order).
- **Switching to the Next at runtime** tears down clashing
  classic-bus peripherals (DISCiPLE/Multiface/IF1) — fixes the
  uninitialised-RAM "coloured blocks" screen.
- **Classic 48K black screen** — removed a fake `gdos.rom` that
  shadowed the real embedded GDOS and bricked DISCiPLE boots.
- **divMMC RAM** config-mode window covers the full 128 KB.
- **tt-rewind** restores the Halted flag and rewinds the
  instruction counter.

## [Unreleased]

### Fixed
- **128K BASIC launch now shows the Sinclair "128" menu** (was a black
  screen / NextZXOS welcome). NextZXOS's More…→128K BASIC fires the
  Multiface NMI to snapshot machine state; its handler reads paging
  registers back through Multiface-3 ports that ours treated as open
  bus. Three faithful, VHDL-backed fixes (found via an ours-vs-reference
  first-divergence audit — `[[project_divergence_audit]]`): (1) cold
  RAM now zero-fills like the oracle (was a `$C0FFEE` pseudo-random
  workaround for a since-fixed banking bug); (2) `IN $7F3F`→port
  `$7FFD` and `IN $1F3F`→port `$1FFD` when the Multiface is active
  (`multiface.vhd:43-44`, `zxnext.vhd` `mf_port_dat` mux) — open bus
  here flipped a `cp $04` in the MF ROM into the abort path; (3) `IN
  $123B` returns the last value written to the Layer 2 port
  (`zxnext.vhd:2822`) — open bus left Layer 2 visible, bleeding
  striping into the menu's top border. The menu now renders
  pixel-identical to the reference emulator and is stable over a long soak.
- **i2c DS1307 real-time clock on ports `$103B`/`$113B`.** The Next
  bit-bangs its RTC over dedicated SCL/SDA I/O ports
  (`zxnext.vhd:2630-2631` decode, `:3234-3250` open-drain latches);
  previously the bus was absent so SDA floated and NextZXOS's clock
  fetch failed every frame — degrading the main-menu engine into a
  re-render storm. The new `pkg/next/rtc.Bus` implements the full i2c
  slave protocol (START/STOP, address `$68`, ACKs, register pointer,
  sequential reads) over the existing host-clock DS1307 register
  model. The NextZXOS menu now renders its date/time line and idles
  at the reference cadence.
- **SD/SPI Ncr response latency.** Every SD command response (R1/R3/R7,
  CSD/CID, read-block) is now preceded by one $FF Ncr pad byte, matching
  the VHDL SPI master and real-card timing (previously responses arrived
  on the byte immediately after the command, one byte early). Boot-neutral
  for NextZXOS but removes a whole class of off-by-one-byte driver
  divergences vs hardware. Tests: `ncr_pad_test.go`.
- **Spectrum Next cold-boot faithfulness (ongoing).** Several
  hardware-faithful Z80/NextReg/divMMC fixes that advance the NextZXOS
  cold boot, each verified against the FPGA VHDL and an instruction-level
  reference oracle: divMMC automap variant handling (the four NR$B8/$B9/$BA
  rom/rom3 × instant/delayed combinations, fixing the `$0038` IM1 derail);
  divMMC ROM3-context gating for the `$0038`/`$3DXX` entry points; NR$00
  machine-ID reported as `$0A` (ZX Spectrum Next issue 2, per
  `zxnext_top_issue2.vhd` — an earlier `$08` "emulator" value made ROM1's
  `$1E69` machine-ID check take the emulator branch and diverge from
  hardware); and a **faithful clip-window
  model** for NextRegs `$18`-`$1B` (four x1/x2/y1/y2 sub-coordinates cycled
  by a 2-bit read/write index, with NR`$1C` index reset/packed read-back)
  replacing the previous single-byte approximation. Also: **NR$41 palette
  write now sets the 9-bit value's low blue bit to `(byte bit1 | bit0)`**
  per `zxnext.vhd` (was forced to 0), and **NR$41/$44 gained read-back
  handlers** (returning the palette value, not the last-written byte). The
  cold boot is not yet at the Browser; the running investigation is logged
  in the development log.
- NextReg dispatcher gained a reset hook (`SetOnReset`) so subsystems with
  state outside the 256-byte register array (the clip-window index +
  coordinates) restore their power-on defaults correctly on soft/hard reset.

### Added
- Spectrum Next support (`ModelNext`) across an eight-sprint program:
  Z80N CPU, 8K MMU, NextReg port file, divMMC, esxDOS API, .NEX V1.2
  loader, SD card host-directory mount, multi-AY, 9-bit palette,
  Layer 2 (256×192 8bpp), sprites (128, 4bpp basic), compositor with
  four priority modes, Copper coprocessor, zxnDMA, RTC, UART stub.
  Status table in `docs/spectrum-next.md` flags partial/deferred work.
- `pkg/config` settings persistence at `$UserConfigDir/zx_go/config.json`.
  Schema covers machine model, window scale, joystick mapping, CRT
  filter, and enabled peripherals (DISCiPLE, Multiface variant,
  Interface 1, Kempston Mouse, ZX Printer). Atomic save via
  tmp + rename. Restored at startup with a single best-effort reboot
  if any peripheral that affects boot ROM was re-enabled.
- Z80 conformance suites: vendored Cringle `zexdoc.com` and `zexall.com`
  under `pkg/z80/testdata/conformance/` with a CP/M BDOS-trap harness
  (`TestZex{doc,all}`) and a dedicated `conformance` CI job. Both
  suites pass.
- **TZX tape save.** `pkg/ula/tzx.go:SaveTZX` emits block types 0x10
  (standard speed), 0x11 (turbo), and 0x14 (pure data). Wired into
  the File menu as "Save Tape (TZX)..." next to the existing TAP
  save. Tests cover roundtrip + hex-comparison against a hand-
  crafted reference layout, so a future field-order regression is
  caught before it ships. TZX-only metadata that LoadTZX skipped
  (pure tone, pulse sequence, group / archive info, text) is not
  preserved across save.
- `CONTRIBUTING.md` — build / test / lint commands, project layout,
  three test-gating levels (default vs `-short` vs `conformance`),
  "adding a ULA test" pattern with verified-accurate
  `testharness.New` API, "adding a NextReg handler" pattern,
  filing-issues guidance.
- `docs/compatibility.md` — title manifest with status legend,
  per-genre tables (48K / 128K / +3 disk / demoscene / Next), a
  Foundation Tests section that maps integration tests to the
  category of title they prove the foundation works for, and an
  "add a title" protocol for contributors. Cobra (Ocean, 1986) on
  +3 disk verified as "Parses cleanly" through
  `plus3fdc.ParseDiskImage`.
- **Spectrum Next Tier 3 polish** — three items that move the Next
  from "experimental" toward "stable":
  - esxDOS F_WRITE / F_OPENDIR / F_READDIR end-to-end tests via
    the RST 8 → dispatcher → host-directory mount path. Six new
    tests in `pkg/next/esxdos/file_handlers_test.go`.
  - .NEX banks 8+ support. Memory model extended from 8 to 128
    16K pages; ModelNext allocates the full 2 MB, classic models
    stay bit-identical in heap footprint (only 0..7 allocated).
    `testharness.LoadNEX` no longer drops banks > 7.
    `TestLoadNEXAcceptsExtendedBanks` is the regression test.
  - DAC → audio mixer wiring. `dac.Bank.MixInto` produces centred
    contributions; `audio.DACSource` interface mirrors the AY
    mixing path; `ULA.SetNextDAC` routes port writes and
    auto-wires the bank into the mixer when `EnableAudio()` runs.
    v1.0 mixes at frame-snapshot granularity; per-write event
    integration deferred to v1.1.

### Changed
- Existing CI test matrix now passes `-short`; long conformance tests
  run only in the dedicated `conformance` job.

### Fixed
- Z80 halfcarry add/sub lookup tables (indices 1/3/5/6 were wrong).
- CCF set H from the new C; corrected to old C per Z80 spec.
- DAA's hand-rolled H flag; delegated to add/sub so it flows through
  the standard lookup tables.
- DDCB dispatcher rewritten to handle the undocumented "store result
  to register named by low 3 opcode bits" variants and to call `sll()`
  for SLL (IX+d) instead of `sla()`.
- ADC HL,rr / SBC HL,rr undocumented F3/F5 now taken from the high
  byte (bits 11 and 13) instead of the low byte.
- BIT n,(IX+d) / BIT n,(IY+d) undocumented F3/F5 now taken from the
  effective address high byte instead of the operand value.
- Partial MEMPTR (Z80 WZ register) implementation. Update sites
  covered: `LD (BC/DE/nn),A`, `LD A,(BC/DE/nn)`, `LD HL,(nn)`,
  `LD (nn),HL`, the ED-prefixed `LD rr,(nn)` and `LD (nn),rr`
  family, and `ADC/SBC HL,rr`. **Not** yet updated by jumps,
  calls, returns, RST, JR, DJNZ, block moves, block compares,
  block I/O, RLD/RRD, EX (SP),HL/IX/IY, or single-byte I/O.
  Programs that test undocumented F3/F5 from BIT n,(HL)
  immediately after one of those unhandled operations will still
  see wrong flag bits.
- All `Ctrl` / `Alt` / `Cmd-Super` keys on the host keyboard now
  map to Spectrum `SYMBOL SHIFT`. Previously `LeftSuper` /
  `RightSuper` (= macOS Cmd) were explicitly mapped to `{}` and
  did nothing; `LeftCommand` / `RightCommand` keymap entries
  were dead code (Fyne's desktop driver uses `LeftSuper` /
  `RightSuper` for the Cmd keys, not `LeftCommand`).

## [0.3.2] — 2026-05-11

### Changed
- `pkg/z80` no longer depends on the concrete `memory.Memory` type;
  it now talks to a `Memory` interface so peripherals (DISCiPLE,
  IF1, Multiface, divMMC) can compose without import cycles.

## [0.3.1] — 2026-05-11

### Fixed
- Tape fast-load trap (`LD-BYTES`) reads the main A/F registers at
  PC=0x0556, not the alternate set.
- DISCiPLE auto-page triggers now match FUSE's exact PC list.
- DISCiPLE RAM pre-initialised on `PageIn` so the NMI handler works.
- DISCiPLE port mapping, paging, and ROM corrected per FUSE.
- DISCiPLE starts paged out so mid-session enable is safe.
- DISCiPLE cold boot reliable — GDOS inits, BASIC responds.

### Added
- File menu items for loading DISCiPLE disk images.

## [0.3.0] — 2026-04-09

### Added
- AY-3-8912 sound chip (128K-series models).
- TZX tape loader; fast-load trap at 0x0556.
- Kempston / Sinclair 1+2 / Cursor joystick interfaces.
- +3 FDC (µPD765A) with DSK, EDSK, UDI, MGT/IMG, TRD, SAD, D40/D80
  formats including weak sectors and deleted DAMs.
- RZX input recording and playback with per-frame instruction count,
  embedded snapshots, multi-snapshot support.
- Sinclair Interface 1 + Microdrive support with FUSE parity.
- Sinclair Interface 2 ROM cartridge slot (48K-only).
- Kempston Mouse and ZX Printer peripherals.
- Multiface 1 / 128 / 3 with hardware-accurate NMI + paging.
- View menu with 100%–300% scale and full-screen toggle.
- CRT scanline filter post-process.
- Debugger improvements: full 64KB hex dump; Hex/Dec/Oct address entry.
- Spectrum-themed app icon.
- Beeper amplitude bump and pre-filled ring buffer to fix BEEP fuzz.
- Embedded ROMs with optional filesystem override.
- Headless scripted test harness; IF1 CAT gold-standard test.
- Pentagon ROM, GDOS ROM.

### Fixed
- Multiface 3 paging corruption: freeze RAM/screen during session.
- Multiface NMI sequencing: page in ROM at NMI execution time.
- Multiface 128 port decode backwards-compatible with MF1 pattern.
- NMI IFF2 preservation; unimplemented DD/FD opcodes filled in.
- Segfault when toggling peripherals in menu.
- 4:3 aspect ratio preserved with black letterbox bars.
- Lint compliance for golangci-lint v2.
- ZX Printer drum advances on reads so ROM-driven `COPY` actually prints.

## [0.2.0] — 2026-04-05

### Fixed
- Release packaging: binaries uploaded directly from build jobs
  instead of an artifact round-trip; packaged as tar.gz / zip to
  preserve executable permission.

### Added
- Undocumented 8-bit IXh / IXl / IYh / IYl Z80 ops.

### Fixed
- Paging-port reset on reboot so the +3 menu returns.

[Unreleased]: https://github.com/conorarmstrong/zx_go/compare/v0.3.2...HEAD
[0.3.2]: https://github.com/conorarmstrong/zx_go/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/conorarmstrong/zx_go/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/conorarmstrong/zx_go/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/conorarmstrong/zx_go/releases/tag/v0.2.0
