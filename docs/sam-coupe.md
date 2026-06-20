# SAM Coupé support in zx_go

The **MGT SAM Coupé** (1989) is a Z80B-based home computer — a 6 MHz Z80 with a
custom display/IO ASIC (four screen modes, a 128-colour palette), 256/512 KB of
paged memory, a Philips SAA1099 stereo sound chip and a WD1772 floppy
controller. zx_go emulates it as a first-class machine (`pkg/sam`).

## Running it

- **GUI:** `Machine → SAM Coupé`.
- **Command line:** `zx_go --sam`.
- **Headless** (boot + screenshot): `zx_go --sam --headless --save-screen=out.png`.

The SAM boots to its copyright screen and then **SAM BASIC**. The system ROM
(MGT ROM 3.0) is **bundled** — Andrew Wright placed the SAM ROMs into free
redistribution in 2008 — so nothing needs installing (see
`LICENSES/samcoupe-rom-NOTICE.md`).

## What works

- **Boots the real ROM 3.0** end to end to the SAM BASIC prompt — press a key at
  the copyright screen to drop into BASIC, then type and run programs.
- **All four screen modes**, rendered line-accurately (mid-frame palette/mode
  splits are honoured):
  - MODE 1 — ZX-Spectrum-compatible, 256×192, 2 colours per 8×8 cell + FLASH.
  - MODE 2 — 256×192 with per-line attributes.
  - MODE 3 — 512×192, 4 colours (hi-res).
  - MODE 4 — 256×192, 16 colours.
- **128-colour master palette** + the 16-entry CLUT; per-mode colour resolution.
- **Memory:** the full LMPR/HMPR/VMPR/LEPR/HEPR paging model — 256 KB or 512 KB
  internal (512 KB default) plus up to 4 MB external RAM, ROM0/ROM1 overlay,
  write-protect.
- **Sound:** the SAA1099 chip — six tone channels (two groups of three), per
  channel left/right amplitude, two noise generators, two envelope generators —
  mixed to the audio device (currently down-mixed to mono; `--no-sound` silences
  it).
- **Light pen:** the ASIC LPEN/HPEN raster registers (the boot ROM syncs to the
  raster through them).
- **Disk:** the WD1772 controller and MGT (800K/720K) + SAD disk images, loaded
  from **File → Load SAM Disk 1/2**. Real games boot — load the disk, then type
  `BOOT` at the SAM BASIC prompt (verified end to end with Manic Miner and
  Tetris booting to their title screens).
- **Interrupts:** the 50 Hz frame interrupt and the programmable line interrupt,
  with the active-low STATUS register.
- **ASIC contention:** the SAM's heavy memory/IO contention (it makes the 6 MHz
  Z80 run noticeably slower, as on real hardware). Opt out for A/B timing with
  `ZX_GO_SAM_NO_CONTENTION=1`.
- **Keyboard:** the 9-row SAM matrix — letters, digits, the editing keys, SHIFT,
  SYMBOL and the cursor keys, plus a typed-character layer so symbols (`" : ; ,
  . = + - ( ) ! @ # $ % & ' / * _ < >`) type correctly on any host layout.
- **Border:** rendered around the active display in the live BORDER colour,
  line-accurately (mid-frame `BORDER` raster splits are honoured). Like the
  Spectrum, the BASIC default is a white border on white paper (so it's not
  obvious until a program sets `BORDER`).
- **Reset:** **Machine → Reboot** cold-restarts the SAM to its copyright screen.

## Current limitations / in progress

- **Stereo audio** — the SAA1099 is emulated in full stereo, but the shared
  audio device currently plays a mono down-mix; widening the device to true
  stereo (and adding the 1-bit beeper) is the remaining audio work.
- **Disk formats** — EDSK and SBT are not yet parsed (MGT and SAD are).
- **Save states (.ssx) and SAM-specific debugger views** — planned.

## Notes

The SAM display is rendered at MODE 3's native 512-pixel width (the lo-res modes
double each pixel), scaled to 4:3 for display.
