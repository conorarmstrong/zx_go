package kempmouse

// Spec-derived test suite for the Kempston mouse interface.
//
// There is no FPGA core to wrap for this peripheral — the Kempston
// mouse is a small fixed-function I/O device, so the authoritative
// oracle is the documented "Kempston mouse interface" hardware spec.
// The behaviours pinned here are cross-checked against:
//
//   - Sinclair Wiki, "Kempston Mouse": ports X=$FBDF, Y=$FFDF,
//     Buttons=$FADF; button bits "D0:Right D1:Left D2:Middle (some
//     compatibles only)"; X/Y are 8-bit counters that "Roll over at
//     0 and $FF".
//   - The de-facto Kempston-mouse port protocol as documented for the
//     ZX Spectrum: a button reads 0 when pressed and 1 at rest
//     (active-low); the idle buttons byte is therefore $FF. X and Y
//     are free-running 8-bit counters incremented/decremented by host
//     motion, wrapping cyclically through $FF<->$00.
//
// Where our implementation makes a choice the spec is silent on
// (e.g. partial address decode beyond the three canonical ports, or
// the host-Y inversion in Move), the choice is recorded below as a
// pinned-behaviour assertion rather than changed, so a future edit
// that alters it trips a test.

import "testing"

// --- Port decode: the three canonical Kempston-mouse addresses ----

// The spec defines exactly three canonical ports. Each must decode to
// its own register and to nothing else.
func TestSpecCanonicalPortDecode(t *testing.T) {
	const (
		portButtons = 0xFADF
		portX       = 0xFBDF
		portY       = 0xFFDF
	)
	m := New()
	// Give each register a distinct value so a mis-decode is visible.
	m.Move(0x11, 0) // X = 0x11
	// Y: Move inverts host-Y, so push Y directly to a known value via
	// repeated moves; simpler to assert the relationship explicitly.
	m.SetButton(0, true) // buttons = 0xFE

	if v, ok := m.HandlePortRead(portButtons); !ok || v != 0xFE {
		t.Errorf("$FADF buttons: got %02X ok=%v, want FE true", v, ok)
	}
	if v, ok := m.HandlePortRead(portX); !ok || v != 0x11 {
		t.Errorf("$FBDF X: got %02X ok=%v, want 11 true", v, ok)
	}
	if v, ok := m.HandlePortRead(portY); !ok || v != 0x00 {
		t.Errorf("$FFDF Y: got %02X ok=%v, want 00 true", v, ok)
	}
}

// The buttons port must NOT alias onto the X or Y register, and the X
// port must not alias onto Y (the decode is order-independent for the
// three canonical addresses).
func TestSpecCanonicalPortsAreExclusive(t *testing.T) {
	m := New()
	m.Move(0x55, 0)      // X = 0x55
	m.SetButton(1, true) // buttons = 0xFD

	// X port returns the X counter, never the buttons byte.
	if v, _ := m.HandlePortRead(0xFBDF); v == 0xFD {
		t.Errorf("$FBDF returned the buttons byte %02X — X aliased onto buttons", v)
	}
	// Buttons port returns the buttons byte, never the X counter.
	if v, _ := m.HandlePortRead(0xFADF); v == 0x55 {
		t.Errorf("$FADF returned the X counter %02X — buttons aliased onto X", v)
	}
	// Y port (idle 0) must not return the live X counter.
	if v, _ := m.HandlePortRead(0xFFDF); v == 0x55 {
		t.Errorf("$FFDF returned the X counter %02X — Y aliased onto X", v)
	}
}

// Unrelated ports must fall through cleanly so the peripheral bus can
// reach the next device (ULA, AY, joystick, ...).
func TestSpecRejectsUnrelatedPorts(t *testing.T) {
	m := New()
	// The ULA port (0x00FE) and the 128K paging / AY ports (0x7FFD,
	// 0xBFFD, 0xFFFD) share none of the decoded Kempston-mouse bits, so
	// they must fall through. (NB: 0x001F — the canonical Kempston
	// *joystick* port — DOES collide with the mouse buttons' partial
	// decode and is pinned separately in TestSpecPartialDecodeAliases.)
	for _, p := range []uint16{0x00FE, 0x7FFD, 0xBFFD, 0xFFFD} {
		if v, ok := m.HandlePortRead(p); ok {
			t.Errorf("port %04X: Kempston mouse claimed it (val=%02X) — should fall through", p, v)
		}
	}
}

// PINNED BEHAVIOUR (deliberate, not a bug): the real interface only
// decodes a subset of the address lines, so addresses sharing the
// decoded bits with a canonical port also hit that register. Our masks
// (0x0121/0x0001 buttons, 0x0521/0x0101 X, 0x0521/0x0501 Y) reproduce
// that partial decode. These are NOT the canonical ports but are
// genuine hardware aliases; pin them so a future "tighten the decode"
// edit is a conscious decision.
func TestSpecPartialDecodeAliases(t *testing.T) {
	m := New()
	m.SetButton(0, true) // buttons = 0xFE
	// 0xFEDF and 0x00DF share the decoded buttons bits with 0xFADF.
	// 0x001F is the canonical Kempston *joystick* port, which also
	// satisfies (port & 0x0121) == 0x0001 — a real consequence of the
	// mouse interface's partial address decode. Pinned, not "fixed":
	// in a full machine the joystick is read first / the mouse is an
	// optional add-on, so the collision is a documented hardware fact
	// rather than a decode bug.
	for _, p := range []uint16{0xFEDF, 0x00DF, 0x001F} {
		if v, ok := m.HandlePortRead(p); !ok || v != 0xFE {
			t.Errorf("alias %04X: got %02X ok=%v, want FE true (partial decode)", p, v, ok)
		}
	}
}

// --- Buttons: polarity, per-button, simultaneous, idle ------------

// Spec: at rest every button bit is 1; the idle byte is $FF.
func TestSpecButtonsIdleHigh(t *testing.T) {
	if got := New().Buttons(); got != 0xFF {
		t.Errorf("idle buttons = %02X, want FF (all released, active-low)", got)
	}
}

// Spec bit assignment + active-low polarity, exercised one button at a
// time from a clean idle state.
func TestSpecButtonBitAssignment(t *testing.T) {
	cases := []struct {
		name string
		bit  int
		want byte // buttons byte with only this bit pressed (cleared)
	}{
		{"right (D0)", 0, 0xFE},
		{"left (D1)", 1, 0xFD},
		{"middle (D2)", 2, 0xFB},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New()
			m.SetButton(c.bit, true)
			if got := m.Buttons(); got != c.want {
				t.Errorf("press %s: %02X, want %02X", c.name, got, c.want)
			}
			// Read it back through the canonical port too.
			if v, ok := m.HandlePortRead(0xFADF); !ok || v != c.want {
				t.Errorf("$FADF after press %s: %02X ok=%v, want %02X true", c.name, v, ok, c.want)
			}
			// Release returns to idle.
			m.SetButton(c.bit, false)
			if got := m.Buttons(); got != 0xFF {
				t.Errorf("release %s: %02X, want FF", c.name, got)
			}
		})
	}
}

// Pressing a button is idempotent (a second press leaves the bit low),
// and releasing an already-released button leaves it high.
func TestSpecButtonIdempotent(t *testing.T) {
	m := New()
	m.SetButton(0, true)
	m.SetButton(0, true)
	if got := m.Buttons(); got != 0xFE {
		t.Errorf("double-press right: %02X, want FE", got)
	}
	m.SetButton(0, false)
	m.SetButton(0, false)
	if got := m.Buttons(); got != 0xFF {
		t.Errorf("double-release right: %02X, want FF", got)
	}
}

// Simultaneous buttons: each bit is independent and their cleared bits
// AND together. All three down = $F8.
func TestSpecSimultaneousButtons(t *testing.T) {
	m := New()
	m.SetButton(0, true) // right
	m.SetButton(1, true) // left
	m.SetButton(2, true) // middle
	if got := m.Buttons(); got != 0xF8 {
		t.Errorf("right+left+middle: %02X, want F8", got)
	}
	// Releasing only the middle button restores bit2, leaving F8|0x04 = FC.
	m.SetButton(2, false)
	if got := m.Buttons(); got != 0xFC {
		t.Errorf("right+left (middle released): %02X, want FC", got)
	}
	// Releasing left leaves only right pressed.
	m.SetButton(1, false)
	if got := m.Buttons(); got != 0xFE {
		t.Errorf("right only: %02X, want FE", got)
	}
}

// PINNED BEHAVIOUR: SetButton accepts bits 0..7 (the upper bits read
// back through the same byte). Out-of-range indices are ignored — they
// must never panic or corrupt other bits. The original spec only
// defines D0..D2; D3..D7 are unused/undefined on real hardware, so we
// pin that our implementation leaves them high at rest and lets the
// caller drive them without side effects.
func TestSpecButtonOutOfRangeIgnored(t *testing.T) {
	m := New()
	m.SetButton(-1, true)
	m.SetButton(8, true)
	m.SetButton(99, true)
	if got := m.Buttons(); got != 0xFF {
		t.Errorf("out-of-range button presses: %02X, want FF (no effect)", got)
	}
}

// --- X / Y counters: increment, decrement, 8-bit wraparound -------

// Spec: X increments with positive host motion; the counter is 8-bit
// and only the low byte is observable.
func TestSpecXIncrementDecrement(t *testing.T) {
	m := New()
	m.Move(10, 0)
	if got := m.X(); got != 10 {
		t.Errorf("X after +10: %d, want 10", got)
	}
	m.Move(-4, 0)
	if got := m.X(); got != 6 {
		t.Errorf("X after -4: %d, want 6", got)
	}
}

// Spec: the X counter rolls over at $FF -> $00 and underflows
// $00 -> $FF, cyclically (8-bit wrap-around).
func TestSpecXWraparound(t *testing.T) {
	m := New()
	m.Move(0xFF, 0) // X = 0xFF
	if got := m.X(); got != 0xFF {
		t.Fatalf("X = %02X, want FF", got)
	}
	m.Move(1, 0) // 0xFF + 1 wraps to 0x00
	if got := m.X(); got != 0x00 {
		t.Errorf("X after wrap +1 from FF: %02X, want 00", got)
	}
	m.Move(-1, 0) // 0x00 - 1 underflows to 0xFF
	if got := m.X(); got != 0xFF {
		t.Errorf("X after underflow -1 from 00: %02X, want FF", got)
	}
	// A large positive delta wraps modulo 256.
	m.Reset()
	m.Move(300, 0) // 300 mod 256 = 44
	if got := m.X(); got != 44 {
		t.Errorf("X after +300: %d, want 44 (300 mod 256)", got)
	}
}

// PINNED BEHAVIOUR: Move inverts host-Y. The Kempston convention is
// "+Y = screen up" whereas host event deltas use "+Y = screen down",
// so Move(_, dy) subtracts dy from the Y counter. The spec is silent
// on host-event polarity (that's a host-binding choice), so this is
// pinned, not treated as a hardware fact.
func TestSpecYInverted(t *testing.T) {
	m := New()
	m.Move(0, 3) // host +3 down -> Kempston Y -3
	if got := m.Y(); got != 253 {
		t.Errorf("Y after host +3: %d, want 253 (0-3 mod 256, inverted)", got)
	}
	m.Move(0, -10) // host -10 up -> Kempston Y +10
	if got := m.Y(); got != 7 {
		t.Errorf("Y after host -10: %d, want 7 (253+10 mod 256)", got)
	}
}

// Spec: the Y counter is an independent 8-bit wrap-around counter, just
// like X (verified through the inversion).
func TestSpecYWraparound(t *testing.T) {
	m := New()
	// Drive Kempston-Y up to 0xFF: host-down by 1 gives Kempston-Y 0xFF
	// (0 - 1). Then host-down by 1 more wraps the counter 0xFF -> 0xFE,
	// i.e. it keeps decrementing cyclically.
	m.Move(0, 1)
	if got := m.Y(); got != 0xFF {
		t.Fatalf("Y after host +1: %02X, want FF", got)
	}
	// Bring Kempston-Y back to 0 then over the top the other way.
	m.Move(0, -1) // Kempston-Y +1 -> 0x00
	if got := m.Y(); got != 0x00 {
		t.Fatalf("Y back to 0: %02X, want 00", got)
	}
	m.Move(0, -1) // Kempston-Y +1 -> 0x01
	if got := m.Y(); got != 0x01 {
		t.Errorf("Y wrap test: %02X, want 01", got)
	}
}

// X and Y counters are fully independent: moving one never disturbs the
// other.
func TestSpecXYIndependent(t *testing.T) {
	m := New()
	m.Move(20, 0)
	if m.Y() != 0 {
		t.Errorf("X move disturbed Y: Y=%d, want 0", m.Y())
	}
	m.Move(0, 7)
	if m.X() != 20 {
		t.Errorf("Y move disturbed X: X=%d, want 20", m.X())
	}
}

// --- No wheel: the original Kempston interface has no scroll wheel --

// PINNED BEHAVIOUR: the canonical Kempston mouse has no scroll wheel.
// The buttons byte carries only D0..D2; we deliberately do NOT pack a
// wheel value into the high nibble of $FADF. This pins that an idle
// mouse reads $FF (no wheel bits set) so a later "add wheel" change is
// a deliberate, tested decision.
func TestSpecNoWheelInButtonsHighNibble(t *testing.T) {
	m := New()
	v, ok := m.HandlePortRead(0xFADF)
	if !ok || v != 0xFF {
		t.Errorf("$FADF idle: %02X ok=%v, want FF true (no wheel nibble modelled)", v, ok)
	}
	// High nibble stays $F (all ones) regardless of button presses.
	m.SetButton(0, true)
	m.SetButton(1, true)
	m.SetButton(2, true)
	if got := m.Buttons() & 0xF0; got != 0xF0 {
		t.Errorf("buttons high nibble = %02X with all buttons down, want F0 (no wheel)", got)
	}
}

// --- Reset --------------------------------------------------------

// Reset restores the power-on state: counters 0, buttons $FF.
func TestSpecResetPowerOnState(t *testing.T) {
	m := New()
	m.Move(123, 45)
	m.SetButton(0, true)
	m.SetButton(2, true)
	m.Reset()
	if m.X() != 0 || m.Y() != 0 || m.Buttons() != 0xFF {
		t.Errorf("after Reset: x=%d y=%d buttons=%02X, want 0 0 FF",
			m.X(), m.Y(), m.Buttons())
	}
	// Ports reflect the reset state too.
	if v, _ := m.HandlePortRead(0xFADF); v != 0xFF {
		t.Errorf("$FADF after reset: %02X, want FF", v)
	}
}
