package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func newTestULAForModel(t *testing.T, model roms.SpectrumModel, dir string) *ULA {
	t.Helper()
	createTestROMs(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })
	mem, err := memory.New(dir, model)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	return New(mem, keyboard.New())
}

// Port $FF is the Timex SCLD video-mode register. A Sinclair 48K or
// 128K has no SCLD: the ULA drives $FF as the floating bus and a write
// to it does nothing. We stored every $FF write regardless, so an
// ordinary `OUT (C),r` with C = $FF from a Spectrum program switched
// the emulator into a 640-wide Timex frame.
func TestPortFFIsIgnoredOnSinclairModels(t *testing.T) {
	for _, tc := range []struct {
		model roms.SpectrumModel
		dir   string
	}{
		{roms.Model48K, "test_roms_ula_timex48"},
		{roms.Model128K, "test_roms_ula_timex128"},
		{roms.ModelPlus3, "test_roms_ula_timexp3"},
	} {
		u := newTestULAForModel(t, tc.model, tc.dir)
		u.WritePort(0x00FF, 0x06) // hi-res select
		if u.timexHiResActive() {
			t.Errorf("model %v: port $FF put the ULA into Timex hi-res", tc.model)
		}
		if got := u.TimexScreenMode(); got != 0 {
			t.Errorf("model %v: TimexScreenMode = %#02x, want 0", tc.model, got)
		}
	}
}

// The Next does decode the SCLD register: NextZXOS's 64/85-column text
// modes depend on it.
func TestPortFFDrivesTheSCLDOnTheNext(t *testing.T) {
	u := newTestULAForModel(t, roms.ModelNext, "test_roms_ula_timexnext")
	u.WritePort(0x00FF, 0x06)
	if !u.timexHiResActive() {
		t.Error("Next: port $FF did not select Timex hi-res")
	}
}

// Reset must drop the SCLD mode with everything else, or a reboot out
// of a 64-column NextZXOS screen keeps drawing a 640-wide scrambled
// picture until something writes $FF again.
func TestResetClearsTheTimexMode(t *testing.T) {
	u := newTestULAForModel(t, roms.ModelNext, "test_roms_ula_timexreset")
	u.WritePort(0x00FF, 0x06)
	if !u.timexHiResActive() {
		t.Fatal("setup: hi-res was not selected")
	}
	u.Reset()
	if u.timexHiResActive() {
		t.Error("Timex hi-res still active after Reset")
	}
	if got := u.TimexScreenMode(); got != 0 {
		t.Errorf("TimexScreenMode after Reset = %#02x, want 0", got)
	}
}
