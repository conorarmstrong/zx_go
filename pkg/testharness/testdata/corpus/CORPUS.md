# Real-software compatibility corpus

Third-party ZX Spectrum Next programs, vendored here to boot in CI as a
faithfulness regression suite (see `../../corpus_golden_test.go`). Every entry
is redistributable under a verified license; the license text is in
`LICENSES/`. Binaries are loaded via the emulator's own ROM-independent `.nex`
loader (`Harness.LoadNEX`) — **no proprietary NextZXOS/ROM is used or shipped**.

Retrieved 2026-07-08. Do not edit the binaries; if a golden changes, it must be
because emulator output changed, not because the program was altered.

| File | Title / author | License | Source | SHA-256 |
|------|----------------|---------|--------|---------|
| `bin/zxnext_layer2_tilemap.nex` | zxnext_layer2_tilemap — Ben Baker | MIT, README-stated (`LICENSES/zxnext_layer2_tilemap.MIT.txt`) | https://github.com/benbaker76/zxnext_layer2_tilemap | `d1d3316ed8bafa1030584f164885133888f906a691164fa381ad43c692b6e9e9` |
| `bin/zxnext_tilemap.nex` | zxnext_tilemap — Ben Baker | MIT, README-stated (`LICENSES/zxnext_tilemap.MIT.txt`) | https://github.com/benbaker76/zxnext_tilemap | `bdd3086cca92f4a1c53e2cc146a3280dcd6d17060eacae935e96e9edee455b96` |
| `bin/SpecBong.nex` | SpecBong — Peter "Ped" Helcmanovsky | MIT (`LICENSES/SpecBong.MIT.txt`) | https://github.com/ped7g/SpecBong (release Part_12) | `021b85e36c9323ebe22181107f277e0347bce95ac5b06f2b95ffb583b3d7e10f` |

## Hardware exercised

- **zxnext_layer2_tilemap** — Layer 2 256x192 background composited under a
  tilemap layer (a full JRPG-style scene). The richest renderer of the set.
- **zxnext_tilemap** — tilemap layer with scrolling.
- **SpecBong** — hardware sprites + tilemap-text HUD over a Layer 2 playfield;
  captured at its pre-start frame (no input is injected in the slice).

## Boot environment

Programs load through `Harness.LoadNEX` (parses the `.nex`, pages banks, sets
`PC`/`SP` from the header) — **not** NextZXOS's NEXLOAD. The bottom 16K holds
the emulator's embedded original 48K Sinclair ROM (redistributable under
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
