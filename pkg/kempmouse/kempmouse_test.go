package kempmouse

import "testing"

func TestNewIsIdle(t *testing.T) {
	m := New()
	if m.X() != 0 || m.Y() != 0 {
		t.Errorf("new mouse: x=%d y=%d, want 0 0", m.X(), m.Y())
	}
	if m.Buttons() != 0xFF {
		t.Errorf("new mouse: buttons=%02X, want 0xFF (all released)", m.Buttons())
	}
}

func TestMoveAccumulates(t *testing.T) {
	m := New()
	m.Move(5, 3)
	if m.X() != 5 {
		t.Errorf("after Move(5,3): X=%d, want 5", m.X())
	}
	// Y is inverted on the way in.
	if m.Y() != 253 { // 0 - 3 = -3, cast to byte = 253
		t.Errorf("after Move(5,3): Y=%d, want 253 (Y inverted)", m.Y())
	}
	m.Move(-2, -10)
	if m.X() != 3 {
		t.Errorf("after -2: X=%d, want 3", m.X())
	}
	if m.Y() != 7 { // 253 - (-10) = 263 → 7 (mod 256)
		t.Errorf("after +10: Y=%d, want 7", m.Y())
	}
}

func TestButtonSetClear(t *testing.T) {
	m := New()
	m.SetButton(1, true) // left button down
	if got := m.Buttons(); got != 0xFD {
		t.Errorf("left down: %02X, want 0xFD", got)
	}
	m.SetButton(0, true) // right button down
	if got := m.Buttons(); got != 0xFC {
		t.Errorf("both down: %02X, want 0xFC", got)
	}
	m.SetButton(1, false) // left released
	if got := m.Buttons(); got != 0xFE {
		t.Errorf("right only: %02X, want 0xFE", got)
	}
}

func TestHandlePortReadButtons(t *testing.T) {
	m := New()
	m.SetButton(0, true) // right down
	val, ok := m.HandlePortRead(0xFADF)
	if !ok {
		t.Fatal("0xFADF: not handled")
	}
	if val != 0xFE {
		t.Errorf("0xFADF: %02X, want 0xFE", val)
	}
}

func TestHandlePortReadXY(t *testing.T) {
	m := New()
	m.Move(42, 0)
	valX, okX := m.HandlePortRead(0xFBDF)
	if !okX || valX != 42 {
		t.Errorf("0xFBDF: %02X ok=%v, want 42 true", valX, okX)
	}
	valY, okY := m.HandlePortRead(0xFFDF)
	if !okY || valY != 0 {
		t.Errorf("0xFFDF: %02X ok=%v, want 0 true", valY, okY)
	}
}

func TestHandlePortReadRejectsUnrelated(t *testing.T) {
	m := New()
	// The ULA port 0xFE doesn't match any of the Kempston mouse
	// patterns — the mouse must fall through cleanly.
	if _, ok := m.HandlePortRead(0xFE); ok {
		t.Error("0xFE: Kempston mouse claimed a ULA port")
	}
}

func TestResetClearsState(t *testing.T) {
	m := New()
	m.Move(50, 30)
	m.SetButton(0, true)
	m.Reset()
	if m.X() != 0 || m.Y() != 0 || m.Buttons() != 0xFF {
		t.Errorf("after Reset: x=%d y=%d buttons=%02X, want 0 0 0xFF",
			m.X(), m.Y(), m.Buttons())
	}
}
