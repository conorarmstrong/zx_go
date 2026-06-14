# Contributing to zx_go

Thanks for considering a contribution. This document covers what you
need to get a working dev loop, where things live in the codebase, the
conventions in force, and the patterns for the two most common
extensions — adding a new ULA test and adding a Spectrum Next NextReg
handler.

## Getting started

Requirements:
- Go 1.25 or newer
- A working OpenGL stack and audio backend (Fyne + GLFW; package
  install names below)

Platform packages:
- **macOS** — Xcode command-line tools (`xcode-select --install`).
- **Linux (Debian / Ubuntu)** — `apt-get install libgl1-mesa-dev xorg-dev libasound2-dev`.
- **Windows** — MSYS2 with `mingw-w64-x86_64-gcc` or the MSVC build
  tools. CI uses the default Windows runner with no extra packages.

Clone and build:

```
git clone https://github.com/conorarmstrong/zx_go
cd zx_go
go build ./...
./zx_go
```

The binary launches with the bundled 48K ROM. Switch models via the
`Machine` menu. Drop a `.tap`, `.tzx`, `.dsk`, `.z80`, `.sna`, `.szx`,
`.rzx`, `.nex`, or `.mdr` onto the window or use `File → Load…`.

## Dev loop

```
go test -short ./...     # fast feedback — ~30s
go test ./...            # full suite including conformance — ~3-4 min
go vet ./...
go build ./...
```

Three test gating levels exist:
1. **Default** (`go test`) — runs everything.
2. **`-short`** — skips Cringle Z80 conformance, real-NextZXOS-ROM
   boot, and a handful of other long integration tests. Use during
   inner-loop iteration.
3. **`-run TestZex`** — Frank Cringle's zexdoc + zexall instruction
   exerciser. ~85s combined. Runs in the dedicated `conformance` CI
   job; required to pass before merge if you touched `pkg/z80`.

Linting:

```
golangci-lint run
```

CI uses golangci-lint v2 with stricter defaults than v1 — if your
local lint passes on v1 it may still fail in CI. Match the CI version
locally:

```
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

## Project layout

```
cmd/zx_go/           — Fyne front-end + main loop
pkg/z80/             — Z80 / Z80N CPU core
pkg/memory/          — Paged memory model (classic + Next 8K MMU)
pkg/ula/             — ULA: video, audio, ports, tape I/O, contention
pkg/keyboard/        — Host-key-to-Spectrum-matrix mapping
pkg/ay/              — AY-3-8912 PSG (128K-series + Next multi-AY)
pkg/snapshot/        — SNA, Z80, SZX load / save
pkg/rzx/             — RZX input recording / playback
pkg/peripherals/     — Manager for optional add-ons
pkg/multiface/       — Multiface 1 / 128 / 3
pkg/if1/             — Interface 1 + Microdrive
pkg/if2/             — Interface 2 cartridge slot
pkg/disciple/        — DISCiPLE / +D disk
pkg/plus3fdc/        — +3 floppy disk controller (µPD765A)
pkg/microdrive/      — Microdrive cartridge format
pkg/kempmouse/       — Kempston Mouse
pkg/zxprinter/       — ZX Printer
pkg/next/            — Spectrum Next subsystems
  pkg/next/nextregs/   — NextReg port-file dispatcher
  pkg/next/divmmc/     — divMMC auto-pager
  pkg/next/esxdos/     — esxDOS RST 8 API
  pkg/next/nex/        — .NEX V1.2 loader
  pkg/next/layer2/     — Layer 2 framebuffer
  pkg/next/palette/    — 9-bit per-layer palette
  pkg/next/sprite/     — Hardware sprite engine
  pkg/next/copper/     — Copper coprocessor
  pkg/next/dma/        — zxnDMA
  pkg/next/rtc/        — Host-clock RTC
  pkg/next/uart/       — ESP UART stub
  pkg/next/sdcard/     — SD card host-directory mount
  pkg/next/compositor/ — Layer compositor
  pkg/next/dac/        — 4x DACs
  pkg/next/install/    — Next ROM blob install helpers
pkg/config/          — Persisted user settings
pkg/debugger/        — Debugger UI
pkg/testharness/     — Headless emulator for integration tests
pkg/roms/            — Embedded ROM bytes + ROM-manager
docs/                — User-facing documentation
fuse/                — Vendored FUSE 1.6.0 source for cross-reference
                       (FUSE is the reference Z80/Spectrum emulator;
                       we compare against it frequently)
```

## Conventions

**Memory and ULA interaction.** The CPU talks to a `memory.Memory` and
a `z80.ULA` interface, not concrete types. Peripherals that intercept
M1 fetches (DISCiPLE, IF1, Multiface, divMMC) register named hooks via
`cpu.AddPreFetchHook` / `cpu.AddPostFetchHook` rather than the legacy
single-slot `PreFetchHook`/`PostFetchHook` fields.

**Tests for hardware behaviour are integration tests, not unit tests.**
A "ULA test" usually means: build a small emulator (`pkg/testharness`),
load a known-good fragment of code, run a measured number of frames,
assert on rendered pixels or scanned matrix state. Pure-function tests
exist for things like flag computation; everything that touches the
bus uses the harness pattern.

**Comments cost something.** Add a comment only when the *why* is
non-obvious — a hidden constraint, a subtle invariant, a workaround
for a specific bug. Don't restate what the code does. Don't reference
the current task or the PR — those belong in commit messages, which
git preserves.

**One bundled commit per coherent change.** Sprints are committed
incrementally as they progress, but a single "fix the foo" commit
should land all the test, doc, and code changes for that fix.

**No co-authored-by trailers.** This repo doesn't attribute commits
to AI tooling.

## Adding a ULA / hardware test

The harness lives in `pkg/testharness`. Most existing tests follow
this shape:

```go
func TestSomeULABehaviour(t *testing.T) {
    h, err := testharness.New(roms.Model48K)
    if err != nil {
        t.Fatal(err)
    }

    // Poke a small program at 0x8000 and point the CPU at it.
    program := []byte{
        0xAF,       // XOR A
        0xD3, 0xFE, // OUT (0xFE), A — set border to 0
        0x76,       // HALT
    }
    for i, b := range program {
        h.WriteMemory(uint16(0x8000+i), b)
    }
    h.CPU().PC = 0x8000

    h.RunFrames(2)

    // Read back state via h.Memory / h.CPU() / h.ULA() accessors.
}
```

Accessors on `*Harness`: `CPU()`, `ULA()`, `MemoryBus()`, `Memory(addr)`,
`WriteMemory(addr, val)`, `RunFrames(n)`, `RunUntil(predicate, maxFrames)`,
`PressKey(key)`, `Reboot()`, `Peripherals()`. See `pkg/testharness/screen.go`
for the higher-level `ScreenText()` and `RunUntilText(want, maxFrames)`
helpers used by the IF1 / DISCiPLE / Next integration tests.

`pkg/testharness/next.go` does the same for ModelNext: wires the
NextReg dispatcher, palette, Layer 2, sprite engine, compositor,
Copper, and DMA into the ULA so a test can configure them via
register writes and assert on the rendered frame.

If your test needs a real ROM (NextZXOS, +3 boot, etc.) that doesn't
ship in the repo, gate it behind `testing.Short()` or a build tag and
skip when the ROM is absent. `TestNextRealROMBoot` is the working
example.

## Adding a NextReg handler

NextReg writes go through `pkg/next/nextregs.Dispatcher`. To handle a
new register:

```go
disp.SetOnWrite(0x42, func(d *nextregs.Dispatcher, val byte) {
    // 1. Apply side effects to whichever subsystem owns this register.
    // 2. Store the byte if guest code is allowed to read it back.
    d.Store(0x42, val)
})

disp.SetOnRead(0x42, func(d *nextregs.Dispatcher) byte {
    // Return live state, not the stored byte, when the register has
    // side-effect semantics on read (e.g. Copper cursor).
    return liveState
})
```

If the new register controls a subsystem that lives in `pkg/next/<x>`,
the wiring helper goes in `pkg/next/wire.go` so it can be applied to
both the production bus and the test harness from one place. Add a
matching test in the subsystem package and an integration test in
`pkg/testharness` if the register has cross-subsystem effects (e.g.
"writing this register changes how Layer 2 composites").

## Filing issues

- **Bug reports** should include the host platform, the failing
  command or repro steps, and whatever .tap / .z80 / .nex file
  triggers it (or a link to where it lives in the World of Spectrum
  archive).
- **Hardware-accuracy bugs** ideally cite the FUSE source line they
  diverge from (`fuse/fuse-1.6.0/...`).
- **Feature requests** are welcome — but if it's for an unimplemented
  peripheral (TR-DOS, TurboSound, etc.), check `ROADMAP.md` first to
  see if it's already on the deferred list.

## License

zx_go is released under the terms in `LICENSE`. Bundled ROM blobs
(48K, 128K, +2, +2A, +3, Interface 1 v2) are redistributed under
Amstrad's 1999 World-of-Spectrum letter. Next-era blobs (NextZXOS,
divMMC, esxDOS) are user-installed and not bundled — see
`docs/spectrum-next.md` for the install workflow.
