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
  played in **true stereo**, plus the 1-bit beeper on BORDER bit 4 that SAM
  software inherits from the Spectrum. Both are AC-coupled like the machine's
  own output stage, so a held speaker level is silence rather than a DC rail.
  `--no-sound` silences it.
- **Light pen:** the ASIC LPEN/HPEN raster registers (the boot ROM syncs to the
  raster through them).
- **Disk:** the WD1772 controller and MGT (800K/720K), SAD and **Extended DSK**
  images, loaded from **File → Load SAM Disk 1/2**. EDSK is what SAMdisk writes
  and is read by the same parser the +3 uses. An EDSK whose tracks do not share
  one geometry is refused with the offending track named rather than flattened:
  the SAM's sector store has a single geometry, so accepting one would place
  every sector after the odd track at the wrong offset. Real games boot — load
  the disk, then type `BOOT` at the SAM BASIC prompt (verified end to end with
  Manic Miner and Tetris booting to their title screens).
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
- **Reset:** **Emulator → Reboot** cold-restarts the SAM to its copyright screen.

## Current limitations / in progress

- **SBT files** — not supported, and the obstacle is a **DOS image**, not the
  format. An SBT is not a disk image: it is a single SAM CODE file meant to be
  copied onto a disk and booted, so handling one means building a disk around
  it.

  Building the disk is the easy half and is well understood. What stops it is
  that `BOOT` loads **directory slot 1 as the DOS**, so a disk carrying only the
  user's file has no DOS on it and the ROM answers `53 No DOS`. A real bootable
  SAM disk shows the arrangement: SAMDOS occupies slot 1, the title's own files
  the next few slots, and the auto-run file a later one. Note that the file-type
  byte does not identify the DOS — SAMDOS and the title's code files share a
  base type of 19 (CODE), differing only in the hidden/protected flags — so it
  is the **slot position** that matters.

  Emulators that support SBT do it by supplying a DOS of their own and injecting
  it. We have no DOS image to place in slot 1. Bundling one would need its
  redistribution status established: the notice in `LICENSES/` covers "all my
  SAM Coupé titles (including ROMs)", which may or may not reach SAMDOS
  depending on its authorship, and nobody here has checked. Copying a DOS out of
  a disk the user already owns is the other route, and is a design decision
  rather than a detail.
- **SAM-specific debugger views** — planned.

## Notes

The SAM display is rendered at MODE 3's native 512-pixel width (the lo-res modes
double each pixel), scaled to 4:3 for display.
