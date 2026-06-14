package main

import (
	"fmt"
	"strings"
)

// crashKind labels which heuristic fired.
type crashKind string

const (
	crashNopSlide  crashKind = "nop-slide"
	crashSPLow     crashKind = "sp-low"
	crashSPHigh    crashKind = "sp-high"
	crashPCRegion  crashKind = "pc-region"
	crashHaltNoIFF crashKind = "halt-no-iff"
)

// crashRegion: forbidden PC range with a human-readable label.
type crashRegion struct {
	name   string
	lo, hi uint16
}

// crashDetectorConfig wires up which heuristics are active. Zero
// values disable the corresponding heuristic.
type crashDetectorConfig struct {
	nopSlideMin int           // ≥1 → fire after N consecutive $00 fetches
	spLow       uint16        // SP < spLow → fire (0 = disabled)
	spHigh      uint16        // SP > spHigh → fire (0 = disabled)
	pcRegions   []crashRegion // PC entering any of these → fire
	haltNoIFF   bool          // HALT with IFF1=0 → fire (true deadlock)
}

// enabled reports whether any heuristic is active.
func (c crashDetectorConfig) enabled() bool {
	return c.nopSlideMin > 0 ||
		c.spLow > 0 ||
		c.spHigh > 0 ||
		len(c.pcRegions) > 0 ||
		c.haltNoIFF
}

// crashEnv abstracts the CPU+Mem state the detector reads at each
// pre-fetch. Using function values keeps the detector unit-testable
// without needing a real CPU/Memory pair.
type crashEnv struct {
	Read   func(addr uint16) byte
	SP     func() uint16
	Halted func() bool
	IFF1   func() bool
}

// crashDetector ticks each pre-fetch. It fires onCrash exactly once
// per kind per re-arm window — when the failing condition clears
// (NOP run breaks, SP returns to bounds, PC leaves region, HALT
// cleared) the heuristic re-arms.
type crashDetector struct {
	cfg     crashDetectorConfig
	env     crashEnv
	onCrash func(kind crashKind, pc uint16, detail string)

	nopRun int
	armed  map[crashKind]bool
}

// newCrashDetector returns nil when cfg disables every heuristic so
// callers can skip installing the hook entirely.
func newCrashDetector(
	cfg crashDetectorConfig,
	env crashEnv,
	on func(crashKind, uint16, string),
) *crashDetector {
	if !cfg.enabled() {
		return nil
	}
	d := &crashDetector{
		cfg:     cfg,
		env:     env,
		onCrash: on,
		armed:   map[crashKind]bool{},
	}
	for _, k := range []crashKind{crashNopSlide, crashSPLow, crashSPHigh, crashPCRegion, crashHaltNoIFF} {
		d.armed[k] = true
	}
	return d
}

// Step is the PreFetchHook callback — called before every M1.
func (d *crashDetector) Step(pc uint16) {
	if d.cfg.nopSlideMin > 0 {
		if d.env.Read(pc) == 0x00 {
			d.nopRun++
			if d.armed[crashNopSlide] && d.nopRun >= d.cfg.nopSlideMin {
				d.fire(crashNopSlide, pc, fmt.Sprintf("%d consecutive NOPs", d.nopRun))
			}
		} else {
			d.nopRun = 0
			d.armed[crashNopSlide] = true
		}
	}

	sp := d.env.SP()
	if d.cfg.spLow > 0 {
		if sp < d.cfg.spLow {
			if d.armed[crashSPLow] {
				d.fire(crashSPLow, pc, fmt.Sprintf("SP=$%04X below $%04X", sp, d.cfg.spLow))
			}
		} else {
			d.armed[crashSPLow] = true
		}
	}
	if d.cfg.spHigh > 0 {
		if sp > d.cfg.spHigh {
			if d.armed[crashSPHigh] {
				d.fire(crashSPHigh, pc, fmt.Sprintf("SP=$%04X above $%04X", sp, d.cfg.spHigh))
			}
		} else {
			d.armed[crashSPHigh] = true
		}
	}

	if len(d.cfg.pcRegions) > 0 {
		hit := false
		for _, r := range d.cfg.pcRegions {
			if pc >= r.lo && pc <= r.hi {
				hit = true
				if d.armed[crashPCRegion] {
					d.fire(crashPCRegion, pc, fmt.Sprintf("PC in %s ($%04X-$%04X)", r.name, r.lo, r.hi))
				}
				break
			}
		}
		if !hit {
			d.armed[crashPCRegion] = true
		}
	}

	if d.cfg.haltNoIFF {
		if d.env.Halted() && !d.env.IFF1() {
			if d.armed[crashHaltNoIFF] {
				d.fire(crashHaltNoIFF, pc, "HALT with IFF1=0 (only NMI can wake)")
			}
		} else {
			d.armed[crashHaltNoIFF] = true
		}
	}
}

func (d *crashDetector) fire(kind crashKind, pc uint16, detail string) {
	d.armed[kind] = false
	d.onCrash(kind, pc, detail)
}

// parseCrashRegions decodes "NAME@LO-HI,NAME@LO-HI". Hex defaults
// for the address part — `$` and `0x` prefixes also accepted.
// Unparseable entries land in the errors slice; the caller is
// expected to log via slog.Warn.
func parseCrashRegions(spec string) ([]crashRegion, []string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []crashRegion
	var errs []string
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		at := strings.Index(raw, "@")
		if at < 0 {
			errs = append(errs, fmt.Sprintf("crash-pc-region %q: missing @", raw))
			continue
		}
		name := strings.TrimSpace(raw[:at])
		rng := strings.TrimSpace(raw[at+1:])
		dash := strings.Index(rng, "-")
		if dash < 0 {
			errs = append(errs, fmt.Sprintf("crash-pc-region %q: range needs LO-HI", raw))
			continue
		}
		lo, err1 := parseHex(strings.TrimSpace(rng[:dash]))
		hi, err2 := parseHex(strings.TrimSpace(rng[dash+1:]))
		if err1 != nil || err2 != nil {
			errs = append(errs, fmt.Sprintf("crash-pc-region %q: bad hex", raw))
			continue
		}
		if hi < lo {
			lo, hi = hi, lo
		}
		out = append(out, crashRegion{name: name, lo: lo, hi: hi})
	}
	return out, errs
}

// defaultCrashConfig is what `--crash-detect` (with no per-heuristic
// flags) turns on. Conservative — pc-region and sp-high stay
// opt-in to avoid Next false positives (Layer 2 framebuffer reads
// look like screen-RAM reads; some routines legitimately push past
// the high SP).
func defaultCrashConfig() crashDetectorConfig {
	return crashDetectorConfig{
		nopSlideMin: 32,
		spLow:       0x4000,
		haltNoIFF:   true,
	}
}

// crashConfigFromFlags merges the per-heuristic CLI flags with the
// --crash-detect default. Per-heuristic flags always win over the
// default value; --crash-halt-no-iff-off forces the halt heuristic
// off even when the default would enable it.
func crashConfigFromFlags(f *cliFlags) crashDetectorConfig {
	var cfg crashDetectorConfig
	if f.crashDetect {
		cfg = defaultCrashConfig()
	}
	if f.crashNopSlide > 0 {
		cfg.nopSlideMin = f.crashNopSlide
	}
	if f.crashSPLow > 0 {
		cfg.spLow = f.crashSPLow
	}
	if f.crashSPHigh > 0 {
		cfg.spHigh = f.crashSPHigh
	}
	if len(f.crashPCRegions) > 0 {
		cfg.pcRegions = f.crashPCRegions
	}
	if f.crashHaltNoIFF {
		cfg.haltNoIFF = true
	}
	if f.crashHaltNoIFFOff {
		cfg.haltNoIFF = false
	}
	return cfg
}
