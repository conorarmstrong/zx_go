package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/trace"
	"github.com/conorarmstrong/zx_go/pkg/version"
	"github.com/conorarmstrong/zx_go/pkg/zxlog"
)

// cliFlagsActive holds the parsed --log-sd, --log-bank-switches,
// etc. flags so setup paths that don't take a *cliFlags argument
// (e.g. setupNextSubsystems on the GUI path) can still consult
// them. Set by parseCLI() before the GUI/headless dispatch.
//
// nil-safe — checkers should `if cliFlagsActive != nil &&
// cliFlagsActive.X` so callers that bypass parseCLI (tests, the
// headless harness in pkg/testharness) keep the defaults.
var cliFlagsActive *cliFlags

// cliFlags collects every command-line option the binary accepts.
// Parsed early in main(), before any of the GUI or emulator
// machinery comes up.
type cliFlags struct {
	logLevel        slog.Level
	traceChannels   trace.Channels
	tracePCStart    uint16
	tracePCEnd      uint16
	tracePCRange    bool
	traceOutput     string
	headless        bool
	frames          int
	dumpState       int
	saveScreen      string
	startInNext     bool
	startInZX81     bool
	startInZX80     bool
	startInPentagon bool
	startInSAM      bool

	// trdPath, when set, mounts a TR-DOS .TRD image in Beta drive A at
	// startup (classic models only).
	trdPath string

	// Periodic state-snapshot interval. When non-zero, the
	// headless loop dumps registers, sysvars, paging state, and
	// optionally a screenshot every snapshotEvery frames.
	snapshotEvery   int
	snapshotPrefix  string
	snapshotScreens bool

	// Memory-region watch: comma-separated `name:addr` (e.g.
	// "ERR_NR:5C3A,CH-ADD:5C5D,FLAGS2-ext:D5D4"). Each entry is
	// dumped at every snapshot and on bank-switch events.
	watchSpec string

	// SD-card command logging (alias for the ZX_GO_SD_DEBUG
	// environment variable, but more discoverable).
	logSDCommands bool

	// Print bank-switch + MMU-slot change events.
	logBankSwitches bool

	// Comma-separated list of `name@addr[=val]` write-watch entries.
	// Every write to one of these addresses is logged with the
	// CPU PC at the time, so you can see exactly which code wrote
	// what. Same address syntax as --watch. When the optional `=val`
	// is present, a write of that specific value causes headless
	// mode to dump full state (including any --dump-mem ranges) and
	// exit — useful for catching the exact instant a poisoned byte
	// appears in memory.
	watchWrites string

	// detectUninit enables the unified-RAM uninitialised-read
	// detector: a guest read of a RAM byte never written since
	// power-on logs its source PC + pool location. uninitPCRange
	// (`$LO-$HI`, hi optional) restricts logging to a code window;
	// uninitCap bounds the number of distinct (pc,addr) reports.
	detectUninit  bool
	uninitPCRange string
	uninitCap     int

	// watchBankWrite logs guest writes landing in a specific unified-
	// pool 16K bank + offset range, keyed on the pool location (not
	// the CPU address), so paging can't hide which bank received a
	// write. Format "BANK:LO-HI[,…]" (bank decimal, offsets hex).
	watchBankWrite string

	// Memory ranges to print after a normal --dump-state finish or
	// when a --watch-writes break fires. `ADDR:LEN[,ADDR:LEN…]`,
	// hex on both sides. Each range is dumped as a 16-byte hex grid.
	dumpMem string

	// Headless key-press schedule: `KEY@FRAME[,KEY@FRAME…]`. KEY is
	// one of the Spectrum keys (enter, space, 0-9, a-z, caps, sym).
	// At the given frame the key is held down; it's released a few
	// frames later so guest software's debouncing logic sees a
	// realistic press. Used to drive NextZXOS Browser navigation
	// in headless screenshot runs.
	pressKey string

	// When >0, trigger an in-headless Reboot at this frame using
	// the same emu.reboot() entry point as the GUI's
	// "Emulator → Reboot" menu item. Used to regression-test that
	// the reboot cleanly re-enters the FPGA bootrom + clears
	// NextRegs without leaving stale tilemap / Layer 2 / paging
	// state.
	rebootAt int

	// Comma-separated list of `name@addr` (or `name@lo-hi`) PC
	// triggers. The first time the CPU fetches an instruction at
	// one of these addresses, a full snapshot is emitted (CPU,
	// MMU, sysvars, disasm window, stack walk). Massively useful
	// for finding when supposedly-unreachable code first gets
	// reached.
	snapshotOnPC string

	// Period (in instructions) over which to consider a stall.
	// If the same PC has been seen for ≥ loopThreshold consecutive
	// M1 fetches without any other PC intervening, emit a
	// "stall-detected" snapshot.
	loopThreshold int

	// Symbol map: a file with `addr name` lines, used to annotate
	// disasm windows and stack-walk return-address dumps.
	symbolMap string

	// Remote debugger TCP port. Connect with `nc localhost N` for
	// a ZRCP-style line-based protocol (help / pause / continue /
	// step / set-breakpoint / get-registers / get-memory / etc).
	debuggerPort        int
	debuggerHistory     int
	debuggerHistoryWide bool

	// Start with the CPU paused — handy when you want to set
	// breakpoints before the boot ROM has executed even one
	// instruction.
	debuggerPauseAtStart bool

	// noSound silences the audio system (no oto context, no beeper,
	// no AY, no DAC mix). Useful while debugging — long
	// debugger-driven sessions where the beeper sings under your
	// step traces. Also reduces CPU overhead for headless runs.
	noSound bool

	// recordAudio, when non-empty, begins WAV capture of the mixed
	// audio output at startup (the exact stream sent to the device).
	// A diagnostic for audio artefacts (clicks/pops) that only appear
	// in the live GUI: the captured file is ground truth for what we
	// emit, independent of the host's Core Audio / speaker behaviour.
	recordAudio string

	// audioKeepAlive overrides the sub-audible keep-alive dither level
	// (peak amplitude in 16-bit units) that stops the host audio amp
	// powering down during silence and popping on the next sound. -1
	// means "use the built-in default"; 0 disables it.
	audioKeepAlive int

	// audioDCBlock toggles the audio DC-blocking high-pass. On by default
	// (removes the idle DC rail / battery-click); --audio-dc-block=false
	// emits raw ±beeper levels — an A/B diagnostic for beeper-audio fidelity.
	audioDCBlock bool

	// warmBoot opts INTO the captured-reference-state warm-boot path,
	// which bypasses the real FPGA bootrom → TBBLUE.FW → NextZXOS
	// chain and loads pre-captured CPU/RAM/NextRegs state directly.
	// This is a DEBUG SHORTCUT — useful for testing post-boot
	// behaviour (Browser, .NEX loading) without waiting for the
	// real cold-boot path to clear all its remaining issues. The
	// real boot path is the default; this flag flips on the
	// shortcut.
	warmBoot bool

	// sdWriteback opts INTO persisting guest writes on the mounted
	// SD image back to the image file at exit (with a .bak backup
	// of the previous file). Default OFF: a crashed or killed
	// session must never be able to corrupt the user's only card
	// image, so in-session writes live in RAM unless asked for.
	sdWriteback bool

	// Capture-Next-snapshot mode: when non-empty, connects to a
	// running the reference emulator on the given host:port (defaults to
	// localhost:10000 if just ":N" or unset host), captures its
	// current Machine RAM + NextReg state + CPU registers, and
	// writes them into roms/next/ as the warm-boot snapshot files.
	// Then exits. Use after launching the reference emulator with the NextZXOS
	// distro and letting it boot to the post-init steady state.
	// See docs/spectrum-next.md for the full procedure.
	captureNextSnapshot string

	// nextBisect, when non-empty, runs the first-divergence bisection of
	// our Next boot vs a live reference emulator, then exits. Format:
	// "anchorPC,lo,hi,refEmuDir,sdImage" (anchorPC hex). DEV-ONLY.
	nextBisect string

	// nextLockstep, when non-empty, runs the instruction-level PC-lockstep
	// of our Next boot vs a live reference emulator from a matching checkpoint,
	// then exits. Format: 'anchorPC,hit,refEmuDir,sdImage,maxSteps'.
	nextLockstep string

	// nextNRDiff, when non-empty, dumps every NextReg READ-BACK value
	// ($00-$FF) from our Next and a live reference emulator at the n-th hit of
	// an anchor PC and prints the registers that differ, then exits.
	// Format: 'anchorPC,hit,refEmuDir,sdImage'.
	nextNRDiff string

	// nextMemDiff, when non-empty, compares the full 64 KB logical memory
	// image of our Next vs a live reference emulator at the n-th hit of an
	// anchor PC and prints the differing ranges, then exits. Aimed at the
	// last register-matched checkpoint, where a memory diff isolates a
	// stale cell forking the path. Format: 'anchorPC,hit,refEmuDir,sdImage'.
	nextMemDiff string

	// crashDetect: master enable for the heuristic crash detector.
	// When true (and no per-heuristic override flag is set), the
	// detector activates defaultCrashConfig — NOP-slide threshold
	// 32, SP-low $4000, HALT-with-IFF1=0. Per-heuristic flags below
	// override the corresponding default; set one of them on its
	// own (without --crash-detect) to enable just that heuristic.
	crashDetect       bool
	crashNopSlide     int
	crashSPLow        uint16
	crashSPHigh       uint16
	crashPCRegions    []crashRegion
	crashHaltNoIFF    bool
	crashHaltNoIFFOff bool

	// timeTravel: when > 0, auto-capture a full snapshot every N
	// instructions in headless mode. timeTravelKeep bounds the ring.
	timeTravel     int
	timeTravelKeep int

	// provenance: arm the last-writer index (Tool #1) from boot, so
	// mid-boot writes are recorded and `why-pc` / `provenance ADDR`
	// can walk a bad-jump / corrupt-stack trap back to its cause.
	provenance bool

	// whyPCAt: when non-empty, arm provenance and dump a why-pc trace
	// the FIRST time PC enters this address — the instant a bad jump
	// lands, before a self-loop trap obscures the stack. Form: $ADDR.
	whyPCAt string

	// traceDB: when non-empty, record M1 fetches into a bounded ring
	// and flush them to this SQLite file at end-of-run for ad-hoc SQL
	// queries (Tool #2). traceDBKeep bounds the ring (most-recent kept).
	traceDB     string
	traceDBKeep int

	// readTape: when non-empty, record a value-level RAM-read tape to
	// this file (the instruction-level lockstep facility). Recording
	// arms at readTapeFrom (n-th execution of a PC); readTapeMax caps
	// lines. Compared against an oracle's tape via _tools/tapediff to
	// find the first read whose VALUE diverges. See readtape.go.
	readTape     string
	readTapeFrom string // "$PC[:hit]" arming checkpoint (empty = from boot)
	readTapeMax  int
	readTapeAll  bool // log EVERY read (logical addr) not just RAM — bank-agnostic vs the reference emulator

	// tape: when set with --headless, load this .tap/.tzx into the virtual
	// deck at startup and start it playing (the headless equivalent of the
	// GUI "Open .tap" path). Empty = no tape mounted.
	tape string
}

// parseCLI reads os.Args once and returns a populated cliFlags.
// On error (unknown flag, bad value) it prints usage to stderr
// and exits with status 2 — the standard `flag` package
// convention.
func parseCLI() *cliFlags {
	var (
		logLevelStr     = flag.String("log-level", "info", "Log level: debug, info, warn, error")
		traceStr        = flag.String("trace", "", "Comma-separated trace channels: pc, nextreg, ports, bankswitch, screen")
		tracePCRange    = flag.String("trace-pc-range", "", "PC range filter for pc channel, e.g. $0D6B-$0D80 or 0x1F00-0x1F50")
		traceOutput     = flag.String("trace-output", "", "Path to write JSON-lines trace events; empty = stderr")
		headless        = flag.Bool("headless", false, "Run without GUI; combine with --frames to exit after N simulated frames")
		frames          = flag.Int("frames", 0, "When --headless, exit after this many simulated frames; 0 = run forever")
		dumpState       = flag.Int("dump-state", 0, "When non-zero, run headless for N frames then print CPU/sysvar/NextReg snapshot and exit")
		saveScreen      = flag.String("save-screen", "", "When set with --headless, write the final rendered screen to this PNG path before exiting")
		startInNext     = flag.Bool("next", false, "Boot directly into ZX Spectrum Next mode (skips the 48K default)")
		startInZX81     = flag.Bool("zx81", false, "Boot directly into Sinclair ZX81 mode")
		startInZX80     = flag.Bool("zx80", false, "Boot directly into Sinclair ZX80 mode")
		startInPentagon = flag.Bool("pentagon", false, "Boot directly into Pentagon 128 mode")
		startInSAM      = flag.Bool("sam", false, "Boot directly into SAM Coupé mode")
		trdPath         = flag.String("trd", "", "Mount a TR-DOS .TRD disk image in Beta drive A at startup (classic models, e.g. with --pentagon)")
		tapePath        = flag.String("tape", "", "Load a .tap/.tzx tape into the deck at startup and start playing (works in --headless; on the 48K type LOAD\"\" or use the 128 Tape Loader to read it)")

		snapshotEvery   = flag.Int("snapshot-every", 0, "Print a state snapshot (CPU, sysvars, paging, watched memory) every N frames in headless mode")
		snapshotPrefix  = flag.String("snapshot-prefix", "", "When set, write per-snapshot screenshots to <prefix><frame-number>.png")
		snapshotScreens = flag.Bool("snapshot-screens", false, "When --snapshot-prefix is set, save the rendered screen at each snapshot")

		watchSpec           = flag.String("watch", "", "Comma-separated name:addr pairs for memory-watch in snapshots, e.g. 'ERR_NR:5C3A,CH-ADD:5C5D'")
		logSDCommands       = flag.Bool("log-sd", false, "Log every SD-card SPI command dispatched to the virtual card")
		logBankSwitches     = flag.Bool("log-bank-switches", false, "Log every ROM-bank switch and MMU-slot reassignment")
		watchWrites         = flag.String("watch-writes", "", "Comma-separated NAME@ADDR[=VAL] pairs; every write logs with the source PC, and an optional =VAL turns the entry into a break-on-write that dumps state and exits")
		detectUninit        = flag.Bool("detect-uninit", false, "Enable the unified-RAM uninitialised-read detector: a guest read of a RAM byte never written since power-on logs its source PC + pool (bank16k,off). Reference-free; answers 'is this garbage value from uninit RAM or computed?'. Combine with --detect-uninit-pc and --detect-uninit-cap.")
		uninitPCRange       = flag.String("detect-uninit-pc", "", "Restrict --detect-uninit logging to reads whose PC is in this window, e.g. $1E00-$1FFF or 0x1E00-0x1FFF (hi optional).")
		uninitCap           = flag.Int("detect-uninit-cap", 200, "Max distinct (pc,addr) uninit-read reports to log before suppressing (default 200).")
		watchBankWrite      = flag.String("watch-bank-write", "", "Log guest writes to unified-pool BANK:LO-HI (bank decimal, offsets hex), keyed on pool location so paging can't hide the target bank. e.g. 111:$201C-$202B")
		dumpMemSpec         = flag.String("dump-mem", "", "Comma-separated ADDR:LEN memory ranges to print on dump-state / break (hex)")
		pressKeySpec        = flag.String("press-key", "", "Comma-separated KEY@FRAME[,KEY@FRAME…] schedule to drive guest software in headless runs")
		rebootAt            = flag.Int("reboot-at", 0, "When >0, trigger an in-headless Reboot at the given frame (same code path as the Emulator → Reboot GUI menu item). Use to test that the reboot cleanly re-enters the FPGA bootrom + clears stale NextRegs state.")
		snapshotOnPC        = flag.String("snapshot-on-pc", "", "Comma-separated NAME@ADDR (or NAME@LO-HI) PC triggers; emit a full snapshot the first time PC enters one")
		loopThreshold       = flag.Int("loop-threshold", 0, "When >0, emit a stall snapshot if the same PC fires this many consecutive M1 fetches without any intervening PC change")
		symbolMap           = flag.String("symbol-map", "", "Path to a symbol map file (lines of '$XXXX NAME') used to annotate disasm + stack output")
		debuggerPort        = flag.Int("debugger-port", 0, "When >0, open a ZRCP-style telnet debugger on this TCP port (e.g. 10000)")
		debuggerHistory     = flag.Int("debugger-history", 0, "When >0, record the last N M1 fetches in a ring buffer accessible via the debugger's `history` / `prev` commands. Sensible defaults: 1024 (cheap) to 65536 (~1.5 MB). 0 disables; the pre-fetch hook isn't installed in that case so there's no runtime cost.")
		debuggerHistoryWide = flag.Bool("debugger-history-wide", false, "Capture BC/DE/HL/IX/IY into every history entry as well as the base PC/SP/A/F set. Costs an extra 10 bytes per entry; needed for `what was IX at insn N` style diagnostics. No-op unless --debugger-history > 0.")
		debuggerPause       = flag.Bool("debugger-pause-at-start", false, "When --debugger-port is set, start with the CPU paused so breakpoints can be set before any instruction runs")
		captureNextSnapshot = flag.String("capture-next-snapshot", "", "Capture warm-boot snapshot from a running reference Next emulator. Argument is HOST:PORT (e.g. localhost:10000) — connect to its ZRCP. Captures the full Machine RAM (2 MB) + all 256 NextRegs + CPU registers; writes them into the install dir for zx_go to use on subsequent boots. Exits when done. See docs/spectrum-next.md for the procedure.")
		noSound             = flag.Bool("no-sound", false, "Mute audio output (no oto context, no beeper / AY / DAC mix). Useful during long debugger sessions where the beeper sings under step traces, and for headless runs that don't need audio.")
		recordAudio         = flag.String("record-audio", "", "Capture the mixed audio output to this WAV path from startup (the exact stream sent to the device). Diagnostic for clicks/pops that only appear live — the file is ground truth for what we emit.")
		audioKeepAlive      = flag.Int("audio-keepalive-level", -1, "Peak amplitude (16-bit units, out of 32767) of the keep-alive dither that stops the macOS speaker amp powering down during silence and popping on the next sound. -1 = built-in default (off); set e.g. 16/24 to enable if the startup/load pop bothers you (trades a faint noise floor).")
		audioDCBlock        = flag.Bool("audio-dc-block", true, "Apply the DC-blocking high-pass to audio output (removes the idle DC rail / 'battery click'). Use --audio-dc-block=false to emit raw beeper levels — an A/B diagnostic for beeper-audio fidelity.")
		sdWriteback         = flag.Bool("sd-writeback", false, "Persist guest writes on the mounted SD image back to the image file at exit (previous file kept as .bak). Default: writes live in RAM only.")
		warmBoot            = flag.Bool("warm-boot", false, "Opt INTO the captured-reference-state warm-boot path (DEBUG SHORTCUT — bypasses real FPGA bootrom → NextZXOS chain). Default is the real cold-boot path. Requires the warm-boot snapshot files to be installed; capture via --capture-next-snapshot.")
		nextBisect          = flag.String("next-bisect", "", "DEV: first-divergence bisection of our Next boot vs a live reference emulator, then exit. Format: 'anchorPC,lo,hi,refEmuDir,sdImage' (anchorPC hex, e.g. $3E00). Compares SP+regs+MMU8 at the n-th hit of anchorPC, kill+relaunching the reference emulator per probe.")
		nextLockstep        = flag.String("next-lockstep", "", "DEV: instruction-level PC-lockstep of our Next boot vs a live reference emulator from a matching checkpoint, then exit. Format: 'anchorPC,hit,refEmuDir,sdImage,maxSteps'. Steps both one instruction at a time and reports the FIRST instruction where the reference emulator doesn't follow our predicted path (our emulator is the next-PC oracle).")
		nextNRDiff          = flag.String("next-nrdiff", "", "DEV: at the n-th hit of an anchor PC, dump every NextReg READ-BACK value ($00-$FF) from our Next and a live reference emulator and print the registers that differ, then exit. Format: 'anchorPC,hit,refEmuDir,sdImage'. Finds NextReg read-back faithfulness gaps that derail the boot.")
		nextMemDiff         = flag.String("next-memdiff", "", "DEV: at the n-th hit of an anchor PC, compare the full 64 KB logical memory image of our Next vs a live reference emulator and print the differing ranges, then exit. Format: 'anchorPC,hit,refEmuDir,sdImage'. Point it at the last register-matched checkpoint: a memory diff there isolates a stale cell forking the path; no diff redirects the hunt to PORT inputs (SD/SPI).")

		crashDetect       = flag.Bool("crash-detect", false, "Enable heuristic crash detection with conservative defaults (NOP-slide threshold 32, SP<$4000, HALT-with-IFF1=0). Per-heuristic flags below override these defaults; supply them without --crash-detect to enable just one. Fires once per heuristic per re-arm window — emits a snapshot + slog at DEBUG.")
		crashNopSlide     = flag.Int("crash-nop-slide", 0, "Crash heuristic: fire after N consecutive $00 opcode fetches (a PC walking through zero-filled memory). Sensible values 32-256; 0 disables. Overrides --crash-detect default.")
		crashSPLowStr     = flag.String("crash-sp-low", "", "Crash heuristic: fire if SP drops below this address (hex; $XXXX / 0xXXXX). Detects stack underrun (popped past the stack base). 0/empty disables. Overrides --crash-detect default.")
		crashSPHighStr    = flag.String("crash-sp-high", "", "Crash heuristic: fire if SP rises above this address (hex). Detects stack underflow wrap. Opt-in only; off by default. Overrides --crash-detect default.")
		crashPCRegionStr  = flag.String("crash-pc-region", "", "Crash heuristic: fire when PC enters any of these regions. Format NAME@LO-HI[,NAME@LO-HI]. Use for forbidden code regions (screen-RAM execution, etc). Opt-in only.")
		crashHaltNoIFF    = flag.Bool("crash-halt-no-iff", false, "Crash heuristic: fire on HALT executed with IFF1=0 — only NMI can wake the CPU, in practice a true deadlock. Overrides --crash-detect default.")
		crashHaltNoIFFOff = flag.Bool("crash-halt-no-iff-off", false, "Force-disable the HALT-with-IFF1=0 heuristic even when --crash-detect is set. Useful when the boot ROM legitimately HALTs waiting for NMI.")

		timeTravel     = flag.Int("time-travel", 0, "Auto-capture a full-state snapshot every N Z80 instructions into an in-memory ring. Use with --time-travel-keep=K to bound ring size. 0 disables; sensible values 10000-200000. Coexists with the runtime telnet `tt-*` commands.")
		timeTravelKeep = flag.Int("time-travel-keep", 16, "Maximum number of time-travel snapshots to retain (oldest evicted). Memory budget: K × ~128 KB on classic, K × ~128 KB on Next (visible 64 K only — upper banks/NextRegs are Phase 2). No-op without --time-travel > 0.")

		provenance  = flag.Bool("provenance", false, "Arm the backward provenance tracer from boot: record the last instruction (PC + counter) to write each memory byte. Pairs with the debugger `why-pc` and `provenance $ADDR` commands to walk a bad-jump / corrupt-stack trap back to its cause. When a headless run ends with PC in a known trap, the end-of-run dump prints who pushed the return word.")
		whyPCAt     = flag.String("why-pc-at", "", "Arm provenance and emit a why-pc trace the FIRST time PC enters $ADDR — captures the stack at the instant a bad jump lands, before a self-loop trap obscures it. Use to crack bank-2 $0000-style traps. Form: $0000.")
		traceDB     = flag.String("trace-db", "", "Record M1 fetches into a bounded ring and flush them to this SQLite file at end-of-run (Tool #2). Query with the sqlite3 CLI: e.g. cross-bank transfers, SP-collapse, hot PCs. Pairs with --trace-db-keep.")
		traceDBKeep = flag.Int("trace-db-keep", 500000, "Max M1 rows retained in the --trace-db ring (most-recent kept). ~80 bytes/row in memory.")

		readTape     = flag.String("read-tape", "", "Record a value-level RAM-read tape to this file (instruction-level lockstep). Once two emulators share an identical state at a checkpoint, only a divergent READ can split them — diff this tape against an oracle's via _tools/tapediff to find the first read whose VALUE differs. Arm with --read-tape-from; cap with --read-tape-max.")
		readTapeFrom = flag.String("read-tape-from", "", "Arm --read-tape at the n-th execution of a PC, form $PC[:hit] (e.g. $3E00:2 = the 2nd time PC=$3E00). Empty = record from boot.")
		readTapeMax  = flag.Int("read-tape-max", 2000000, "Max --read-tape lines recorded before stopping (0 = unbounded).")
		readTapeAll  = flag.Bool("read-tape-all", false, "With --read-tape: log EVERY read (logical addr + value), not just RAM reads. Bank-agnostic — for diffing against a reference emulator (e.g. the reference emulator) that logs all reads by logical address, when code is banked through a window (divMMC $2000-$3FFF). ROM reads match by construction; the first divergent VALUE is the bug.")
	)
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		_, _ = fmt.Fprintf(out, "zx_go %s — ZX Spectrum emulator (48K / 128K / +2 / +2A / +3 / Next)\n",
			zxlog.Version)
		_, _ = fmt.Fprintf(out, "Copyright (C) 2026 Conor Armstrong.\n")
		_, _ = fmt.Fprintf(out, "Source: https://github.com/conorarmstrong/zx_go\n")
		_, _ = fmt.Fprintf(out, "\nUsage: %s [flags]\n", os.Args[0])
		_, _ = fmt.Fprintf(out, "\nWith no flags, launches the Fyne GUI. Use --headless for\n")
		_, _ = fmt.Fprintf(out, "scripted / batch / CI use; --debugger-port for interactive\n")
		_, _ = fmt.Fprintf(out, "live debugging over telnet; --trace=... for high-throughput\n")
		_, _ = fmt.Fprintf(out, "JSON-lines event capture.\n")
		_, _ = fmt.Fprintf(out, "\nFlags:\n")
		flag.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nFor full documentation and the Spectrum Next install guide\n")
		_, _ = fmt.Fprintf(out, "see the README and docs/ directory in the repo.\n")
	}
	// Provide --version explicitly so it prints just the version and exits,
	// independent of the help flow.
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Printf("zx_go %s (built %s)\n", zxlog.Version, version.Date())
		os.Exit(0)
	}

	f := &cliFlags{}

	switch strings.ToLower(*logLevelStr) {
	case "debug":
		f.logLevel = slog.LevelDebug
	case "info":
		f.logLevel = slog.LevelInfo
	case "warn", "warning":
		f.logLevel = slog.LevelWarn
	case "error":
		f.logLevel = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "invalid --log-level %q (want: debug, info, warn, error)\n", *logLevelStr)
		os.Exit(2)
	}

	ch, err := trace.ParseChannels(*traceStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	f.traceChannels = ch

	if *tracePCRange != "" {
		start, end, err := parsePCRange(*tracePCRange)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		f.tracePCStart = start
		f.tracePCEnd = end
		f.tracePCRange = true
	}

	f.traceOutput = *traceOutput
	f.headless = *headless
	f.frames = *frames
	f.dumpState = *dumpState
	f.saveScreen = *saveScreen
	f.startInNext = *startInNext
	f.startInZX81 = *startInZX81
	f.startInZX80 = *startInZX80
	f.startInPentagon = *startInPentagon
	f.startInSAM = *startInSAM
	f.trdPath = *trdPath
	f.tape = *tapePath
	f.snapshotEvery = *snapshotEvery
	f.snapshotPrefix = *snapshotPrefix
	f.snapshotScreens = *snapshotScreens
	f.watchSpec = *watchSpec
	f.logSDCommands = *logSDCommands
	f.logBankSwitches = *logBankSwitches
	f.watchWrites = *watchWrites
	f.detectUninit = *detectUninit
	f.uninitPCRange = *uninitPCRange
	f.uninitCap = *uninitCap
	f.watchBankWrite = *watchBankWrite
	f.dumpMem = *dumpMemSpec
	f.pressKey = *pressKeySpec
	f.rebootAt = *rebootAt
	f.snapshotOnPC = *snapshotOnPC
	f.loopThreshold = *loopThreshold
	f.symbolMap = *symbolMap
	f.debuggerPort = *debuggerPort
	f.debuggerHistory = *debuggerHistory
	f.debuggerHistoryWide = *debuggerHistoryWide
	f.debuggerPauseAtStart = *debuggerPause
	f.captureNextSnapshot = *captureNextSnapshot
	f.noSound = *noSound
	f.recordAudio = *recordAudio
	f.audioKeepAlive = *audioKeepAlive
	f.audioDCBlock = *audioDCBlock
	f.warmBoot = *warmBoot
	f.sdWriteback = *sdWriteback
	f.nextBisect = *nextBisect
	f.nextLockstep = *nextLockstep
	f.nextMemDiff = *nextMemDiff
	f.nextNRDiff = *nextNRDiff

	f.crashDetect = *crashDetect
	f.crashNopSlide = *crashNopSlide
	if *crashSPLowStr != "" {
		v, err := parseHex(*crashSPLowStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --crash-sp-low %q: %v\n", *crashSPLowStr, err)
			os.Exit(2)
		}
		f.crashSPLow = v
	}
	if *crashSPHighStr != "" {
		v, err := parseHex(*crashSPHighStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --crash-sp-high %q: %v\n", *crashSPHighStr, err)
			os.Exit(2)
		}
		f.crashSPHigh = v
	}
	if regs, errs := parseCrashRegions(*crashPCRegionStr); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "%s\n", e)
		}
		os.Exit(2)
	} else {
		f.crashPCRegions = regs
	}
	f.crashHaltNoIFF = *crashHaltNoIFF
	f.crashHaltNoIFFOff = *crashHaltNoIFFOff

	f.timeTravel = *timeTravel
	f.timeTravelKeep = *timeTravelKeep
	f.provenance = *provenance
	f.traceDB = *traceDB
	f.traceDBKeep = *traceDBKeep
	f.readTape = *readTape
	f.readTapeFrom = *readTapeFrom
	f.readTapeMax = *readTapeMax
	f.readTapeAll = *readTapeAll
	f.whyPCAt = *whyPCAt
	if f.whyPCAt != "" {
		f.provenance = true // why-pc-at implies the tracker
	}

	if f.dumpState > 0 {
		// --dump-state implies headless with --frames=N.
		f.headless = true
		if f.frames == 0 {
			f.frames = f.dumpState
		}
	}

	// Any of the debug-instrumentation flags implies --log-level=debug
	// unless the user explicitly chose a level. The debug events live at
	// slog.LevelDebug so they don't pollute the default Info-level
	// output, but they're nearly invisible without this auto-bump.
	logLevelExplicit := false
	flag.Visit(func(fl *flag.Flag) {
		if fl.Name == "log-level" {
			logLevelExplicit = true
		}
	})
	wantsDebug := f.snapshotEvery > 0 ||
		f.logBankSwitches ||
		f.logSDCommands ||
		f.watchWrites != "" ||
		f.snapshotOnPC != "" ||
		f.loopThreshold > 0 ||
		f.symbolMap != "" ||
		crashConfigFromFlags(f).enabled() ||
		f.timeTravel > 0 ||
		f.detectUninit ||
		f.watchBankWrite != "" ||
		f.provenance ||
		f.traceDB != "" ||
		f.readTape != ""
	if wantsDebug && !logLevelExplicit {
		f.logLevel = slog.LevelDebug
	}

	cliFlagsActive = f
	return f
}

// parsePCRange decodes the START-END syntax accepted by
// --trace-pc-range. Both endpoints accept hex with $ or 0x prefix,
// or decimal. The end is inclusive — "$0D6B-$0D6B" captures one
// instruction.
func parsePCRange(s string) (uint16, uint16, error) {
	dash := strings.Index(s, "-")
	if dash < 0 {
		return 0, 0, fmt.Errorf("--trace-pc-range %q: expected START-END", s)
	}
	startS, endS := strings.TrimSpace(s[:dash]), strings.TrimSpace(s[dash+1:])
	start, err := parseAddr(startS)
	if err != nil {
		return 0, 0, fmt.Errorf("--trace-pc-range start %q: %w", startS, err)
	}
	end, err := parseAddr(endS)
	if err != nil {
		return 0, 0, fmt.Errorf("--trace-pc-range end %q: %w", endS, err)
	}
	if end < start {
		return 0, 0, fmt.Errorf("--trace-pc-range %q: end before start", s)
	}
	return start, end, nil
}

// parseAddr accepts $XXXX, 0xXXXX, or decimal address strings.
// Returns the 16-bit value; errors on overflow or syntax.
func parseAddr(s string) (uint16, error) {
	s = strings.TrimSpace(s)
	var base = 10
	switch {
	case strings.HasPrefix(s, "$"):
		base = 16
		s = s[1:]
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		base = 16
		s = s[2:]
	}
	v, err := strconv.ParseUint(s, base, 16)
	if err != nil {
		return 0, err
	}
	return uint16(v), nil
}
