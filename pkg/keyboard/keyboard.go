package keyboard

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// Keyboard handles keyboard input by mapping modern keys to the Spectrum's 8x5 matrix.
type Keyboard struct {
	// Matrix state: 8 rows, 5 bits per row. A bit is 0 when a key is pressed.
	matrix   [8]byte
	keyMap   map[fyne.KeyName][]keyMapping
	matrixMu sync.RWMutex

	// Special key states
	breakPressed bool
	nmiCallback  func() // Callback for NMI (Multiface red button simulation)

	// Typed-character symbol pulse. TypeRune injects the SYMBOL-SHIFT
	// combination for a typed symbol (e.g. '.', ';', ':') as a brief overlay
	// on the physical matrix, so symbols type correctly regardless of the host
	// keyboard layout (a French AZERTY '.' is Shift+';'-key, which the physical
	// key path can't express). While pulseFrames > 0, Scan applies pulseMatrix
	// and releases CAPS SHIFT, so a host Shift held to reach the symbol (as on
	// AZERTY) doesn't leak into the injected combo. pulseFrames counts down
	// once per 50 Hz frame in Tick().
	pulseMatrix [8]byte
	pulseFrames int
}

// runePulseFrames is how many 50 Hz frames an injected symbol is held. Two
// frames guarantees the ROM's keyboard scan sees it at least once while keeping
// the overlay window small enough not to disturb concurrently-held keys.
const runePulseFrames = 2

type keyMapping struct {
	row, mask byte
}

// New creates a new Keyboard instance.
func New() *Keyboard {
	k := &Keyboard{}
	for i := range k.matrix {
		k.matrix[i] = 0xFF // All keys up
	}
	for i := range k.pulseMatrix {
		k.pulseMatrix[i] = 0xFF
	}
	k.initKeyMap()
	return k
}

// HandleKeyEvent processes a Fyne key event with modifier information.
func (k *Keyboard) HandleKeyEvent(ev *fyne.KeyEvent, isPressed bool) {
	k.HandleKeyWithModifiers(ev.Name, isPressed, false, false, false, false)
}

// HandleKeyWithModifiers processes a key with explicit modifier information.
func (k *Keyboard) HandleKeyWithModifiers(keyName fyne.KeyName, isPressed, shift, ctrl, alt, cmd bool) {
	// F12 = Multiface red button NMI; F8 = ZX Spectrum Next NMI
	// for the NextZXOS Browser. Both trigger the same Z80 NMI line —
	// the running ROM (Multiface or NextZXOS) decides what to do
	// with it. F8 is a no-op when neither a Multiface nor Next is
	// active.
	if keyName == fyne.KeyF12 || keyName == fyne.KeyF8 {
		if isPressed && k.nmiCallback != nil {
			if keyName == fyne.KeyF8 {
				log.Println("NMI triggered (F8 — Next NMI Browser)")
			} else {
				log.Println("NMI triggered (Multiface red button)")
			}
			k.nmiCallback()
		}
		return
	}

	k.matrixMu.Lock()
	defer k.matrixMu.Unlock()

	// Handle special keys first
	switch keyName {
	case fyne.KeyF11: // F11 = BREAK (CAPS SHIFT + SPACE)
		k.breakPressed = isPressed
		if isPressed {
			k.matrix[0] &= ^byte(0x01) // CAPS SHIFT
			k.matrix[7] &= ^byte(0x01) // SPACE
		} else {
			k.matrix[0] |= byte(0x01) // CAPS SHIFT
			k.matrix[7] |= byte(0x01) // SPACE
		}
		log.Printf("BREAK key %s", map[bool]string{true: "pressed", false: "released"}[isPressed])
		return
	}

	// Handle modifier keys - activate CAPS SHIFT or SYMBOL SHIFT
	if shift {
		// Shift pressed = CAPS SHIFT active
		if isPressed {
			k.matrix[0] &= ^byte(0x01) // Activate CAPS SHIFT
		} else {
			k.matrix[0] |= byte(0x01) // Deactivate CAPS SHIFT
		}
	}

	if ctrl || alt || cmd {
		// Control/Alt/Cmd pressed = SYMBOL SHIFT active
		if isPressed {
			k.matrix[7] &= ^byte(0x02) // Activate SYMBOL SHIFT
		} else {
			k.matrix[7] |= byte(0x02) // Deactivate SYMBOL SHIFT
		}
	}

	// Handle the base key
	if mappings, ok := k.keyMap[keyName]; ok {
		for _, m := range mappings {
			if isPressed {
				k.matrix[m.row] &= ^m.mask
			} else {
				k.matrix[m.row] |= m.mask
			}
		}
	} else {
		// Log unmapped keys for debugging
		log.Printf("Unmapped key: %s", keyName)
	}
}

// PressMatrixKey directly sets or clears the given bit of the
// Spectrum keyboard matrix, bypassing the host-key-to-Spectrum-key
// mapping entirely. Used by the test harness to simulate specific
// Spectrum key combinations (CAPS SHIFT alone, SYMBOL SHIFT alone,
// extended-mode sequences) that are awkward to express through
// the host modifier-based API.
//
// row is 0..7, mask selects the bit (0x01..0x10 on most rows),
// pressed=true drives the bit low (the Spectrum matrix is
// active-low — a pressed key is a 0).
func (k *Keyboard) PressMatrixKey(row int, mask byte, pressed bool) {
	if row < 0 || row >= 8 {
		return
	}
	k.matrixMu.Lock()
	defer k.matrixMu.Unlock()
	if pressed {
		k.matrix[row] &= ^mask
	} else {
		k.matrix[row] |= mask
	}
}

// ReleaseAll lifts every key in the matrix (all bits set = released). Used when
// the host window loses focus or the machine reboots: the OS stops delivering
// key-up events for anything held, so without this a held key would stick on.
func (k *Keyboard) ReleaseAll() {
	k.matrixMu.Lock()
	for i := range k.matrix {
		k.matrix[i] = 0xFF
	}
	k.matrixMu.Unlock()
}

// Scan reads the keyboard matrix for a given port address.
// The high byte of the address determines which rows are being scanned.
func (k *Keyboard) Scan(addr uint16) byte {
	k.matrixMu.RLock()
	defer k.matrixMu.RUnlock()

	result := byte(0xFF)
	addrHi := byte(addr >> 8)
	pulse := k.pulseFrames > 0

	for row := 0; row < 8; row++ {
		if (addrHi & (1 << row)) == 0 {
			m := k.matrix[row]
			if pulse {
				if row == 0 {
					m |= 0x01 // release CAPS SHIFT so a host Shift doesn't corrupt the symbol
				}
				m &= k.pulseMatrix[row] // overlay the injected SYMBOL-SHIFT combo
			}
			result &= m
		}
	}
	return result
}

// initKeyMap sets up the mapping from fyne.KeyName to Spectrum keyboard matrix.
// This is based on the layout of a standard UK Spectrum.
func (k *Keyboard) initKeyMap() {
	k.keyMap = map[fyne.KeyName][]keyMapping{
		// Row 0: CAPS SHIFT, Z, X, C, V
		desktop.KeyShiftLeft: {{0, 0x01}}, // CAPS SHIFT
		fyne.KeyZ:            {{0, 0x02}},
		fyne.KeyX:            {{0, 0x04}},
		fyne.KeyC:            {{0, 0x08}},
		fyne.KeyV:            {{0, 0x10}},
		// Lowercase versions
		"z": {{0, 0x02}},
		"x": {{0, 0x04}},
		"c": {{0, 0x08}},
		"v": {{0, 0x10}},

		// Row 1: A, S, D, F, G
		fyne.KeyA: {{1, 0x01}},
		fyne.KeyS: {{1, 0x02}},
		fyne.KeyD: {{1, 0x04}},
		fyne.KeyF: {{1, 0x08}},
		fyne.KeyG: {{1, 0x10}},
		// Lowercase versions
		"a": {{1, 0x01}},
		"s": {{1, 0x02}},
		"d": {{1, 0x04}},
		"f": {{1, 0x08}},
		"g": {{1, 0x10}},

		// Row 2: Q, W, E, R, T
		fyne.KeyQ: {{2, 0x01}},
		fyne.KeyW: {{2, 0x02}},
		fyne.KeyE: {{2, 0x04}},
		fyne.KeyR: {{2, 0x08}},
		fyne.KeyT: {{2, 0x10}},
		// Lowercase versions
		"q": {{2, 0x01}},
		"w": {{2, 0x02}},
		"e": {{2, 0x04}},
		"r": {{2, 0x08}},
		"t": {{2, 0x10}},

		// Row 3: 1, 2, 3, 4, 5
		fyne.Key1: {{3, 0x01}},
		fyne.Key2: {{3, 0x02}},
		fyne.Key3: {{3, 0x04}},
		fyne.Key4: {{3, 0x08}},
		fyne.Key5: {{3, 0x10}},

		// Row 4: 0, 9, 8, 7, 6
		fyne.Key0:         {{4, 0x01}},
		fyne.Key9:         {{4, 0x02}},
		fyne.Key8:         {{4, 0x04}},
		fyne.Key7:         {{4, 0x08}},
		fyne.Key6:         {{4, 0x10}},
		fyne.KeyBackspace: {{4, 0x01}, {0, 0x01}}, // 0 + CAPS SHIFT

		// Row 5: P, O, I, U, Y
		fyne.KeyP: {{5, 0x01}},
		fyne.KeyO: {{5, 0x02}},
		fyne.KeyI: {{5, 0x04}},
		fyne.KeyU: {{5, 0x08}},
		fyne.KeyY: {{5, 0x10}},
		// Lowercase versions
		"p": {{5, 0x01}},
		"o": {{5, 0x02}},
		"i": {{5, 0x04}},
		"u": {{5, 0x08}},
		"y": {{5, 0x10}},

		// Row 6: ENTER, L, K, J, H
		fyne.KeyReturn: {{6, 0x01}},
		fyne.KeyL:      {{6, 0x02}},
		fyne.KeyK:      {{6, 0x04}},
		fyne.KeyJ:      {{6, 0x08}},
		fyne.KeyH:      {{6, 0x10}},
		// Lowercase versions
		"l": {{6, 0x02}},
		"k": {{6, 0x04}},
		"j": {{6, 0x08}},
		"h": {{6, 0x10}},

		// Row 7: SPACE, SYMBOL SHIFT, M, N, B
		fyne.KeySpace:         {{7, 0x01}},
		desktop.KeyShiftRight: {{7, 0x02}}, // SYMBOL SHIFT
		fyne.KeyM:             {{7, 0x04}},
		fyne.KeyN:             {{7, 0x08}},
		fyne.KeyB:             {{7, 0x10}},
		// Lowercase versions
		"m": {{7, 0x04}},
		"n": {{7, 0x08}},
		"b": {{7, 0x10}},

		// Arrow keys as 5,6,7,8 with CAPS SHIFT
		fyne.KeyLeft:  {{3, 0x10}, {0, 0x01}}, // 5 + CAPS SHIFT
		fyne.KeyDown:  {{4, 0x10}, {0, 0x01}}, // 6 + CAPS SHIFT
		fyne.KeyUp:    {{4, 0x08}, {0, 0x01}}, // 7 + CAPS SHIFT
		fyne.KeyRight: {{4, 0x04}, {0, 0x01}}, // 8 + CAPS SHIFT

		// All Ctrl / Alt / Cmd-Super variants map to SYMBOL SHIFT.
		// Fyne's desktop driver emits the typed constants below as
		// their underlying string values (see fyne.io/fyne/v2/driver/
		// desktop/key.go): "LeftControl", "RightControl", "LeftAlt",
		// "RightAlt", "LeftSuper", "RightSuper". macOS's Cmd key
		// arrives as LeftSuper / RightSuper, so it must be mapped
		// here too for Cmd to act as SYMBOL SHIFT on Mac.
		desktop.KeyControlLeft:  {{7, 0x02}},
		desktop.KeyControlRight: {{7, 0x02}},
		desktop.KeyAltLeft:      {{7, 0x02}},
		desktop.KeyAltRight:     {{7, 0x02}},
		desktop.KeySuperLeft:    {{7, 0x02}}, // Cmd on macOS, Win key elsewhere
		desktop.KeySuperRight:   {{7, 0x02}},

		// Punctuation/symbols are NOT mapped from physical key names here.
		// A key's physical name is layout-dependent and shift-independent
		// (a French AZERTY '.' is Shift+';'-key — same name as ';'), so a
		// physical mapping can't tell ';' from '.' and a host Shift would add
		// CAPS SHIFT and corrupt the symbol. Instead symbols are driven from
		// the typed character via TypeRune/symbolRuneTable, which is correct
		// for every keyboard layout.

		// Control keys.
		fyne.KeyTab:    {{0, 0x01}, {7, 0x01}}, // CAPS SHIFT + SPACE (BREAK)
		fyne.KeyEscape: {{0, 0x01}, {7, 0x01}}, // CAPS SHIFT + SPACE (BREAK)
	}
}

// symbolRuneTable maps a typed character to the second key of its Spectrum
// SYMBOL-SHIFT combination (TypeRune adds SYMBOL SHIFT itself). It is the
// layout-independent source of truth for symbols; letters and digits are NOT
// here — they come from the physical key path so games can hold them.
// Combos use the same row/bit layout as the matrix (Z=row0 bit1, O=row5 bit1,
// M=row7 bit2, etc.). Verified against the ZX Spectrum symbol-shift legend.
var symbolRuneTable = map[rune][]keyMapping{
	'!':  {{3, 0x01}}, // SYM+1
	'@':  {{3, 0x02}}, // SYM+2
	'#':  {{3, 0x04}}, // SYM+3
	'$':  {{3, 0x08}}, // SYM+4
	'%':  {{3, 0x10}}, // SYM+5
	'&':  {{4, 0x10}}, // SYM+6
	'\'': {{4, 0x08}}, // SYM+7
	'(':  {{4, 0x04}}, // SYM+8
	')':  {{4, 0x02}}, // SYM+9
	'_':  {{4, 0x01}}, // SYM+0
	'<':  {{2, 0x08}}, // SYM+R
	'>':  {{2, 0x10}}, // SYM+T
	';':  {{5, 0x02}}, // SYM+O
	'"':  {{5, 0x01}}, // SYM+P
	'=':  {{6, 0x02}}, // SYM+L
	'+':  {{6, 0x04}}, // SYM+K
	'-':  {{6, 0x08}}, // SYM+J
	'^':  {{6, 0x10}}, // SYM+H
	':':  {{0, 0x02}}, // SYM+Z
	'£':  {{0, 0x04}}, // SYM+X
	'?':  {{0, 0x08}}, // SYM+C
	'/':  {{0, 0x10}}, // SYM+V
	'*':  {{7, 0x10}}, // SYM+B
	',':  {{7, 0x08}}, // SYM+N
	'.':  {{7, 0x04}}, // SYM+M
}

// TypeRune injects the Spectrum SYMBOL-SHIFT combination for the typed
// character r (e.g. '.', ';', ':') as a brief overlay pulse, independent of the
// host keyboard layout. Returns true if r is a known Spectrum symbol (letters
// and digits return false — they are handled by the physical key path).
func (k *Keyboard) TypeRune(r rune) bool {
	combo, ok := symbolRuneTable[r]
	if !ok {
		return false
	}
	k.matrixMu.Lock()
	defer k.matrixMu.Unlock()
	for i := range k.pulseMatrix {
		k.pulseMatrix[i] = 0xFF
	}
	k.pulseMatrix[7] &= ^byte(0x02) // SYMBOL SHIFT
	for _, m := range combo {
		k.pulseMatrix[m.row] &= ^m.mask
	}
	k.pulseFrames = runePulseFrames
	return true
}

// Tick advances the typed-character symbol pulse by one 50 Hz frame, releasing
// it when it expires. Safe to call every frame for every machine (no-op when no
// pulse is active).
func (k *Keyboard) Tick() {
	k.matrixMu.Lock()
	defer k.matrixMu.Unlock()
	if k.pulseFrames > 0 {
		k.pulseFrames--
		if k.pulseFrames == 0 {
			for i := range k.pulseMatrix {
				k.pulseMatrix[i] = 0xFF
			}
		}
	}
}

// UseZX8xLayout switches the keyboard to the Sinclair ZX80/ZX81 matrix. Letter,
// digit, SPACE and SHIFT positions match the Spectrum, but the ZX8x has no
// SYMBOL SHIFT: ENTER is NEWLINE (row 6, bit 0) and "." is row 7, bit 1.
func (k *Keyboard) UseZX8xLayout() {
	k.matrixMu.Lock()
	defer k.matrixMu.Unlock()
	k.keyMap = zx8xKeyMap()
}

// zx8xKeyMap builds the ZX80/ZX81 host-key-to-matrix table.
func zx8xKeyMap() map[fyne.KeyName][]keyMapping {
	m := map[fyne.KeyName][]keyMapping{
		// Row 0: SHIFT, Z, X, C, V
		desktop.KeyShiftLeft:  {{0, 0x01}},
		desktop.KeyShiftRight: {{0, 0x01}},
		fyne.KeyZ:             {{0, 0x02}}, "z": {{0, 0x02}},
		fyne.KeyX: {{0, 0x04}}, "x": {{0, 0x04}},
		fyne.KeyC: {{0, 0x08}}, "c": {{0, 0x08}},
		fyne.KeyV: {{0, 0x10}}, "v": {{0, 0x10}},
		// Row 1: A, S, D, F, G
		fyne.KeyA: {{1, 0x01}}, "a": {{1, 0x01}},
		fyne.KeyS: {{1, 0x02}}, "s": {{1, 0x02}},
		fyne.KeyD: {{1, 0x04}}, "d": {{1, 0x04}},
		fyne.KeyF: {{1, 0x08}}, "f": {{1, 0x08}},
		fyne.KeyG: {{1, 0x10}}, "g": {{1, 0x10}},
		// Row 2: Q, W, E, R, T
		fyne.KeyQ: {{2, 0x01}}, "q": {{2, 0x01}},
		fyne.KeyW: {{2, 0x02}}, "w": {{2, 0x02}},
		fyne.KeyE: {{2, 0x04}}, "e": {{2, 0x04}},
		fyne.KeyR: {{2, 0x08}}, "r": {{2, 0x08}},
		fyne.KeyT: {{2, 0x10}}, "t": {{2, 0x10}},
		// Row 3: 1, 2, 3, 4, 5
		fyne.Key1: {{3, 0x01}}, fyne.Key2: {{3, 0x02}}, fyne.Key3: {{3, 0x04}},
		fyne.Key4: {{3, 0x08}}, fyne.Key5: {{3, 0x10}},
		// Row 4: 0, 9, 8, 7, 6
		fyne.Key0: {{4, 0x01}}, fyne.Key9: {{4, 0x02}}, fyne.Key8: {{4, 0x04}},
		fyne.Key7: {{4, 0x08}}, fyne.Key6: {{4, 0x10}},
		// Row 5: P, O, I, U, Y
		fyne.KeyP: {{5, 0x01}}, "p": {{5, 0x01}},
		fyne.KeyO: {{5, 0x02}}, "o": {{5, 0x02}},
		fyne.KeyI: {{5, 0x04}}, "i": {{5, 0x04}},
		fyne.KeyU: {{5, 0x08}}, "u": {{5, 0x08}},
		fyne.KeyY: {{5, 0x10}}, "y": {{5, 0x10}},
		// Row 6: NEWLINE, L, K, J, H
		fyne.KeyReturn: {{6, 0x01}},
		fyne.KeyL:      {{6, 0x02}}, "l": {{6, 0x02}},
		fyne.KeyK: {{6, 0x04}}, "k": {{6, 0x04}},
		fyne.KeyJ: {{6, 0x08}}, "j": {{6, 0x08}},
		fyne.KeyH: {{6, 0x10}}, "h": {{6, 0x10}},
		// Row 7: SPACE, ., M, N, B
		fyne.KeySpace:  {{7, 0x01}},
		fyne.KeyPeriod: {{7, 0x02}},
		fyne.KeyM:      {{7, 0x04}}, "m": {{7, 0x04}},
		fyne.KeyN: {{7, 0x08}}, "n": {{7, 0x08}},
		fyne.KeyB: {{7, 0x10}}, "b": {{7, 0x10}},
		// Cursor keys: SHIFT + 5/6/7/8 (same arrow scheme as the Spectrum)
		fyne.KeyLeft:  {{3, 0x10}, {0, 0x01}},
		fyne.KeyDown:  {{4, 0x10}, {0, 0x01}},
		fyne.KeyUp:    {{4, 0x08}, {0, 0x01}},
		fyne.KeyRight: {{4, 0x04}, {0, 0x01}},

		// RUBOUT (delete) and EDIT: SHIFT + 0 / SHIFT + 1.
		fyne.KeyBackspace: {{4, 0x01}, {0, 0x01}}, // SHIFT + 0
		fyne.KeyDelete:    {{4, 0x01}, {0, 0x01}}, // SHIFT + 0

		// Symbols/operators. The ZX80/ZX81 have no dedicated symbol keys — each
		// is SHIFT + a letter — so the host symbol keys map to those combos.
		// Physical-key (fyne) constants for the unshifted symbols:
		fyne.KeyEqual:        {{6, 0x02}, {0, 0x01}}, // = : SHIFT + L
		fyne.KeyMinus:        {{6, 0x08}, {0, 0x01}}, // - : SHIFT + J
		fyne.KeyComma:        {{7, 0x08}, {0, 0x01}}, // , : SHIFT + N
		fyne.KeySemicolon:    {{0, 0x04}, {0, 0x01}}, // ; : SHIFT + X
		fyne.KeySlash:        {{0, 0x10}, {0, 0x01}}, // / : SHIFT + V
		fyne.KeyLeftBracket:  {{5, 0x04}, {0, 0x01}}, // ( : SHIFT + I
		fyne.KeyRightBracket: {{5, 0x02}, {0, 0x01}}, // ) : SHIFT + O
		fyne.KeyApostrophe:   {{5, 0x01}, {0, 0x01}}, // " : SHIFT + P
		// Typed-rune variants for shifted characters that have no distinct fyne
		// key constant (the constants above already cover = - , ; /).
		"+":  {{6, 0x04}, {0, 0x01}}, // SHIFT + K
		"*":  {{7, 0x10}, {0, 0x01}}, // SHIFT + B
		":":  {{0, 0x02}, {0, 0x01}}, // SHIFT + Z
		"?":  {{0, 0x08}, {0, 0x01}}, // SHIFT + C
		"(":  {{5, 0x04}, {0, 0x01}}, // SHIFT + I
		")":  {{5, 0x02}, {0, 0x01}}, // SHIFT + O
		"$":  {{5, 0x08}, {0, 0x01}}, // SHIFT + U
		"\"": {{5, 0x01}, {0, 0x01}}, // SHIFT + P
		"<":  {{2, 0x02}, {0, 0x01}}, // SHIFT + W
		">":  {{2, 0x04}, {0, 0x01}}, // SHIFT + E
	}
	return m
}

// SetNMICallback sets the callback function for NMI (Non-Maskable Interrupt)
// This is used for Multiface red button simulation
func (k *Keyboard) SetNMICallback(callback func()) {
	k.nmiCallback = callback
}

// IsBreakPressed returns whether the BREAK key is currently pressed
func (k *Keyboard) IsBreakPressed() bool {
	k.matrixMu.RLock()
	defer k.matrixMu.RUnlock()
	return k.breakPressed
}

// GetKeyStatus returns a human-readable status of special keys
func (k *Keyboard) GetKeyStatus() string {
	k.matrixMu.RLock()
	defer k.matrixMu.RUnlock()

	status := ""
	if k.breakPressed {
		status += "BREAK "
	}

	// Check if CAPS SHIFT is pressed (any key in row 0, bit 0)
	if (k.matrix[0] & 0x01) == 0 {
		status += "CAPS_SHIFT "
	}

	// Check if Symbol Shift is pressed (row 7, bit 1)
	if (k.matrix[7] & 0x02) == 0 {
		status += "SYMBOL_SHIFT "
	}

	if status == "" {
		status = "None"
	}

	return status
}

// SimulateNMI manually triggers an NMI (for testing or programmatic use)
func (k *Keyboard) SimulateNMI() {
	if k.nmiCallback != nil {
		log.Println("NMI triggered programmatically")
		k.nmiCallback()
	}
}

// MappingEntry is the JSON-friendly representation of a single matrix
// position. row is 0..7 and mask is one of {0x01, 0x02, 0x04, 0x08, 0x10}.
type MappingEntry struct {
	Row  byte `json:"row"`
	Mask byte `json:"mask"`
}

// SetMapping replaces the mapping for a single physical key. Multiple
// MappingEntry values represent compound keystrokes (e.g. CAPS SHIFT + 5
// for cursor left). Pass an empty slice to clear a mapping entirely.
func (k *Keyboard) SetMapping(key fyne.KeyName, entries []MappingEntry) {
	k.matrixMu.Lock()
	defer k.matrixMu.Unlock()
	if k.keyMap == nil {
		k.keyMap = map[fyne.KeyName][]keyMapping{}
	}
	mappings := make([]keyMapping, len(entries))
	for i, e := range entries {
		mappings[i] = keyMapping{row: e.Row, mask: e.Mask}
	}
	k.keyMap[key] = mappings
}

// GetMapping returns the current mapping for a physical key, or an empty
// slice if the key is not mapped.
func (k *Keyboard) GetMapping(key fyne.KeyName) []MappingEntry {
	k.matrixMu.RLock()
	defer k.matrixMu.RUnlock()
	src := k.keyMap[key]
	out := make([]MappingEntry, len(src))
	for i, m := range src {
		out[i] = MappingEntry{Row: m.row, Mask: m.mask}
	}
	return out
}

// LoadOverrides applies a JSON map of physical key name -> []MappingEntry
// from the given file path. Missing files are not an error (overrides are
// optional). Each entry replaces the matching default mapping.
func (k *Keyboard) LoadOverrides(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read keymap %s: %w", path, err)
	}
	var raw map[string][]MappingEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse keymap %s: %w", path, err)
	}
	for name, entries := range raw {
		k.SetMapping(fyne.KeyName(name), entries)
	}
	return nil
}

// SaveOverrides writes the given override map (physical key name ->
// MappingEntry list) to path as JSON. The caller chooses which entries to
// persist; this is intentionally separate from LoadOverrides so the UI can
// expose just the user's customisations rather than dumping the full
// default table.
func SaveOverrides(path string, overrides map[string][]MappingEntry) error {
	data, err := json.MarshalIndent(overrides, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal keymap: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
