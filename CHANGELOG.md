# Changelog

All notable changes to this project are documented here. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the
project targets [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **The Z80 CTC is wired, so a guest can use its timers.** All eight
  counter/timer channels answer at ports `$183B`..`$1F3B` (`zxnext.vhd:2690`)
  and tick on the CPU clock: a program can write a channel's control word and
  time constant and read back its live down-counter. The device itself was
  complete and pinned by golden vectors captured from the FPGA VHDL under GHDL
  long before this; nothing constructed it and no port reached it, so no program
  could tell it existed. Its **interrupts** are still not delivered, because the
  IM2 daisy chain is not connected to the CPU's interrupt path.

- **Coverage claims now separate "modelled" from "reachable".** A tick in the
  feature tables used to mean implemented and pinned against the VHDL; it now
  also means a guest can exercise it, and NOT WIRED means the first two without
  the third. `pkg/next/reachability_test.go` enforces the package-level half:
  every subsystem under `pkg/next` must be imported by production code or listed
  with the reason it is not, and the test fails in both directions so the list
  cannot rot. This exists because the project had already published the wrong
  answer once, describing the zxnDMA bus arbitration as modelled in three
  documents while nothing drove the pin it needs.

- **SAM Coupé `.sbt` files now load.** An SBT is not a disk image but the raw
  content of a single SAM CODE file, so zx_go builds a bootable 800K MGT disk
  around it: the file at cylinder 4 head 0 sector 1 behind a 9-byte CODE header,
  a 501-then-510 byte sector chain with next-track/next-sector links, and a
  SAMDOS-shaped directory entry with its allocation map, so a DOS booted from
  the disk lists the file rather than overwriting it. No DOS is bundled or
  needed. A file that does not carry the boot signature the ROM checks is
  refused by name rather than left to fail as the machine's cryptic
  `53 No DOS`. `File → Load SAM Disk 1/2` now offers `.sbt`; `BOOT` reads
  drive 1 only, and the load dialog now says so instead of telling drive 2
  users to type `BOOT`.

- **`watch-nextreg` halts on a write to a named NextReg.** Neither existing
  half could do it. `nr-trace` names registers and logs their writes without
  stopping; `watch-port` stops, but a NextReg write is a select on `$243B`
  followed by a value on `$253B`, so a watch on either port sees half a
  transaction and a `=VAL` on `$253B` matches the value whatever register it
  lands in. Keying the watch on the register instead also catches the Z80N
  `NEXTREG nn,nn` opcodes and the single-write `$57` path, which never touch
  the ports at all. Takes an optional `=VAL` filter and a `log` mode that
  reports without halting.

  Copper MOVEs trip it too, deliberately: a MOVE reaches the same
  `Dispatcher.WriteReg` the CPU reaches through the ports, and "which copper
  list is stamping on this register" is one of the questions the command
  exists to answer. So the hit line names the writer. `Copper.Executing` is
  raised across the MOVE's register write only, and `Copper.PC` reports the
  MOVE itself rather than the instruction after it, so a MOVE is never
  reported against whatever address the Z80 happened to be at.

### Changed

- **The Copper is paced per raster column, and three fictions are gone.** Its
  `hcount_i` is `hc_ula`, whose origin sits twelve columns before displayed
  pixel 0, but the ULA passed the raw display x, so every `WAIT` released
  twelve pixels late, rounded to sixteen by the old eight-pixel grid; the whole
  line's clock budget was also handed to the step at pixel 0, letting a burst
  of `MOVE`s retire before the first pixel existed. The row is now walked
  column by column over the real line length, each column paid exactly four
  Copper clocks, with a `MOVE` straddling a boundary paid out of the next
  column and that debt carried across line and frame boundaries. Compose ranges
  split at the pixel a `MOVE` wrote in. The Copper is clocked on every line of
  the frame, not only the 192 displayed ones. The three fictions, each
  disproved under GHDL against the real `copper.vhd` before being changed:
  `WAIT` column 63 does not park for ever, because `(x<<3)+12` is a 9-bit add
  and 516 wraps to 4; there is no HALT opcode, `$FFFF` being `WAIT x=63,y=511`,
  so a list terminated the standard way ran once here and every frame on
  hardware; and a `WAIT` whose target line is behind the raster parks rather
  than releasing. Not done: NR$03 machine timing is not consulted, so a program
  selecting 48K or Pentagon timing still gets 456 columns and 311 lines.

- **zxnDMA bus arbitration is now modelled, and half of it is live.** A block
  holds the bus for its bytes at each port's programmed cycle length and charges
  that to the CPU; burst mode gives the bus back only where the FPGA does, in
  `WAITING_CYCLES`, which is reachable only with a prescaler; and `$83` Disable
  DMA abandons a block in flight, so end-of-block never latches and auto-restart
  never reloads. The `dma_delay_i` pin, which parks a block in `START_DMA` and
  resumes it with the pointers where they stand, is modelled and tested but
  never driven: its source is the IM2 daisy chain, which is itself an unwired
  reference model here, so no guest can reach it yet. That is marked in the
  package documentation rather than left to be inferred. The Z80 DMA's interrupt and match logic is
  deliberately still absent, because the FPGA does not implement it either:
  no interrupt output, no daisy-chain pins, the mask/match and
  interrupt-control registers commented out, and the five WR6 interrupt
  commands decoding into empty branches.

- **A command streamed into the DMA's own port can no longer corrupt the block
  carrying it.** An IO endpoint may be aimed at `$6B`, so a transferred byte
  can be a command, and `$83` or `$C3` arriving that way took the transfer FSM
  to IDLE while the block loop carried on to its epilogue. The block would then
  latch end-of-block, reload under auto-restart and charge the CPU for bytes it
  never moved, and a `$C3` would leave a freshly reset controller reporting a
  finished block. Both the synchronous and the interleaved-burst paths now stop
  where the hardware stops. The byte counter and `status_atleastone` were also
  moved to the sides of the write cycle the FPGA sets them on.

### Fixed

- **The zxnDMA's fixed-time prescaler ran 4x to 32x too fast.** The period is
  not a T-state count. The FPGA waits until `DMA_timer_s(13 downto 5)` reaches
  the prescaler (`dma.vhd:424`, `:451`), and `DMA_timer_s` advances by 8, 4, 2
  or 1 per CPU clock at 3.5, 7, 14 and 28 MHz (`dma.vhd:250-254`), so the real
  period is `4 x prescaler x the speed multiplier`: a constant wall time, which
  is what scaling the increment is for. Taking the byte itself as T-states made
  every fixed-time transfer 4x too fast at 3.5 MHz and 32x at 28 MHz, so
  DMA-streamed audio played at the wrong pitch on every machine.

- **The Copper mishandled a list that reconfigured the Copper.** A `MOVE` can
  write NextReg $61/$62, so a list can change its own start mode from inside its
  own execution, and the engine applied that change in the middle of the
  instruction that made it. A restart left the program counter at 1 rather than
  0, silently skipping instruction 0 on every self-restart, and a `MOVE` that
  stopped the Copper did not stop it, because the stop condition was tested once
  before the clock budget rather than on every clock. Both now follow the
  device's per-clock if/elsif chain: the clock on which the mode changes
  performs the restart and executes nothing, and the stopped state is checked
  every clock. Enabling the Copper therefore costs one clock, as it does in
  hardware. `last_state_s` is now part of the captured state, so a rewind cannot
  reintroduce a restart the recorded machine had already performed. The Copper
  state version is 2, and version 1 captures are migrated rather than refused:
  `last_state_s` differed from the stored mode only inside one copper clock, so
  assuming they agreed is wrong only for a capture taken in that window, whereas
  refusing it would have discarded the whole snapshot and every rewind buffer,
  because one device's load error rolls back the entire machine restore.

- **Copper writes below the last display row were journalled.** The raster
  journal exists to replay the guest's writes, and suppresses recording across
  the display loop so the Copper's own writes stay out of it. The Copper also
  runs below the display, outside that window, so a `MOVE` on a border or
  blanking line was recorded and then undone by the next frame's replay for the
  whole of that frame.

- **Three NextReg debugger commands crashed the emulator on a non-Next
  machine.** `watch-nextreg`, `nr-trace` and `trace-nextreg-deltas` all arm a
  tracer on the NextReg dispatcher, which exists only on the Next, and none of
  them checked for it first, so issuing one on a 48K session nil-panicked
  instead of returning an error. Their siblings `nextreg-read`, `nr-panel` and
  `layer-state` already had the guard. `trace-nextreg-deltas` needed it on both
  of its arming paths. Clearing and listing never touched the dispatcher and
  still work on any machine.

- **A Copper write could wipe the pixels to its left on the same row.** A write
  splits the row so each part keeps the layer state it was generated under,
  which means re-composing only the tail. When the write left no active overlay
  layer, the compositor's shortcut copied the whole row and ignored the bounds
  it was given, erasing the Layer 2, sprite and tilemap pixels in the head.

- **A wedged zxnDMA survived a reboot.** A WR4 byte with D4 set and D2/D3 clear
  parks the register-write sequencer in the FPGA's unimplemented `R4_BYTE_2`
  state, where it swallows every later command byte including `$C3` RESET. Only
  the reset pin clears it, and the reboot path never drove that pin, so a guest
  could leave the DMA dead for the rest of the process.

- **The zxnDMA follow-byte codes were renumbered, and they are a savestate wire
  format.** Retiring the WR4 interrupt-control code from the middle of the block
  moved the read-mask code from 11 to 10, so an older capture with a read-mask
  byte pending failed to load and one with an interrupt-control byte pending
  loaded as a read-mask follow, applying the guest's next `$6B` byte to the
  wrong place. The retired code keeps its slot, and the numbers are now pinned
  by a test.

- **`watch-nextreg` could half-arm a watch, and lost its hook on a model
  switch.** A register list that failed to parse partway through left the
  registers before the bad one armed while reporting an error and never
  installing the tracer, so they fired later with no visible cause; the list is
  now parsed in full before any of it is armed. A bare `watch-nextreg log` was
  parsed as a register named "log"; it now reports the missing register. And the
  installed-hook memo was a bare flag, so after a model switch replaced the
  NextReg dispatcher the hook sat on the old one and armed watches silently
  never fired again while still listing as active. The memo now records which
  dispatcher it hooked, and re-installs when that changes. `nr-trace` and
  `trace-nextreg-deltas` had the same flag and are fixed with it.

- **The Copper is no longer clocked when it cannot do anything.** It was stepped
  once per column of every line of the frame whenever a Next compositor was
  wired, about 142,000 calls a frame, even for the overwhelming majority of
  programs that never enable it. Measured at 334 microseconds a frame, or two
  percent of a 50 Hz budget, spent entirely on call overhead. A stopped Copper
  with no pending mode change is now skipped a row at a time.

- **The Copper disassembler did not stop at the lowest unreachable `WAIT`
  line.** Its bound held the frame's line count but was compared as a maximum
  line number, so a `WAIT` for line 312, which parks exactly as surely as the
  idiomatic `$FFFF`, was printed as an ordinary instruction and the trailing
  NOOPs it exists to suppress were dumped after it.

- **`TestHardwareResetClearsTheWedge` was not testing the wedge.** Its fixture
  transferred `$A0 $A1 $A2` to `$6000` before the reset and then asserted the
  same three bytes at the same address afterwards, so a still-wedged controller
  that swallowed the entire post-reset command stream passed it. The
  post-reset transfer now targets an address the fixture has not written.

- **The bus acquisition is no longer charged to the CPU.** Its three cycles are
  correct per the FPGA, but a whole block reaches the CPU as one jump of the
  T-state counter, which is also what schedules the frame interrupt, so the
  size of that jump decides which side of the interrupt window a block lands
  on. Charging the acquisition was on its own enough to stop a working title
  booting. The constant stays derived and documented, and a test pins that it
  is deliberately excluded. See ROADMAP item 1 for this, and item 4 for the
  other time-model defects the arbitration work uncovered.

- **Four VHDL citations in `pkg/next/dma` pointed at the wrong lines.** The WR4
  base-byte ladder is `dma.vhd:603-611` and its R4_BYTE_2 arm `:607-608`, not
  `:800-811` and `:807-808`; the register read path is `:895`, not `:864`.

- **The reason SBT was unsupported was wrong twice over, and SBT now works.**
  v1.11.0 said the format was not published anywhere the project could use. That
  was corrected, in this file, to "`BOOT` loads directory slot 1 as the DOS, so
  we need a DOS image we do not have and cannot establish the redistribution
  status of". That was wrong too. The ROM never reads the directory and never
  needs a DOS: `$591E` targets track 4 sector 1, `$5939` reads it to `$8000`,
  `$5967` compares the four bytes at `$8100` against the literal at `$FB94`
  under `AND $5F`, `$5976` raises error 53, and `$597B` jumps to `$8009`. The
  whole 32K ROM holds exactly one `CF 35`, so `53 No DOS` has that single
  origin. See the Added entry above.

## [v1.11.0]

**Sound is in stereo, the SAM Coupé can be rewound, and three more machines
have save states.**

A minor bump rather than a patch one, which breaks the run of v1.10.x: this
adds capability on four fronts rather than completing something already
started.

### Added

- **Stereo audio, end to end.** Three subsystems computed a genuine two-channel
  pair and had it discarded one line before the sink, because the ring buffer,
  the mixer and the WAV recorder were all mono and only widened by duplicating
  each sample. The FPGA keeps the two apart the whole way through a dedicated
  mixer entity (`audio/audio_mixer.vhd`), whose ports are `pcm_L_o` and
  `pcm_R_o`. This does the same.

  The beeper stays mono on purpose, and that is not a shortcut: it is one bit
  driving one speaker, as are the tape's EAR line, SpecDrum and Covox. Those
  are summed in mono and widened once.

- **AY panning, and the two registers that drive it.** The law is
  `turbosound.vhd:186-192`, resolved: `mono = A+B+C`, `ABC = (A+B, B+C)`,
  `ACB = (A+C, B+C)`. Only the middle letter moves between the two stereo
  cases, which is the whole content of the naming — it is the channel heard
  from both speakers.

  `NR$08` bit 5 and `NR$09` bits 7:5 now reach the chips. Both were decoded,
  stored and read back correctly while driving nothing, so a guest selecting
  ACB heard ABC. Mono remains the default and the only correct setting for
  every classic machine, whose AY drives one internal speaker.

- **The Next's DAC bank is two stereo pairs.** `soundrive.vhd:110-111` sums
  chA+chB into `pcm_L_o` and chC+chD into `pcm_R_o`, and labels them "left" and
  "right" in its port list. Meaning all four together turned a hard-panned pair
  into silence.

- **The SAM Coupé's 1-bit beeper.** The machine keeps the Spectrum's speaker on
  `$FE` bit 4 for compatibility, and it was not modelled at all — so 48K
  software made no sound, and neither did the SAM ROM's key click. Only a
  genuine edge is recorded, since that port also carries the border colour, MIC
  and SOFF.

- **The SAM Coupé is captured.** It was the one machine in the line with no
  state capture: the registry returned an *empty* set for it, so rewind and
  time travel silently did nothing. Six devices now: the CPU, the memory map,
  the keyboard, the SAA1099 and both WD1772s, plus the ASIC latches.

  Disks and the ROM are deliberately excluded, on the rule the tape player
  already follows. A rewind cannot un-write a floppy and must not un-insert one.

- **Save states for the Spectrum Next, the SAM and the ZX80/ZX81.** All three
  were refused outright, which was right while SZX was the only option: `.sna`,
  `.z80` and `.szx` all describe a 48K/128K memory map and a Z80, and there is
  nowhere in any of them for the Next's 2 MB and its NextRegs, the SAM's paging
  and SAA1099, or the ZX81's CPU-generated display.

  They now save the machinestate capture — the same one the rewind ring takes —
  in a container tagged with the machine, so a wrong-machine load says which
  machine the file came from rather than listing device names. The classic
  Spectrums keep SZX, because it is portable and other emulators read it.

- **Extended DSK images on the SAM**, through the parser the +3 already uses.
  An image whose tracks disagree on geometry is refused with the offending
  track named rather than flattened; the SAM's sector store has one geometry,
  so accepting it would place every sector after the odd track at the wrong
  offset — the image would load, most of it would read correctly, and it would
  fail as a corrupt file mid-game. An *unformatted* track is a different case
  and is accepted: real dumps carry them.

  SBT is deliberately **not** supported. They are not disk images, and where in
  a blank disk the file goes is not published anywhere this project can use.

### Fixed

- **The Next's DAC reset to the negative rail.** `soundrive.vhd:71-74` loads
  `$80` into all four channels, because mid-scale is what silence means for a
  centred DAC. Resetting to 0 was a full-scale DC offset that the AC-coupling
  downstream then removed — inaudible *and* wrong, which is the combination
  that keeps a bug alive.

- **The SAM's beeper had no output clamp**, so an isolated toggle overshot to
  twice the level the speaker is driven to. A high-pass's step response is the
  step height, which is why `pkg/ula` bounds its own.

- **A reset left the SAM's speaker asserted.** It cleared the BORDER latch and
  nothing behind it, so the guest's next BEEP-high write was not an edge and
  the first click after a reset was swallowed.

- **An edge in a frame's overshoot window was discarded.** `ExecuteFrame` runs
  past its budget by the length of whichever instruction crossed the boundary,
  so a write in that window lands past the frame length and no sample can reach
  it. Truncating the list afterwards threw it away, leaving the speaker at the
  pre-write level for the whole of the next frame.

- **The SAM's beeper event list grew without bound** with no audio consumer —
  `--no-sound` and every headless run — and the capture path encoded the whole
  thing on every rewind frame.

### Worth knowing

Three findings that cost more to reach than they look.

**A clamp that models one source must not be applied to the bus.** The SAM's DC
blocker was filtering the summed output, so the beeper's cone-excursion limit
became a level cap on the SAA1099, which reaches full scale on its own: a loud
frame was clipped to a quarter of its range with nearly half its samples pinned
on the limit. The beeper is filtered alone now, before it is summed.

**An interface method cannot silently stop matching; a type assertion can.**
The ULA reached the Next's DAC through a runtime assertion, so renaming the
method it looked for disconnected the DAC from the audio path with no build
error and no failing test. `NextDAC` declares those methods now.

**gob does not police a schema.** Renaming a field in a device's wire struct
decodes cleanly and leaves the new one zero-filled, which for a DAC resting at
`$80` means the negative rail rather than silence. The container's version has
to be bumped when any device's wire struct changes shape, not only when the
framing does.

## [v1.10.2]

**LoRes, Radastan and ULA+ work.** A guest can select the mode, set its
colours, scroll it, clip it, and see it. Before this the registers were decoded,
stored, captured, and read by no render path, so selecting the mode silently
produced the ordinary ULA picture.

The layer itself was never the missing part. `pkg/next/lores` has been a
faithful port of `video/lores.vhd` since it was written, golden-tested against
captured FPGA vectors. What was missing was every wire into and out of it.

### Added

- **LoRes / Radastan reaches the screen.** `NR$15` bit 7 enables it, `NR$6A`
  carries the mode and palette offset, `NR$32`/`NR$33` its own scroll offsets,
  and the ULA clip window its clip ports.

  It is **not** a layer in the mixer, and building it as one would have been
  wrong. It substitutes for the ULA's own pixels before anything else runs
  (`zxnext.vhd:6980`), and the result goes through the ULA palette. So Layer 2,
  the tilemap, sprites, transparency and priority needed no changes at all —
  they see a substituted ULA row and cannot tell.

  The path is inert while `NR$15` bit 7 is clear: one nil check per frame, and
  the picture is byte-identical to before the layer existed. The end-to-end
  test pins that by disabling the layer again and comparing frames.

- **ULA+, both halves.** The enable is one bit written from `NR$68` bit 3 and
  from port `$FF3B` in `$BF3B` mode group 01, into a single location, read back
  by both, and ANDed with NOT ULAnext before it reaches the layer.

  The palette is the part worth knowing about: **ULA+ does not have a palette of
  its own.** A `$FF3B` colour write is routed into the Next's own NextReg
  palette stream, and the 64 entries land at `$C0..$FF` of the ULA palette
  (`zxnext.vhd:6958`). That is precisely why `lores.vhd`'s Radastan ULA+ nibble
  is `"11" & offset(1:0)` — it addresses that range. Implementing ULA+ as its
  own 64-entry table, which is the obvious reading of the ULA+ standard, would
  have left Radastan-with-ULA+ reading colours nothing ever wrote.

### Fixed

- **The display file was half-wired.** `lores.vhd` takes port `$FF` screen-mode
  bit 0 XOR `NR$6A` bit 4 (`zxnext.vhd:6796`), and only the NextReg half was
  connected, so Radastan would have read from the wrong 6 KB block for every
  program using a Timex display file.

- **The ULA+ enable had two storage locations that could disagree.** `Raw` and
  `SaveState` reported the last value written to `NR$68` while `ReadReg`
  reported the enable through an overlay, so a snapshot could export a machine
  that never existed. Worse, a rewind restored the layer input without its
  driver, and the next unrelated register write recomputed it from the
  un-rewound value. The enable now lives in `NR$68` bit 3 and nowhere else,
  which is what the FPGA does and why its own `nr_68_ulap_en` is commented out.

- **An unmodelled `$FF3B` write was swallowed** rather than declined, so the
  byte vanished with no trace and no later handler could observe it, while the
  read side for the same group fell through. Both decline now.

- **A reset left the sprite and tilemap engines clipping to a stale window.**
  The coordinates were restored and never pushed down. Found while adding the
  ULA sink, and pinned by a test that asserts what reached the layers rather
  than what the register reads back.

### Changed

- The mutation audit's own measurement was corrected. It matched only
  `x.field = s.Field`, so `copy()`, `append()`-based slice restores and nested
  targets were never mutated — and an unmutated line reads as absent rather
  than as a gap. Re-run over all 25 capture files: **376 restores, 375 killed,
  0 survived, 1 invalid**, against a previously documented 278.

## [v1.10.1]

Completes the rewind device set, and fixes a disk-corruption bug found while
doing it. The two remaining uncaptured disk interfaces, the +D / DISCiPLE and
the Opus Discovery, are now captured and registered, so **every disk interface
the emulator offers is restored by a rewind**.

The bug is the part worth reading. Nothing about it was found by looking at the
controller: it surfaced because a mutation of the new capture code survived, and
the reason nothing in the fixture could move that field turned out to be the
defect itself.

### Added

- **The +D / DISCiPLE and the Opus Discovery are captured**
  (`pkg/disciple/state.go`, `pkg/opus/state.go`). Rewinding while either had an
  operation in flight used to put the machine back and leave the controller
  where it was, so the operation resumed against a controller mid-command.

  Most of what they carry is state no port reports: the transfer buffer and its
  position, the sector a write was addressed to when the command started, the
  physical head position of each drive as distinct from the track register, the
  direction the last step went, and the whole WRITE TRACK parser mid-format.

  The Opus additionally carries its DRQ byte-clock. It wires DRQ to the Z80's
  NMI and its ROM moves one byte per interrupt, so a capture that lost the
  pending byte-period would restore a transfer that never advances: BUSY stays
  asserted and the ROM's wait loop spins forever.

  Disks are deliberately not captured. They are the medium: a rewind cannot
  un-write a floppy, and must not un-eject or re-eject one either.

- **A structural field-coverage guard** in both new packages, and one direction
  of the divMMC ROM restore that had never been exercised.

### Fixed

- **The +D destroyed a track when a Write Track was interrupted.** The format
  flag was set by Write Track and cleared by nothing, so a format abandoned by a
  Force Interrupt left every later Write Sector to be committed through
  `commitWriteTrack`: its 512 sector bytes were parsed as a raw track image and
  the whole track rebuilt from them.

  Clearing the flag alone was not enough, and measuring both ways on the same
  probe is what showed it. With the flag sticky, 5509 bytes of the track were
  overwritten and every sector ID destroyed; with only the flag cleared, 5463
  were overwritten through the sector path instead. The real defect was that
  `executeCommand` never aborted the in-flight transfer at all, so the
  abandoned 6250-byte buffer stayed live with DRQ asserted. Every new command
  now drops the buffer, position, length, direction, format flag, BUSY and DRQ,
  which is what `pkg/opus`'s controller has always done.

- **Captured states that would panic or wedge the controller are refused.** The
  Opus accepted a transfer position exactly at the end of its buffer, which the
  live controller can never hold and which neither `readData` nor `writeData`
  bounds-checks, and a format parser claiming a sector ID it did not carry. The
  +D accepted a position past the transfer length, where nothing advances the
  transfer so BUSY never drops and GDOS's wait loop spins with no way out.

- **`SaveState` no longer panics on an encode failure** in either new device.
  Capture runs from the CPU pre-fetch hook, which deliberately skips a failed
  capture rather than stopping the machine; panicking there would have taken
  the emulator down mid-frame. Both now return nil, matching the contract
  `pkg/next/divmmc` documents, and `LoadState` rejects an empty blob.

### Changed

- Both new devices reference their live buffers when capturing rather than
  pre-copying them, since gob serialises before `SaveState` returns. The copy
  was happening three times per rewind frame, 8 KB of it from the +D alone.

- Two registry tests and `TestStateRegistryOptionalPeripherals` now fail rather
  than skip when a ROM will not load. All four ROMs involved are embedded, so a
  skip could only fire when something was already broken.

- **The mutation audit was measuring a third less than it claimed.**
  The audit's pattern matched only `x.field = s.Field`, which cannot
  see a `copy()` call, an `append()`-based slice restore, or a nested target
  like `f.target.track`. Those lines were not counted and were never mutated,
  and an unmutated line reads as absent rather than as a gap — so the totals
  looked complete while roughly a quarter of the restores in the tree had never
  been tested once.

  Corrected, and re-run over all 25 capture files: **376 restores, 375 killed,
  0 survived, 1 invalid.** The invalid is divMMC's `copy(p.ram[i], s.RAM[i])`,
  which cannot be neutered inside a `for … range` body without the package
  ceasing to compile; it was verified by hand with a different mutation, and
  four tests fail on it.

  The coverage turned out to be real almost everywhere — the previously
  unmutated lines all kill — but one genuine gap was hiding in it, the divMMC
  ROM restore fixed above. Two further traps are recorded in the tool: scanning
  only `LoadState` finds nothing in `pkg/ay` or `pkg/plus3fdc`, whose restores
  live in `apply()` and `fdc.loadState()`, and deleting a line is not always a
  valid mutation, so `copy()` is neutered by truncating its source instead.

### Documentation

- **LoRes / Radastan is implemented but never reaches the screen**, and one
  comment claimed the opposite. `pkg/next/lores` is a golden-tested port of
  `video/lores.vhd` that no non-test code imports; `NR$6A` is decoded, stored,
  and read by no render path. A guest selecting the mode silently gets the
  ordinary ULA picture. Recorded as roadmap item 6 and deliberately parked, on
  the same evidence test as the other parked items: none of the 24 installed
  `.nex` files sets the LoRes loading-screen flag.

## [v1.10.0]

A minor release: the machine can now be rewound and re-executed, and the
documentation was audited against the code rather than trusted.

Two things are worth reading before the list. Every capture in the tree is
mutation-verified — each field restore was deleted in turn and the tests had to
fail for a real assertion — because an audit found 39 of 144 restores could be
removed with everything still green. And 180 documented claims were checked
against the code, of which about 60 were false; the corrections are throughout
this entry rather than gathered in one place.

### Added

- **Reverse debugging, on both surfaces.** Step and run backwards through
  executed instructions. The M1 ring already recorded every instruction; what
  was missing was the ability to walk it. `HistoryEntry` now also carries the
  shadow registers, which `EXX` and `EX AF,AF'` make part of the visible
  state, and an eight-word window on the stack, without which a backwards
  step through a `CALL` cannot show where control came from — both filled by
  one shared producer, so an entry means the same thing whichever debugger
  opened the ring. `ReverseCursor` moves through the ring and searches
  backwards for a condition.

  It reaches the user through the telnet commands `step-back`,
  `step-forward`, `run-back`, `to-present` and `reverse-status`, and through
  the visual debugger's **Reverse** tab; both drive one cursor, and while it
  is away from the present every panel and every state-reading command
  answers from the instant under it, tagged as history rather than as the
  live machine.

  Anything the recording cannot answer is **refused, not guessed**. Memory is
  not recorded per instruction, so `run-back "read $5C78 == 0"` returns an
  error rather than testing the condition against a zero; the same applies to
  a register a narrow ring never recorded. A breakpoint that quietly stops
  being the breakpoint you wrote costs far more than an error message.

  Answerability is checked at every step of a backwards search rather than
  probed once. Not because evaluation short-circuits — it does not:
  `binOp.eval` computes both operands eagerly, so `a == 0 && read $4000 == 1`
  reaches the memory read on the first instant whatever `A` holds. The check
  is per step because it costs nothing there, and an earlier revision of this
  entry justified it with the opposite of what the code does.
- **`replay-back [N]`**, which moves the machine — not a cursor — back to an
  instruction the checkpoint ring never captured, by restoring the newest
  checkpoint at or before it and re-executing forward. The instant it hands
  back was produced by running, so memory, the sound chip's counters and the
  disk controller are what they were. Before answering it re-runs the window
  to the present and compares the whole machine device by device, and refuses
  when the two differ: a window replay cannot reproduce contains no instant
  replay can be trusted to produce.
- **`pkg/machinestate`**, the device registry a complete state capture is
  built on. It refuses a state whose device set does not match the machine in
  either direction, and validates before applying, so a failed restore leaves
  the machine untouched rather than half-rewound. Captures are canonically
  ordered, which is what lets a test assert that replaying from a point
  reproduced the same machine.
- **Complete AY state capture.** The 16 registers are what the guest writes
  and what every snapshot format stores, but the next sample comes from the
  tone dividers, the 17-bit noise LFSR and the envelope's position and
  direction, none of them guest-readable. Restoring the registers alone gives
  a chip that runs and does not resume. Tested by replaying audio from a
  capture rather than by comparing fields.
- **A `machinestate.Device` for every device a rewind has to return**: the
  CPU, memory, the ULA, the keyboard, the DAC, the AY and the Next's
  TurboSound engine, the +3 FDC, the Beta interface, Interface 1 (microdrive
  head position included), the Multiface, the tape player's playback
  position, and the Next's NextRegs, palette, tilemap, copper, sprites, Layer
  2, DAC, DMA and divMMC. `(*emulator).stateRegistry` registers each one
  exactly when the machine carries it, so a capture is refused rather than
  applied to a machine that has since gained a controller. Still uncaptured,
  and recorded in `KNOWN_ISSUES.md`: the +D and the Opus Discovery.
- **A replay-equivalence oracle** (`cmd/zx_go/replay_oracle_test.go`). It
  fingerprints outputs and memory in bulk rather than device state, so it
  cannot pass by comparing a capture with itself, and it passes on the 48K,
  128K, +2, +2A, +3 and the Next. A companion test leaves one device
  un-rewound to prove the oracle still bites.
- **`pkg/screendiff`**, the verdict logic for differential screen comparison:
  which of two machines' screens agree, and when a comparison must be refused
  instead. It was previously inside a gitignored developer tool, where its
  tests could not run in CI. The taxonomy is mostly refusal, and a guard that
  can never fire is indistinguishable from one that never needed to, so it now
  lives where `go test ./...` reaches it.

### Fixed

- **The history ring advertised fields no producer ever filled.** The shadow
  registers and the stack window were added to `HistoryEntry`, promised in
  three documents and rendered by the reverse register view, and written by
  nothing — so the view showed a confident zero as the machine's real state.
  Both surfaces now fill them through one shared builder, which also closed a
  divergence between them: the GUI producer omitted the ROM bank, so a trace
  recorded with the GUI open reported bank 0 for its whole length. The stack
  window stays wide-ring only, because eight extra memory reads on a path that
  runs a million times a second is a cost a narrow ring did not ask for. And
  `bank` is no longer refused going backwards: it is recorded in every entry,
  and a refusal only means something while it is reserved for what genuinely
  was not.
- **The device registry would have panicked on first real use.** `AY.StateID`
  returned the constant `"ay"`, but a Next carries four AY chips — three in
  the TurboSound engine and the ULA's own — so the first `Register` call would
  have hit "duplicate device identifier" and killed the emulator at startup.
  The identifier is per instance now, and the engine is a device in its own
  right, which also captures the two pieces of state no chip holds: which chip
  `$FFFD` has selected, and the NextReg $06 reset-hold.
- **A failed restore left the machine half-rewound.** `Registry.Restore`
  validates the device set before applying anything, but cannot know whether
  each blob will decode, so a device rejecting its state part way through left
  every earlier device already restored — the outcome the package doc calls
  the worst available. It captures a rollback first and unwinds on failure,
  and reports separately when the rollback itself fails.
- **`plus3fdc`'s `formatFail` was captured and could never be observed.** It
  is cleared on entry to the write-ID path, set while the track builder runs,
  read once, and cleared again before the same function returns — so it never
  outlives the port write that set it, and captures are taken at instruction
  boundaries. Removed, with `TestFormatFailNeverOutlivesTheCommandThatSetsIt`
  as the defence: removing a captured field is a claim needing the same rigour
  as adding one. It was the one survivor of an audit of 278 field restores,
  which reported 277/277 killed afterwards, against a green baseline and with
  no invalid results.

  A review caught that "all" overstated it: the sweep globbed `pkg/*/state.go`
  and `pkg/next/*/state.go`, which misses `pkg/ula/tapestate.go` and
  `pkg/next/state.go` — the tape and clip-window devices added in the same
  release. Both were audited separately and are 8/8 and 10/10, so the coverage
  is real; the claim was narrower than it read. The glob now matches them.

  **A second and larger gap surfaced in v1.10.1: 278 was never the number of
  field restores.** The audit matched only `x.field = s.Field`, so `copy()`
  calls, `append()`-based slice restores and nested targets like
  `f.target.track` were not counted and were never mutated — and an unmutated
  line reads as absent rather than as a gap, which is why the totals looked
  complete. The real figure is **376**. See v1.10.1; the coverage was genuine,
  but this measurement of it was not.
- **Four ways the `PHASE` verdict could report agreement it had not earned.**
  `PHASE` rescues a pair the pointwise comparison rejected, by asking whether
  either machine's screen appears anywhere in the other's sampled sequence, so
  a false rescue does not produce a wrong number but a wrong conclusion. All
  four had one cause: the cross-match searched raw screens with none of the
  guards protecting the direct comparison.
  - A flat screen anywhere in either 13-screen sequence matched perfectly and
    meant nothing, which a title that clears the display between attract pages
    produces every time.
  - So did a capture that failed, because a zero-length overlap scores 0%.
  - The rescue outranked `INERT`, reporting agreement about a probe key that
    neither machine answered.
  - It reached into the `match (minor)` and `PARTIAL` bands, relabelling a
    pair 0.6% apart as `PHASE` at 0.0% and discarding the real figure.
- **The reference's tape-block and reset flags were read 36 s later than
  ours**, after the cycle sampling rather than at the anchor, so a multiload
  title re-entering its loader inside that window inflated one side's count
  alone and voided the comparison as `SPLIT`.
- **`TestNexloadSDGames` now fails a title that launches and renders nothing**
  rather than only logging it. "All 12 launch and render" was true, but a
  regression to a blank screen across the whole corpus would have exited zero.

## [v1.9.1]

A patch release. One user-visible fix, in the Windows console; the rest is a
correction pass over what the project claims about itself — the compatibility
manifest, and the roadmap entries that had drifted out of step with the code.

### Fixed

- **ANSI escape sequences printed literally in the Windows console.** The
  startup banner and every log line arrived as `<-[38;5;196m` and similar.
  `term.IsTerminal` accepts a Windows console handle, because GetConsoleMode
  succeeds on one, but nothing ever set
  `ENABLE_VIRTUAL_TERMINAL_PROCESSING`, so the console printed the escapes
  rather than interpreting them. Colour now requires the console to accept
  ANSI as well as be a terminal, and falls back to plain text when it will
  not, so the banner stays readable either way (`pkg/zxlog/color.go`). Found
  by a Windows 11 ARM64 test run, but not ARM-specific: it affected any
  legacy Windows console on any architecture. Redirected output was never
  affected, which is why no log capture ever showed it.

- **READ A TRACK did not report ND for an ID that is not on the track.** The
  uPD765A datasheet is explicit: "the FDC compares the ID information read from
  each sector with the value stored in the IDR and sets the ND flag of Status
  Register 1 to a 1 if there is no comparison." We returned ST1=$80 —
  end-of-cylinder alone — when the sector named was nowhere on the track. This
  is the fifth bug in `pkg/plus3fdc` found by tracing the guest rather than
  theorising about the hardware, and like the other four it is what a
  protection track is read for.

  It was found from California Games, whose loader seeks to a protection track
  numbered $B1-$B8 and asks for R=01. **It does not fix that title** — the run
  is unchanged at 632 lit pixels — so it ships on the datasheet's authority,
  not as a compatibility fix.
- **A flat bitmap is no longer scored as a title screen.** A display file whose
  every bitmap byte holds one value carries no shape at all, but uninitialised
  video memory renders as even vertical stripes and measures 18432 px in 2
  colours — clearing both content floors. Eight +3 disk rows were recorded as
  *Boots* on exactly that figure, five of them identical to the pixel, which is
  what gave it away: unrelated games do not draw the same screen. `Screening`
  now carries `FlatBitmap` and `Classify` treats it as blank. This is the test
  the differential driver has always applied, and it reported all eight as blank.

### Changed (compatibility manifest)

A differential sweep of all 62 +3 disk rows against a reference emulator.
**12 come back byte-identical, 10 more match as PHASE** (the same sequence
sampled at a different point), and nine rows were corrected:

- Eight flat-screen rows above drop from *Boots* to *Known issue*. Both
  machines are blank on those images, so this is very likely the dumps rather
  than an emulator fault — but nothing drew a title.
- California Games drops too: its *Boots (responds)* was measured on the +3 ROM
  menu, which this manifest's own notes say to read as "never left the menu".
- Barbarian II and Capitan Sevilla go the other way, up to **Works (caveat)**.
  Both were recorded as screen-verified-only because the differential run sent
  SPACE alone and reported INERT; their menus take number keys. With the full
  probe set both reach the game and match the reference to 0.0%.
- Three titles score DIVERGES with **us ahead of the reference**: Back to the
  Future Part III reaches gameplay while the reference drops to a BASIC report,
  Chase H.Q. acts on its own "PRESS ENTER FOR OPTIONS" prompt where the
  reference does not, and Comando Quatro reaches its control menu while the
  reference is still on the loading artwork.

**California Games was not broken: it loads and waits for a keypress.** It was
filed as a Known issue because it draws almost nothing and no reference would
corroborate it — ZEsarUX never leaves its own ROM menu on that image and MAME's
automated ENTER never registered. Disassembly settled what comparison could
not: the guest sits at `$7670` in `EI / HALT / RET`, called from a loop that
spins until `$76CC` returns carry, and `$76CC` scans nine bytes at `$5B0C` for a
pressed key. Tapping SPACE moves the PC immediately.

**Every Known issue row now carries third-emulator evidence**, on a third
emulator's reading rather than inference. MAME `specpls3`, driven from
our own vendored ROMs, **refuses to mount four of them** — "floppy tracks=45,
drive tracks=42", against a real +3 drive's 40 — and fails three more
identically to us, ending on the same vertical stripe pattern. Only California
Games remains undecided.

### Tooling (not part of a clone)

- The differential driver probes with a **set** of keys rather than one, matching the
  screening harness, because SPACE alone reported INERT for two titles whose
  menus take numbers. Our side leads and the reference replays the same count:
  the two runs cannot agree live, and comparing differently-driven machines
  proves nothing. A side effect is that driving input largely dissolves the
  attract-phase problem, by pulling both machines into a menu.
- New **PHASE** verdict for what remains. Each side samples 12 screens 3 s
  apart, and the question becomes whether one machine's screen appears anywhere
  in the other's sequence rather than whether two frames match. It only ever
  rescues a pair the pointwise verdict failed, and a voiding verdict still
  wins. Pinned by unit tests, because driving a real title is not a reliable
  way to reach the branch — the probe keys synchronise the machines.

## [v1.9.0]

A minor rather than a patch release: it adds exported API to `pkg/testharness`
(`Harness.TapeBlocksDecoded`, the `TapeIdleQuietT` constant, and the two new
`Screening` fields), and `pkg/ula` gains `TapePlayer.DataBlockCount`.

### Added

- **`Harness.TapeBlocksDecoded` — how much of a tape the guest actually
  read.** Neither existing count answers that. `TapeBlocksConsumed` counts
  fast-load trap fires and so is blind to a title's own loader: RoboCop traps
  6 of its 16 blocks and decodes the other ten off the EAR bit.
  `TapePlayer.CurrentBlock` has the opposite fault — the player is stepped
  from every port-$FE read, so the tape keeps rolling under a title that is
  merely scanning its keyboard at a menu, and the cursor runs on through
  blocks nothing is reading. The new count credits a block only when the
  guest read it, through either path, and `Screening` now carries it so a
  manifest note is written from the right figure rather than whichever came
  to hand.

### Fixed

- **Seven compatibility rows were wrong, and are corrected.** RoboCop, Target
  Renegade and The Way of the Tiger decode **every block on their tape** —
  Target Renegade was measured *in play*, with both scores, the energy bar
  and the 6:00 timer on screen. Lemmings, Gauntlet, Turrican, Double Dragon,
  Last Ninja 2 and Myth are multiloads sitting on their own title screen with
  the rest of the tape still to come. *Known issue* rows drop from 11 to 2,
  and the two that remain are +3 disk dumps whose behaviour a reference
  emulator reproduces exactly.
- **The v1.8.23 note below claimed R-Type "now loads all 24 of its blocks".
  It does not, and never should have.** That figure was the tape player's
  cursor, which advances whether or not anything is reading it. R-Type
  decodes 8 blocks — side 1, exactly the header/data pairs its block table
  lists before the layout changes — and settles on **"REWIND SIDE 2 THEN
  PRESS ANY KEY"**, which a reference emulator does too, at 3724 lit pixels
  each, 0.1% apart and both still. Movie's "all 6" was the same mistake; it
  decodes 2.

### Tooling (not part of a clone)

- The tape probe reports the decoded count, dumps a tape's block table
  (`-blocks`) and writes a screenshot per title (`-shots`). The shot is taken
  *after* the screening's input probe, which is stated in the flag help
  because it matters: R-Type's shot shows the ERROR IN LOADING that follows
  keys being pressed at its rewind prompt.
- The differential driver now covers the tape class: across its 18 rows the block
  counts agree with a reference emulator on 15, and every pair was read by eye
  as the same correct sequence. Three fixes were needed to get there, all of
  them things the tool was measuring wrongly:
  - It compared **our trap fires against the reference's loader entries**, two
    different quantities, and voided five titles as SPLIT for it. RoboCop and
    Target Renegade each read all 16 blocks on their tape while the trap fired
    6 times, and were reported as having loaded less than half of what the
    reference did. It now compares blocks decoded.
  - The motion guard sampled over **0.29 s**, which sees a scroller and is
    blind to a screen that holds a page for seconds and then swaps. Gauntlet
    III's credits scored PARTIAL at 21% for two machines showing consecutive
    pages of the same sequence, each perfectly still when sampled. Now 5 s,
    settable with `-motion`.
  - A short quiet window mistook a loader handover for the end of a load, so
    both sides sampled a screen still being drawn. `-settle` walks them on
    together.
  - `-keep` writes both screens as PNGs. Without the pictures a verdict is a
    percentage between two screens nobody has read, which is how a
    "side-change prompt" got recorded for a screen no one had looked at.

  The +3 disk rows were re-run because that path shares the motion window:
  every percentage is unchanged or better, no regression. It did surface that
  two of them claimed more than they had — Barbarian II and Capitan Sevilla
  report INERT, meaning SPACE moved *neither* machine, so their matching
  screens said nothing about input handling. Both menus take number keys.
  Those rows now say screen-verified rather than input-verified.

  What the fixes could not remove, and what the pictures show: **an attract
  cycle puts the two machines on different phases of the same correct
  sequence.** Renegade and Turrican came back byte-identical on one run and
  6.4% and 14.0% apart on the next with nothing changed but the quiet window,
  so a byte-identical verdict on a cycling title is a coincidence of sampling
  rather than a property worth quoting.

## [v1.8.23]

### Fixed

- **The harness could not see a tape load that bypasses the ROM loader**, so
  it screened such titles mid-load and recorded their loading screens as
  title screens. R-Type now loads all 24 of its blocks (was 8) and Movie all
  6, with **no change to the emulator** — a test pinning that a custom
  edge-decoding loader already works passed before any fix. The screening
  wait now also watches loader activity, and its deadline is derived from the
  tape with headroom for the fact that a real-time load costs more guest time
  than the medium's nominal length.
- **An unfinished tape load is now Inconclusive, not a verdict.** The wait's
  result was being discarded, so a load that hit its deadline was
  indistinguishable from one that completed.
- **The harness guest clock counted cycles executed, not time elapsed.** It
  scaled by the turbo multiplier, so on a Next at 28 MHz every window
  documented as "N seconds of guest time" was an eighth of that.
- **The harness enabled the $0556 tape trap on the Next unconditionally**,
  reintroducing the hazard that function exists to prevent: through the
  bootrom chain, NextZXOS or divMMC RAM, $0556 holds unrelated code, and
  firing there consumes a block, writes it wherever IX/DE point, and returns
  to a bogus PC. Now gated on the embedded 48K ROM being paged.
- **StartTapeLoad started a DISK load on the +2A/+3.** Its own doc said those
  models were left alone; instead they fell through to typing LOAD"", whose
  trailing ENTER selects the +3 menu's pre-highlighted disk Loader. It now
  refuses and says so, and callers honour the refusal.
- **DisplayFile returned ordinary RAM as a screen** on models that have no
  display file, and 6912 zeros for a short page — both read as "a blank
  screen" in a tool that compares it against another emulator's dump.
- Seven findings in the NextZXOS launch harness, listed in the previous
  commit: a doubled-character race, ENTER injected into a running game, a
  near-vacuous launch assertion, a PC logged 20 frames after its verdict, a
  prompt-open check that could pass before ENTER was processed, a GUI macro
  that dropped keystrokes silently, and an orphaned doc comment.
- The tape-loader read threshold was duplicated by hand in the GUI and the
  harness with a comment in each asserting they were identical and nothing
  enforcing it. It now has one definition in `pkg/ula`.

### Changed

- **Seven more manifest rows corrected from Boots to Known issue.** Counting
  tape blocks instead of looking at the screen shows Lemmings loads 4 of 125,
  Gauntlet 11 of 86, Turrican 9 of 30, Double Dragon 16 of 55, Last Ninja 2
  6 of 16, Myth 6 of 12, The Way of the Tiger 16 of 18. A partial load draws
  a screen, which is exactly why screening called them title screens.

## [v1.8.22]

### Fixed

- **The Next launch harness was host-clock coupled.** `bootNextToMenu` left
  the guest RTC on `time.Now()`, and NextZXOS runs a per-second redraw off it,
  so whether a keypress landed in the idle context depended on host wall-clock
  rate — machine speed, and what else was running. Measured: three identical
  runs of TX-1696 gave three different verdicts. The RTC is now pinned, which
  `cmd/zx_go/next.go` already documented as the reason that env var exists.
  All 12 SD games now render on every run, including the verbose run whose
  different timing used to flip the result.

### Changed

- **The launch assertion is strict again.** An intermediate version made
  "the game ran and then handed the machine back" a passing outcome, which
  would have hidden a load-then-die regression across the whole corpus. That
  widening was compensating for the nondeterminism above; with the clock
  pinned it is not needed, and returning to the OS now fails.

- **Robocop and Target Renegade are corrected from Boots to Known issue.**
  Differential comparison against a reference emulator shows both stop after
  6 of their 16 tape blocks and sit static. The screens previously recorded
  as their title screens (13996 px and 31712 px) are loading screens. The
  underlying gap — the handover from the ROM loader to a game's own loader
  using a non-standard flag — is now an open roadmap item.

## [v1.8.21]

### Fixed

- **The NEXLOAD test harness launched games from the card root**, so a title
  that opens its assets by a path relative to the current directory found
  none of them. A real user reaches a game through the Browser, which changes
  into its folder first; `nexloadFromMenu` now does the same.

  Eternal Battle is the case in point. Every relative open failed, and the
  game did not check for the error: it built its IM 2 table, ran off into its
  own data, hit an `FF` byte, `RST 38`'d into a filler ramp and halted. That
  looked like a hardware fault and was not one. With the working directory
  set it renders its full title screen.

  This is a harness fix, not an emulator change; the binary behaves as
  v1.8.20 did. It is released so the version and the compatibility record
  stay in step.

- **The harness retries the command line before calling a game broken.**
  Typing into a real OS prompt one synthetic keystroke at a time is not
  perfectly reliable, and a single dropped character makes the whole line
  wrong. That made the suite pass or fail on test ordering. A game that
  genuinely does not launch still fails every attempt.

### Changed

- All 12 `.nex` games on the SD card now launch and render, and every title
  directory holding a launchable program runs.

## [v1.8.20]

### Fixed

- **The NR$22/$23 line-interrupt target was offset by 64 lines**, so any
  target near the end of the frame could never fire. The target is an
  absolute raster line; we treated it as relative to the 256x192 active area
  and added `min_vactive`. `zxula_timing.vhd` is explicit:

  ```vhdl
  if i_int_line = 0 then
     int_line_num <= c_max_vc;
  else
     int_line_num <= unsigned(i_int_line) - 1;
  ...
  if (i_inten_line = '1') and (hc_ula = 255) and (cvc = int_line_num) then
  ```

  and `cvc` is a plain 0..c_max_vc raster counter with no active-area offset.
  A target of 0 selects the last line of the frame, which we mapped to line 0.

  Found from a Next game that disables the ULA frame interrupt (NR$22 bit 2)
  and drives itself entirely from a line interrupt at line 310 of 311. We
  waited on line 373, which does not exist, so the CPU sat halted in IM 2 on
  a black screen forever.

  Three tests asserted the old offset and are corrected, now citing the VHDL
  rather than a reading of the register docs.

## [v1.8.19]

### Fixed

Code review of v1.8.18 found the end-of-cylinder work was done in the read
path only, so reads and writes disagreed for identical commands. The
termination rule now lives in one place, `selfTerminated`:

- **WRITE DATA and READ DIAGNOSTIC reported a normal termination** where
  READ DATA reported an abnormal one. The +3 asserts no Terminal Count, so
  every transfer ends because the controller itself stopped, and the
  datasheet defines IC=01 as "execution started but was not successfully
  completed". A loader checking a write result the way Comando Quatro checks
  a read result would have read a good write as a failure.
- **`ST1.EN` was set whenever the transfer stopped at or after EOT**, even
  when it stopped for another reason. EN means the FDC tried to reach a
  sector beyond EOT, so a read aborted at the last sector by a data CRC
  error reported `$A0` where hardware reports `$20`, blurring "this sector is
  bad" into "I ran out of cylinder".
- **R was compared against EOT with `>=`.** The FDC compares for equality, so
  an R that starts above EOT never matches and it keeps stepping until it
  runs off the track and reports no-data. The old form stopped after one
  sector and called it end-of-cylinder, which since v1.8.18 turned a reported
  success into a reported error.
- **A multi-sector read cut short by a deleted address mark reported
  success.** Stopping after 3 of 9 requested sectors is not a successful
  completion; it now reports IC=01 with EN clear, since it never reached EOT.
- **WRITE DATA only ever wrote one sector**, though the package header
  documented it as spanning consecutive sectors to EOT. A guest writing 9
  sectors had 8 of them silently discarded and was told the write succeeded.

### Changed

- `docs/compatibility.md` and `ROADMAP.md` credited an `ST3.RY` change that
  was **reverted** and never shipped, and still listed Comando Quatro as an
  open failure. Both corrected, with the reverted attempt recorded so it is
  not retried. Screening counts refreshed: 108 of 113 render content.
- The datasheet-derived test suite had been edited to cite this emulator's
  own tests and a game's loader. Its whole value is independence, so those
  assertions now cite the datasheet's own definition of IC and EN.

### Testing

- `TestShortOfEndOfCylinderStaysNormal` asserted nothing about the status it
  was named for: inverting the guard it covered left the suite green. Replaced
  with tests that reach the result phase, and each new condition is checked by
  mutation — inverting EN, forcing IC normal, or restoring `>=` now fails 2,
  16 and 1 tests respectively.

## [v1.8.18]

### Fixed

- **Comando Quatro loads and runs.** The last +3 disk title that rendered
  nothing. Reaching the end of a cylinder is an **abnormal** termination on
  the µPD765, not a normal one: the host is expected to end a transfer by
  asserting Terminal Count, and an FDC that instead runs past the last sector
  and stops of its own accord reports that in ST0's interrupt code as well as
  ST1.EN. We set EN but reported normal completion.

  The game's own loader is the proof. Its read routine checks the three
  status bytes literally:

  ```
  FDCC  LD A,($FECA) / CP $40 / JR NZ,fail   ; ST0 must be $40
  FDD3  LD A,($FECB) / CP $80 / JR NZ,fail   ; ST1 must be $80
  FDDA  LD A,($FECC) / AND A  / JR NZ,fail   ; ST2 must be $00
  ```

  Reporting `ST0 = $00` failed that check, so it retried three times, gave up
  one sector into the 40766 bytes it wanted from track 3, and jumped to
  `$F9EC` — inside the range it had just failed to load. Hence a black screen
  and a CPU sliding through empty RAM.

  Found by tracing the guest: catching the runaway with a CPU pre-fetch hook,
  then disassembling the loader that jumped into it.

## [v1.8.17]

### Fixed

Five +3 disk titles that rendered nothing now load and run: **Captain
Planet**, **Barbarian II**, **Chase HQ II**, **Back to the Future Part II**
and **Adidas Championship Tie-Break**. Barbarian II and Captain Planet match
a +3 reference emulator screen for screen.

These had been recorded as blocked by copy protection the DSK format cannot
represent. That was wrong, twice — the explanation was inferred from the
images carrying unusual track layouts, never from evidence that the layout
caused the failure. Running the same images on a reference settled it: they
all load there, so the fault was ours. Three controller bugs, all found with
the new `ZX_GO_FDC_TRACE`:

- **EDSK ST1/ST2 CRC attribution** (`pkg/plus3fdc/disk.go`). `ST1.DE`
  reports that a CRC error occurred; `ST2.DD` reports it was in the *data*
  field. Every `DE` was being treated as an ID-field error, which corrupted
  the ID of exactly the sectors protection flags this way. `READ DATA` then
  answered "no data" for sectors `READ ID` was still returning — the
  controller contradicting itself, which is what the command trace showed.
- **Oversized sectors were refused** (`pkg/plus3fdc/upd765.go`,
  `track.go`). A sector whose ID declares N=6 (a nominal 8192 bytes) cannot
  fit a double-density track, and `findSector` rejected it. A real µPD765
  cannot tell: it reads the ID, streams the bytes N asks for, and crosses
  the index hole to get them. Track reads now wrap at the index hole, which
  is what the head physically does.
- **The ID compare ignored the size code** (`pkg/plus3fdc/upd765.go`). A
  sector was matched on R alone, so a command asking for N=2 was satisfied
  by a sector whose ID declares N=0. The µPD765 matches the whole ID. This
  is load-bearing for protection: Action Force puts a 128-byte sector at
  R=79 on track 0 and reads it asking for N=2, expecting the read to fail.
  Fixing the two bugs above without this one broke Action Force, which is
  how it was found.

### Added

- **`ZX_GO_FDC_TRACE=1`** logs every µPD765 command and result on the +3 /
  +2A. A title that will not load is almost always asking for something the
  controller answers wrongly, and the command stream says which one. All
  three bugs above were found with it. Documented in `DEBUGGER.md`.

### Changed

- `docs/compatibility.md`, `ROADMAP.md`, `README.md` and `docs/manual.md` no
  longer claim a copy-protection limitation, and the manual no longer says
  such disks are refused with a message — they load. Known issue is now
  Comando Quatro (still ours, still unexplained), 3D Grand Prix and Bonanza
  Bros (a +3 reference fails both of those images the same way we do, so
  neither is an emulator fault).

## [v1.8.16]

### Fixed

- **Screening recorded three working games as broken.** Alien Syndrome,
  Capitan Sevilla and Action Force 2 all load, draw their own screen and
  answer input. `MinContentPixels = 1000` threw all three away. The floor had
  been calibrated against a sparsest-real-screen of 3724, and no title with a
  bare one-line prompt was in that calibration set — Alien Syndrome's "PRESS
  ENTER" is 498 pixels.

  Lowering the number could not fix it. Adidas Tie-Break's bare BASIC cursor
  is 181 pixels and nothing is running, and it answers keypresses too because
  BASIC echoes what it is sent. Neither pixel count nor input response
  separates a running game from an idle ROM prompt.

  What separates them is *where* the pixels are. The frame is now measured a
  second time with the bottom two character rows excluded — the editor and
  report area the ROM owns — and `Screening` carries `CanvasPixels` /
  `CanvasColours` alongside the raw counts. Same reasoning as cropping the
  border: chrome the ROM drew is not evidence the guest drew something.

  The canvas rule can only rescue a frame the main floor rejected, never
  condemn one it accepted, so no verdict already recorded can move.
  `TestCanvasRuleOnlyEverRescuesBlank` pins that property.

### Changed

- **The "twelve titles fail on copy protection" claim was wrong**, in both
  `docs/compatibility.md` and `ROADMAP.md`, and is corrected. Dumping every
  track of all ten images shows three distinct groups: five genuinely
  protected disks with whole tracks given to a single 6144-byte sector, two
  ordinary unprotected disks that fail for reasons still unknown (3D Grand
  Prix ends at the ROM tape prompt, Adidas drops to BASIC), and three titles
  that were never broken at all. The manifest is now 7 Known issue, not 10,
  and each row says what the title actually does.

## [v1.8.15]

### Changed

- **File dialogs open large enough to be useful.** Every file browser — Open
  File, the snapshot load/save pickers, all the disk, tape, ROM, cartridge and
  recording pickers, 25 in total — now opens at 90% of the emulator window
  instead of Fyne's default, which showed only a handful of entries at a time
  and made a directory of hundreds of snapshots or games painful to navigate.
  The size floors at 900x700 and is clamped to the window, since a dialog
  cannot be larger than the window it belongs to (`cmd/zx_go/filedialog_size.go`).

### Verified

- **A 30-minute interactive GUI session ran clean**, which was the one open
  roadmap item an agent could not close: the headless proxy had passed long ago
  (50 000 frames, no leak, steady frame rate, final frame still pixel-perfect),
  but nothing had exercised the windowed app over a sustained real session.

## [v1.8.14]

### Fixed

- **`LastFrame` returned the wrong buffer in 80-column and hi-res modes**, a
  bug introduced with `LastFrame` itself in v1.8.10. `Render` has several
  exits — the 320-pixel base, the 640-pixel frame the 80-column tilemap path
  builds, and the 320x256 hi-res Layer 2 frame — and `LastFrame` handed back
  the base unconditionally. The NextZXOS Guide, which renders 80-column, came
  out as vertical stripes instead of its text.

  It now returns exactly what `Render` returned. Two corpus goldens
  regenerated in v1.8.10 had captured the wrong buffer (320x240 instead of
  320x256); regenerating them now reproduces the v1.8.9 files **byte for
  byte**, which is the proof the behaviour is restored rather than merely
  changed.

### Added

- **NextReg $68 bit 2, the ULA half-pixel horizontal scroll**, is now
  rendered. `zxula.vhd:353` builds the shift as `px(2 downto 0) & px(8)` — a
  4-bit count in HALF pixels — so the low bit moves the picture by half of one
  ULA pixel. The 320-pixel path cannot represent that, but the 640-pixel
  80-column path gives each ULA pixel two units, and half a pixel is exactly
  one of them. Applied there; the narrow path still rounds it away, and the
  bit reads back either way.

### Changed

- **Local-title screening is opt-in** (`ZX_GO_SCREEN=1`). Booting a machine per
  title for thousands of frames each runs well past Go's default 10-minute
  package timeout, so left on by default it turned `go test ./...` into a
  timeout failure for anyone with a title list. It is a measurement tool, and
  is now run deliberately.

- **+3 disk screening now runs 5000 frames.** Protected titles load in stages:
  one measured here drew nothing at all until about frame 5000, so the earlier
  1500-frame budget was recording a mid-load blank as a failure.

## [v1.8.13]

### Fixed

- **Copy-protected +3 disks load.** Those tracks pack more sector data than a
  physical double-density track holds by OVERLAPPING sectors: one oversized ID
  (N=6, claiming 8192 bytes) covers the region the ordinary 512-byte sectors
  occupy, read back through a different ID. Adidas Championship Tie-Break
  declares 11264 bytes of sector data on one track.

  A flat byte-stream model cannot overlap them — but the image declares its own
  track length (11520 bytes for that track), and the guest only ever reads by
  sector ID. The track is now sized to what the image describes rather than to
  the nominal figure, so every read returns the right bytes, where refusing the
  disk returned none. The nominal size stays the floor, so ordinary disks are
  laid out exactly as before.

  **Across a 250-image sample, failures fell from 35 to 1** over this and the
  earlier DSK fixes. The one remaining is a truncated dump whose track data
  runs past the end of the file.

## [v1.8.12]

### Added

- **The sprite engine now runs out of scanline time, as the hardware does.**
  `sprites.vhd` walks the sprite list from a state machine on the 28 MHz master
  clock — one clock to enter, one per sprite examined, one per pixel emitted —
  and raises `sprites_overtime` if the walk has not finished when the line
  resets (sprites.vhd:977), latching bit 1 of the `$303B` status port.

  Neither half was modelled: the limit was unenforced and that bit always read
  0, so software reading the flag to throttle its own sprite use saw a machine
  that never saturates, and scenes that should visibly drop sprites rendered
  complete.

  The budget follows the machine's line length — two 7 MHz columns per T-state,
  four master clocks per column, so 1824 clocks on a 228-T-state line. With 16
  pixel-wide sprites that is 107 of 128 drawn before the line runs out, which
  is the arithmetic the hardware does.

  Verified against the whole Next SD library afterwards: all 10 games still
  launch and render, and the 15 pixel-golden frames are unchanged.

## [v1.8.11]

### Fixed

- **61 disk entries in the compatibility manifest were measuring the ROM's own
  menu.** A +3 powers on into Loader / +3 BASIC / Calculator / 48 BASIC and
  stays there until ENTER selects "Loader". The screening never sent that key,
  so every `.dsk` title scored on the ROM menu — a drawn screen whose highlight
  moves under a probe keypress, giving "Boots (responds)" whether or not the
  disk was readable at all.

  Screening now starts the loader explicitly. Re-measured, the picture is
  quite different: 27 titles animate where 12 did before, and 007 Licence To
  Kill — previously recorded as booting — turns out to reach its actual title
  screen and control menu, which it never had.

  The same class of error was checked for on the tape path early on (0 of 100
  titles were sitting at the 128 menu) and simply never checked on this one.

- **Where Time Stood Still (+3 disk reissue)** is no longer Untested: the
  harness gained a +3 disk loader in v1.8.4 and the entry was never revisited.

## [v1.8.10]

### Fixed

- **Looking at the screen no longer changes it.** `ULA.Render()` composes a
  frame, and on the Next that walk steps the Copper as it goes — a MOVE has to
  affect the segments after it within the same frame, so the coupling is
  correct and Render is legitimately a mutation. The bug was that *inspection*
  went through it: screenshots, measurements and debugger views all called
  Render again, running the Copper program a second time for a frame the
  machine had already shown.

  Measured on TX-1696 from identical state with no CPU time in between: the
  first render produced its title screen in 20 colours, and every render after
  it a black frame.

  New `ULA.LastFrame()` returns the frame already composed, and composes once
  lazily if none has been. Screenshots, the debugger view, the headless dumps
  and `Harness.ScreenImage` now use it. `Render()` keeps its meaning — advance
  the picture by one frame — and is documented as once-per-frame.

  A frame-identity cache was tried first and rejected: the CPU's T-state
  counter is frame-relative and rebases, so `FrameID()` is always 0 and the
  cache froze the picture at the first frame. That showed up as blank corpus
  frames, which the liveness floor caught.

- **Four corpus goldens regenerated**, because they had captured the bug. The
  harness rendered each frame and then rendered the final one *again* to
  capture it, so the committed frames were composed twice: the Next entries
  (tilemap, specbong, mrk_zilogdma) had their border-tilemap pass applied over
  already-composited pixels, and mrk_z80bltst — a plain 48K program — had an
  extra FLASH tick, because merely looking at the screen advanced the flash
  phase.

  The GUI only ever renders once per frame, so it was always showing the new
  output; the old goldens recorded something only the test harness produced.

## [v1.8.9]

### Fixed

- **TX-1696 runs. It was never an emulator fault.** The last Next SD game that
  would not render is solved: it loads its assets from `C:/common/ayfx3.afb`
  and `C:/common/highScore.bin` — absolute paths at the **root** of the SD
  card — while this card shipped them only inside the game's own directory.

  Traced through the guest's own calls: the first `F_OPEN` (`main.nex`)
  succeeds with handle `0x02`; the second returns carry-set with `0x11`. The
  game does not check carry, uses the errno as a file handle, and every
  `F_READ` on it then fails with `0x0D` and zero bytes — so it retries for
  ever, never enables Layer 2, and shows a blank screen.

  Confirmed from both ends: walking the card image's FAT shows no `/common`
  directory at the root, and copying the game's `common/` folder there makes
  it run — title screen, PLAY/CREDITS/SETTINGS menu and ship all rendering.

  **Users with this game should copy its `common/` folder to their card root.**

### Known gaps

- **`Render()` mutates the machine**, found while chasing the above. The Next
  compose walk steps the Copper and replays the raster journal, so rendering
  the same frame twice runs the Copper program twice. Measured on TX-1696 from
  identical state with no CPU time between calls: the first render produced its
  title screen (5333 drawn pixels, 20 colours) and every render after it a
  black frame. Anything rendering alongside a render sees a picture the machine
  was never showing, and the Copper only advances when something renders.

  Not fixed here: the correct change moves Copper stepping out of the
  composition path, which touches the most output-sensitive code in the
  emulator, and is now item 2 on the roadmap rather than a rushed patch.

## [v1.8.8]

### Fixed

- **The active video line was not a raster position.** `NextReg $1E/$1F` report
  the beam's scanline, and software waits on them: TX-1696 polls the pair about
  1700 times a frame. Two defects made the number meaningless.

  `BeamPosition` masked the line with `& 0x1FF`, bounding it at 511. A
  128K-family frame is 311 lines, so it reported scanlines that do not exist —
  measured sweeping 0..511 against a real range of 0..310. It now wraps at the
  frame.

  Worse, the origin it measures from was only reset inside `flushAudioFrame`,
  which returns early when audio is disabled and is reached only from
  `Render()`. Any run with audio off, or any loop not rendering every frame,
  let the offset grow without bound and turned the "raster line" into a
  free-running counter unrelated to the beam. Wrapping at the frame makes the
  result independent of when the origin was last reset.

  The pixel-golden corpus is unchanged by this, so the rendered output of every
  vendored program is byte-identical; what changes is what software *reading*
  the raster registers sees.

  A pre-existing test asserted the old behaviour outright — "9-bit counter
  wraps: 513 & 0x1FF == 1" — so the bug was written down as intended. It now
  pins the frame wrap.

### Known gaps

- **TX-1696** still does not render. The raster fix moves it past the wait it
  was stuck in (PC advances from `0x8a5e` to a new loop at `0xc670`), and it
  demonstrably draws into Layer 2, but it never sets the Layer 2 enable bit —
  it writes `NR$69 = 0x00` and `OUT $123B, 0x00`. Root cause not yet found.

## [v1.8.7]

### Added

- **The rest of the Next SD card screened.** v1.8.6 covered the 10 `.nex`
  games; this covers everything else on it.

  - **NextBASIC games** (`TestNextBasicSDGames`) launch the way a user does:
    Command Line, `.cd`, `LOAD`, `RUN`. All three run — Orb, baSnake and
    NextBASIC Invaders. The path must be **quoted**: `.cd` splits its argument
    on spaces, so a card path containing one was read as two paths and both
    reported missing, which is why that game would not load.
  - **NEXTipede** loads from tape and runs its DEMO MODE attract sequence.
  - **Pogie and THEH** have nothing to launch: only assets and a 49179-byte
    `.snx`, which is a 48K snapshot and cannot hold a Next game's banked
    state. Recorded as such rather than as failures.

- **`DetectFormat` now falls back to file size** when the extension is
  unknown, which its contract had always claimed ("extension and content")
  while only ever looking at the extension. NextZXOS ships snapshots named
  `.snx` that are byte-for-byte 48K SNA; nothing could load them. Z80 and SZX
  are variable-length and are deliberately not guessed at.

### Fixed

- **NextBASIC Invaders was listed as a Known issue and is not one.** Verified
  by playing it: it autostarts to its control-selection screen, and ~48
  seconds of emulated play show the full invader formation, bases and shots
  with **no `Integer out of range`**. The two root causes that entry cited
  were fixed in earlier releases without the record being revisited.

- **A launch check that could not tell a game from a cursor.** The Next SD
  screening judged "did it render" with `uniformImage`, which only asks
  whether every pixel is identical — so a bare BASIC prompt with one flashing
  cursor block passed it, and NextBASIC Invaders was reported as rendering
  when it had not run at all. It now measures drawn pixels and distinct
  colours inside the display window with the border cropped, the same basis
  the rest of the screening uses.

## [v1.8.6]

### Added

- **Every Next SD game screened through the genuine NEXLOAD path**
  (`TestNexloadSDGames`, cmd/zx_go). It discovers each `.nex` on the card and
  drives the real NextZXOS `.nexload` dot command, which is the only path that
  can host a title calling the OS at runtime.

  **9 of 10 launch and render**: AngryBloaters, Halls of The Things, Lords of
  Midnight, Nextoid, Night-Knight, Revival Survival, Santa's Pressie, Sonic
  and Warhawk. Confirmed by eye for Halls of The Things and Night-Knight.

  This is the Next half of the compatibility corpus, and the evidence roadmap
  items 4 and 7 were waiting on. Both concern Next-only hardware — the
  zxnDMA's interrupt/match logic and exact Copper `MOVE` timing — and ten
  games run correctly without either being modelled. Neither is blocked now;
  both are simply unmotivated, which is a better answer than a guess.

### Fixed

- **Screening could record a working title as broken, and did.** A `.nex` is
  loaded by bank injection — banks copied in, then a jump — and that path
  provably cannot host a game which calls NextZXOS at runtime: the game's
  banks 5/2/0 overwrite the ones the OS keeps its screen and workspace in, so
  the OS handler runs against corrupt state and dies. Warhawk is such a game.
  It works through the genuine NEXLOAD path and is covered by
  `TestNexloadOSGamesIfPresent`, but automated screening saw a blank frame and
  it was written into the manifest as a Known issue.

  A blank frame from bank injection now classifies as **Inconclusive** rather
  than Blank, so this class of title can never be recorded as a fault again.
  The manifest entry is corrected to Works, with the evidence.

  This was the exact failure mode called out as the worst one a compatibility
  harness can have, committed anyway a release after saying so.

### Known gaps

- **TX-1696** is the one Next SD game that does not render. NEXLOAD launches
  it — the CPU is at `0xb14d`, not the menu loop — but the screen stays blank.

## [v1.8.5]

### Added

- **Input probing in the screening harness** (`Harness.ProbeInput`). A still
  screen is sent keys and watched for a material change, which separates a
  title waiting at a menu from one hung with a menu on it — a distinction the
  frame measurements alone cannot make, since both are simply still.

  The probe runs a **control first**: the same elapsed time with no key
  pressed. Only a change exceeding what the screen does unprompted counts.
  That control is the whole point of it. Without one the first version
  credited self-animation to the keypress — Cybernoid's title menu animates
  its border decoration — and reported 85 of 87 titles as responding, a figure
  too clean to be true. Each key also costs exactly one 32-frame FLASH period,
  so a flashing attribute is never mistaken for motion.

  Guarded by tests for all three failure modes: a responsive machine, a halted
  one, and a self-animating screen.

- **A `Boots (responds)` manifest status**, carrying exactly what that
  evidence supports: the title answered a keypress, so it is waiting for input
  rather than hung. It explicitly does **not** mean playable — what the
  keypress did is unknown.

### Changed

- Manifest: 78 of the 99 booting titles answer input. A separate check
  confirmed **0 of 100** screened titles were merely sitting at the 128 menu,
  which would have inflated every figure in the manifest had it been
  happening.

## [v1.8.4]

### Fixed

Screening the `.dsk` class for the first time surfaced three real bugs in the
+3 disk loader. All were found by running 60 real disk images, not by reading
the code.

- **Standard DSK images were rejected on a too-strict signature.** Only the
  first eight bytes of the CPCEMU header (`MV - CPC`) are a signature; the rest
  of the description field is free text chosen by whichever tool wrote the
  image. Matching the full `MV - CPCEMU Disk-File` refused valid disks —
  Batman - The Movie (Ocean 1989) carries `MV - CPCEMU / 27 Sep 97 14:45`.

- **Real parse errors were swallowed.** When an image's signature was
  recognised but its body would not parse, `loadDiskByPath` discarded that
  error and fell through to its extension fallback, reporting "unrecognised
  disk image format". That sent the reader looking for a missing loader
  instead of at the actual fault. The underlying error is now surfaced.

- **Denser-than-nominal track layouts were refused.** The track builder emitted
  the nominal IBM System 34 gaps, spending 54 bytes on GAP III after every
  sector. Ten 512-byte sectors need about 6410 bytes that way, against a
  6250-byte double-density track, so the disk was rejected — even though a real
  formatter simply shortens the gap to fit, which is how publishers packed
  them. The inter-sector gap now tightens to fit, down to a 12-byte floor, and
  refuses only when the data genuinely exceeds the medium.

  Together these took the failures from 12 of 60 disk images to 9.

### Added

- **`.dsk` / `.edsk` screening**, via `Harness.InsertPlus3Disk`. The +3 boots a
  disk from reset with no typing, which makes the whole class screenable — and
  it is the largest class by far, with 592 images in the collection screened
  from.

### Changed

- **The compatibility manifest went from 74 title rows to 135**: 7 Works, 12
  Works (caveat), 99 Boots, 1 Parses cleanly, 11 Known issue, 5 Untested. 109
  titles screened, 99 rendered content.

### Known gaps

- **Copy-protection track layouts are not modelled.** Nine of the disks
  screened declare more sector data than a physical double-density track holds
  — a size code of N=6 claims 8192 bytes against roughly 6250 — which is a
  protection scheme rather than a real geometry. The loader now names that as
  the reason rather than failing with an opaque "track too small".

## [v1.8.3]

### Added

- **Automated title screening** (`TestScreenLocalTitles`, `pkg/testharness`).
  The compatibility manifest carried thirteen "Untested" entries because
  verifying one meant a person watching a screen. This does it mechanically: a
  title is loaded, run, and measured, and the result classified Blank / Static
  / Live / Error.

  Measurement is of the **composited frame, display window only, border
  cropped**. Both halves of that were found by being wrong first:

  - Measuring the ULA display file at `$4000` reported two of the three
    vendored Next demos as blank, because a Next title can draw entirely into
    Layer 2 or the tilemap. Reporting a working title as broken is the worst
    error a compatibility harness can make.
  - Measuring the whole frame counted a tape loader's moving border stripes as
    content, which forced the colour floor up, which then rejected genuinely
    monochrome titles — Elite draws 35 000 pixels in three colours and was
    being called blank. Cropping the border fixes both ends.

  Commercial titles never enter the repository: the test reads a path list
  from a gitignored `testdata/local_titles.tsv` and skips when it is absent.

- **`.tzx` screening support**, with `Harness.LoadTZX`. Much of the canonical
  catalogue ships as TZX rather than TAP, since TZX is the format that can
  describe the custom loaders publishers used.

### Changed

- **The compatibility manifest went from 36 title rows to 74**, and from 13
  unresolved entries to 5. Forty-eight titles were screened; 47 rendered
  content. Ten were confirmed by eye against the captured frames rather than
  trusted from the numbers.

  A new **Boots** status carries the honest meaning of a screening: the
  guest's own code ran and drew its title or menu screen, but no input was
  sent, so playability is unverified. It is deliberately weaker than Works.

  The five still unresolved now record *why* — no copy to hand, or no +3 disk
  loader in the harness yet — rather than sitting silently as "Untested".

### Known gaps

- **Warhawk** does not run through the harness's ROM-independent `LoadNEX`: it
  loads, runs its entry stub at `$5C50`, and lands in the ROM main loop at
  `$1304`, rendering nothing. The README screenshot came from the full
  NextZXOS boot, so this may be a loader limitation rather than an emulator
  fault. Recorded as a Known issue pending investigation.

## [v1.8.2]

### Fixed

- **Stale claims across the README and comparison docs**, found by auditing
  each factual statement against the code rather than reading for sense:

  - The headline sentence listed every machine except the **SAM Coupé**.
  - The downloads table offered only x64 for Windows and Linux, though the
    release workflow builds and publishes **arm64** for both. The Windows
    ARM64 entry is marked experimental, since it compiles and publishes but
    its OpenGL path has not been exercised on real hardware.
  - "six disk formats" appeared twice. It counted one table row, and read as
    a total once TR-DOS and Opus were listed beside it; there are ten. The
    completeness claim now points at the table instead of repeating a count.
  - The project layout omitted **seven packages**, including those behind
    headline features — `sam`, `zx8x`, `betadisk` and `opus`. All 31 are now
    listed, checked programmatically against `pkg/`.
  - "~90s, forty-plus packages" for the test suite: a clean run is ~2 minutes
    over 53 packages, most of it the Cringle exercisers. `-short` (~1 min) is
    now documented alongside it.
  - The comparison tables had no Opus Discovery row in either media formats or
    peripherals.

- **Compatibility manifest** gained an Opus Discovery section recording the
  titles and operations actually driven end to end.

## [v1.8.1]

### Added

- **Opus formatting, via the WD1770's WRITE TRACK.** `FORMAT 1;"name"` works.
  v1.8.0 refused the command on the grounds that faking success could corrupt
  a disk — true of a silent no-op, but not a reason to leave it unimplemented.

  The controller is handed a whole raw track a byte at a time — gaps, sync,
  address marks, ID fields and sector data — and recovers the sectors from it,
  which is what the real chip does. The ROM builds that stream from the
  run-length table at `$1BDB`, substituting track, side, sector and size into
  its `$F0-$F4` placeholders; the result is standard IBM System 34 double
  density. WRITE TRACK carries no length in the command — it runs from one
  index pulse to the next — so the controller ends it itself after a track's
  worth of bytes.

  A `.opd` stores only sector data, so ID fields and gaps are discarded. A
  sector's physical position comes from where the head actually is rather than
  what the ID claims, because a flat image has nowhere to record a
  disagreement; sectors outside the geometry are dropped rather than aliased
  onto a neighbour. Address marks count only after the sync run, so `$FE` and
  `$FB` inside sector data stay data.

  Verified against the ROM's own commands rather than in isolation: a blank
  image formatted by `FORMAT` and read back by `CAT`, showing the new disk
  name and a full 178 free blocks against the 148 a game disk reports; and a
  copy of a real game disk formatted while the source file is compared byte
  for byte and left untouched.

### Fixed

- **Windows ARM64 release builds.** The target compiled with the runner's
  stock `clang`, which targets MSVC. go-gl's `build.go` puts glfw's bundled
  mingw headers on the include path for every Windows build, so `<xinput.h>`
  resolved to a header using `WINBOOL` — a mingw-w64 typedef the MSVC SDK does
  not have — and clang read `XInputEnable(WINBOOL)` as an untyped parameter
  and stopped. cgo wants a GCC-compatible toolchain on Windows in any case, so
  the build now installs llvm-mingw and uses `aarch64-w64-mingw32-clang`.

  The release workflow also gained `workflow_dispatch`, with publishing
  restricted to tag runs, so the matrix can be built and checked without
  cutting a release.

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
  class.** The GHDL extract of the real MMU decode gained an `$EFF7` stimulus
  input and testbench sequences for the
  `$8E` bit-3 suppression, the bit-3-set reload, the `$DFFD` clamp, and the
  `$EFF7` bit-3 RAM-at-`$0000` behaviour. Regenerated
  `testdata/paging_golden.txt` (144 mappings) and taught the replay the
  `WEFF7` op. Confirmed the golden has teeth: reverting the `$DFFD`-clamp
  fix makes it fail (`$C000` 228 vs 4).
- Unit-level `TestNR8E_GateMatrix` mirroring the same suppress/reload/clamp
  cases beside the unconditional-port matrix.
- An internal catalogue of paging/MMU gates with per-rule status
  (fixed / modelled /
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
