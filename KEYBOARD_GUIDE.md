# zx_go keyboard guide

zx_go runs on macOS, Linux, and Windows. The same key mappings work
everywhere — the only platform-specific detail is which physical key
your keyboard puts the modifier on. `Cmd` on macOS, `Ctrl` and `Alt`
elsewhere all behave the same way in the emulator.

If you prefer to point and click, there's also a graphical Spectrum
keyboard at the bottom of the emulator window — clicking a key sends
the same matrix event as pressing it on your physical keyboard.

## Basic key mappings

| Host key | ZX Spectrum key | Notes |
|---|---|---|
| `A`–`Z` | `A`–`Z` | Direct mapping |
| `0`–`9` | `0`–`9` | Direct mapping |
| `Space` | `SPACE` | |
| `Return` / `Enter` | `ENTER` | |
| `Backspace` | `DELETE` (`0` + `CAPS SHIFT`) | Spectrum delete |
| `Tab` | `BREAK` | `CAPS SHIFT` + `SPACE` |
| `Escape` | `BREAK` | `CAPS SHIFT` + `SPACE` |
| `F11` | `BREAK` | Alternative break |
| `F12` | `NMI` | Multiface red button (requires a Multiface enabled) |
| `F8` | `NMI` | ZX Spectrum Next NMI Browser — same NMI line as `F12`; on ModelNext brings up the NextZXOS Browser overlay if the OS is loaded |

## Modifier keys

| Host key | ZX Spectrum function |
|---|---|
| `Left Shift` | `CAPS SHIFT` |
| `Right Shift` | `SYMBOL SHIFT` |
| `Left Cmd` / `Right Cmd` (macOS) | `SYMBOL SHIFT` |
| `Left Ctrl` / `Right Ctrl` | `SYMBOL SHIFT` |
| `Left Alt` / `Right Alt` (Option on macOS) | `SYMBOL SHIFT` |

Use `Left Shift` for `CAPS SHIFT` and anything else (`Right Shift`,
`Ctrl`, `Alt`, `Cmd`) for `SYMBOL SHIFT`.

## Arrow keys

The arrow keys send the standard Spectrum cursor-key combinations
(`CAPS SHIFT` + 5/6/7/8). When a joystick interface is selected via
`Peripherals → Joystick`, the arrow keys are **redirected** to the
joystick instead of the keyboard matrix — see "Joystick redirection"
below.

| Host arrow key | Spectrum equivalent |
|---|---|
| `←` Left | `5` + `CAPS SHIFT` |
| `↓` Down | `6` + `CAPS SHIFT` |
| `↑` Up | `7` + `CAPS SHIFT` |
| `→` Right | `8` + `CAPS SHIFT` |

## Joystick redirection

If a joystick interface is active (`Peripherals → Joystick → …`),
the arrow keys and `Right Ctrl` / `Right Alt` (= FIRE) are routed
to the joystick instead of being interpreted as cursor keys. Pick
`None` to get the cursor-key behaviour back. The Kempston / Sinclair
(left & right) / Cursor (Protek) interfaces are all supported.

| Joystick key | Direction |
|---|---|
| `← / ↓ / ↑ / →` | Left / Down / Up / Right |
| `Right Ctrl` or `Right Alt` | Fire |

## ZX Spectrum keyboard layout reference

The Spectrum keyboard is an 8×5 matrix. Half-rows read outer→inner:

```
Row 0:  CAPS SHIFT  Z  X  C  V
Row 1:  A           S  D  F  G
Row 2:  Q           W  E  R  T
Row 3:  1           2  3  4  5
Row 4:  0           9  8  7  6
Row 5:  P           O  I  U  Y
Row 6:  ENTER       L  K  J  H
Row 7:  SPACE     SYMBOL SHIFT  M  N  B
```

## Symbols on the Spectrum

The Spectrum produces punctuation via `SYMBOL SHIFT` (right shift on
the host) plus a letter. The emulator also accepts the corresponding
punctuation keys on your physical keyboard and translates them to the
matrix combination automatically.

### SYMBOL SHIFT combinations

| Combination | Symbol |
|---|---|
| `SYMBOL SHIFT` + `O` | `;` |
| `SYMBOL SHIFT` + `P` | `"` |
| `SYMBOL SHIFT` + `L` | `=` |
| `SYMBOL SHIFT` + `K` | `+` |
| `SYMBOL SHIFT` + `J` | `-` |
| `SYMBOL SHIFT` + `H` | `↑` (caret) |
| `SYMBOL SHIFT` + `M` | `.` |
| `SYMBOL SHIFT` + `N` | `,` |
| `SYMBOL SHIFT` + `B` | `*` |
| `SYMBOL SHIFT` + `Y` | `]` |
| `SYMBOL SHIFT` + `U` | `[` |
| `SYMBOL SHIFT` + `0` | `` ` `` (backtick) |
| `SYMBOL SHIFT` + `ENTER` | `\` |

The emulator's automatic translations let you type `;` `"` `=` `+` `-`
`.` `,` `*` `]` `[` `` ` `` `\` directly using your host keyboard —
they're remapped to the matrix combinations above.

### CAPS SHIFT + digit combinations

These are the Spectrum's "shifted digit" symbols. Press `Left Shift`
plus the digit:

| Combination | Symbol |
|---|---|
| `CAPS SHIFT` + `1` | edit |
| `CAPS SHIFT` + `2` | caps lock |
| `CAPS SHIFT` + `3` | true video |
| `CAPS SHIFT` + `4` | inv video |
| `CAPS SHIFT` + `5–8` | cursor left/down/up/right |
| `CAPS SHIFT` + `9` | graph |
| `CAPS SHIFT` + `0` | delete |
| `CAPS SHIFT` + `SPACE` | BREAK |

## Multiface controls (red button)

| Key | Function |
|---|---|
| `F12` | NMI / red button (Multiface) |
| `F8` | NMI / Spectrum Next NMI Browser (same Z80 NMI line) |
| `Menu → Emulator → Trigger NMI (F12)` | Manual NMI trigger |

F12 and F8 both trigger the same Z80 NMI line — whichever ROM is
loaded (Multiface or NextZXOS) decides what to do with it. A
Multiface variant must be enabled via `Peripherals → Enable
Multiface 1 / 128 / 3` for F12 to do anything useful in classic
mode; F8 only does anything useful in ModelNext with NextZXOS
loaded (it brings up the Browser overlay). The NMI pages the
loaded ROM in at `$0000` and jumps the CPU to `$0066` (the
standard Spectrum NMI vector). From there you can poke memory,
save a snapshot, navigate the Browser, etc.

## DISCiPLE / +D disk system

`File → Load DISCiPLE Disk 1…` and `File → Load DISCiPLE Disk 2…`
each mount an `.mgt` or `.img` disk image on the corresponding
drive. Enable the DISCiPLE first via `Peripherals → Enable
DISCiPLE`; this reboots into GDOS. Type GDOS commands
(`CAT 1`, `LOAD d1"name"`, `CAT 2`, etc.) at the cursor.

## Spectrum Next

When the Next model is selected (`Machine → ZX Spectrum Next`), the
host keyboard maps through the same matrix as the classic Spectrum.
`F8` triggers the same NMI line as `F12`, which NextZXOS catches
and turns into its Browser overlay. Other Next-specific extended
editor keys are not wired yet — see `docs/spectrum-next.md`.

## Troubleshooting

- **Keys not working**: focus has to be on the emulator window, not
  the keyboard widget. Click the screen first.
- **Modifier intercepted by host OS**: some macOS shortcuts grab
  `Cmd`-combinations (`Cmd + Tab`, `Cmd + Q`). Use `Ctrl` or `Alt`
  as an alternative `SYMBOL SHIFT` source.
- **F12 doesn't trigger NMI**: confirm a Multiface is enabled via
  `Peripherals` and that `Emulator → Peripheral Status` lists it as
  active.
- **Joystick keys still doing arrows**: confirm `Peripherals →
  Joystick` is set to anything other than `None`.
