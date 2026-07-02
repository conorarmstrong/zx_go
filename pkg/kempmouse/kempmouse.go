// Package kempmouse implements the Kempston mouse interface — one
// of two competing Spectrum mouse peripherals (the other is the
// AMX mouse, not implemented here). The Kempston mouse exposes
// three I/O ports:
//
//	buttons: (port & 0x0121) == 0x0001   // canonical 0xFADF
//	X pos:   (port & 0x0521) == 0x0101   // canonical 0xFBDF
//	Y pos:   (port & 0x0521) == 0x0501   // canonical 0xFFDF
//
// Reading X / Y returns an 8-bit free-running counter that
// increments with host mouse movement; the host-side driver
// accumulates deltas into these counters and they wrap naturally
// at 0/255. Reading the buttons port returns an 8-bit bitmap where
// 0 = pressed (active-low), the same convention as the Kempston
// joystick: bit 0 = right button, bit 1 = left button, default all 1.
package kempmouse

import "sync/atomic"

// Port-decode masks and values for the three canonical Kempston
// mouse ports.
const (
	maskButtons = 0x0121
	valButtons  = 0x0001

	maskXY = 0x0521
	valX   = 0x0101
	valY   = 0x0501
)

// Mouse is the Kempston mouse controller. Safe for concurrent use:
// the emulation goroutine reads X / Y / buttons via HandlePortRead,
// and the UI goroutine writes them via Move / SetButton as host
// events arrive. All accessors use atomic operations so there's
// no tearing and no lock-hold penalty on the per-IN-read hot path.
type Mouse struct {
	// Position counters. Stored as int32 so atomic operations are
	// available and signed arithmetic (Move dx dy deltas) doesn't
	// need a mutex. Only the low 8 bits are meaningful — they're
	// masked on read.
	x atomic.Int32
	y atomic.Int32

	// Button bitmap. Starts at 0xFF (all buttons released,
	// active-low). Stored as int32 for atomic access; only the
	// low 8 bits matter.
	buttons atomic.Int32
}

// New constructs an idle mouse: cursor at 0,0 and no buttons
// pressed.
func New() *Mouse {
	m := &Mouse{}
	m.buttons.Store(0xFF)
	return m
}

// Reset restores the power-on state: position cleared, no buttons
// pressed. Does not detach the mouse from the peripheral bus.
func (m *Mouse) Reset() {
	m.x.Store(0)
	m.y.Store(0)
	m.buttons.Store(0xFF)
}

// Move accumulates host-mouse deltas into the X / Y counters. dx
// and dy are signed deltas in "mouse units" — usually pixels, but
// the Spectrum software interprets them freely. Y is inverted on
// the way in to match Kempston convention (host +Y = screen down,
// Kempston +Y = screen up).
func (m *Mouse) Move(dx, dy int) {
	m.x.Add(int32(dx))
	m.y.Add(-int32(dy))
}

// SetButton presses or releases the given Kempston button bit:
// bit 0 is right-click, bit 1 is left-click. Pressed drives the bit
// LOW on the Kempston bus (active-low).
func (m *Mouse) SetButton(btn int, pressed bool) {
	if btn < 0 || btn > 7 {
		return
	}
	mask := int32(1 << btn)
	for {
		old := m.buttons.Load()
		var next int32
		if pressed {
			next = old &^ mask
		} else {
			next = old | mask
		}
		if m.buttons.CompareAndSwap(old, next) {
			return
		}
	}
}

// HandlePortRead is the peripheral-manager-compatible read handler.
// Decodes the port address against the three Kempston patterns and
// returns the corresponding counter / button byte, or (0xFF, false)
// for unrelated addresses so the caller can fall through to the
// next peripheral.
func (m *Mouse) HandlePortRead(port uint16) (byte, bool) {
	switch {
	case port&maskXY == valX:
		return byte(m.x.Load() & 0xFF), true
	case port&maskXY == valY:
		return byte(m.y.Load() & 0xFF), true
	case port&maskButtons == valButtons:
		return byte(m.buttons.Load() & 0xFF), true
	}
	return 0xFF, false
}

// X returns the current X counter value. Used by tests and the UI
// status bar.
func (m *Mouse) X() byte { return byte(m.x.Load() & 0xFF) }

// Y returns the current Y counter value.
func (m *Mouse) Y() byte { return byte(m.y.Load() & 0xFF) }

// Buttons returns the current button bitmap (active-low).
func (m *Mouse) Buttons() byte { return byte(m.buttons.Load() & 0xFF) }
