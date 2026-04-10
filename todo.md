# zx_go — v1.0 TODO

Full list of work identified on 2026-04-09 as remaining before a v1.0
release. Derived from a code walk + grep of `TODO` / `FIXME` / `placeholder`
/ `not implemented` markers, plus comparison against what a period-accurate
Spectrum emulator should ship with.

Priority markers in square brackets at the start of each item:

- **[blocker]** — must be resolved before v1.0. Either broken or
  falsely advertised functionality.
- **[should-do]** — strong pride-of-product items. v1.0 without them
  would feel unfinished.
- **[nice-to-have]** — can ship v1.0 without these; catalogued so
  they're not forgotten.

---

## Emulation accuracy

- [ ] **[blocker]** Audit the Z80 ED-prefix opcode table for missing
  documented instructions. `pkg/z80/z80.go:1557-1559` silently treats
  any unrecognised ED opcode as an 8-T-state NOP (correct for
  *undocumented* ED opcodes, but there's no verification that all
  *documented* ED instructions are implemented). Cross-reference
  against a full Z80 opcode table (Zilog docs / Zaks / FUSE's
  `z80_ed.c`). Add any missing documented instruction with correct
  flags + cycle count.

- [ ] **[should-do]** Wire Z80 accuracy test suites into CI. Standard
  tests: Zexdoc (documented instruction behaviour) and Zexall
  (documented + undocumented), plus the Spectrum-specific
  `z80full.tap`. None of these currently run. Publish a pass/fail
  report in README and fix any failures.

- [ ] **[nice-to-have]** Implement RZX DSA signing.
  `pkg/rzx/recording.go:82` and `:265` write the Signed header flag
  but the signing itself is a TODO. FUSE has the same gap, so this
  matches upstream parity, but a real v1.0 would close it.

## Peripherals — blockers

- [ ] **[blocker]** Fix or remove the DISCiPLE disk interface.
  `pkg/disciple/disciple.go` is almost entirely a stub and the
  Peripherals menu enables something that does almost nothing.
  Specifics:
  - `readSector()` (line 284-289) returns zero bytes —
    "In a real implementation, this would read from an MGT disk image"
  - `writeSector()` (line 291-296) is a no-op
  - `pageInROM()` (line 332-341) sets `d.romPaged = true` but
    doesn't actually modify the memory map — "In a real
    implementation, this would modify the memory map to page in the
    Disciple ROM at 0x0000-0x1FFF"
  - `LoadDisk()` (line 381) is a placeholder
  - Falls back to a hand-rolled 4-byte placeholder ROM if the real
    one isn't bundled (line 104-108)

  Either finish the implementation (port from FUSE `disciple.c`, or
  reuse the WD1770 patterns from `pkg/plus3fdc/` as a reference) or
  remove the menu item and the README mention entirely. Honesty
  beats a broken feature.

- [x] **[blocker]** ~~Fix or remove Multiface `SaveSnapshot` /
  `LoadSnapshot` stubs~~ — deleted. Neither method was called from
  the UI; dead code all the way through to `PeripheralManager`.

## Peripherals — emulation gaps

- [ ] **[should-do]** Implement the IF1 RS-232 serial port.
  `pkg/if1/ula.go:60-62` currently returns "no peripheral connected"
  for serial-port polling, so the IF1 ROM silently proceeds past
  any serial wait. A handful of period programs used RS-232 (MIDI
  hardware, serial printers, modem software). At minimum, wire it
  to a host file or pipe so saved serial data is accessible to the
  user.

- [ ] **[nice-to-have]** Implement IF1 SinclairNET.
  Same stub path as RS-232. Very niche hardware (almost no
  commercial software used it) but authentic to the interface.

- [ ] **[nice-to-have]** Beta Disk / TR-DOS FDC implementation.
  `pkg/roms/data/trdos.rom` is shipped but no FDC exists for it.
  Required for the Soviet Pentagon / ATM ecosystem.

- [ ] **[nice-to-have]** Covox (DISCiPLE / +D) 8-bit DAC sound
  output. A simple DAC peripheral driven from a parallel port —
  adds sample-playback support used by a few late-era demos.

- [ ] **[nice-to-have]** TurboSound — two AY-3-8912 chips in stereo
  configuration. Adds depth to AY-based music.

- [ ] **[nice-to-have]** SpecDrum — a sample-playback drum machine
  peripheral from Cheetah Marketing.

- [ ] **[nice-to-have]** Opus Discovery disk interface.

- [ ] **[nice-to-have]** Rotronics Wafadrive — competed with the
  Microdrive; tape-loop mass storage with a slightly different
  format.

- [ ] **[nice-to-have]** AMX Mouse. Called out as explicitly not
  implemented in the header comment at
  `pkg/kempmouse/kempmouse.go:3`.

- [ ] **[nice-to-have]** Spectranet (modern Ethernet adapter).
  Debatable fit with the period-authenticity philosophy, since no
  original Spectrum shipped with networking. Include only if the
  user wants retro-network-play demos.

## Testing and CI

- [x] **[blocker]** ~~Run `go test ./...` on all three OSes in CI.~~
  Currently `ci.yml` runs tests only on `ubuntu-latest`. The
  `release.yml` builds binaries for Linux / macOS-arm64 /
  macOS-amd64 / Windows but never runs tests on any of them. A
  macOS- or Windows-specific runtime bug could ship undetected.
  Add `macos-latest` and `windows-latest` to the test matrix in
  `ci.yml`.

- [ ] **[should-do]** Publish a compatibility manifest in the README:
  a verified list of 20-30 commercial titles that run correctly,
  covering each major genre (platformer, shooter, adventure,
  simulation, demo, utility). Doesn't have to be exhaustive — the
  goal is "v1.0 is known to work on this known-good corpus".

- [x] **[should-do]** ~~Add a test coverage metric.~~ `-cover` flag
  added to CI test job. Badge/artifact upload deferred.

## Documentation and repo hygiene

- [x] **[blocker]** ~~Delete or rewrite stale Z80 docs:
  `opcode_matrix.md` and `Z80_INSTRUCTION_MATRIX.md`.~~ Deleted.

- [x] **[blocker]** ~~Remove the broken symlink at the repo root:
  `./LEGACY`.~~ Deleted.

- [ ] **[blocker]** Review `KEYBOARD_GUIDE.md` for staleness.
  Verify it matches the current `pkg/keyboard/` behaviour. If the
  modifier / matrix layout has changed since it was written,
  update or delete.

- [ ] **[should-do]** Add `CHANGELOG.md` with sections for each
  tagged release. The v0.2.0 → v0.3.0 delta already exists in the
  git tag annotation and can seed the first entry. User-facing
  changelog helps people browsing the repo for "what's new".

- [ ] **[should-do]** Add `CONTRIBUTING.md` with build / test /
  style guidance. v1.0 is the point where outside contributions
  become plausible; onboarding docs pay off.

- [x] **[nice-to-have]** ~~Stray `zx_go` binary at repo root.~~
  README now builds to `bin/zx_go`; `.gitignore` updated to `/bin/`.

## User experience

- [ ] **[should-do]** Settings persistence. The emulator doesn't
  remember the last Machine model, joystick choice, scale level, or
  enabled peripherals between runs. Add a small config file
  (`~/.config/zx_go/config.json` or similar) that's written on
  exit and restored on startup.

- [ ] **[nice-to-have]** Recent files menu in the File menu. Common
  quality-of-life feature — shows the last N loaded snapshots /
  tapes / disks.

- [ ] **[nice-to-have]** Drag-and-drop file loading. Fyne supports
  it; needs wiring to the existing format-detection dispatch that
  the File menu uses.

- [ ] **[nice-to-have]** "Save Session" — bundle snapshot + loaded
  tape + cartridge + disk state + peripheral configuration into
  one file. Lets users save a mid-game state exactly as it appears
  on screen and reload it later without manually re-inserting
  media.

- [ ] **[nice-to-have]** First-run experience. New users boot into
  a bare BASIC prompt with no hints. Offer a small "what to do
  next" dialog on first launch, or bundle a public-domain
  cartridge / tape so users have something to play immediately.

- [ ] **[nice-to-have]** Audit the Emulator menu for the "ROM info"
  and "Peripheral Status" dialogs the deleted `FEATURES.md` used to
  mention. Verify whether they still exist as menu items; if not,
  either restore them or confirm their removal was intentional.
