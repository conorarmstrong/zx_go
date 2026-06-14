package main

import (
	"reflect"
	"testing"
)

// testEnv builds a crashEnv backed by mutable test state.
type testEnv struct {
	mem    map[uint16]byte
	sp     uint16
	halted bool
	iff1   bool
}

func (t *testEnv) env() crashEnv {
	return crashEnv{
		Read:   func(a uint16) byte { return t.mem[a] },
		SP:     func() uint16 { return t.sp },
		Halted: func() bool { return t.halted },
		IFF1:   func() bool { return t.iff1 },
	}
}

type fired struct {
	kind   crashKind
	pc     uint16
	detail string
}

func collect() (*[]fired, func(crashKind, uint16, string)) {
	got := &[]fired{}
	return got, func(k crashKind, pc uint16, d string) {
		*got = append(*got, fired{k, pc, d})
	}
}

func TestCrashDetectorDisabledReturnsNil(t *testing.T) {
	got, on := collect()
	cd := newCrashDetector(crashDetectorConfig{}, crashEnv{}, on)
	if cd != nil {
		t.Fatalf("expected nil detector for empty config, got %#v", cd)
	}
	if len(*got) != 0 {
		t.Fatalf("expected no fires, got %d", len(*got))
	}
}

func TestCrashDetectorNopSlide(t *testing.T) {
	te := &testEnv{mem: map[uint16]byte{}}
	// $1000..$100F filled with NOPs ($00).
	for i := uint16(0x1000); i < 0x1010; i++ {
		te.mem[i] = 0x00
	}
	got, on := collect()
	cd := newCrashDetector(crashDetectorConfig{nopSlideMin: 4}, te.env(), on)
	if cd == nil {
		t.Fatal("expected detector")
	}

	// 3 NOPs: should not fire.
	cd.Step(0x1000)
	cd.Step(0x1001)
	cd.Step(0x1002)
	if len(*got) != 0 {
		t.Fatalf("expected no fires at 3 NOPs, got %v", *got)
	}
	// 4th NOP: fires.
	cd.Step(0x1003)
	if len(*got) != 1 || (*got)[0].kind != crashNopSlide {
		t.Fatalf("expected nop-slide fire at 4, got %v", *got)
	}
	// 5th NOP: re-armed only if non-zero seen, so should NOT fire again.
	cd.Step(0x1004)
	if len(*got) != 1 {
		t.Fatalf("expected exactly 1 fire while sliding, got %v", *got)
	}
	// Non-zero opcode breaks the run.
	te.mem[0x1005] = 0xC3 // JP
	cd.Step(0x1005)
	if len(*got) != 1 {
		t.Fatalf("non-NOP should not fire, got %v", *got)
	}
	// Back into NOPs.
	cd.Step(0x1006)
	cd.Step(0x1007)
	cd.Step(0x1008)
	cd.Step(0x1009)
	if len(*got) != 2 || (*got)[1].kind != crashNopSlide {
		t.Fatalf("expected re-armed fire on second slide, got %v", *got)
	}
}

func TestCrashDetectorSPLow(t *testing.T) {
	te := &testEnv{mem: map[uint16]byte{0x1000: 0xC3}, sp: 0x8000}
	got, on := collect()
	cd := newCrashDetector(crashDetectorConfig{spLow: 0x4000}, te.env(), on)
	if cd == nil {
		t.Fatal("expected detector")
	}

	// SP in range — no fire.
	cd.Step(0x1000)
	if len(*got) != 0 {
		t.Fatalf("expected no fire above sp-low, got %v", *got)
	}
	// SP drops below — fires once.
	te.sp = 0x0100
	cd.Step(0x1000)
	cd.Step(0x1000)
	if len(*got) != 1 || (*got)[0].kind != crashSPLow {
		t.Fatalf("expected one sp-low fire, got %v", *got)
	}
	// SP returns to range — re-arm.
	te.sp = 0x8000
	cd.Step(0x1000)
	te.sp = 0x0100
	cd.Step(0x1000)
	if len(*got) != 2 {
		t.Fatalf("expected re-armed fire, got %v", *got)
	}
}

func TestCrashDetectorSPHigh(t *testing.T) {
	te := &testEnv{mem: map[uint16]byte{0x1000: 0xC3}, sp: 0x8000}
	got, on := collect()
	cd := newCrashDetector(crashDetectorConfig{spHigh: 0xFF00}, te.env(), on)
	if cd == nil {
		t.Fatal("expected detector")
	}

	cd.Step(0x1000)
	if len(*got) != 0 {
		t.Fatalf("no fire below threshold, got %v", *got)
	}
	te.sp = 0xFFFE
	cd.Step(0x1000)
	if len(*got) != 1 || (*got)[0].kind != crashSPHigh {
		t.Fatalf("expected sp-high fire, got %v", *got)
	}
}

func TestCrashDetectorPCRegion(t *testing.T) {
	te := &testEnv{mem: map[uint16]byte{0x1000: 0xC3, 0x4000: 0xC3}}
	got, on := collect()
	cd := newCrashDetector(crashDetectorConfig{
		pcRegions: []crashRegion{{name: "screen", lo: 0x4000, hi: 0x57FF}},
	}, te.env(), on)
	if cd == nil {
		t.Fatal("expected detector")
	}

	cd.Step(0x1000)
	if len(*got) != 0 {
		t.Fatalf("PC outside region should not fire, got %v", *got)
	}
	cd.Step(0x4000)
	if len(*got) != 1 || (*got)[0].kind != crashPCRegion {
		t.Fatalf("PC in region should fire, got %v", *got)
	}
	cd.Step(0x4001)
	if len(*got) != 1 {
		t.Fatalf("still inside region, should not refire, got %v", *got)
	}
	cd.Step(0x1000) // leave region — re-arm.
	cd.Step(0x4002)
	if len(*got) != 2 {
		t.Fatalf("expected refire after re-arm, got %v", *got)
	}
}

func TestCrashDetectorHaltNoIFF(t *testing.T) {
	te := &testEnv{mem: map[uint16]byte{0x1000: 0xC3}}
	got, on := collect()
	cd := newCrashDetector(crashDetectorConfig{haltNoIFF: true}, te.env(), on)
	if cd == nil {
		t.Fatal("expected detector")
	}

	// Not halted — no fire.
	cd.Step(0x1000)
	// HALT with IFF1 set — NOT a deadlock (IRQ can wake).
	te.halted = true
	te.iff1 = true
	cd.Step(0x1000)
	if len(*got) != 0 {
		t.Fatalf("HALT with IFF1=1 should not fire, got %v", *got)
	}
	// HALT with IFF1=0 — deadlock.
	te.iff1 = false
	cd.Step(0x1000)
	if len(*got) != 1 || (*got)[0].kind != crashHaltNoIFF {
		t.Fatalf("HALT IFF1=0 should fire, got %v", *got)
	}
	// Still halted same way — no refire.
	cd.Step(0x1000)
	if len(*got) != 1 {
		t.Fatalf("no refire while still halted, got %v", *got)
	}
	// Cleared — re-arm.
	te.halted = false
	cd.Step(0x1000)
	te.halted = true
	cd.Step(0x1000)
	if len(*got) != 2 {
		t.Fatalf("expected refire after re-arm, got %v", *got)
	}
}

func TestParseCrashRegions(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []crashRegion
		wantErr int
	}{
		{"empty", "", nil, 0},
		{"single", "screen@4000-57FF",
			[]crashRegion{{"screen", 0x4000, 0x57FF}}, 0},
		{"dollar prefix", "scr@$4000-$57FF",
			[]crashRegion{{"scr", 0x4000, 0x57FF}}, 0},
		{"two entries", "scr@4000-57FF,attr@5800-5AFF",
			[]crashRegion{
				{"scr", 0x4000, 0x57FF},
				{"attr", 0x5800, 0x5AFF},
			}, 0},
		{"swapped order", "rev@57FF-4000",
			[]crashRegion{{"rev", 0x4000, 0x57FF}}, 0},
		{"missing @", "bad", nil, 1},
		{"missing range", "name@4000", nil, 1},
		{"bad hex", "name@ZZ-AA", nil, 1},
		{"mixed good+bad", "ok@1000-2000,bad",
			[]crashRegion{{"ok", 0x1000, 0x2000}}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, errs := parseCrashRegions(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("regions: got %v want %v", got, c.want)
			}
			if len(errs) != c.wantErr {
				t.Errorf("errors: got %d (%v) want %d", len(errs), errs, c.wantErr)
			}
		})
	}
}

func TestDefaultCrashConfig(t *testing.T) {
	c := defaultCrashConfig()
	if !c.enabled() {
		t.Error("default should be enabled")
	}
	if c.nopSlideMin == 0 || c.spLow == 0 || !c.haltNoIFF {
		t.Errorf("default missing core heuristics: %#v", c)
	}
	if c.spHigh != 0 || len(c.pcRegions) != 0 {
		t.Errorf("default should leave sp-high/pc-region opt-in: %#v", c)
	}
}

func TestCrashConfigFromFlags(t *testing.T) {
	t.Run("master flag alone uses defaults", func(t *testing.T) {
		c := crashConfigFromFlags(&cliFlags{crashDetect: true})
		want := defaultCrashConfig()
		if !reflect.DeepEqual(c, want) {
			t.Errorf("got %#v want %#v", c, want)
		}
	})
	t.Run("per-heuristic overrides default", func(t *testing.T) {
		c := crashConfigFromFlags(&cliFlags{
			crashDetect:   true,
			crashNopSlide: 128,
		})
		if c.nopSlideMin != 128 {
			t.Errorf("override failed: got %d", c.nopSlideMin)
		}
		if c.spLow != 0x4000 {
			t.Errorf("non-overridden default lost: spLow=%04X", c.spLow)
		}
	})
	t.Run("per-heuristic without master", func(t *testing.T) {
		c := crashConfigFromFlags(&cliFlags{crashHaltNoIFF: true})
		if !c.haltNoIFF {
			t.Errorf("halt heuristic should be on")
		}
		if c.nopSlideMin != 0 {
			t.Errorf("nopSlide should stay off: %d", c.nopSlideMin)
		}
	})
	t.Run("halt-off forces off even with master", func(t *testing.T) {
		c := crashConfigFromFlags(&cliFlags{
			crashDetect:       true,
			crashHaltNoIFFOff: true,
		})
		if c.haltNoIFF {
			t.Errorf("halt-off should override default-on")
		}
	})
	t.Run("empty flags → disabled", func(t *testing.T) {
		c := crashConfigFromFlags(&cliFlags{})
		if c.enabled() {
			t.Errorf("no flags should be disabled: %#v", c)
		}
	})
}
