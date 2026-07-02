package sam

import "sync"

// Keyboard is the SAM Coupé key matrix: 9 rows of 8 columns, active-low (a
// pressed key reads 0). Rows 0-7 are selected by the high address byte on an
// IN (active-low, bit r selects row r); row 8 (CONTROL + cursor keys) is read
// only when the whole high byte is 0xFF. The low five columns of a row appear
// on the keyboard port (0xFE) and the high three on the status port (0xF9).
type Keyboard struct {
	mu     sync.Mutex
	matrix [9]byte

	// Typed-character symbol pulse. TypeRune overlays the SAM key combination
	// for a host symbol (e.g. '"', '(', '<') for a few frames so symbols type
	// correctly whatever the host layout, without disturbing held physical keys.
	// While pulseFrames > 0 Scan ANDs pulseMatrix over the physical matrix;
	// pulseFrames counts down once per 50 Hz frame in Tick().
	pulseMatrix [9]byte
	pulseFrames int
}

// samRunePulseFrames is how long a typed-symbol overlay is held. Long enough for
// the SAM ROM's 50 Hz keyboard scan + debounce to register one keypress, short
// enough that it does not trip auto-repeat (which begins much later).
const samRunePulseFrames = 6

// SAM matrix layout (row → columns 0..7). Columns 0-4 read on port 0xFE;
// columns 5-7 read on port 0xF9. (Empirically confirmed against ROM 3.0.)
//
//	0: SHIFT  Z X C V  F1 F2 F3        5: P O I U Y  EQUALS QUOTES F0
//	1: A S D F G  F4 F5 F6             6: RETURN L K J H  SEMICOLON COLON EDIT
//	2: Q W E R T  F7 F8 F9             7: SPACE SYMBOL M N B  COMMA PERIOD INV
//	3: 1 2 3 4 5  ESC TAB CAPS         8: CONTROL UP DOWN LEFT RIGHT  (high==0xFF)
//	4: 0 9 8 7 6  MINUS PLUS DELETE

const (
	samShift  = 0 // row of SHIFT (column 0)
	samSymbol = 7 // row of SYMBOL (column 1, bit 0x02)
)

// keyPress is a matrix position as (row, column-bit mask).
type keyPress struct {
	row  int
	mask byte
}

// samSymbolRuneTable maps a typed host symbol to the SAM key combination that
// produces it. The SAM has dedicated symbol keys (single press) plus bit-paired
// SHIFT and SYMBOL combinations; every entry below was verified by typing it at
// the SAM BASIC prompt and reading back the echoed character.
var samSymbolRuneTable = map[rune][]keyPress{
	// Dedicated keys (single press).
	'-': {{4, 0x20}}, // MINUS
	'+': {{4, 0x40}}, // PLUS
	'=': {{5, 0x20}}, // EQUALS
	'"': {{5, 0x40}}, // QUOTES
	';': {{6, 0x20}}, // SEMICOLON
	':': {{6, 0x40}}, // COLON
	',': {{7, 0x20}}, // COMMA
	'.': {{7, 0x40}}, // PERIOD
	// SHIFT + number row (bit-paired British layout).
	'!':  {{samShift, 0x01}, {3, 0x01}}, // SHIFT+1
	'@':  {{samShift, 0x01}, {3, 0x02}}, // SHIFT+2
	'#':  {{samShift, 0x01}, {3, 0x04}}, // SHIFT+3
	'$':  {{samShift, 0x01}, {3, 0x08}}, // SHIFT+4
	'%':  {{samShift, 0x01}, {3, 0x10}}, // SHIFT+5
	'&':  {{samShift, 0x01}, {4, 0x10}}, // SHIFT+6
	'\'': {{samShift, 0x01}, {4, 0x08}}, // SHIFT+7
	'(':  {{samShift, 0x01}, {4, 0x04}}, // SHIFT+8
	')':  {{samShift, 0x01}, {4, 0x02}}, // SHIFT+9
	'~':  {{samShift, 0x01}, {4, 0x01}}, // SHIFT+0
	// SHIFT + dedicated keys.
	'/': {{samShift, 0x01}, {4, 0x20}}, // SHIFT+MINUS
	'*': {{samShift, 0x01}, {4, 0x40}}, // SHIFT+PLUS
	'_': {{samShift, 0x01}, {5, 0x20}}, // SHIFT+EQUALS
	// SYMBOL + key.
	'<': {{samSymbol, 0x02}, {7, 0x20}}, // SYMBOL+COMMA
	'>': {{samSymbol, 0x02}, {7, 0x40}}, // SYMBOL+PERIOD
}

// NewKeyboard returns a keyboard with all keys released.
func NewKeyboard() *Keyboard {
	k := &Keyboard{}
	for i := range k.matrix {
		k.matrix[i] = 0xFF
		k.pulseMatrix[i] = 0xFF
	}
	return k
}

// SetKey presses (pressed=true) or releases a matrix key at row (0-8) and
// column bit (0-7). The matrix is active-low, so a press drives the bit to 0.
func (k *Keyboard) SetKey(row, bit int, pressed bool) {
	if row < 0 || row > 8 || bit < 0 || bit > 7 {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	mask := byte(1) << uint(bit)
	if pressed {
		k.matrix[row] &^= mask
	} else {
		k.matrix[row] |= mask
	}
}

// ReleaseAll lifts every key (used on reset / focus loss).
func (k *Keyboard) ReleaseAll() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.matrix {
		k.matrix[i] = 0xFF
	}
}

// TypeRune overlays the SAM key combination for the typed host symbol r as a
// brief pulse, independent of the host keyboard layout. Returns true if r is a
// known SAM symbol (letters and digits return false — they come from the
// physical key path so games can hold them).
func (k *Keyboard) TypeRune(r rune) bool {
	combo, ok := samSymbolRuneTable[r]
	if !ok {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.pulseMatrix {
		k.pulseMatrix[i] = 0xFF
	}
	for _, p := range combo {
		k.pulseMatrix[p.row] &^= p.mask
	}
	k.pulseFrames = samRunePulseFrames
	return true
}

// Tick advances the typed-character pulse by one 50 Hz frame, releasing it when
// it expires. Safe to call every frame (no-op when no pulse is active).
func (k *Keyboard) Tick() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pulseFrames > 0 {
		k.pulseFrames--
		if k.pulseFrames == 0 {
			for i := range k.pulseMatrix {
				k.pulseMatrix[i] = 0xFF
			}
		}
	}
}

// Scan returns the ANDed column bits of the rows selected by the high address
// byte. A cleared bit r of highByte selects row r (active-low); multiple
// selected rows are ANDed. A high byte of 0xFF selects row 8 (CONTROL + cursor
// keys). While a typed-symbol pulse is active its overlay is ANDed in too. The
// caller masks the result for the keyboard (bits 0-4) vs status (bits 5-7) ports.
func (k *Keyboard) Scan(highByte byte) byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	pulse := k.pulseFrames > 0
	if highByte == 0xFF {
		if pulse {
			return k.matrix[8] & k.pulseMatrix[8]
		}
		return k.matrix[8]
	}
	result := byte(0xFF)
	for r := 0; r < 8; r++ {
		if highByte&(1<<uint(r)) == 0 {
			result &= k.matrix[r]
			if pulse {
				result &= k.pulseMatrix[r]
			}
		}
	}
	return result
}
