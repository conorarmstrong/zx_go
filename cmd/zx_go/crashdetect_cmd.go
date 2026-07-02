package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// cmdCrashDetect implements the live `crash-detect` debugger command.
// Subcommands:
//
//	crash-detect                — show current status
//	crash-detect on             — enable with default heuristics
//	crash-detect off            — disable + remove hook
//	crash-detect pause-on-fire  — toggle whether a fire pauses the CPU
//	crash-detect set KEY VALUE  — adjust an active heuristic
//
// Recognised KEYs for `set`:
//
//	nop-slide N              — set/clear NOP-slide threshold (0 = off)
//	sp-low $XXXX             — set/clear SP-low bound
//	sp-high $XXXX            — set/clear SP-high bound
//	halt-no-iff on|off       — toggle HALT-with-IFF1=0 heuristic
//	pc-region NAME@LO-HI     — add a forbidden PC range
//	pc-region clear          — drop all PC ranges
func (d *remoteDebugger) cmdCrashDetect(args []string) string {
	if len(args) == 0 {
		return "OK " + d.crashDetectStatus()
	}
	switch strings.ToLower(args[0]) {
	case "status":
		return "OK " + d.crashDetectStatus()
	case "on", "enable":
		return d.crashDetectEnable(defaultCrashConfig())
	case "off", "disable":
		return d.crashDetectDisable()
	case "pause-on-fire", "pause":
		// Toggle.
		now := !d.crashDetectPause.Load()
		d.crashDetectPause.Store(now)
		return fmt.Sprintf("OK crash-detect pause-on-fire=%v", now)
	case "set":
		return d.crashDetectSet(args[1:])
	}
	return "ERR crash-detect: unknown subcommand (try: on / off / status / pause-on-fire / set KEY VALUE)"
}

func (d *remoteDebugger) crashDetectStatus() string {
	if d.crashDetect == nil {
		return "crash-detect off"
	}
	c := d.crashDetect.cfg
	var b strings.Builder
	b.WriteString("crash-detect on:")
	if c.nopSlideMin > 0 {
		fmt.Fprintf(&b, " nop-slide=%d", c.nopSlideMin)
	}
	if c.spLow > 0 {
		fmt.Fprintf(&b, " sp-low=$%04X", c.spLow)
	}
	if c.spHigh > 0 {
		fmt.Fprintf(&b, " sp-high=$%04X", c.spHigh)
	}
	if c.haltNoIFF {
		b.WriteString(" halt-no-iff")
	}
	for _, r := range c.pcRegions {
		fmt.Fprintf(&b, " pc-region[%s=$%04X-$%04X]", r.name, r.lo, r.hi)
	}
	fmt.Fprintf(&b, " pause-on-fire=%v", d.crashDetectPause.Load())
	return b.String()
}

func (d *remoteDebugger) crashDetectEnable(cfg crashDetectorConfig) string {
	if !cfg.enabled() {
		return "ERR crash-detect: refusing to enable with empty config"
	}
	if d.crashDetect != nil {
		d.emu.cpu.RemovePreFetchHook("debug-crash-detector")
		d.crashDetect = nil
	}
	cpu := d.emu.cpu
	mem := d.emu.mem
	env := crashEnv{
		Read:   mem.Read,
		SP:     func() uint16 { return cpu.SP },
		Halted: func() bool { return cpu.Halted },
		IFF1:   func() bool { return cpu.IFF1 },
	}
	d.crashDetect = newCrashDetector(cfg, env, func(kind crashKind, pc uint16, detail string) {
		slog.Info("crash-detected",
			"kind", string(kind),
			"pc", annotateAddr(pc),
			"detail", detail,
			"insns", cpu.InstructionCount(),
		)
		if d.crashDetectPause.Load() {
			d.paused.Store(true)
		}
	})
	d.emu.cpu.AddPreFetchHook("debug-crash-detector", d.crashDetect.Step)
	return "OK " + d.crashDetectStatus()
}

func (d *remoteDebugger) crashDetectDisable() string {
	if d.crashDetect == nil {
		return "OK crash-detect already off"
	}
	d.emu.cpu.RemovePreFetchHook("debug-crash-detector")
	d.crashDetect = nil
	return "OK crash-detect off"
}

func (d *remoteDebugger) crashDetectSet(args []string) string {
	if len(args) < 2 {
		return "ERR crash-detect set: need KEY VALUE"
	}
	// Start from the current config (or default if uninitialised)
	// so set acts as a delta.
	var cfg crashDetectorConfig
	if d.crashDetect != nil {
		cfg = d.crashDetect.cfg
	}
	switch strings.ToLower(args[0]) {
	case "nop-slide":
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 {
			return fmt.Sprintf("ERR nop-slide: bad N %q", args[1])
		}
		cfg.nopSlideMin = n
	case "sp-low":
		v, err := parseHex(args[1])
		if err != nil {
			return fmt.Sprintf("ERR sp-low: bad hex %q", args[1])
		}
		cfg.spLow = v
	case "sp-high":
		v, err := parseHex(args[1])
		if err != nil {
			return fmt.Sprintf("ERR sp-high: bad hex %q", args[1])
		}
		cfg.spHigh = v
	case "halt-no-iff":
		switch strings.ToLower(args[1]) {
		case "on", "true", "1":
			cfg.haltNoIFF = true
		case "off", "false", "0":
			cfg.haltNoIFF = false
		default:
			return "ERR halt-no-iff: want on|off"
		}
	case "pc-region":
		if strings.EqualFold(args[1], "clear") {
			cfg.pcRegions = nil
		} else {
			regs, errs := parseCrashRegions(args[1])
			if len(errs) > 0 {
				return "ERR pc-region: " + strings.Join(errs, "; ")
			}
			cfg.pcRegions = append(cfg.pcRegions, regs...)
		}
	default:
		return "ERR crash-detect set: unknown KEY " + args[0]
	}
	if !cfg.enabled() {
		return d.crashDetectDisable()
	}
	return d.crashDetectEnable(cfg)
}
