# zx_go

A ZX Spectrum emulator written in Go.

The ZX Spectrum 48K was my first computer, and this project was created as a learning exercise to get to grips with Go. Building an emulator turned out to be a great way to learn the language — it touches concurrency, bit manipulation, real-time rendering, and low-level hardware emulation all at once.

## Features

- **Z80 CPU emulation** — full instruction set including undocumented opcodes, interrupt modes, and alternate register sets
- **Multiple Spectrum models** — 48K, 128K, +2, +2A, and +3 with appropriate ROM and memory paging
- **ULA rendering** — screen, border, attribute colours, and flash
- **Audio** — beeper sound via the ULA speaker bit
- **Keyboard** — mapped from the host keyboard with modifier key support
- **Snapshot support** — load and save in SNA, Z80, and SZX formats
- **Peripheral emulation** — DISCiPLE disk interface and Multiface (1, 128, 3) with NMI trigger

## Downloads

Pre-built binaries for macOS, Windows, and Linux are available on the [Releases](https://github.com/conorarmstrong/zx_go/releases) page.

| Platform | Binary |
|----------|--------|
| macOS (Apple Silicon) | `zx_go-macos-arm64` |
| macOS (Intel) | `zx_go-macos-amd64` |
| Windows | `zx_go-windows-amd64.exe` |
| Linux | `zx_go-linux-amd64` |

## Screenshot

![ZX Spectrum 128K boot screen](screenshot.png)

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

- **File** — load/save snapshots (.sna, .z80, .szx) or select a ROM
- **Machine** — switch between Spectrum models (48K, 128K, +2, +2A, +3)
- **Peripherals** — enable/disable DISCiPLE and Multiface interfaces
- **Emulator** — reboot, pause/resume, view ROM info, trigger NMI (F12)

## ROMs

Place ROM files in a `roms/` directory alongside the binary. The emulator expects standard Spectrum ROM images (e.g. `48.rom`, `128-0.rom`, `128-1.rom`, etc.).

## Project Structure

```
cmd/zx_go/         Main application entry point (Fyne GUI)
pkg/
  z80/             Z80 CPU core
  ula/             ULA — display rendering, border, audio, I/O ports
  memory/          Memory management and bank paging
  keyboard/        Keyboard matrix emulation
  audio/           Beeper audio system
  snapshot/        Snapshot load/save (SNA, Z80, SZX)
  roms/            ROM loading and model configuration
  peripherals/     Peripheral manager
  disciple/        DISCiPLE disk interface
  multiface/       Multiface hardware interface
```

## License

This project is provided as-is for educational purposes.
