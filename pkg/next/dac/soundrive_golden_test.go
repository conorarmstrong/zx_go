package dac

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestSounDriveMatchesFPGAGolden replays the FPGA SounDrive golden — captured
// from the real audio/soundrive.vhd plus the zxnext.vhd mode-dependent port
// decode under GHDL (tooling: _tools/dac-vhdl-test/) — through SounDrive and
// asserts the 9-bit stereo pcm_L/pcm_R match across all DAC modes (sd1, sd2,
// stereo A/D, stereo B/C, mono) and the DAC-enable gate.
func TestSounDriveMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/dac_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	field := func(fl []string, k string) string {
		for _, x := range fl {
			if strings.HasPrefix(x, k+"=") {
				return x[len(k)+1:]
			}
		}
		return ""
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }

	s := NewSounDrive()
	sc := bufio.NewScanner(f)
	ln, n := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "RESET" {
			s = NewSounDrive()
			continue
		}
		if !strings.HasPrefix(line, "SD ") {
			continue
		}
		fl := strings.Fields(line)
		mode := atoi(field(fl, "mode"))
		s.SD1, s.SD2, s.StereoAD, s.StereoBC = false, false, false, false
		s.MonoFB, s.MonoBC, s.MonoDF = false, false, false
		switch mode {
		case 1:
			s.SD1 = true
		case 2:
			s.SD2 = true
		case 3:
			s.StereoAD = true
		case 4:
			s.StereoBC = true
		case 5:
			s.MonoFB = true // pentagon FB → A,D
		case 6:
			s.MonoBC = true // GS covox B3 → B,C
		case 7:
			s.MonoDF = true // specdrum DF → A,D
		}
		s.DACEnable = field(fl, "dac_en") == "1"

		p := field(fl, "port")
		vv, _ := strconv.ParseUint(field(fl, "val"), 16, 16)
		switch {
		case p == "NR": // NextReg audio mirror write (mode 9), routed by label
			switch fl[1] {
			case "nr_mono":
				s.WriteNRMono(byte(vv))
			case "nr_left":
				s.WriteNRLeft(byte(vv))
			case "nr_right":
				s.WriteNRRight(byte(vv))
			default:
				t.Fatalf("line %d: unknown NR mirror label %q", ln, fl[1])
			}
		case p != "XX":
			pv, _ := strconv.ParseUint(p, 16, 16)
			s.WritePort(uint16(pv), byte(vv))
		}

		wantL, wantR := atoi(field(fl, "L")), atoi(field(fl, "R"))
		if int(s.PCM_L()) != wantL || int(s.PCM_R()) != wantR {
			t.Errorf("line %d: got L=%d R=%d, want L=%d R=%d\n  %q", ln, s.PCM_L(), s.PCM_R(), wantL, wantR, line)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 18 {
		t.Fatalf("expected the full SounDrive golden replay, only saw %d writes", n)
	}
}
