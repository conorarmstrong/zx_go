package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestFrameTStatesForModel — timing.md rows D1/D2. The ULA frame length
// is model-specific: 48K = 69888 T-states; the 128K family (128K/+2/+2A/
// +3) and the Spectrum Next (which boots in +3/128K timing) = 70908. The
// headless + GUI ExecuteFrame loops must use the model-appropriate
// length so the Next maskable INT stays aligned with the FPGA and with
// StepInstructionWithIRQ's stepFrameBudget (70908).
func TestFrameTStatesForModel(t *testing.T) {
	cases := []struct {
		model roms.SpectrumModel
		want  int
	}{
		{roms.Model48K, 69888},
		{roms.Model128K, 70908},
		{roms.ModelPlus2, 70908},
		{roms.ModelPlus2A, 70908},
		{roms.ModelPlus3, 70908},
		{roms.ModelNext, 70908},
	}
	for _, c := range cases {
		if got := frameTStatesForModel(c.model); got != c.want {
			t.Errorf("frameTStatesForModel(%v) = %d, want %d", c.model, got, c.want)
		}
	}
}
