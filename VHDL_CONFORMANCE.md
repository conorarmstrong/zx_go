# VHDL → emulator conformance matrix

**Scope:** this matrix covers the Spectrum **Next** FPGA core
(`zxnext.vhd` + `video/zxula_timing.vhd`) only. The other machines zx_go
emulates (48K…+3, ZX80/ZX81, Pentagon 128) have no FPGA reference and are
tracked via their own tests, README and CHANGELOG, not here.

**Premise (the reason this file exists):** a truly hardware-accurate Spectrum
Next emulator MUST boot the firmware that runs on the real hardware. zx_go
cold-boots NextZXOS end-to-end (welcome → menu → Browser/NextBASIC; v1.2.0) —
which it reached by closing exactly the kind of inaccuracies this file
enumerates. Per-feature "validated against the VHDL" claims are **spot
checks**; they do not prove conformance of the whole surface. This matrix
replaces spot-checking with **enumeration**: list every aspect of
the FPGA core (`_tools/reference/tbblue-fpga/cores/zxnext/src/zxnext.vhd` +
`video/zxula_timing.vhd`), map each to our implementation and a test, and tick
it off only when a test pins it to the VHDL value. A green row = that aspect is
conformant. The boot is the integration test that the rows are *complete*.

Status legend: ✅ test pins it to VHDL · ⚠️ partial / value differs / untested ·
❌ gap (VHDL feature with no faithful impl) · — n/a.

How a gap was already found by this method: `NR$6E/$6F` reset default. We had a
test for the bit-6 **write** mask (iter 205) but never for the **reset value**.
VHDL resets them to `$2C/$0C`; we defaulted to `$00`. The nrdiff vs the reference emulator at
`$3F1B` flagged it; the matrix would have flagged it without an oracle.

---

## Axis 1 — Initialization / reset defaults  (source: zxnext.vhd reset block ~4900-5111)

Every `nr_XX_* <= value` in the reset process. Read-back byte composed per the
`port_253b_dat` mux. Tests: `pkg/next/nextregs/dispatcher_test.go`
`TestNewDispatcherDefaults` (power-on) + `TestResetRestoresDefaults` (Reset()).

| NR | VHDL reset | ours | status | notes / VHDL line |
|----|-----------|------|--------|-------------------|
| $00 machine id | $0A issue-2 | $0A | ✅ | zxnext_top_issue2.vhd:35; $08 'emulator' made ROM1 $1E69 fork (D30f) — TestMachineID_MatchesFPGA |
| $01 core ver | (ver) | defaultCoreVersion | ✅ | |
| $02 reset | $01* | $01 | ✅ | *bit0 "last=soft"; matches the reference emulator |
| $05 periph1 | $01 | $01 | ✅ | 50 Hz |
| $06 periph2 | $A0 (hotkey en 7,5) | $A0 | ✅ | zxnext.vhd:5161-5165 — FIXED (was $00, untracked by the matrix) |
| $08 periph3 | $10 | $10 | ✅ | AY enable |
| $0B joy iomode | bit0=1 | ⚠️ | ⚠️ | read-back composition unverified |
| $10 core id | $01 (vhd) | $00 | ⚠️ | $00 matches the reference emulator (boots); vhd=$01. AND $03 @ $017E |
| $12 L2 bank | $08 | $08 | ✅ | |
| $13 L2 shadow | $0B | $0B | ✅ | |
| $14 transp rgb | $E3 | $E3 | ✅ | |
| $18-$1B clip | L2/spr/ULA {00,FF,00,BF}; tm {00,9F,00,FF} | WireClipWindows | ✅ | verified: reset coords + index match zxnext.vhd:4959-4982 (clipWindow.def, OnRead/OnWrite, onReset) |
| $42 ulanext fmt | **$07** | **$07** | ✅ | FIXED this session (was $0F) — zxnext.vhd:5002 |
| $4A fallback | **$E3** | **$E3** | ✅ | FIXED this session (was $00) — zxnext.vhd:5014 |
| $4B sprite transp | $E3 | $E3 | ✅ | |
| $4C tm transp | $0F | $0F | ✅ | |
| $6E tilemap base | **$2C** | **$2C** | ✅ | FIXED this session (was $00) — zxnext.vhd:5042 |
| $6F tilemap tiles | **$0C** | **$0C** | ✅ | FIXED this session (was $00) — zxnext.vhd:5045 |
| $68 ula control | reset ula_en=1; read bit7 = ¬ula_en = 0 | $00 | ✅ | NOT a gap (earlier misread): zxnext.vhd:5026/5445 invert bit7 on write (nr_68_ula_en ⇐ ¬wr(7)), so the reset read-back is $00 |
| $98 pi gpio o | $FF | $FF | ✅ | zxnext.vhd:5070 — FIXED (reset default) |
| $99 pi gpio o | $01 | $01 | ✅ | zxnext.vhd:5071 — FIXED |
| $A9 esp gpio0 | 1 | $00 | ⚠️ | composed; nrdiff showed ours $02 — verify read |
| i2c $103B/$113B | SCL/SDA latches, open-drain | rtc.Bus + ULA dispatch | ✅ | zxnext.vhd:2630/3234 — TestI2C_* + TestI2CPortRouting (D31ai) |
| $8C alt-rom | reset: 7:4←3:0 | promote in WireReset | ✅ | zxnext.vhd:2255 staged-nibble promote (both reset types) — TestWireResetPromotesAltROMStagedNibble (D31g) |
| (rom3 automap gate) | (altrom_en∧alt_128_n)∨(rom3∧¬altrom_en) | Memory.DivMMCRom3Gate | ✅ | zxnext.vhd:3138 full gate — TestDivMMCRom3Gate (D31g) |
| $B8 divmmc ep0 | $83 | $83 | ✅ | |
| $B9 divmmc ep_valid | $01 | $01 | ✅ | divmmc.New seeds epValid0=$01 (verified 2026-06-05); soft reset re-arms via WireReset |
| $BA divmmc ep_timing | $00 | $00 | ✅ | |
| $BB divmmc ep1 | $CD | $CD | ✅ | |
| $C4 int en 0 | bit expbus=1 | $00 | ❌ | **GAP**: NR$C4 expbus int enable resets to 1 |
| $C0 im2/nmi | $00 | $00 | ✅ | |
| (all others) | $00 | $00 | ✅ | clip/scroll/copper/dma-int reset to 0 |

**Axis 1 remaining gaps to close:** NR$C4 (expbus bit 7 resets to 1, but its
read-back is composed with the ULA/line int-enable bits — needs the composed
mux, not a bare default), NR$0B/$A9 (composed). None are boot-blocking. (NR$98/
$99 fixed this pass; NR$68 was a misread — already conformant.)
**Action:** extend the reset test to assert the FULL vhd reset vector incl.
composed read-backs.

---

## Axis 2 — NextReg write semantics (masks + side effects)
Source: zxnext.vhd `nr_wr_en` case (~5113+). Tests: `pkg/next/wire_specderived_test.go`,
`wire_*_test.go` (iters 186-250). Status: **broad ✅** (reserved-bit masks for
$0A,$22,$34,$1C,$12/$13,$2F,$6A,$6E/$6F,$70/$71,$4A-$4C,$CE/$D8/$DA; NR$8E paging;
$80-$8A bus enables; $C0/$C4/$C6; $CD). **Gap:** no single test enumerates ALL
256 write-maskable bits vs the VHDL case — spot-covered, not exhaustive.

## Axis 3 — NextReg read-back (port_253b_dat mux, zxnext.vhd ~5890-6250)
Tests: read-shape fixes for $07 (iter 223), $00 machine-id, $06, $41/$44 palette,
clip windows. Tool: `--next-nrdiff` (CAVEAT: the reference emulator returns $00 for unimplemented
read-backs — verify vs VHDL, never blind-match). **Gap:** ~30 composed read-backs
($68,$69,$C0,$C4,$C6,$CC-$CE,$A9,$0B,...) not individually pinned to the VHDL mux.

## Axis 4 — Ports / IO decode
Source: zxnext.vhd port decode (`port_*`). Tests: scattered. **Confirmed gap this
session:** port **$FF** (Timex/SCLD) — bit6 = ULA-frame-INT disable
(`port_ff_interrupt_disable`, vhd 3635/6711/6750) is **not implemented** in
WritePort. ⚠️ $FE,$7FFD,$1FFD,$243B/$253B,$E3/$E7/$EB,$6B,$DFFD,AY ports present
but no port-by-port VHDL decode conformance test.

## Axis 5 — Interrupts / timing  (zxula_timing.vhd + zxnext.vhd 2014-2033)
Tests: `pkg/z80/int_timing_test.go` (narrow pulse, DI-across-pulse, speed-scaled
frame boundary), `pkg/next/inttiming_test.go` (per-mode assert tstate + pulse).
Fixed this session: **StepInstructionWithIRQ 28 MHz frame boundary not
SpeedMultiplier-scaled (8× over-fire)**; narrow-pulse default. ⚠️ frame-ORIGIN
offset (CPU tstate=0 ↔ ULA hc0/vc0) unvalidated; line-INT at turbo; IM2 vector
table; NR$22/$C0/$C4/$C6 enable gates not all wired to the INT generator.

## Axis 6 — Z80 / Z80N operations  (FUSE + Sean Young + GHDL gate oracle)
Tests: canonical T-state tables (iter 266-270), per-op timing batches, flags
(SCF/CCF/CPL), WZ/MEMPTR for every group, R-register, IM/NEG/RETI mirrors, SLL,
INT atomicity, 28 MHz M1 wait. **GHDL gate-oracle: bit-exact over 363 bootrom
insns** ([[project_next_cpu_faithful]]). Status: **strongest axis (✅).**

## Axis 7 — Memory / paging
MMU8 ($50-$57) defaults ✅ (this-session $DE/$DF fix), divMMC overlay, config-mode
RAMPAGE (NR$04), alt-ROM (NR$8C), 7FFD/1FFD. ⚠️ no end-to-end paging conformance
matrix vs the VHDL mux priority.

## Axis 8 — divMMC / SD
automap triggers (rom3/delayed variants, $3DXX gate), SPI, CSD v1/v2. The
`$2401` divMMC-RAM NOP-slide that once blocked the cold boot is **closed** — the
Next now boots NextZXOS end-to-end from the SD image (`TestNextRealROMBoot`).
⚠️ Remaining: no port-by-port conformance test pins the automap trigger variants
or the SPI / CSD command set to the VHDL.

## Axis 9 — Video (ULA/L2/tilemap/sprite/lores/palette/copper)
Broad render edge tests (iters 204-217). Not boot-critical (boot stalls pre-render).

---

## Method to complete the matrix
1. Per axis, extract the VHDL truth into a table (above).
2. For each row write/extend a test that pins our value to the VHDL value.
3. Tick ✅ only when green. Fix ❌/⚠️ rows (TDD).
4. Re-run the cold boot after each axis — when the last real gap closes, it boots.
This is parallelizable per axis (a fan-out audit) if scaled up.
