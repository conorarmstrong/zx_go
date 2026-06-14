# zx_go Debuggers

zx_go ships two complementary debuggers:

1. **Visual debugger** — a Fyne window inside the running GUI, with live-updating panels for registers, flags, disassembly, hex memory, memory paging, and (on Spectrum Next) NextReg state. Best for interactive exploration.
2. **Telnet (ZRCP-style) debugger** — a line-oriented TCP server you connect to with `nc` / `telnet` / a script. Best for scripted reproductions, headless CI runs, and anything you want to drive from a shell pipeline.

Both share the same CPU underneath, so anything you observe in one is observable in the other. You can run both at the same time.

> The emulator also ships extensive headless diagnostics (provenance tracer, trace-DB, crash detector, time-travel, …); run `zx_go --help` for the flags.

---

## Contents

- [Visual debugger](#visual-debugger)
  - [Opening it](#opening-it)
  - [Layout](#layout)
  - [Toolbar](#toolbar)
  - [Registers panel](#registers-panel)
  - [Flags panel](#flags-panel)
  - [Disassembly panel](#disassembly-panel)
  - [Memory hex dump panel](#memory-hex-dump-panel)
  - [Memory-paging diagram](#memory-paging-diagram)
  - [Tools tabs](#tools-tabs)
  - [Edit Regs / Write Mem dialogs](#edit-regs--write-mem-dialogs)
  - [Breakpoint workflow](#breakpoint-workflow)
- [Telnet debugger](#telnet-debugger)
  - [Starting the listener](#starting-the-listener)
  - [Connecting](#connecting)
  - [Protocol shape](#protocol-shape)
  - [Address syntax](#address-syntax)
  - [Command reference](#command-reference)
  - [Implicit pause behaviour](#implicit-pause-behaviour)
  - [Bank-aware breakpoints](#bank-aware-breakpoints)
  - [Symbol-map annotation](#symbol-map-annotation)
  - [Worked examples](#worked-examples)
- [Memory watchpoints in headless mode](#memory-watchpoints-in-headless-mode)
- [Common workflows](#common-workflows)

---

## Visual debugger

A live, interactive debugger that runs inside the GUI process. It refreshes at ~20 Hz so registers and the disassembly cursor animate as the emulator runs.

### Opening it

`Emulator → Debugger` from the menu bar. The window opens alongside the main emulator window and survives model switches.

### Layout

Four main panels arranged in a 2×2 grid, plus a top toolbar and (on Spectrum Next) a right-hand sidebar.

```
┌──────────────────────────────────────────────────────────┐
│  Toolbar  [Pause] [Step] [Step Frame] [Run] [Go to PC]  │
│           [Edit Regs...] [Write Mem...] [Clear BPs]     │
├──────────────────────┬───────────────────────────────────┤
│                      │                                   │
│   REGISTERS panel    │     DISASSEMBLY panel             │
│   (PC, AF/BC/DE/HL,  │     (Z80 disassembly from PC,     │
│    alt set, I/R, SP, │      click line = breakpoint)     │
│    flags, etc.)      │                                   │
│                      │                                   │
├──────────────────────┼───────────────────────────────────┤
│                      │                                   │
│   MEMORY hex dump    │     PAGE MAP diagram              │
│   ($0000-$FFFF, hex  │     +                             │
│    + ASCII, address  │     TOOLS TABS (Next State /      │
│    entry at top)     │     Bank / Backtrace / History /  │
│                      │     NextReg / Breakpoints /       │
│                      │     Palette / Sprites / Layer 2 / │
│                      │     Tilemap)                      │
└──────────────────────┴───────────────────────────────────┘
```

Splits between panels are draggable. The graphical Palette / Sprites / Layer 2 / Tilemap tabs are only meaningful on Spectrum Next.

### Toolbar

Buttons across the top of the window:

| Button | Function |
| --- | --- |
| **Pause** | Stop the CPU. Status indicator turns amber. |
| **Step** | Execute exactly one Z80 instruction, then re-pause. The disassembly cursor and registers update. Skips the interrupt-acceptance check at the frame boundary. |
| **Step Frame** | Execute one full emulator frame (69 888 T-states at the 48K reference rate; more on faster Next CPU speeds). Useful for "advance to the next interrupt". |
| **Run** | Resume execution. Status indicator turns green. If you're sitting on a breakpoint, single-steps over it first so the BP doesn't immediately re-fire. |
| **Go to PC** | Scroll the disassembly to the current PC. Doesn't pause / change state. |
| **Edit Regs…** | Open the register-edit dialog. |
| **Write Mem…** | Open the memory-write dialog. |
| **Clear BPs** | Remove every breakpoint. |

A "running / paused" status indicator sits next to the buttons.

### Registers panel

Live readout of every Z80 register the CPU tracks:

- **PC** — highlighted in yellow. Annotated with the symbol-map name when a symbol is loaded (e.g. `PC=$00EF (POST_RESET)`).
- **AF / BC / DE / HL** — main register pairs, shown as 16-bit and as paired 8-bit (`A=$3E F=$50` etc.)
- **AF' / BC' / DE' / HL'** — alternate set (the `EXX` / `EX AF,AF'` shadow).
- **IX / IY** — index registers.
- **SP** — stack pointer.
- **I / R** — interrupt vector base + memory-refresh register.
- **Interrupt state** — IFF1, IFF2, IM (interrupt mode).
- **Halted** — true if the CPU is in HALT.
- **Stack preview** — 4 16-bit words from SP upward.

All values are hex unless explicitly labelled. The panel refreshes at 20 Hz so values animate while the emulator runs.

### Flags panel

Sits in the top-right of the registers area. Shows the eight Z80 flag bits as individual indicators:

| Flag | Meaning |
| --- | --- |
| S | Sign |
| Z | Zero |
| H | Half-carry |
| P/V | Parity / overflow |
| N | Subtract |
| C | Carry |

Each flag is shown green when set, dim when clear. Bits 3 and 5 (the "undocumented" Y/X flags) are not shown but are still tracked by the CPU.

### Disassembly panel

Scrollable Z80 disassembly starting at the current PC. Every prefix group is decoded:

- Base opcodes
- `CB` (bit / rotate / shift)
- `ED` (extended Z80 instructions including Z80N `NEXTREG` opcodes)
- `DD` (IX-based)
- `FD` (IY-based)
- `DDCB` / `FDCB` (indexed bit ops)

Annotations:

- A yellow `>` marks the current instruction.
- A red `*` marks any active breakpoint at that PC.
- Symbols from a loaded `--symbol-map` file appear next to addresses (`CALL $00EF (POST_RESET)`).

**Click any disassembly line to toggle a breakpoint at that PC.** The line goes red. Click again to clear.

The disassembly window also re-anchors when you click "Go to PC" — you can scroll freely without losing the cursor.

### Memory hex dump panel

Full $0000-$FFFF address space, formatted in 16-byte rows with hex columns + ASCII. Live-updates as the CPU writes memory.

A control row at the top:

- **Addr:** — type an address to jump to. Accepts `$XXXX`, `0xXXXX`, decimal, or octal — the base selector dropdown picks how the field is interpreted.

For Spectrum Next, the dump shows the *effective* byte at each CPU address — i.e. what an `LD A,(HL)` would actually read, taking the active MMU slot, ROM bank, and divMMC overlay into account.

### Memory-paging diagram

A graphical view of which physical memory page (ROM bank, RAM bank, divMMC ROM/RAM bank) is mapped at each 16-KB (classic) or 8-KB (Next) slot of the CPU's address space.

- **Classic models (48K, 128K, +2, +2A, +3):** four 16-KB slots, labelled `0x0000-0x3FFF`, `0x4000-0x7FFF`, `0x8000-0xBFFF`, `0xC000-0xFFFF`.
- **Spectrum Next:** eight 8-KB slots, labelled `0x0000-0x1FFF` … `0xE000-0xFFFF`, plus the divMMC overlay's paged-in state shown as an overlay band.

Slots that are write-protected (ROM) are shown in a different colour from RAM slots. The currently-active ROM bank for the Z80's view is labelled at the top.

### Tools tabs

The bottom-right area is an icon-led tab strip (Next State, Bank Inspect, Backtrace, History, NextReg, Breakpoints, Watchpoints, Heatmap, Time Travel, Palette, Sprites, Layer 2, Tilemap). The text panels mirror a feature of the telnet debugger so the two surfaces stay feature-equivalent; the last four are **graphical** Next inspectors (live-rendered each tick) that match the visual viewers in GUI emulators like jnext:

- **🎨 Palette** — the active 256-entry palette as a 16×16 colour-swatch grid (each cell the real expanded RGB of that entry).
- **▦ Sprites** — every *visible* sprite's 16×16 pattern drawn as actual pixels through the sprite palette, tiled into a sheet, with a live count.
- **🖼 Layer 2** — the live Layer-2 framebuffer (256×192) rendered through the Layer-2 palette, with a resolution/enable status line.
- **⛶ Tilemap** — the tilemap layer (tiles + attributes) rendered through the tilemap palette.

These read live emulator state and repaint on every pause/step, so you can *see* what the guest has drawn — even mid-boot. They're the visual counterparts to the telnet `palette-dump` / `sprite-list` / `layer-state` commands.

#### Shared backend (telnet ↔ GUI)

The visual and telnet debuggers are two surfaces over **one shared
backend**, so state set on either appears (and acts) on the other:

- **Breakpoints** — one shared store. A `set-breakpoint` over telnet
  shows up in the GUI's Breakpoints tab and on the disassembly gutter,
  and a click-to-toggle in the GUI is listed by `list-breakpoints`.
- **Watchpoints** (register watches) — one shared set. `watch-reg`
  and the **Watchpoints** tab edit the same list; a watch fires the
  CPU pause regardless of which surface added it.
- **Time Travel** — one emulator-owned snapshot ring. `tt-on` /
  `tt-snap` / `tt-rewind` and the **Time Travel** tab (enable,
  snapshot-now, tap-a-row-to-rewind, clear) drive the same buffer.
- **Heatmap** tab — hot-PC / call / ret / rst views over the same M1
  history ring as `hot` / `callgraph` / `retgraph` / `rstgraph`
  (recomputed on demand via its Refresh button).
- The toolbar also has **Step Over** (run past a CALL/RST to its
  return) and the register panel shows **IRQ taken / rejected** counts
  (the GUI equivalent of `step-over` and `irq-stats`).

Watch types other than register watches (`watch-mem` / `watch-read` /
`watch-port` / `watch-zero`) and the streaming trace commands
(`trace-*`, `nr-trace`, tracepoints) remain telnet-primary — they
install hooks that stream to the log rather than mapping to a panel.

#### Next State

Only meaningful when the emulator is in `ModelNext`. Lists:

- **MMU shadow** — slots 0..7 with their current 8-KB page indices.
- **divMMC state** — `paged_in` / `mapram` / `automap`.
- **Selected NextRegs** — a fixed set of the most useful registers, listed below.

The fixed NextReg view (chosen for boot-debugging value):

| Reg | Name |
| --- | --- |
| $00 | Machine ID |
| $01 | Core version |
| $02 | Reset reason / state |
| $07 | Turbo control |
| $08 | Peripheral 1 |
| $09 | Peripheral 4 |
| $0A | Peripheral 5 (divMMC automap on bit 4) |
| $14 | Transparency colour |
| $15 | Sprite & layers system |
| $18 | Layer 2 control |
| $19 | Sprite control |
| $43 | Palette control |
| $50–$57 | MMU slots 0–7 |
| $69 | Layer 2 + ULA enables |
| $6B | Tilemap control |
| $80–$87 | Internal port-decode flags |
| $B8 | divMMC enable / automap mask |

#### Bank Inspect

A direct front-end for the `bank-peek` / `bank-poke` telnet commands. Pick a **Kind** (`ram` / `rom` / `altrom` / `divmmc-ram`), set **Bank** + **Off** + **Len**, hit **Dump** for an xxd-style hexdump of physical bytes — regardless of what the CPU has paged in. Write the same way: type bytes into the **Bytes** field (space- or comma-separated, hex) and hit **Poke**.

`pool-scan HEXBYTES [KIND]` — search every physical bank of KIND (default `ram`) for a byte pattern, reporting `kind:bank:$offset` per hit (max 32). Finds where a DOS load or DMA landed regardless of CPU mapping.

This is the surface to use when you want to confirm "what does bank 5 actually contain right now" while bank 0 is mapped, or to pre-stage bytes into alt-ROM buffers before flipping the redirect.

#### Backtrace

Renders `debugger.Backtrace()`'s output. Set **Depth** and hit **Refresh** (the CPU should be paused). Each line shows the stack address, the 16-bit word, a heuristic classification (`from CALL nn` / `from CALL cc` / `from RST` / `speculative`), and — for non-speculative frames — three instructions of disassembly at the return target.

#### History

Front-end for the M1-fetch ring buffer. **Prev N** shows the last N entries; **Full** dumps the whole ring. Each row has the global instruction counter, PC, SP, AF, IFF1, IM, Halted, and the active ROM bank.

The ring is shared with the telnet `history` / `prev` commands. If you launched with `--debugger-history N`, both surfaces read from the same buffer; otherwise the visual debugger creates a default 4096-entry ring on first open.

#### NextReg

Equivalent to the telnet `nextreg-read` / `nextreg-write` commands. Type a **Reg** index, hit **Read** to dump the value (also shown in binary), or type a **Val** and hit **Write** to commit it (firing any OnWrite hooks just like a real `NEXTREG $XX,$YY`).

#### Breakpoints

The conditional / bank-filtered breakpoint surface. The form mirrors the telnet `set-breakpoint ADDR [bank=N] [if EXPR]` syntax:

- **PC** — target address (hex)
- **Bank** — ROM bank filter, or `-1` for "any bank"
- **If** — guard expression in the same grammar the telnet debugger accepts (see [Conditional breakpoints](#conditional-breakpoints))

**Add** commits the entry; tap a row in the list to remove it; **Clear all** wipes the set. Plain-tap breakpoints (set by clicking a line in the disassembly) appear here too, alongside richer entries.

### Edit Regs / Write Mem dialogs

**Edit Regs…** opens a dialog with one entry per 16-bit register pair. Type a new value (hex by default), Apply. The CPU's register file is updated immediately.

**Write Mem…** opens a small dialog with two fields: address and byte value. Apply writes a single byte through the normal memory bus — i.e. it goes through MMU translation, ROM-write filtering, divMMC overlay routing, etc.

### Breakpoint workflow

1. **Pause** the CPU.
2. **Click the line in the disassembly** where you want to break. The line turns red with `*`.
3. **Run**. The emulator executes until PC hits that line, then auto-pauses.
4. To clear a single breakpoint, click the same line again.
5. To clear them all, **Clear BPs**.

Breakpoints set via the visual debugger fire alongside those set via the telnet debugger — there's one shared breakpoint set on the CPU.

Breakpoints are kept in memory only; they are not persisted across `bin/zx_go` restarts.

---

## Telnet debugger

A line-oriented ZRCP-style remote-debug protocol over TCP. One command per line; one response per line; trivially scriptable.

### Starting the listener

Pass `--debugger-port=N` to enable. The listener opens on `localhost:N`.

```bash
./bin/zx_go --next --headless --debugger-port=10000
```

Optional:

| Flag | Effect |
| --- | --- |
| `--debugger-pause-at-start` | Halt the CPU at `PC=$0000` before the first instruction fetches. Useful if you want to set breakpoints before any code has executed. |
| `--debugger-history=N` | Record the last N M1 fetches in a ring buffer accessible via the `history` / `prev` commands. Sensible range 1024..65536. 0 disables (and the pre-fetch hook isn't installed at all, so there's no runtime cost). |
| `--debugger-history-wide` | Make every history entry carry `BC / DE / HL / IX / IY` as well as the base set. No-op unless `--debugger-history>0`. Cost: +10 bytes per entry. Needed for "what was IX at insn N" diagnostics where the answer doesn't live in the disassembly. |
| `--symbol-map=PATH` | Load a `$XXXX NAME` symbol file; PC / call targets / stack words are annotated with the symbol when present. |

You can combine `--debugger-port` with `--headless` (CI / scripted) or with the GUI (run the listener in parallel with the visual debugger). Both work.

### Connecting

Any TCP client works:

```bash
nc localhost 10000
```

or `telnet localhost 10000`. On connect you'll see:

```
OK welcome to zx_go remote debugger
```

Type `help` for the command list.

Multiple connections are allowed; they share the CPU. Each command runs to completion atomically — there's no command interleaving.

### Protocol shape

- **Plain text.** One command per line, LF or CRLF terminated.
- **Case-insensitive** command words.
- **Hex arguments** accept `$XXXX`, `0xXXXX`, or bare hex (e.g. `5C3A`).
- **Decimal is not accepted** in addresses or values — Spectrum addresses are universally written in hex.
- **Every response begins with `OK ` on success or `ERR ` on failure** so scripts can parse by prefix.
- **One line of output per command**, except `disassemble` which spans multiple lines (one per instruction).

### Address syntax

All of these are equivalent:

```
$5C3A
0x5C3A
5C3A
0X5C3A
```

Trailing whitespace is trimmed. Numbers larger than 16-bit error out.

### Command reference

#### CPU control

| Command | Aliases | Purpose |
| --- | --- | --- |
| `pause` | — | Stop the CPU. (Most state-reading commands also implicitly pause — see [Implicit pause behaviour](#implicit-pause-behaviour).) |
| `set-pause-timeout [SECONDS]` | — | Bound how long a state-touching command waits for the headless loop to acknowledge a pause (default 2s). No arg reports the current value. Raise it when awaiting a `continue` to a deep breakpoint in a long boot, where reaching the BP can take several seconds of wall-clock and the 2s default would give up first. |
| `break-on-sd CMDn\|ACMDn [arg] \| off` | `bsd` | One-shot breakpoint that fires when the SD card receives a matching command, pausing the CPU at the live PC of the issuing instruction. `arg` (hex `$..`/`0x..` or decimal) restricts the match to a specific 32-bit argument (e.g. an LBA). No arg shows the armed spec; `off` clears it. Catches code in paged regions where static-address breakpoints don't fire (e.g. the NextZXOS DOS SD driver): `break-on-sd CMD12` lands at the DOS mount entry (the bootrom never issues CMD12); `break-on-sd CMD17 $3F` at a boot-sector read. |
| `continue` | `cont`, `c`, `run`, `r` | Resume execution. If sitting on a breakpoint, single-steps over it first so it doesn't immediately re-fire. |
| `step [N]` | `s` | Execute N Z80 instructions in one ZRCP roundtrip and pause again. N defaults to 1; max 65535 (chain calls for larger ranges). The response is sent only AFTER the step(s) have run — the next command sees post-step state. Used by bulk-advance tools (lockstep diff, divide-and-conquer divergence finders) where one-roundtrip-per-step would dominate runtime. Each instruction is processed with full IRQ-acceptance semantics (`StepInstructionWithIRQ` — frame-boundary INT, M1 sample point, HALT exit on /INT), so single-stepping produces the same per-instruction state evolution as bulk-frame execution. |
| `step-over` | `n`, `next` | If the next instruction pushes a return address (CALL / CALL cc / RST / Z80N PUSH NN), plant a one-shot tentative BP at PC+instruction-length and continue until it fires. Otherwise degenerates to a plain `step`. The one-shot is invisible to `list-breakpoints` and auto-clears on hit. |
| `cold-reset` | — | Power-cycle the machine: `cpu.Reset` + `mem.Reset` + `nextRegs.Reset` + peripheral reset via the same code path as the GUI `Reboot` menu item. Instruction counter returns to zero. Lets bisect-style tools restart from cold without killing the process. CAUTION: on ModelNext with a warm-boot snapshot installed, the snapshot will reapply (matching the GUI behaviour). |
| `help` | `?` | Print the command list. |
| `quit` | `exit` | Close this TCP connection. Other connections and the emulator stay running. |

#### Inspection

| Command | Aliases | Purpose | Response shape |
| --- | --- | --- | --- |
| `get-registers` | `regs` | Full Z80 register dump. PC is annotated with the symbol map if loaded. | `OK PC=$XXXX (NAME) SP=$XXXX AF=$XXXX BC=$XXXX … insns=N` |
| `get-stack` | `stack` | 16 16-bit words at SP, each annotated. | `OK sp=$XXXX words=$XXXX $XXXX …` |
| `backtrace [N]` | `bt` | Walk the stack downward up to N words (default 8, max 64). Each word is classified as `from CALL nn`, `from CALL cc`, `from RST`, or `speculative` based on the bytes preceding the address it points to. Real return addresses also get a 3-instruction disasm at their target. Best command to run at a breakpoint to understand the call-graph ancestry. | `OK\r\n  SP = $XXXX  IFF1=BOOL  IM=N  Halted=BOOL\r\n  $XXXX: $XXXX   CLASS   HINT\r\n          XXXX  bytes  MNEMONIC\r\n  …` |
| `history` | `hist` | Show the M1-fetch ring buffer's fill / capacity. Enabled by `--debugger-history=N` at launch. | `OK entries=N capacity=N` |
| `prev [N]` | `p` | Dump the last N M1-fetch ring entries in chronological order. Each line: instruction count, PC (symbol-annotated), SP, A/F, IFF1, IM, Halted, ROM bank — plus `via=KIND@$XXXX` for each non-sequential transition (`JP / JR / CALL / RET / RST / INT / NMI / RESET`). Default N=20. With `--debugger-history-wide` the line also carries BC / DE / HL / IX / IY. | `OK\r\n  insn=N PC=$XXXX  SP=$XXXX AF=$XXXX  …  via=JR@$1F0E\r\n  …` |
| `get-memory ADDR LEN` | `mem` | LEN bytes of memory starting at ADDR as a flat hex list (scriptable). LEN capped at 256. | `OK XX XX XX …` |
| `hexdump ADDR LEN` | `hd` | Classic hexdump of LEN bytes (cap 1024): one row per 16 bytes with an address column and a printable-ASCII gutter (non-printables as `.`). The human-readable counterpart to `get-memory`. | `OK\r\n  $XXXX  hh hh … hh  ascii\r\n  …` |
| `read-memory ADDR` | `peek` | Single byte. | `OK $XX` |
| `disassemble [ADDR [N]]` | `disasm`, `d` | N-instruction window starting at ADDR. ADDR defaults to current PC. N defaults to 6, capped at 64. | `OK\r\n  XXXX  bb …  MNEMONIC\r\n  …` |
| `get-mmu` | `mmu` | Current ROM bank index plus all 8 8-K MMU slot values. | `OK rom_bank=N slots=XX XX XX XX XX XX XX XX` |
| `get-divmmc` | `divmmc` | divMMC overlay state. | `OK paged_in=BOOL mapram=BOOL automap=BOOL` |
| `nextreg-read REG` | `nr-r` | Read a Spectrum Next NextReg by index. | `OK $XX` (or `ERR no nextregs`) |
| `nr-panel` | `nrp` | Decoded NextReg dashboard — machine/timing ($03), CPU speed ($07), peripherals ($05/$06 incl. divMMC-enable), altrom ($8C), paging ($8E), automap ($0A), the MMU8 slot table ($50–$57), Layer 2 ($12/$13/$70), sprite-select ($34) and palette-control ($43), all human-readable. | `OK NextReg panel\r\n  $07 CPU speed = $03 (28MHz)\r\n  …` |
| `copper-disasm` | `copper` | Disassemble the live Copper program: header (write cursor + decoded start-mode) then one line per instruction (`MOVE NR$rr,$vv` / `WAIT line=Y hpos=X` / `HALT`), stopping at the first HALT so trailing NOOPs aren't dumped. | `OK Copper  cursor=$XXX  mode=OnVBL\r\n  000: MOVE  NR$40,$07\r\n  …` |
| `layer-state` | `layers` | Decoded video-layer panel — SLU layer-priority order (NR$15, incl. blend modes), sprite enable / over-border / priority, ULA enable (NR$68), Layer 2 resolution (NR$70) and tilemap enable (NR$6B). | `OK Layers\r\n  $15 layer priority = S>L>U\r\n  …` |
| `sprite-list` | `sprites` | List the visible sprites in the live bank: header (enable + selected slot) then one line per VISIBLE sprite (X/Y/pattern/palette). Invisible slots are skipped; reports `(no visible sprites)` when the bank is empty. | `OK Sprites: enabled  sel=N\r\n  003: X=100 Y=50 pat=7 pal=2\r\n  …` |
| `palette-dump` | `palette` | Dump the active 256-entry palette as a 9-bit-hex grid (16 per row, each row labelled with its start index) plus the decoded 8-bit RGB of entry 0. | `OK Palette (9-bit RRRGGGBBb)\r\n  $00: 000 1FF …\r\n  entry 0 RGB = #000000\r\n` |
| `bank-peek KIND BANK OFF [LEN]` | `bp-peek` | Hex-dump LEN bytes (default 16, cap 256) from physical bank BANK in KIND, ignoring whatever the CPU has mapped. Useful for "what's actually in bank 5 right now" while bank 0 is paged in. KIND is one of `ram` / `rom` / `altrom` / `divmmc-ram`. BANK / OFF / LEN are hex (use `$` or `0x` prefix; bare digits are still hex). | `OK XX XX XX …` |
| `list-banks` | — | Describe the bank pools available on this build. | `OK ram=128*16K rom=4*16K altrom=2*16K [divmmc-ram=8*8K]` |
| `disasm-bank KIND BANK OFF [N]` | — | Disassemble N instructions (default 8, cap 64) starting at OFF inside the underlying buffer of KIND BANK, ignoring whatever the CPU has mapped. KIND values as for `bank-peek`. Output lines are prefixed `KIND:BANK:OFFSET` so disasms from different banks stay unambiguous. | `OK\r\n  rom:0:00EF  ED 91 07 03  NEXTREG $07,$03\r\n  …` |
| `provenance on\|off\|$ADDR [word]` | `prov` | Backward provenance tracer (Tool #1) — the *data-lineage* axis the time-travel ring can't give. `on` arms a last-writer index (records the instruction PC + counter that last wrote each logical byte); `off` disarms; `$ADDR` reports who last wrote that byte (`word` also reports `$ADDR+1`). Arm it *before* the writes you care about (or use the `--provenance` flag to arm from boot). Designed to crack bad-jump / corrupt-stack traps: walk a fatal value back to whoever wrote it. **Caveat:** keyed by logical address, so a write recorded under a logical address later re-paged to a different physical bank can read stale — cross-check `rom_bank` in the result. | `OK provenance $C000: val=$AB written by insn N at PC=$1234 (rom_bank=0)` |
| `why-pc` | — | Explain how the CPU reached the current PC by reading the return word just consumed off the stack (`[SP-2,SP-1]`) and pairing it with its last-writer provenance. If that word equals PC, the jump arrived via `RET` and the records name the pushing instruction; otherwise it reports the word was not the source (likely `JP`/`JR`/interrupt) so you disassemble the preceding code. Requires `provenance on` first. | `OK why-pc: PC=$XXXX SP=$XXXX\r\n   popped return word $XXXX …\r\n      lo $XXXX: …` |
| `provenance-phys BANK OFF` | `provp` | Physical-pool variant of `provenance` (Tool #1 Phase 2): who last wrote RAM 16K-bank BANK at intra-bank offset OFF, keyed by physical location so it survives re-paging (the logical `provenance` goes stale when a slot is remapped). BANK / OFF are hex. Note: only standard RAM writes are captured — config-mode / DMA / cold-fill bytes are not (Phase 2b). | `OK provenance-phys bank=111 off=$002D: val=$C7 written by insn N at PC=$XXXX` |

#### Mutation

| Command | Aliases | Purpose | Response shape |
| --- | --- | --- | --- |
| `write-memory ADDR VAL` | `poke` | Single-byte write. Goes through MMU / ROM-write-filter / divMMC overlay routing exactly like a Z80 `LD (nn),A`. | `OK` |
| `set-reg NAME VAL` | — | Write a CPU register. NAME is case-insensitive — 8-bit (A/F/B/C/D/E/H/L/I/R) or 16-bit (AF/BC/DE/HL/IX/IY/SP/PC/IM) accepted. Used by lockstep / bisect tools that need to align initial register state across two emulators (the Z80 reset spec only fixes PC/I/R/IFF/IM; everything else is implementation-defined). | `OK` (or `ERR unknown register …`) |
| `nextreg-write REG VAL` | `nr-w` | NextReg write. Fires any OnWrite hooks (so writing $0A bit 4 actually toggles divMMC automap, $8E swaps ROM banks, etc.). | `OK` |
| `bank-poke KIND BANK OFF BYTE [BYTE…]` | `bp-poke` | Write a list of bytes into physical bank BANK at OFF in KIND. Bypasses the CPU's mapping — the bytes land directly in the underlying pool, visible on the next CPU read. KIND values as for `bank-peek`. Each BYTE is hex. | `OK wrote N bytes` |
| `load-bin KIND BANK OFF FILE [LEN]` | — | Read raw bytes from the host filesystem (the FILE path) and poke them into KIND BANK at OFF. Same dispatch as `bank-poke`, so MAPRAM / stub-protected windows still drop bytes. Typical use: capture a runtime bank dump from another emulator and overlay it as a starting state (see the `compare-foreign` workflow below). Limit to LEN bytes if supplied. | `OK loaded N bytes from FILE into KIND bank=N off=$XXXX` |
| `sym $ADDR NAME` / `sym clear $ADDR` / `sym` | — | Add, remove, or list symbol-map entries at runtime. Naming an address mid-session makes it appear in subsequent `disasm` / `prev` / `regs` annotations without restarting. `sym` with no args lists every loaded symbol, address-sorted. |
| `reload-syms PATH` | — | Re-read PATH as a symbol-map file, replacing the in-memory table. Use after editing the file mid-session. |

#### Breakpoints

| Command | Aliases | Purpose |
| --- | --- | --- |
| `set-breakpoint ADDR [bank=N\|any-bank] [if EXPR] [do "CMD; CMD"]` | `set-bp`, `bp` | Halt when PC equals ADDR. `bank=N` (0..3) limits firing to when ROM bank N is mapped at $0000. Defaults to the CURRENT ROM bank when no `bank=` is given — pass `any-bank` for the polymorphic match. `if EXPR` adds a runtime guard; `do "..."` runs semicolon-separated commands on hit (responses emitted via `slog.Info("bp-action")`). |
| `clear-breakpoint ADDR` | `clear-bp`, `cbp` | Remove the breakpoint at ADDR. |
| `list-breakpoints` | `list-bp`, `lbp` | Print every active breakpoint, annotated. Bank-less BPs show `[any bank!]` to flag the footgun where the same PC means different code in different banks on the Spectrum Next. |
| `cont-until EXPR` | `continue-until` | One-shot conditional continue. Resumes the CPU and halts on the first M1 where EXPR is true; auto-clears on fire. Same expression grammar as `bp ... if EXPR`. `cont-until off` clears without resuming. |
| `forward [N]` | `f` | Step N M1 fetches forward, emitting one line per step with the same columns as `prev`. Default 10, max 256. Mirror of `prev N` post-hit. |
| `xref ADDR` | — | Scan loaded ROMs (NextZXOS bank-0..3 + divMMC ROM) for any opcode statically referencing ADDR. Covers CALL/JP/cond-CALL/cond-JP/LD-imm/LD-(nn)/LD-(nn),r/ED-prefix LD/DD-FD-prefix LD/RST+DEFW. Static dump-walk — flags raw instruction bytes so false positives are recognisable. |
| `bp-first-entry $LO[-$HI] [snap=PREFIX]` | — | Arms a SINGLE-FIRE breakpoint that halts on the FIRST M1 fetch whose PC lies in the range and auto-clears. Without this, `set-breakpoint` halts every steady-state visit; this primitive caches the "first-entry" semantics for "catch the moment PC first enters NOP-slide" / "catch first IRQ" workflows. Optional `snap=PREFIX` triggers `snapshot-on-bp` style capture on fire (`bp-first-entry:PREFIX` reason tag). `bp-first-entry off` disarms; no-arg shows armed range. |

#### Watchpoints

| Command | Aliases | Purpose |
| --- | --- | --- |
| `watch-reg REG [from VAL] [to VAL]` | `wr` | Halt when REG changes. With no clauses, fires on any change. `to VAL` fires only when the new value matches; `from VAL` only when the old value matches; both, only on the exact transition. Useful for `watch-reg iff1 to 1` ("when does IRQ become enabled?") or `watch-reg ix from $FFFF` ("when does IX leave its uninitialised state?"). REG names match the conditional-bp grammar (`pc`, `sp`, `a`, `b`, …, `ix`, `iy`, `iff1`, `iff2`, `im`, `halted`, `bank`, plus the divMMC paging predicates `dmmc`/`automap`/`mapram`/`conmem`). |
| `list-watches` | — | Show every active register watch. |
| `clear-watch [REG]` | — | Remove one (by REG) or all watches. |
| `watch-mem ram BANK FROM TO` | — | Halt on any guest write into ram bank BANK at offset FROM..TO. Replaces any prior memory watch (single active spec). Doesn't disturb the env-var `ZX_GO_RAM_WRITE_TRACE` diagnostic — the watch is chained on top of any existing RAM-write hook. |
| `clear-watch-mem` | — | Clear the active memory watch. |
| `watch-read ram BANK FROM TO` | — | Mirror of `watch-mem` for **reads**: halt on any guest read from ram bank BANK at offset FROM..TO, at the reading instruction's PC. This is what catches a validation/parse routine reading a buffer byte and rejecting it — invisible to the write watch. Installed only on first use (reads are hot — every opcode fetch is a read — so the chained hook no-ops when no spec is set). Fires `snapshot-on-bp` with reason="watch-read". |
| `clear-watch-read` | — | Clear the active read watch. |
| `watch-zero on/off` | — | Halt as soon as M1 fetches from a region where the next 16 bytes ahead of PC all read as $00. Pinpoints the moment a guest JP / RET / JR lands in uninitialised RAM or an empty config-mode backing buffer. Fires `snapshot-on-bp` with reason="watch-zero". |
| `watch-port PORT [=VAL]` | — | Halt on guest port writes. Ports < $100 match the low byte of the actual port (so `watch-port $FE` catches `OUT ($FE),A` regardless of A); ports >= $100 match exactly. `=VAL` filters by byte value. `watch-port off` clears all; no-arg lists active. Z80N `NEXTREG nn,nn` opcodes bypass the port path — use `nr-trace` for NextReg visibility. |

#### Tracepoints (non-halting)

| Command | Aliases | Purpose |
| --- | --- | --- |
| `tp ADDR` | — | Arm a tracepoint at ADDR. When PC hits ADDR, the debugger emits an `slog.Info("tp-hit")` line with full register state (PC, SP, AF, BC, DE, HL, IX, IY, IFF1, insns) and **does not halt**. A hit counter accrues per-PC. |
| `list-tp` | — | Show every active tracepoint with its hit count. |
| `clear-tp [ADDR]` | — | Remove one (by ADDR) or all. |
| `nr-trace REG[,REG…]` | — | Add NextReg numbers to the runtime trace set. Each guest write to a watched NR emits an `slog.Info("nr-trace")` line with reg + value + CPU PC. `nr-trace` (no arg) lists active. `nr-trace off` clears all. Chained on top of any tracer the `ZX_GO_NEXTREG_WATCH` env var installed at startup. |
| `trace-divmmc-ram [BANK\|all\|any] [OFF\|LO-HI]` | — | Log every write that lands in divMMC RAM matching the filter. `slog.Info("divmmc-ram-trace")` per match with bank/addr/val/pc/insns. `trace-divmmc-ram` (no args) shows current filter + hit count. `trace-divmmc-ram off` disables. Replaces any prior `pager.SetWriteLogger` callback, so env-var diagnostics that share the slot are clobbered while this is armed. |
| `trace-writes $LO[-$HI] [limit=N]` | — | Log every CPU write (Memory.Write path) into the virtual address range, with the RESOLVED destination: 8K slot + MMU8 bank value + override flag + ROM bank. Distinguishes config-mode NR$04 routing from MMU8 mapping from alt-rom redirects — `bank=0` could mean three different physical destinations and the existing `watch-mem` doesn't disambiguate. `slog.Info("write-trace")` per match. `trace-writes off` disarms; no-arg shows armed range + hit count; `limit=N` caps log lines (counter still tracks hits beyond the cap). |
| `trace-nextreg-deltas REG[,REG…]\|all` | `nr-deltas` | Log only NextReg writes that ACTUALLY CHANGE the stored value — silently drops idempotent writes. NR$04 (RAMPAGE) can take tens of thousands of writes per boot via the FPGA-bootrom INIR loop; this filter cuts the log volume by >100x while still capturing every transition. Each line is one `slog.Info("nr-delta")` with reg/old/new/pc/insns. First observation per reg shows `old=init` to distinguish a fresh capture from a real value change. `trace-nextreg-deltas off` clears; no-arg shows armed set + hit count. |

#### Interrupts

| Command | Aliases | Purpose |
| --- | --- | --- |
| `irq-stats [reset]` | — | Report interrupt-taken vs interrupt-rejected counts since process boot (or since the last `irq-stats reset`). Each entry includes the PC + instruction counter of the most recent fire of that kind. Surfaces "IRQ pulse missed every frame" patterns in one line. |
| `catch irq [off]` | — | Halt the CPU at the next interrupt-taken event. Logs an `slog.Info("catch-irq hit")` line with the pre-push PC and fires `snapshot-on-bp` with reason="catch-irq". |

#### NextReg snapshots

| Command | Aliases | Purpose |
| --- | --- | --- |
| `nr-snap NAME` | — | Capture the full 256-byte NextReg state into a named slot. `nr-snap` (no arg) lists active. `nr-snap clear [NAME]` drops one or all. |
| `nr-diff` | — | Compare the last two captured snapshots (no args), or one snapshot against the live state (`nr-diff NAME`), or two named snapshots (`nr-diff A B`). Lists every NR whose value differs. |

#### Heatmaps

| Command | Aliases | Purpose |
| --- | --- | --- |
| `hot [N]` | — | Dump the top-N (default 20, cap 256) PCs in the current M1 history ring by hit count, descending; ties break by PC ascending. Identifies tight loops in one command. Needs `--debugger-history > 0`. |
| `callgraph [N]` | — | Walk the M1 history ring, keep only `CALL`-sourced transitions, and list the top-N (default 20, cap 256) `caller → callee` edges by frequency. Answers "which subroutines does the outer loop keep calling?" where `hot` answers "which PCs dominate the inner loop?". Needs `--debugger-history > 0`. |
| `retgraph [N]` | — | As `callgraph` but for `RET`-sourced transitions (`callee → return-site` edges). Surfaces the dominant return paths — useful for spotting a routine that keeps returning to a bad/`speculative` site. Needs `--debugger-history > 0`. |
| `rstgraph [N]` | — | As `callgraph` but for `RST`-sourced transitions (`caller → RST-vector` edges). Surfaces which `RST $00/$08/$28/$38…` vectors the code leans on — handy on the Next where `RST $28` (calc) and the divMMC entry vectors dominate. Needs `--debugger-history > 0`. |

#### Time-travel buffer

An in-memory rolling ring of full-state emulator snapshots. The
buffer auto-captures every N Z80 instructions while the CPU runs;
the user can then `tt-rewind` to restore state to any captured
moment, or `tt-find-pc` to locate every snapshot that passed
through a given PC. Backed by the same snapshot machinery that
powers `snapshot-on-bp` (CPU + visible 8 RAM pages + 7FFD/1FFD +
border colour).

**Capture scope.** On classic models the Phase-1 snapshot (CPU +
visible 64 K + border + 7FFD/1FFD) is loss-free. On the **Next**, each
snapshot *additionally* captures the complete machine state (Phase 2b):
the full 2 MB RAM pool (all 128 × 16 K banks, not just the visible 8),
the MMU8 slot table (NR$50-$57), the divMMC RAM (16 × 8 K), the entire
256-byte NextReg file, and the paging ports. `tt-rewind` restores all of
it, so **forward re-execution from a Next rewind point is faithful** —
upper-bank state (Layer 2 framebuffer, divMMC channel/descriptor
scratch, alt-ROM windows) is fully recoverable.

Cost: on the Next each snapshot is ~2 MB, so the ring is ~`KEEP × 2 MB`
(logged at startup); lower `--time-travel-keep` if memory is tight.
Phase 2a additionally records per-snapshot bank/paging context for
`tt-status` (see below). A future Phase 2c may add changed-bank deltas
to shrink the ring.

| Command | Aliases | Purpose |
| --- | --- | --- |
| `tt-on [EVERY [KEEP]]` | — | Install the buffer. `EVERY` is the instruction interval between auto-captures (default 50000); `KEEP` is the ring depth (default 16). Memory budget ≈ `KEEP × 128 KB` on classic models, ≈ `KEEP × 2 MB` on the Next (full-state capture, Phase 2b). The pre-fetch hook fires inline with the CPU; cost per fetch is one uint64 compare. |
| `tt-off` | — | Remove the hook and drop the ring. |
| `tt-status` | `tt` | List ring contents: insn count + PC + optional label + **bank/paging context** per entry (`rom=N slots=… dmmc:paged/automap/mapram`). The bank context (Phase 2a) is captured per snapshot so a trace can't mis-attribute a PC to the wrong ROM/bank. (The context is informational; the actual state restore is handled by the full Phase-2b capture below.) |
| `tt-snap [LABEL]` | — | Force an immediate manual capture, optionally tagged. |
| `tt-rewind INSN` | — | Restore the latest snapshot whose insn count ≤ INSN. The CPU register set + visible 64 K + border are rolled back; **on the Next the full 2 MB pool + MMU8 slots + divMMC RAM + NextRegs + paging are restored too** (Phase 2b), so re-execution from the rewind point matches. |
| `tt-find-pc $XXXX` | — | List every captured snapshot whose saved PC equals `$XXXX`. Useful for "show me every visit to the failing PC". |
| `tt-clear` | — | Drop all captured snapshots; leave auto-capture armed. |

The `--time-travel=N` CLI flag (with optional `--time-travel-keep=K`)
installs the same buffer at headless startup — useful for catching
boot-time states the user would never have time to manually `tt-snap`.

Typical workflow on the menu-RETURN freeze:

```
./bin/zx_go --next --headless --time-travel=50000 \
    --debugger-port=10000 --press-key=space@200,enter@600 &
nc localhost 10000
> pause                                # at the freeze
> tt-status
OK time-travel on every=50000 keep=16 entries=12
  insn=50000   PC=$0038
  insn=100000  PC=$11A0
  ...
  insn=600000  PC=$1D47                # crash site
> tt-rewind 550000                     # roll back ~50K insns
OK rewound to insn=500000 PC=$1A6B
> backtrace ; prev 20                  # inspect pre-crash state
```

#### Provenance tracer + trace database (headless)

Where time-travel answers *"what was the state at time T?"*, the
provenance tracer answers *"which instruction wrote this byte?"* — the
data-lineage axis for cracking bad-jump / corrupt-stack traps. Three
headless CLI flags:

| Flag | Purpose |
| --- | --- |
| `--provenance` | Arm the last-writer index from boot (logical + physical pool keying). At end-of-run, dumps a `why-pc` line — if the run ended in a trap reached via `RET`, it names who pushed the return word. Pairs with the telnet `provenance` / `provenance-phys` / `why-pc` commands. |
| `--why-pc-at $ADDR[:BANK]` | Implies `--provenance`. Emit a `why-pc` dump the FIRST time PC re-enters `$ADDR` (optionally only in ROM `BANK`) — captures the stack + the `jumped_from` instruction at the instant a bad jump lands, before a self-loop trap obscures it. The autonomous form: one run pinpoints the faulty transfer. |
| `--trace-db PATH` (+ `--trace-db-keep N`) | Record M1 fetches into a bounded ring (default 500 000 most-recent rows) and flush them to a SQLite file at end-of-run (Tool #2). Query with the `sqlite3` CLI — the `m1` table columns are `seq, insn, pc, bank, sp, af, bc, de, hl, ix, iy`. Size `--trace-db-keep` large enough to span the event you want; the ring keeps only the most recent rows. |

Example — find the instruction that jumps into the bank-2 `$0000`
trap, then who pushed the bad return word:

```
./bin/zx_go --next --headless --frames 400 --why-pc-at '$0000:2'
... why-pc-at: re-entry ... jumped_from=$C02D opcode=$C7
... jumping-instruction provenance (physical) phys_bank=111 phys_off=$002D ...
```

Query a captured trace DB for control transfers that crossed a ROM
bank in the window:

```
./bin/zx_go --next --headless --frames 360 --trace-db /tmp/boot.db
sqlite3 /tmp/boot.db \
  "SELECT a.insn, printf('\$%04X',a.pc), a.bank, printf('\$%04X',b.pc), b.bank
   FROM m1 a JOIN m1 b ON b.seq=a.seq+1 WHERE a.bank<>b.bank LIMIT 20;"
```

Two trace DBs (e.g. ours vs a reference run) can be diffed with the
`_tools/tracediff` helper, which prints the first aligned row where
PC / bank / SP / registers differ — the first-divergence finder.

#### Crash detection

Heuristic patterns that nearly always indicate the guest has crashed.
Each heuristic fires exactly once per re-arm window — when the
failing condition clears the heuristic re-arms so a later crash is
still caught. Fires log an `slog.Info("crash-detected")` line with
`kind`, `pc`, `detail`, and the instruction count.

Five heuristics:

- **`nop-slide`** — PC walking through N consecutive $00 opcodes.
  Catches the guest falling through past valid code into a
  zero-filled region.
- **`sp-low`** — SP drops below a chosen threshold. Catches stack
  underrun (popped past the stack base).
- **`sp-high`** — SP rises above a chosen threshold. Opt-in; many
  routines legitimately push past high SP, so this stays off by
  default.
- **`pc-region`** — PC enters a forbidden range (e.g. screen RAM
  `$4000-$5AFF`). Opt-in — Layer 2 framebuffer reads can look like
  screen-RAM execution on the Next, so the default is no regions.
- **`halt-no-iff`** — HALT executed with `IFF1=0`. Only NMI can
  wake the CPU, in practice a true deadlock.

| Command | Aliases | Purpose |
| --- | --- | --- |
| `crash-detect` | — | Show current status: which heuristics are armed, their thresholds, and whether pause-on-fire is set. |
| `crash-detect on` | `enable` | Enable the conservative default config (`nop-slide=32`, `sp-low=$4000`, `halt-no-iff`). `pc-region` and `sp-high` remain opt-in. |
| `crash-detect off` | `disable` | Disable + remove the pre-fetch hook. |
| `crash-detect pause-on-fire` | `pause` | Toggle: when true, a fire pauses the CPU at the offending PC for inspection. Default false (= log only). |
| `crash-detect set KEY VALUE` | — | Adjust a single heuristic. Keys: `nop-slide N` / `sp-low $XXXX` / `sp-high $XXXX` / `halt-no-iff on\|off` / `pc-region NAME@LO-HI` / `pc-region clear`. The set is additive — repeated `set pc-region` appends. |

Live workflow: pause at the suspect moment → `crash-detect on` →
`crash-detect pause-on-fire` → `continue` → CPU halts at the
offending PC; `backtrace`, `prev 50`, and `disasm` are then
positioned exactly at the crash site.

#### Snapshots on hit

| Command | Aliases | Purpose |
| --- | --- | --- |
| `snapshot-on-bp [DIR]` | — | Drop a fresh `.szx` into DIR every time any breakpoint / register watch / memory watch / step-over fires. Files are named `bp_$PCNNNN.szx` with a 4-digit zero-padded counter so consecutive hits don't clobber. Pass no arg (or empty) to disable. DIR must already exist — the debugger refuses to mkdir. |

#### Foreign-emulator comparison

When a workload boots correctly under one emulator but not another, the
fastest path to the divergence is a state diff at a chosen sync point.
`compare-foreign` fetches a snapshot from another emulator via that
emulator's debug protocol, builds our own snapshot at the moment of
invocation, and renders a side-by-side diff highlighting only the
differing fields. Provider plugins live in `cmd/zx_go/foreign_*.go`.

| Command | Aliases | Purpose | Response shape |
| --- | --- | --- | --- |
| `compare-foreign` | — | List registered providers + usage hint. | `OK compare-foreign — providers: …` |
| `compare-foreign PROVIDER [ENDPOINT] [SELECTORS…]` | — | Connect to PROVIDER, fetch the requested state pieces, diff against ours. SELECTORS: `regs`, `mmu8`, `all`, `nr=LIST` (hex), `bank=N` (16K), `len=N` (bytes per bank). With no selectors, fetches a sensible default block (regs + MMU8 + 6 common NRs + RAM bank 0). | Multi-section diff: registers, MMU8 slots, NextRegs, RAM banks — each section shows differing fields with `*` markers and matching fields summarised. Bank diffs show match %, first divergent offset, and a 32-byte context hexdump at the divergence. |

To add a provider (a gdb-stub backend, real hardware via a Pi probe, …):
implement `ForeignProvider` / `ForeignEmulator` in
`cmd/zx_go/foreign_<name>.go` and register it via `init()`. The diff
command is provider-agnostic — adding one is roughly 150 LoC of
protocol adapter plus a `foreignRegister(yourProvider{})` call.

Worked example — boot-divergence isolation (reference-emulator details
elided per project policy; substitute your own reference binary +
remote-protocol port number below):

```
$ ./bin/zx_go --next --headless --debugger-port=10000 &       # ours
$ /path/to/reference-emulator --remote-protocol-port 10020 &  # foreign
$ nc localhost 10000
> compare-foreign PROVIDER localhost:10020 nr=03,04,8E,8C,56,57 bank=0
OK compare-foreign
  mine    = zx_go (self)
  foreign = PROVIDER@localhost:10020

--- NextRegs ---
  NR$04: $00  != $03  *
  NR$56: $DE  != $00  *
  NR$57: $DF  != $01  *
  NR$8E: $03  != $08  *
  NR$8C: $00
  NR$03: $B0  != $B3  *

--- RAM bank 0 (16K) ---
  size: mine=16384 foreign=16384  match=5428/16384 (33.1%)
  first divergence at offset $0000
  mine    $0000: 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 …
  foreign $0000: F3 C3 25 01 45 30 06 42 C3 EC 05 2A 2E 2A FF 00 …
```

Combined with `load-bin ram 0 0 /path/to/zes_bank0.bin`, you can OVERLAY
the foreign state on ours and ask "does our boot progress correctly from
this state?" to isolate WHICH of our differences is load-bearing.

### Implicit pause behaviour

Commands that *read* CPU or memory state are dangerous to run on a running CPU — the read could land mid-instruction. The telnet debugger handles this by **implicitly pausing the CPU** before any read-state command, and waiting until the headless loop has acknowledged the pause before reading.

The implicit-pause set is:

```
get-registers, regs, get-stack, stack, backtrace, bt, history,
hist, prev, p, get-memory, mem, hexdump, read-memory, peek,
write-memory, poke, disassemble, disasm, d, get-mmu, get-divmmc,
nextreg-read, nr-r, bank-peek, bank-poke, load-bin, compare-foreign
```

After an implicit pause, the CPU stays paused. Use `continue` to resume.

If the CPU is in a tight HALT or otherwise can't cooperate with the pause handshake, the command times out with `ERR pause-ack timeout`.

### Conditional breakpoints

The `if EXPR` clause attaches a guard expression evaluated at every potential hit. The breakpoint fires only when the expression evaluates true; non-matching hits silently continue. Combines naturally with `bank=N`: the bank filter cheaply rejects most fetches before the guard runs.

```
> bp $6010 if iff1 == 0
OK breakpoint @$6010 if iff1 == 0
> list-bp
OK $6010 if iff1 == 0
```

The expression grammar (small enough to write by hand, large enough for compound guards):

```
expr  := term  ( "||" term )*
term  := cmp   ( "&&" cmp  )*
cmp   := atom OP atom        | atom "in" range
OP    := "==" | "!=" | "<" | "<=" | ">" | ">="
range := atom "-" atom
atom  := hex | dec | reg | "read" atom
hex   := "$" XX..XX  |  "0x" XX..XX
reg   := pc | sp | a | f | b | c | d | e | h | l | ix | iy |
         iff1 | iff2 | im | halted | bank |
         dmmc | automap | mapram | conmem
```

`read $ADDR` performs a Memory.Read at evaluation time — same path the guest CPU sees, so MMU paging / divMMC overlay / alt-ROM redirect resolve correctly. `bank` is the current ROM bank (0..3) mapped at `$0000-$3FFF`.

The divMMC paging predicates (`dmmc` = paged-in, `automap`, `mapram`, `conmem`) mirror the `get-divmmc` fields and resolve to 0/1. They are the **faithful fix for low-PC bank aliasing**: a PC in `$0000-$1FFF` executes enNxtmmc, a NextZXOS bank, or the bootrom depending on whether divMMC is paged, so an `any-bank` breakpoint can land on the wrong instance. `set-breakpoint $0814 if dmmc == 1` halts only on the divMMC-paged visit — replacing the old `if iy < $8000` heuristic. Combine for precise modes, e.g. `if automap == 1 && conmem == 0`.

Worked examples:

```
bp $0066 if iff1 == 0                    # NMI vector only when masked
bp $6010 if iff1 == 0 && halted == 0     # the HALT-about-to-fire moment
bp $0038 if a != $80                     # IM 1 vector with an odd register
bp pc in $1FFA-$3FFF if bank == 0        # NOT VALID: pc-in-range guards need an
                                          # outer PC anchor, since the bp itself
                                          # is anchored to ADDR. Use the EXPR for
                                          # state filters; range is for atoms
                                          # (e.g. `a in $80-$FF`).
```

### Bank-aware breakpoints

The Spectrum Next has four 16-KB ROM banks visible at `$0000-$3FFF` (switched via NextReg `$8E` or the legacy 7FFD/1FFD ports). The same PC means different code in different banks.

A plain `set-breakpoint $1B67` fires whenever PC hits `$1B67`, regardless of which bank is mapped — usually unhelpful, because three of the four banks have unrelated code at that address.

`set-breakpoint $1B67 bank=2` fires only when ROM bank 2 is mapped at `$0000`. The CPU side checks the bank on every break-test; mismatches don't halt the emulator.

Use `get-mmu` to see the current bank:

```
> get-mmu
OK rom_bank=2 slots=FF FF 0A 0B 04 05 00 01
```

### Symbol-map annotation

A symbol-map file is plain text with one symbol per line:

```
# Lines starting with # or ; are comments.
$0000   RESET_ENTRY
$00EF   POST_RESET
$3BE8   SOFT_RESET_TRAMPOLINE
```

Pass it via `--symbol-map=PATH`. When loaded, `get-registers`, `get-stack`, `disassemble`, and `list-breakpoints` annotate addresses with their names.

`_tools/nextzxos.sym` ships in the repo with the well-known boot-ROM entry points labelled.

### Worked examples

#### Step through the cold-boot sequence

```bash
./bin/zx_go --next --headless --debugger-port=10000 \
            --debugger-pause-at-start \
            --symbol-map=_tools/nextzxos.sym &
nc localhost 10000
```

```
OK welcome to zx_go remote debugger
> get-registers
OK PC=$0000 (RESET_ENTRY) SP=$FFFF AF=$FFFF BC=$FFFF … insns=0
> step
OK stepped
> step
OK stepped
> get-registers
OK PC=$00EF (POST_RESET) SP=$FFFF AF=$FFFF … insns=2
> disasm
OK
  00EF  ED 91 07 03  NEXTREG $07,$03
  00F3  ED 91 03 B0  NEXTREG $03,$B0
  00F7  ED 91 C0 08  NEXTREG $C0,$08
  …
```

#### Bank-aware breakpoint

```
> set-breakpoint $1B67 bank=2
OK breakpoint @$1B67 (bank=2)
> continue
OK continuing
… (the emulator runs until bank 2 is mapped and PC hits $1B67)
> pause
OK paused
> get-registers
OK PC=$1B67 (BANK2_POST_LDIR_CALL_00A3) SP=$5BFB AF=$FFA8 … insns=475797
```

#### Inspect sysvars

```
> read-memory $5C3A
OK $FF
> get-memory $5C3A 16
OK FF 1C 00 00 00 00 00 00 00 00 00 00 00 00 00 00
```

`$FF` at `$5C3A` (ERR_NR) and `$1C` at `$5C3B` (FLAGS) is the standard post-NEW state for 128K BASIC.

#### Walk past a HALT

```
> disasm $34 8
OK
  0034  CC F2 FF     CALL Z,$FFF2
  0037  FF           RST $38
  0038  FB           EI
  0039  …
```

#### Toggle divMMC automap mid-boot

```
> nextreg-read $0A
OK $00
> nextreg-write $0A $10
OK
> get-divmmc
OK paged_in=false mapram=false automap=true
```

#### Poke a byte and verify

```
> write-memory $4000 $AA
OK
> read-memory $4000
OK $AA
```

#### Trace what code led up to a breakpoint hit

`--debugger-history=N` records the last N M1 fetches in a ring buffer. At a breakpoint, `prev N` dumps the lead-up — by the time most "why did the CPU end up here?" questions get asked, the relevant instructions have already executed and the only way to inspect them is through a history trace.

```bash
./bin/zx_go --next --headless --debugger-port=10000 \
            --debugger-history=64 \
            --debugger-pause-at-start
```

```
> bp $6010 if iff1 == 0
OK breakpoint @$6010 if iff1 == 0
> continue
OK continuing
*** breakpoint at $6010
> prev 12
OK
  insn=16185725   $1FC9  SP=$FFF9 AF=$07A3  IFF1=false IM=1 Halted=false  bank=0
  insn=16185726   $1FCA  SP=$FFFB AF=$07A3  IFF1=false IM=1 Halted=false  bank=0
  insn=16185727   $1FCB  SP=$FFFD AF=$07A3  IFF1=false IM=1 Halted=false  bank=0
  insn=16185728   $1FCC  SP=$FFFD AF=$07A3  IFF1=false IM=1 Halted=false  bank=0
  insn=16185730   $1FCF  SP=$FFFD AF=$0701  IFF1=false IM=1 Halted=false  bank=0
  insn=16185732   $1FD2  SP=$FFFD AF=$3E01  IFF1=false IM=1 Halted=false  bank=0
  insn=16185734   $1FD5  SP=$FFFD AF=$3D39  IFF1=false IM=1 Halted=false  bank=0
  insn=16185736   $1FD8  SP=$FFFD AF=$3D83  IFF1=false IM=1 Halted=false  bank=0
  insn=16185737   $1FE0  SP=$FFFD AF=$3D83  IFF1=false IM=1 Halted=false  bank=0
  insn=16185739   $1FE3  SP=$FFFD AF=$3D83  IFF1=false IM=1 Halted=false  bank=0
  insn=16185740   $600A  SP=$FFFF AF=$3D83  IFF1=false IM=1 Halted=false  bank=0
```

The SP transitions `$FFF9 → $FFFB → $FFFD → $FFFF` make the "three POPs + RET" sequence visible at a glance: the final RET at `$1FE3` pops the pre-reset stack frame all the way back to `$600A`. Without the history ring, reconstructing this took a manual env-var-driven trace + several emulator restarts last session — see `project_next_post_soft_reset.md` for the full investigation.

Note: `history` records PC + key register state, NOT memory. True "rewind" (restore SRAM to a past moment) needs full memory snapshots which the snapshot machinery covers separately.

#### Inspect the call ancestry at a HALT

When the CPU has wedged at a `HALT` and you need to know how it
got there, `backtrace` walks the stack and classifies each word.
Words preceded by a `CALL nn` opcode are "real" return addresses;
those preceded by `RST n` opcodes are tagged as `from RST`;
everything else is `speculative` (probably data left on the stack
by a `PUSH` or just uninitialised RAM).

```
> bp $6010 if iff1 == 0       (once conditional breakpoints land)
> continue
*** breakpoint at $6010
> backtrace 8
OK
  SP = $FFFB  IFF1=false  IM=1  Halted=false
  $FFFB: $0001   from RST  RST $00 @ $0000
          0001  C3 6A 00     JP $006A
          0004  EA 00 ED     JP PE,$ED00
          0007  8A           ADC A,D
  $FFFD: $600A   from CALL nn  CALL $6AFB @ $6007
          600A  C3 10 60     JP $6010
          600D  FF           RST $38
          600E  FF           RST $38
  ...
```

The "CALL $6AFB @ $6007" annotation tells you exactly which CALL
site pushed this return address — saving the manual hex dump
+ Z80 decode I had to do to crack #149's "the RET pops a
boot.bin pre-reset stack frame" insight.

#### Clean exit

```
> quit
OK bye
```

The connection closes; the TCP listener and emulator stay running.

---

## Memory watchpoints in headless mode

For batch / CI investigations where the GUI debugger isn't running, two headless flags cover the workflow:

### `--watch-writes NAME@ADDR[=VAL][,…]`

Logs every guest write to ADDR. The slog event includes the source PC plus
the full routing context at the moment of the write — ROM bank, alt-ROM
shadow (NR`$8C`), MMU0/MMU1 slot values, and divMMC paged/`E3` state — so a
hit on a `$0000-$3FFF` address is immediately attributable to the right
overlay (machine ROM vs esxDOS vs divMMC RAM bank) without a second run.

```bash
ZX_GO_USE_FPGA_BOOTROM=1 ./bin/zx_go --next \
    --watch-writes "DRIVE-STATE@E579" \
    --dump-state 600 \
    --log-level=debug 2>&1 | grep watch-write
```

```
… DEBUG watch-write name=DRIVE-STATE addr=$E579 val=$20 pc=$4045 rom_bank=3 alt=$C0 mmu0=$FF mmu1=$FF div=paged=true,E3=$00
… DEBUG watch-write name=DRIVE-STATE addr=$E579 val=$02 pc=$11DE rom_bank=3 alt=$C0 mmu0=$FF mmu1=$FF div=paged=true,E3=$00
…
```

### Break-on-write: `NAME@ADDR=VAL`

Append `=VAL` to upgrade a watch entry into a break-point. When a write of the matching value lands at ADDR, headless mode emits the same `break-on-write` slog event, dumps full state (CPU + sysvars + non-zero NextRegs + every `--dump-mem` range), and exits cleanly.

```bash
ZX_GO_USE_FPGA_BOOTROM=1 ./bin/zx_go --next \
    --watch-writes "BUG@E579=15" \
    --dump-mem "E570:20,F660:10" \
    --dump-state 600
```

If the trap fires, the output includes a full state dump exactly as `--dump-state` would emit, plus the configured `--dump-mem` grids. If the trap never fires, the normal end-of-run `--dump-state` runs as usual.

The value form is the right tool for finding _when_ a specific poisoned byte first appears. The log form (no `=VAL`) is for catching the _writer_ of any value, e.g. when reverse-engineering which routine touches a sysvar.

### `--dump-mem ADDR:LEN[,ADDR:LEN…]`

Adds arbitrary memory ranges to `--dump-state` and to the dump emitted by a break-on-write trap. Ranges are formatted as 16-byte hex rows.

```bash
$ ./bin/zx_go --next --dump-state 600 --dump-mem "E570:20,F660:10"
…

Memory $E570..$E58F:
  $E570: 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00
  $E580: 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00

Memory $F660..$F66F:
  $F660: 80 00 00 00 00 80 00 00 00 00 00 00 00 00 00 00
```

Reads go through `Memory.Read`, so they resolve through whatever paging / overlay state is active at dump time (MMU8 slots, alt-ROM, divMMC, FPGA bootrom). Same view the guest CPU would see — never the raw bank backing store.

### `--detect-uninit` — uninitialised-memory read detector

Answers the question that step-tracing and disassembly *can't*: when a
register or pointer holds a garbage value, **is it sourced from
uninitialised RAM (a missing init step) or computed from bytes the guest
actually wrote (a logic / emulation bug)?**

On enable, `Memory.EnableUninitTracking()` marks every allocated RAM
byte "not yet written by the guest" — crucially, the power-on cold-RAM
fill does *not* count as a write, so power-on garbage stays flagged.
Every guest `Write` then marks its byte written; a `Read` of a byte the
guest never wrote fires the detector, logging the **source PC** plus the
unified-pool location (`bank16k`, `off`). Default off → zero hot-path
cost; gated behind a single `if m.TrackUninit` in the RAM read/write
paths.

```bash
# Flag uninitialised reads made by code in the NextZXOS SD driver window
$ ./bin/zx_go --next --headless --frames 480 \
    --detect-uninit --detect-uninit-pc '$1E00-$1FFF' --detect-uninit-cap 40
DEBUG uninit-read pc=$1ED5 addr=$E022 bank16k=111 off=$2022
DEBUG uninit-read pc=$1ED8 addr=$E023 bank16k=111 off=$2023
DEBUG uninit-read pc=$1EEC addr=$E01C bank16k=111 off=$201C
…
```

The example reads `pc=$1ED5+` show the SD driver pulling a block-address
struct out of `$E01C-$E02B` (RAM bank 111 = $6F) that the guest never
initialised — i.e. the garbage LBA is *uninitialised RAM*, so the fix is
to find the missing initialiser (`--watch-writes` that address), not to
hunt an arithmetic bug.

Flags:
- `--detect-uninit` — enable. Implies `--log-level=debug`.
- `--detect-uninit-pc $LO-$HI` — only log reads whose PC is in the
  window (hi optional). Same syntax as `--trace-pc-range`. Without it,
  *every* uninitialised read logs — very noisy during early boot (stack,
  scratch buffers), so a window is usually essential.
- `--detect-uninit-cap N` — stop after N distinct `(pc,addr)` reports
  (default 200). Reports are deduped by `(pc,addr)`; the same site read
  twice logs once.

Notes / limits:
- Tracks the unified RAM pool (`Memory.ram`, all 128 × 16K banks) via the
  MMU8 and classic read/write paths. ROM reads and peripheral-served
  reads are not RAM and never flagged. Config-mode `$0000-$3FFF` window
  writes during the bootrom phase are not tracked (that phase is all
  "writes" anyway).
- It flags *every* uninitialised read, not just pointer-flowing ones —
  filter by PC window to the code you're investigating.
- `Memory.UninitReadObserver` is the programmatic hook (signature
  `func(addr uint16, bank16k int, off uint16)`); the headless wiring in
  `cmd/zx_go/uninit_cmd.go` captures the CPU to add the PC, dedupe, and
  cap. Telnet/visual wiring can reuse the same observer.

### `--watch-bank-write BANK:LO-HI`

Logs guest writes that land in a specific unified-pool **16K bank** at a
byte-offset range, keyed on the *pool location* — unlike
`--watch-writes` (which keys on the CPU address and so conflates every
bank that was ever paged there). Format `BANK:LO-HI[,…]` (bank decimal,
offsets hex with `$`/`0x`; `-HI` optional). The companion to
`--detect-uninit`: once the detector flags an uninitialised read at
`(bank,off)`, this answers "is that byte *ever* written, and by whom?"

```bash
# Is the struct the SD driver reads (bank 111 $201C-$202B) ever written?
$ ./bin/zx_go --next --headless --frames 700 --watch-bank-write '111:$201C-$202B'
# (no output) → never written by the guest → missing initialiser, not
# a late-ordering write. Widen the range to see neighbouring fields
# that ARE written and by which PC.
```

Implemented via `Memory.SetRAMWriteHook` (already bank-aware: fires with
the 16K bank index + offset on every RAM write), chained so it composes
with the debugger's existing write hook.

### `--time-travel` and related flags

Headless launch of the time-travel buffer. Installs the same
in-memory ring as the telnet `tt-on` command but at boot time, so
the early instructions are captured before a debugger session even
attaches.

```bash
./bin/zx_go --next --headless --time-travel=10000 --time-travel-keep=64 \
    --debugger-port=10000 --frames=500000 &
nc localhost 10000
> pause
> tt-rewind 50000
OK rewound to insn=50000 PC=$1B40
```

| Flag | Effect |
| --- | --- |
| `--time-travel=N` | Auto-capture every N Z80 instructions. 0 disables. |
| `--time-travel-keep=K` | Ring depth (oldest evicted). Default 16. |

See the runtime [Time-travel buffer](#time-travel-buffer) section
for the live command reference, scope limitations, and example
workflow.

### `--crash-detect` and related flags

Drives the same heuristic crash detector as the telnet `crash-detect`
command, but from launch rather than after a pause. Useful in
headless CI runs that need to fail fast when the boot wanders
off the rails.

```bash
./bin/zx_go --next --headless --frames 50000 --crash-detect \
    --snapshot-every 5000 --snapshot-prefix /tmp/snap_
```

`--crash-detect` enables the conservative default set
(`nop-slide=32`, `sp-low=$4000`, `halt-no-iff`). Per-heuristic flags
override individual fields and can be used alone (without
`--crash-detect`) to enable just one heuristic:

| Flag | Effect |
| --- | --- |
| `--crash-detect` | Master enable; turns on conservative defaults. |
| `--crash-nop-slide=N` | Fire after N consecutive `$00` opcode fetches. 0 disables. |
| `--crash-sp-low=$XXXX` | Fire if SP drops below this address. |
| `--crash-sp-high=$XXXX` | Fire if SP rises above this address. Opt-in only. |
| `--crash-pc-region=NAME@LO-HI[,…]` | Fire when PC enters any range. Opt-in. |
| `--crash-halt-no-iff` | Fire on HALT-with-IFF1=0 (true deadlock). |
| `--crash-halt-no-iff-off` | Force-disable the HALT heuristic even when `--crash-detect` is set. |

Each fire emits an `slog.Debug("crash-detected")` line with
`kind`, `pc`, `detail`, and the instruction counter, and also
emits a snapshot via the same one-shot path as `--snapshot-on-pc` /
`--loop-threshold`. Re-arm semantics: a fire is silenced until the
failing condition clears (NOP run broken, SP returns to bounds, PC
leaves region, HALT cleared), so a long-running headless boot
won't flood the log with the same fire.

### Combining with `--trace`

Trace channels and watchpoints stack: pair `--trace=nextreg,ports,bankswitch` with `--watch-writes` so that when the break fires you have a structured event log showing every NextReg / I/O / paging change leading up to the bug.

```bash
ZX_GO_USE_FPGA_BOOTROM=1 ./bin/zx_go --next \
    --watch-writes "BUG@E579=15" \
    --trace=nextreg,ports,bankswitch \
    --trace-output /tmp/trace.jsonl \
    --dump-state 600
```

On break-fire: stdout has the full state dump; `/tmp/trace.jsonl` has every NextReg / port event from boot to break with PCs and values. From there it's a quick diff against a known-working trace to find the divergence.

---

## Common workflows

### "Why is PC wandering through uninitialised RAM?"

```bash
./bin/zx_go --next --headless --debugger-port=10000 &
nc localhost 10000
> pause
OK paused
> get-registers
OK PC=$FA40 …  insns=170869
> get-stack
OK sp=$FFC6 words=$082D $FA61 $110F …
```

Top-of-stack `$082D` is what the most recent RET will pop. Trace backward from there.

### "Catch the moment X register changes"

The telnet protocol doesn't have a hardware watchpoint. Use the headless `--watch-writes` flag instead — see [Memory watchpoints in headless mode](#memory-watchpoints-in-headless-mode) below.

### "Compare CPU state across two runs"

Both `get-registers` and `get-mmu` emit single-line output. Diff them with normal shell tools:

```bash
diff <(echo regs | nc localhost 10000) <(echo regs | nc localhost 10001)
```

### "Run the visual debugger and the telnet debugger together"

They coexist. Open `Emulator → Debugger` in the GUI and ALSO pass `--debugger-port=N`. Both share the CPU and the breakpoint set; mutations from one are visible in the other.

### "Reuse my disasm output in another tool"

The disassembly format is stable: 4-digit hex address, two spaces, hex bytes, two spaces, mnemonic. Each line is one Z80 instruction. Safe to feed to `awk` / `grep`.

### "Find where two emulators first diverge"

For boot-path-faithfulness investigations against a reference Z80 emulator, the bundled scripts in `_tools/zrcp/` are the workhorse:

- **`cold_boot_lockstep.py`** — single-step both emulators, compare all CPU registers (PC/SP/AF/BC/DE/HL/IX/IY/I/IM) after each instruction. The undocumented F3/F5 bits are masked because they reflect a known emulator-spec divergence (FUSE-style vs reference) and don't typically alter behaviour. On the first divergence, prints both states and surrounding disassembly. Slow (one ZRCP roundtrip per step per emulator) but reliable; expect ~10 minutes for 100K-step divergence.

- **`cold_boot_bisect.py`** — exponential-growth + binary-search version: launch both emulators, advance N instructions (using the bulk `step N` / reference equivalent), compare. If matched, double N and restart. When divergence found, binary-search the bracket by relaunching both with smaller targets. Each restart ≈ 1.3 s; ~14 iterations to bracket a 64K-step divergence ≈ 18 s total. Much faster than lockstep for finding the approximate divergence step. Falls back on the reference emulator's quirks for fine-grain pinpointing (use lockstep inside the bracket for exact instruction).

Both scripts use the `step N`, `cold-reset`, and `set-reg` commands documented above. Output goes to stdout; redirect to a file to save the trace.

Workflow:
1. Launch the reference Z80 emulator with its remote-protocol port open (the bundled scripts hardcode the path to your installed reference binary — see comments at the top of each).
2. Launch our emulator paused at PC=0 with `--debugger-pause-at-start --frames=0`.
3. Run the bisect script first to locate the bracket in ~30 s.
4. Run lockstep inside the bracket for exact pinpoint.
5. Inspect both emulators' state at the divergent instruction. Common causes: NextReg power-on default mismatch, port read returning different bytes, undocumented opcode flag-bit difference.

---

For the full list of debug-instrumentation flags (`--snapshot-every`, `--watch-writes`, `--snapshot-on-pc`, `--loop-threshold`, `--trace=…`, etc.), see the "Headless mode and debug instrumentation" section in the main [README](README.md).
