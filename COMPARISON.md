# How zx_go compares to other ZX Spectrum Next emulators

This document compares **zx_go** with the three most prominent ZX Spectrum Next
emulators: **CSpect**, **ZEsarUX**, and **MAME** (its `tbblue` driver).

It is written and maintained by the zx_go project, but the goal is a *fair* and
*accurate* picture, not a sales pitch. Two honesty notes up front:

- **Feature presence ≠ maturity.** A green check means "implemented," not "as
  battle-tested as a tool that thousands of people have shipped games on for
  years." CSpect and ZEsarUX have large user bases and a long track record;
  MAME is the community's accuracy/preservation reference. **zx_go is young**
  and its focus to date has been *reference-faithful OS boot* and hardware
  correctness rather than broad commercial-title compatibility.
- **Data is as of mid-2026**, gathered from each project's public sources
  (repositories, official docs, the SpecNext community wiki, and DeZog's
  documentation). Where a fact could not be confirmed from public sources it is
  marked **❔**, not guessed. Corrections via pull request are welcome.

## Legend

| Symbol | Meaning |
|:---:|---|
| ✅ | Implemented / yes / full |
| ⚠️ | Partial, approximate, or with notable caveats |
| ❌ | Not implemented / no |
| ❔ | Could not be verified from public sources |

Columns: **zx_go** · **CSpect** · **ZEsarUX** · **MAME** (`tbblue` driver).

---

## 1. Project & licensing

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| **Open source** | ✅ | ❌ | ✅ | ✅ |
| License | MIT ¹ | Proprietary freeware ² | GPL-3.0 | GPL-2.0+/BSD-3 |
| Source publicly available | ✅ | ❌ | ✅ | ✅ |
| Redistribution permitted | ✅ | ❌ | ✅ | ✅ |
| Implementation language | Go | C# / .NET | C | C++ |
| Cost | Free | "Name your price" | Free | Free |

¹ Project code is MIT; the bundled FPGA boot ROM (`tbblue_loader.rom`) is GPLv3.
The licensed NextZXOS system ROMs are **not** bundled — see §6.
² CSpect is a closed-source personal "devkit"; the author states there is no
support and that redistribution is not permitted.

---

## 2. Platforms

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| Windows | ✅ | ✅ (native) | ✅ | ✅ (official binaries) |
| macOS | ✅ | ⚠️ (via Mono) | ✅ | ⚠️ (from source) |
| Linux | ✅ | ⚠️ (via Mono) | ✅ | ⚠️ (from source) |
| Native ARM (Apple Silicon / Pi) | ✅ ³ | ❌ (Mono only) | ✅ | ✅ ⁴ |
| Other (BSD/Haiku/Docker) | ❌ | ❌ | ✅ | ⚠️ |

³ Native binaries for Linux arm64 and macOS arm64; Windows arm64 is experimental.
⁴ MAME has an ARMv8 recompiler back-end and official Windows-ARM64 binaries.

---

## 3. Machines emulated

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| ZX Spectrum 48K | ✅ | ✅ | ✅ | ✅ |
| 128K / +2 | ✅ | ✅ | ✅ | ✅ |
| +2A / +3 (with disk) | ✅ | ❔ | ⚠️ ⁵ | ✅ |
| ZX Spectrum Next | ✅ | ✅ | ✅ | ✅ |
| ZX80 / ZX81 | ✅ ⁷ | ❌ | ✅ | ✅ |
| Pentagon 128 | ✅ ⁹ | ❌ | ✅ | ✅ |
| SAM Coupé | ✅ ¹⁰ | ❌ | ✅ | ✅ |
| Other systems (CPC, MSX, QL, …) | ❌ | ❌ | ✅ | ✅ ⁶ |

⁵ ZEsarUX emulates +2A/+3 but disk support is DSK-focused.
⁶ MAME is a multi-system emulator covering tens of thousands of machines;
ZEsarUX covers a very wide range of 8-bit/16-bit home computers. zx_go and
CSpect are focused on the Spectrum family / Next.
⁹ Pentagon 128: 128K paging + AY with no memory contention and the 71680-T
Pentagon frame; boots its editor ROM to the 128 menu and runs 128/48 BASIC. The
TR-DOS / Beta-disk interface (WD1793 + auto-paging TR-DOS ROM) loads `.trd`
images on the Pentagon (and the 48K/128K); `.scl` is not yet supported.
⁷ zx_go runs the genuine ZX80 (4K) and ZX81 (8K) ROMs with a faithful
CPU-generated display, so each machine uses its own native keyword layout
(e.g. PRINT is on O for the ZX80, P for the ZX81). `.P` / `.O` programs load.
¹⁰ SAM Coupé (MGT, 1989): boots the bundled MGT ROM 3.0 to SAM BASIC; all four
screen modes, the 128-colour palette + CLUT, SAA1099 stereo sound, the WD1772
floppy with MGT/SAD images (real disk games boot — verified with Manic Miner and
Tetris), ASIC memory/IO contention, the light-pen registers, and the frame/line
interrupts. `--sam` or **Machine → SAM Coupé**. See `docs/sam-coupe.md`.

---

## 4. Spectrum Next video hardware

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| Layer 2 (256×192) | ✅ | ✅ | ✅ | ✅ |
| Layer 2 hi-res (320×256 / 640×256) | ✅ | ✅ | ✅ | ✅ |
| Sprites (4bpp / 8bpp, scaling, mirror/rotate) | ✅ | ✅ | ✅ | ✅ |
| Sprite anchor / relative groups | ✅ | ✅ | ✅ | ✅ |
| **Sprite collision (`$303B`)** | ✅ | ❌ | ❔ | ✅ ⁷ |
| Tilemap (incl. text mode) | ✅ | ✅ | ✅ | ✅ |
| Copper | ✅ ⁸ | ⚠️ | ⚠️ | ✅ |
| 9-bit palettes / ULANext | ✅ ⁹ | ⚠️ | ✅ | ✅ |
| Compositor & per-layer priority | ✅ | ⚠️ | ⚠️ | ⚠️ |

⁷ MAME implements the collision bit; the "max sprites per line" status bit is a
documented TODO. zx_go implements collision and the max-per-line status flag;
the per-line *bandwidth* render limit is visual-only and deferred.
⁸ Copper in zx_go is driven from a per-T-state beam-position model but rendered
per-scanline (full per-pixel hpos deferred). CSpect/ZEsarUX Copper timing is
documented as sub-scanline-imprecise.
⁹ ULANext palette handling in zx_go is wired for the static-palette paths the OS
boot exercises; per-scanline ULA palette writes are deferred.

> All four emulators describe their Next graphics pipeline as a work in
> progress; cross-emulator and on-hardware verification is the norm.

---

## 5. CPU & timing accuracy

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| Full Z80 + undocumented opcodes | ✅ | ✅ | ✅ | ✅ |
| Z80N (Next) extended opcodes | ✅ | ✅ | ⚠️ ¹⁰ | ✅ |
| Turbo clocks (3.5/7/14/28 MHz) | ✅ | ✅ | ✅ | ✅ |
| T-state / cycle accuracy | ✅ ¹¹ | ⚠️ ¹² | ⚠️ | ⚠️ |
| **Next memory contention** | ✅ | ⚠️ | ❔ | ❌ ¹³ |
| Z80 conformance suite (zexall/zexdoc) | ✅ | ❔ | ❔ | ✅ |
| Verified against FPGA reference | ✅ ¹⁴ | ❔ | ❔ | ✅ ¹⁵ |

¹⁰ ZEsarUX documents "almost all" Z80N opcodes.
¹¹ zx_go targets cycle accuracy with contention that scales by turbo multiplier;
the boot path is byte-identical to the FPGA reference at 3.5 MHz. Exhaustive
per-software timing validation is ongoing.
¹² CSpect is explicitly described (by the community and author) as functionally
accurate but **not** cycle-exact.
¹³ MAME's `tbblue` driver has memory contention as an explicit TODO (its
*classic* Spectrum drivers do model contention). This is the clearest single
accuracy gap of MAME's Next driver.
¹⁴ zx_go's boot path was verified divergence-by-divergence against the FPGA
core's behaviour; a GHDL gate-level testbench is deferred research.
¹⁵ MAME's `tbblue` is a near-line-by-line translation of the official FPGA VHDL,
which is why the community treats it as the accuracy reference.

---

## 6. Boot, media & file formats

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| Cold-boots NextZXOS to interactive shell | ✅ | ✅ | ✅ | ✅ ¹⁶ |
| SD card / FAT image | ✅ ¹⁷ | ✅ | ✅ | ✅ |
| Next system ROMs | Downloaded ¹⁸ | User-supplied | Bundled | On SD image |
| Snapshots (.sna / .z80 / .szx) | ✅ | ✅ | ✅ | ❌ ¹⁹ |
| `.nex` (Next snapshot) | ✅ (load) | ✅ | ✅ | ❌ |
| Tape (TAP / TZX) | ✅ | ⚠️ ²⁰ | ✅ | ⚠️ ²¹ |
| Disk (DSK / +3) | ✅ | ❔ | ⚠️ | ✅ ²¹ |
| TR-DOS / Beta disk (.TRD) | ✅ | ❌ | ✅ | ❌ |
| RZX recordings | ✅ ²² | ❔ | ⚠️ (playback only) | ❌ |

¹⁶ MAME boots NextZXOS to navigable menus; full Browser use is highly likely but
was not photographically confirmed in this survey.
¹⁷ zx_go can mount a host directory (building a FAT image on the fly) *or* a
pre-built `.img`/`.mmc`.
¹⁸ The licensed NextZXOS ROMs (Amstrad/Sky copyright) are downloaded on demand
from the official distribution rather than bundled.
¹⁹ MAME's Next machine removes the snapshot device; it has no `.nex` loader.
²⁰ CSpect loads tape mainly via the emulated SD/NextZXOS path; TZX is not natively
decoded.
²¹ On the *Next* machine MAME's tape/floppy devices are vestigial (the real Next
has no tape/floppy port); MAME's *classic* +3 driver has a full FDC.
²² zx_go supports RZX playback **and** recording, including competition-mode DSA
signing/verification.

---

## 7. Peripherals

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| DivMMC / esxDOS | ✅ | ✅ | ✅ | ✅ |
| Multiface (1 / 128 / 3) | ✅ | ❔ | ✅ | ✅ |
| Kempston joystick + mouse | ✅ | ✅ | ✅ | ✅ |
| RTC (DS1307) | ✅ ²³ | ✅ | ✅ | ✅ |
| Next DMA (zxnDMA) | ✅ ²⁴ | ✅ | ⚠️ | ✅ |
| UART / Wi-Fi (ESP) | ⚠️ ²⁵ | ⚠️ | ⚠️ | ⚠️ ²⁶ |
| DISCiPLE / +D | ✅ | ❌ | ✅ | ❌ |
| Interface 1 + Microdrive | ✅ | ❌ | ✅ | ⚠️ |
| ZX Printer | ✅ | ❌ | ✅ | ⚠️ |

²³ zx_go persists battery-backed RTC NVRAM to disk.
²⁴ zx_go implements the zxnDMA's memory↔memory and memory↔IO transfers
(sprite/Layer 2/DAC port endpoints), per-byte prescaler + cycle-length timing
(continuous-mode duration charged to the CPU clock; burst-mode + prescaler
transfers interleaved with the CPU so DMA-streamed audio is paced across the CPU
timeline), Continue / auto-restart, and read-mask register read-back.
²⁵ zx_go provides a UART/AT-command stub; real Wi-Fi networking is out of scope.
²⁶ None of these emulators run a real ESP8266 Wi-Fi firmware stack; they model the
UART and optionally bridge to host serial / a real device.

---

## 8. Audio

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| Beeper | ✅ | ✅ | ✅ | ✅ |
| AY-3-8912 | ✅ | ✅ | ✅ | ✅ |
| Turbosound (multi-AY) | ✅ | ✅ | ✅ | ✅ |
| Next DAC channels | ✅ | ✅ | ✅ | ✅ |
| Covox / SpecDrum | ✅ ⁸ | ✅ | ✅ | ✅ |
| Measured AY volume curve | ✅ | ❔ | ❔ | ✅ |

⁸ Classic-Spectrum SpecDrum (port $DF) and Covox (port $FB), opt-in from the
Peripherals menu. Event-timed: each DAC write is recorded with its T-state
offset and reconstructed per audio-sample (like the beeper), so PCM playback is
sample-accurate rather than a per-frame snapshot.

---

## 9. Debugging & developer tooling

| Criterion | zx_go | CSpect | ZEsarUX | MAME |
|---|:---:|:---:|:---:|:---:|
| Built-in interactive debugger | ✅ ²⁷ | ✅ | ✅ | ✅ |
| Breakpoints (conditional) | ✅ | ⚠️ ²⁸ | ✅ | ✅ |
| Bank-aware breakpoints | ✅ | ✅ | ✅ | ⚠️ |
| Memory / register / port watchpoints | ✅ | ⚠️ | ✅ | ✅ |
| Reverse / time-travel debugging | ⚠️ ²⁹ | ⚠️ (lite) | ✅ | ⚠️ (rewind) |
| Disassembler (Z80N-aware) | ✅ | ✅ | ✅ | ✅ |
| Remote debug protocol | ⚠️ ³⁰ | ✅ (DZRP) | ✅ (ZRCP) | ⚠️ ³¹ |
| DeZog / VS Code integration | ❌ | ✅ | ✅ | ⚠️ |
| Plugin / scripting API | ⚠️ ³² | ✅ (C#) | ⚠️ | ✅ (Lua) |
| Headless / automation mode | ✅ | ⚠️ | ✅ | ✅ |

²⁷ zx_go has two surfaces — a live GUI debugger and a scriptable telnet server —
over one shared backend (breakpoints, watchpoints, time-travel, M1 trace ring).
²⁸ In CSpect, conditional breakpoints are typically evaluated by DeZog rather than
natively.
²⁹ zx_go offers a snapshot ring with rewind (not continuous reverse execution).
ZEsarUX's "time machine" is the most complete reverse-debugging implementation
here.
³⁰ zx_go exposes a custom line-oriented telnet protocol; it is not DeZog/DZRP/
ZRCP-compatible.
³¹ MAME's GDB stub is i386-only and cannot debug the Z80, so it is not usable for
Next debugging; MAME instead offers a strong native debugger plus Lua.
³² zx_go has no plugin API, but offers extensive headless trace/automation flags.

---

## 10. Maturity & positioning

| | zx_go | CSpect | ZEsarUX | MAME |
|---|---|---|---|---|
| First public release | 2024 | 2017 | 2013 | (Next driver 2024) |
| Community role | Reference-faithful newcomer | De-facto **dev** emulator | Broadest features + richest debugger | Accuracy / **preservation** reference |
| Primary strength | Faithful OS boot, open Go codebase, dual debugger, RZX competition signing | Speed, integrated debugger, plugin SDK, DeZog, team-insider provenance | Machine breadth, reverse-debugging, accessibility, open source | FPGA-derived fidelity, mature tooling |
| Main caveats | Young; narrow machine set; game-library breadth unproven | Closed-source; not cycle-exact; no sprite collision | Some Next accuracy gaps | No Next contention; no `.nex`/snapshots/RZX; GDB stub can't debug Z80 |

---

## Honest summary

**Where zx_go stands out today:** it is a clean, fully open-source (MIT) Go
implementation that cold-boots NextZXOS faithfully, models Next memory contention
and sprite collision (areas where some rivals have gaps), ships both a GUI and a
scriptable telnet debugger over a shared backend, and uniquely supports RZX
recording with competition-mode signing among this group.

**Where the others lead:** **CSpect** is the emulator most Next developers
actually use day-to-day, with a mature plugin ecosystem and first-class DeZog
support. **ZEsarUX** has the broadest machine coverage, the most complete
reverse-debugging ("time machine"), notable accessibility features, and years of
refinement. **MAME** is the FPGA-faithful preservation reference with deep,
well-documented tooling.

If you are writing Next software today, you will likely still reach for CSpect or
ZEsarUX (and verify on real hardware). zx_go's aim is to be a *correct, readable,
open* reference implementation — and on the specific axes of faithful OS boot,
contention modelling, and an open codebase, it already compares well.

*Spotted an error or an out-of-date row? Please open an issue or PR.*
