# timing.md — Interrupt & Frame-Timing Faithfulness Plan

**Goal:** make zx_go's Spectrum-Next interrupt + frame timing *provably equal*
to the FPGA core, in one coherent pass — so **every** INT-timing divergence is
fixed together, not discovered/fixed one boot-symptom at a time.

**Principle (why "one fell swoop" is possible):** interrupt-timing bugs are not
N independent defects. They are a small, *finite* set of wrong parameters in a
**single model**. The FPGA fully specifies that model (`zxnext.vhd` +
`video/zxula_timing.vhd`). Transcribe the whole contract once, conform to it as
a unit, and lock it with an exhaustive conformance suite. The boot-divergence
hunt finds them serially only because each fixed parameter lets the boot run to
the next symptom — fixing the *model* clears all symptoms at once.

**KEY FINDING (2026-06-03):** the INT-timing **model** is now faithful +
TDD-locked (frame length D1/D2, narrow pulse B3/B4, assert/pulse conformance
A1-A4/B1-B2 via `FrameIntTiming`). Wiring it into the Next boot
(`ZX_GO_INT_TIMING=1`, assert=291/pulse=32) and bisecting vs the reference emulator leaves the
first divergence **unchanged at `$3F1B` hit#16** ⇒ **`$3F30`/hit#16 is NOT an
interrupt-timing divergence** — the D9 "INT-acceptance" reading was wrong (the
the reference emulator ISR-stack signature has another cause). The boot blocker is a **value/
path divergence (D6)**, not INT timing. Net value of this work: the INT model
is correct-by-construction and regression-locked, so INT timing is now
*ruled out* and need not be re-chased. The Next wiring stays env-gated
(`ZX_GO_INT_TIMING`) because the frame-origin *offset* (291 vs 0) is
unvalidated — it needs the **deterministic GHDL** reference to pin (the reference emulator is
nondeterministic on INT timing and can't arbitrate the offset).

**Status legend:** ⬜ todo · 🟡 in progress · ✅ done · ⛔ blocked
**Reference of record:** `zxnext.vhd` / `zxula_timing.vhd` (the spec —
deterministic). the reference emulator is a *secondary* check only and is **run-to-run
nondeterministic on INT timing** — never the source of truth for it. The GHDL
gate-oracle (Tool #3, in progress) is the deterministic *running* reference for
the final total-differential.

---

## 1. The complete timing model (extracted from the FPGA — the closed parameter set)

Everything that decides *whether/when* an interrupt is taken is this list. Get
these right ⇒ done.

### 1a. Maskable frame INT (`int_ula`)
- Asserted for **one 7 MHz tick** when `hc == c_int_h AND vc == c_int_v`
  (`zxula_timing.vhd:551`). `hc`/`vc` are the **7 MHz** video counters
  (independent of CPU turbo speed).
- Per machine timing (`NR$03` bits 2:0), the assert coordinate + frame geometry
  (`zxula_timing.vhd:155-298`):

  | Mode      | `c_int_h` | `c_int_v` | `c_max_hc` (cols-1) | `c_max_vc` (lines-1) |
  |-----------|-----------|-----------|---------------------|----------------------|
  | 48K       | 439 (`448+3-12`) | 319 | 447 | 319 |
  | 128K 50Hz | 128 (`136+4-12`) | 1   | 455 | 310 |
  | +3 50Hz   | 126 (`136+2-12`) | 1   | 455 | 310 |
  | 128K 60Hz | 128       | 0         | 455 | 263 |
  | +3 60Hz   | 126       | 0         | 455 | 263 |
  | Pentagon  | 116 (`128+0-12`) | 0  | 447 | 311 |

  Frame INT tstate (3.5 MHz) ≈ `(c_int_v*(c_max_hc+1) + c_int_h) / 2`.
  *(NextZXOS boot runs +3 timing — `NR$03` default `"011"`, `zxnext.vhd:1099`.)*

### 1b. Line INT (`int_line`, NR$22/$23)
- Asserted for one 7 MHz tick when `hc_ula == 255 AND cvc == int_line_num`
  (`zxula_timing.vhd:577`), where `int_line_num = (NR$23==0 ? c_max_vc : NR$23-1)`
  (`:566-569`). Semantics: fires **before** the target line is drawn.
- Enable = `NR$22` bit 1 (`nr_22_line_interrupt_en`); line number = 9-bit
  `NR$23` (`:5297-5301`).

### 1c. INT pulse width (`pulse_count`)
- `pulse = int_ula OR int_line` sets `pulse_int_n=0`; held for **32 CPU cycles
  (48K/+3) / 36 CPU cycles (128K/Pentagon)** then released
  (`zxnext.vhd:2014-2033`). Counted on **`i_CLK_CPU`** (turbo-scaled), sampled
  on its rising edge (`:2019`). ⇒ a `DI` that covers the pulse **misses** the
  INT entirely.

### 1d. IM2 vs pulse mode
- `z80_int_n = pulse_int_n AND im2_int_n` (`zxnext.vhd:1840`). `NR$C0` bit
  (`nr_c0_int_mode_pulse_0_im2_1`) selects classic pulse (0) vs IM2 device
  vectoring (1). IM2 sources: UART tx, `ula_int`, CTC, line_int, etc.
  (`:1941-1949`). Boot uses pulse/IM1; IM2 is a later concern.

### 1e. CPU speed scaling (NR$07)
- Video `hc/vc` run at 7 MHz **always**; the CPU runs at 3.5/7/14/28 MHz
  (`NR$07` bits 1:0). ⇒ instructions-per-frame scale ×1/2/4/8; the INT lands at
  the same *wall-clock* tstate but a different *instruction* depending on speed.
  Contention disabled at >3.5 MHz and on Pentagon (`zxnext.vhd:4481`).

### 1f. CPU acceptance rules (Z80 core — already mostly modelled)
- Sample at instruction boundary; honour 1-instruction **EI delay**; `DI`
  clears IFF1; **HALT** wakes on INT; IM0/1/2 acceptance latency + pushes.

---

## 2. Our current model + identified gaps

`pkg/z80/z80.go:625` `ExecuteFrame`:
- **GAP-A (assert point):** asserts INT at **frame start (tstate 0)**, not at
  `(c_int_h,c_int_v)`. → INT lands on the wrong instruction near frame edges.
- **GAP-B (pulse width):** holds INT **the whole frame**, not 32/36 CPU cycles.
  → a DI-covered pulse is *missed* on HW but *taken late* by us (stale INT), and
  vice-versa. **This is the prime suspect for the `$3F30` (D9) divergence.**
- **GAP-C (speed scaling):** budget = `tstatesPerFrame × SpeedMultiplier`
  (`:630`) — verify the multiplier + frame length per machine-timing mode and
  that the INT-assert tstate is *not* scaled by turbo (video is 7 MHz fixed).
- **GAP-D (frame length):** `tstatesPerFrame` constant — confirm per mode
  (`c_max_hc`,`c_max_vc` give the true frame length; 48K 69888 vs 128K 70908 vs
  +3 70908 vs Pentagon 71680 — verify against the table above).
- **GAP-E (line INT):** `LineIntOffsetTstates` — verify against the
  `hc_ula==255 && cvc==NR$23-1` rule.
- Already-correct: EI-delay (`:685`), HALT-wake (`HaltWakeOnInt`), one-INT-per-
  frame latch.

---

## 3. Test strategy

Three layers, spec-first (no flaky oracle on the critical path):

**L1 — Spec-derived unit conformance (boot-independent, exhaustive).**
Pure-Go tests that drive the INT model directly over its *entire* input space
and assert against the VHDL-derived constants. Because the input space is small
and finite, L1 *is* the "all at once" coverage. TDD: each matrix row is a test.

**L2 — Differential INT-acceptance trace (enumerates any residual in one run).**
Instrument `c.interrupt()` to log `(frame#, tstate-in-frame, interrupted-PC,
which-INT)`. Run the boot; the per-frame INT-landing stream is compared to the
deterministic reference (GHDL when ready; meanwhile checked against the L1
model). Discrepancies *cluster* by root parameter → self-diagnosing.

**L3 — Boot lockstep / bisection (regression confirmation only).**
After L1+L2 pass, the existing `--next-bisect` / `--next-lockstep` should show
the `$3F30` family of divergences gone. Used to *confirm*, not to *discover*.

**Determinism rule:** truth = `zxnext.vhd`/`zxula_timing.vhd` and GHDL. the reference emulator
INT-timing comparisons are advisory only (documented nondeterminism).

---

## 4. TEST MATRIX

> Columns: **ID** · **What** · **Condition** · **Expected (spec)** · **Source** ·
> **Layer** · **Status**

### A. Frame-INT assert point
| ID | What | Condition | Expected | Source | Layer | Status |
|----|------|-----------|----------|--------|-------|--------|
| A1 | INT assert tstate | +3 50Hz | 291 = (1·456+126)/2 | zxula_timing:189,199 | L1 | ✅ `TestFrameIntTiming` (FrameIntTiming) |
| A2 | …128K 50Hz | 128K | 292 = (1·456+128)/2 | :187,199 | L1 | ✅ `TestFrameIntTiming` |
| A3 | …48K | 48K | 71675 = (319·448+439)/2 | :155,163 | L1 | ✅ `TestFrameIntTiming` |
| A4 | …Pentagon | Pentagon | 58 = 116/2 | :257,265 | L1 | ✅ `TestFrameIntTiming` |
| A5 | INT-assert tstate NOT scaled by turbo | NR$07=28MHz | same wall-clock tstate as 3.5MHz | §1e | L1 | ⬜ |
| A6 | exactly one frame-INT pulse / frame | any | 1 | :551 | L1 | ⬜ |

### B. INT pulse width / miss-if-DI
| ID | What | Condition | Expected | Source | Layer | Status |
|----|------|-----------|----------|--------|-------|--------|
| B1 | pulse width | +3/48K | 32 CPU cycles | zxnext:2014 | L1 | ✅ `TestFrameIntTiming` |
| B2 | pulse width | 128K/Pentagon | 36 CPU cycles | zxnext:2015,2033 | L1 | ✅ `TestFrameIntTiming` |
| B3 | DI across whole pulse ⇒ INT **missed** | IFF1=0 during pulse, EI after | no INT that frame | §1c | L1 | ✅ `TestFrameInt_NarrowPulse_MissedWhenDIAcrossPulse` |
| B4 | EI mid-pulse ⇒ INT taken once | EI while pulse active | exactly 1 | :2024 | L1 | ✅ `TestFrameInt_NarrowPulse_TakenWhenEnabledDuringPulse` |
| B5 | pulse measured in CPU cycles (scales with turbo) | 28MHz | 32 CPU cyc = 4 ULA-tstate | :2035 | L1 | ⬜ |
| B6 | NR$C0 read bit7 = `not pulse_int_n` (line live) | during pulse | bit7=1 | zxnext:5992 | L1 | ⬜ |

### C. Line interrupt (NR$22/$23)
| ID | What | Condition | Expected | Source | Layer | Status |
|----|------|-----------|----------|--------|-------|--------|
| C1 | line INT fires at `hc=255,cvc=NR$23-1` | NR$22.1=1 | assert | zxula_timing:577 | L1 | ⬜ |
| C2 | NR$23=0 ⇒ line `c_max_vc` | NR$22.1=1,NR$23=0 | last line | :566 | L1 | ⬜ |
| C3 | disabled when NR$22.1=0 | NR$22.1=0 | no line INT | :577 | L1 | ⬜ |
| C4 | line + frame INT share the one pulse latch | both due | single pulse | zxnext:1941 | L1 | ⬜ |
| C5 | 9-bit line number (NR$23 bit8 from NR$22.0) | NR$23>255 | 9-bit compare | :5298 | L1 | ⬜ |

### D. Frame geometry / speed
| ID | What | Condition | Expected | Source | Layer | Status |
|----|------|-----------|----------|--------|-------|--------|
| D1 | frame length +3/128K | 50Hz | 70908 = (456·311)/2 | §1a | L1 | ✅ `TestFrameTStatesForModel` + headless/GUI now use it |
| D2 | frame length 48K | — | 69888 | §1a | L1 | ✅ `TestFrameTStatesForModel` |
| D3 | frame length Pentagon | — | 71680 | §1a | L1 | ⬜ |
| D4 | 60Hz variants line counts | NR$05 60Hz | c_max_vc=263 | :238,298 | L1 | ⬜ |
| D5 | SpeedMultiplier 1/2/4/8 | NR$07=0..3 | ×1/2/4/8 instr/frame | §1e | L1 | ⬜ |
| D6 | machine-timing follows NR$03 | NR$03 writes | timing switches | zxnext:1624 | L1 | ⬜ |

### E. Z80 acceptance edges (mostly done — verify, add missing)
| ID | What | Condition | Expected | Source | Layer | Status |
|----|------|-----------|----------|--------|-------|--------|
| E1 | EI delays INT 1 instruction | EI;… | take after next insn | z80.go:685 | L1 | ✅(verify) |
| E2 | DI ⇒ no INT | DI | none | core | L1 | ✅(verify) |
| E3 | HALT wakes on INT | DI;HALT / EI;HALT | wake | HaltWakeOnInt | L1 | ✅(verify) |
| E4 | IM1 accept = push PC, jump $0038, 13T | IM1 | spec | core | L1 | ⬜ |
| E5 | IM2 accept = vector from (I<<8)|bus | IM2 | spec | zxnext:1840 | L1 | ⬜(boot N/A) |
| E6 | INT not sampled between prefix+opcode | DD/FD/ED | atomic | iter272 | L1 | ✅(verify) |

### F. Differential / boot (discovery + regression)
| ID | What | Condition | Expected | Source | Layer | Status |
|----|------|-----------|----------|--------|-------|--------|
| F1 | INT-acceptance trace hook (frame,tstate,PC) | boot | logs every INT | new | L2 | ⬜ |
| F2 | total INT-landing diff vs GHDL | boot | empty | Tool#3 | L2 | ⛔(GHDL) |
| F3 | `$3F30` (D9) divergence heals | post-fix bisect | gone / moves later | --next-bisect | L3 | ❌ **NOT healed** — spec timing (assert=291, pulse=32, `ZX_GO_INT_TIMING=1`) leaves the bisect at **hit#16 with identical regs** ⇒ `$3F30` is **NOT** INT-timing (D9 was a misread). Real blocker = a value/path divergence (D6). |
| F4 | INT-suppressed bisect partitions bugs | FrameIntDisabled both | INT-class vanishes | z80.go | L3 | ⬜ |

---

## 5. Execution phases (work through in order)

- **P0 — Spec extraction** ✅ (this doc, §1; constants pulled from VHDL).
- **P1 — L1 conformance scaffold + RED tests** 🟡 — B3/B4 ✅ (miss-if-DI +
  taken-during-pulse, `pkg/z80/int_timing_test.go`). TODO: B1/B2 (assert the
  32/36 width per mode), A1-A6 (assert coordinate).
- **P2 — Reimplement the INT generator** 🟡 — **narrow-pulse mechanism
  landed** in `ExecuteFrame` (`z80.go`: `IntAssertTstate`/`IntPulseTstates`,
  assert-at-window-start + one-shot withdraw; gated on `IntPulseTstates>0` so
  classic models are byte-identical — full `pkg/z80` suite green). TODO:
  (a) mirror the same window logic into `StepInstructionWithIRQ` (the
  lockstep/bisect path, so the boot diff reflects the fix); (b) Next wiring —
  compute `IntAssertTstate` from NR$03 (hc/vc → tstate) and set
  `IntPulseTstates` = 32 (+3/48K) / 36 (128K/Pentagon), scaled by CPU clock.
- **P2.5 — FRAME-LENGTH BUG FIXED (D1/D2)** ✅ — the headless + GUI
  `ExecuteFrame` loops hardcoded the 48K `69888` for **every** model incl. the
  Next, drifting the maskable INT ~1020 T-states/frame vs the FPGA AND
  disagreeing with `StepInstructionWithIRQ`'s `70908`. Added
  `frameTStatesForModel` (`main.go`, TDD `frametiming_test.go`); threaded an
  `emulator.model` field; both loops now use 70908 for Next/128K-family.
  Measured effect on the boot: soft-reset count ~unchanged (610→614) — a real
  correctness fix but NOT the boot-healer (the soft-reset loop is downstream of
  the `$3F30` INT-*acceptance* divergence). The narrow-pulse mechanism
  (`frameIntPulse`) is now in BOTH INT paths (ExecuteFrame +
  StepInstructionWithIRQ), gated, no regression.
- **P3 — Line INT (C1-C5) + remaining geometry/speed (D3-D6)** conformance +
  fixes; then **wire** `IntAssertTstate`/`IntPulseTstates` for the Next boot
  (the empirical-offset step: frame-origin alignment vs the reference emulator needs the
  bisect to tune — `$3F30` heals iff the pulse window lands where the reference emulator's
  does). A/B1-B2 rows land with that wiring.
- **P4 — L2 INT-acceptance trace hook (F1)**; run boot, eyeball INT landings vs
  the L1 model; fix residuals.
- **P5 — Regression (F3/F4)**: re-run `--next-bisect`; confirm the `$3F30`
  family is gone or has moved materially later. Update the development log.
- **P6 — GHDL total diff (F2)** when Tool #3 lands — the final proof.

Each P-step: RED test → minimal faithful impl → GREEN → no regression in
`pkg/z80` / `pkg/next`. No hacks; every value traces to a VHDL line above.
