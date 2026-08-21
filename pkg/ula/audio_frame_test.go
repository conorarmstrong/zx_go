package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The audio mixer hard-coded a 48K frame of 69888 T-states. A 128K frame
// is 70908 and a Pentagon's is 71680, so 128K beeper music ran about
// 1.4% fast and every event in the last ~1020 T of each frame was
// dropped. On a Next at 28 MHz the CPU burns SpeedMultiplier times as
// many T-states per ULA frame, so most of the frame's events fell
// outside the window entirely.
func TestAudioFrameLengthFollowsTheModel(t *testing.T) {
	for _, tc := range []struct {
		model roms.SpectrumModel
		dir   string
		want  int
	}{
		{roms.Model48K, "test_roms_ula_af48", 69888},
		{roms.Model128K, "test_roms_ula_af128", 70908},
		{roms.ModelPlus3, "test_roms_ula_afp3", 70908},
		{roms.ModelPentagon, "test_roms_ula_afpent", 71680},
	} {
		u := newTestULAForModel(t, tc.model, tc.dir)
		if got := u.audioFrameTStates(); got != tc.want {
			t.Errorf("model %v: audio frame = %d T, want %d", tc.model, got, tc.want)
		}
	}
}

// At turbo the CPU accumulates SpeedMultiplier times as many T-states in
// one ULA frame, and the events are stamped on the CPU clock.
func TestAudioFrameLengthScalesWithTurbo(t *testing.T) {
	createTestROMs(t, "test_roms_ula_afturbo")
	t.Cleanup(func() { cleanupTestROMs("test_roms_ula_afturbo") })
	mem, err := memory.New("test_roms_ula_afturbo", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	mem.SpeedMultiplier = func() int { return 8 } // 28 MHz
	u := New(mem, keyboard.New())
	if got, want := u.audioFrameTStates(), 70908*8; got != want {
		t.Errorf("28 MHz audio frame = %d T, want %d", got, want)
	}
}
