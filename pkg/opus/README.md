# pkg/opus — Opus Discovery

The Opus Discovery disk interface: an 8 KB ROM that pages itself over the
Spectrum ROM, a WD1770 floppy controller, 2 KB of interface RAM, a 6821 PIA
carrying the drive lines and a Centronics port, and a Kempston-compatible
joystick input.

It boots, hooks itself into BASIC, formats disks, and loads and runs real
games off real `.opd` images.

## The interface is memory-mapped, not port-mapped

This is the thing to know before reading the code. The Opus ROM contains
almost no `IN`/`OUT` instructions because the hardware appears as ordinary
memory in the window it pages in alongside its ROM:

| Range | Function |
|---|---|
| `$0000-$1FFF` | The 8 KB Opus ROM |
| `$2000-$27FF` | 2 KB interface RAM (an IC 6116) |
| `$2800-$2803` | WD1770: command/status, track, sector, data |
| `$3000-$3003` | 6821 PIA: PRA/DDRA, CRA, PRB/DDRB, CRB |

The only genuine port instructions are `IN A,($FE)` for the keyboard and
`IN A,($1F)` for the joystick.

## ROM paging is an M1 address trap, delayed one cycle

The hardware watches the address bus during opcode fetches. Three addresses
page the ROM **in** and one pages it **out** — but the change lands on the
*next* fetch, not the one that triggered it. So the instruction sitting at a
trap address always executes from whichever ROM was already paged:

| Address | Effect | The instruction that actually runs there |
|---|---|---|
| `$0008` | page in | the Spectrum's `LD HL,(CH_ADD)`; the Opus takes over at `$000B` |
| `$0048` | page in | the Spectrum's `PUSH BC`; the Opus takes over at `$0049` |
| `$1708` | page in | the Spectrum's `INC HL`; the Opus's `DEC HL` at `$1709` cancels it |
| `$1748` | page out | the Opus's bare `RET`, fetched before the ROM drops |

The delay is not a detail — it is why the Opus ROM carries **placeholder bytes
that copy the Spectrum's**. Opus `$1708` is `NOP` where the Spectrum has
`INC HL`; Opus `$0048` is `C5`, the same `PUSH BC` the Spectrum has. Those
bytes are never executed. Finding them is what confirmed the trap addresses.

At power-on the ROM is paged in, so the Z80 runs the Opus reset vector:
`DI / LD SP,$1019 / JP $1748`. `$1019` holds zero, so paging out and returning
lands at `$0000` in the Spectrum ROM.

## Transfers are NMI-driven

There is no DMA. The WD1770's DRQ line is wired to the Z80's **NMI**, and the
handler at `$0066` (`JP (HL)`) vectors to one of three routines that move
exactly one byte through `$2803` and `RETN`.

The byte spacing matters. A real WD1770 at 250 kbit/s hands over a byte every
~32 µs, which is 112 T-states, and the handler needs about 83. Assert DRQ the
instant the previous byte is taken and the next NMI re-enters the handler
*mid-way* — at `$188C`, before the store and before the `RETN` — so `DE` and
`BC` never advance and the transfer spins on the stack instead of moving data.
`BytePeriodTStates` exists for that reason.

The pacing counts down from a delta rather than comparing against a deadline,
because the CPU's T-state counter is rebased every frame; an absolute deadline
stalls mid-sector at the first frame boundary with BUSY still asserted.

## Formatting

`FORMAT 1;"name"` drives the WD1770's WRITE TRACK. The controller is handed a
whole raw track a byte at a time — gaps, sync, address marks, ID fields and
sector data — and picks the sectors out of it, which is what the real chip
does. The ROM builds that stream from a run-length table at `$1BDB` with the
track, side, sector and size substituted into `$F0-$F4` placeholders; it is
standard IBM System 34 double density.

WRITE TRACK has no length in the command — it runs from one index pulse to the
next — so the controller stops itself after a track's worth of bytes
(`TrackRawBytes`). The ROM feeds rather more than that and the tail gap gets
cut short, exactly as on real hardware.

A `.opd` stores only sector data, so ID fields and gaps are discarded. The
sector's physical position comes from where the head actually is rather than
what the ID field claims, because a flat image has nowhere to record a
disagreement; sectors outside the geometry are dropped rather than aliased
onto a neighbour.

## Disk format

`.opd` images are 40 cylinders of 18 256-byte sectors on a single side: a flat
**184320 bytes** with no header, so size is the only validation available.

The ROM's own drive table states it:

```
18E5 DRIVE_1   DEFB +28    (00) 40 tracks.
               DEFB +12    (01) 18 sectors/track.
               DEFB +41    (02) %01000001   <- bits 7:6 = 01 = 256-byte blocks
               DEFB +00    (03) no extra sectors.
```

**Sectors are numbered from 0**, not 1. That is not the usual Western Digital
convention, but "(03) no extra sectors" plus `RD_WR_M_9` writing the raw block
number to `$2802` means the first sector of the disk is requested as track 0,
sector 0.

That geometry is why this package has its own controller rather than reusing
`pkg/betadisk`'s, which is built around TR-DOS's 16 sectors per track.

## Provenance

Derived from the complete v2.2 shadow-ROM disassembly published on World of
Spectrum — the same ROM version vendored at `roms/opus.rom` — cross-checked
against the ROM bytes and against the 48K ROM at every trap address. **No GPL
implementation was consulted**, so this stays clean against the project's MIT
licence. A GPL Fuse tree happens to sit in this working copy; it was
deliberately not read.

## Limitations

- Guest writes land in the mounted image, not the `.opd` file. The emulator
  offers an explicit save, so a game disk cannot be damaged by running it.
- The Centronics printer port is decoded and its direction bits behave, but
  nothing is attached, so BUSY reads as ready.
- 48K only. The trap addresses are 48K ROM addresses and mean something else
  under any other ROM, so the emulator refuses to fit the interface elsewhere.
