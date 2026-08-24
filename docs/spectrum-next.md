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

A ✅ here means implemented, pinned against the FPGA VHDL, **and** reachable by a
guest running on this emulator. **NOT WIRED** means the first two without the
third: the
model exists and matches the hardware, but nothing in the emulator constructs or
drives it, so no program can exercise it. That distinction is deliberate. A
subsystem nobody can reach is not a feature, and it looks identical to a working
one unless the table says otherwise. `pkg/next/reachability_test.go` fails the
build if a package moves between those states without this table being updated.

| Subsystem | Status |
|---|---|
| Z80 CTC (8 counter/timer channels) | ✅ all eight channels at ports `$183B`..`$1F3B` (`zxnext.vhd:2690`), pinned by GHDL-captured golden vectors from the FPGA VHDL, ticked on the CPU clock: a guest can program a channel's control word and time constant and read back its live down-counter. Its **interrupts** are not delivered yet, because the IM2 daisy chain below is not wired (ROADMAP item 2) |
| IM2 vectored-interrupt daisy chain | **NOT WIRED**: complete and pinned by GHDL-captured golden vectors (`pkg/next/im2.go`), but not connected to the CPU's interrupt path: NR$C0 (vector base) and NR$CC/$CD/$CE (interrupt enables) are stored and never acted on, so IM2 interrupts from the CTC, UART or line counter are not delivered (ROADMAP item 2) |
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
| ULA scroll (NextReg 0x26 / 0x27) | ✅ horizontal (whole-character + sub-character pixel shift) and vertical (with the FPGA's 192-line fold). NR$68 bit 2 half-pixel is stored but needs a 2x-wide ULA render to show |
| Compositor (NextReg 0x15) | ✅ all eight priority modes (SLU / LSU / SUL / LUS / USL / ULS + the two additive blend orderings), the per-pixel SUL "below" stencil + Layer 2 priority bit, and NR$14 / NR$4A transparency. The blend modes read NR$68's blend selection, which is wired (`cmd/zx_go/nr68.go`): bit 7 ULA output, bits 6:5 blend source, bit 2 half-pixel scroll and bit 0 the SLU stencil are all decoded and applied |
| Copper coprocessor | ✅ instruction store / decode, paced per raster column over the real line length: four copper clocks a column (28 MHz copper against a 7 MHz hcount), a `MOVE` that straddles a boundary paid out of the next column with the debt carried across line and frame boundaries, and compose ranges split at the pixel a `MOVE` wrote in. `WAIT` uses the FPGA's `hc_ula` origin, twelve columns before displayed pixel 0. Clocked on every line of the frame, not only the 192 displayed ones. NR$03 machine timing is not consulted, so a program selecting 48K or Pentagon timing still gets 456 columns and 311 lines |
| zxnDMA (ports 0x6B / 0x0B) | ⚠️ the transfer engine is complete and spec-checked: memory↔memory and memory↔IO endpoints, the variable-length Z80-DMA WR-group protocol, per-byte prescaler and cycle-length timing (burst + prescaler transfers interleaved with the CPU), Continue / auto-restart and read-mask read-back. Bus arbitration is partly live: a block holds the bus for its bytes and charges the CPU, burst yields it only in `WAITING_CYCLES` as the FPGA does, and `$83` abandons a block where it stands. The `dma_delay_i` pin is modelled but never driven, because its source is the IM2 daisy chain, which is itself an unwired reference model here, so no guest can have its DMA held off the bus. The interrupt / match logic is absent because the FPGA does not implement it either (no interrupt output, no daisy-chain pins, the mask/match and interrupt-control registers commented out). Timing defects remain open: see ROADMAP items 1 and 4 (`pkg/next/dma/dma.go`) |
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

Remaining work is mostly **game compatibility**: see the open items in
`ROADMAP.md`. Of the two hardware gaps once catalogued there, Copper `MOVE`
timing is now done, and the zxnDMA interrupt/match logic turned out not to be
implementable, because the FPGA does not implement it. What that work did leave
is a shorter list of zxnDMA timing defects, recorded as ROADMAP items 1 and 4; no screened
title is known to need them. Niche timing personalities (e.g. Pentagon) and the
F8 hardware-NMI menu are best-effort; file an issue if a specific title needs
them.

## Loading a .NEX file

```
File → Open File… → pick any .NEX file
```

In the GUI a `.nex` is launched the way the machine itself would launch it:
the emulator validates the header, asks before copying the file onto the
configured SD card (an SD card is required for this reason), and then drives
NextZXOS's own `.nexload` dot command from the Command Line, so a title that
calls the OS at runtime is hosted properly. Bank
injection — parsing the header, copying every bank into RAM, setting SP/PC and
paging the entry bank at `0xC000` — is what `Harness.LoadNEX` does for headless
tests, and it cannot host an OS-calling title, which is why the compatibility
figures are taken from the NEXLOAD path (`TestNexloadSDGames`).

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
