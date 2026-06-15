# Changelog

All notable changes to this project are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the
project targets [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.2.0]

Adds the **Sinclair ZX80 and ZX81** and the **Pentagon 128** as supported
machines, the **TR-DOS / Beta Disk** interface, **quick save/load** state
slots, a **user manual**, and completes the **zxnDMA** (IO endpoints + prescaler
timing + read-back).

### Added
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
