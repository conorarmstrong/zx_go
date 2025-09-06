package keyboard

import "fyne.io/fyne/v2"

// Keyboard handles keyboard input by mapping modern keys to the Spectrum's 8x5 matrix.
type Keyboard struct {
	// Matrix state: 8 rows, 5 bits per row. A bit is 0 when a key is pressed.
	matrix [8]byte
	keyMap map[fyne.KeyName][]keyMapping
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

// HandleKeyEvent processes a Fyne key event.
func (k *Keyboard) HandleKeyEvent(ev *fyne.KeyEvent, isPressed bool) {
	if mappings, ok := k.keyMap[ev.Name]; ok {
		for _, m := range mappings {
			if isPressed {
				k.matrix[m.row] &= ^m.mask
			} else {
				k.matrix[m.row] |= m.mask
			}
		}
	}
}

// Scan reads the keyboard matrix for a given port address.
// The high byte of the address determines which rows are being scanned.
func (k *Keyboard) Scan(addr uint16) byte {
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
		"ShiftLeft":  {{0, 0x01}},
		fyne.KeyZ:          {{0, 0x02}},
		fyne.KeyX:          {{0, 0x04}},
		fyne.KeyC:          {{0, 0x08}},
		fyne.KeyV:          {{0, 0x10}},

		// Row 1: A, S, D, F, G
		fyne.KeyA: {{1, 0x01}},
		fyne.KeyS: {{1, 0x02}},
		fyne.KeyD: {{1, 0x04}},
		fyne.KeyF: {{1, 0x08}},
		fyne.KeyG: {{1, 0x10}},

		// Row 2: Q, W, E, R, T
		fyne.KeyQ: {{2, 0x01}},
		fyne.KeyW: {{2, 0x02}},
		fyne.KeyE: {{2, 0x04}},
		fyne.KeyR: {{2, 0x08}},
		fyne.KeyT: {{2, 0x10}},

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

		// Row 6: ENTER, L, K, J, H
		fyne.KeyReturn: {{6, 0x01}},
		fyne.KeyL:      {{6, 0x02}},
		fyne.KeyK:      {{6, 0x04}},
		fyne.KeyJ:      {{6, 0x08}},
		fyne.KeyH:      {{6, 0x10}},

		// Row 7: SPACE, SYMBOL SHIFT, M, N, B
		fyne.KeySpace:      {{7, 0x01}},
		"ShiftRight": {{7, 0x02}}, // Symbol Shift
		fyne.KeyM:          {{7, 0x04}},
		fyne.KeyN:          {{7, 0x08}},
		fyne.KeyB:          {{7, 0x10}},

		// Arrow keys as 5,6,7,8 with CAPS SHIFT
		fyne.KeyLeft:  {{3, 0x10}, {0, 0x01}}, // 5
		fyne.KeyDown:  {{4, 0x10}, {0, 0x01}}, // 6
		fyne.KeyUp:    {{4, 0x08}, {0, 0x01}}, // 7
		fyne.KeyRight: {{4, 0x04}, {0, 0x01}}, // 8
	}
}