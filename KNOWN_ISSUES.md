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

and `gl_core.go` carries an explicit `!arm64`, so **no build tag can select
desktop OpenGL on arm64**. `internal/driver/glfw/glfw_es.go:8` then requests
`ClientAPI = OpenGLESAPI` at version 2.0.

That rule is correct for ARM single-board computers and mobile, where GLES is
the norm. On Windows on ARM it means the desktop build asks for a GLES 2.0
context through **WGL**, which requires the display driver to expose
`WGL_EXT_create_context_es2_profile`. That is an uncommon path on Windows,
where GLES normally arrives through ANGLE's EGL instead.

**Status.** Open, and not yet observed on real hardware. The one test run so
far was inside an emulated guest with no GPU, so it could not distinguish
"this driver refuses GLES" from "there is no graphics stack here at all".
Whether a Qualcomm driver grants the context is the entire open question, and
one launch on a physical device answers it.

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

**The upstream fix is one line.** In `internal/driver/glfw/glfw_es.go`,
alongside the hints already set:

```go
glfw.WindowHint(glfw.ContextCreationAPI, glfw.EGLContextAPI)
```

Both symbols exist in the GLFW binding already depended on
(`go-gl/glfw/v3.3`, `window.go:89` and `:132`). With that hint set, GLFW loads
`libEGL.dll` and bundling ANGLE becomes a working answer, since ANGLE maps
GLES onto Direct3D, which Windows on ARM does have. This is a change to the
GUI toolkit rather than to zx_go, so it needs raising upstream.

### Snapshot rewind does not restore the tape position

**Affects:** the **Time Travel** tab, the `tt-rewind` and `replay-back`
commands, all versions. It does **not** affect reverse debugging, which is a
separate mechanism described in `DEBUGGER.md`.

**What you see.** Rewinding during a tape load puts the machine back and leaves
the tape where it was, so the load resumes from the wrong place in the stream.

**Cause.** A capture is built from the devices registered in
`(*emulator).stateRegistry`, and the tape player is not one of them — it has no
`machinestate.Device`. Everything else the rewind used to leave behind is now
captured: the CPU, the whole RAM pool and the paging window, the ULA (frame
position and flash phase included), the AY's tone dividers, noise LFSR and
envelope position, the keyboard, the +3 FDC, the Beta interface, Interface 1,
the Multiface, the DAC, and the Next's own blocks. The +D and the Opus
Discovery are in the same position as the tape: neither has a `Device`, so
neither is captured.

**Two further limits, one of which has gone.** Rewind still lands on the
nearest capture at or before the target rather than on an exact instruction —
but `replay-back` now reaches any instruction inside the ring by re-executing
from a capture, with the whole machine. The ring still holds 16 captures by
default, so the reachable window is short.

**How you find out.** `replay-back` re-runs the window to the present and
compares the machine it produced against the machine that was there before
handing back an instant, so a load in flight over the window is refused rather
than replayed wrongly. `tt-rewind` does not check, because it is not
re-executing anything.

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
