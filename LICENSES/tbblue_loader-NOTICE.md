# Bundled component: ZX Spectrum Next FPGA loader (`tbblue_loader.rom`)

zx_go bundles one third-party binary, embedded at
`pkg/roms/data/tbblue_loader.rom` (8192 bytes) and loaded as the
Spectrum Next FPGA boot loader.

## What it is

The 8 KB Z80 firmware loader the Spectrum Next's FPGA core runs first
at power-on: it initialises the machine, reads the core / NextZXOS ROM
from the SD card, and hands off. zx_go (like the jnext emulator, which
calls the same file `nextboot.rom`) loads it as a standalone blob
because it does not execute the FPGA bitstream itself.

## Copyright and license

> ZX Spectrum Next Firmware
> Copyright 2020 Garry Lancaster, Fabio Belavenuto & Victor Trucco

Licensed under the **GNU General Public License, version 3 or later
(GPL-3.0-or-later)**. The startup runtime (`crt0.s`) is from SDCC and
is GPL-2.0-or-later. The full GPLv3 text is in
[`LICENSES/GPL-3.0.txt`](GPL-3.0.txt).

This component is **distinct** from the proprietary NextZXOS ROMs
(`enNextZX.rom`, `enNxtmmc.rom`), which contain Amstrad / Sky
copyrighted material and are therefore **NOT** bundled — they are
downloaded by the user from the official Spectrum Next distribution.

## Corresponding source

The binary is an unmodified build of the official firmware source.
The complete corresponding source ships inside every official
Spectrum Next distribution archive (the same one users download for
the NextZXOS ROMs) under:

    src/firmware/loader/        (the loader C/asm sources + Makefile)
    src/firmware/LICENSE        (the GPLv3 text)

e.g. in `sn-complete-24.11.zip` from
<https://www.specnext.com/latestdistro/>. The firmware is also
maintained in the official "next-firmware" repository published by the
ZX Spectrum Next team (Garry Lancaster et al.).

In accordance with GPLv3 §6, that source is the corresponding source
for the bundled binary. No modifications were made to the firmware;
zx_go embeds an unmodified build of it.

## Why this satisfies the GPL

The loader is a separate Z80 program that zx_go *loads as data* at
runtime — it is not linked into the zx_go binary. This is an
"aggregate" under GPLv3 §5: only `tbblue_loader.rom` is GPL-licensed;
the rest of zx_go is under its own license. zx_go's obligation for
this file is to ship the GPLv3 license text (done, above) and to make
the corresponding source available (done, above).
