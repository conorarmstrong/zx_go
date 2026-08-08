# pkg/opus — Opus Discovery

The Opus Discovery disk interface: an 8 KB ROM, a WD1770 floppy controller,
interface RAM, and a Kempston-compatible joystick port.

## The interface is memory-mapped, not port-mapped

This is the thing to know before reading the code, and the thing that made it
hard to work out. The Opus ROM contains almost no `IN`/`OUT` instructions,
because the hardware appears as ordinary memory in the window the interface
pages in alongside its ROM:

| Range | Function |
|---|---|
| `$0000-$1FFF` | The 8 KB Opus ROM |
| `$2800-$2803` | WD1770 registers: command/status, track, sector, data |
| `$3000-$3003` | Drive control and status |
| rest of `$2000-$3FFF` | Interface RAM |

The only genuine port instructions in the ROM are `IN A,($FE)` for the
keyboard and `IN A,($1F)` for the joystick — the latter confirmed as real code
by its surrounding context (it reads `$3000`, tests bit 7, then reads the
joystick), rather than by a byte scan.

### Provenance

Derived from the published v2.15 ROM disassembly at speccy4ever.speccy.org
(`rom/opus/opus215disas.rtf`), cross-checked against the v2.22 ROM vendored at
`roms/opus.rom`. The clincher was that the disassembly's instructions reach
`$2800-$2803` and `$3000-$3003`, and `$3000` turns up in the independent v2.22
ROM immediately before the joystick read — the same address in two ROM
versions. **No GPL implementation was consulted**, so this stays clean against
the project's MIT licence.

## Disk format

`.opd` images are 40 cylinders of 18 256-byte sectors on a single side: a flat
**184320 bytes** with no header or signature, so size is the only validation
available. Confirmed against three unrelated real images, each exactly that
size.

That geometry is why this package has its own controller rather than reusing
`pkg/betadisk`'s: that one is built around TR-DOS's 16 sectors per track.

## What works

Sector read and write through the memory-mapped register file — the same path
the guest uses — verified against real `.OPD` images. Type I positioning
(restore, seek, step in/out), Type II read/write sector, drive selection,
write protection, and record-not-found on an empty drive.

## What does not

**The ROM auto-paging trigger is unknown.** Something in the hardware pages the
interface in and out over the Spectrum ROM, and nothing available says what.
Ruled out so far:

- Running the ROM standalone from reset: it polls `$3001` a few hundred times,
  then runs off into RAM. Sweeping every single-bit value for that status
  register changes which way it fails but never reaches the controller.
- Static disassembly: there is no port map to find, because the interface is
  memory-mapped.
- The published disassembly's `page-in` / `page-out` sections: these turn out
  to be BASIC-editor routines, not hardware paging.
- The v2.22 manual PDF from the same source is a 5.9 KB stub with no technical
  content.

Until that is known the package is a working disk subsystem rather than a
drop-in boot device, so it is deliberately not wired into the peripherals
manager or the GUI yet.
