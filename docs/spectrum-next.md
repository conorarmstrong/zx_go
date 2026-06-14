# ZX Spectrum Next support in zx_go

zx_go supports the ZX Spectrum Next as `ModelNext` — a separate machine
alongside the existing 48K / 128K / +2 / +2A / +3 lines. The CPU, memory,
NextReg, divMMC, esxDOS, .NEX, palette, Layer 2, sprite, Copper and zxnDMA
subsystems are in place. **As of v1.0 RC1 the Next boots NextZXOS
end-to-end** through faithful Z80 execution: FPGA bootrom → TBBLUE splash
→ NextZXOS welcome → main menu, and the menu items work — ENTER on Browser
opens the SD card's `C:/` listing, NextBASIC runs interactive programs,
and the firmware configuration menu boots whichever machine personality
you pick. .NEX games also load via `File → Open File…`. The cold boot
runs entirely through faithful Z80 execution — no captured-state replay.

## Installing the Next ROMs

The Next needs the NextZXOS ROMs from the official distro. **The FPGA
loader (`tbblue_loader.rom`) is GPLv3 open-source firmware and is now
bundled with the emulator — you don't install it.** Only the two
licensed ROMs below are user-provided; when you first select the Next
without them, the emulator offers to download them from the official
distribution (or install by hand — licensing, see README):

- `enNextZX.rom` — the 64 KB NextZXOS distro ROM (four 16 KB banks)
- `enNxtmmc.rom` — the 8 KB divMMC ROM
- a NextZXOS-compatible SD card image or unzipped distro tree (the SD
  card content; the emulator can also build a FAT32 card from the tree)

To install:

1. Download the official Spectrum Next distribution zip from
   `https://www.specnext.com/distro/` (the 24.11 release is the one zx_go
   has been validated against).
2. From the running emulator, choose **File → Install Next ROMs…** and
   point the file picker at each of `enNextZX.rom` and `enNxtmmc.rom`
   in the zip's `machines/next/` directory.
3. The install action copies each blob to the repo-local install
   directory `roms/next/` (resolved by `pkg/next/install/install.go`:
   `$ZX_GO_NEXT_ROM_DIR` if set, else `<repo-root>/roms/next` when
   running inside a Go module, else `<cwd>/roms/next`) and reports
   the SHA-256 digest for each.

## Booting

There is one boot path now: the **authentic cold boot**. `./bin/zx_go
--next` (or **Machine → ZX Spectrum Next** in the GUI) runs the real
chain — FPGA bootrom → TBBLUE splash → NextZXOS welcome → main menu —
with no snapshots or setup. Give it a moment: the splash is ~5 s of
real time and the NextZXOS welcome ~10 s. From the welcome, SPACE
opens the menu; ENTER on **Browser** lists the SD card's `C:/`;
**NextBASIC** drops to an interactive `>` prompt.

(The historical "warm-boot snapshot" path — capturing a post-init
state from a reference emulator — has been **removed**: it was a
workaround from before the cold boot worked, and is no longer needed.)

## Selecting the Next

Once the ROMs are installed (or downloaded on first selection), the
**Machine → ZX Spectrum Next** menu entry boots the emulator into the
Next. Switching away and back tears down / re-wires the Next-only
subsystems cleanly.

## What works

| Subsystem | Status |
|---|---|
| Z80N CPU (extended opcodes) | ✅ all ~30 opcodes; cycle accurate at 3.5 MHz |
| 8K MMU (NextRegs 0x50–0x57) | ✅ slot table maintained, classic-paging coexistence |
| NextReg port file (0x243B / 0x253B) | ✅ select/data ports; per-register write masks + read-back semantics audited against `zxnext.vhd` (incl. clip-window NR$18-$1B 4-coordinate read/write index); a few read-backs still under audit |
| Z80N NEXTREG opcodes | ✅ |
| divMMC auto-pager | ✅ all six trigger PCs |
| esxDOS API surface | ✅ F_OPEN, F_CLOSE, F_READ, F_WRITE, F_SEEK, F_FSTAT, F_OPENDIR, F_READDIR, M_GETHANDLE, M_DRVAPI, M_GETDATE |
| SD card filesystem | ✅ host-directory mount **and** a bootable FAT32-LBA card image (built from the distro tree, or any real card image) |
| .NEX V1.2 loader | ✅ all 112 banks (0–111) supported; entry banks ≥8 must page themselves via NextReg 0x50..0x57 since classic 7FFD only addresses 0–7 |
| CPU turbo (7 / 14 / 28 MHz) | ✅ frame budget, M1 waits, and per-access contention magnitude all scale with the multiplier (×1 at 3.5 MHz keeps the boot byte-identical). Sample-exact audio-event placement above 3.5 MHz is approximate — a known limit. |
| RAM contention (NextReg 0x08 bit 1) | ✅ both the contention pattern position and the per-access stall magnitude scale with the turbo multiplier; bit 1 disables contention entirely |
| Multi-AY (3 chips, NextReg 0x06) | ✅ |
| 4× DAC | ✅ all four channels routed via ULA port dispatch; mixer contribution at audio-Read-window granularity (~23 ms — one MixedLevel snapshot per oto playback callback). Sample-playback chiptunes that write at 8 kHz typical rate will be audible but flattened to the rolling average over each window; v1.1 polish: per-write event integration mirroring the beeper's audioEvents log |
| 9-bit palette (NextRegs 0x40–0x44) | ✅ per-layer first/second selection |
| Layer 2 | ✅ 256×192 8bpp **and** 320×256 / 640×256 hi-res modes |
| Tilemap (Layer 3) | ✅ tile + 1bpp text modes; per-tile mirror / rotate, pixel scroll, clip window |
| Sprites (128) | ✅ position, pattern, palette, scale (1/2/4/8×), mirror, rotate, 8bpp, anchor groups (composite + unified), and the `$303B` status port (collision + max-per-line, clear-on-read) |
| Compositor (NextReg 0x15) | ✅ all four priority modes (SLU / LSU / SUL / LUS), the per-pixel SUL "below" stencil + Layer 2 priority bit, and NR$14 / NR$4A transparency |
| Copper coprocessor | ✅ instruction store / decode / per-line Step, driven by a per-T-state beam-position model so WAITs release on the correct scanline + hpos (full per-pixel hpos is the precision limit of a per-scanline renderer) |
| zxnDMA (port 0x6B) | ⚠️ memory-to-memory + the variable-length Z80-DMA WR-group protocol work; per-byte prescaler timing and descriptor mode (port 0xDB) deferred |
| RTC | ✅ host clock via the esxDOS M_GETDATE API **and** the i2c DS1307 bus on ports `$103B`/`$113B` (the NextZXOS date/time line renders) |
| UART stub (NextReg 0xA8 / 0xA9) | ⚠️ AT / AT+ command set produces plausible responses; no real Wi-Fi, no socket emulation |
| esxDOS file API | ✅ F_OPEN / F_CLOSE / F_READ / F_WRITE / F_SEEK / F_FSTAT / F_OPENDIR / F_READDIR / M_GETHANDLE / M_DRVAPI / M_GETDATE all wired and unit-tested via the RST 8 → dispatcher → host-directory mount path. Real-NextZXOS-program coverage is the next step (no contributor has scripted a NEXTBASIC program that exercises every call yet). |

## Status

The Spectrum Next hardware-feature set is implemented and TDD'd (see
`ROADMAP.md` for the per-feature catalogue and `CHANGELOG.md` for the
history). Now working — items this doc previously listed as gaps:

- **NextZXOS boots end-to-end** through the authentic FPGA-bootrom →
  TBBLUE → NextZXOS chain to the welcome/menu; ENTER on **Browser**
  reaches the `C:/` listing, **NextBASIC** runs, and the firmware config
  menu boots every machine personality — all via faithful Z80 execution,
  no captured-state replay.
- **128K BASIC** (More…→128K BASIC) launches the Sinclair "128" menu,
  pixel-identical to the reference emulator.
- **Layer 2** 256/320×256/640×256, **tilemap** (Layer 3) incl.
  mirror/rotate/scroll/clip, **full sprite** rendering (scale/mirror/
  rotate/8bpp/anchor groups + `$303B` collision), per-pixel **layer
  priority + SUL stencil**, **Copper** raster-precise execution, the
  **zxnDMA** Z80-DMA protocol, **NR$14/$4A** transparency, and the
  classic/LoRes/Timex/ULAnext screen paths.

Remaining work is **dev-tooling / research / boot-path polish** (the
`[nice-to-have]` / `[v1.1]` backlog in `ROADMAP.md`), not hardware-emulation
gaps. Niche timing personalities (e.g. Pentagon) and the F8 hardware-NMI
menu are best-effort; file an issue if a specific title needs them.

## Loading a .NEX file

```
File → Load… → pick any .NEX file
```

The emulator parses the header (rejects screens it doesn't yet support),
loads RAM banks 0..7, sets SP and PC, paging-maps the entry bank at
0xC000, and jumps. Single-bank simple games run; multi-bank-with-Layer-2
demos partially render.

## Testing

CI runs the per-component unit tests plus `TestNextRealROMBoot` (gated
on the installed distro). The end-to-end integration test
`TestModelNextLayer2VisibleEndToEnd` configures Layer 2 + palette via
the NextReg dispatcher and asserts on rendered RGBA pixels.

## Licensing notes

zx_go does not redistribute any Spectrum Next ROM. The classic Sinclair
ROMs bundled for 48K / 128K / +2 / +2A / +3 are covered by Amstrad's
1999 letter to the World of Spectrum project; Next-era blobs (NextZXOS,
divMMC, esxDOS) are governed by separate terms and must be installed
by the user.
