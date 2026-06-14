package main

import (
	"fmt"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"image/png"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// runHeadless boots the requested model, runs for f.frames
// simulated frames (or forever if 0), then either dumps state
// (--dump-state) or exits cleanly. Skips the Fyne UI entirely
// — no window, no audio output device, no key wait. Useful for
// the reference emulator comparison runs, CI regression checks, and one-shot
// state diffs.
//
// The trace hooks installed by installTraceHooks emit events
// throughout the run, written to the file or stderr per the
// --trace-output flag.
func runHeadless(f *cliFlags) {
	model := roms.Model48K
	if f.startInNext {
		model = roms.ModelNext
	}

	emu, err := newEmulator(model)
	if err != nil {
		slog.Error("headless: emulator construction failed", "err", err)
		os.Exit(1)
	}

	_, closeFn := installTraceHooks(emu, f)
	defer closeFn()

	if f.logBankSwitches {
		installBankSwitchLogger(emu.mem)
	}
	loadSymbolMap(f.symbolMap)
	if f.detectUninit {
		var lo, hi uint16
		if f.uninitPCRange != "" {
			if l, h, err := parsePCRange(f.uninitPCRange); err == nil {
				lo, hi = l, h
			} else {
				slog.Warn("detect-uninit: bad --detect-uninit-pc, ignoring", "spec", f.uninitPCRange, "err", err)
			}
		}
		installUninitDetector(emu.mem, emu.cpu, lo, hi, f.uninitCap)
	}
	if f.watchBankWrite != "" {
		installBankWriteWatchers(emu.mem, emu.cpu, f.watchBankWrite)
	}
	installWriteWatchers(emu, f.watchWrites, func(e watchWriteEntry, pc uint16) {
		slog.Info("break-on-write",
			"name", e.name,
			"addr", fmt.Sprintf("$%04X", e.addr),
			"val", fmt.Sprintf("$%02X", e.breakValue),
			"pc", fmt.Sprintf("$%04X", pc),
		)
		dumpState(emu)
		dumpMemRanges(emu, parseDumpMemSpec(f.dumpMem))
		os.Exit(0)
	})
	snap := newSnapshotConfig(emu, f)

	// Trigger and stall snapshots reuse the same snapshot
	// machinery but bypass the periodic frame counter — they
	// emit one-shot regardless of --snapshot-every. We build a
	// dedicated emitter so triggering doesn't accidentally enable
	// every-frame snapshots.
	oneShot := &snapshotConfig{emu: emu, watch: parseWatchSpec(f.watchSpec)}

	if pct := parsePCTriggers(f.snapshotOnPC, func(name string, pc uint16) {
		slog.Debug("pc-trigger", "name", name, "pc", annotateAddr(pc))
		oneShot.emit()
	}); pct != nil {
		emu.cpu.AddPreFetchHook("debug-pc-trigger", pct.Step)
	}

	// Loop detector: emit a stall snapshot once when the same PC
	// has fired `threshold` times in a row.
	if ld := newLoopDetector(f.loopThreshold, func(pc uint16, count int) {
		slog.Debug("stall-detected", "pc", annotateAddr(pc), "count", count)
		oneShot.emit()
	}); ld != nil {
		emu.cpu.AddPreFetchHook("debug-loop-detector", ld.Step)
	}

	// Crash detector: heuristic patterns that almost always
	// indicate the guest has crashed. Fires once per kind per
	// re-arm window; each fire emits a snapshot.
	if cd := newCrashDetector(
		crashConfigFromFlags(f),
		crashEnv{
			Read:   emu.mem.Read,
			SP:     func() uint16 { return emu.cpu.SP },
			Halted: func() bool { return emu.cpu.Halted },
			IFF1:   func() bool { return emu.cpu.IFF1 },
		},
		func(kind crashKind, pc uint16, detail string) {
			slog.Debug("crash-detected",
				"kind", string(kind),
				"pc", annotateAddr(pc),
				"detail", detail,
				"insns", emu.cpu.InstructionCount(),
			)
			oneShot.emit()
		},
	); cd != nil {
		emu.cpu.AddPreFetchHook("debug-crash-detector", cd.Step)
	}

	// Remote-debugger TCP server (ZRCP-style). Hooks the CPU's
	// BreakpointCheck and gates ExecuteFrame via WaitIfPaused.
	rdbg := newRemoteDebugger(emu, f.debuggerPort, f.debuggerPauseAtStart, f.debuggerHistory, f.debuggerHistoryWide)
	// Publish the debugger on the emulator so hooks created during wiring
	// (e.g. the SD-card command logger's `break-on-sd` check, which reads
	// e.rdbg live) reach this instance. Without this, e.rdbg stays nil in
	// headless mode and those hooks silently no-op.
	emu.rdbg = rdbg

	// Time-travel buffer: auto-captures a full-state snapshot every
	// --time-travel insns into a bounded ring. Installed before the
	// frame loop starts so the boot itself is captured.
	if f.timeTravel > 0 {
		tt := newTimeTravelBuffer(emu, f.timeTravel, f.timeTravelKeep)
		if tt != nil {
			emu.cpu.AddPreFetchHook("debug-time-travel", tt.Step)
			emu.timeTravel = tt
			slog.Info("time-travel enabled",
				"every", f.timeTravel, "keep", f.timeTravelKeep)
			// On the Next each snapshot also captures the full machine
			// state (~2 MB pool + divMMC RAM + NextRegs), so the ring's
			// memory footprint is far larger than on classic models.
			// Surface it so a long --time-travel-keep doesn't surprise.
			if model == roms.ModelNext {
				slog.Info("time-travel Next full-state capture active (Phase 2b)",
					"approx_ring_mb", (f.timeTravelKeep*2200)/1024,
					"hint", "lower --time-travel-keep if memory is tight")
			}
		}
	}

	// Provenance tracer (Tool #1): arm from boot so mid-boot writes
	// are recorded. ensureProvenanceHook chains the WriteObserver on
	// top of any already installed (e.g. trace-writes). Works without
	// a debugger port — host the tracker on a bare debugger that owns
	// only the emulator pointer when no telnet debugger exists.
	var provHost *remoteDebugger
	if f.provenance {
		provHost = rdbg
		if provHost == nil {
			provHost = &remoteDebugger{emu: emu}
		}
		provHost.ensureProvenanceHook()
		provHost.provenance.enabled.Store(true)
		slog.Info("provenance tracer armed (last-writer index recording)")

		if f.whyPCAt != "" {
			// Accept "$ADDR" or "$ADDR:BANK" — the optional ROM-bank
			// qualifier lets why-pc-at skip benign entries (e.g. the
			// legit post-reset bank-0 $0000) and fire only on the
			// fatal one (bank-2 $0000 trap, #229).
			spec := f.whyPCAt
			bankFilter := -1
			if i := strings.IndexByte(spec, ':'); i >= 0 {
				bv, err := strconv.Atoi(spec[i+1:])
				if err != nil {
					slog.Error("why-pc-at: bad bank", "arg", f.whyPCAt, "err", err)
					os.Exit(1)
				}
				bankFilter = bv
				spec = spec[:i]
			}
			target, err := parseHex(spec)
			if err != nil {
				slog.Error("why-pc-at: bad address", "arg", f.whyPCAt, "err", err)
				os.Exit(1)
			}
			tgt := uint16(target)
			// Fire on the first RE-ENTRY to the target (optionally in
			// the filtered ROM bank): ignore the initial cold-boot
			// fetch, arm once the CPU has been elsewhere, then dump at
			// the moment PC lands on the target — when SP still
			// reflects the transfer.
			var left, fired bool
			var prevPC uint16 // instruction fetched immediately before
			emu.cpu.AddPreFetchHook("why-pc-at", func(pc uint16) {
				if fired {
					return
				}
				if pc != tgt {
					left = true
					prevPC = pc
					return
				}
				if !left {
					return // the cold-boot entry — skip
				}
				if bankFilter >= 0 && provHost.currentROMBank() != bankFilter {
					prevPC = pc
					return // not the bank we're after; keep waiting
				}
				fired = true
				// prevPC is the instruction that transferred control to
				// the target — the JP/JR/RET/RST source. This is the
				// smoking gun for a non-RET trap entry.
				slog.Info("why-pc-at: re-entry — capturing stack",
					"target", fmt.Sprintf("$%04X", tgt), "bank_filter", bankFilter,
					"jumped_from", annotateAddr(prevPC),
					"opcode", fmt.Sprintf("$%02X", emu.mem.Read(prevPC)))
				// Who wrote the (self-modified?) opcode bytes of the
				// jumping instruction — the cause behind a corrupted
				// branch (e.g. a stray RST $00 = $C7). Logical keying
				// can read stale across re-paging, so also resolve the
				// PHYSICAL pool cell and report its last writer — that
				// survives the slot remap.
				slog.Info("why-pc-at: jumping-instruction provenance (logical)",
					"addr", fmt.Sprintf("$%04X", prevPC),
					"byte0", provHost.formatProv(prevPC),
					"byte1", provHost.formatProv(prevPC+1))
				if bank16k, off, isRAM := provHost.resolvePhysical(prevPC); isRAM {
					slog.Info("why-pc-at: jumping-instruction provenance (physical)",
						"addr", fmt.Sprintf("$%04X", prevPC),
						"phys_bank", bank16k, "phys_off", fmt.Sprintf("$%04X", off),
						"byte0", provHost.formatProvPhys(bank16k, off),
						"byte1", provHost.formatProvPhys(bank16k, off+1))
				}
				provHost.logEndOfRunProvenance()
			})
			slog.Info("why-pc-at armed", "target", f.whyPCAt)
		}
	}

	// Trace DB (Tool #2): record M1 fetches into a ring, flushed to a
	// SQLite file at end-of-run for ad-hoc SQL queries.
	var tdb *traceDB
	var tdbFramePtr *int
	if f.traceDB != "" {
		tdb = newTraceDB(f.traceDBKeep)
		if tdb != nil {
			cpu := emu.cpu
			// Overlay attribution (the development log): without per-row
			// alt-rom + divMMC state, logical PCs in multi-overlay
			// eras cannot be mapped to the code that really executed.
			var tdbFrame int
			tdbFramePtr = &tdbFrame
			var dmcPager *divmmc.Pager
			if p, ok := emu.ula.NextDivMMC().(*divmmc.Pager); ok {
				dmcPager = p
			}
			emu.cpu.AddPreFetchHook("trace-db", func(pc uint16) {
				p7, p1, _ := emu.mem.GetPortState()
				bank := int((p7>>4)&1) | int((p1>>1)&2)
				alt := int(emu.mem.AltROMReg())
				dmc := 0
				if dmcPager != nil && dmcPager.IsPagedIn() {
					dmc = 1
				}
				tdb.record(traceDBRow{
					insn: cpu.InstructionCount(), pc: pc, sp: cpu.SP, bank: bank,
					alt: alt, dmc: dmc, frame: tdbFrame,
					af: uint16(cpu.A)<<8 | uint16(cpu.F),
					bc: uint16(cpu.B)<<8 | uint16(cpu.C),
					de: uint16(cpu.D)<<8 | uint16(cpu.E),
					hl: uint16(cpu.H)<<8 | uint16(cpu.L),
					ix: cpu.IX, iy: cpu.IY,
				})
			})
			slog.Info("trace-db recording", "path", f.traceDB, "keep", f.traceDBKeep)
		}
	}

	// ZX_GO_NAV_TRACE handoff tracer: a prefetch hook on the REAL
	// ExecuteFrame path (single-stepping diverges on INT timing). Armed
	// from navPhase 6; logs PC/opcode/regs/divMMC for navTraceLeft M1
	// fetches then exits. Diagnostic only.
	navTraceArmed := false
	navTraceReady := false
	navTraceLeft := 0
	navTraceN := 20000
	if ns := os.Getenv("ZX_GO_NAV_TRACE_N"); ns != "" {
		if v, e := strconv.Atoi(ns); e == nil {
			navTraceN = v
		}
	}
	var navTraceArmPC uint16
	navTraceHasArmPC := false
	if ps := os.Getenv("ZX_GO_NAV_TRACE_ARMPC"); ps != "" {
		if pv, e := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(ps, "0x"), "$"), 16, 16); e == nil {
			navTraceArmPC = uint16(pv)
			navTraceHasArmPC = true
		}
	}
	var navTraceArmInsn uint64
	if is := os.Getenv("ZX_GO_NAV_TRACE_ARMINSN"); is != "" {
		if iv, e := strconv.ParseUint(is, 10, 64); e == nil {
			navTraceArmInsn = iv
		}
	}
	// ZX_GO_NAV_POLL=FRAME: from FRAME, tally memory reads by address for a
	// fixed window, then dump the hottest addresses and exit — to find what
	// the stuck event loop is polling (the exit condition that never fires).
	if fp := os.Getenv("ZX_GO_FORCE_REVEAL_PC"); fp != "" {
		if pv, e := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(fp, "0x"), "$"), 16, 16); e == nil {
			target := uint16(pv)
			forceActive := false
			emu.cpu.AddPreFetchHook("force-reveal", func(pc uint16) {
				if pc == target {
					forceActive = true
				}
				if forceActive && emu.mem.AltROMReg() != 0x80 {
					emu.mem.SetAltROMReg(0x80) // force NR$8C reveal (reads-mode)
				}
			})
		}
	}
	if os.Getenv("ZX_GO_NAV_7FFD") != "" {
		emu.mem.SetPagingTracer(func(source string, val byte, applied, sb, sa bool) {
			fmt.Printf("PAGE %s val=$%02X bit3(shadow)=%d applied=%v pc=$%04X\n",
				source, val, (val>>3)&1, applied, emu.cpu.PC)
		})
	}
	navPollActive := false
	navPollCounts := map[uint16]int{}
	if os.Getenv("ZX_GO_NAV_POLL") != "" {
		emu.mem.SetAllReadHook(func(addr uint16, val byte) {
			if navPollActive {
				navPollCounts[addr]++
			}
		})
	}
	if os.Getenv("ZX_GO_NAV_TRACE") != "" {
		var navPager *divmmc.Pager
		if p, ok := emu.ula.NextDivMMC().(*divmmc.Pager); ok {
			navPager = p
		}
		emu.cpu.AddPreFetchHook("nav-trace", func(pc uint16) {
			// Self-arm on the configured PC once the nav is ready to launch.
			// With no ARMPC set, arm at the FIRST fetch after navTraceReady
			// (= the launch start, navPhase 5) so the sub-frame, interrupt-
			// free menu→launch dispatch window can be captured in full.
			if !navTraceArmed && navTraceReady && (!navTraceHasArmPC || pc == navTraceArmPC) &&
				(navTraceArmInsn == 0 || emu.cpu.InstructionCount() >= navTraceArmInsn) {
				navTraceArmed = true
				navTraceLeft = navTraceN
			}
			if !navTraceArmed || navTraceLeft <= 0 {
				return
			}
			// One-shot memory dump at a configurable address on first armed
			// fetch (ZX_GO_NAV_TRACE_DUMP=hexaddr) — e.g. the $DA35 command
			// string the launcher hands to RST $00.
			if ds := os.Getenv("ZX_GO_NAV_TRACE_DUMP"); ds != "" && navTraceLeft == navTraceN {
				if da, e := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(ds, "0x"), "$"), 16, 16); e == nil {
					var b strings.Builder
					for k := 0; k < 48; k++ {
						by := emu.mem.Read(uint16(da) + uint16(k))
						ch := '.'
						if by >= 0x20 && by < 0x7f {
							ch = rune(by)
						}
						fmt.Fprintf(&b, "%02X%c ", by, ch)
					}
					fmt.Printf("DUMP @%04X: %s\n", uint16(da), b.String())
				}
			}
			c := emu.cpu
			e3 := byte(0xFF)
			if navPager != nil {
				e3 = navPager.LastE3()
			}
			fmt.Printf("T %04X: %02X %02X %02X  AF=%02X%02X BC=%04X DE=%04X HL=%04X IX=%04X IY=%04X SP=%04X e3=%02X rb=%d\n",
				pc, emu.mem.Read(pc), emu.mem.Read(pc+1), emu.mem.Read(pc+2),
				c.A, c.F, c.BC(), c.DE(), c.HL(), c.IX, c.IY, c.SP, e3, emu.mem.GetROMBank())
			navTraceLeft--
			if navTraceLeft == 0 {
				os.Exit(0)
			}
		})
	}

	// Read-tape (instruction-level lockstep): record a value-level
	// RAM-read tape from a checkpoint, for first-divergence-by-value
	// comparison against an oracle's tape (_tools/tapediff). See
	// readtape.go.
	if f.readTape != "" {
		startPC, startHit := parseReadTapeFrom(f.readTapeFrom)
		fobj, err := os.Create(f.readTape)
		if err != nil {
			slog.Error("read-tape: cannot create file", "path", f.readTape, "err", err)
		} else {
			rtape := newReadTape(fobj, startPC, startHit, uint64(f.readTapeMax))
			emu.cpu.AddPreFetchHook("read-tape", func(pc uint16) { rtape.onExecute(pc) })
			if f.readTapeAll {
				// Bank-agnostic: log EVERY read by logical address (for
				// diffing vs a reference emulator that logs all reads,
				// when code is banked through a window). No port tracer:
				// a memory-read tape must match a memory-only oracle tap.
				emu.mem.SetAllReadHook(func(addr uint16, val byte) { rtape.onReadAll(addr, val) })
			} else {
				emu.mem.SetRAMReadHook(func(bank int, addr uint16, val byte) { rtape.onRead(bank, addr, val) })
				// Chain port writes into the tape (keep any prior tracer).
				priorTracer := emu.ula.GetPortTracer()
				emu.ula.SetPortTracer(func(addr uint16, val byte, write, handled bool) {
					if priorTracer != nil {
						priorTracer(addr, val, write, handled)
					}
					if write {
						rtape.onPortWrite(addr, val)
					} else {
						rtape.onPortRead(addr, val)
					}
				})
			}
			defer func() { rtape.flush(); _ = fobj.Close() }()
			slog.Info("read-tape recording", "path", f.readTape, "all", f.readTapeAll,
				"from_pc", startPC, "from_hit", startHit, "max", f.readTapeMax)
		}
	}

	pressKeys := parsePressKeySpec(f.pressKey)
	// keyHold is the number of frames a press-key entry stays
	// "held down" — long enough that NextZXOS's debouncer + scan
	// loop sees the press but short enough not to register as auto-
	// repeat. 30 frames (~0.6s at 50Hz) matches a deliberate
	// human keypress and survives any debounce filters NextZXOS
	// applies.
	const keyHold = 30

	// ZX_GO_NAV_128K: reliable cursor-feedback navigation to NextZXOS
	// "128K BASIC" (the timed --press-key spec can't — NextZXOS auto-
	// repeats cursor keys, and the slow/key-dependent boot shifts the
	// welcome). Reads the menu cursor index at logical $F700 and HOLDS
	// cursor-down until it reads the target, then ENTER. Menu: page1
	// item5 = More…; page2 item5 = 128K BASIC.
	nav128k := os.Getenv("ZX_GO_NAV_128K") != ""
	navPhase, navAct := 0, 0

	// ZX_GO_TDIFF_AT="100,300,500": no-nav, guest-clock-anchored full-state
	// dumper for the rigorous ours<->reference first-divergence binary search.
	// The shared clock is the guest FRAMES sysvar ($5C78/$5C79 word) — it is
	// driven by the instruction stream (the ROM frame-INT handler ticks it),
	// so it aligns across two emulators regardless of host T-state accounting.
	// At each listed FRAMES value we emit one FD| block (identical format to
	// the reference emulator's full_state_dump.lua), then exit after the last anchor.
	var tdiffAt []int
	tdiffArmed := false
	if s := os.Getenv("ZX_GO_TDIFF_AT"); s != "" {
		for _, p := range strings.Split(s, ",") {
			if v, e := strconv.Atoi(strings.TrimSpace(p)); e == nil {
				tdiffAt = append(tdiffAt, v)
			}
		}
	}
	dumpFD := func(tag string) {
		fmt.Printf("FD|TAG|%s\n", tag)
		nrAll := ""
		for r := 0; r < 256; r++ {
			nrAll += fmt.Sprintf("%02X", emu.nextRegs.ReadReg(byte(r)))
		}
		fmt.Printf("FD|NR|%s\n", nrAll)
		c := emu.cpu
		b2 := func(v bool) int {
			if v {
				return 1
			}
			return 0
		}
		fmt.Printf("FD|CPU|af=%02X%02X bc=%02X%02X de=%02X%02X hl=%02X%02X af_=%02X%02X bc_=%02X%02X de_=%02X%02X hl_=%02X%02X ix=%04X iy=%04X sp=%04X pc=%04X i=%02X r=%02X iff1=%d iff2=%d im=%d\n",
			c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L,
			c.A_, c.F_, c.B_, c.C_, c.D_, c.E_, c.H_, c.L_,
			c.IX, c.IY, c.SP, c.PC, c.I, c.R, b2(c.IFF1), b2(c.IFF2), c.IM)
		p7d, p1d, spc := emu.mem.GetPortState()
		e3d := byte(0xFF)
		if pg, ok := emu.ula.NextDivMMC().(interface{ LastE3() byte }); ok {
			e3d = pg.LastE3()
		}
		fmt.Printf("FD|PORT|7ffd=%02X 1ffd=%02X e3=%02X special=%v f700=%02X\n", p7d, p1d, e3d, spc, emu.mem.Read(0xF700))
		for bank8k := 0; bank8k < 256; bank8k++ {
			pg := emu.mem.RAM8KPage(bank8k)
			if pg == nil {
				continue
			}
			sum := 0
			for _, x := range pg {
				sum = (sum + int(x)) & 0xFFFFFF
			}
			fmt.Printf("FD|RAM8K|%03d=%06X|%02X%02X%02X%02X\n", bank8k, sum, pg[0], pg[1], pg[2], pg[3])
		}
		if pg, ok := emu.ula.NextDivMMC().(interface{ RAMBank(int) []byte }); ok {
			for b := 0; b < 16; b++ {
				bank := pg.RAMBank(b)
				if len(bank) < 0x2000 {
					continue
				}
				sum := 0
				for _, x := range bank {
					sum = (sum + int(x)) & 0xFFFFFF
				}
				fmt.Printf("FD|DIVRAM|%02d=%06X\n", b, sum)
			}
		}
		// Optional full-byte hex of specific 8K banks (ZX_GO_TDIFF_HEX="10,16")
		// for localising WHERE inside a divergent bank the bytes differ.
		if hb := os.Getenv("ZX_GO_TDIFF_HEX"); hb != "" {
			for _, p := range strings.Split(hb, ",") {
				bk, e := strconv.Atoi(strings.TrimSpace(p))
				if e != nil {
					continue
				}
				pg := emu.mem.RAM8KPage(bk)
				if pg == nil {
					continue
				}
				var sb strings.Builder
				for _, x := range pg {
					fmt.Fprintf(&sb, "%02X", x)
				}
				fmt.Printf("FD|HEX|%03d|%s\n", bk, sb.String())
			}
		}
		fmt.Printf("FD|END\n")
	}

	emu.paused.Store(false)
	if f.frames > 0 {
		slog.Info("headless run starting", "model", roms.GetModelName(model),
			"frames", f.frames)
		for i := 0; i < f.frames; i++ {
			if tdbFramePtr != nil {
				*tdbFramePtr = i
			}
			if len(tdiffAt) > 0 {
				gf := int(emu.mem.Read(0x5C78)) | int(emu.mem.Read(0x5C79))<<8
				// Arm only after NextZXOS has zeroed the FRAMES sysvar (welcome
				// up); before that $5C78 holds uninitialised garbage that is
				// >= every anchor and would fire them all on junk state.
				if !tdiffArmed && gf < 50 {
					tdiffArmed = true
				}
				if i%64 == 0 {
					slog.Info("tdiff probe", "hostframe", i, "guestFRAMES", gf,
						"armed", tdiffArmed, "insns", emu.cpu.InstructionCount(), "pc", fmt.Sprintf("%04X", emu.cpu.PC))
				}
				for tdiffArmed && len(tdiffAt) > 0 && gf >= tdiffAt[0] {
					dumpFD(fmt.Sprintf("FRAMES=%d hostframe=%d insns=%d", tdiffAt[0], i, emu.cpu.InstructionCount()))
					tdiffAt = tdiffAt[1:]
				}
				if len(tdiffAt) == 0 {
					os.Exit(0)
				}
			}
			if f.rebootAt > 0 && i == f.rebootAt {
				slog.Info("headless reboot", "frame", i)
				emu.reboot()
			}
			for _, pk := range pressKeys {
				switch i {
				case pk.frame:
					emu.kbd.PressMatrixKey(pk.row, pk.mask, true)
					slog.Info("press-key down", "name", pk.name, "frame", i)
				case pk.frame + keyHold:
					emu.kbd.PressMatrixKey(pk.row, pk.mask, false)
					slog.Info("press-key up", "name", pk.name, "frame", i)
				}
			}
			if nav128k {
				cur := emu.mem.Read(0xF700)
				dn := func(on bool) { emu.kbd.PressMatrixKey(0, 0x01, on); emu.kbd.PressMatrixKey(4, 0x10, on) }
				en := func(on bool) { emu.kbd.PressMatrixKey(6, 0x01, on) }
				sp := func(on bool) { emu.kbd.PressMatrixKey(7, 0x01, on) }
				shot := func(tag string) {
					dir := os.Getenv("ZX_GO_NAV_SHOTS")
					if dir == "" {
						return
					}
					p := fmt.Sprintf("%s/nav-%05d-%s.png", dir, i, tag)
					fp, err := os.Create(p)
					if err != nil {
						return
					}
					_ = png.Encode(fp, emu.ula.Render())
					_ = fp.Close()
					slog.Info("nav shot", "path", p, "cursor", cur)
				}
				if i%40 == 0 {
					slog.Info("nav", "frame", i, "phase", navPhase, "cursor", cur)
				}
				switch navPhase {
				case 0: // wait past the welcome, then SPACE -> main menu
					switch i {
					case 2300:
						sp(true)
					case 2335:
						sp(false)
						navPhase, navAct = 1, i
					}
				case 1: // page1: hold cursor-down until cursor==5 (More…)
					if i >= navAct+90 {
						if cur == 5 {
							shot("page1-more")
							dn(false)
							navPhase, navAct = 2, i
						} else {
							dn(true)
						}
					}
				case 2: // ENTER More… -> page2
					switch i {
					case navAct + 5:
						en(true)
					case navAct + 40:
						en(false)
						navPhase, navAct = 3, i
					}
				case 3: // let page2 settle
					if i >= navAct+90 {
						shot("page2-top")
						navPhase, navAct = 4, i
					}
				case 4: // page2: hold cursor-down until cursor==5 (128K BASIC)
					if cur == 5 {
						shot("page2-128k")
						dn(false)
						navPhase, navAct = 5, i
						navTraceReady = true // arm the launch handoff tracer
					} else {
						dn(true)
					}
				case 5: // ENTER -> launch 128K BASIC
					if ls := os.Getenv("ZX_GO_LAUNCH_SPEED"); ls != "" && len(ls) == 1 && i == navAct+1 {
						// Nav ran at the normal 28 MHz (so it completes); now
						// LOCK the CPU speed for the launch only — to test
						// whether the 128K reveal is timing-driven (the reference emulator runs
						// the launch at 3.5 MHz).
						emu.cpu.LockSpeedSelect(ls[0] - '0')
						slog.Info("LAUNCH_SPEED locked", "sel", string(ls))
					}
					if ip := os.Getenv("ZX_GO_INJECT"); ip != "" && i == navAct+1 {
						// State TRANSPLANT: overwrite our pre-ENTER state with
						// the reference emulator's exact state, then let the nav press ENTER and
						// run OUR launch code over it. White => our execution is
						// correct; black => our execution/timing is the bug.
						if div, ok := emu.ula.NextDivMMC().(divInjectTarget); ok {
							if err := injectForeignState(ip, emu.mem, emu.cpu, emu.nextRegs, div); err != nil {
								slog.Error("inject failed", "err", err)
							}
						} else {
							slog.Error("inject: divMMC pager does not satisfy divInjectTarget")
						}
					}
					if os.Getenv("ZX_GO_FULLDUMP") != "" && i == navAct+1 {
						// EXHAUSTIVE pre-ENTER state dump for the conclusive
						// ours<->reference identity audit: every NextReg, the full CPU
						// register file, the paging ports, every 8K RAM page,
						// every divMMC-RAM bank. Machine-parseable "FD|" lines;
						// exits immediately after.
						nrAll := ""
						for r := 0; r < 256; r++ {
							nrAll += fmt.Sprintf("%02X", emu.nextRegs.ReadReg(byte(r)))
						}
						fmt.Printf("FD|NR|%s\n", nrAll)
						c := emu.cpu
						b2 := func(v bool) int {
							if v {
								return 1
							}
							return 0
						}
						fmt.Printf("FD|CPU|af=%02X%02X bc=%02X%02X de=%02X%02X hl=%02X%02X af_=%02X%02X bc_=%02X%02X de_=%02X%02X hl_=%02X%02X ix=%04X iy=%04X sp=%04X pc=%04X i=%02X r=%02X iff1=%d iff2=%d im=%d\n",
							c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L,
							c.A_, c.F_, c.B_, c.C_, c.D_, c.E_, c.H_, c.L_,
							c.IX, c.IY, c.SP, c.PC, c.I, c.R, b2(c.IFF1), b2(c.IFF2), c.IM)
						p7d, p1d, spc := emu.mem.GetPortState()
						e3d := byte(0xFF)
						if pg, ok := emu.ula.NextDivMMC().(interface{ LastE3() byte }); ok {
							e3d = pg.LastE3()
						}
						fmt.Printf("FD|PORT|7ffd=%02X 1ffd=%02X e3=%02X special=%v f700=%02X\n", p7d, p1d, e3d, spc, emu.mem.Read(0xF700))
						for bank8k := 0; bank8k < 256; bank8k++ {
							pg := emu.mem.RAM8KPage(bank8k)
							if pg == nil {
								continue
							}
							sum := 0
							for _, x := range pg {
								sum = (sum + int(x)) & 0xFFFFFF
							}
							fmt.Printf("FD|RAM8K|%03d=%06X|%02X%02X%02X%02X\n", bank8k, sum, pg[0], pg[1], pg[2], pg[3])
						}
						if pg, ok := emu.ula.NextDivMMC().(interface{ RAMBank(int) []byte }); ok {
							for b := 0; b < 16; b++ {
								bank := pg.RAMBank(b)
								if len(bank) < 0x2000 {
									continue
								}
								sum := 0
								for _, x := range bank {
									sum = (sum + int(x)) & 0xFFFFFF
								}
								fmt.Printf("FD|DIVRAM|%02d=%06X\n", b, sum)
							}
						}
						fmt.Printf("FD|END\n")
						os.Exit(0)
					}
					if os.Getenv("ZX_GO_PREENTER_DUMP") != "" && i == navAct+1 {
						// Pre-ENTER full-state dump (128K BASIC highlighted, not
						// yet launched) for the ours<->reference identical-state audit.
						if pg, ok := emu.ula.NextDivMMC().(interface{ RAMBank(int) []byte }); ok {
							for b := 0; b < 16; b++ {
								bank := pg.RAMBank(b)
								if len(bank) < 0x2000 {
									continue
								}
								sum := 0
								for _, x := range bank {
									sum = (sum + int(x)) & 0xFFFFFF
								}
								slog.Info("PREDIVRAM", "b", b, "sum", fmt.Sprintf("%06X", sum),
									"head", fmt.Sprintf("%02X%02X%02X%02X%02X%02X%02X%02X",
										bank[0x300], bank[0x301], bank[0x302], bank[0x303],
										bank[0x310], bank[0x311], bank[0x312], bank[0x313]))
							}
						}
						pregs := []byte{0x00, 0x01, 0x02, 0x03, 0x05, 0x06, 0x07, 0x08, 0x0a, 0x0b, 0x0e, 0x0f, 0x10, 0x11, 0x8c, 0x8e}
						ps := ""
						for _, r := range pregs {
							ps += fmt.Sprintf("%02X=%02X ", r, emu.nextRegs.ReadReg(r))
						}
						p7, p1, sp := emu.mem.GetPortState()
						ks := ""
						for a := 0x5C00; a <= 0x5C0A; a++ {
							ks += fmt.Sprintf("%02X", emu.mem.Read(uint16(a)))
						}
						slog.Info("PRENR", "regs", ps, "f700", fmt.Sprintf("%02X", emu.mem.Read(0xF700)), "a5800", fmt.Sprintf("%02X", emu.mem.Read(0x5800)),
							"p7ffd", fmt.Sprintf("%02X", p7), "p1ffd", fmt.Sprintf("%02X", p1), "special", sp, "kstate5C00", ks)
						a0 := emu.mem.GetAltROM(0)
						a1 := emu.mem.GetAltROM(1)
						s0, s1 := 0, 0
						for _, x := range a0 {
							s0 += int(x)
						}
						for _, x := range a1 {
							s1 += int(x)
						}
						slog.Info("PREALTROM",
							"alt0sum", fmt.Sprintf("%06X", s0), "alt0head", fmt.Sprintf("%02X%02X%02X%02X%02X%02X", a0[0], a0[1], a0[2], a0[3], a0[4], a0[5]),
							"alt1sum", fmt.Sprintf("%06X", s1), "alt1head", fmt.Sprintf("%02X%02X%02X%02X%02X%02X", a1[0], a1[1], a1[2], a1[3], a1[4], a1[5]))
					}
					enterFrame := navAct + 5
					if os.Getenv("ZX_GO_INJECT") != "" {
						enterFrame = navAct + 2 // press ENTER right after injection (no idle drift)
					}
					if ds := os.Getenv("ZX_GO_ENTER_DELAY"); ds != "" {
						if dv, e := strconv.Atoi(ds); e == nil {
							enterFrame = navAct + dv // delay ENTER (let cursor-down KSTATE clear)
						}
					}
					releaseFrame := enterFrame + 35
					if hs := os.Getenv("ZX_GO_ENTER_HOLD"); hs != "" {
						if hv, e := strconv.Atoi(hs); e == nil {
							releaseFrame = enterFrame + hv
						}
					}
					switch i {
					case enterFrame:
						en(true)
					case releaseFrame:
						en(false)
						navPhase, navAct = 6, i
						slog.Info("nav: launched 128K BASIC", "frame", i, "enterHeld", releaseFrame-enterFrame)
					}
				case 6: // post-launch: sample PC + paging to see what the 128K switch did
					// ZX_GO_POSTLAUNCH=N: N frames after ENTER, render a PNG +
					// dump the launch-outcome signals (PC, NR$8C reveal, MMU
					// slots, f700) and exit — the conclusive "did the Sinclair
					// 128 menu appear?" check (menu => revealed AltROM NR$8C=$80,
					// PC in the $3xxx editor; welcome => NextZXOS, PC $0C90).
					if pl := os.Getenv("ZX_GO_POSTLAUNCH"); pl != "" {
						if n, e := strconv.Atoi(pl); e == nil && i == navAct+n {
							c := emu.cpu
							nrf := func(r byte) string { return fmt.Sprintf("%02X", emu.nextRegs.ReadReg(r)) }
							slog.Info("POSTLAUNCH", "frame", i,
								"pc", fmt.Sprintf("%04X", c.PC),
								"nr8c", nrf(0x8C), "nr50", nrf(0x50), "nr51", nrf(0x51),
								"f700", fmt.Sprintf("%02X", emu.mem.Read(0xF700)))
							slog.Info("POSTLAUNCH-VIDEO",
								"nr69_l2ctl", nrf(0x69), "nr15_layerpri", nrf(0x15),
								"nr70_l2res", nrf(0x70), "nr12_l2bank", nrf(0x12),
								"nr1c_clip", nrf(0x1C), "nr14_gtrans", nrf(0x14),
								"nr4a_fb", nrf(0x4A), "nr68_ulactl", nrf(0x68))
							if dir := os.Getenv("ZX_GO_NAV_SHOTS"); dir != "" {
								p := fmt.Sprintf("%s/postlaunch-%05d.png", dir, i)
								if fp, err := os.Create(p); err == nil {
									_ = png.Encode(fp, emu.ula.Render())
									_ = fp.Close()
									slog.Info("POSTLAUNCH shot", "path", p)
								}
							}
							os.Exit(0)
						}
					}
					// ZX_GO_NAV_TRACE=FRAME: at FRAME, single-step a fixed
					// window of instructions logging PC/opcode/regs/divMMC
					// paging, then exit — to read the stuck streaming loop's
					// (never-taken) exit branch. Diagnostic only.
					// ZX_GO_NAV_TRACE_PC=HHHH: if set, arm only once i>=FRAME
					// AND PC==HHHH (catches the Nth-ish occurrence of a PC at
					// the handoff). Otherwise trigger purely at i==FRAME.
					if tf := os.Getenv("ZX_GO_NAV_TRACE"); tf != "" {
						if pf := os.Getenv("ZX_GO_NAV_POLL"); pf != "" {
							if pstart, e := strconv.Atoi(pf); e == nil {
								if i == pstart {
									navPollActive = true
								}
								if i == pstart+40 {
									type kv struct {
										a uint16
										n int
									}
									var ks []kv
									for a, n := range navPollCounts {
										ks = append(ks, kv{a, n})
									}
									sort.Slice(ks, func(x, y int) bool { return ks[x].n > ks[y].n })
									for n := 0; n < len(ks) && n < 30; n++ {
										fmt.Printf("POLL %04X reads=%d val=%02X\n", ks[n].a, ks[n].n, emu.mem.Read(ks[n].a))
									}
									os.Exit(0)
								}
							}
						}
						start, perr := strconv.Atoi(tf)
						if perr == nil && i == start && !navTraceArmed {
							n := 20000
							if ns := os.Getenv("ZX_GO_NAV_TRACE_N"); ns != "" {
								if v, e := strconv.Atoi(ns); e == nil {
									n = v
								}
							}
							navTraceLeft = n
							navTraceArmed = true
						}
					}
					if i%8 == 0 {
						dmmc := "n/a"
						if pg, ok := emu.ula.NextDivMMC().(interface {
							IsPagedIn() bool
							AutomapEnabled() bool
							MAPRAM() bool
							LastE3() byte
						}); ok && pg != nil {
							dmmc = fmt.Sprintf("paged=%v auto=%v mapram=%v e3=$%02X",
								pg.IsPagedIn(), pg.AutomapEnabled(), pg.MAPRAM(), pg.LastE3())
						}
						ar0, ar1 := emu.mem.GetAltROM(0), emu.mem.GetAltROM(1)
						a0sig, a1sig := "nil", "nil"
						if len(ar0) >= 4 {
							a0sig = fmt.Sprintf("%02X%02X%02X%02X", ar0[0], ar0[1], ar0[2], ar0[3])
						}
						if len(ar1) >= 4 {
							a1sig = fmt.Sprintf("%02X%02X%02X%02X", ar1[0], ar1[1], ar1[2], ar1[3])
						}
						frames24 := int(emu.mem.Read(0x5C78)) | int(emu.mem.Read(0x5C79))<<8 | int(emu.mem.Read(0x5C7A))<<16
						// Which RAM bank actually holds drawn menu content? Count
						// non-zero attribute bytes ($1800-$1AFF in the bank) for
						// banks 5 & 7, vs what the ULA renders (ScreenPage).
						nz := func(bank int) int {
							pg := emu.mem.GetPage(bank)
							c := 0
							if len(pg) >= 0x1B00 {
								for k := 0x1800; k < 0x1B00; k++ {
									if pg[k] != 0 {
										c++
									}
								}
							}
							return c
						}
						nzpix := func(bank int) int {
							pg := emu.mem.GetPage(bank)
							c := 0
							if len(pg) >= 0x1800 {
								for k := 0x0000; k < 0x1800; k++ {
									if pg[k] != 0 {
										c++
									}
								}
							}
							return c
						}
						attrSample := func(bank int) string {
							pg := emu.mem.GetPage(bank)
							hist := map[byte]int{}
							if len(pg) >= 0x1B00 {
								for k := 0x1800; k < 0x1B00; k++ {
									if pg[k] != 0 {
										hist[pg[k]]++
									}
								}
							}
							s := ""
							for v, c := range hist {
								s += fmt.Sprintf("%02X:%d ", v, c)
							}
							return s
						}
						_ = nzpix
						_ = attrSample
						p7, p1, _ := emu.mem.GetPortState()
						_ = p1
						mmu := ""
						for s := 0; s < 8; s++ {
							mmu += fmt.Sprintf("%02X ", emu.nextRegs.ReadReg(byte(0x50+s)))
						}
						slog.Info("screen-banks", "frame", i, "ScreenPage", emu.mem.ScreenPage,
							"7FFD", fmt.Sprintf("$%02X", p7), "nzAttrBank5", nz(5), "nzAttrBank7", nz(7),
							"renderedAttr5800", fmt.Sprintf("$%02X", emu.mem.Read(0x5800)),
							"nzPixBank7", nzpix(7), "nzPixBank5", nzpix(5),
							"attrSample7", attrSample(7),
							"MMU50-57", mmu,
							"NR69", fmt.Sprintf("$%02X", emu.nextRegs.ReadReg(0x69)),
							"NR6B", fmt.Sprintf("$%02X", emu.nextRegs.ReadReg(0x6B)))
						slog.Info("post-launch", "frame", i, "pc", fmt.Sprintf("$%04X", emu.cpu.PC),
							"rombank", emu.mem.GetROMBank(),
							"FRAMES", fmt.Sprintf("$%06X", frames24),
							"intfires", z80.IntFireCount,
							"iff1", emu.cpu.IFF1,
							"altrom", fmt.Sprintf("$%02X", emu.mem.AltROMReg()),
							"altrom0", a0sig, "altrom1", a1sig,
							"divmmc", dmmc,
							"attr0", fmt.Sprintf("$%02X", emu.mem.Read(0x5800)),
							"border", emu.ula.BorderColour)
					}
				}
			}
			rdbg.WaitIfPaused()
			runOneFrameHeadless(emu, model)
			if os.Getenv("ZX_GO_RENDER_EVERY_FRAME") != "" {
				emu.ula.Render() // GUI-parity probe (the development log)
			}
			if snap != nil {
				snap.Tick()
			}
		}
		slog.Info("headless run complete", "frames", f.frames,
			"pc", emu.cpu.PC, "insns", emu.cpu.InstructionCount(),
			"int_fires", z80.IntFireCount)
		emu.flushSDWriteback()
		if provHost != nil {
			provHost.logEndOfRunProvenance()
		}
		if tdb != nil {
			n, err := tdb.flushSQLite(f.traceDB)
			if err != nil {
				slog.Error("trace-db flush failed", "path", f.traceDB, "err", err)
			} else {
				slog.Info("trace-db flushed", "path", f.traceDB, "rows", n,
					"hint", "query with: sqlite3 "+f.traceDB+" 'SELECT ...'")
			}
		}
	} else {
		slog.Info("headless run starting", "model", roms.GetModelName(model),
			"frames", "unlimited")
		for {
			rdbg.WaitIfPaused()
			runOneFrameHeadless(emu, model)
			if os.Getenv("ZX_GO_RENDER_EVERY_FRAME") != "" {
				emu.ula.Render() // GUI-parity probe (the development log)
			}
			if snap != nil {
				snap.Tick()
			}
		}
	}

	if f.dumpState > 0 {
		dumpState(emu)
		dumpMemRanges(emu, parseDumpMemSpec(f.dumpMem))
	}

	if f.saveScreen != "" {
		img := emu.ula.Render()
		fp, err := os.Create(f.saveScreen)
		if err != nil {
			slog.Error("save-screen: create", "path", f.saveScreen, "err", err)
			os.Exit(1)
		}
		if err := png.Encode(fp, img); err != nil {
			slog.Error("save-screen: encode", "path", f.saveScreen, "err", err)
			_ = fp.Close()
			os.Exit(1)
		}
		_ = fp.Close()
		slog.Info("screenshot saved", "path", f.saveScreen)
	}
}

// runOneFrameHeadless advances the CPU by one ULA frame. Normally that's a
// single ExecuteFrame call. ZX_GO_STEP_FRAME=1 instead drives the CPU through
// the per-instruction StepInstructionWithIRQ path (the same code the debugger
// / bisection / memdiff use) for a frame's worth of T-states — a diagnostic
// to test whether ExecuteFrame's batch body diverges from the step path
// (which reaches NextZXOS while ExecuteFrame drops to 48K BASIC).
func runOneFrameHeadless(emu *emulator, model roms.SpectrumModel) {
	if os.Getenv("ZX_GO_STEP_FRAME") == "" {
		emu.cpu.ExecuteFrame(frameTStatesForModel(model))
		return
	}
	budget := uint64(frameTStatesForModel(model)) * uint64(emu.cpu.SpeedMultiplier())
	target := emu.cpu.Tstates() + budget
	for emu.cpu.Tstates() < target {
		emu.cpu.StepInstructionWithIRQ()
	}
}
