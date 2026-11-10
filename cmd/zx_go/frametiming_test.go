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

// TestFrameTStatesForModel60 pins the 60 Hz Next frame lengths. The
// selection comes from NR$05 bit 2; the emitter must use the same value
// for the CPU budget and the wall-clock frame period.
func TestFrameTStatesForModel60(t *testing.T) {
	for _, tc := range []struct {
		model roms.SpectrumModel
		want  int
	}{
		{roms.Model48K, 59136},
		{roms.Model128K, 60192},
		{roms.ModelPlus2, 60192},
		{roms.ModelPlus2A, 60192},
		{roms.ModelPlus3, 60192},
		{roms.ModelNext, 60192},
		{roms.ModelPentagon, 71680}, // no 60 Hz mode
	} {
		if got := frameTStatesForModel(tc.model, true); got != tc.want {
			t.Errorf("frameTStatesForModel(%v, 60Hz) = %d, want %d", tc.model, got, tc.want)
		}
	}
}
