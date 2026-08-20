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

### File → Save Snapshot / Load Snapshot crash on the SAM, ZX80 and ZX81

**Affects:** File → Snapshots & ROM → Save Snapshot… and Load Snapshot…, on
the SAM Coupé, ZX80 and ZX81. F2 / F4 quick-save is a different path and is
not this bug.

**What you see.** The window disappears. The process dies. There is no
error dialog: `createSnapshotFromEmulator` and
`applySnapshotToEmulator` both dereference `emu.ula` (`cmd/zx_go/main.go`),
and `ula` is nil on those three machines (the cleanup path already guards
the same pointer for this reason).

Those machines cannot be represented by `.szx` / `.z80` / `.sna` at all,
which is why F2 writes `quicksave.zxgostate` instead. The File menu still
offers the classic formats and then panics rather than refusing.

**Workaround:** use F2 / F4. Do not use File → Save Snapshot or Load
Snapshot on these machines.

### Saving a 128K-family machine records port $7FFD as 0

**Affects:** File → Save Snapshot, F2 on 48K/128K/+2/+2A/+3/Pentagon,
RZX recording, and `snapshot-on-bp`. All three portable formats.

**What you see.** Save a 128K (or +2 / +2A / +3 / Pentagon) game and load
it back. The picture is the wrong screen, the 128 editor ROM is paged in
over the game, or the machine crashes a moment later. RAM contents are
intact; the paging latch is not.

**Cause.** `createSnapshotFromEmulator` copies the eight physical banks
correctly, then writes `snap.Memory.Port7FFD = 0`
(`cmd/zx_go/main.go`). `GetPortState()` already returns the live latch
and is used everywhere else. On load, `applySnapshotToEmulator` feeds
that zero through `PageMemory`, which maps bank 0 at `$C000`, ROM 0, and
screen page 5, and leaves paging unlocked.

A second fault on the load path: `PageMemory` is the guest-facing port
write, so it no-ops when paging is already locked (`PagingEnabled` is
false). Restoring a snapshot after a 128K title has written bit 5 of
`$7FFD` therefore does not change the map at all.

`$1FFD` is not in `snapshot.MemoryState`. SZX writes `Port1FFD: 0` with
the comment "Not used in our emulator" (`pkg/snapshot/szx.go`), which
has not been true since +2A/+3 paging existed. A +3 snapshot loses
special paging and ROM 2/3.

**Workaround:** rewind / `replay-back` restore the registry, including
the paging latches. Use those rather than File → Save on a 128K-family
machine. A 48K save is unaffected, because there is no `$7FFD`.

### A 48K `.sna` written by File → Save does not store PC

**Affects:** File → Save Snapshot with a `.sna` name, on a 48K machine.
128K `.sna` stores PC in the trailer and is not this bug. F2 writes
`.szx`, so it is not this bug either.

**What you see.** Save a 48K game as `.sna`, load it, and execution
resumes at whatever two bytes happened to sit on the stack, with SP two
higher than you left it. Usually a crash or a jump into garbage.

**Cause.** The 48K SNA format keeps PC on the stack. `loadSNA` pops it
(`pkg/snapshot/sna.go`). `saveSNA` never pushes it. The tests already
say so: "48K SNA stores PC on the stack and our save path doesn't push"
(`pkg/snapshot/snapshot_test.go`). The round-trip tests avoid the case
by saving 128K SNA instead.

**Workaround:** save as `.szx` or `.z80`, or use F2.

### A `.z80` written by File → Save always sets bit 7 of R

**Affects:** File → Save Snapshot as `.z80`. Load of a `.z80` from
another emulator is fine; the decoder reads bit 0 of the flags byte
correctly. SZX stores the whole of R and is not this bug.

**What you see.** Save as `.z80` and reload. R is `original | 0x80`.
Refresh-based random numbers and a few loaders that sample R come back
wrong. Most games do not notice.

**Cause.** `saveZ80` initialises the flags byte to `0x01` and then ORs
bit 0 if R bit 7 is set (`pkg/snapshot/z80.go`). Bit 0 is therefore
always set. On load, bit 0 of that byte is copied into R bit 7. The
round-trip test uses `R = 0x80`, which cannot see the fault, and does
not check R at all on the way back.

**Workaround:** save as `.szx`.

### A Pentagon snapshot from another emulator loads as 48K

**Affects:** File → Load Snapshot of a `.szx` or `.z80` that names the
Pentagon (SZX machine id 7, `.z80` v3 hardware mode 9) or +3e (SZX id
6). Snapshots this emulator writes itself are tagged 128K, not
Pentagon, so they do not hit this path.

**What you see.** A Pentagon 128 snapshot from Fuse or similar loads as
a 48K machine: `ensureModelForSnapshot` does not switch, because
`Is128K` is false, and only banks 5/2/0 are treated as the 48K image.
The program crashes on resume.

**Cause.** `loadSZX` classifies `ZXSTMID_PENTAGON128` and
`ZXSTMID_PLUS3E` in the default branch as 48K (`pkg/snapshot/szx.go`),
even though both constants are defined. `loadZ80` has no Pentagon
hardware mode; the comment still says Pentagon is a machine "the
emulator doesn't model", which stopped being true when `--pentagon`
landed. Scorpion (SZX id 10, `.z80` v3 mode 10) has the same
classification bug; we do not emulate it, so a 48K fallback there is at
least a refusal rather than a wrong 128K map.

**Workaround:** switch to Pentagon 128 first, then load only snapshots
this emulator saved (they are tagged 128K). Or convert the file in
another emulator to a plain 128K `.szx` before loading.

### File → Save Snapshot races the emulation goroutine

**Affects:** File → Save Snapshot only. F2 and RZX capture both call
`withEmulationPaused` first.

**What you see.** Rare torn saves: PC from one instruction, registers
from the next, RAM a mix of both. Usually invisible; when it hits, the
file reloads as a crash.

**Cause.** `createSnapshotFromEmulator` copies CPU registers and eight
banks with no pause and without `coreMu`. The RZX recorder's own
comment says it pauses "so the CPU state isn't sampled mid-frame". The
File menu callback does not.

### Machine → Reboot can still take a pending NMI

**Affects:** F8 / F12 (NMI) followed by a reboot, on any machine that
uses `cpu.Reset`. The SAM and ZX80/ZX81 have their own reset and are
not this path.

**What you see.** Press F8 or F12, then reboot before the NMI is
serviced. The first instruction at `$0000` runs, then the CPU jumps to
`$0066` as if the button were still down. On a Next that opens the NMI
Browser in the middle of bootrom; with a Multiface it pages the MF ROM
in over the freshly reset machine.

**Cause.** `CPU.Reset` (`pkg/z80/z80.go`) clears registers, IFF, IM and
`tstates`. It does not clear `PendingNMI`, `IRQPending` or `eiDelay`.
`rebootLocked` calls that Reset and nothing else drops the latch.

### 128K / Next beeper and tape audio are mixed on a 48K frame

**Affects:** every 128K-family machine, including the Next. 48K is
correct.

**What you see.** 128K beeper music runs about 1.4% fast and can click
at the frame join. The tape-loading whistle is cut for the last ~1020
T-states of each 128K frame. On the Next at 28 MHz, most timed
beeper/DAC events after 69888 T in the frame are dropped.

**Cause.** `mixAudioFrame` and `generateSquareWaveFrame`
(`pkg/ula/ula.go`) hard-code `tstatesPerFrame = 69888`. Tape EAR
transitions use the same bound. The rest of the emulator already knows
the 128K/Next frame is 70908 (`pkg/roms/timing.go`,
`pkg/ula/classic_timing_test.go`).

### A Timex hi-res mode survives reboot

**Affects:** Machine → Reboot after a program has written Timex SCLD
(port `$FF`) mode 6, most visibly on the Next.

**What you see.** After NextZXOS 64/85-column, reboot can still draw a
640-wide scrambled picture until something writes `$FF` again.

**Cause.** `ULA.Reset` clears border, MIC, speaker, flash and AY. It
does not clear `timexVideoMode`, `ulaOutputDisabled`, the ULA scroll
offsets, or the Layer 2 `$123B` latch (`pkg/ula/ula.go`). Reboot only
calls `u.Reset()` for the ULA.

### A typed symbol can stick after focus loss or reboot

**Affects:** typing punctuation (`.`, `;`, `:`, …) and then alt-tabbing
or rebooting within two frames. F11 BREAK can also stick across
`ReleaseAll`.

**What you see.** A SYMBOL-SHIFT combo stays down after every physical
key has been released. BASIC types a run of the same symbol, or BREAK
stays asserted after reboot.

**Cause.** `TypeRune` holds the combo in `pulseMatrix` /
`pulseFrames`. `ReleaseAll` (`pkg/keyboard/keyboard.go`) only fills
`matrix` with `$FF`. `Scan` still ANDs the overlay while
`pulseFrames > 0`. Focus-loss and reboot both call `ReleaseAll`.

### NextZXOS writes to the SD card land on the wrong sector

**Affects:** Spectrum Next, any guest write to the SD image (save,
mkdir, copy). Reads, including cold boot, are not this bug.

**What you see.** NextZXOS boots and reads files correctly. A save or
copy writes into the wrong place in the `.img`, so the file is missing
or the image is corrupted. A subsequent read of that file returns the
old data.

**Cause.** The card advertises SDSC (CCS=0). CMD17 converts the host's
byte address to an LBA (`arg / 512`). CMD24 / CMD25 / erase pass the
raw argument to `WriteBlock` as an LBA (`pkg/next/sdcard/spi.go`). Real
SDSC uses byte addresses for both. A write of sector 2 therefore lands
at offset 2, not 1024. CMD25 then increments that value as if it were
an LBA, so sequential blocks are 512 sectors apart.

### Next DAC port `$5F` (and SpecDrum `$DF`) never reach the bank

**Affects:** Next programs that use SounDrive mode 1 or stereo A/D
(port `$5F` → channel D), and SpecDrum on `$DF` when the classic
add-on is not enabled.

**What you see.** The right-hand pair stays at mid-scale. A hard-panned
right DAC is silent.

**Cause.** The live decode in `Bank.WritePort` (`pkg/next/dac/dac.go`)
maps `$1F/$F1`, `$0F/$F3`, `$4F/$F9`, `$FB`. The FPGA map also has
`$5F` → D and `$DF` → A+D (`pkg/next/dac/soundrive.go`, labelled as
the faithful reference that is not wired). Stereo mix of A+B / C+D is
already correct; channel D is simply never written from those ports.

### Layer 2 ignores its clip window (NextReg `$18`)

**Affects:** Next titles that write the Layer 2 clip window, including
copper lists that MOVE `$18`.

**What you see.** Layer 2 is drawn full-frame. Sprites, ULA/LoRes and
the tilemap clip correctly.

**Cause.** `wire(0x18, l2)` stores the four coordinates
(`pkg/next/wire.go`). Sprite (`$19`), ULA (`$1A`) and tilemap (`$1B`)
each push into their layer. Layer 2 has no `SetClip`; `fpgaSramAddr`
hard-codes a full-screen window (`pkg/next/layer2/layer2.go`). The
layer's own state comment already records that the coordinates are
never pushed down.

### Load Tape, Open File, RZX and Kempston crash on the SAM (and Load Tape on the ZX80/ZX81)

**Affects:** File → Load Tape, File → Open File / drag-and-drop of
`.tap` / `.tzx` / `.rzx`, and Machine → Joystick → Kempston, whenever
`ula` is nil. File → Load Tape is also unguarded on the ZX80/ZX81
(Open File there already refuses Spectrum formats). This is separate
from File → Save/Load Snapshot, which is the same class of nil
dereference on a different menu item.

**What you see.** The window disappears. There is no error dialog.

**Cause.** Those handlers call `emu.ula.SetTapePlayer`,
`startRZXPlayback` (`e.ula.SetRZXPlaybackHook`) or
`emu.ula.KempstonEnabled` (`cmd/zx_go/main.go`). `loadFileByPath`
guards ZX80/ZX81 only. The SAM is not in that check.

**Workaround:** do not load tapes, RZX or Kempston on those machines.
Use File → Load SAM Disk on the Coupé, and `.p` / `.o` on the ZX80/ZX81.

### F11 BREAK release lifts CAPS SHIFT and SPACE even if they are still held

**Affects:** F11 as BREAK, while Left Shift or Space is down.

**What you see.** Hold Shift, tap F11, keep holding Shift. CAPS SHIFT
is gone: cursor keys and CAPS-LOCK combos stop working until Shift is
released and pressed again. The same happens with Space.

**Cause.** F11 down clears matrix bits 0.0 and 7.0. F11 up ORs them
back on (`pkg/keyboard/keyboard.go`) and returns, without looking at
the live Shift/Space state. This is not the TypeRune overlay already
logged under focus-loss.

### Port `$FF` Timex hi-res is honoured on 48K / 128K / +3

**Affects:** classic Sinclair machines. The Next is supposed to decode
this register (SCLD). The existing reboot-leaves-hi-res issue is
separate: that is leftover state after a valid Next write.

**What you see.** A 48K or 128K program that writes port `$FF` with
bits 2:0 equal to `110` (including `OUT (C),r` with C=`$FF`) switches
the emulator into a 640-wide Timex frame. Real hardware ignores that
write.

**Cause.** `writePortInternal` stores every `$FF` write in
`timexVideoMode` with no model check (`pkg/ula/ula.go`). `render()`
then takes `timexHiResActive()` on any machine.

### Z80N `JP (C)` jumps from BC, not from `IN (C)`

**Affects:** Spectrum Next programs that use `ED 98`.

**What you see.** The jump lands in the wrong 64-byte slot of the
current 16K page. Keyboard / UART jump tables go to the wrong
handler.

**Cause.** The opcode is `PC := (PC & $C000) | (IN(C) << 6)`
(SpecNext wiki; FPGA `t80n.vhd` JP_C: IORQ read, then
`PC[13:6] <= DI`, `PC[5:0] <= 0`). The implementation is
`PC = (PC & 0xC000) | (BC & 0x3FFF)` (`pkg/z80/z80n.go`) and never
reads the port. The unit test pins that wrong formula.

### SAM pokes, hexdump and the visual debugger look at a dummy 48K

**Affects:** the SAM Coupé: Emulator → Enter Poke, the visual
debugger, telnet `hexdump` / peek / poke, and `read $addr`
breakpoint conditions.

**What you see.** A poke reports success and the game does not
change. The hex view is a blank 48K Spectrum, not SAM RAM.

**Cause.** `newSamEmulator` (`cmd/zx_go/sam.go`) puts the live
machine in `e.sam` and installs an inert `memory.New(..., Model48K)`
as `e.mem` so the menus do not nil-panic. Those tools still read
`emu.mem`.

### GUI Debugger Step never delivers INT or NMI

**Affects:** the visual debugger's Step button, on every machine.
Telnet `step` and GUI Step Over are a different path.

**What you see.** HALT never ends. IM 1 / IM 2 handlers never run.
F8 / F12 does nothing while single-stepping.

**Cause.** The Step callback is `emu.cpu.StepInstruction()`
(`cmd/zx_go/main.go`), which only fetches and executes. Telnet
`step` uses `StepInstructionWithIRQ`. Step Over also uses
WithIRQ. Step does not.

A second fault on the IRQ-aware path: `StepInstructionWithIRQ`
hard-codes a 70908 T frame (`pkg/z80/z80.go`). SAM's frame is
119808 T with INT at 99840, so even telnet step never latches
the SAM frame interrupt.

### Copper write cursor is an instruction index, not a byte address

**Affects:** Next programs that upload a Copper list through
NextReg `$60`–`$62` (and anything that reads `$61` back). Cycle-
accurate MOVE timing is a separate, already-documented gap.

**What you see.** A list uploaded at byte address 2 lands in
instruction 2 (byte 4). After two `$60` writes, a read of `$61`
returns 1, not 2. The top 1K of Copper RAM cannot be addressed.
A list that does not HALT stops instead of wrapping. Writing
`$62` with the same start mode restarts the PC.

**Cause.** Hardware `nr_copper_addr` is 11 bits, 0–`$7FF`; `$60`
writes one byte and increments that address; `$62` bits 2:0 are
addr 10:8. This model treats `writePtr` as a 10-bit instruction
index, masks `$62` with `0x03`, and commits `$60` every two writes
(`pkg/next/copper/copper.go`). Mode 01/10 set `stopped` at
instruction 1024; any `$62` write with mode 01 or 11 does
`pc = 0`.

### Palette NextReg `$44` half-pair survives `$40` / `$41` / `$43`

**Affects:** Next software that writes a 9-bit colour, then changes
index or palette select, then writes `$44` again. Reboot already
clears the latch; this is the live-session case.

**What you see.** Colours land in the wrong slot. Later 9-bit writes
are off by one. NextZXOS palette editors and any copper list that
mixes `$40`/`$43` with `$44` show the wrong paper.

**Cause.** FPGA `zxnext.vhd` clears `nr_palette_sub_idx` on NR `$40`,
`$41` and `$43`. `WriteNR44` only clears `have9` after the second
byte (`pkg/next/palette/palette.go`). The `$40`/`$41`/`$43`
handlers never call `ResetWriteLatches`.

### Tilemap nibble 0 drops the palette offset and is treated as transparent

**Affects:** Next tilemap graphics whose pixel nibble is 0 but whose
attribute palette offset is not.

**What you see.** Background tiles vanish, or they pick `palette[0]`
instead of `offset<<4`. Sprites and Layer 2 are unaffected.

**Cause.** `tilemap.go` writes `dst[x] = 0` when the nibble is 0,
instead of `(paletteOffset << 4) | nibble`. Hardware compares the
nibble to NR `$4C` (reset `$0F`), so nibble 0 is opaque as index
`$00`, `$10`, `$20`, … The compositor then also skips low-nibble 0
when `on_top` is off (`pkg/next/compositor/compositor.go`).

### +3 / DISCiPLE DSK tracks are stored by the Track-Info C/H labels

**Affects:** Extended DSK images whose Track-Info C/H does not match
the file order (copy-protection, bad dumps, every track tagged
`C=0`). Standard commercial disks whose labels match physical order
are fine.

**What you see.** SEEK 0 hits an empty or wrong track. The loader
fails or reads the wrong sectors. Save and reload scramble the map
further.

**Cause.** After parsing each Track-Info block, `ParseDiskImage`
indexes `d.Tracks[H][C]` from the header labels if they are in
range (`pkg/plus3fdc/disk.go`). EDSK physical order is the
track-size table. In-range but lying labels skip the table-order
fallback, so two tracks with the same C/H overwrite one slot.

### SAM FORMAT reports success and writes nothing

**Affects:** SAM Coupé `FORMAT` / WRITE TRACK, and READ TRACK.

**What you see.** SAMDOS reports the disk formatted. The old sectors
are still there. A subsequent `BOOT` or `LOAD` reads the previous
contents.

**Cause.** `WD1772.WriteCommand` treats `$E` / `$F` as "complete
benignly" (`pkg/sam/wd1772.go`): INTRQ, no error, no bytes moved.

### A SAM reboot leaves the SAA1099 running

**Affects:** Machine → Reboot on the SAM Coupé.

**What you see.** Music from the previous program keeps playing over
the copyright screen.

**Cause.** `Machine.Reset` clears the CPU, paging, border, line INT
and beeper (`pkg/sam/sam.go`). It does not call `SAA.Reset()`.
`rebootLocked` only calls `e.sam.Reset()`.

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
