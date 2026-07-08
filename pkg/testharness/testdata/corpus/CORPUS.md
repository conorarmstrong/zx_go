# Real-software compatibility corpus

Third-party ZX Spectrum programs, vendored here to boot in CI as a
faithfulness regression suite (see `../../corpus_golden_test.go`). Every entry
is redistributable under a verified license; the license text is in
`LICENSES/`. **No proprietary NextZXOS/ROM is used or shipped** — Next demos
load via the emulator's own ROM-independent `.nex` loader, and the classic
conformance snapshots run on the 48K model with the emulator's embedded
(redistributable) 48K ROM.

Retrieved 2026-07-08. Do not edit the binaries; if a golden changes, it must be
because emulator output changed, not because the program was altered.

## Next hardware demos (ModelNext, `LoadNEX`)

| File | Title / author | License | Source | SHA-256 |
|------|----------------|---------|--------|---------|
| `bin/zxnext_layer2_tilemap.nex` | zxnext_layer2_tilemap — Ben Baker | MIT, README-stated (`LICENSES/zxnext_layer2_tilemap.MIT.txt`) | https://github.com/benbaker76/zxnext_layer2_tilemap | `d1d3316ed8bafa1030584f164885133888f906a691164fa381ad43c692b6e9e9` |
| `bin/zxnext_tilemap.nex` | zxnext_tilemap — Ben Baker | MIT, README-stated (`LICENSES/zxnext_tilemap.MIT.txt`) | https://github.com/benbaker76/zxnext_tilemap | `bdd3086cca92f4a1c53e2cc146a3280dcd6d17060eacae935e96e9edee455b96` |
| `bin/SpecBong.nex` | SpecBong — Peter "Ped" Helcmanovsky | MIT (`LICENSES/SpecBong.MIT.txt`) | https://github.com/ped7g/SpecBong (release Part_12) | `021b85e36c9323ebe22181107f277e0347bce95ac5b06f2b95ffb583b3d7e10f` |

## Classic Z80/ULA conformance tests (Model48K, `LoadSnapshot`)

Kevin Watkins' ZXSpectrumNextTests — hardware conformance oracles that draw a
pass/fail result screen. All MIT (`LICENSES/ZXSpectrumNextTests.MIT.txt`), from
https://github.com/MrKWatkins/ZXSpectrumNextTests (`release/`).

| File | Test | SHA-256 |
|------|------|---------|
| `bin/ccffrm.sna` | CCF/SCF undocumented-flag stability per frame (Ped7g) | `0c11629c6b357c890ff073e91b05725bc00bc8d72cc36ac3bd0e228c35b7c009` |
| `bin/DIHalt.sna` | DI + HALT (halts forever with no NMI) | `644ae437cab4c426a260049487ff93e0a1983807602891273027e3d034fd1397` |
| `bin/z80bltst.sna` | Flags of IM2-interrupted block instructions (Ped7g/D. Banks) | `6d099eef1ed30f6b978cb1f49a21a7d91b66f526531b7d1058eb40f5c6bb24a7` |
| `bin/int_skip.sna` | Interrupt acceptance across long DD/FD prefix blocks (Ped7g) | `cfaeacda9a2266289da6e6f0e8d659b5c2ebef64ec915d7de9b2f3b857121065` |
| `bin/ULAvsSJS.sna` | Keyboard/joystick port read-back matrix (Ped7g) | `be48097a7cc02d4a961103df920202c5fe760558736b03e9a758fd1fe6969372` |

## Hardware exercised

- **zxnext_layer2_tilemap** — Layer 2 256x192 background composited under a
  tilemap layer (a full JRPG-style scene). The richest renderer of the set.
- **zxnext_tilemap** — tilemap layer with scrolling.
- **SpecBong** — hardware sprites + tilemap-text HUD over a Layer 2 playfield;
  captured at its pre-start frame (no input is injected in the slice).

- **ccffrm** — reads **"No error ✓"**: we pass the SCF/CCF undocumented-flag
  (bits 3/5) stability test.
- **DIHalt** — green border (the CPU stays HALTed with interrupts disabled and
  no NMI, as it should): pass.
- **z80bltst** — some pass/fail cells are red. Mixed; captured as-is.
- **int_skip** — reports **`!ERR! allows ISR`** for DD/FD prefix blocks,
  i.e. our CPU appears to accept a maskable interrupt part-way through a long
  chain of DD/FD prefixes, where a Z80 should defer it until the prefixed
  opcode completes. A probable faithfulness bug in interrupt acceptance vs
  prefix handling — flagged for a follow-up hunt; the golden guards against it
  changing further meanwhile.
- **ULAvsSJS** — interactive keyboard test; the captured frame is the idle
  (no-key) state.

> Conformance goldens capture our CURRENT output, not a certificate of
> correctness. A red/`!ERR!` result is a real signal to investigate, and when a
> fix lands the affected golden must be regenerated with `-update`.

## Boot environment (Next demos)

The Next demos load through `Harness.LoadNEX` (parses the `.nex`, pages banks,
sets `PC`/`SP` from the header) — **not** NextZXOS's NEXLOAD. The bottom 16K
holds the emulator's embedded original 48K Sinclair ROM (redistributable under
Amstrad's permission, already vendored in `pkg/roms/data/48.rom`), standing in
for the OS so no proprietary NextZXOS file is used. These programs render
identically with a zero stub ROM — their output comes purely from Next
hardware — so the golden reflects hardware faithfulness, not ROM content.

## License notes

- SpecBong ships a standalone MIT `LICENSE`, reproduced verbatim.
- The two Ben Baker demos state MIT in their README rather than a standalone
  `LICENSE` file. `LICENSES/` reproduces the README statement, the author
  attribution, the credited contributors, and the full MIT text so the
  required notice travels with the binary.

## Adding to the corpus

1. Verify the program's license explicitly permits redistribution; add its
   text to `LICENSES/`.
2. Download the binary to `bin/`, record size + SHA-256 in the table above.
3. Add an entry to `corpusPrograms` in `../../corpus_golden_test.go` and run
   `go test ./pkg/testharness -run TestCorpus -update` to capture its golden.
4. Eyeball the generated `golden/<name>.png` to confirm it renders correctly
   (not garbage) before committing.
