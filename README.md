# zx_go

A ZX Spectrum emulator written in Go.

The ZX Spectrum 48K was my first computer, and this project was created as a learning exercise to get to grips with Go. Building an emulator turned out to be a great way to learn the language — it touches concurrency, bit manipulation, real-time rendering, and low-level hardware emulation all at once.

![ZX Spectrum +3 menu](screenshot_plus3.png)

## Features

### Emulation
- **Z80 CPU** — complete instruction set including all prefix groups (CB, ED, DD, FD, DDCB, FDCB), undocumented opcodes, and correct flag behaviour
- **Five Spectrum models** — 48K, 128K, +2, +2A, and +3, each with correct ROM and memory paging
- **+3/+2A 4-ROM paging** — both port 0x7FFD and 0x1FFD with all four special paging modes
- **ULA rendering** — pixel-accurate screen display with attribute colours, bright, and 16-frame flash cycle
- **Mid-frame border effects** — border colour changes are tracked per-scanline for loading stripes and demo effects
- **Audio** — beeper sound output via the ULA speaker bit
- **Keyboard** — full 8x5 matrix emulation with CAPS SHIFT, SYMBOL SHIFT, arrow keys, and modifier support
- **Cycle-accurate timing** — memory contention during active display, port I/O contention with correct patterns for ULA/non-ULA ports, and model-specific contention (no ULA port contention on +2A/+3)
- **R register** — correctly incremented only on M1 opcode fetch cycles

### File Formats
- **Snapshots** — load and save in SNA, Z80 (v1/v2/v3), and SZX formats with full 48K and 128K support
- **TAP tape loading** — load .tap files and play them into the emulator with accurate pilot tones, sync pulses, and data encoding

### Peripherals
- **DISCiPLE** — Miles Gordon Technology disk interface with GDOS ROM and port-level emulation
- **Multiface** — Romantic Robot Multiface 1, 128, and 3 with NMI trigger (F12) and ROM paging

### Display
- **Scalable window** — View menu with 100%, 125%, 150%, 200%, and 300% scaling options
- **Full screen** — toggle from the View menu, press Esc to return to windowed mode
- **4:3 aspect ratio** — maintained at all sizes with black letterbox/pillarbox bars
- **Pixel-nearest scaling** — sharp pixels at all zoom levels

### Built-in Debugger

![Debugger](screenshot_debugger.png)

A full-featured Z80 debugger accessible from **Emulator > Debugger**:

- **Registers** — all Z80 registers displayed with colour-coded values: PC (yellow), register names (blue), values (white). Includes alternate registers (A'-L'), index registers (IX, IY), interrupt state (IFF1/IFF2, IM), and a stack preview showing the top 4 words at SP
- **Flags** — individual S, Z, H, P/V, N, C indicators with set/clear state
- **Disassembly** — scrollable Z80 disassembly from the current PC with full instruction decoding across all prefix groups. The current instruction is highlighted in yellow (>). Tap any line to toggle a breakpoint (shown in red with *)
- **Memory hex dump** — scrollable view of the entire 64KB address space (0000-FFFF) with hex and ASCII columns. Address entry supports hex, decimal, and octal via a base selector dropdown. PC location highlighted in yellow
- **Breakpoints** — tap disassembly lines to set/clear. When a breakpoint is hit during execution, the emulator automatically pauses. Active breakpoints shown in the status bar
- **Controls** — Pause, Step (single Z80 instruction without triggering interrupts), Step Frame (one full 69888 T-state frame), Run, Go to PC
- **Editing** — modify any register via the Edit Registers dialog; write hex bytes to any memory address via Write Memory
- **Live updates** — all panels refresh at 20Hz while the emulator is running, so you can watch registers change and the disassembly cursor move in real time
- **Status bar** — shows running/paused/halted state, all register pairs, interrupt mode, and active breakpoint addresses

## Downloads

Pre-built binaries for macOS, Windows, and Linux are available on the [Releases](https://github.com/conorarmstrong/zx_go/releases) page.

| Platform | Binary |
|----------|--------|
| macOS (Apple Silicon) | `zx_go-macos-arm64` |
| macOS (Intel) | `zx_go-macos-amd64` |
| Windows | `zx_go-windows-amd64.exe` |
| Linux | `zx_go-linux-amd64` |

## Building from source

Requires Go 1.25+ and the system dependencies for [Fyne](https://fyne.io/) (OpenGL, C compiler).

```bash
go build -o zx_go ./cmd/zx_go
```

## Running

```bash
./zx_go
```

The emulator starts in 48K mode by default. Use the menu bar to:

- **File** — load/save snapshots (.sna, .z80, .szx), load TAP tapes, or select a ROM
- **Machine** — switch between Spectrum models (48K, 128K, +2, +2A, +3)
- **View** — scale the display (100%-300%) or go full screen
- **Peripherals** — enable/disable DISCiPLE and Multiface interfaces
- **Emulator** — reboot, pause/resume, open debugger, view ROM info, trigger NMI (F12)

### Keyboard mapping

| Spectrum key | Host key |
|-------------|----------|
| CAPS SHIFT | Left Shift |
| SYMBOL SHIFT | Right Shift / Ctrl / Alt / Cmd |
| Arrow keys | Arrow keys (mapped to CAPS SHIFT + 5/6/7/8) |
| DELETE | Backspace (CAPS SHIFT + 0) |
| BREAK | F11 (CAPS SHIFT + SPACE) |
| NMI | F12 (Multiface red button) |
| Escape | Exit full screen |

## ROMs

ROM files are embedded in the binary, so no external ROM directory is needed. The emulator includes ROMs for all five models plus peripheral ROMs (Multiface, DISCiPLE/GDOS).

You can override embedded ROMs by placing files in a `roms/` directory alongside the binary. The emulator checks the filesystem first and falls back to embedded ROMs.

## Architecture

The emulator was developed with reference to the [FUSE](http://fuse-emulator.sourceforge.net/) emulator source code for accuracy, particularly:

- +3/+2A port decode isolation (0x7FFD vs 0x1FFD require stricter address matching)
- +3 special paging modes with correct slot 3 restore on exit
- Port I/O contention patterns (contended address + ULA port, contended + non-ULA, non-contended + ULA)
- IN instruction behaviour for unhandled ports (always update target register with 0xFF)
- Memory contention timing during active display

## Project Structure

```
cmd/zx_go/         Main application entry point (Fyne GUI)
pkg/
  z80/             Z80 CPU core with StepInstruction for debugger
  ula/             ULA — display, border, audio, tape, I/O ports
  memory/          Memory management, bank paging, contention
  keyboard/        Keyboard matrix emulation
  audio/           Beeper audio system
  snapshot/        Snapshot load/save (SNA, Z80, SZX)
  roms/            ROM loading with embedded fallback
  peripherals/     Peripheral manager
  disciple/        DISCiPLE disk interface
  multiface/       Multiface hardware interface
  debugger/        Built-in Z80 debugger with disassembler
```

## License

This project is provided as-is for educational purposes.
