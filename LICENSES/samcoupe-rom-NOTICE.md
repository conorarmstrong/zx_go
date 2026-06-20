# SAM Coupé system ROM — attribution & redistribution notice

The file `pkg/sam/data/samcoupe.rom` (embedded into the emulator for the SAM
Coupé machine) is the **MGT SAM Coupé system ROM, version 3.0** — the last
official MGT release and the standard image for emulation.

- **Size:** 32,768 bytes (32 KB = ROM0 + ROM1)
- **SHA-256:** `14d52ffc635a2ece0244aa3fd327bab5ee796f92570361aade0d6df3eba41d9f`
- **Reset entry:** `F3 C3 B0 00` (`DI ; JP $00B0`)
- **Boot banner:** `MILES GORDON TECHNOLOGY plc — © 1990 SAM Coupé`

## Copyright & permission

The SAM Coupé ROM is copyright its author, **Andrew Wright**. In April 2008 he
explicitly placed his SAM Coupé software, including the ROMs, into free
redistribution:

> "I hereby allow all my SAM Coupé titles (including ROMs) and associated
> manuals to be freely (re)distributed." — Andrew Wright, April 2008

On the strength of that grant the ROM is bundled with this emulator (rather than
requiring a separate download, as the licensed Spectrum Next ROMs do).

## Source / provenance

Obtained from the SimCoupe author's ROM repository — `roms/ROM30`, the canonical
MGT ROM 3.0 image SimCoupe itself uses:

- <https://github.com/simonowen/samrom> — `roms/ROM30`

Also available from the World of SAM ROM archive
(<https://www.worldofsam.org/products/rom>).

This notice covers the ROM image only; the surrounding emulator code is under the
project's MIT license (see `../LICENSE`).
