package main

import "testing"

// fakeDivMMCPager satisfies the duck-typed pager interfaces that
// divmmcFlagsFrom probes (mirrors the real *divmmc.Pager surface).
type fakeDivMMCPager struct {
	paged, automap, mapram bool
	e3                     byte
}

func (f fakeDivMMCPager) IsPagedIn() bool      { return f.paged }
func (f fakeDivMMCPager) AutomapEnabled() bool { return f.automap }
func (f fakeDivMMCPager) MAPRAM() bool         { return f.mapram }
func (f fakeDivMMCPager) LastE3() byte         { return f.e3 }

// TestDivmmcFlagsFrom verifies the pager-flag extraction used to
// populate cpuState for `if dmmc`/`automap`/`mapram`/`conmem` guards.
// conmem is E3 bit 7. A nil pager yields all-false (classic models).
func TestDivmmcFlagsFrom(t *testing.T) {
	if p, a, m, c := divmmcFlagsFrom(nil); p || a || m || c {
		t.Errorf("nil pager: got paged=%v automap=%v mapram=%v conmem=%v, want all false", p, a, m, c)
	}
	p, a, m, c := divmmcFlagsFrom(fakeDivMMCPager{paged: true, automap: true, mapram: false, e3: 0x80})
	if !p || !a || m || !c {
		t.Errorf("got paged=%v automap=%v mapram=%v conmem=%v; want true,true,false,true", p, a, m, c)
	}
	if _, _, _, c := divmmcFlagsFrom(fakeDivMMCPager{e3: 0x00}); c {
		t.Error("conmem should be false when E3 bit 7 clear")
	}
}

// TestCPUStateDMMCFlags verifies cpuState exposes the divMMC paging
// state through the condition-evaluator Reg() interface, so a
// breakpoint guard like `if dmmc == 1` resolves against live paging.
// These flags are what get-divmmc reports; surfacing them in the
// condition grammar is the faithful fix for low-PC bank aliasing.
func TestCPUStateDMMCFlags(t *testing.T) {
	cases := []struct {
		name string
		st   cpuState
		reg  string
		want int64
	}{
		{"paged-true", cpuState{dmmcPaged: true}, "dmmc", 1},
		{"paged-false", cpuState{}, "dmmc", 0},
		{"automap", cpuState{dmmcAutomap: true}, "automap", 1},
		{"mapram", cpuState{dmmcMapram: true}, "mapram", 1},
		{"conmem", cpuState{dmmcConmem: true}, "conmem", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.st.Reg(c.reg)
			if !ok {
				t.Fatalf("Reg(%q) returned ok=false", c.reg)
			}
			if got != c.want {
				t.Errorf("Reg(%q) = %d, want %d", c.reg, got, c.want)
			}
		})
	}
}
