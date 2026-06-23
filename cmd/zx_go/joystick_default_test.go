package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestEffectiveJoystickFor locks in the rule that an unconfigured joystick on
// the Spectrum Next falls back to Kempston. The Next FPGA always decodes the
// Kempston port ($1F) and nearly all Next software (e.g. Sonic) reads it, so
// without this fallback a fresh user's arrow keys drive nothing and the game
// is uncontrollable. Other models, and any explicit choice, are untouched.
func TestEffectiveJoystickFor(t *testing.T) {
	cases := []struct {
		name       string
		configured JoystickType
		model      roms.SpectrumModel
		want       JoystickType
	}{
		{"Next + unconfigured -> Kempston", JoystickNone, roms.ModelNext, JoystickKempston},
		{"Next + explicit Kempston stays", JoystickKempston, roms.ModelNext, JoystickKempston},
		{"Next + explicit Sinclair stays", JoystickSinclair1, roms.ModelNext, JoystickSinclair1},
		{"48K + unconfigured stays None", JoystickNone, roms.Model48K, JoystickNone},
		{"48K + Kempston stays", JoystickKempston, roms.Model48K, JoystickKempston},
		{"128K + unconfigured stays None", JoystickNone, roms.Model128K, JoystickNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveJoystickFor(c.configured, c.model); got != c.want {
				t.Fatalf("effectiveJoystickFor(%v, %v) = %v, want %v", c.configured, c.model, got, c.want)
			}
		})
	}
}
