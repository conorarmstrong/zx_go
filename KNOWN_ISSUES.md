# Known issues

Platform and runtime problems that are open, together with what is known
about each and what would close it. Every entry says what you will actually
see, so a symptom can be matched to a diagnosis without reading the source.

For **per-title** compatibility (which games load, which do not, and why) see
[`docs/compatibility.md`](docs/compatibility.md) instead. This file is about
the emulator itself on a given platform.

---

## Open

### The GUI may not open on Windows on ARM

**Affects:** `zx_go-windows-arm64.exe`, all versions to date. The x64 Windows
build is unaffected.

**What you see.** No window appears. The console shows:

```
Fyne error: window creation error
  Cause: VersionUnavailable: WGL: Failed to create OpenGL ES context
```

or, on a machine with no OpenGL of any kind:

```
  Cause: APIUnavailable: WGL: The driver does not appear to support OpenGL
```

**Cause.** Our GUI toolkit selects its OpenGL ES painter by **architecture**,
not by platform. In fyne v2.6.3, `internal/painter/gl/gl_es.go` is chosen by

```
(gles || arm || arm64) && !android && !ios && !mobile && !darwin && !wasm
```

and `gl_core.go` carries an explicit `!arm && !arm64`, so **no build tag can select
desktop OpenGL on arm64**. `internal/driver/glfw/glfw_es.go:8` then requests
`ClientAPI = OpenGLESAPI` at version 2.0.

That rule is correct for ARM single-board computers and mobile, where GLES is
the norm. On Windows on ARM it means the desktop build asks for a GLES 2.0
context through **WGL**, which requires the display driver to expose
`WGL_EXT_create_context_es2_profile`. That is an uncommon path on Windows,
where GLES normally arrives through ANGLE's EGL instead.

**Status: blocked upstream. There is nothing to fix in zx_go.** The defect is
in the GUI toolkit's build constraints, and it is reported at
[fyne-io/fyne#6483](https://github.com/fyne-io/fyne/issues/6483) with a fix
offered at [#6484](https://github.com/fyne-io/fyne/pull/6484). It stays open
here until Fyne take a view. We are not carrying a local patch for it: a
`replace` onto a forked toolkit, for a platform we cannot test, is exactly the
kind of workaround this project rejects.

Note the one thing that is **not** blocked by that. It has still never been
observed on real hardware. Both reports are from virtual GPUs, so whether a
Qualcomm driver grants the context is unanswered, and if it does then the GUI
already works on a physical device and none of the above applies to it. One
launch settles that, independently of anything Fyne decide.

**What works regardless.** Headless mode does not touch the GUI toolkit or
OpenGL at all, and is fully functional on Windows ARM64:

```
zx_go-windows-arm64.exe --headless --frames 500 --save-screen out.png
```

**Ruled out by test: shipping ANGLE next to the executable does not help.**
Placing `libEGL.dll` and `libGLESv2.dll` beside the binary, the way Chromium
does, changes nothing: the error still names WGL. GLFW never consults EGL
unless the application asks it to, and the toolkit sets only `ClientAPI` and
the context version, never `ContextCreationAPI`. Do not spend time on DLL
placement; it cannot work without the upstream change below.

**The upstream fix, and the shape it must take.** GLFW will use EGL, and so
load `libEGL.dll`, only if asked via `glfw.WindowHint(glfw.ContextCreationAPI,
glfw.EGLContextAPI)`. Both symbols exist in the GLFW binding the toolkit
already depends on (`go-gl/glfw/v3.3`, `window.go:89` and `:132`; also v3.4,
which Fyne's `develop` uses), so no new dependency is involved. Once the hint
can be reached, bundling ANGLE becomes a working answer, since ANGLE maps GLES
onto Direct3D, which Windows on ARM does have.

**It is not simply "set that hint", which is how this section once read.**
Setting it unconditionally bets that no Windows-on-ARM driver exposes
`WGL_EXT_create_context_es2_profile`. A physical Snapdragon may well do so, and
forcing EGL there would break a working device and oblige every Fyne
application to ship ANGLE. The fix has to be a *fallback*, tried only after the
native context API has already refused, so that it cannot regress anything.
That is what #6484 does.

### Snapshot rewind lands on a capture, not on an exact instruction

**Affects:** the **Time Travel** tab and the `tt-rewind` command. It does
**not** affect reverse debugging, which is a separate mechanism described in
`DEBUGGER.md`.

**What you see.** A rewind puts the machine back to the nearest capture at or
before the target rather than to the instruction you asked for, and the ring
holds 16 captures by default, so the reachable window is short.

**What to use instead.** `replay-back` reaches any instruction inside the ring
by restoring the newest capture at or before it and re-executing forward, with
the whole machine. Before handing an instant back it re-runs the window to the
present and compares the machine it produced against the machine that was
there, so a window it cannot reproduce is refused rather than answered wrongly.
`tt-rewind` does not check, because it is not re-executing anything.

### The process does not exit when window creation fails

**Affects:** any platform where the GUI cannot obtain an OpenGL context, which
today means the case above.

After the error, the process stays resident with no window and no console
prompt returned. It was still running three minutes later using about 40 MB.

The cause is that the toolkit's run loop blocks with no window to service, and
its `NewWindow` returns no error for us to test. A clean exit with a non-zero
status and a one-line explanation would be friendlier, but the only way to do
that from our side would be a watchdog around the run loop, which is the kind
of workaround this project rejects. It belongs in the same upstream report as
the hint above.

**Workaround:** close the process from Task Manager, and use `--headless`.

---

## Recently fixed

### Snapshot rewind did not restore a +D or Opus Discovery disk interface

**Fixed.** Rewinding while a +D or Opus Discovery operation was in flight put
the machine back and left the controller where it was, so the operation resumed
against a controller mid-command. Neither interface had a
`machinestate.Device`, so neither was in the capture at all.

Both have one now (`pkg/disciple/state.go`, `pkg/opus/state.go`), and with them
**every disk interface the emulator offers is captured**. What they carry is
mostly state no port reports: the transfer buffer and its position, the sector
a write was addressed to when the command started, the physical head position
of each drive as distinct from the track register, the direction the last step
went, and the whole WRITE TRACK parser mid-format.

The Opus additionally carries its DRQ byte-clock, which matters more than it
sounds. The Opus wires DRQ to the Z80's NMI and its ROM moves one byte per
interrupt, so a capture that lost the pending byte-period would restore a
transfer that never advances: BUSY stays asserted and the ROM's wait loop spins
forever.

Disks are deliberately not captured, on the same rule as the tape below: they
are the medium. A rewind cannot un-write a floppy, and it must not un-eject or
re-eject one either.

Fixing this turned up a real controller bug on the way. The +D's format flag
was sticky across commands, so a Write Track abandoned by a Force Interrupt
left every later Write Sector to be committed as a format: its 512 sector bytes
were parsed as a track image and the whole track rebuilt from them. That is
fixed too.

### Snapshot rewind did not restore the tape position

**Fixed.** Rewinding during a tape load put the machine back and left the tape
where it was, so the load resumed from the wrong place in the stream and came
back as `R Tape loading error`.

The tape player had no `machinestate.Device`, so it was not in the capture. It
has one now (`pkg/ula/tapestate.go`), carrying the playback position and
everything that decides the next edge: the block index, the position in that
block's pulse train, the T-state clock and the moment the last edge fired, the
EAR level, whether the block's bytes are already off the tape, whether the tape
is playing, and any open TZX loop.

The tape **image** is deliberately not captured. It is media, the contents of a
file the user mounted, and a rewind does not unmount or reload it, exactly as
rewinding does not change which cassette is in the deck. Leaving it out also
keeps a capture at a few hundred bytes instead of the size of the whole tape.

The player is registered only when a tape is actually mounted, so a capture
taken with an empty deck does not claim to carry a tape position, and is
refused if it is applied to a machine that has since had a tape put in.

### ANSI escape sequences printed literally in the Windows console

**Fixed** (see `CHANGELOG.md`). The startup banner and log output appeared as
`<-[38;5;196m` and similar rather than as colour.

`term.IsTerminal` accepts a Windows console handle, because `GetConsoleMode`
succeeds on one, but nothing enabled `ENABLE_VIRTUAL_TERMINAL_PROCESSING`, so
the console printed the escapes instead of interpreting them. Colour now
requires the console to accept ANSI as well as be a terminal, and falls back
to plain text when it will not (`pkg/zxlog/color.go`).

This was never ARM-specific. It affected any legacy Windows console on any
architecture, and was only found when someone first looked at a real Windows
console rather than at a redirected log. Redirected output was never affected,
which is why no captured log ever showed it.

---

## What the Windows ARM64 evidence does and does not support

The emulator core is **proven** on ARM64. A headless run matched a
linux-amd64 reference on every deterministic value:

| Measure | amd64 | Windows ARM64 |
| --- | --- | --- |
| Final PC | 5631 | 5631 |
| Instructions executed | 3 972 119 | 3 972 119 |
| Interrupt fires | 419 | 419 |
| Screen SHA-256 | `e0a590c3…a108a04` | `e0a590c3…a108a04` |

Identical instruction counts and a byte-identical screen rule out the whole
class of architecture bugs worth worrying about: word size, struct alignment,
shift and rotate edge cases, and any float in timing.

**One caveat on that evidence.** The guest was a fully emulated ARM64 machine
(QEMU TCG on an x86 host), not physical hardware. TCG executes real AArch64
semantics, so the arm64 compiler output genuinely ran, and the headless path
is single-threaded, so its determinism result holds in full. What TCG does
**not** reproduce is ARM's weak memory model, so a data race between the
emulation goroutine and the GUI would be invisible there and could still
appear on a physical device. That gap closes with the same single test run
that answers the GLES question.
