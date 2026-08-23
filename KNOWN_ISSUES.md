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
(gles || arm || arm64) && !android && !ios && !mobile && !darwin && !wasm && !test_web_driver
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

### The process does not exit when window creation fails

**Affects:** any platform where the GUI cannot obtain an OpenGL context, which
today means the case above.

After the error, the process stays resident with no window and no console
prompt returned. It was still running three minutes later using about 40 MB.

**Cause.** The toolkit does try to exit, and its own guard throws the attempt
away. `ShowAndRun` is `Show()` then `Run()`, and `Show()` runs the window
creation *inline* on the main goroutine, because the run loop has not started
yet (`internal/async/goroutine.go:44`, `internal/driver/glfw/loop.go:41`). So
when `glfw.CreateWindow` fails, `initFailed` sees `running` still false and
takes the `Quit()` branch rather than the `os.Exit(1)` one
(`glfw/driver.go:146-153`). `Quit()` closes the `done` channel only under
`running.CompareAndSwap(true, false)`, which cannot succeed while `running` is
false, so `done` is never closed (`glfw/driver.go:110-113`). `Run()` then
starts the loop, which blocks on `<-d.done` (`glfw/loop.go:128`) waiting for a
quit that was raised before it existed and discarded.

None of that is reachable from here. `NewWindow` returns no error, and the
window, the flag and the channel are all unexported. A watchdog around the run
loop would paper over it, which is the kind of workaround this project rejects.
It belongs in the same upstream report as the hint above, and the shape of the
fix is that a quit raised before the loop starts has to be remembered rather
than dropped.

**Workaround:** close the process from Task Manager, and use `--headless`.

---

## Recently fixed

### SAM `.sbt` files could not be loaded, for a reason that was wrong

**Fixed.** An SBT is not a disk image. It is the raw content of a single SAM
CODE file meant to be written onto a disk and booted, so loading one means
building a disk around it. That much was right.

The stated obstacle was not. This file, and `docs/sam-coupe.md`, both said
`BOOT` loads **directory slot 1 as the DOS**, so a disk carrying only the
user's file had no DOS on it and the ROM answered `53 No DOS` — and that we
therefore needed a DOS image we did not have and could not establish the
redistribution status of. The ROM disproves all of it. `BOOT` never reads the
directory:

| | |
| --- | --- |
| `$591E` | `LD DE,$0401` — track 4, sector 1 |
| `$5939` | `DI` / `LD C,$80` (READ SECTOR) / `LD HL,$8000` |
| `$5967` | compare the four bytes at `$8100` against the literal at `$FB94` (`42 4F 4F D4`), under `AND $5F` |
| `$5976` | `RST $08` / `DEFB $35` — error 53 |
| `$597B` | `JP $8009` |

Error 53 is that four-byte comparison failing, and nothing else: the whole 32K
ROM contains exactly one `CF 35`, at `$5976`. The `AND $5F` is why case and the
BASIC keyword-terminator bit do not matter. No DOS is read, needed, or bundled.

So an `.sbt` now builds an 800K MGT disk in memory: the file at cylinder 4 head
0 sector 1 behind a 9-byte CODE header, then a sector chain carrying 501 bytes
in the first sector and 510 in each one after it, because the bootstrap reloads
two bytes back over the previous sector's tail. Those two bytes are the next
track and sector, with bit 7 of the track byte selecting side 1, and a zero pair
ends the chain. A SAMDOS-shaped directory entry goes in so a DOS booted from the
disk does not overwrite the file.

A file that does not carry the signature the ROM checks is refused by name, so
it fails as "not a bootable SBT" rather than as the machine's `53 No DOS`.

Two details were measured against a real SAMDOS rather than taken from folklore,
because the first attempt at them was wrong: the CODE header's byte 7 is the
length divided by 16384, and byte 8 is the start page plus a fixed bias
(SAMDOS 2 wrote `$5F`, `$60`, `$61` for pages 0, 1 and 2). `$5F` is not a page
this machine can page in, which is a further reason to treat it as an opaque
stored field.

**Known limit.** `BOOT` reads drive 1 only, so an `.sbt` loaded into drive 2
can be read with `LOAD` but not booted. The load dialog says so.

### File → Save / Load Snapshot crashed on the SAM, ZX80 and ZX81

**Fixed.** Those three machines have no Spectrum ULA — `emu.ula` is nil
on all of them — but the File menu offered the classic snapshot formats
anyway and both handlers dereferenced it for the border colour. The
window disappeared with no error dialog.

A new `spectrumOnlyFeature()` names the machine and what to use instead,
and guards `createSnapshotFromEmulator`, `applySnapshotToEmulator` and
`startRZXPlayback`. F2 / F4 were always the right answer on those
machines; now the menu says so instead of crashing.

### Saving a 128K-family machine recorded port $7FFD as 0

**Fixed.** `createSnapshotFromEmulator` copied the eight banks correctly
and then wrote a hardcoded zero for the paging latch, so every reload
mapped bank 0 at $C000, ROM 0 and screen page 5 whatever the machine was
actually doing. Both latches now come from `GetPortState`.

Two related faults went with it. `$1FFD` was absent from
`snapshot.MemoryState` and the SZX writer put a literal 0 in its slot, so
a +2A/+3 save lost special paging and ROM 2/3. It is now part of the wire
format and round-trips through both SZX and `.z80`: a snapshot carrying a
non-zero latch goes out as a 55-byte v3 header tagged +3, and the reader
takes offset 86 only from the machines that define it, because on
anything else that byte is undefined and acting on it would switch the
guest into special paging. And the restore ran through `PageMemory`, the
guest-facing port write, which no-ops once a title has locked paging with
`$7FFD` bit 5 — a restore after that changed nothing at all. Restores go
through `RestorePagingLatches` now: placing a machine into a state is not
the same as asking it to change state, so the lock cannot block it, and
bit 5 re-establishes the lock afterwards exactly as on hardware.

### A 48K `.sna` written by File → Save did not store PC

**Fixed.** The 48K SNA format has no PC field: PC is pushed onto the
guest stack and the header SP points at it, which is why `loadSNA` pops
it. `saveSNA` never pushed. A reload resumed at whatever two bytes
happened to sit under SP.

The push is now modelled the way the Z80 does it, on a copy of the
affected banks so a save cannot mutate the snapshot it was handed. Bytes
that land below $4000 fall in ROM and are discarded, as on hardware.

### A `.z80` written by File → Save always set bit 7 of R

**Fixed.** The format splits R across two bytes, with bit 7 travelling in
bit 0 of the flags byte. `saveZ80` seeded that byte with `0x01` before
OR-ing the real bit in, so bit 0 was set whatever R held and every reload
came back with `R|0x80`. The old round-trip test used `R = 0x80`, which
cannot see the fault.

### A Pentagon snapshot from another emulator loaded as 48K

**Fixed.** `loadSZX` classified Pentagon 128, +3e, Scorpion,
Pentagon 512/1024 and the SE in its default branch as 48K, so only banks
5/2/0 were placed and paging was left off — the program crashed on
resume. Every one of them pages banks 0-7 through `$7FFD` the way a 128K
does, whatever else it adds, and all are classified as 128K now. The
`.z80` reader gained the v3 hardware modes it was missing (6 = 128K+MGT,
9 = Pentagon 128, 10 = Scorpion 256). One existing test pinned the
Pentagon bug; it was corrected.

### File → Save Snapshot raced the emulation goroutine

**Fixed.** The copy walked the CPU registers and eight banks with the
emulation goroutine still running, so a save could take PC from one
instruction and the registers from the next and reload as a crash.

Capture now takes `coreMu`, which the emulation goroutine holds for a
whole frame. Setting the pause flag is not enough on its own — nothing
acknowledges it, so a goroutine already inside `ExecuteFrame` finishes
the frame regardless — which is why the load path had always used the
lock. Two callers run inside that frame, the RZX autosave and the
snapshot-on-breakpoint hook, and use a `Locked` variant.

### Machine → Reboot could still take a pending NMI

**Fixed.** `CPU.Reset` cleared the registers, IFF, IM and the T-state
counter but left `PendingNMI`, `IRQPending` and `eiDelay` latched. An NMI
raised by F8 / F12 just before a reboot survived it: the machine ran the
first instruction at `$0000` and then jumped to `$0066` as if the button
were still down — on a Next that opened the NMI Browser in the middle of
bootrom, and with a Multiface it paged the MF ROM in over the fresh
machine. A hardware /RESET drops the request lines with everything else.

### 128K / Next beeper and tape audio were mixed on a 48K frame

**Fixed.** The mixer hard-coded 69888 T-states. A 128K frame is 70908 and
a Pentagon's is 71680, so 128K beeper music ran about 1.4% fast and every
event in the last ~1020 T of each frame was dropped; on a Next at 28 MHz,
where the CPU burns `SpeedMultiplier` times as many T-states inside one
50 Hz frame, most of the frame went with it. The length now comes from
the model and the speed multiplier.

### A Timex hi-res mode survived reboot

**Fixed.** `ULA.Reset` cleared the border, MIC, speaker, flash and AY but
not `timexVideoMode`, so a reboot out of a 64-column NextZXOS screen kept
drawing a 640-wide scrambled picture until something wrote `$FF` again.
The NextReg `$68` ULA-output disable, the ULA scroll offsets and the
`$123B` read-back shadow went the same way; all four reset to zero on the
FPGA and now do here.

### A typed symbol could stick after focus loss or reboot

**Fixed.** `TypeRune` injects a SYMBOL-SHIFT combination as a brief
overlay on the physical matrix. `ReleaseAll` — which focus loss and
reboot both call — cleared only the physical matrix, and `Scan` went on
ANDing the overlay while its frame counter ran, so BASIC typed a run of
the same symbol after every key had been let go.

### NextZXOS writes to the SD card landed on the wrong sector

**Fixed.** The card advertises SDSC, whose command argument is a BYTE
address. Only the read path divided it back to a block number: CMD24,
CMD25 and the CMD32/33 erase range passed the raw argument through as if
it were already an LBA, so a write addressed at sector 2 landed at offset
2 rather than 1024 and CMD25 stepped 512 sectors between consecutive
blocks. All five paths go through one conversion now. Four existing tests
passed raw block numbers, which was only correct under the bug.

### Next DAC port `$5F` (and SpecDrum `$DF`) never reached the bank

**Fixed.** The live decode was missing `$5F` — channel D in SounDrive mode
1 and in stereo A/D — and `$DF`, the SpecDrum mono A+D pair, so the
right-hand pair stayed at mid-scale and a hard-panned right DAC was
silent.

The decode is now the FPGA's whole port-to-channel table
(`zxnext.vhd:2652-2655`), which also corrected `$FB`: it is a mono A+D
port like `$DF`, not channel D alone, so a Covox or mono program writing
there came out hard-panned right for exactly the same reason. `$3F` and
`$B3` are in the table too. Taking part of the table was worse than
taking none of it.

The classic SpecDrum / Covox dispatch moved ahead of the internal bank in
the process: both decode `$DF` and `$FB`, and an add-on the user has
explicitly enabled should win the port.

### Layer 2 ignored its clip window (NextReg `$18`)

**Fixed.** Sprites (`$19`), the ULA (`$1A`) and the tilemap (`$1B`) each
had their window pushed into the layer that renders with it. Layer 2's was
stored in the wire layer and never arrived, so Layer 2 drew full-frame
however the guest clipped it.

`Layer2` gained `SetClip` and applies `layer2_clip_en` in both the address
model and the render path, with the FPGA's 2-pixel X scaling in the wide
resolutions and its $00/$FF/$00/$BF reset window. The GHDL golden passes
on every captured vector once the fixture sets the window its testbench
tied wide open.

A clipped pixel is reported through the FPGA's per-pixel `layer2_en`
plane rather than as index 0. The compositor decides Layer 2 transparency
by comparing a pixel's COLOUR against NR$14, so a zero index is an
ordinary opaque black to it: signalling "no pixel" that way painted the
clipped region solid black over the layers beneath instead of letting
them through. Worst case was a 320x256 program that never writes NR$18,
where the FPGA's `$BF` reset window covers only the first 192 rows.

Found on the way and fixed with them: `ClipWindows.LoadState` restored
the register copies but never pushed them down, and the NextReg reset
handler pushed three of the four windows and left Layer 2's behind — so
a rewind or a reboot across a clip change put the registers back and left
the layers drawing the rectangle the running program had set.

### Load Tape, Open File, RZX and Kempston crashed on the SAM

**Fixed.** The file-load guard covered only the ZX80/ZX81, so a `.tap`,
`.tzx`, `.rzx` or snapshot opened on the SAM went straight through to the
nil `emu.ula`. The extension check is split out as `admitFileForMachine`,
both so the SAM is covered and so the guard is reachable from a test —
the loader itself is a closure over the whole menu-construction scope.

Machine → Joystick → Kempston wrote `emu.ula.KempstonEnabled`
unconditionally; the other joystick types go through the keyboard matrix
and were already fine.

### F11 BREAK release lifted CAPS SHIFT and SPACE even if they were held

**Fixed.** BREAK asserts both keys, and releasing it ORed both back on
without looking at the live state — so a physically-held Shift or Space
was lifted out of the matrix with it and stayed up until the user
released and pressed that key again. The release now lifts only what
BREAK itself put down.

The converse held too: the modifier and base-key release paths cleared
the same two bits without consulting BREAK, so letting go of Shift or
Space while F11 was still down dropped half the combination and the guest
stopped seeing BREAK. Those releases are now recorded against the BREAK
bookkeeping instead of clearing the bit, so releasing F11 afterwards
still lifts it rather than leaving it stuck down for a key nobody is
holding.

### Port `$FF` Timex hi-res was honoured on 48K / 128K / +3

**Fixed.** Only a machine with an SCLD decodes that register. A Sinclair
ULA drives `$FF` as the floating bus and ignores writes to it, but every
write was stored regardless — so an ordinary `OUT (C),r` with C = `$FF`
from a Spectrum program switched the emulator into a 640-wide Timex
frame. The decode is now gated on the Next.

### Z80N `JP (C)` jumped from BC, not from `IN (C)`

**Fixed.** The FPGA core drives IORQ on M-cycle 2 with the address set to
BC and then takes the jump target from the byte that comes back
(`cpu/t80n.vhd:979-983`): `PC = (PC & $C000) | (IN(C) << 6)`, landing on
one of 256 64-byte slots in the current 16K page. We used BC directly and
never read the port, so keyboard and UART jump tables went to the wrong
handler. Two existing tests pinned the wrong formula; both corrected.

The byte is used whether or not the ULA claims the port, the same as
every other IN in the core: an unclaimed port returns the floating-bus
value, which is what the FPGA's `DI_Reg` latches and what chooses the
slot.

### SAM pokes, hexdump and the visual debugger looked at a dummy 48K

**Fixed.** `newSamEmulator` installs a stand-in 48K memory so the
Spectrum-shaped menus have something non-nil to talk to, and the live
machine's RAM sits behind the SAM itself. Every memory tool read the
stand-in: Enter Poke reported success and changed nothing, the hex view
showed a blank 48K Spectrum whatever the SAM was doing, and `read $addr`
breakpoint conditions tested the wrong bytes.

`pkg/debugger`'s memory is an interface now, which the Spectrum's
`memory.Memory` satisfies directly and a small SAM adapter fills in: the
SAM's four 16K sections are a page map of the same shape, LMPR and HMPR
occupy the two paging-latch slots, and VMPR gives the screen page.

### GUI Debugger Step never delivered INT or NMI

**Fixed.** The Step callback called `StepInstruction`, which only fetches
and executes, so HALT never ended, IM 1 / IM 2 handlers never ran and
F8 / F12 did nothing while single-stepping. It uses the IRQ-aware path
now, as telnet `step` already did, and so does Step Over's non-call
fall-through.

The IRQ-aware path had its own fault: a hardcoded 70908-T frame. The
SAM's frame is 119808 with INT at 99840, so single-stepping a SAM never
latched the frame interrupt at all. `CPU.FrameTStates` carries the
machine's frame length now, in the units that machine's own T-state
counter accumulates at its base clock: 3.5 MHz on a Spectrum, 6 MHz on
the SAM, with the turbo multiplier scaling from there.

### Copper write cursor was an instruction index, not a byte address

**Fixed.** `nr_copper_addr` is an 11-bit BYTE address and NR$60 writes
each byte straight into its own half of the addressed word — there is no
two-byte staging latch. We modelled a 10-bit instruction index with a
hi/lo pairing latch, so an address written to NR$61 landed at twice its
intended word, the top 1 KB of copper RAM was unreachable, and a read of
NR$61 after two NR$60 writes returned 1 instead of 2.

NR$62 carries three address bits, not two, and its read-back packs three.
The program counter restarts only when the mode actually CHANGES to 01 or
11, so setting the cursor's high bits — which has to go through the same
register — no longer rewinds a running list. Running off the end wraps:
the address counter has no terminal condition, where we stopped the
copper. The GHDL copper golden passes on the new model.

Allowing the wrap exposed a budget the caller had never charged
correctly. `Step` returned only the MOVE count, which the ULA subtracts
from one shared per-scanline budget, so NOOPs and re-tested WAITs were
free and a wrapping list lapped itself many times a line. `Step` now
charges in copper clocks — a MOVE costs two, one to raise `copper_dout_s`
and one to clear it, and everything else costs one — and the caller
budgets in the same unit.

The captured state carries a version with it. The write cursor changed
meaning rather than name, and gob does not police a schema: an older
capture would have restored silently at half its intended position and
assembled every instruction after it from the wrong offset.

### Palette NextReg `$44` half-pair survived `$40` / `$41` / `$43`

**Fixed.** The FPGA clears `nr_palette_sub_idx` on NR$40, NR$41 and NR$43
as well as at the end of a pair. We cleared it only after a completed
NR$44 pair, so an odd write followed by a change of index or palette left
the latch armed: the next `$44` was taken as the second byte of a pair the
guest thought it had abandoned, and every colour after it landed one write
out of step.

### Tilemap nibble 0 dropped the palette offset

**Fixed.** The FPGA assembles a tilemap pixel as the attribute's palette
offset in the high nibble and the tile's pixel nibble in the low one,
unconditionally, and decides transparency downstream by comparing the low
nibble against NR$4C (reset `$F`). We special-cased nibble 0 to a bare 0
and threw the offset away, so a background tile drawn with a non-zero
offset came out as `palette[0]`.

The compositor's matching approximation — "transparent over ULA when
`on_top` is off and the nibble is 0" — is replaced by the real per-pixel
`pixel_below_o` plane: `(attr bit 0 or mode_512) and not on_top`.

Dropping the nibble-0 rule needed a second plane with it. A palette index
is a colour — nibble 0 is an ordinary opaque `$00`, `$10`, `$20` … once
the offset is kept — so "no pixel here" cannot be signalled in the index,
and every pixel the renderer skips would otherwise paint as `palette[0]`
over the ULA. The renderer now also carries `pixel_en_s`, which is clear
outside the NR$1B clip window and off the tilemap's rows.

### +3 / DISCiPLE DSK tracks were stored by the Track-Info C/H labels

**Fixed.** The DSK track-size table IS the physical order, but tracks were
filed under their Track-Info C/H labels whenever those were in range. The
labels are IDs written into the format for the FDC to match, not a
position, and copy protection and bad dumps mislabel them freely — a disk
tagged `C=0` throughout piled every track into one slot and left the rest
of the image empty. The labels are still carried on the track, because the
FDC matches an ID field's C against the command's cylinder.

### SAM FORMAT reported success and wrote nothing

**Fixed.** The WD1772's `$E` and `$F` commands fell into a "complete
benignly" branch: INTRQ raised, no error reported, not one byte moved.
SAMDOS said the disk was formatted and every old sector was still there.

WRITE TRACK now collects the host's track image through DRQ and parses the
IDAM/DAM framing into sectors, and READ TRACK synthesises a track image
with the same framing so the two round-trip. A new command or a FORCE
INTERRUPT mid-format commits what already streamed past the head, as on
the real part. The image is a flat geometry store, so a format lays fresh
data over the track rather than moving or resizing sectors, and an
out-of-geometry ID is skipped.

A commit driven by a new command does not raise INTRQ, because the
interrupt belongs to the command that is starting: signalling there let a
host polling the line between the command write and the first DRQ read
"finished" and abandon the transfer. A FORCE INTERRUPT still signals,
which is what it is for.

### A SAM reboot left the SAA1099 running

**Fixed.** `Machine.Reset` cleared the CPU, paging, border, line interrupt
and beeper but not the sound chip, so music from the previous program kept
playing over the copyright screen.

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
