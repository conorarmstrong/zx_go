# ZX Spectrum Emulator - Keyboard Guide

## Mac Keyboard Mappings

This guide explains how Mac keyboard keys map to ZX Spectrum keys in the emulator.

### Basic Key Mappings

| Mac Key | ZX Spectrum Key | Notes |
|---------|-----------------|-------|
| A-Z | A-Z | Direct mapping |
| 0-9 | 0-9 | Direct mapping |
| Space | SPACE | Direct mapping |
| Return | ENTER | Direct mapping |
| Backspace | DELETE (0 + CAPS SHIFT) | Spectrum delete |

### Special ZX Spectrum Keys

| Mac Key | ZX Spectrum Function | Description |
|---------|---------------------|-------------|
| Left Shift | CAPS SHIFT | Main modifier key |
| Right Shift | SYMBOL SHIFT | Symbol modifier key |
| Left Cmd (⌘) | CAPS SHIFT | Alternative to Left Shift |
| Right Cmd (⌘) | SYMBOL SHIFT | Alternative to Right Shift |
| Left Ctrl | CAPS SHIFT | Alternative to Left Shift |
| Right Ctrl | SYMBOL SHIFT | Alternative to Right Shift |
| Left Alt (⌥) | SYMBOL SHIFT | Alternative to Right Shift |
| Right Alt (⌥) | SYMBOL SHIFT | Alternative to Right Shift |

### Arrow Keys
Arrow keys simulate the ZX Spectrum cursor movement using number keys with CAPS SHIFT:

| Mac Key | ZX Spectrum Equivalent | 
|---------|----------------------|
| ← Left | 5 + CAPS SHIFT |
| ↓ Down | 6 + CAPS SHIFT |
| ↑ Up | 7 + CAPS SHIFT |
| → Right | 8 + CAPS SHIFT |

### Special Function Keys

| Mac Key | Function | Description |
|---------|----------|-------------|
| F11 | BREAK | Equivalent to CAPS SHIFT + SPACE |
| F12 | NMI (Red Button) | Triggers Multiface Non-Maskable Interrupt |
| Tab | BREAK | Alternative BREAK key |
| Escape | BREAK | Alternative BREAK key |

## ZX Spectrum Keyboard Layout Reference

The ZX Spectrum has an 8×5 matrix keyboard:

```
Row 0: CAPS SHIFT  Z  X  C  V
Row 1:      A      S  D  F  G  
Row 2:      Q      W  E  R  T
Row 3:      1      2  3  4  5
Row 4:      0      9  8  7  6
Row 5:      P      O  I  U  Y
Row 6:   ENTER     L  K  J  H
Row 7:   SPACE  SYMBOL SHIFT  M  N  B
```

## Using CAPS SHIFT and SYMBOL SHIFT

### CAPS SHIFT combinations produce:
- CAPS SHIFT + 1 = !
- CAPS SHIFT + 2 = @
- CAPS SHIFT + 3 = #
- CAPS SHIFT + 4 = $
- CAPS SHIFT + 5 = %
- CAPS SHIFT + 6 = &
- CAPS SHIFT + 7 = '
- CAPS SHIFT + 8 = (
- CAPS SHIFT + 9 = )
- CAPS SHIFT + 0 = _

### SYMBOL SHIFT combinations produce:
- SYMBOL SHIFT + O = ;
- SYMBOL SHIFT + P = "
- SYMBOL SHIFT + L = =
- SYMBOL SHIFT + K = +
- SYMBOL SHIFT + J = -
- SYMBOL SHIFT + H = ↑ (up arrow)
- SYMBOL SHIFT + M = .
- SYMBOL SHIFT + N = ,
- SYMBOL SHIFT + B = *

## Multiface Controls

### Triggering the Multiface (Red Button)
- **F12**: Simulate pressing the Multiface red button (NMI)
- **Menu > Emulator > Trigger NMI**: Manual NMI trigger
- This only works when a Multiface is enabled via the Peripherals menu

### What happens when you press the red button:
1. The Multiface ROM is paged in at address 0x0000
2. A Non-Maskable Interrupt (NMI) is triggered
3. The Z80 CPU jumps to address 0x0066 (NMI vector)
4. The Multiface software takes control
5. You can now use Multiface features like memory snapshots

## BREAK Key Usage

The BREAK key is used to:
- Stop running programs
- Return to BASIC from machine code
- Interrupt loading/saving operations

Press **F11**, **Tab**, or **Escape** to trigger BREAK.

## Troubleshooting

### Keys not working?
1. Check if you're in the right window focus
2. Some key combinations might be intercepted by macOS
3. Try alternative key mappings (e.g., use Cmd instead of Shift)

### Multiface not responding to F12?
1. Make sure a Multiface is enabled: **Peripherals > Enable Multiface [variant]**
2. Check the peripheral status: **Emulator > Peripheral Status**
3. Try the manual trigger: **Emulator > Trigger NMI**

### Can't access ZX Spectrum symbols?
Use the Right Shift key (mapped to SYMBOL SHIFT) with the appropriate letters:
- Right Shift + P = " (quotes)
- Right Shift + M = . (period)
- Right Shift + N = , (comma)

## Advanced Usage

### Creating ZX Spectrum Programs
1. Switch to an appropriate model (48K/128K) via **Machine** menu
2. Use BREAK (F11) to stop any running program
3. Type your BASIC program using the mapped keys
4. Use Multiface (F12) to create snapshots of your work

### Using Different Spectrum Models
Each model has different capabilities:
- **48K**: Basic model with 48KB RAM
- **128K**: Enhanced BASIC with music capabilities  
- **+2/+2A/+3**: Advanced models with additional features

Switch models via the **Machine** menu - the emulator will automatically reboot with the new ROM.

### Peripheral Integration
- Enable Disciple for disk operations
- Enable appropriate Multiface for your current Spectrum model
- Check status via **Emulator > Peripheral Status** to see what's active

This keyboard mapping provides authentic ZX Spectrum experience while accommodating modern Mac keyboards.