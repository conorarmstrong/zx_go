package keyboard

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

func TestKeyboardCreation(t *testing.T) {
	kbd := New()

	if kbd == nil {
		t.Fatal("Keyboard creation returned nil")
	}

	// All matrix rows should start with all bits set (keys up)
	for i, row := range kbd.matrix {
		if row != 0xFF {
			t.Errorf("Row %d should be 0xFF initially, got 0x%02X", i, row)
		}
	}

	// Key map should be initialized
	if kbd.keyMap == nil {
		t.Error("Key map should be initialized")
	}

	if len(kbd.keyMap) == 0 {
		t.Error("Key map should not be empty")
	}
}

func TestBasicKeyMapping(t *testing.T) {
	kbd := New()

	testCases := []struct {
		key         fyne.KeyName
		expectedRow int
		expectedBit byte
	}{
		// Row 0: CAPS SHIFT, Z, X, C, V
		{fyne.KeyZ, 0, 0x02},
		{fyne.KeyX, 0, 0x04},
		{fyne.KeyC, 0, 0x08},
		{fyne.KeyV, 0, 0x10},

		// Row 1: A, S, D, F, G
		{fyne.KeyA, 1, 0x01},
		{fyne.KeyS, 1, 0x02},
		{fyne.KeyD, 1, 0x04},
		{fyne.KeyF, 1, 0x08},
		{fyne.KeyG, 1, 0x10},

		// Row 2: Q, W, E, R, T
		{fyne.KeyQ, 2, 0x01},
		{fyne.KeyW, 2, 0x02},
		{fyne.KeyE, 2, 0x04},
		{fyne.KeyR, 2, 0x08},
		{fyne.KeyT, 2, 0x10},

		// Row 3: 1, 2, 3, 4, 5
		{fyne.Key1, 3, 0x01},
		{fyne.Key2, 3, 0x02},
		{fyne.Key3, 3, 0x04},
		{fyne.Key4, 3, 0x08},
		{fyne.Key5, 3, 0x10},

		// Row 4: 0, 9, 8, 7, 6
		{fyne.Key0, 4, 0x01},
		{fyne.Key9, 4, 0x02},
		{fyne.Key8, 4, 0x04},
		{fyne.Key7, 4, 0x08},
		{fyne.Key6, 4, 0x10},

		// Row 5: P, O, I, U, Y
		{fyne.KeyP, 5, 0x01},
		{fyne.KeyO, 5, 0x02},
		{fyne.KeyI, 5, 0x04},
		{fyne.KeyU, 5, 0x08},
		{fyne.KeyY, 5, 0x10},

		// Row 6: ENTER, L, K, J, H
		{fyne.KeyReturn, 6, 0x01},
		{fyne.KeyL, 6, 0x02},
		{fyne.KeyK, 6, 0x04},
		{fyne.KeyJ, 6, 0x08},
		{fyne.KeyH, 6, 0x10},

		// Row 7: SPACE, SYMBOL SHIFT, M, N, B
		{fyne.KeySpace, 7, 0x01},
		{fyne.KeyM, 7, 0x04},
		{fyne.KeyN, 7, 0x08},
		{fyne.KeyB, 7, 0x10},
	}

	for _, tc := range testCases {
		// Press key
		ev := &fyne.KeyEvent{Name: tc.key}
		kbd.HandleKeyEvent(ev, true)

		// Check that the correct bit is cleared (pressed)
		if (kbd.matrix[tc.expectedRow] & tc.expectedBit) != 0 {
			t.Errorf("Key %v: bit should be cleared when pressed, row %d = 0x%02X",
				tc.key, tc.expectedRow, kbd.matrix[tc.expectedRow])
		}

		// Release key
		kbd.HandleKeyEvent(ev, false)

		// Check that the bit is set again (released)
		if (kbd.matrix[tc.expectedRow] & tc.expectedBit) == 0 {
			t.Errorf("Key %v: bit should be set when released, row %d = 0x%02X",
				tc.key, tc.expectedRow, kbd.matrix[tc.expectedRow])
		}
	}
}

func TestShiftKeys(t *testing.T) {
	kbd := New()

	// Test left shift (CAPS SHIFT)
	leftShiftEv := &fyne.KeyEvent{Name: desktop.KeyShiftLeft}
	kbd.HandleKeyEvent(leftShiftEv, true)

	if (kbd.matrix[0] & 0x01) != 0 {
		t.Error("Left shift should clear bit 0 of row 0")
	}

	kbd.HandleKeyEvent(leftShiftEv, false)
	if (kbd.matrix[0] & 0x01) == 0 {
		t.Error("Left shift release should set bit 0 of row 0")
	}

	// Test right shift (SYMBOL SHIFT)
	rightShiftEv := &fyne.KeyEvent{Name: desktop.KeyShiftRight}
	kbd.HandleKeyEvent(rightShiftEv, true)

	if (kbd.matrix[7] & 0x02) != 0 {
		t.Error("Right shift should clear bit 1 of row 7")
	}

	kbd.HandleKeyEvent(rightShiftEv, false)
	if (kbd.matrix[7] & 0x02) == 0 {
		t.Error("Right shift release should set bit 1 of row 7")
	}
}

func TestMultiKeyMappings(t *testing.T) {
	kbd := New()

	// Test arrow keys which map to multiple keys
	testCases := []struct {
		key          fyne.KeyName
		expectedRows []struct {
			row int
			bit byte
		}
	}{
		{
			fyne.KeyLeft, // 5 + CAPS SHIFT
			[]struct {
				row int
				bit byte
			}{
				{3, 0x10}, // 5
				{0, 0x01}, // CAPS SHIFT
			},
		},
		{
			fyne.KeyDown, // 6 + CAPS SHIFT
			[]struct {
				row int
				bit byte
			}{
				{4, 0x10}, // 6
				{0, 0x01}, // CAPS SHIFT
			},
		},
		{
			fyne.KeyUp, // 7 + CAPS SHIFT
			[]struct {
				row int
				bit byte
			}{
				{4, 0x08}, // 7
				{0, 0x01}, // CAPS SHIFT
			},
		},
		{
			fyne.KeyRight, // 8 + CAPS SHIFT
			[]struct {
				row int
				bit byte
			}{
				{4, 0x04}, // 8
				{0, 0x01}, // CAPS SHIFT
			},
		},
		{
			fyne.KeyBackspace, // 0 + CAPS SHIFT
			[]struct {
				row int
				bit byte
			}{
				{4, 0x01}, // 0
				{0, 0x01}, // CAPS SHIFT
			},
		},
	}

	for _, tc := range testCases {
		// Reset keyboard
		for i := range kbd.matrix {
			kbd.matrix[i] = 0xFF
		}

		// Press key
		ev := &fyne.KeyEvent{Name: tc.key}
		kbd.HandleKeyEvent(ev, true)

		// Check that all expected bits are cleared
		for _, expected := range tc.expectedRows {
			if (kbd.matrix[expected.row] & expected.bit) != 0 {
				t.Errorf("Key %v: bit 0x%02X in row %d should be cleared when pressed",
					tc.key, expected.bit, expected.row)
			}
		}

		// Release key
		kbd.HandleKeyEvent(ev, false)

		// Check that all bits are set again
		for _, expected := range tc.expectedRows {
			if (kbd.matrix[expected.row] & expected.bit) == 0 {
				t.Errorf("Key %v: bit 0x%02X in row %d should be set when released",
					tc.key, expected.bit, expected.row)
			}
		}
	}
}

func TestKeyboardScan(t *testing.T) {
	kbd := New()

	// Test scanning with no keys pressed
	result := kbd.Scan(0x0000) // Scan all rows
	if result != 0xFF {
		t.Errorf("Scan with no keys should return 0xFF, got 0x%02X", result)
	}

	// Test scanning specific rows
	// Press 'A' (row 1, bit 0)
	aEv := &fyne.KeyEvent{Name: fyne.KeyA}
	kbd.HandleKeyEvent(aEv, true)

	// Scan row 1 only
	result = kbd.Scan(0xFD00) // All bits except bit 1 set in high byte
	expected := byte(0xFE)    // All bits except bit 0 set
	if result != expected {
		t.Errorf("Scan row 1 with 'A' pressed: expected 0x%02X, got 0x%02X", expected, result)
	}

	// Scan row 0 (shouldn't see 'A')
	result = kbd.Scan(0xFE00) // All bits except bit 0 set in high byte
	if result != 0xFF {
		t.Errorf("Scan row 0 with 'A' pressed: expected 0xFF, got 0x%02X", result)
	}

	// Reset and test multiple keys in same row
	kbd.HandleKeyEvent(aEv, false) // Release A

	// Press 'A' and 'S' (both in row 1)
	kbd.HandleKeyEvent(aEv, true)
	sEv := &fyne.KeyEvent{Name: fyne.KeyS}
	kbd.HandleKeyEvent(sEv, true)

	result = kbd.Scan(0xFD00) // Scan row 1
	expected = byte(0xFC)     // Bits 0 and 1 clear
	if result != expected {
		t.Errorf("Scan row 1 with 'A' and 'S' pressed: expected 0x%02X, got 0x%02X", expected, result)
	}
}

func TestKeyboardScanMultipleRows(t *testing.T) {
	kbd := New()

	// Press keys in different rows
	aEv := &fyne.KeyEvent{Name: fyne.KeyA}         // Row 1, bit 0
	spaceEv := &fyne.KeyEvent{Name: fyne.KeySpace} // Row 7, bit 0

	kbd.HandleKeyEvent(aEv, true)
	kbd.HandleKeyEvent(spaceEv, true)

	// Scan both rows 1 and 7
	result := kbd.Scan(0x7D00) // Bits 1 and 7 clear in high byte (rows 1 and 7)
	expected := byte(0xFE)     // Both keys affect bit 0, so result is 0xFE
	if result != expected {
		t.Errorf("Scan rows 1&7 with keys pressed: expected 0x%02X, got 0x%02X", expected, result)
	}

	// Scan just row 1
	result = kbd.Scan(0xFD00)
	expected = byte(0xFE) // Only A pressed in row 1
	if result != expected {
		t.Errorf("Scan row 1 only: expected 0x%02X, got 0x%02X", expected, result)
	}

	// Scan just row 7
	result = kbd.Scan(0x7F00)
	expected = byte(0xFE) // Only Space pressed in row 7
	if result != expected {
		t.Errorf("Scan row 7 only: expected 0x%02X, got 0x%02X", expected, result)
	}
}

func TestUnmappedKeys(t *testing.T) {
	kbd := New()

	// Save initial state
	initialMatrix := kbd.matrix

	// Try to handle an unmapped key
	unmappedEv := &fyne.KeyEvent{Name: fyne.KeyTab}
	kbd.HandleKeyEvent(unmappedEv, true)

	// Matrix should be unchanged
	for i := range kbd.matrix {
		if kbd.matrix[i] != initialMatrix[i] {
			t.Errorf("Unmapped key changed matrix row %d: was 0x%02X, now 0x%02X",
				i, initialMatrix[i], kbd.matrix[i])
		}
	}
}

func TestKeyboardMatrix8x5Layout(t *testing.T) {
	kbd := New()

	// Verify matrix dimensions
	if len(kbd.matrix) != 8 {
		t.Errorf("Matrix should have 8 rows, got %d", len(kbd.matrix))
	}

	// Test that we only use 5 bits per row (bits 0-4)
	// The Spectrum keyboard has 8 rows of 5 keys each

	// Press all mapped keys and verify only bits 0-4 are affected
	allKeys := []fyne.KeyName{
		// All alphabet keys
		fyne.KeyA, fyne.KeyB, fyne.KeyC, fyne.KeyD, fyne.KeyE,
		fyne.KeyF, fyne.KeyG, fyne.KeyH, fyne.KeyI, fyne.KeyJ,
		fyne.KeyK, fyne.KeyL, fyne.KeyM, fyne.KeyN, fyne.KeyO,
		fyne.KeyP, fyne.KeyQ, fyne.KeyR, fyne.KeyS, fyne.KeyT,
		fyne.KeyU, fyne.KeyV, fyne.KeyW, fyne.KeyX, fyne.KeyY, fyne.KeyZ,
		// All number keys
		fyne.Key0, fyne.Key1, fyne.Key2, fyne.Key3, fyne.Key4,
		fyne.Key5, fyne.Key6, fyne.Key7, fyne.Key8, fyne.Key9,
		// Special keys
		fyne.KeySpace, fyne.KeyReturn,
	}

	for _, key := range allKeys {
		// Reset matrix
		for i := range kbd.matrix {
			kbd.matrix[i] = 0xFF
		}

		// Press key
		ev := &fyne.KeyEvent{Name: key}
		kbd.HandleKeyEvent(ev, true)

		// Check that only bits 0-4 can be affected
		for i, row := range kbd.matrix {
			if (row & 0xE0) != 0xE0 {
				t.Errorf("Key %v affected bits above bit 4 in row %d: 0x%02X", key, i, row)
			}
		}
	}
}

func TestSpectrumKeyboardLayout(t *testing.T) {
	// Test the complete Spectrum keyboard layout based on actual mapping
	// Format: row -> {bit0, bit1, bit2, bit3, bit4}
	spectrumLayout := map[int][]struct {
		key fyne.KeyName
		bit byte
	}{
		0: {{fyne.KeyZ, 0x02}, {fyne.KeyX, 0x04}, {fyne.KeyC, 0x08}, {fyne.KeyV, 0x10}}, // CAPS SHIFT = 0x01
		1: {{fyne.KeyA, 0x01}, {fyne.KeyS, 0x02}, {fyne.KeyD, 0x04}, {fyne.KeyF, 0x08}, {fyne.KeyG, 0x10}},
		2: {{fyne.KeyQ, 0x01}, {fyne.KeyW, 0x02}, {fyne.KeyE, 0x04}, {fyne.KeyR, 0x08}, {fyne.KeyT, 0x10}},
		3: {{fyne.Key1, 0x01}, {fyne.Key2, 0x02}, {fyne.Key3, 0x04}, {fyne.Key4, 0x08}, {fyne.Key5, 0x10}},
		4: {{fyne.Key0, 0x01}, {fyne.Key9, 0x02}, {fyne.Key8, 0x04}, {fyne.Key7, 0x08}, {fyne.Key6, 0x10}},
		5: {{fyne.KeyP, 0x01}, {fyne.KeyO, 0x02}, {fyne.KeyI, 0x04}, {fyne.KeyU, 0x08}, {fyne.KeyY, 0x10}},
		6: {{fyne.KeyL, 0x02}, {fyne.KeyK, 0x04}, {fyne.KeyJ, 0x08}, {fyne.KeyH, 0x10}}, // ENTER = 0x01
		7: {{fyne.KeyM, 0x04}, {fyne.KeyN, 0x08}, {fyne.KeyB, 0x10}},                    // SPACE = 0x01, SYMBOL SHIFT = 0x02
	}

	kbd := New()

	for row, keyMappings := range spectrumLayout {
		for _, mapping := range keyMappings {
			// Reset matrix
			for i := range kbd.matrix {
				kbd.matrix[i] = 0xFF
			}

			// Press key
			ev := &fyne.KeyEvent{Name: mapping.key}
			kbd.HandleKeyEvent(ev, true)

			// Check it's in the right position
			if (kbd.matrix[row] & mapping.bit) != 0 {
				t.Errorf("Key %v should clear bit 0x%02X in row %d", mapping.key, mapping.bit, row)
			}

			// Check other positions are unaffected (except for multi-key mappings)
			for r := 0; r < 8; r++ {
				if r == row {
					continue
				}
				if kbd.matrix[r] != 0xFF {
					// Check if this is a valid multi-key mapping
					isMultiKey := false
					multiKeys := []fyne.KeyName{fyne.KeyLeft, fyne.KeyDown, fyne.KeyUp, fyne.KeyRight, fyne.KeyBackspace}
					for _, mk := range multiKeys {
						if mapping.key == mk {
							isMultiKey = true
							break
						}
					}
					if !isMultiKey {
						t.Errorf("Key %v affected unexpected row %d: 0x%02X", mapping.key, r, kbd.matrix[r])
					}
				}
			}
		}
	}
}

func TestKeyEventCycling(t *testing.T) {
	kbd := New()

	// Test rapid press/release cycles
	key := fyne.KeyA
	ev := &fyne.KeyEvent{Name: key}

	for i := 0; i < 100; i++ {
		// Press
		kbd.HandleKeyEvent(ev, true)
		if (kbd.matrix[1] & 0x01) != 0 {
			t.Errorf("Iteration %d: key should be pressed", i)
		}

		// Release
		kbd.HandleKeyEvent(ev, false)
		if (kbd.matrix[1] & 0x01) == 0 {
			t.Errorf("Iteration %d: key should be released", i)
		}
	}
}

// Benchmark tests
func BenchmarkKeyPress(b *testing.B) {
	kbd := New()
	ev := &fyne.KeyEvent{Name: fyne.KeyA}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kbd.HandleKeyEvent(ev, i%2 == 0) // Alternate press/release
	}
}

func BenchmarkKeyboardScan(b *testing.B) {
	kbd := New()

	// Press a few keys
	keys := []fyne.KeyName{fyne.KeyA, fyne.KeyS, fyne.KeyD}
	for _, key := range keys {
		ev := &fyne.KeyEvent{Name: key}
		kbd.HandleKeyEvent(ev, true)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kbd.Scan(0xFE00) // Scan common address
	}
}

func BenchmarkMultiKeyScan(b *testing.B) {
	kbd := New()

	// Press keys in different rows
	keys := []fyne.KeyName{fyne.KeyA, fyne.KeySpace, fyne.Key1, fyne.KeyP}
	for _, key := range keys {
		ev := &fyne.KeyEvent{Name: key}
		kbd.HandleKeyEvent(ev, true)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kbd.Scan(0x0000) // Scan all rows
	}
}
