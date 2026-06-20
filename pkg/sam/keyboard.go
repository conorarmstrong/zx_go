package sam

// Keyboard is the SAM Coupé key matrix: 9 rows of 8 columns, active-low (a
// pressed key reads 0). Rows 0-7 are selected by the high address byte on an
// IN (active-low, bit r selects row r); row 8 (CONTROL + cursor keys) is read
// only when the whole high byte is 0xFF. The low five columns of a row appear
// on the keyboard port (0xFE) and the high three on the status port (0xF9).
// (SimCoupe Keyboard.h / SAMIO.cpp:427-467, plan Appendix E.)
type Keyboard struct {
	matrix [9]byte
}

// SAM matrix layout (row → columns 0..7), for reference when host-mapping is
// added with the GUI:
//
//	0: SHIFT  Z X C V  F1 F2 F3        5: P O I U Y  EQUALS QUOTES F0
//	1: A S D F G  F4 F5 F6             6: RETURN L K J H  SEMICOLON COLON EDIT
//	2: Q W E R T  F7 F8 F9             7: SPACE SYMBOL M N B  COMMA PERIOD INV
//	3: 1 2 3 4 5  ESC TAB CAPS         8: CONTROL UP DOWN LEFT RIGHT  (high==0xFF)
//	4: 0 9 8 7 6  MINUS PLUS DELETE
//
// Columns 0-4 read on port 0xFE; columns 5-7 read on port 0xF9.

// NewKeyboard returns a keyboard with all keys released.
func NewKeyboard() *Keyboard {
	k := &Keyboard{}
	for i := range k.matrix {
		k.matrix[i] = 0xFF
	}
	return k
}

// SetKey presses (pressed=true) or releases a matrix key at row (0-8) and
// column bit (0-7). The matrix is active-low, so a press drives the bit to 0.
func (k *Keyboard) SetKey(row, bit int, pressed bool) {
	if row < 0 || row > 8 || bit < 0 || bit > 7 {
		return
	}
	mask := byte(1) << uint(bit)
	if pressed {
		k.matrix[row] &^= mask
	} else {
		k.matrix[row] |= mask
	}
}

// ReleaseAll lifts every key (used on reset / focus loss).
func (k *Keyboard) ReleaseAll() {
	for i := range k.matrix {
		k.matrix[i] = 0xFF
	}
}

// Scan returns the ANDed column bits of the rows selected by the high address
// byte. A cleared bit r of highByte selects row r (active-low); multiple
// selected rows are ANDed. A high byte of 0xFF selects row 8 (CONTROL + cursor
// keys). The caller masks the result for the keyboard (bits 0-4) vs status
// (bits 5-7) ports.
func (k *Keyboard) Scan(highByte byte) byte {
	if highByte == 0xFF {
		return k.matrix[8]
	}
	result := byte(0xFF)
	for r := 0; r < 8; r++ {
		if highByte&(1<<uint(r)) == 0 {
			result &= k.matrix[r]
		}
	}
	return result
}
