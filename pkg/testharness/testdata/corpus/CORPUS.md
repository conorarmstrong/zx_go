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

`bin/int_skip.sna` is booted twice: once on the default 48K held-INT model
(`mrk_int_skip`) and once with a faithful ~32T narrow /INT pulse
(`mrk_int_skip_narrowint`) — the timing the 128K/+3/Next use. The narrow-pulse
run is the integration guard for the `pkg/z80` frameIntPulse fix (see below).

## zxnDMA conformance (Next, tape-loaded)

| File | Test | SHA-256 |
|------|------|---------|
| `bin/zilogdma.tap` | Zilog/Z80 DMA transfer modes + timing (MrKWatkins) | `00fce5ea0a8331d435c67a9950b62d232ac7ade831125993107eb19881a645a8` |

`mrk_zilogdma` runs on **ModelNext** — the machine that has the zxnDMA the test
drives. The Next boots to 48K BASIC on the embedded 48K ROM (no proprietary
NextZXOS), the corpus types `LOAD""`, and `Harness.LoadTAP` injects each tape
block through a fast-load trap on the ROM's LD-BYTES ($0556). The test then
runs on the real DMA engine and draws the A->B / B->A transfer-mode grids and
the DMA timing rows. MIT (`LICENSES/ZXSpectrumNextTests.MIT.txt`).

The golden shows **red cells** — investigated 2026-07-09, three real zxnDMA
gaps (not expected patterns), confirmed against `zxnext.vhd` and the test's own
documented "TBBLue zxnDMA core 3.1.5" reference:

1. **Port $0B is not decoded.** The test defaults to DMA port **$0B**; our ULA
   routes the DMA only at **$6B** (`pkg/ula/ula.go` `(addr&0xFF)==0x6B`), so at
   $0B every read returns floating `$FF` and no transfer happens (the default
   golden's grids are mostly red). The FPGA decodes BOTH — `port_dma_6b_io_en`
   (enable bit 5) and `port_dma_0b_io_en` (enable bit 25) — and the port used
   selects `dma_mode` (`dma_mode <= port_0b_lsb`; `0 = zxn dma, 1 = z80 dma`,
   zxnext.vhd:1778/1817). We model neither the $0B decode nor `dma_mode`.
2. **Read-sequence address readback is one low.** Cycled to $6B, the readback
   reads `3A 00 1A030329 1A 1A03D003 1A 1A03D306` vs the FPGA reference
   `3A 3A 1A03042A 1A 1A03D104 1A 1A03D508`: the portA/portB LSBs are each 1
   below the FPGA (`03/29` vs `04/2A`, …). The ReadMe documents the zxnDMA
   reporting the destination address as start+N+1; our read-SEQUENCE path
   reports start+N. (Our `dma_readback_test.go` only covers the simpler counter
   readback, which is correct — this read-sequence path was uncovered.) The
   off-by-one also lands chained transfers one byte short, so cells go red at
   $6B too.
3. **Read-status-byte returns `00` instead of `3A`** (the `SS` field).

The golden captures this current behaviour as a regression guard; fixing the
three gaps is scoped as a separate zxnDMA change (the DMA core is otherwise
FPGA-golden-verified, so it needs care not to regress the read-mask/counter
paths).

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
- **int_skip** — reports **`!ERR! allows ISR`** for DD/FD prefix blocks.
  Investigated (2026-07-09): the original "interrupt accepted mid-prefix-chain"
  hypothesis was DISPROVEN — the model executes a chained DD/FD block as one
  atomic instruction (confirmed), so an interrupt cannot be taken mid-chain.
  The real cause is the classic 48K interrupt model: it uses the deliberate
  legacy "held-INT for the whole frame" approximation (`cmd/zx_go/main.go`,
  `IntPulseTstates == 0`), whereas the test expects a ~32T narrow /INT pulse
  that a long prefix block would span and lose. Making it pass would require
  migrating the classic 48K/128K INT model to a narrow pulse AND finer INT
  sampling across atomic instructions — a broad change to a proven,
  FPGA-bit-exact CPU that a conscious decision currently avoids. Left as a
  documented limitation; the golden guards the current behaviour. A related
  genuine narrow-pulse bug found during this hunt (an atomic instruction
  spanning the pulse window raised the INT late) WAS fixed in
  `pkg/z80` `frameIntPulse` — it improves the Next/128K/+3 narrow-pulse models
  but does not change this 48K golden.
- **int_skip (narrow /INT)** — the same test with a faithful ~32T narrow /INT
  pulse (`intAssert=58`, `intPulse=32`) instead of the held-INT approximation.
  Here the DD/FD/DDFD prefix blocks read **`0 !OK inhibits ISR`** — the correct
  hardware result — because the atomic prefix block spans the narrow pulse and
  the interrupt is lost, exactly as the frameIntPulse window fix models. This
  golden is the end-to-end guard for that fix: reverting it flips the blocks
  back to `43 !ERR! allows ISR` (verified). It represents the interrupt
  behaviour of the 128K/+3/Next, which the 48K memory model can host because
  int_skip is a ZX48/ZX128 program (a real 128K memory model renders 48K
  snapshots as garbage, so the memory stays 48K while only the INT timing is
  switched to the narrow pulse those machines use).
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
