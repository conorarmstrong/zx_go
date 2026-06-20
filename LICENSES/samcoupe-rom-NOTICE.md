# SAM Coupé system ROM — attribution & redistribution notice

The file `pkg/sam/data/samcoupe.rom` (embedded into the emulator for the SAM
Coupé machine) is the **MGT SAM Coupé system ROM, version 3.0** — the last
official MGT release and the standard image for emulation.

- **Size:** 32,768 bytes (32 KB = ROM0 + ROM1)
- **SHA-256:** `a5d2855d243bd46b56ad9ffb68bd6a5e6f58fe11cec3260e43eb7e0f57b0b9b6`
- **Reset entry:** `F3 C3 B0 00` (`DI ; JP $00B0`)

## Copyright & permission

The SAM Coupé ROM is copyright its author, **Andrew Wright**. In April 2008 he
explicitly placed his SAM Coupé software, including the ROMs, into free
redistribution:

> "I hereby allow all my SAM Coupé titles (including ROMs) and associated
> manuals to be freely (re)distributed." — Andrew Wright, April 2008

On the strength of that grant the ROM is bundled with this emulator (rather than
requiring a separate download, as the licensed Spectrum Next ROMs do).

## Source / provenance

Obtained from the World of SAM ROM archive:

- <https://www.worldofsam.org/products/rom> — `samco_v3.0.rom_.zip`

The same image is mirrored by the SimCoupe author at
<https://github.com/simonowen/samrom>.

This notice covers the ROM image only; the surrounding emulator code is under the
project's MIT license (see `../LICENSE`).
