# zx_go User Manual

This is the everyday user guide to **zx_go** — how to start it, pick a machine,
load software, save your place, wire up peripherals, and get sound and the right
keys. It covers the graphical app; the headless/CLI debugging flags are
documented separately (run `zx_go --help`, and see
[DEBUGGER.md](../DEBUGGER.md)).

For deeper, per-topic detail this manual links out to:

- **[KEYBOARD_GUIDE.md](../KEYBOARD_GUIDE.md)** — the full Spectrum keyboard map
- **[docs/spectrum-next.md](spectrum-next.md)** — Spectrum Next setup (ROMs + SD card)
- **[docs/zx80-zx81.md](zx80-zx81.md)** — the ZX80 / ZX81 machines
- **[docs/compatibility.md](compatibility.md)** — what runs, and known gaps
- **[COMPARISON.md](../COMPARISON.md)** — how zx_go compares to other emulators

---

## 1. Starting up

Launch the app and you get a 48K Spectrum by default, sitting at the `©`
copyright line ready for BASIC. Everything else — a different machine, a game,
a peripheral — is reachable from the menu bar.

You can also pick a machine straight from the command line:

| Flag | Boots into |
|------|------------|
| *(none)* | ZX Spectrum 48K |
| `--next` | ZX Spectrum Next |
| `--pentagon` | Pentagon 128 |
| `--zx81` | Sinclair ZX81 |
| `--zx80` | Sinclair ZX80 |

To run without a window (for scripting/CI), add `--headless --frames N`.

---

## 2. Choosing a machine

**Machine** menu:

- **48K**, **128K**, **+2**, **+2A**, **+3** — the Sinclair line. 48K is the
  classic; 128K/+2 add the AY sound chip and the 128 BASIC editor; +2A/+3 add
  the later memory layout (and +3 the disk interface).
- **Pentagon 128** — the popular 128K clone. Same software base, but **no
  memory contention** and a longer 71680-T-state frame, which is why some
  demos that are timed for Pentagon only run correctly here.
- **Spectrum Next** — the modern FPGA machine: Layer 2, hardware sprites,
  tilemap, copper, extra sound. Needs its ROMs and an SD card image first — see
  [§8 Spectrum Next](#8-spectrum-next).
- **Sinclair ZX81 / ZX80** — the 1981/1980 originals. The picture is generated
  by the CPU itself, so the display looks and behaves exactly as the real
  hardware did. See [§9 ZX80 / ZX81](#9-zx80--zx81).

Switching machine reboots into the new one. Your loaded peripherals and config
are remembered where they still apply.

---

## 3. Loading software

Everything loads from the **File** menu, or by **dragging a file onto the
window** — zx_go picks the right loader from the extension.

| You have | Use | Notes |
|----------|-----|-------|
| `.tap` / `.tzx` tape | File → Load Tape | Then `LOAD ""` (48K) or pick *Tape Loader* (128). Auto-starts most tapes. |
| `.z80` / `.szx` / `.sna` snapshot | File → Load Snapshot | Restores a frozen machine instantly. |
| `.rom` | File → Load ROM | Replace the system ROM (advanced). |
| `.dck` / Interface 2 cartridge | File → Insert Interface 2 Cartridge | 16K ROM cartridges. |
| `.trd` TR-DOS disk | File → Load TR-DOS Disk A/B | Pentagon / 48K / 128K. Enter TR-DOS, then `CAT`/`LOAD`. See below. |
| `.dsk` / `.mgt` disk | File → Load Disk / DISCiPLE Disk | See [§6 Peripherals](#6-peripherals-disks-printers-mice). |
| `.p` / `.o` (ZX81/ZX80) | File → Open File | Loads and runs the program. |

**Recent files** (File → Recent) keeps your last-opened items one click away.

### Tapes in detail

zx_go does proper **tape emulation** (the signal is decoded by the ROM loader,
not faked), so loaders with custom timing work. You can:

- **Tape Browser** (File → Tape Browser) — see the blocks on a tape and jump to one.
- **Save Tape** — capture what the machine writes back out to a `.tap`/`.tzx`.
- **Stop Tape** — halt playback.

Some protected games need the **Speedlock Workaround** (File → disk submenu)
toggled on.

### TR-DOS disks (.TRD)

TR-DOS is the disk system used on the Pentagon and other 128K clones. Pick a
**Pentagon 128** (or 48K/128K) machine, then **File → Load TR-DOS Disk A**
(or B) and choose a `.trd` image — the Beta Disk interface and its TR-DOS ROM
are enabled automatically. To use the disk, enter TR-DOS from BASIC with
`RANDOMIZE USR 15616` (or via the 128 menu), then the usual `CAT`, `LOAD "name"`,
`RUN`, etc. You can also mount a disk at launch with `--trd path.trd`.

### Loading a tape at launch (including headless)

Pass `--tape path.tap` (or `.tzx`) to mount a tape into the deck at startup and
start it playing. This works in both GUI and **headless** mode — the one way to
feed a standard tape to a scripted/CI run without the window:

```bash
# Headless: boot 48K, mount a tape, run; then drive LOAD"" yourself
zx_go --headless --tape game.tap --press-key 'l@60,...' --frames 3000

# 128K clones auto-start most tapes via the boot menu's Tape Loader
zx_go --headless --pentagon --tape game.tzx --frames 5000
```

On the 48K the guest still has to read the tape — type `LOAD ""` (or schedule it
with `--press-key`); on the 128 choose *Tape Loader* from the boot menu. The
fast-load trap is installed automatically so the load is near-instant.

---

## 4. Saving your place

There are two independent mechanisms:

### Quick save / quick load (F2 / F4)

The fastest way to bookmark a moment. **F2** writes the whole machine to a
single quick-save slot; **F4** restores it. They're also in the **File** menu
("Quick Save State" / "Quick Load State").

The slot lives in your user config directory (`quicksave.szx`) and survives
restarts, so you can quit, come back, and press F4 to pick up where you were.

Quick save/load is available on the **48K…+3 and Pentagon** machines. It is
**not** offered for the ZX80/ZX81 (no compatible state format) or the Spectrum
Next (its full hardware state isn't captured by an `.szx` snapshot — use the
Next's own snapshot facilities instead).

### Named snapshots

File → **Save Snapshot…** writes a `.szx` / `.z80` / `.sna` file you name and
keep. Use these for permanent saves and for sharing a state with others. Load
them back via File → Load Snapshot.

---

## 5. The keyboard

The Spectrum keyboard is mapped onto your PC keyboard as faithfully as the
layouts allow. The essentials:

- **CAPS SHIFT** = either Shift key.
- **SYMBOL SHIFT** = either Ctrl key (the punctuation printed in red on the keys).
- **BREAK** = Shift + Space (or Esc).
- **Cursor / editing**: the arrow keys act as the Spectrum's cursor keys; Backspace = DELETE.

On **128K/+2/+3** you type keywords letter-by-letter (the later editor); on
**48K** the single-key keyword entry applies. On the **ZX80/ZX81** each keyword
sits on its native key — see [§9](#9-zx80--zx81).

You can remap any key: **Help → Custom Keymap…** opens an editor and saves your
layout. The complete default matrix, with every shifted symbol, is in
**[KEYBOARD_GUIDE.md](../KEYBOARD_GUIDE.md)**.

---

## 6. Peripherals (disks, printers, mice)

Peripherals are enabled from the **Peripherals** menu and then driven from
**File**. Available on the classic Spectrums (not the ZX80/ZX81):

- **Joysticks** — choose one: Kempston, Sinclair (left 1-5 / right 6-0), or
  Cursor/Protek. Controlled with the **arrow keys**, fire = **right Alt or
  right Ctrl**.
- **Kempston Mouse** — your mouse drives it once enabled.
- **Interface 1 + Microdrives** — enable Interface 1, then File → Microdrives
  to insert/save cartridges, eject, and toggle write-protect (per drive).
- **DISCiPLE / +D / disk drives** — enable, then File → Load Disk / DISCiPLE
  Disk, with Save Disk (DSK), Eject, and per-drive Write Protect.
- **Multiface 1 / 128 / 3** — enable one, then press **F12** to trigger its NMI
  (the freezer/poke menu).
- **ZX Printer** — enable it; printed output is captured and you can save it as
  a PNG (File → Save ZX Printer Output) or clear the buffer.

---

## 7. Sound

zx_go mixes all sound sources sample-accurately:

- **Beeper** (all machines) and the **AY-3-8912** (128K and up; the Next has
  three AYs).
- **SpecDrum** (8-bit DAC on port `$DF`) and **Covox** (port `$FB`) — enable
  each from the **Peripherals** menu. Both are event-timed, so percussion and
  digi-speech land on the right T-state.
- The **Spectrum Next's** four built-in DACs are always active in Next mode.

**Record (WAV)** under File captures the audio to a file. **Mute** everything
with `--no-sound` at launch (handy for long debugger sessions).

---

## 8. Spectrum Next

The Next needs two things before it will boot, both one-time:

1. **ROMs** — File → **Install Next ROMs…** (the licensed Next firmware is not
   bundled; the app fetches/installs it for you).
2. **An SD card** — either point it at a folder (File → Set Next SD Card
   Directory…) or at a card image (File → Set Next SD Card Image (.img/.mmc)…).

Then **Machine → Spectrum Next** (or launch with `--next`) boots NextZXOS the
real way — FPGA bootrom → firmware → NextZXOS — with Layer 2, sprites, tilemap,
the copper and the extra sound all live. Full setup and troubleshooting:
**[docs/spectrum-next.md](spectrum-next.md)**.

---

## 9. ZX80 / ZX81

These two run their original ROMs with **CPU-generated video** — the Z80
literally builds each scanline, exactly as it did in 1980/81. So expect the
authentic behaviour: the screen flickers/jumps during heavy computation on the
ZX81 in FAST mode, SLOW mode steadies it, and the cursor starts as an
inverse **K**.

Load programs with File → Open File (`.p` for ZX81, `.o` for ZX80). The keyword
keyboard differs per machine — for example **PRINT is on `O` on the ZX80** but
on `P` on the ZX81. Details and the per-machine keymap:
**[docs/zx80-zx81.md](zx80-zx81.md)**.

---

## 10. Display & view

**View** menu:

- **Zoom** — 100% / 125% / 150% / 200% / 300%, or **Full Screen** (Esc exits).
- **CRT scanline filter** — a subtle scanline/upscale effect for the retro look.

**Save Screenshot…** (File) writes the current frame to a PNG.

---

## 11. Tools & extras

- **Emulator → Reboot** — cold-restart the current machine.
- **Emulator → Pause/Resume**.
- **Emulator → Enter Poke…** — apply a POKE (cheats, patches).
- **Emulator → Debugger** — opens the built-in debugger (breakpoints, memory,
  registers, time-travel, and much more — see [DEBUGGER.md](../DEBUGGER.md)).
- **RZX** (File) — open and play back input recordings, or record your own;
  rollback to the last snapshot during playback.
- **Help → ROM Info / Peripheral Status** — what's loaded and what's enabled.
- **Help → About zx_go** — version and credits.

---

## 12. Keyboard shortcuts

| Key | Action |
|-----|--------|
| **F2** | Quick save state |
| **F4** | Quick load state |
| **F12** | Trigger NMI (Multiface freezer) |
| **Esc** | Exit full screen |
| Arrow keys | Joystick directions (when a joystick is enabled) |
| Right Alt / Right Ctrl | Joystick fire |

---

## 13. Troubleshooting

- **A game won't load from tape** — make sure the tape isn't stopped (File →
  Stop Tape toggles), and on 128K choose *Tape Loader* from the boot menu.
  Protected titles may need the Speedlock Workaround.
- **No sound** — check you didn't launch with `--no-sound`; for SpecDrum/Covox
  titles, enable the matching peripheral.
- **The Next won't boot / black screen** — you almost certainly need to install
  the Next ROMs and set an SD card first ([§8](#8-spectrum-next)).
- **Wrong keys / can't find a symbol** — see
  [KEYBOARD_GUIDE.md](../KEYBOARD_GUIDE.md), or remap via Help → Custom Keymap.
- **Quick save says it's unavailable** — that machine (ZX80/81 or Next) doesn't
  use the `.szx` quick-save slot; use a named snapshot or the machine's own
  facilities instead.

For anything deeper — timing, hardware conformance, the debugger, or how zx_go
stacks up against other emulators — follow the links at the top of this manual.
