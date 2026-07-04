# Changelog

All notable changes to this project are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the
project targets [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
