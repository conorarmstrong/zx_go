# zx_go — a ZX Spectrum emulator written in Go

zx_go is a faithful emulator for every classic Sinclair ZX Spectrum (48K, 128K, +2, +2A, +3) **and the Spectrum Next** — which boots real NextZXOS end-to-end through the authentic hardware chain. It supports every common file format (snapshots, tapes, disks, microdrive cartridges, `.NEX`, RZX recordings), three different debuggers (a visual GUI, a scriptable telnet server, and headless trace instrumentation — the visual and telnet debuggers share one live backend), and a wide range of period-accurate peripherals.

The 48K was the author's first computer; this project was written as a Go learning exercise that turned into a serious emulator.

| ZX Spectrum +3 | NextZXOS welcome | NextZXOS menu |
| :---: | :---: | :---: |
| ![+3 menu](screenshot_plus3.png) | ![NextZXOS welcome](next_welcome.png) | ![NextZXOS menu](nextzxos_menu.png) |

---

## Contents

- [Quick start](#quick-start) — five minutes from clone to playing a game
- [Installation](#installation)
 - [Pre-built binaries](#pre-built-binaries)
 - [Building from source](#building-from-source)
- [What works](#what-works) — supported models and file formats
- [Running classic Spectrum modes](#running-classic-spectrum-modes)
- [Running the Spectrum Next](#running-the-spectrum-next-modelnext)
 - [What you need (and the easy way to get it)](#what-you-need-and-the-easy-way-to-get-it)
 - [Step-by-step: installing the Next ROMs by hand](#step-by-step-installing-the-next-roms-by-hand)
 - [Step-by-step: pointing the emulator at an SD card](#step-by-step-pointing-the-emulator-at-an-sd-card)
 - [Verifying the Next is configured](#verifying-the-next-is-configured)
 - [Current status of NextZXOS boot](#current-status-of-nextzxos-boot)
- [Menu reference](#menu-reference) — every menu, every item
- [Keyboard, joystick and mouse](#keyboard-joystick-and-mouse)
- [The visual debugger](#the-visual-debugger)
- [The telnet debugger](#the-telnet-debugger)
- [Headless mode and debug instrumentation](#headless-mode-and-debug-instrumentation)
- [Project structure](#project-structure)
- [License](#license)

---

## Quick start

If you just want to load a snapshot and play a game on a 48K Spectrum:

```bash
# Build (requires Go 1.25+ and a C toolchain for Fyne)
go build -o bin/zx_go./cmd/zx_go

# Run
./bin/zx_go
```

`File → Load Snapshot…`, pick a `.sna` / `.z80` / `.szx` file. Done.

For tape: `File → Load Tape (TAP)…` or `Load Tape (TZX)…`. Once loaded, type `LOAD ""` in 48K BASIC and press Enter to begin loading (or `J` then `"` `"` Enter using single-key BASIC entry).

---

## Installation

### Pre-built binaries

Pre-built binaries for macOS, Windows, and Linux are on the [Releases](https://github.com/conorarmstrong/zx_go/releases) page.

| Platform | Download |
| --- | --- |
| macOS (Apple Silicon) | `zx_go-macos-arm64.tar.gz` |
| macOS (Intel) | `zx_go-macos-amd64.tar.gz` |
| Windows | `zx_go-windows-amd64.exe.zip` |
| Linux | `zx_go-linux-amd64.tar.gz` |

On macOS/Linux, extract and run:

```bash
tar xzf zx_go-macos-arm64.tar.gz
./zx_go-macos-arm64
```

On Windows, unzip and double-click `zx_go-windows-amd64.exe`.

The classic ROMs (48K, 128K, +2, +2A, +3, plus the peripheral ROMs for DISCiPLE / Multiface / Interface 1) are **embedded in the binary** — no separate ROM install needed for those modes.

### Building from source

You need:
- Go 1.25 or newer
- A C compiler (`cc`/`gcc`/`clang`) — Fyne uses cgo for the OS windowing layer
- OpenGL libraries (system-provided on macOS and most Linux distros; usually trivial on Windows)

```bash
git clone https://github.com/conorarmstrong/zx_go
cd zx_go
go build -o bin/zx_go./cmd/zx_go
./bin/zx_go
```

Run the tests:

```bash
go test./...
```

There are forty-plus test packages; the full suite takes roughly 90 seconds on a modern laptop.

---

## What works

### Models
- **ZX Spectrum 48K, 128K, +2, +2A, +3** — fully working and interactive. Cycle-accurate timing, memory contention, port contention, +3 / +2A 4-ROM paging, all five tape formats, all six disk formats.
- **ZX Spectrum Next** — **boots real NextZXOS end-to-end through the authentic chain**: FPGA bootrom → TBBLUE firmware splash → NextZXOS welcome → main menu — and the menu items work: the **Browser** opens the SD card's `C:/` listing, **NextBASIC** runs interactive programs (type, `RUN`, `BREAK`), the firmware configuration menu (SPACE at the splash) boots whichever machine personality you pick. Every Next hardware extension is wired: Z80N CPU extensions, NextRegs, 8K MMU, divMMC (128 KB), esxDOS, Layer 2, palette, sprites, Copper, zxnDMA, DAC, i2c RTC, 28 MHz CPU mode. **`.NEX` games load and run** via `File → Open File…`. See [Current status of NextZXOS boot](#current-status-of-nextzxos-boot).

### File formats supported

| Format | Extension | Load | Save | Notes |
| --- | --- | --- | --- | --- |
| Snapshots | `.sna` / `.z80` / `.szx` | ✓ | ✓ | Full 48K + 128K support |
| Tape | `.tap` / `.tzx` | ✓ | ✓ | TZX save covers block types 0x10 / 0x11 / 0x14 |
| Disk (+3) | `.dsk` / `.edsk` | ✓ | ✓ (as `.dsk`) | EDSK (CPCEMU) handles weak sectors |
| Disk (other) | `.udi` / `.mgt` / `.img` / `.trd` / `.sad` / `.d40` / `.d80` | ✓ | — | Full format coverage |
| Microdrive | `.mdr` | ✓ | ✓ | Standard Sinclair microdrive cartridge format |
| Interface 2 cartridge | `.rom` | ✓ | — | 16 KB, 48K-only |
| RZX recordings | `.rzx` | ✓ | ✓ | Per-frame instruction count + IN-byte stream |
| Audio capture | `.wav` | — | ✓ | Record emulator output |
| Screenshot | `.png` | — | ✓ | Save current frame |
| Spectrum Next | `.nex` | ✓ | — | v1.2 loader: banks, palette, Copper, entry delay |

### Peripherals
- **+3 FDC** — NEC μPD765A; two drives, READ/WRITE/FORMAT/READ ID/READ DIAGNOSTIC/SCAN/SEEK
- **Interface 1 + Microdrive** — 8 daisy-chained slots, GAP/SYNC formatting, embedded v2 ROM
- **Interface 2** — cartridge slot, BASIC ROM override
- **DISCiPLE** — MGT disk interface with GDOS ROM and full port-level emulation
- **Multiface 1 / 128 / 3** — NMI button (F12) and ROM paging
- **Kempston mouse** — 3 ports (X / Y / buttons)
- **ZX Printer** — Sinclair's 1-bit thermal printer; PNG export
- **Joysticks** — Kempston (port `$1F`), Sinclair Interface 2 left (1-5) and right (6-0), Cursor / Protek

### Sound
- **Beeper** — ULA speaker bit through the host audio system
- **AY-3-8912** — on 128K and later; correct port decoding (`$BFFD` / `$FFFD`)
- **Spectrum Next DAC** — 4-channel DAC bank

---

## Running classic Spectrum modes

Everything classic just works. Pick your model from `Machine`:

- **48K** — the original. Has the Interface 1 peripheral option for microdrives.
- **128K** — adds the AY sound chip and a 128 KB paged memory map.
- **+2** — same hardware as 128K, different shell ROM.
- **+2A** — adds the `$1FFD` port and 4-ROM paging.
- **+3** — adds the integrated FDC and disk support.

The 48K boots straight to `(C) 1982 Sinclair Research Ltd`. The 128K family boots into the menu screen. Use `Machine → Reboot` to cold-restart the current model.

---

## Running the Spectrum Next (ModelNext)

The Spectrum Next is a 2017-era successor designed by Sinclair / Henrique Olifiers / Victor Trucco / Fabio Belavenuto. It runs everything a 48K/128K/+3 runs, plus its own NextZXOS, plus modern `.NEX`-format games. zx_go's Spectrum Next implementation is the most active area of the codebase.

**Note**: the classic Spectrum modes (48K through +3) are fully usable and don't need anything beyond the bundled embedded ROMs. The instructions below are only for users who want to try the Next.

### What you need (and the easy way to get it)

**The easy way:** just pick **Machine → ZX Spectrum Next**. If the NextZXOS
ROMs aren't installed yet, zx_go offers to **download them for you** from the
official Spectrum Next distribution, installs them and the SD-card content, and
boots straight into NextZXOS. (The licensed ROMs are not bundled — see below —
so this one-time download is how you get them legally from the source.)

If you'd rather install by hand, here's what the Next uses:

| File | Size | Status | Where to get it |
| --- | --- | --- | --- |
| `enNextZX.rom` | 64 KB | **Required** | The NextZXOS OS ROM. Licensed (Amstrad/Sky) — not bundled. From `sn-complete-24.11.zip` in the official [Spectrum Next distribution](https://www.specnext.com/latestdistro/): `machines/next/enNextZX.rom`. |
| `enNxtmmc.rom` | 8 KB | Recommended | The divMMC / esxDOS ROM (licensed, not bundled). Same zip: `machines/next/enNxtmmc.rom`. Without it the SD-card / dot-command surface returns "no media". |
| `tbblue_loader.rom` | 8 KB | **Bundled** | The FPGA boot loader. This one is the **GPLv3 open-source** ZX Spectrum Next firmware (not a licensed ROM), so it ships *embedded in zx_go* — nothing to install. See `LICENSES/tbblue_loader-NOTICE.md`. |
| SD card contents | — | **Required** for SD-backed features | The contents of `sn-complete-24.11.zip` *are* the SD card. The on-demand download installs them for you; or point the emulator at the unzipped directory (see below). |

So only the **two licensed ROMs** are user-provided — and the download flow
fetches them for you. All paths are case-sensitive (lookup is by exact basename).

### Step-by-step: installing the Next ROMs by hand

> You usually don't need this — just pick **Machine → ZX Spectrum Next** and
> accept the download prompt. Use the manual steps below only if you already
> have the distro, want a specific version, or prefer to do it yourself.

1. Download `sn-complete-24.11.zip` from <https://www.specnext.com/latestdistro/>. (The version number may have moved on by the time you read this; any 24.x release works.)
2. Unzip it somewhere — call that location `~/SpecNextDistro/` for the rest of this guide.
3. Launch zx_go.
4. **File menu → "Install Next ROMs…"**. A file picker opens, filtered to `.rom` files.
5. Navigate to `~/SpecNextDistro/machines/next/` and double-click `enNextZX.rom`.
6. A confirmation dialog tells you:
 - `✓ Recognised: NextZXOS firmware (REQUIRED for ModelNext).`
 - The file's installed size, destination path, and SHA-256 digest.
7. Click OK.
8. Repeat steps 4-7 for `enNxtmmc.rom`. The confirmation will say:
 - `✓ Recognised: divMMC / esxDOS overlay (enables SD-card and dot-commands).`
9. `tbblue_loader.rom` is **embedded in the emulator** (the GPLv3 Next firmware loader), so you never need to install it — the cold-boot path works as soon as the two ROMs above are present. (Installing your own copy still overrides the embedded one if you want a different version.)

If you install a file that *isn't* one of those three filenames, you get a friendly warning telling you which names the emulator looks up. The install still proceeds in case you know what you're doing.

#### Where exactly does it install?

The destination is `roms/next/<basename>` — repo-local. The install
directory is resolved by `pkg/next/install/install.go::Path()` in
this order:

1. `$ZX_GO_NEXT_ROM_DIR` if set (test sandbox override).
2. `<repo-root>/roms/next` if running inside a Go module (= walks up
   from cwd looking for `go.mod`; lets the binary find the ROMs from
   any subdirectory of a checkout).
3. `<cwd>/roms/next` as a last resort.

The repo-local layout is intentional: `.gitignore` keeps the ROM
binaries out of source control, and a missing test-fixture override
at worst writes to `./roms/next/` inside the repo rather than
clobbering a developer's real user-config installation. An earlier
scheme used `os.UserConfigDir()` (= `~/Library/Application Support/`
on macOS, `$XDG_CONFIG_HOME/` on Linux, `%AppData%\` on Windows)
but was replaced after a test fixture missing its `RedirectConfig`
clobbered a real install once.

### Step-by-step: pointing the emulator at an SD card

The SD card is just a directory on your host machine. zx_go builds a bootable FAT32 image from it on the fly. To set the source:

1. **File menu → "Set Next SD Card Directory…"**.
2. A folder picker opens. Navigate to `~/SpecNextDistro/` (the **unzipped** distro root — the folder that contains `TBBLUE.FW`, `TBBLUE.TBU`, `machines/`, `nextzxos/`, etc.). Choose it.
3. A confirmation dialog says where the SD root is now pointing and reminds you to restart ModelNext.
4. **Machine → Spectrum Next** (or restart the emulator) — the next boot reads from your chosen directory.

Alternative — **using a raw.img/.mmc file**:

1. **File menu → "Set Next SD Card Image (.img/.mmc)…"**.
2. Pick the image file. Filtered to `.img` / `.mmc`.
3. The emulator now serves the file verbatim instead of building a FAT32 image from a directory.

This is the way to use any known-working pre-built Spectrum Next SD image you may have (typical extensions are `.img` or `.mmc`).

To clear either setting: **File menu → "Clear Next SD Card Setting"**. The emulator falls back to its default search (`<install-dir>/sd` or `roms/next/sd`).

The current SD setting is persisted in `<config-dir>/zx_go/config.json` as `next_sd_dir` or `next_sd_image`.

### Verifying the Next is configured

Switch to **Machine → Spectrum Next**. Watch the launch logs:

- ✓ `FPGA bootrom loaded size=8192` — `tbblue_loader.rom` was found.
- ✓ `SD card loaded from image path=…` *or* the absence of `next: no SD card root` warning — the SD source is wired.
- ✗ `ZX Spectrum Next distro ROM is not installed` — `enNextZX.rom` is missing; install it via the menu.

For a deeper sanity check, run headless with a state dump:

```bash
./bin/zx_go --next --headless --dump-state=300
```

This boots the Next, runs 300 frames, then prints the CPU registers, paging state, NextReg values, sysvars, and screen statistics. Useful for confirming the ROMs are correctly loaded without spinning up the GUI.

### Current status of NextZXOS boot

**It boots.** `./bin/zx_go --next` runs the authentic cold-boot chain with no
captured-state replay and no shortcuts:

1. **FPGA bootrom splash** (~5 s real time) — "sinclair ZX Spectrum Next",
   firmware/core version banner.
2. **NextZXOS welcome screen** (~10 s) — press SPACE to continue.
3. **NextZXOS main menu** — Browser / Command Line / NextBASIC / Calculator /
   Guide / More…, with the live clock from the emulated i2c RTC.
4. **Menu items launch**: ENTER on *Browser* opens the SD card's `C:/`
   listing; *NextBASIC* drops you into the editor where you can type a
   program, `RUN` it, and BREAK out of it; *128K BASIC* (under *More…*)
   brings up the Sinclair "128" menu.

Press SPACE at the splash instead and you get the **firmware configuration
menu**: pick any machine personality (Next, 48K, 128K, +2, +3, …) and it
boots that machine.

The boot needs the Next ROMs + an SD card source (see the install steps
above). Two SD options:

- **A card image** (`sd.img` in the install dir, or `ZX_GO_NEXT_SD_IMG=path`):
  any FAT32 NextZXOS card image works — including one made by `dd`-ing a real
  Next's card.
- **A host directory** (`roms/next/sd/` = the unzipped distro tree): the
  emulator builds a bootable FAT32 image from it in memory at startup, so you
  can boot straight out of the distro zip with no image file at all.

Guest writes to a mounted image stay in RAM by default (a crashed session
can never corrupt your only card). Opt into persistence with
`--sd-writeback` — at exit the image file is rewritten and the previous
version kept as `.bak`.

#### Important: ROM versions must match the SD card distro

The OS, divMMC ROM, and SD card all reference each other's version stamps.
If you install `enNextZX.rom` from one distro and point the SD card at a
directory built from a different distro, NextZXOS detects the mismatch and
traps with a "Version mismatch:" error. **Install Next ROMs from the SAME
distro as your SD card content.** The "Install Next ROMs…" dialog
cross-checks against the configured SD source and surfaces a "⚠️ VERSION
MISMATCH WARNING" with both files' SHA-256s when they differ.

#### Engineering history

The cold boot was brought up divergence-by-divergence against the Next's
open FPGA sources as the hardware oracle — every step verified
hardware-faithful, with no captured-state replay.


## Menu reference

### File

The File menu groups related actions into submenus (▸) to stay manageable:

| Item | What it does |
| --- | --- |
| Open File… | Universal opener — sniffs the file's magic and dispatches to the right loader (snapshots, tapes, disks, `.nex`, `.rzx`, microdrive). |
| Recent | Most-recently-loaded files, click to reopen. "Clear Recent Files" at the bottom. |
| **Snapshots & ROM** ▸ | Load ROM… (replace the active model's BASIC ROM), Load Snapshot…, Save Snapshot… (`.sna` / `.z80` / `.szx`). |
| **Spectrum Next** ▸ | Install Next ROMs…, Set Next SD Card Directory… (builds a bootable FAT32 image from a host dir), Set Next SD Card Image (.img/.mmc)…, Clear Next SD Card Setting. |
| **Tapes, Disks & Cartridges** ▸ | Load Tape (TAP/TZX)…; Insert/Eject Interface 2 cartridge (48K); DISCiPLE disks; +3/+2A floppy load/save/eject/write-protect (`.dsk` / `.edsk` / `.udi` / `.mgt` / `.img` / `.trd` / `.sad` / `.d40` / `.d80`); Speedlock workaround. |
| **Recording (RZX)** ▸ | Open / play back, start / stop recording, rollback to last snapshot. |
| **Microdrives** ▸ | 48K-only. 8-slot submenu; per-slot Insert / Save / Eject / Write-Protect. |
| **ZX Printer** ▸ | Save the accumulated printout as a PNG, or clear it. |
| **Save Tape** ▸ | Save the in-memory tape as `.tap`/`.tzx`, Stop Tape, Tape Browser (list blocks with names/types/lengths). |
| Start Recording (WAV)… | Capture the host audio output to a `.wav` (toggles to Stop). |
| Save Screenshot… | Write the current frame as a PNG (auto-adds `.png` if you don't type an extension). Works for every model and Next video mode. |

### Machine

| Item | What it does |
| --- | --- |
| 48K / 128K / +2 / +2A / +3 / Spectrum Next | Switch model. State is wiped (cold boot). |
| Reboot | Cold-restart the current model. |

### View

| Item | What it does |
| --- | --- |
| 100% / 125% / 150% / 200% / 300% | Scale the display. 4:3 aspect ratio is preserved with black bars at non-4:3 host sizes. |
| Full Screen | Toggle full-screen. Esc returns to windowed. |
| CRT Filter | Toggle a 2× upscale with darkened alternate rows for a CRT scanline look. |

### Peripherals

| Item | What it does |
| --- | --- |
| DISCiPLE | Toggle the MGT DISCiPLE disk interface (48K). |
| Interface 1 | Toggle the IF1 microdrive interface (48K only). |
| Multiface | Off / 1 / 128 / 3. Each maps the appropriate ROM and the NMI-button behaviour. |
| Kempston Mouse | Toggle the 3-port Kempston mouse interface. |
| ZX Printer | Toggle the Sinclair 1-bit thermal printer; the printout view is in the Emulator menu. |
| Joystick | None / Kempston / Sinclair 1 / Sinclair 2 / Cursor. |

### Emulator

| Item | What it does |
| --- | --- |
| Reboot | Cold restart (same as Machine → Reboot). |
| Pause / Resume | Stop / restart the emulation loop. |
| Poke… | Enter `address value` (hex or decimal) to write a single byte. |
| **Debugger** | Open the visual debugger window (see [below](#the-visual-debugger)). |
| ROM Info | Show the SHA-256 of every currently-mapped ROM. |
| Trigger NMI (F12) | Drive the NMI line. Multiface (if enabled) intercepts; otherwise the running OS sees it. |
| Save ZX Printer Output… | Save accumulated ZX Printer rows as a PNG. |

### Help

| Item | What it does |
| --- | --- |
| About zx_go | Show the version (e.g. `zx_go v1.0 RC1`) and a one-line description. |

---

## Keyboard, joystick and mouse

### Keyboard mapping

| Spectrum key | Host key |
| --- | --- |
| CAPS SHIFT | Left Shift |
| SYMBOL SHIFT | Right Shift / Ctrl / Alt / ⌘ (any) |
| Arrow keys | Arrow keys (map to CAPS SHIFT + 5/6/7/8) |
| DELETE | Backspace (CAPS SHIFT + 0) |
| BREAK | F11 (CAPS SHIFT + SPACE) |
| NMI / Multiface | F12 |
| Esc | Exit full-screen |

You can edit the mapping via `Emulator → Keyboard Mapping…` (custom keymaps persist in `config.json`).

### Joystick

`Peripherals → Joystick` lets you pick the joystick scheme. Host arrow keys + space are used by default; gamepad/joystick autodetection happens at startup.

| Scheme | Ports | Notes |
| --- | --- | --- |
| Kempston | `$1F` reads | The most common period choice |
| Sinclair Left | keys `1`-`5` | Interface 2 left port |
| Sinclair Right | keys `6`-`0` | Interface 2 right port |
| Cursor / Protek | keys `5`/`6`/`7`/`8`+`0` | Slightly older standard |

### Mouse

Enable `Peripherals → Kempston Mouse`. Host mouse movement and button presses then drive ports `$FBDF` (X), `$FFDF` (Y), `$FADF` (buttons).

---

## The visual debugger

Open it with `Emulator → Debugger`.

![Debugger](screenshot_debugger.png)

### Layout

The window is laid out in two rows, all live-updating at 20 Hz so the registers / disassembly / memory animate while the emulator runs:

**Top row** — the live CPU view:

1. **Registers** — every Z80 register: PC (highlighted), AF/BC/DE/HL/IX/IY, the alternate set (A'-L'), I/R, SP, interrupt state (IFF1/IFF2), interrupt mode, Halted state, IRQ taken/rejected counters, the flag line (S, Z, H, P/V, N, C), and a stack preview at SP.
2. **Disassembly** — scrollable Z80 disassembly from the current PC. All prefix groups decoded (CB, ED, DD, FD, DDCB, FDCB) plus the Next Z80N extensions. `>` marks the current instruction; `*` marks active breakpoints — **click any line to toggle a breakpoint**.
3. **Memory** — hex + ASCII dump of the whole 64 KB address space. The address field accepts hex (`$XXXX` / `0xXXXX`), decimal or octal via the base selector.

**Bottom row** — the paging diagram alongside an icon-led **tools tab strip**:

- **Next State**, **Bank Inspect** (peek/poke any bank), **Backtrace**, **History** (M1-fetch ring), **NextReg** (read/write), **Breakpoints** (conditional + bank-filtered).
- **Watchpoints** — register watches (`watch-reg` parity), **Heatmap** (hot-PC / call / ret / rst views over the history ring), **Time Travel** (snapshot ring: enable, snapshot-now, tap a row to rewind).
- On the Spectrum Next, four **graphical** inspectors render live state: **Palette** (256-entry swatch), **Sprites** (each visible sprite's pixels), **Layer 2** (the framebuffer), and **Tilemap**.

The window fits a standard laptop display and every split divider is draggable. See [DEBUGGER.md](DEBUGGER.md#tools-tabs) for the full list.

### One backend, two surfaces

The visual debugger and the [telnet debugger](#the-telnet-debugger) are two views over **one shared backend**: a breakpoint set in telnet shows up (and fires) in the GUI's Breakpoints tab and disassembly gutter, and vice versa. The same is true for register watchpoints, the time-travel snapshot ring, and the M1 history/heatmap. So you can drive a scripted session over telnet and watch it live in the GUI.

### Common operations

| Goal | How |
| --- | --- |
| Pause the emulator | Click the **Pause** button at the top. Status changes from green "running" to amber "paused". |
| Resume | Click **Run**. |
| Single-step one instruction | Click **Step**. The CPU executes exactly one Z80 instruction, skipping the interrupt check. The disasm cursor and registers update. |
| Step over a call | Click **Step Over**. Runs past a `CALL` / `RST` / Z80N `PUSH nn` to its return address; single-steps otherwise. |
| Execute one full frame | Click **Step Frame** (69 888 T-states at the 48K reference rate; the actual instruction count depends on what code is running). |
| Set a breakpoint | Click any line in the disassembly. The line turns red with `*`. Click again to clear. |
| Continue until a breakpoint hits | **Run**. When PC reaches a breakpoint, the emulator auto-pauses. |
| Go to a specific address | **Go to PC**, enter address. Disasm jumps; emulation is not affected. |
| Edit registers | **Edit Registers** button. Dialog lets you set any 8-bit or 16-bit register. |
| Write to memory | **Write Memory** button. Address + byte value. |

Breakpoints (and register watchpoints) live in the shared backend for the session, so they're visible to the telnet debugger too; they aren't persisted across launches.

---

## The telnet debugger

zx_go ships a ZRCP-style line-oriented remote debugger on a configurable TCP port. The protocol is designed to be scriptable from `nc` / `telnet` / shell pipelines. It shares its backend with the [visual debugger](#one-backend-two-surfaces) — breakpoints, register watchpoints and the time-travel ring set here are live in the GUI too.

The tables below cover the everyday commands; the full set is much larger — register/memory/port watchpoints (`watch-reg`, `watch-mem`, `watch-port`), tracepoints (`tp`, `trace-writes`, `nr-trace`), heatmaps (`hot`, `callgraph`), time-travel (`tt-on`, `tt-snap`, `tt-rewind`), provenance (`provenance`, `why-pc`), `step-over`, `irq-stats`, `crash-detect` and more. Type `help` for the complete list, and see [DEBUGGER.md](DEBUGGER.md) for full detail.

### Starting it

```bash
./bin/zx_go --next --headless --debugger-port=10000 --debugger-pause-at-start
```

`--debugger-port=N` opens a TCP listener on port N. `--debugger-pause-at-start` (optional) halts the CPU at PC=`$0000` before fetching the first instruction, so you can set up breakpoints before any code runs.

Connect from another terminal:

```bash
nc localhost 10000
```

You'll see `OK welcome to zx_go remote debugger`. Type `help` for the command list.

### Commands

Every response begins with `OK ` on success or `ERR ` on failure, so commands can be parsed by line.

#### CPU control

| Command | Aliases | Purpose |
| --- | --- | --- |
| `pause` | | Stop the CPU. Implicitly issued by any state-reading command. |
| `continue` | `cont`, `c`, `run`, `r` | Resume execution. If currently at a breakpoint, single-steps over it first so it doesn't immediately re-fire. |
| `step` | `s` | Execute exactly one Z80 instruction; pause again. Synchronous — the next command sees post-step state. |

#### Inspection

| Command | Aliases | Purpose |
| --- | --- | --- |
| `get-registers` | `regs` | Full register dump on one line. PC is annotated with the symbol map if loaded. |
| `get-stack` | `stack` | 16 16-bit words at SP, annotated. |
| `get-memory $ADDR $LEN` | `mem`, `hexdump` | Hex bytes (max 256 per call). |
| `read-memory $ADDR` | `peek` | Single byte. |
| `disassemble [$ADDR [N]]` | `disasm`, `d` | N-instruction window (default 6) starting at $ADDR (default current PC). |
| `get-mmu` | `mmu` | Current ROM bank + 8K-MMU slots. |
| `get-divmmc` | `divmmc` | divMMC overlay state (paged_in / mapram / automap). |
| `nextreg-read $REG` | `nr-r` | Read NextReg by index. |

#### Mutation

| Command | Aliases | Purpose |
| --- | --- | --- |
| `write-memory $ADDR $VAL` | `poke` | Single byte write. |
| `nextreg-write $REG $VAL` | `nr-w` | NextReg write (fires any OnWrite hooks). |

#### Breakpoints

| Command | Aliases | Purpose |
| --- | --- | --- |
| `set-breakpoint $ADDR [bank=N]` | `set-bp`, `bp` | Halt when PC equals $ADDR. `bank=N` (0-3) filters by which ROM bank is mapped at $0000 — critical for the Spectrum Next, where the same PC means different code in different banks. |
| `clear-breakpoint $ADDR` | `clear-bp`, `cbp` | Remove a breakpoint. |
| `list-breakpoints` | `list-bp`, `lbp` | Show all active breakpoints, annotated. |

### Address syntax

Addresses accept `$XXXX`, `0xXXXX`, or bare hex (`5C3A` works too). Decimal isn't supported in debugger commands — Spectrum addresses are universally written in hex.

### Worked examples

#### Step through the cold-boot sequence on the Spectrum Next

```bash
./bin/zx_go --next --headless --debugger-port=10000 --debugger-pause-at-start &
nc localhost 10000
```

```
OK welcome to zx_go remote debugger
> get-registers
OK PC=$0000 (RESET_ENTRY) SP=$FFFF AF=$FFFF BC=$FFFF... insns=0
> step
OK stepped
> get-registers
OK PC=$0001 SP=$FFFF AF=$FFFF... insns=1
> step
OK stepped
> get-registers
OK PC=$00EF (POST_RESET) SP=$FFFF AF=$FFFF... insns=2
> disasm
OK
 00EF ED 91 07 03 NEXTREG $07,$03
 00F3 ED 91 03 B0 NEXTREG $03,$B0
 00F7 ED 91 C0 08 NEXTREG $C0,$08
...
```

#### Bank-aware breakpoint: catch entry to bank-2 SD-mount

```
> set-breakpoint $1B67 bank=2
OK breakpoint @$1B67 (bank=2)
> continue
OK continuing
[... wait a second...]
> pause
OK paused
> get-registers
OK PC=$1B67 (BANK2_POST_LDIR_CALL_00A3) SP=$5BFB AF=$FFA8... insns=475797
```

The breakpoint fires only when bank-2 is mapped — bank-0 / bank-1 / bank-3 also have code at `$1B67` but it's something else.

#### Inspect sysvars

```
> read-memory $5C3A
OK $FF
> get-memory $5C3A 12
OK FF 1C 00 00 00 00 00 00 00 00 00 00
```

That's ERR_NR = $FF and FLAGS = $1C — standard post-NEW state for 128K BASIC.

#### Walk memory

```
> disasm $34 8
OK
 0034 CC F2 FF CALL Z,$FFF2
 0037 FF RST $38
 0038 FB EI
...
```

#### Quit

```
> quit
OK bye
```

The TCP listener stays up; the emulator keeps running until you Ctrl-C it.

### Bank-aware breakpoints with symbol maps

Load a symbol map to annotate addresses with names:

```bash
./bin/zx_go --next --headless --debugger-port=10000 --debugger-pause-at-start \
 --symbol-map=mymap.sym
```

With a symbol map, `get-registers`, `disasm`, `get-stack`, and `list-breakpoints` annotate addresses with their names — `PC=$00EF (POST_RESET)` instead of `PC=$00EF`. Symbol files are plain text: one `$XXXX NAME` per line, `#` and `;` comments allowed.

---

## Headless mode and debug instrumentation

`--headless` runs the emulator without a GUI. Useful for CI tests, scripted reproductions, batch boot-flow analysis, regression captures.

### Basic non-interactive boot

```bash
./bin/zx_go --next --headless --frames=3000 --save-screen=/tmp/boot.png
```

Runs 3000 simulated frames (~60 emulated seconds) then writes the framebuffer.

### Periodic snapshots

```bash
./bin/zx_go --next --headless --frames=3000 \
 --snapshot-every=500 \
 --watch="ERR_NR:5C3A,FLAGS:5C3B,CH_ADD:5C5D:w"
```

`--snapshot-every=N`: every N frames, dump a multi-line state record: CPU registers, MMU slots, watched sysvars, divMMC state, disassembly window around PC, stack walk. Add `--snapshot-prefix=foo_ --snapshot-screens` to save per-snapshot PNGs.

`--watch="NAME:ADDR[:w]"`: per-snapshot memory watch. Addresses default to hex. Append `:w` to read a 16-bit little-endian word.

### Memory-write watchpoints

```bash
./bin/zx_go --next --headless --frames=3000 \
 --watch-writes="FLAGS2_ext@D5D4,FLAGS@5C3B"
```

Every write to one of these addresses is logged with the source CPU PC. Massively useful for "who wrote to X and when?".

### Bank-switch logging

```bash
./bin/zx_go --next --headless --frames=300 --log-bank-switches
```

Every classic 7FFD / 1FFD ROM-bank switch and every NextReg $50-$57 8K-MMU slot change emits a slog event with the source (NEXTREG_8E / NEXTREG_50+slot / 7FFD / 1FFD).

### SD command logging

```bash
./bin/zx_go --next --headless --frames=300 --log-sd
```

Every SPI command (CMD0, CMD8, CMD55+ACMD41, CMD17 etc.) the boot sends to the virtual SD card is logged with its argument. Maps directly onto the SD-protocol commands.

### PC triggers — snapshot the first time some code runs

```bash
./bin/zx_go --next --headless --frames=3000 \
 --snapshot-on-pc="AUTOEXEC_DISPATCH@\$08F0-\$0920,SD_MOUNT@\$1B67"
```

One-shot snapshot the first time PC enters any of the named ranges. Different from a breakpoint: doesn't halt, just records and continues.

### Loop / stall detector

```bash
./bin/zx_go --next --headless --frames=3000 --loop-threshold=5000
```

Emits a "stall-detected" snapshot when the same PC fires N M1 cycles in a row without any other PC intervening. Auto-finds tight loops, LDIR-based RAM clears, HALT spins, etc.

### High-throughput trace channels

For bulk capture:

```bash
./bin/zx_go --next --headless --frames=300 \
 --trace=pc,nextreg,bankswitch \
 --trace-pc-range='$0000-$3FFF' \
 --trace-output=/tmp/boot.jsonl
```

Writes JSON-lines events for post-processing. Channels: `pc`, `nextreg`, `ports`, `bankswitch`, `screen`. `--trace-pc-range` filters the PC channel (otherwise you get one event per M1 fetch — millions per minute).

### One-shot state dump

```bash
./bin/zx_go --next --headless --dump-state=300
```

Runs 300 frames then prints CPU + MMU + NextRegs + sysvars + screen statistics to stdout. Easy diffing.

### See everything

```bash
./bin/zx_go --help
```

The output includes the version, copyright, and every flag with its purpose. `--version` prints just the version tag.

---

## Project structure

```
cmd/zx_go/ Main application entry point (Fyne GUI + headless CLI)
pkg/
 z80/ Z80 + Z80N CPU core
 ula/ ULA (display, border, audio, tape I/O, port dispatch)
 memory/ Memory management, bank paging, contention, FPGA bootrom
 keyboard/ Keyboard matrix
 audio/ Beeper audio
 ay/ AY-3-8912 sound chip
 snapshot/ SNA / Z80 / SZX
 plus3fdc/ +3 FDC (μPD765A) and disk image parsers
 microdrive/.mdr cartridge format
 if1/ Interface 1 + Microdrive
 if2/ Interface 2 cartridge
 kempmouse/ Kempston mouse
 zxprinter/ ZX Printer
 rzx/ RZX input recording
 debugger/ Visual debugger (used by the GUI window)
 next/
 nextregs/ NextReg dispatcher
 divmmc/ divMMC overlay
 esxdos/ esxDOS file API (F_OPEN / F_READ / …)
 sdcard/ FAT32 builder, SD-SPI emulator
 install/ Spectrum Next ROM install + SD-card source resolution
 layer2/ Layer 2 framebuffer
 palette/ 9-bit palette
 sprite/ 128 hardware sprites
 copper/ Copper coprocessor
 dma/ zxnDMA
 dac/ 4-channel DAC bank
 rtc/ Real-time clock (host time)
 uart/ ESP UART stub
 nex/.NEX V1.2 loader
 compositor/ Layer composition pipeline
 testharness/ Headless scripted emulator (40+ integration tests)
 roms/ ROM loading + embedded fallback
 peripherals/ Peripheral manager (DISCiPLE, Multiface, IF1, …)
 disciple/ DISCiPLE disk interface
 multiface/ Multiface 1 / 128 / 3
 config/ Persistent user settings (config.json)
 trace/ JSON-lines event emitter for --trace
 zxlog/ Colored startup banner, version, slog setup
docs/ Per-subsystem docs
LICENSES/ GPLv3 text + NOTICE for the embedded tbblue_loader.rom
```

---

## License

MIT — see [LICENSE](LICENSE) for the full text. The bundled FPGA loader (`pkg/roms/data/tbblue_loader.rom`, embedded) is GPLv3; see [`LICENSES/tbblue_loader-NOTICE.md`](LICENSES/tbblue_loader-NOTICE.md).
