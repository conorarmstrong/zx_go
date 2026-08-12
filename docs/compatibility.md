# Compatibility manifest

What runs in zx_go. This is a working document — every entry has a
status, the model it was tested on, and (if "Issue") a brief note.
The list isn't exhaustive; it's the titles a contributor has
specifically loaded and run to a point of confident "works" or
"broken here". To add an entry, follow the protocol at the bottom.

Status legend:
- **Works** — boots to title screen, accepts input, plays.
- **Works (caveat)** — playable but with a known emulator-side
  imperfection. Caveat documented in the Notes column.
- **Parses cleanly** — file-format parser accepts the image
  without error. No gameplay verification.
- **Boots** — loaded headlessly and rendered its own title, menu
  or attract screen, measured in the display window with the
  border cropped. Stronger than "parses cleanly": the guest's own
  code ran and drew. Weaker than "Works": playability is
  unverified. Produced by `TestScreenLocalTitles`; see
  "Automated screening" below.
- **Boots (responds)** — as Boots, and the still screen changed
  materially after keys were sent, beyond what it changes
  unprompted. Evidence the title is waiting for input rather than
  hung. It is **not** evidence the title is playable: what the
  keypress did is unknown.
- **Known issue** — loads partway, crashes, or visibly wrong.
  Filed as a GitHub issue (linked).
- **Untested** — no contributor has run this through yet.

Confidence note: every title below sits on top of the same Z80 core
that passes both Cringle exerciser suites
([zexdoc and zexall](../pkg/z80/testdata/conformance/README.md)).
"Works" therefore means CPU correctness is not under suspicion when
something does go wrong — it's almost always a peripheral or timing
issue. See "Foundation tests" for what blanket-cover tests do and
don't buy you.

## Foundation tests

These integration tests under `pkg/testharness` (and one in
`pkg/plus3fdc`) prove the *foundation* for each category works.
They do **not** prove arbitrary programs in the category run
correctly — they prove the boot path and primary API surface are
sound, which narrows the debug area when a specific title misbehaves.

| Test | What it proves |
|---|---|
| `TestNewBoots48K` (testharness) | The 48K ROM boots and writes screen attribute state — any failure here would indicate a broken core, not a broken title. |
| `TestIF1CATCommand` (testharness) | Interface 1 + Microdrive paging, ROM hooks, and `CAT 1` execution work. Doesn't prove arbitrary IF1 software runs. |
| `TestDiscipleColdBootAndKeypress` (testharness) | DISCiPLE pages in, boots GDOS, and accepts keypresses. Doesn't prove arbitrary GDOS programs run. |
| `TestSaveDSKRoundTrip` (plus3fdc) | +3 disk image parser / writer is symmetric. Doesn't prove arbitrary +3 disk titles boot. |
| `TestNextRealROMBoot` (testharness) | NextZXOS boots through ~10K instructions without crashing. Doesn't prove it reaches the BASIC prompt or any specific Next title runs. |
| `TestModelNextLayer2VisibleEndToEnd` (testharness) | The NextReg → palette → Layer 2 → compositor pipeline produces correct RGBA pixels. Doesn't prove arbitrary Layer 2 demos look right (timing, sprite layering, Copper sync). |
| `TestLoadNEXAgainstRealSample` (testharness) | The .NEX V1.2 parser correctly reads banks 0–7 from a real distro file. Banks ≥8 are silently skipped — `.nex` files using extended banks won't load fully. |
| `TestZexdoc`, `TestZexall` (z80) | Every documented Z80 instruction passes Cringle's exerciser; every undocumented behaviour (F3/F5, MEMPTR/WZ partial, DDCB register-copy) passes too. |

If a title in a covered category fails, the bug is almost certainly
in a peripheral driver, timing, or untested undocumented behaviour —
not the core CPU.

## Opus Discovery disks

Run from real `.opd` images on a 48K with the Opus fitted, which
exercises the ROM overlay, the WD1770 and the NMI-per-byte transfer
end to end. The disks themselves are commercial and not redistributable,
so these are not in the automated corpus.

| Title | Status | Notes |
|---|---|---|
| Batty | **Works** | Verified 2026-08-10: `RUN` autoboots the disk's `run` file; the high-score table renders and the game starts. |
| Exolon | **Works** | Verified 2026-08-10: `RUN` autoboots; title screen, author credit and the 5-entry control menu all render. |
| `CAT` / `FORMAT` | **Works** | Verified 2026-08-10: `CAT 1` lists a real disk (name, files, free-block count); `FORMAT 1;"name"` writes all 40 tracks and the result catalogues back with a full free count. Covered by `TestOpusFormatsABlankDiskAndCatalogsIt`. |

## 48K titles

Canonical 48K titles. None have been individually verified through
zx_go yet; foundation tests confirm the 48K boot path works.

| Title | Status | Notes |
|---|---|---|
| Manic Miner | **Works** | Verified 2026-08-08: plays — Central Cavern renders with the animated hazards, the AIR bar and the score/lives panel. |
| Jet Set Willy | Untested | Not screened: no original release to hand — the collection searched holds only fan variants and sequels. |
| Knight Lore | **Works** | Verified 2026-08-08: menu accepts input, starts, and plays — the filled-3D room, character sprite and status panel all render correctly. |
| Atic Atac | **Works** | Verified 2026-08-08: boots to the game-selection menu, accepts keyboard input through it, and plays — room graphics, sprites and the HUD (timer, lives, health) all render correctly. |
| Sabre Wulf | **Works** | Verified 2026-08-08: menu accepts input, starts, and plays — jungle playfield with the score/lives HUD. |
| Skool Daze | **Works (caveat)** | Verified 2026-08-08: boots and runs its attract-mode demo with full in-game graphics (classrooms, sprites, HUD). Not driven into a played game. |
| The Hobbit | Untested | Not screened: no copy to hand; it is an adventure, absent from the arcade collection searched. |
| Lords of Midnight | **Works (caveat)** | Verified 2026-08-08: renders the in-game landscape-and-text view correctly. Not driven through the game. |
| Elite (Spectrum port) | **Works (caveat)** | Verified 2026-08-08: boots to its title screen (Elite logo, Torus/Firebird artwork). Not driven into flight. |
| Chuckie Egg | **Works (caveat)** | Verified 2026-08-08: boots to the title and high-score table. Not driven into a game. |
| Horace Goes Skiing | **Works (caveat)** | Verified 2026-08-08: boots to its title screen. Not driven further. |
| Jet Pac (Interface 2 cartridge) | **Works (caveat)** | Verified 2026-08-08 from the snapshot release, NOT through the cartridge slot (no `.rom` image to hand): boots to its game-selection menu. The IF2 path itself remains covered only by `if2_test.go`. |
| Pssst (Interface 2 cartridge) | **Works (caveat)** | Verified 2026-08-08 from the snapshot release, NOT through the cartridge slot: boots to its game-selection menu. Same IF2 caveat as Jet Pac. |

## 128K / +2 titles

Titles that use the AY-3-8912 sound chip and/or the extended 128K
memory map. Foundation tests confirm 128K paging works.

| Title | Status | Notes |
|---|---|---|
| Robocop (Ocean) | **Boots** | Verified 2026-08-11 by automated screening: rendered its title screen (13996 px, 10 colours). Input not driven. |
| Renegade | **Works (caveat)** | Verified 2026-08-08 on the 128K: loads from `.tap` through the 128 Tape Loader and renders its title screen. Not driven into gameplay, so the verdict stops at "loads and titles". |
| Target: Renegade | **Boots** | Verified 2026-08-11 by automated screening: rendered its title screen (31712 px, 13 colours). Input not driven. |
| R-Type (Spectrum port) | **Boots** | Verified 2026-08-11 by automated screening: rendered its title screen (3724 px, 6 colours). Input not driven. |
| Lemmings (Spectrum port) | **Boots** | Verified 2026-08-11 by automated screening: rendered its title screen (14758 px, 6 colours). Input not driven. |
| Where Time Stood Still | **Boots** | Verified 2026-08-11 by automated screening: rendered its title screen (4981 px, 4 colours). Input not driven. |
| Head over Heels | **Works (caveat)** | Verified 2026-08-08 on the 48K release: accepts input at the options menu and progresses into the Blacktooth Empire world-select. Not driven into play. |
| Last Ninja 2 | **Boots** | Verified 2026-08-11 by automated screening: rendered its title screen (10127 px, 10 colours, animating). Input not driven. |
| Match Day II | **Works (caveat)** | Verified 2026-08-08 on the 128K: accepts input and navigates joystick-select through to the main menu. Not driven into a match. |
| The Way of the Tiger | **Boots** | Verified 2026-08-11 by automated screening: rendered its title screen (8411 px, 11 colours). Input not driven. |

## +3 / +2A disk titles

Titles distributed on +3 floppy disk. Foundation: `plus3fdc`
DSK / EDSK / UDI round-trip tests prove the disk container code is
symmetric, but no individual +3 title has been loaded and run.

| Title | Status | Notes |
|---|---|---|
| Cobra (Ocean, 1986) — +3 disk | Parses cleanly | 194816-byte CPCEMU DSK, 40 tracks, 1 side. Round-trips through `plus3fdc.ParseDiskImage` without errors. Not yet run to gameplay end-to-end. |
| Lemmings (+3 disk reissue) | Untested | Not screened: no `.dsk` to hand. |
| Where Time Stood Still (+3 disk reissue) | **Boots (responds)** | Screened 2026-08-12 from the `.dsk`: 18432 px, 2 colours, answered a keypress. |
| Driller (+3 disk reissue) | **Works (caveat)** | Verified 2026-08-08 on the 48K tape release, not the +3 disk: boots to the control-options screen with the cockpit HUD rendered. |
| Total Eclipse (+3 disk reissue) | **Works (caveat)** | Verified 2026-08-08 on the 48K tape release, not the +3 disk: boots to the control-options screen with the full border artwork. |

If you have a +3 DSK image that isn't listed and you want to add it,
follow the protocol at the bottom of this document.

## Demoscene

Demoscene productions exercise timing-sensitive features —
mid-frame border palette switches, AY register tricks, contention
patterns, multi-loader tape rituals — that the test harness doesn't
cover. Visual + audio inspection is the only verification.

A general note rather than specific titles: zx_go's mid-frame border
tracking (per-scanline, see `pkg/ula`) and AY-3-8912 register
semantics should handle typical demo techniques, but no specific
demo has been run through the full pipeline yet. Spectrum demos worth
trying as stress tests are anything that uses a multicolour technique
(8×1 attribute blocks via raster-synced palette writes), or anything
labelled "intro" or "megademo" from a known group (Triebkraft, Raww
Arse, Mayhem, Lyra) — these are the productions most likely to
expose timing imperfections.

## Spectrum Next titles

The Next cold-boots NextZXOS end-to-end — splash → firmware → welcome
→ menu/Browser/NextBASIC — see [docs/spectrum-next.md](spectrum-next.md).
The individual hardware blocks (Layer 2 incl. hi-res, the sprite
engine, tilemap, Copper, the NextReg file, the 8K MMU) are tested
against the FPGA VHDL.

**Running arbitrary `.NEX` games is the newest and least-finished
part of zx_go, and the most likely place to hit a bug.** `.nex`
titles are launched through NextZXOS's own loader, so OS-dependent
games run as on hardware — but per-title behaviour varies widely.
The honest state, as verified by a contributor:

| Title | Status | Notes |
|---|---|---|
| Sonic the Hedgehog | Works (caveat) | Renders level/scroll/sprite/HUD and is controllable (arrows + Right-Alt/Ctrl). Residual: a few HUD icons in the top-right diverge from hardware (a game-loop/interrupt-timing detail, not a render bug). |
| Nextoid | Works (caveat) | Bat/ball/HUD render and the game is drivable ('S' then SPACE). A load-time reset-to-Welcome (Copper byte-pairing) is fixed. |
| NextBASIC Invaders | **Works** | Verified 2026-08-12: loads from the Command Line and autostarts to its control-selection screen; started at difficulty 0 and played for ~48 seconds of emulated time with the full invader formation, bases and shots rendering. **No `Integer out of range`.** The earlier entry recorded a fault whose two root causes were fixed in later releases without this record being revisited. |
| Baggers in Space (Stonechat Games) | Untested | Public `.nex` distribution; uses Layer 2 + sprites; foundation: `TestModelNextLayer2VisibleEndToEnd` Not screened: no copy to hand. |
| Warhawk | **Works** | Verified 2026-08-11 via the genuine NextZXOS NEXLOAD path (`TestNexloadOSGamesIfPresent`, cmd/zx_go): launches and renders, PC=0x9071. It calls NextZXOS at runtime, so it cannot be loaded by bank injection — the game's banks overwrite the ones the OS keeps its screen and workspace in. An earlier entry here recorded it as a Known issue purely because the automated screening used that unsuitable path. |

If a Next game fails for you, **that is expected at this stage** —
please file it (with what you see vs. real hardware / a stable
emulator) so it can be fixed. The Next title scene is small enough
that any contributor running public `.nex` releases from specnext.com
or itch.io should add their own rows above as they verify them.
**No fabrications** — only list titles you've actually run, and
mark anything unverified as Untested.

## Verifying a title yourself

The manifest is mostly "Untested" because verification needs the actual
software, which is not redistributable and so is not vendored. Nothing here
should be marked Works on the strength of unit tests alone.

The quickest route is `pkg/testharness`, which boots a machine headless and
renders a frame you can look at:

```go
h, _ := testharness.New(roms.Model48K)
h.LoadSnapshot("your.z80")   // or h.LoadTAP("your.tap")
h.RunFrames(500)
png.Encode(f, h.ULA().Render())
```

`h.TapKey` / `h.TapKeyShift` drive the keyboard, which is what turns "it
rendered something" into a real verdict. On the 128K a tape needs the Tape
Loader selected first (`h.TapKey("Return")` at the menu); on the 48K it needs
`LOAD ""` typed.

When you add a verdict, say what you actually saw and stop there — "loads and
titles" is a useful, honest entry, and more valuable than an optimistic
"Works".

## Tape format edge cases

The `pkg/ula` tape pipeline supports both TAP (load + save) and TZX.
On load it plays the data blocks (0x10 / 0x11 / 0x14), the signal-only
blocks (0x12 pure tone, 0x13 pulse sequence, 0x15 direct recording,
0x20 pause) and honours the flow-control blocks (0x20-with-zero and
0x2A stops, 0x23 jump, 0x24 / 0x25 counted loops, 0x2B signal level).
It also decodes the two stream blocks: **0x18 (CSW recording)**, both
RLE and Z-RLE, and **0x19 (generalised data block)**, including its
symbol alphabets, run-length pilot stream and packed data stream.
Metadata blocks (group, text, archive info, hardware type, custom) are
parsed and skipped. A malformed stream block is skipped with a warning
rather than failing the load. TZX save covers 0x10 / 0x11 / 0x14 only.

Edge cases worth verifying manually:

- **Speedlock / Alkatraz loaders**: turbo blocks with non-standard pilot
  lengths. Format support is present; correct decoding of every
  variant is not exhaustively tested.
- **Multi-side tapes**: side-A side-B "flip the tape" sequences in
  adventure games (Lords of Midnight Vol 1, etc.). Stop Tape from the
  menu and load the next side.
- **Multi-load games (lots of action games)**: ensure the fast tape
  trap doesn't interfere with multi-block loaders; turn it off if so.

## Joystick configurations

Verified by the keymap unit tests (`pkg/keyboard/keyboard_test.go`).
Picking the right joystick for a given game is essential:

- **Kempston** (port 0x1F) — most arcade games of the late 80s
- **Sinclair 1** (keys 1-5) — Sinclair Interface 2 left side; games
  with explicit "Sinclair joystick" support
- **Sinclair 2** (keys 6-0) — right side
- **Cursor / Protek** (keys 5/6/7/8/0) — fewer games; check title
  documentation

## How class evidence works

When a title isn't on the list, you can still predict whether it'll
run by figuring out which class it belongs to:

1. Is it pure 48K BASIC? → `TestNewBoots48K` covers it.
2. Does it use the AY sound chip? → Multi-AY tests cover register
   semantics; if your title uses AY through standard ports, the
   sound will play.
3. Does it use IF1 / Microdrive? → `TestIF1CATWorks` covers the boot
   path.
4. Does it use DISCiPLE / +D? → `TestDISCiPLEBootsIntoGDOS` covers
   the boot path.
5. Does it use +3 disks? → `TestPlus3FDCDiskRoundtrip` covers disk
   I/O.
6. Does it depend on cycle-exact contention (e.g. multicolour
   scanline tricks)? → Open in a hex editor, check whether it does
   raster waiting via T-state loops; if yes, expect potential
   wobble.
7. Is it a Next .NEX file? → Banks 0-7 fully supported; 8-111
   silently skipped today.

## Spectrum Next SD games (NEXLOAD)

Every `.nex` on the SD card, driven through the genuine NextZXOS
`.nexload` dot command by `TestNexloadSDGames` (cmd/zx_go). That is the
only path that can host a title which calls the OS at runtime, so it is
the only honest way to judge Next game compatibility — bank injection
clobbers the OS's workspace banks and would report working games as
broken.

**9 of 10 launch and render.** Confirmed by eye for Halls of The Things
(full menu) and Night-Knight (in-game, sprite and platform drawn).

This is the Next half of the corpus. The classic titles screened
elsewhere in this document say nothing about Next-only hardware.

| Title | Status | Notes |
|---|---|---|
| AngryBloaters | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x9c58). Input not driven. |
| Halls of The Things | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x99d9). Input not driven. |
| LordsOfMidnight | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x615d). Input not driven. |
| Nextoid | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0xb46b). Input not driven. |
| Night-Knight | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x1f3e). Input not driven. |
| Revival Survival | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x7aca). Input not driven. |
| Santa's Pressie | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x9819). Input not driven. |
| Sonic | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x3404). Input not driven. |
| Orb | **Boots** | NextBASIC. Screened 2026-08-12 via the Command Line (`.cd` then `LOAD`): title and instructions screen render (9775 px, 38 colours). |
| baSnake | **Boots** | NextBASIC. Screened 2026-08-12: full instructions screen renders (4742 px, 6 colours). |
| NEXTipede | **Boots** | Next tape. Screened 2026-08-12: loads and runs its DEMO MODE attract sequence — mushrooms, centipede and spider all animating. |
| Pogie | Untested | Not screenable from the files present: the directory holds only assets (`.spr`, `.cpa`, `.map`) plus a 49179-byte `.snx`, which is a 48K snapshot and cannot hold a Next game's banked state. No standalone loadable program. |
| THEH | Untested | Not screenable from the files present, as Pogie: assets plus a 48K-sized `.snx`. Loading that snapshot alone gives a black screen. |
| TX-1696 | **Works** | Solved 2026-08-12. **Not an emulator fault: the game's assets must sit at the SD card root.** It opens `C:/common/ayfx3.afb` and `C:/common/highScore.bin` — absolute paths at the root of C: — while this card shipped them only inside `/games/Next/TX-1696/common/`. The open fails, the game does not check carry and uses the errno as a file handle, so every read then fails and it retries for ever with a blank screen. Copy the game's `common/` folder to the card root and it runs: title screen, PLAY/CREDITS/SETTINGS menu and ship all render. |
| Warhawk | **Boots** | Screened 2026-08-12 via the genuine NextZXOS NEXLOAD path: launches and renders (PC=0x9071). Input not driven. |

## Automated screening

Titles screened headlessly by `TestScreenLocalTitles` (pkg/testharness).
Each is loaded, run, and measured in the **display window with the
border cropped** — cropping matters, because a tape loader fills the
border with moving stripes while the display area stays empty, so an
uncropped measurement counts a loader as content.

Still screens are then probed: keys are sent and the display watched
for a material change, against a **no-key control** so a screen that
animates on its own is not credited to the keypress.

**A +3 disk is started explicitly.** The machine powers on into its ROM
menu (Loader / +3 BASIC / Calculator / 48 BASIC) and stays there until
ENTER selects "Loader". An earlier run of this table did not send that
key, so 61 disk entries recorded the ROM's own menu — a drawn screen
whose highlight moves under a probe keypress — rather than the game.
Every one of those verdicts was wrong and has been re-measured.

**What these verdicts do and do not mean.** *Boots* means the guest's
own code ran and drew its screen. *Boots (responds)* adds that the
screen answered a keypress, so the title is waiting for input rather
than hung. Neither means the title is playable: what the keypress did
is unknown, and no gameplay is verified.

The titles themselves are commercial and are not in this repository.
The test reads a path list from `pkg/testharness/testdata/local_titles.tsv`,
which is gitignored, and skips entirely when it is absent. It never
fails the build: the collection is uncontrolled, and some titles
legitimately will not load.

**Of 113 titles screened, 98 rendered content (27 of them
animating and 57 answering a keypress), 8 would not load, and the
rest drew almost nothing.**

The 8 that will not load share one cause: a copy-protection track
whose declared sector sizes exceed what a physical double-density track
holds (an N=6 code claims 8192 bytes against ~6250). That layout is not
modelled; the loader says so explicitly rather than failing opaquely.

Verdicts confirmed by eye against captured frames: Elite, Renegade,
Pssst, R-Type, Last Ninja 2, Where Time Stood Still, RoboCop, The Way of
the Tiger, Lemmings, Target: Renegade, Cybernoid and 007 Licence To Kill.

| Title | Status | Notes |
|---|---|---|
| 007 - Licence To Kill (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 2581 px, 6 colours, answered a keypress. |
| 3D Construction Kit (+3 disk) | **Boots** | Screened 2026-08-12: 1708 px, 2 colours. |
| 3D Game Maker (+3 disk) | **Boots** | Screened 2026-08-12: 1126 px, 3 colours, animating. |
| 3D Grand Prix Championship (+3 disk) | Known issue | Screened 2026-08-12: loads but renders almost nothing (313 px, 2 colours). |
| ACE 2 - The Ultimate Head to Head Conflict (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 1924 px, 3 colours, answered a keypress. |
| Action Fighter (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 1986 px, 5 colours, answered a keypress. |
| Action Force (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 3261 px, 2 colours, answered a keypress. |
| Action Force 2 (+3 disk) | **Boots** | Screened 2026-08-12: 1686 px, 3 colours, animating. |
| Adidas Championship Tie-Break (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Afterburner (+3 disk) | **Boots** | Screened 2026-08-12: 18432 px, 2 colours. |
| Airborne Ranger (+3 disk) | **Boots** | Screened 2026-08-12: 18432 px, 2 colours. |
| Alien 8 | **Boots (responds)** | Screened 2026-08-12: 16388 px, 6 colours, answered a keypress. |
| Alien Storm (+3 disk) | **Boots** | Screened 2026-08-12: 3034 px, 6 colours, animating. |
| Alien Syndrome (+3 disk) | Known issue | Screened 2026-08-12: loads but renders almost nothing (498 px, 2 colours). |
| APB - All Points Bulletin (+3 disk) | **Boots** | Screened 2026-08-12: 32931 px, 9 colours, animating. |
| Arkanoid 2 - Revenge of Doh (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 2521 px, 2 colours, answered a keypress. |
| Artura (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 2616 px, 7 colours, answered a keypress. |
| Aspar GP Master (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 21119 px, 14 colours, answered a keypress. |
| ATF - Advanced Tactical Fighter (+3 disk) | **Boots** | Screened 2026-08-12: 31165 px, 9 colours. |
| Auf Wiedersehen Monty | **Boots (responds)** | Screened 2026-08-12: 10204 px, 7 colours, answered a keypress. |
| Autocrash (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 1157 px, 2 colours, answered a keypress. |
| Back to the Future Part II (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Back to the Future Part III (+3 disk) | **Boots** | Screened 2026-08-12: 13747 px, 8 colours, animating. |
| Badlands (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 17648 px, 5 colours, answered a keypress. |
| Barbarian II - The Dungeon of Drax (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Batman | **Boots (responds)** | Screened 2026-08-12: 4904 px, 4 colours, answered a keypress. |
| Batman - The Caped Crusader (+3 disk) | **Boots** | Screened 2026-08-12: 18432 px, 2 colours. |
| Batman - The Movie (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 1208 px, 2 colours, answered a keypress. |
| Bestial Warrior (+3 disk) | **Boots** | Screened 2026-08-12: 7515 px, 8 colours, animating. |
| Beverly Hills Cop (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 29113 px, 10 colours, answered a keypress. |
| Beyond the Ice Palace (+3 disk) | **Boots** | Screened 2026-08-12: 18432 px, 2 colours. |
| Black Lamp (+3 disk) | **Boots** | Screened 2026-08-12: 21035 px, 7 colours, animating. |
| Blasteroids (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 1425 px, 2 colours, answered a keypress. |
| Bloodwych (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 1343 px, 4 colours, answered a keypress. |
| Bomb Jack | **Boots** | Screened 2026-08-12: 14798 px, 7 colours, animating. |
| Bonanza Bros (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Brian Clough's Football Fortunes (+3 disk) | **Boots** | Screened 2026-08-12: 8171 px, 6 colours. |
| Bubble Bobble | **Boots** | Screened 2026-08-12: 12020 px, 4 colours, animating. |
| Bubble Bobble (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 12158 px, 8 colours, answered a keypress. |
| Bubble Buster (+3 disk) | **Boots** | Screened 2026-08-12: 9333 px, 5 colours, animating. |
| Buffalo Bill's Wild West Show (+3 disk) | **Boots** | Screened 2026-08-12: 13688 px, 9 colours, animating. |
| Buggy Boy (+3 disk) | **Boots** | Screened 2026-08-12: 18432 px, 2 colours. |
| Buggy Ranger (+3 disk) | **Boots** | Screened 2026-08-12: 16904 px, 11 colours, animating. |
| Butcher Hill (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 5973 px, 7 colours, answered a keypress. |
| By Fair Means...or Foul (+3 disk) | **Boots** | Screened 2026-08-12: 27647 px, 8 colours, animating. |
| Cabal (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 31939 px, 15 colours, answered a keypress. |
| California Games (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 5973 px, 7 colours, answered a keypress. |
| Cannon Bubble (+3 disk) | **Boots** | Screened 2026-08-12: 15102 px, 14 colours, animating. |
| Capitan Sevilla (+3 disk) | Known issue | Screened 2026-08-12: loads but renders almost nothing (845 px, 2 colours). |
| Captain Blood (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Captain Planet (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Carlos Sainz (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 6307 px, 7 colours, answered a keypress. |
| Castle Master (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 16266 px, 9 colours, answered a keypress. |
| Chain Reaction (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 19893 px, 5 colours, answered a keypress. |
| Championship Run (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 17784 px, 4 colours, answered a keypress. |
| Chase H.Q | **Boots (responds)** | Screened 2026-08-12: 5360 px, 4 colours, answered a keypress. |
| Chase H.Q. (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 11930 px, 12 colours, answered a keypress. |
| Chase H.Q. II - Special Criminal Investigations (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Choy-Lee-Fut Kung-Fu Warrior (+3 disk) | **Boots** | Screened 2026-08-12: 12464 px, 12 colours, animating. |
| Chuckie Egg | **Boots** | Screened 2026-08-12: 25143 px, 7 colours, animating. |
| Circus Games (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 25933 px, 12 colours, answered a keypress. |
| Colossus 4 Bridge (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 6327 px, 4 colours, answered a keypress. |
| Colossus 4 Chess (+3 disk) | **Boots** | Screened 2026-08-12: 6066 px, 2 colours, animating. |
| Comando Quatro (+3 disk) | Known issue | Screened 2026-08-12: loads but renders almost nothing (0 px, 1 colours). |
| Comando Tracer (+3 disk) | **Boots** | Screened 2026-08-12: 2877 px, 2 colours, animating. |
| Commando | **Boots** | Screened 2026-08-12: 21972 px, 7 colours, animating. |
| Continental Circus (+3 disk) | Known issue | Screened 2026-08-12: image will not load — copy-protection track layout, see below. |
| Cookie | **Boots (responds)** | Screened 2026-08-12: 3534 px, 3 colours, answered a keypress. |
| Coursemaster v3.88 (+3 disk) | **Boots (responds)** | Screened 2026-08-12: 5472 px, 3 colours, answered a keypress. |
| Cybernoid | **Boots (responds)** | Screened 2026-08-12: 8395 px, 10 colours, answered a keypress. |
| Dan Dare | **Boots** | Screened 2026-08-12: 4559 px, 8 colours. |
| Deathchase | **Boots** | Screened 2026-08-12: 30189 px, 10 colours, animating. |
| Double Dragon | **Boots (responds)** | Screened 2026-08-12: 6997 px, 4 colours, answered a keypress. |
| Elite | **Boots** | Screened 2026-08-12: 7412 px, 3 colours, animating. |
| Exolon | **Boots (responds)** | Screened 2026-08-12: 29562 px, 11 colours, answered a keypress. |
| Fairlight | **Boots (responds)** | Screened 2026-08-12: 22996 px, 11 colours, answered a keypress. |
| Gauntlet | **Boots (responds)** | Screened 2026-08-12: 9213 px, 12 colours, answered a keypress. |
| Ghosts 'n Goblins | **Boots** | Screened 2026-08-12: 21901 px, 7 colours, animating. |
| Green Beret | **Boots (responds)** | Screened 2026-08-12: 14333 px, 13 colours, answered a keypress. |
| Head Over Heels | **Boots (responds)** | Screened 2026-08-12: 8195 px, 4 colours, answered a keypress. |
| Highway Encounter | **Boots** | Screened 2026-08-12: 17104 px, 7 colours, animating. |
| Lunar Jetman | **Boots (responds)** | Screened 2026-08-12: 3977 px, 5 colours, answered a keypress. |
| Match Day | **Boots (responds)** | Screened 2026-08-12: 4084 px, 4 colours, answered a keypress. |
| Midnight Resistance | **Boots (responds)** | Screened 2026-08-12: 17259 px, 15 colours, answered a keypress. |
| Movie | **Boots** | Screened 2026-08-12: 17641 px, 13 colours. |
| Myth | **Boots** | Screened 2026-08-12: 5032 px, 7 colours, animating. |
| Nebulus | **Boots (responds)** | Screened 2026-08-12: 4076 px, 8 colours, answered a keypress. |
| NEXTipede (Next tape) | **Boots** | Screened 2026-08-12: 1719 px, 2 colours, animating. |
| Operation Wolf | **Boots (responds)** | Screened 2026-08-12: 4286 px, 4 colours, answered a keypress. |
| Pssst | **Boots (responds)** | Screened 2026-08-12: 3313 px, 3 colours, answered a keypress. |
| Rainbow Islands | **Boots (responds)** | Screened 2026-08-12: 26765 px, 14 colours, answered a keypress. |
| Renegade | **Boots (responds)** | Screened 2026-08-12: 2524 px, 2 colours, answered a keypress. |
| Rex | **Boots (responds)** | Screened 2026-08-12: 6921 px, 11 colours, answered a keypress. |
| Saboteur | **Boots (responds)** | Screened 2026-08-12: 29650 px, 9 colours, answered a keypress. |
| Sabre Wulf | **Boots** | Screened 2026-08-12: 12580 px, 9 colours. |
| Starquake | **Boots (responds)** | Screened 2026-08-12: 12701 px, 5 colours, answered a keypress. |
| Trap Door | **Boots (responds)** | Screened 2026-08-12: 19619 px, 11 colours, answered a keypress. |
| Turrican | **Boots (responds)** | Screened 2026-08-12: 5739 px, 9 colours, answered a keypress. |
| Uridium | **Boots** | Screened 2026-08-12: 6323 px, 6 colours, animating. |
| Where Time Stood Still (+3 disk) | **Boots** | Screened 2026-08-12: 18432 px, 2 colours. |
| Where Time Stood Still (+3 disk)#01 | **Boots** | Screened 2026-08-12: 18432 px, 2 colours. |
| Wizball | **Boots (responds)** | Screened 2026-08-12: 10087 px, 5 colours, answered a keypress. |
| Zynaps | **Boots** | Screened 2026-08-12: 5092 px, 6 colours. |

## How to add a title

When you load a title and want to record the result:

1. Try the title on the appropriate model (most 48K games stay on
   48K; +3 disk games need +3; etc.).
2. Note any peripheral state required (DISCiPLE on/off, joystick
   selection).
3. Run far enough to confirm the title is functional — not just
   "shows the loading screen" but "playable".
4. Add a row to the appropriate table:
   - **Works** for clean runs.
   - **Works (caveat)** for visible-but-cosmetic issues (audio
     pitch slightly off, etc.).
   - **Known issue** for crashes / corruption / freezes — file a
     GitHub issue first and link it.
5. Submit a PR.

This manifest is the cheapest credibility lever in the project: a
title list with verified statuses is more useful than any number
of internal benchmarks for telling a prospective user "yes, this
works for what you want to do".

## Automated compatibility corpus

`TestCompatibilityCorpus` (in `cmd/zx_go`) turns this manifest into an
automated regression guard: it loads a real title headless, drives it to
its title/menu screen, and asserts the rendered screen matches a recorded
golden hash over a settle window. This catches "a real game silently
stopped loading" — the class of bug a mechanism-level unit test passes
straight through (e.g. the Renegade-128K tape failure).

Game files are copyrighted and **not** committed. Point the corpus at a
folder holding them (titles whose files are absent skip, so CI stays
green):

```bash
# Run the corpus against your own game folder
ZX_GO_CORPUS=/path/to/games go test ./cmd/zx_go/ -run TestCompatibilityCorpus -v

# Record/refresh a title's golden screen hash (new title, or intended
# rendering change)
ZX_GO_CORPUS_UPDATE=1 ZX_GO_CORPUS=/path/to/games \
  go test ./cmd/zx_go/ -run TestCompatibilityCorpus -v
```

To add a title, append a `corpusTitle` to the table in `corpus_test.go`
(file, model, load type, key schedule, frame count) and record its golden.

## Loader fuzzing

The file parsers are fuzzed (Go native fuzzing) so corrupt or hostile
input is rejected, never crashes:

```bash
go test ./pkg/snapshot/ -run x -fuzz FuzzLoadBytes      -fuzztime 60s  # .sna/.z80/.szx
go test ./pkg/betadisk/ -run x -fuzz FuzzLoadImage      -fuzztime 60s  # .trd
go test ./pkg/ula/      -run x -fuzz FuzzLoadTAP        -fuzztime 60s  # .tap (+ FuzzLoadTZX)
go test ./pkg/plus3fdc/ -run x -fuzz FuzzParseDiskImage -fuzztime 60s  # .dsk/.edsk/.udi/.sad
go test ./pkg/rzx/      -run x -fuzz FuzzRead           -fuzztime 60s  # .rzx
```

The seed corpora run as part of the normal `go test ./...`.

`FuzzParseDiskImage` also walks every track of a successfully parsed image
for sectors, because that is what the FDC does on each read — parsing
cleanly is not enough if the resulting track structure then trips the
walker.

## Guest-code stress

Parsers are only half the untrusted surface: the other half is whatever an
emulated program puts on the bus. `TestGuestPortStress` (`pkg/testharness`)
runs randomised guest programs through the real CPU on all seven models —
`OUT`/`IN` across the whole 16-bit port space, Z80N `NEXTREG` writes over
the whole register space, reads and writes across the whole address space —
then renders, so the video stack sees the registers the stream left behind.

Any value a program can write has to be survivable. There is no `recover()`
in the emulator, by design: a panic is a modelling failure and should be
loud rather than swallowed. That makes this test a gate, not a nicety — it
is what caught the `$DFFD` extended-RAM-bank fault in v1.4.1, a crash three
guest instructions could reach. It runs in seconds, so it stays in CI.
