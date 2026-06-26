package ay

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestYM2149MatchesFPGAGolden replays the YM2149 / AY-3-8910 golden — captured
// from the real FPGA core audio/ym2149.vhd simulated under GHDL — through the
// AY public API and asserts the per-channel digital DAC output matches the
// hardware exactly, tick for tick. Tooling: _tools/ym2149-vhdl-test/.
//
// The golden is a stream of ops:
//
//	; <label>          sequence comment
//	RESET              full chip reset
//	AYMODE <0|1>       0 = YM (32-entry DAC), 1 = AY (16-entry DAC)
//	R <reg> <val>      WriteRegister(reg, val)
//	S a b c            sample: the three O_AUDIO_A/B/C bytes for the active mode,
//	                   captured by the FPGA AFTER advancing one tone tick (one
//	                   ena_div / one StepTick()).
//
// Each S therefore corresponds to exactly one StepTick() of advance followed by
// reading ChannelDAC for all three channels — a tick-for-tick lockstep with the
// hardware's tone/noise/envelope generators, mixer and DAC volume tables.
//
// One chip is run continuously across ALL sequences: a RESET op maps to
// ResetRegisters (the faithful RESET_H — clears the registers but NOT the
// noise LFSR / tone phase / envelope state, exactly as the FPGA's reset). This
// matters because the FPGA's noise LFSR keeps shifting through every sequence
// and a reset does not re-seed it, so the noise phase entering each sequence is
// a deterministic function of all preceding ticks.
//
// Volume-scale note: this asserts the FPGA's TRUE 8-bit per-channel DAC output
// (volTableYm / volTableAy, copied verbatim from the core). The separate int16
// mixing path (GenerateSamples / MixInto, see volumeTable) keeps its own
// beeper-headroom scale and is intentionally NOT what is compared here — the
// hardware-faithful contract is the digital channel level, not the mixed
// analog-scaled stream.
func TestYM2149MatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/ym2149_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	// One continuously-running chip across all sequences (see the doc comment).
	a := New()
	aymode := false
	label := ""

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }

	sc := bufio.NewScanner(f)
	ln, samples := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, ";") {
			label = strings.TrimSpace(line[1:])
			continue
		}
		fld := strings.Fields(line)
		switch fld[0] {
		case "RESET":
			a.ResetRegisters()
		case "AYMODE":
			aymode = atoi(fld[1]) != 0
		case "R":
			a.WriteRegister(byte(atoi(fld[1])), byte(atoi(fld[2])))
		case "S":
			wantA, wantB, wantC := byte(atoi(fld[1])), byte(atoi(fld[2])), byte(atoi(fld[3]))
			a.StepTick()
			gotA := a.ChannelDAC(0, aymode)
			gotB := a.ChannelDAC(1, aymode)
			gotC := a.ChannelDAC(2, aymode)
			if gotA != wantA || gotB != wantB || gotC != wantC {
				t.Errorf("line %d [%s] sample %d: got A=%d B=%d C=%d; want A=%d B=%d C=%d",
					ln, label, samples, gotA, gotB, gotC, wantA, wantB, wantC)
			}
			samples++
		default:
			t.Fatalf("line %d: unknown golden op %q", ln, fld[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if samples < 1500 {
		t.Fatalf("expected the full golden replay, only saw %d samples", samples)
	}
}
