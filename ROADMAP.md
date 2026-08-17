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

## CURRENT STATE (2026-08-16 — v1.9.1, plus unreleased work)

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

`docs/compatibility.md` now holds **158 title rows: 11 Works, 20 Works
(caveat), 58 Boots (responds), 51 Boots, 1 Parses cleanly, 11 Known issue, 6
Untested.** It was 36 rows with 13 Untested before automated screening.

**Eight of the eleven Known issue rows are settled as bad dumps, on a third
emulator's evidence.** MAME's `specpls3`, driven from our own vendored ROMs
(CRCs `9bc85686` / `db551783`, matching its expected set exactly), **refuses to
mount four of them**: "floppy tracks=45, drive tracks=42", where a real +3 drive
has 40. Three more it mounts and fails on identically to us, ending on the same
vertical stripe pattern. Add the two already checked against two references (Bonanza Bros, 3D Grand
Prix) and **every Known issue row is now evidenced by a third emulator**.

**California Games came off the list entirely: it loads, and it was waiting for
a keypress.** Two references could not corroborate it — ZEsarUX never leaves its
own ROM menu on that image, and MAME's automated ENTER never registered — so
comparison was a dead end and disassembly settled it instead. The guest sits at
`$7670` inside `EI / HALT / RET`, called from
`7720 CALL $766B / 7723 CALL $76CC / 7726 JP NC,$7720`, and `$76CC` scans nine
bytes at `$5B0C` for a non-zero entry, returning carry set with a key code.
That is a keyboard poll: the loop reads "wait until a key is pressed". Tapping
SPACE moves the PC off `$7670` at once.

The lesson to keep: **when no reference will corroborate a title, disassemble
the loop rather than keep hunting for a reference.** Two emulators refusing to
start an image says nothing about ours. `_tools/loaderstop` (local-only) does
the whole sequence — sample the PC, dump the stack, disassemble the callers,
press a key and report whether it moved.

Tracing it also turned up a genuine datasheet violation on the way: READ A
TRACK was not setting ND when the ID it is given is absent from the track, now
fixed with the datasheet's wording behind it. It did **not** change this title,
and is recorded as a correctness fix rather than a compatibility one.

Both directions of that were corrections rather than fixes, on the same day.
Nine rows came *up* off Known issue: they were working titles downgraded on a
tape-block count that did not mean what it was taken to mean. Nine went *down*
after a differential sweep of the whole +3 disk class — eight of them scored
**Boots** on 18432 px in 2 colours, which is the uniform stripe pattern of
uninitialised video memory rather than any title screen. Five unrelated games
measured it to the pixel, which is the tell.

Screening (`TestScreenLocalTitles`, pkg/testharness) loads a title headlessly,
runs it, and measures the display window with the border cropped. A **Boots**
verdict means the guest's own code ran and drew its title or menu screen; it
does **not** mean the title was played, because no input is sent. That is the
remaining gap.

- [x] ~~Resolve the 13 Untested entries~~ — 8 resolved. Six rows still read
  Untested and each records why: no copy to hand (Jet Set Willy, The Hobbit,
  Baggers in Space, Lemmings +3), or no launchable program in the title's own
  directory (Pogie, THEH — assets plus a 48K-sized `.snx`). Where Time Stood
  Still +3, listed here as the sixth until 2026-08-14, is now a Known issue row
  instead: it was screened, and both machines are blank on that image.
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
- [x] ~~Nine tape titles load only part of their tape~~ — **settled
  2026-08-14: none of them is a fault, and there was never a tenth.** Each
  was screened, its screen looked at, and the block count taken from
  `Harness.TapeBlocksDecoded` rather than from a counter that measures
  something else.

  RoboCop and Target Renegade decode **every block on their tape** — the old
  figures were trap fires, which cannot see a loader that decodes edges itself.
  Lemmings, Gauntlet, Turrican, Double Dragon, Last Ninja 2 and Myth are
  multiloads sitting on their own title screen or menu with the rest of the
  tape still to come: Lemmings shows "PRESS SPACE TO BEGIN" after 2 blocks of
  125, and the rest are levels.

  The Way of the Tiger reaches its main menu and matches a reference on the
  same frame, but **no block figure is quoted for it**. Its menu polls the
  keyboard hard enough to clear the loader read-rate threshold, and that is
  precisely the case where the counter credits a guest with every block the
  tape rolls past. An earlier revision of this item said "all 18 of 18",
  which was the polling being counted as loading.

  R-Type was briefly written up here as a genuine failure on the strength of
  a screenshot reading ERROR IN LOADING. It is not one. Both we and a
  reference emulator settle on **"REWIND SIDE 2 THEN PRESS ANY KEY"** at
  3724 lit pixels each, 0.1% apart and both still. The error screen came
  after the screening's own probe keys pressed on at that prompt.

  Two lessons worth not relearning. **A block count is not a load figure
  unless it is the decoded one** — the trap counter undercounts and the
  player's cursor overcounts, and quoting either put seven wrong rows in
  `docs/compatibility.md`. **Look at the screen**: every conclusion here
  turned on reading the text, and every wrong one before it turned on a
  pixel total.

- [~] **Verify what the keypress did.** Partly automated, and no longer purely
  manual. A response proves a title is waiting rather than hung; it does not
  prove the title is playable. Rather than hand-write an expectation per
  title, `_tools/refdiff` (local-only) drives zx_go and a reference emulator
  through the same load-and-keypress sequence and compares the resulting
  6912-byte display file. **Six +3 disk titles verified so far**, two of them
  byte-identical.

  Both sides are now paced in **guest** T-states rather than one stepping
  frames while the other free-ran, so the old "cannot be synchronised" limit
  is gone; the run prints the residual skew per title. Adidas Tie-Break is
  the one open case, where the reference sticks at 1447 lit pixels regardless
  of how long it is given while a second reference loads the title fully, as
  we do.

  **The tape class is now covered.** Across its 18 rows the block counts agree
  with the reference on 15, and every pair was read by eye: each is the same
  correct sequence, differing only in where the sample landed. Getting there
  needed three fixes to the tool, each of it measuring the wrong thing — it
  compared our trap fires against the reference's loader entries, its motion
  guard sampled over 0.29 s and could not see a page swap, and its quiet window
  mistook a loader handover for the end of a load.

  **The method's real limit, and it is not fixable by tuning: an attract cycle
  puts the two machines on different phases of the same correct sequence.**
  RoboCop's title against its control menu, Target Renegade's title against its
  high-score table, Gauntlet III's credits one page apart, Operation Wolf's
  credits against its title art. The proof that this is phase and not
  divergence is that it moves: Renegade and Turrican came back byte-identical
  on one run and 6.4% and 14.0% apart on the next, with nothing changed but the
  quiet window. **So a byte-identical verdict on a cycling title is a
  coincidence of sampling, not a property to quote.** The longer motion window
  voids most of these as ANIMATED rather than calling them divergences, and the
  reliable move is to read the PNGs `-keep` writes.

  **Phase-tolerant matching is built, and it now lives in `pkg/screendiff`.**
  Each side samples `CycleSamples = 12` screens `CycleStepT` apart after the
  compared one, spanning 36 s, and `BestCrossMatch` asks whether either
  machine's screen appears anywhere in the other's sequence. A hit within
  `PhaseMatchPct = 0.5` reports `PHASE` instead of `DIVERGES`.

  It sits in a tracked package rather than in `_tools/refdiff` (local-only) for
  one reason: that directory is gitignored, so tests written there are
  invisible to CI by construction, and the verdict taxonomy is mostly refusal.
  A guard that can never fire looks exactly like a guard that never needed to
  fire. The driver still produces the screens; the package decides what they
  mean, and `go test ./...` now covers it.

  **A review on 2026-08-16 found four ways the rescue could fire wrongly, all
  now fixed and pinned.** The cause was shared: `BestCrossMatch` searched raw
  screens with none of the guards protecting the pointwise comparison. A flat
  screen anywhere in either 13-screen sequence cross-matched perfectly; so did
  a failed capture, because `Diff` scores a zero-length overlap as 0%. The
  rescue also outranked `INERT`, reporting agreement about a probe key neither
  machine answered, and reached up into the `match (minor)` and `PARTIAL`
  bands, relabelling a pair 0.6% apart as `PHASE` at 0.0%.

  The ordering is now: `RESET`, `LOADING`, `CUSTOM`, `SPLIT`, `BLANK` and
  `INERT` all outrank the rescue; the rescue outranks `ANIMATED`, because an
  attract cycle is moving by definition and requiring stillness would void
  every case the verdict exists for; and it fires only inside the divergence
  band, so it can never replace a verdict that already reports agreement.

  **Not yet validated end to end**, which needs a reference-emulator run.
  `-keep` now writes the two screens a `PHASE` verdict was actually decided on,
  which that run will need: the percentage says how far apart two screens are
  and never what either one shows.

  Two counts still disagree for a structural reason rather than a fault: ours
  is blocks the guest read, the reference's is entries into the ROM loader, and
  a title's own loader never appears in the second. R-Type reads 10 where the
  reference logs 9 and both settle on the same screen to 0.1%; The Way of the
  Tiger reads all 18 where the reference logs 9. The run voids both, which is
  conservative and right, but it is the guard admitting the two quantities are
  not comparable rather than finding anything.

  The +3 disk rows were re-run after the motion window changed, since that path
  shares it: every percentage is unchanged or slightly better than recorded
  (Action Force 2 4.2%, Captain Planet 1.3% from 1.4%, Chase H.Q. II 0.6% from
  2.0%, Barbarian II and Capitan Sevilla still 0.0%). No regression. It did
  surface that two of those rows claimed more than they had: Barbarian II and
  Capitan Sevilla report INERT, meaning SPACE moved *neither* machine, so their
  matching screens said nothing about input. Both menus take number keys. The
  rows now say screen-verified rather than input-verified.

  Remaining work is breadth: the 128K class beyond the tape rows.
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
that can host a title calling the OS at runtime. **All 12 launch and render**,
re-confirmed at v1.9.0 on 2026-08-15.

`SpecNext_Games/` (local-only) is a download staging area, and it holds two
different things. Its four **downloaded** titles — DougieDo, EternalBattle,
BasInvaders, BlokBoy — are already installed under `roms/next/sd/games/Next/`
and are already counted in the figures above, so finding them there is not
evidence of unscreened work.

Its `next_native_games.txt` is not that. It catalogues **108 Next titles by
URL**, of which only a handful are on the SD card, so **roughly 100 titles are
undownloaded and unscreened**. That is the largest single block of open
compatibility work in this document. Fetching them is a bulk pull from a
third-party site and needs the maintainer's say-so, which is why it is recorded
here rather than started.

An earlier revision of this paragraph said everything in the directory was
already installed and told the reader not to look further. That was wrong, and
wrong in the expensive direction: it suppressed those ~100 titles behind a
claim that sounded like a resolution.

**Every Next game on the SD card is now accounted for**: every title directory
holding a launchable program runs — 12 `.nex` through NEXLOAD, 5 NextBASIC
programs through the Command Line, and NEXTipede from tape. Pogie and THEH
hold only assets and a 48K-sized `.snx`, which cannot represent a Next game's
banked state, so there is nothing to launch.

TX-1696's own entry records why it needed its assets at the card root: it
opens `C:/common/...` by absolute path. That is unrelated to the working
directory and is not an emulator fault.

That is what items 2 and 4 were waiting on, and the answer it gives is
"no evidence either is needed": eighteen Next programs run without the zxnDMA
interrupt/match logic or exact Copper MOVE timing being modelled — the 12
`.nex`, the 5 NextBASIC programs and NEXTipede. Neither is now blocked; both
are simply unmotivated.

### 2. [correctness] zxnDMA interrupt / match logic and bus arbitration

**Unblocked, and unmotivated.** The transfer engine, prescaler and cycle
timing are complete and spec-checked. Not modelled: the interrupt/match logic
and DMA-vs-CPU bus arbitration (`pkg/next/dma/dma.go`).

The Next corpus now exists — all 12 SD `.nex` games driven through NEXLOAD, all
12 rendering (`TestNexloadSDGames`, re-run green 2026-08-16) — and none of them
needs this. That is the evidence the item was waiting for, and it argues for
leaving it alone until a title actually demands it.

### 3. [product] Windows ARM64: the core is proven, the GUI is not

**Run 2026-08-14 against v1.8.23 on Windows 11 Pro ARM64 (Build 28000).**
Two of the three checks pass outright; the third could not be reached.

**The emulator core is proven on ARM64.** A headless run
(`--headless --frames 500 --save-screen`) matched a linux-amd64 reference on
every deterministic value: final PC 5631, 3 972 119 instructions, 419
interrupt fires, and a byte-identical screen PNG (SHA-256
`e0a590c3…a108a04`). The binary also loads and reports its version cleanly, so
the llvm-mingw packaging is sound and the toolchain question is closed.

**Read that result with one caveat.** The guest was QEMU TCG on an x86 host,
not a Snapdragon. TCG executes real AArch64 semantics, so the arm64 compiler
output genuinely ran, and the headless path is single-threaded, so its
determinism holds in full. What TCG does **not** reproduce is ARM's weak
memory model, so a data race between the emulation goroutine and the GUI
would be invisible there and could still appear on real hardware.

**Step 3 never produced a window**, because that guest has no GPU (ramfb
framebuffer only). So there is no picture to judge, correct or otherwise, and
the resize check was never reached. **The `experimental` marker stays.**

**The durable finding is a Fyne build constraint, verified in the module
cache** (see "Do not re-derive" below): a Windows ARM64 Fyne binary always
requests OpenGL ES 2.0 over WGL and cannot be built any other way.

**The GUI half is now a toolkit bug, not ours, and is parked.** It is filed at
[fyne-io/fyne#6483](https://github.com/fyne-io/fyne/issues/6483) with a fix at
[#6484](https://github.com/fyne-io/fyne/pull/6484), and the full write-up lives
in `KNOWN_ISSUES.md`. **Do not carry a local patch for it.** A `replace` onto a
forked toolkit, for a platform we cannot test, is the kind of workaround this
project rejects, and the fix must be a fallback rather than an unconditional
hint — a physical Snapdragon may expose the WGL extension, and forcing EGL
would break that case.

Worth knowing if it goes quiet: it is the Windows instance of
[#4782](https://github.com/fyne-io/fyne/issues/4782), open since April 2024,
where a Fyne contributor reached the same diagnosis in one line and the thread
then became an argument about whether devcontainers are in scope. Ours is
argued from a shipping consumer platform plus a third-party report
([netbirdio/netbird#4691](https://github.com/netbirdio/netbird/issues/4691))
so it cannot be dismissed the same way. If it stalls anyway, that is a signal
to leave it stalled, not to work around it here.

- [ ] **Launch the binary once on a physical Windows-on-ARM device.** The one
  piece of this that is **not** waiting on Fyne. Both failure reports are from
  virtual GPUs, so if a Qualcomm driver does grant the context then the GUI
  already works and none of the above applies. If a window appears, check the
  picture against the 48K boot screen and resize once. Needs the hardware.
- [⊘] **The process hangs when window creation fails.** Same toolkit, same
  answer: the run loop blocks with no window to service, and the only fix from
  our side is a watchdog, which is a hack. Noted at the end of #6483. Not
  pursuing it separately.
- [x] ~~ANSI escapes printed literally in the Windows console~~ — ours, and
  fixed. `term.IsTerminal` accepts a console handle because GetConsoleMode
  succeeds, but nothing enabled `ENABLE_VIRTUAL_TERMINAL_PROCESSING`, so the
  banner arrived as `<-[38;5;196m`. Colour now depends on the console
  accepting ANSI as well as being a terminal, and falls back to plain text
  (`pkg/zxlog/color.go`). Not ARM-specific: it affected every legacy Windows
  console on any architecture.

### 4. [product] Time travel: three mechanisms shipped, two devices short

Three mechanisms, deliberately separate.

**Reverse debugging is done and wired to both surfaces**
(`pkg/debugger/reverse.go`, `pkg/debugger/reverse_ui.go`,
`cmd/zx_go/reverse_cmd.go`). A cursor walks the M1 ring backwards one
instruction at a time, and every view reads the instant under it. Registers,
flags and the branch that led to each instruction always; on a wide ring the
pair registers, the shadow registers and an eight-word stack window too, as far
back as the ring holds. Conditions evaluate against a recorded instant through
the same interface the live machine uses, so `run-back "a == 0"` works. The
telnet commands are `step-back`, `step-forward`, `run-back`, `to-present` and
`reverse-status`; the GUI's **Reverse** tab drives the same cursor, and the
visual debugger opens a wide ring by default so its panels have the shadow set
and the stack window to show.

The invariant to keep: **anything the recording cannot answer is refused, not
guessed.** Memory is not recorded per instruction, so a memory condition has
no value to test going backwards, and a narrow ring never recorded BC. Both
return an error. Do not "improve" this into evaluating against zero: a
breakpoint that silently stops being the one the user wrote sends people
hunting bugs that are not there. Note the reason answerability is re-checked at
every step of a backwards search is NOT short-circuit evaluation — `binOp.eval`
computes both operands eagerly — it is that the check costs nothing.

**Snapshot rewind now restores the machine.** The time-travel ring captures
through the registry, so a rewind returns every registered device rather than
the CPU and the visible 64 K. What it still does not return is the +D and the
Opus Discovery, neither of which has a `Device`. The ring holds 16 captures by
default, so the reachable window is short.

`pkg/machinestate` is the registry the complete capture is built on: named
devices, a device-set check in both directions before anything is applied, and
canonical ordering so two captures of an unchanged machine compare equal.

- [x] ~~Registry~~ — done, with the refusal semantics tested.
- [x] ~~AY~~ — full generator state, mutation-verified. The test replays audio
  from a capture rather than comparing fields, because a field-by-field test
  only checks the fields someone remembered to add. Worth knowing: the LFSR
  mutation initially passed, because the fixture wrote `0x38` to the mixer and
  those bits are active low, so it had disabled noise on every channel.
- [x] ~~The remaining devices~~ — the CPU, ULA, memory, `plus3fdc`, `betadisk`,
  keyboard, `if1`, `multiface`, the DAC, the tape player (`pkg/ula/tapestate.go`
  — position, not the tape image) and the whole Next set are captured;
  `(*emulator).stateRegistry` is the list, and it registers each device
  exactly when the machine really carries it. **Not** the +D or the Opus
  Discovery; those two are what is left. `pkg/next/lores` has a `Device` that
  nothing can register, because no code owns a LoRes instance.
- [x] ~~The replay-equivalence oracle~~ — `cmd/zx_go/replay_oracle_test.go`.
  Fingerprints outputs and memory in bulk rather than device state, so it
  cannot pass by comparing the capture with itself, and a companion test
  leaves one device un-rewound to prove the oracle still bites.
- [x] ~~Replay-based step-back~~ — `replay-back [N]` (`cmd/zx_go/replayback.go`).
  Restores the newest checkpoint at or before the target and re-executes to it,
  landing by step count from the checkpoint rather than by instruction counter:
  the counter tallies M1 fetches, so a prefixed instruction moves it by more
  than one and a halted CPU not at all. Before handing an instant back it
  re-runs the window to the present and compares the whole machine, so a window
  it cannot reproduce is refused rather than answered.
- [x] ~~A determinism audit~~ — the step path is deterministic; replay of a
  step-driven span reproduces the machine byte for byte on 48K, 128K, +2, +2A,
  +3 and the Next. Two things are NOT reproducible and are refused rather than
  guessed at: a span the frame loop ran through `ExecuteFrame` (it rebases the
  T-state counter every frame and schedules the frame INT differently from the
  single-step path), and any change made from outside the CPU during the window
  (`write-memory`, a key press, a disk arriving).

### 5. [research] Copper cycle accuracy

**Unblocked, and unmotivated.** Since v1.6.4 the Copper is stepped in 8-pixel
segments, which is exact for `WAIT` — the hardware threshold is `x<<3 + 12`,
so 8 pixels *is* its resolution. What remains is `MOVE` landing mid-segment
(`pkg/next/copper/copper.go:19`).

The 12 Next `.nex` games now screened render correctly without it, so there is
still no observed case where it matters. Leave it until one appears.

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
- [⊘] **A +3 drive's cylinder limit.** A real +3 3" drive has 40 cylinders and
  cannot seek past the head stop; we accept a seek to any cylinder the image
  declares. Four corpus images declare **45**, and MAME refuses to mount them
  for exactly this reason ("floppy tracks=45, drive tracks=42"). Modelling the
  limit would be more faithful, and it is deliberately not done: it would make
  no title work — those four fail on all three emulators either way — while
  risking the images that legitimately use 41 or 42 tracks, since the real
  limit varies by drive. Revisit only if a title is found that depends on a
  seek failing.

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
- **A Windows ARM64 Fyne binary always asks for OpenGL ES, and cannot be
  built to ask for anything else.** Verified in fyne v2.6.3 in the module
  cache. `internal/painter/gl/gl_es.go` is selected by
  `(gles || arm || arm64) && !android && !ios && !mobile && !darwin && !wasm`,
  and `gl_core.go` carries an explicit `!arm64`, so **no build tag can select
  desktop GL on arm64**. `internal/driver/glfw/glfw_es.go:8` then asks GLFW
  for `ClientAPI = OpenGLESAPI` at version 2.0. The constraint keys on
  architecture rather than platform, which is right for ARM SBCs and mobile
  and wrong for Windows-on-ARM: there it means requesting a GLES context
  through **WGL**, which needs the display driver to expose
  `WGL_EXT_create_context_es2_profile`. On Windows, GLES normally arrives via
  ANGLE's EGL instead.
  **Ruled out by test: bundling ANGLE next to the executable does not help.**
  With `libEGL.dll` in the same directory the error still named WGL, because
  GLFW never consults EGL unless asked. It needs
  `glfw.WindowHint(glfw.ContextCreationAPI, glfw.EGLContextAPI)`, which
  `glfw_es.go` never sets. Both symbols exist in the binding we already
  depend on (`go-gl/glfw/v3.3`, `window.go:89` and `:132`; also v3.4, which
  Fyne's `develop` uses), so the fix is upstream in Fyne and nothing in zx_go.
  **Filed 2026-08-17**: fyne-io/fyne#6483 and #6484.
  **The fix is NOT "set the hint", which is how this entry once read.** Setting
  `ContextCreationAPI` to EGL unconditionally is a bet that no Windows-on-ARM
  driver exposes `WGL_EXT_create_context_es2_profile`, and a physical
  Snapdragon may well do so; forcing EGL would then break a working device and
  oblige every Fyne app to ship ANGLE. The PR retries through EGL only *after*
  the native attempt has failed, so it cannot regress a driver that works. If
  this ever needs re-deriving, re-derive the fallback, not the one-liner.
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
  (`cmd/zx_go/next.go:843`). Required for deterministic menu-interaction
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
