package keyboard

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"log"
	"sync"
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
}

type keyMapping struct {
	row, mask byte
}

// New creates a new Keyboard instance.
func New() *Keyboard {
	k := &Keyboard{}
	for i := range k.matrix {
		k.matrix[i] = 0xFF // All keys up
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
	if keyName == fyne.KeyF12 { // F12 = NMI (Multiface red button)
		if isPressed && k.nmiCallback != nil {
			log.Println("NMI triggered (Multiface red button)")
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

// Scan reads the keyboard matrix for a given port address.
// The high byte of the address determines which rows are being scanned.
func (k *Keyboard) Scan(addr uint16) byte {
	k.matrixMu.RLock()
	defer k.matrixMu.RUnlock()

	result := byte(0xFF)
	addrHi := byte(addr >> 8)

	for row := 0; row < 8; row++ {
		if (addrHi & (1 << row)) == 0 {
			result &= k.matrix[row]
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

		// Mac-specific key mappings (using string keys for compatibility)
		"LeftCommand":  {{7, 0x02}}, // Map Left Cmd to Symbol Shift
		"RightCommand": {{7, 0x02}}, // Map Right Cmd to Symbol Shift
		"LeftControl":  {{7, 0x02}}, // Map Left Ctrl to Symbol Shift
		"RightControl": {{7, 0x02}}, // Map Right Ctrl to Symbol Shift
		"LeftAlt":      {{7, 0x02}}, // Map Left Alt to Symbol Shift
		"RightAlt":     {{7, 0x02}}, // Map Right Alt to Symbol Shift
		"LeftSuper":    {},          // Left Cmd key on Mac - no mapping (ignore)
		"RightSuper":   {},          // Right Cmd key on Mac - no mapping (ignore)

		// Fyne key constants for punctuation (using Symbol Shift combinations)
		fyne.KeyBackTick:     {{7, 0x02}, {4, 0x01}}, // Symbol Shift + 0 = Backtick
		fyne.KeyApostrophe:   {{7, 0x02}, {5, 0x01}}, // Symbol Shift + P = Quote
		fyne.KeySemicolon:    {{7, 0x02}, {2, 0x02}}, // Symbol Shift + O = Semicolon
		fyne.KeyBackslash:    {{7, 0x02}, {6, 0x01}}, // Symbol Shift + ENTER = Backslash
		fyne.KeyRightBracket: {{7, 0x02}, {5, 0x10}}, // Symbol Shift + Y = Right bracket
		fyne.KeyLeftBracket:  {{7, 0x02}, {5, 0x08}}, // Symbol Shift + U = Left bracket
		fyne.KeyMinus:        {{7, 0x02}, {6, 0x08}}, // Symbol Shift + J = Hyphen/minus
		fyne.KeyEqual:        {{7, 0x02}, {6, 0x02}}, // Symbol Shift + L = Equals
		fyne.KeyPeriod:       {{7, 0x02}, {7, 0x04}}, // Symbol Shift + M = Period
		fyne.KeyComma:        {{7, 0x02}, {7, 0x08}}, // Symbol Shift + N = Comma

		// Additional string-based keys for special characters
		"\"": {{7, 0x02}, {5, 0x01}}, // Symbol Shift + P = Quote (double quote)

		// Additional useful keys
		fyne.KeyTab:    {{0, 0x01}, {7, 0x01}}, // CAPS SHIFT + SPACE (BREAK)
		fyne.KeyEscape: {{0, 0x01}, {7, 0x01}}, // CAPS SHIFT + SPACE (BREAK)
	}
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
