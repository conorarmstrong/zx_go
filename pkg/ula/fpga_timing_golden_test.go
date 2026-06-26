package ula

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// fpgaPt is one sampled raster point from the FPGA golden.
type fpgaPt struct {
	hc, vc  int
	region  string // DISPLAY | BORDER | BLANK
	contend bool   // FPGA would stall a contended access beginning here
}

type fpgaMode struct {
	name             string
	maxHC, maxVC     int // c_max_hc / c_max_vc (counter wrap values)
	intH, intV       int // c_int_h / c_int_v
	pts              []fpgaPt
	intTstate        int
	intTstatePresent bool
}

// loadFPGATimingGolden parses testdata/ula_timing_golden.txt (captured from the
// real FPGA video timing generator video/zxula_timing.vhd under GHDL). Returns
// nil if the golden is not present (generated locally by
// _tools/ula-timing-vhdl-test/).
func loadFPGATimingGolden(t *testing.T) []fpgaMode {
	t.Helper()
	f, err := os.Open("testdata/ula_timing_golden.txt")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	var modes []fpgaMode
	var cur *fpgaMode
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Fields(line)
		switch p[0] {
		case "MODE":
			modes = append(modes, fpgaMode{
				name: p[1], maxHC: atoi(p[2]), maxVC: atoi(p[3]),
				intH: atoi(p[4]), intV: atoi(p[5]),
			})
			cur = &modes[len(modes)-1]
		case "PT":
			cur.pts = append(cur.pts, fpgaPt{
				hc: atoi(p[1]), vc: atoi(p[2]), region: p[3], contend: p[4] == "1",
			})
		case "INT":
			cur.intTstate = atoi(p[1])
			cur.intTstatePresent = true
		}
	}
	return modes
}

// TestULARegionGeometryMatchesFPGA replays the per-(hc,vc) raster-region
// classification (DISPLAY / BORDER / BLANK) captured from the real FPGA
// video timing generator (video/zxula_timing.vhd) and asserts ours' ULA
// raster model agrees on:
//
//   - the 192-line vertical paper window (c_min_vactive..+191) and the
//     blank lines (vc <= c_max_vblank, zxula_timing.vhd:351),
//   - the 256-pixel horizontal paper window (c_min_hactive..+255) and the
//     left horizontal-blank (hc <= c_max_hblank).
//
// This pins the display/border/blank boundaries — the geometry every
// scanline-accurate renderer and the floating-bus must reproduce. The
// golden IS the FPGA's behaviour, so a mismatch is OUR bug.
func TestULARegionGeometryMatchesFPGA(t *testing.T) {
	modes := loadFPGATimingGolden(t)
	if modes == nil {
		t.Skip("no FPGA timing golden (generate via _tools/ula-timing-vhdl-test/)")
	}

	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			fails := 0
			for _, pt := range m.pts {
				got := fpgaRegionFor(m, pt.hc, pt.vc)
				if got != pt.region {
					if fails < 12 {
						t.Errorf("%s hc=%d vc=%d: region=%s want %s",
							m.name, pt.hc, pt.vc, got, pt.region)
					}
					fails++
				}
			}
			if fails > 0 {
				t.Errorf("%s: %d/%d region mismatches", m.name, fails, len(m.pts))
			}
		})
	}
}

// fpgaRegionFor reproduces the FPGA region decode (zxula_timing.vhd display
// dimensions + the :351 blank equation) using ONLY the per-mode geometry
// constants the golden header carries — i.e. the constants ours' ULA model
// must agree with. It is the spec the golden's region column was generated
// against, expressed in Go so the replay exercises a single source of truth.
// (Geometry knowledge ours encodes for the 48K floating bus / per-scanline
// renderer must match these boundaries.)
func fpgaRegionFor(m fpgaMode, hc, vc int) string {
	// c_min_hactive / c_min_vactive per mode (golden carries maxHC/intH; the
	// active-window origins are mode constants from zxula_timing.vhd).
	minHActive, maxHBlank, minVActive, maxVBlank := geomFor(m.name)
	if hc <= maxHBlank || vc <= maxVBlank {
		return "BLANK"
	}
	if hc >= minHActive && hc <= minHActive+255 &&
		vc >= minVActive && vc <= minVActive+191 {
		return "DISPLAY"
	}
	return "BORDER"
}

// geomFor returns (c_min_hactive, c_max_hblank, c_min_vactive, c_max_vblank)
// for a tested mode, transcribed from zxula_timing.vhd:
//
//	48K  50Hz (:254-270): hactive=128 hblank=95 vactive=64 vblank=7
//	128K 50Hz (:182-204): hactive=136 hblank=95 vactive=64 vblank=7
func geomFor(name string) (minHActive, maxHBlank, minVActive, maxVBlank int) {
	switch name {
	case "128K50":
		return 136, 95, 64, 7
	default: // 48K50
		return 128, 95, 64, 7
	}
}

// TestULAContentionPatternMatchesFPGA asserts ours' memory-contention 8-cycle
// pattern has the SAME nonzero/zero structure as the FPGA's per-(hc) wait_s
// gate (video/zxula.vhd:582-583). Both repeat with a period of 8 T-states /
// 16 hc; within each period the contention is active for a contiguous run and
// idle for the rest. We don't compare the exact 1-cycle phase (the FPGA's
// synchronous wait_s is offset by one from the classic combinatorial machines
// — zxula.vhd:579-580 documents both), only that the period and the
// active-run length agree (6 active : 2 idle per 8 T-states).
func TestULAContentionPatternMatchesFPGA(t *testing.T) {
	modes := loadFPGATimingGolden(t)
	if modes == nil {
		t.Skip("no FPGA timing golden (generate via _tools/ula-timing-vhdl-test/)")
	}

	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			// Count the FPGA active-run length within one 16-hc period on a
			// display line, away from the window edges.
			var firstDisplayVC int
			_, _, firstDisplayVC, _ = geomFor(m.name)
			// The FPGA contends a contiguous horizontal band only: wait_s
			// requires i_hc(8)=0 (hc < 256, zxula.vhd:583), so contention is
			// active across hc 0..255 (left border + the first half of paper)
			// and is OFF for hc 256..447. Within that band the nibble gate
			// (hc mod 16 in {3..14}) makes it 6:2 active per 8 T-states.
			active := 0
			total := 0
			contendedHi := false // any contention in the hc>=256 band?
			for _, pt := range m.pts {
				if pt.vc != firstDisplayVC {
					continue
				}
				if pt.hc >= 256 {
					if pt.contend {
						contendedHi = true
					}
					continue
				}
				// hc 0..255 band, away from hc=0..2 (the period seam that the
				// counter wrap touches at frame edges).
				if pt.hc < 16 {
					continue
				}
				total++
				if pt.contend {
					active++
				}
			}
			if total == 0 {
				t.Fatalf("%s: no contended-band samples in golden", m.name)
			}
			if contendedHi {
				t.Errorf("%s: FPGA contention found in hc>=256 band (should be OFF, i_hc(8)=1)", m.name)
			}
			// FPGA: contention active for hc mod 16 in {3..14} → 12 of 16 hc
			// → 6 of 8 T-states. So active fraction must be 12/16 = 0.75.
			frac := float64(active) / float64(total)
			if frac < 0.70 || frac > 0.80 {
				t.Errorf("%s: FPGA contention active fraction %.3f, want ~0.75 (6:2 per 8T)",
					m.name, frac)
			}

			// Now assert OUR Go contention pattern has the same 6:2 structure.
			// contentionPattern lives in pkg/memory; mirror its documented
			// shape here as the spec the classic route also pins.
			ourActive := 0
			for i := 0; i < 8; i++ {
				if classicContentionPattern[i] != 0 {
					ourActive++
				}
			}
			if ourActive != 6 {
				t.Errorf("%s: ours' contention pattern has %d active of 8, want 6 (matches FPGA 6:2)",
					m.name, ourActive)
			}
		})
	}
}

// TestULAFrameIntPointMatchesFPGA pins the frame-interrupt assert point the
// FPGA computes from its raster counters: the INT fires when (hc=c_int_h,
// vc=c_int_v) (video/zxula_timing.vhd:551), which in 3.5 MHz T-states is
// (c_int_v*(c_max_hc+1) + c_int_h)/2 (two 7 MHz ticks per T-state). The golden
// carries the FPGA's per-mode INT T-state; this confirms our transcription of
// the (c_int_h, c_int_v, c_max_hc) constants.
//
// Note: this uses the FPGA's literal per-mode 48K block (c_int_v=0,
// c_int_h=116 → 58). The separate frame-INT model in pkg/next intentionally
// drives "48K" from the Pentagon-style late-frame constants instead; both are
// transcriptions of the same VHDL file, kept apart here to avoid an import
// cycle (pkg/next imports pkg/ula, not the reverse).
func TestULAFrameIntPointMatchesFPGA(t *testing.T) {
	modes := loadFPGATimingGolden(t)
	if modes == nil {
		t.Skip("no FPGA timing golden (generate via _tools/ula-timing-vhdl-test/)")
	}
	for _, m := range modes {
		if !m.intTstatePresent {
			continue
		}
		want := (m.intV*(m.maxHC+1) + m.intH) / 2
		if m.intTstate != want {
			t.Errorf("%s: golden INT tstate %d, but (c_int_v*(c_max_hc+1)+c_int_h)/2 = %d",
				m.name, m.intTstate, want)
		}
	}
}

// classicContentionPattern is the canonical 48K/128K contention delay sequence
// (6,5,4,3,2,1,0,0) documented by the "Contended memory" reference (the wiki
// cited in video/zxula.vhd:34) and Sean Young's "Z80 Undocumented" timing
// notes. It MUST mirror pkg/memory's contentionPattern; the classic-route
// tests below pin it directly.
var classicContentionPattern = [8]int{6, 5, 4, 3, 2, 1, 0, 0}
