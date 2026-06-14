# Z80 conformance suites

`zexdoc.com` and `zexall.com` are Frank Cringle's Z80 instruction
exerciser binaries, originally released to comp.sys.cpm in 1994 as
part of the YAZE Z80 emulator and distributed without restriction.
The binaries are CP/M COM files: load at 0x0100, exit by jumping to
0x0000, and use BDOS calls at 0x0005 for console output (function 2
= print char in E, function 9 = print `$`-terminated string at DE).

- **zexdoc.com** — exercises only documented instruction behaviour.
  The standard regression baseline for Z80 emulators.
- **zexall.com** — exercises documented *and* undocumented behaviour
  (F3/F5 flags, MEMPTR / WZ register, undocumented BIT n,(HL) flag
  bits, the full DDCB / FDCB undocumented opcode space). Stricter.

Each test prints `OK` if the CRC matches the canonical value, or
`ERROR` followed by expected/got CRCs if it doesn't.

## Provenance

Both files are exactly 8704 bytes — the canonical Cringle distribution
size. SHA-256:

- `zexdoc.com`: `34923a7ed82285d3038b2d54bd64899e12173eebb61f9d07b4fc72e78af2ae8f`
- `zexall.com`: `6e2da55147a04f28d303d5da6a1e6b771557ac244653590a0f24a2d39c8537e8`

These hashes match the binaries distributed under the YAZE source
tarball and re-distributed by z88dk, FUSE, the reference emulator, mednafen, and most
modern Z80 emulator projects. Frank Cringle's original announcement
made them freely redistributable; this repository carries no separate
licence file for them on that basis.

## Running

```
go test -run TestZex ./pkg/z80
```

`-short` mode skips both. On a modern host both suites together
finish in well under two minutes; the 60-billion-instruction ceiling
in the harness is a guard rail, not a target.
