package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// newTestULA constructs a minimal ULA for the RZX hook tests, reusing
// the test-ROM helpers from ula_test.go so we don't need a separate
// memory constructor.
func newTestULA(t *testing.T) *ULA {
	t.Helper()
	dir := "test_roms_ula_rzx"
	createTestROMs(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	kbd := keyboard.New()
	return New(mem, kbd)
}

// TestReadPortPlaybackHookOverridesPeripherals checks that when an RZX
// playback hook is installed, the byte it produces wins over whatever
// the keyboard / peripherals would have returned. This is the
// fundamental playback contract.
func TestReadPortPlaybackHookOverridesPeripherals(t *testing.T) {
	u := newTestULA(t)
	u.SetRZXPlaybackHook(func() (byte, bool) {
		return 0x42, true
	})
	val, handled := u.ReadPort(0x7FFE) // would normally hit keyboard
	if !handled {
		t.Error("playback hook: handled = false, want true")
	}
	if val != 0x42 {
		t.Errorf("playback hook: val = %02X, want 0x42", val)
	}
}

// TestReadPortPlaybackExhaustionFallsThrough verifies that an exhausted
// playback hook (ok=false) falls back to the normal peripheral path
// rather than returning garbage.
func TestReadPortPlaybackExhaustionFallsThrough(t *testing.T) {
	u := newTestULA(t)
	u.SetRZXPlaybackHook(func() (byte, bool) {
		return 0, false // exhausted
	})
	// Unhandled port → falling through the exhausted hook should
	// hit the floating-bus default of 0xFF, not whatever a stale
	// playback hook might leave behind. The handled bit is
	// intentionally ignored — the floating-bus path returns false
	// and that's fine.
	val, _ := u.ReadPort(0xFFFF)
	if val != 0xFF {
		t.Errorf("exhausted playback: val = %02X, want 0xFF (floating bus)", val)
	}
}

// TestReadPortRecordHookCapturesRealValue checks that the record hook
// observes the byte the real peripherals returned, not 0xFF or some
// stub. This is the fundamental recording contract.
func TestReadPortRecordHookCapturesRealValue(t *testing.T) {
	u := newTestULA(t)

	var captured []byte
	u.SetRZXRecordHook(func(b byte) {
		captured = append(captured, b)
	})

	// Three reads with different addresses; we only care that
	// each one ticks the record hook exactly once with the value
	// ReadPort returned.
	v1, _ := u.ReadPort(0xFFFE)
	v2, _ := u.ReadPort(0xFFFF)
	v3, _ := u.ReadPort(0x001F)

	if len(captured) != 3 {
		t.Fatalf("record hook fired %d times, want 3", len(captured))
	}
	if captured[0] != v1 || captured[1] != v2 || captured[2] != v3 {
		t.Errorf("captured = %v, want [%02X %02X %02X]", captured, v1, v2, v3)
	}
}

// TestReadPortHooksMutuallyExclusive sanity-checks that both hooks can
// be installed without one breaking the other (the RZX driver is
// supposed to use one OR the other, but the ULA shouldn't crash if
// they're both set during a transition).
func TestReadPortHooksMutuallyExclusive(t *testing.T) {
	u := newTestULA(t)
	u.SetRZXPlaybackHook(func() (byte, bool) { return 0xAA, true })
	var captured byte
	u.SetRZXRecordHook(func(b byte) { captured = b })

	val, _ := u.ReadPort(0xFFFE)
	if val != 0xAA {
		t.Errorf("playback wins: val = %02X, want 0xAA", val)
	}
	// When playback hook returns ok=true, ReadPort returns immediately
	// without reaching the record hook — verify that.
	if captured != 0 {
		t.Errorf("record hook should not fire during playback override; captured = %02X", captured)
	}
}

// TestSetRZXHooksClearable verifies that passing nil to the setters
// removes a previously installed hook.
func TestSetRZXHooksClearable(t *testing.T) {
	u := newTestULA(t)
	u.SetRZXPlaybackHook(func() (byte, bool) { return 0x99, true })
	u.SetRZXPlaybackHook(nil)

	val, _ := u.ReadPort(0xFFFF)
	if val == 0x99 {
		t.Errorf("after clearing hook: val = %02X, hook still active", val)
	}
}
