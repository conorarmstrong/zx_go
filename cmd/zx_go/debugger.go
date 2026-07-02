package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// remoteDebugger implements a ZRCP-compatible (subset) telnet
// remote-debug protocol. Connect with `nc localhost <port>` or
// `telnet localhost <port>`; one command per line. Multiple
// concurrent connections are allowed but only one connection at
// a time may issue control commands (pause/continue/step) — the
// others see an "OK" response when their commands run. Memory
// and register reads are concurrent-safe because the CPU is
// paused while commands execute.
//
// Protocol design:
//   - Plain-text, line-based (LF or CRLF terminated)
//   - Command words are case-insensitive; hex arguments accept
//     "$XXXX", "0xXXXX", or bare hex.
//   - Each command emits exactly one line of output, prefixed
//     with "OK " on success or "ERR " on failure. (ZRCP doesn't
//     do this, but it makes scripted scraping massively easier.)
//
// Implementation:
//   - One goroutine accepts connections.
//   - One goroutine per connection reads commands.
//   - Commands acquire dbg.mu, mutate state, release, write a
//     response. The CPU is paused whenever a command runs —
//     ensured by the headless-loop integration (see WaitIfPaused).
//   - Breakpoints are enforced via the CPU's BreakpointCheck
//     callback: when PC hits a breakpoint or `stepping` is set,
//     BreakpointCheck returns true, ExecuteFrame exits, and the
//     headless loop transitions back to paused.
//
// cpuState satisfies debugger.State for condition evaluation. The
// register name set matches what ParseCondition accepts. Memory
// reads go through Memory.Read so MMU paging / divMMC overlay /
// alt-ROM redirect resolve the same way the guest sees them — a
// condition like `read $5C78 == $00` checks what guest code would
// read, not the underlying RAM bank.
type cpuState struct {
	cpu  *z80.CPU
	mem  *memory.Memory
	bank byte
	// divMMC paging snapshot, captured at evaluation time. Lets a
	// breakpoint guard target the divMMC-paged context faithfully
	// (`if dmmc == 1`) instead of the `iy < $8000` heuristic used to
	// dodge low-PC bank aliasing. Field names match get-divmmc.
	dmmcPaged   bool
	dmmcAutomap bool
	dmmcMapram  bool
	dmmcConmem  bool
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// divmmcFlagsFrom extracts the divMMC paging snapshot from a pager
// value via the same duck-typed interfaces get-divmmc uses. conmem is
// E3 bit 7. A nil pager (classic models / no divMMC) yields all-false.
func divmmcFlagsFrom(pager any) (paged, automap, mapram, conmem bool) {
	if pager == nil {
		return
	}
	if x, ok := pager.(interface{ IsPagedIn() bool }); ok {
		paged = x.IsPagedIn()
	}
	if x, ok := pager.(interface{ AutomapEnabled() bool }); ok {
		automap = x.AutomapEnabled()
	}
	if x, ok := pager.(interface{ MAPRAM() bool }); ok {
		mapram = x.MAPRAM()
	}
	if x, ok := pager.(interface{ LastE3() byte }); ok {
		conmem = x.LastE3()&0x80 != 0
	}
	return
}

// divmmcStateOf reads the current divMMC paging snapshot for an
// emulator, safe on classic models (nil ula/pager → all false).
func divmmcStateOf(emu *emulator) (paged, automap, mapram, conmem bool) {
	if emu == nil || emu.ula == nil {
		return
	}
	return divmmcFlagsFrom(emu.ula.NextDivMMC())
}

// condState builds the cpuState a Condition / RegWatch evaluates
// against, capturing the live divMMC paging snapshot so guards like
// `if dmmc == 1` resolve faithfully without the `iy < $8000` heuristic.
func (d *remoteDebugger) condState(bank byte) cpuState {
	paged, automap, mapram, conmem := divmmcStateOf(d.emu)
	return cpuState{
		cpu: d.emu.cpu, mem: d.emu.mem, bank: bank,
		dmmcPaged: paged, dmmcAutomap: automap,
		dmmcMapram: mapram, dmmcConmem: conmem,
	}
}

func (s cpuState) Reg(name string) (int64, bool) {
	c := s.cpu
	switch name {
	case "pc":
		return int64(c.PC), true
	case "sp":
		return int64(c.SP), true
	case "a":
		return int64(c.A), true
	case "f":
		return int64(c.F), true
	case "b":
		return int64(c.B), true
	case "c":
		return int64(c.C), true
	case "d":
		return int64(c.D), true
	case "e":
		return int64(c.E), true
	case "h":
		return int64(c.H), true
	case "l":
		return int64(c.L), true
	case "ix":
		return int64(c.IX), true
	case "iy":
		return int64(c.IY), true
	case "iff1":
		if c.IFF1 {
			return 1, true
		}
		return 0, true
	case "iff2":
		if c.IFF2 {
			return 1, true
		}
		return 0, true
	case "im":
		return int64(c.IM), true
	case "halted":
		if c.Halted {
			return 1, true
		}
		return 0, true
	case "bank":
		return int64(s.bank), true
	case "dmmc":
		return b2i(s.dmmcPaged), true
	case "automap":
		return b2i(s.dmmcAutomap), true
	case "mapram":
		return b2i(s.dmmcMapram), true
	case "conmem":
		return b2i(s.dmmcConmem), true
	}
	return 0, false
}

func (s cpuState) ReadMem(addr uint16) byte { return s.mem.Read(addr) }

type remoteDebugger struct {
	// mu guards remoteDebugger-local mutable state (tracepoint map,
	// etc). Breakpoints now live in the shared, lock-free
	// debugger.BreakpointSet (d.bps), so BreakpointCheck stays
	// lock-free on the hot path (every M1 fetch).
	mu sync.Mutex

	emu      *emulator
	paused   atomic.Bool
	stepping atomic.Bool

	// pauseAckTimeoutMS bounds how long a state-touching command waits
	// for the headless loop to acknowledge a pause before giving up.
	// 0 = the 2s default. Raise it (via `set-pause-timeout`) when
	// awaiting a `continue` to a deep breakpoint in a long boot, where
	// reaching the BP can take several seconds of wall-clock.
	pauseAckTimeoutMS atomic.Int64

	// sdBreak, when non-nil, is a one-shot SD-command breakpoint
	// (`break-on-sd`). checkSDCommand — fed from the SD card's command
	// logger — pauses the CPU when a received command matches. Used to
	// catch the NextZXOS DOS driver's mount commands, whose code runs
	// in a paged region where static-address breakpoints don't fire.
	sdBreak atomic.Pointer[sdBreakSpec]

	// bps is the SHARED breakpoint store (emulator-owned), also used
	// by the visual debugger — a breakpoint set here shows up and
	// fires there and vice versa. Lock-free Lookup on the hot path.
	bps *debugger.BreakpointSet

	// resumeCh signals the headless loop to wake from its pause-
	// wait and continue executing. Buffered=1 so a pause that
	// hasn't yet hit the wait doesn't lose a wake.
	resumeCh chan struct{}

	// stepDoneCh signals from the headless loop back to the TCP
	// handler that a single-step has completed. The step command
	// waits on this so the next read sees the post-step state.
	stepDoneCh chan struct{}

	// pauseAckCh is closed each time the headless loop enters
	// WaitIfPaused — i.e. when the CPU has definitively stopped.
	// TCP commands wait on a fresh receive to confirm the CPU is
	// paused before reading state, eliminating the race where a
	// command would read CPU.PC mid-instruction. Re-allocated
	// every pause cycle so each waiter gets exactly one signal.
	pauseAckMu sync.Mutex
	pauseAckCh chan struct{}

	listener     net.Listener
	port         int
	pauseAtStart bool

	// history is an optional CPU pre-fetch ring buffer recording
	// PC + register-state-of-interest at every M1 fetch. Used by
	// the `history` / `prev` commands to inspect what code led up
	// to a breakpoint hit. Constructed when --debugger-history is
	// non-zero; nil otherwise (and the hook isn't installed, so
	// there's no hot-path cost).
	history *debugger.History

	// regWatches is the set of register-change watchpoints. Empty
	// by default; populated via the `watch-reg` command. Evaluated
	// on every M1 fetch by the BreakpointCheck hot path (guarded by
	// Empty() so the cost is zero when no watches are active).
	regWatches *debugger.RegWatchSet

	// stepOverPC is the tentative one-shot breakpoint set by the
	// `step-over` command at "instruction after the CALL we're
	// stepping over". When non-nil, BreakpointCheck fires once at
	// this PC and immediately clears the pointer. nil = no pending
	// step-over.
	stepOverPC atomic.Pointer[uint16]

	// memWatchInstalled tracks whether the chained RAM-write hook
	// has been planted. Set by ensureMemWatchHook on first
	// `watch-mem`; the hook itself stays in place for the session
	// (cheap when the spec pointer is nil — the env-var diagnostic
	// tracer continues to receive the prior chain).
	memWatchInstalled bool

	// readWatchInstalled tracks whether the chained RAM-read hook
	// has been planted (by ensureReadWatchHook on first `watch-read`).
	readWatchInstalled bool

	// watchZero, when set, makes BreakpointCheck halt as soon as
	// M1 fetches from a region where the next 16 bytes ahead of PC
	// are all $00 — pinpoints a bad jump into uninitialised RAM.
	// Atomic so the hot-path read is lock-free.
	watchZero atomic.Bool

	// tracepoints is the gdb-style tracepoint store: PC -> hit
	// counter pair. When BreakpointCheck sees a hit, it logs
	// register state via slog.Info("tp-hit") and continues —
	// never halts. Set via the `tp` command. Atomic pointer for
	// lock-free hot-path read.
	tpMap atomic.Pointer[map[uint16]uint64]

	// portWatches is the live set of port-write watchpoints. The
	// tracer installed by ensurePortWatchHook evaluates these on
	// every guest port write. Internal mutex; the tracer iterates
	// the slice under the same lock the telnet commands use.
	portWatches        portWatchSet
	portWatchInstalled bool

	// nrTraces is the runtime-mutable NextReg trace set. Pairs
	// with ensureNRTraceHook which chains a dispatcher tracer on
	// top of any pre-existing one.
	nrTraces         nrTraceSet
	nrTraceInstalled bool

	// catchIRQ halts the CPU at the next interrupt-taken event.
	// Cheap when off (one atomic read at the head of cpu.interrupt
	// via a hook). irqStatsBaseline lets `irq-stats` report deltas
	// since the last reset rather than process-wide counts.
	catchIRQ             atomic.Bool
	irqStatsBaseTaken    uint64
	irqStatsBaseRejected uint64
	irqHookInstalled     bool

	// nrSnaps holds named full-NextReg captures for nr-snap /
	// nr-diff. Diff against any pair or against the live state.
	nrSnaps nrSnapStore

	// contUntilCond is the one-shot conditional-continue guard
	// (`cont-until EXPR`). When non-nil, BreakpointCheck evaluates
	// it every M1 fetch; on first true, the CPU pauses and the
	// pointer is cleared. Stored via atomic.Pointer (condition and
	// source expression together, so a fetch never observes one
	// updated without the other) so the hot path can short-circuit
	// with a single nil check.
	contUntilCond atomic.Pointer[contUntilSpec]

	// divMMCTrace is the runtime divMMC RAM write tracer. When
	// armed via `trace-divmmc-ram`, every write satisfying the
	// (bank, offset-range) filter emits slog.Info with the writer
	// PC. Replaces any prior pager.SetWriteLogger callback.
	divMMCTrace divMMCTraceState

	// bpFirstEntry is the one-shot range-trap. Non-nil means
	// BreakpointCheck halts the CPU on the first M1 fetch whose PC
	// matches and auto-clears. Set via `bp-first-entry`. The atomic
	// pointer makes the hot-path check a single nil load when
	// disarmed (= one CAS-equivalent op per fetch).
	bpFirstEntry atomic.Pointer[bpFirstEntrySpec]

	// traceWrites is the live CPU-write tracer (`trace-writes`).
	// Captures every Memory.Write inside the spec range with the
	// resolved MMU destination, so config-mode vs MMU8 routing is
	// distinguishable in the log.
	traceWrites traceWritesState

	// nrDeltas is the NextReg-transition tracer. Cuts hot-NR write
	// volume (e.g. NR$04 = 65k+ writes per boot) to only the actual
	// value-changes, making it usable for finding "when did NR$56
	// transition from $DE to $00?" without grep-ing thousands of
	// repeating entries.
	nrDeltas nrDeltaState

	// crashDetect: runtime-attached heuristic crash detector. nil
	// when not installed. Toggled via the `crash-detect` command;
	// fires emit a snapshot + log line and (optionally) pause the
	// CPU at the offending PC for inspection.
	crashDetect      *crashDetector
	crashDetectPause atomic.Bool

	// provenance: last-writer index answering "which instruction
	// wrote this byte?" (Tool #1). Armed via the `provenance on`
	// command (or the --provenance flag); the `provenance ADDR` and
	// `why-pc` commands walk the causal chain behind a bad jump /
	// corrupt-stack trap. Always allocated (the table is the
	// zero-value); enabling only flips the hot-path gate.
	provenance provenanceTracker
}

// newRemoteDebugger spins up the TCP listener and registers the
// breakpoint check / pre-fetch hook on the CPU. If port is 0 or
// negative, returns nil (debugger disabled).
func newRemoteDebugger(emu *emulator, port int, pauseAtStart bool, historySize int, historyWide bool) *remoteDebugger {
	if port <= 0 {
		return nil
	}
	d := &remoteDebugger{
		emu:          emu,
		resumeCh:     make(chan struct{}, 1),
		stepDoneCh:   make(chan struct{}, 1),
		pauseAckCh:   make(chan struct{}),
		port:         port,
		pauseAtStart: pauseAtStart,
		regWatches:   emu.sharedRegWatches(),
		bps:          emu.sharedBreakpoints(),
	}
	if historySize > 0 {
		// Share the ring with the visual debugger by stashing it on
		// the emulator. If one already exists (visual opened first),
		// reuse it instead of allocating a parallel buffer.
		if emu.debugHistory == nil {
			if historyWide {
				emu.debugHistory = debugger.NewHistoryWide(historySize)
			} else {
				emu.debugHistory = debugger.NewHistory(historySize)
			}
		}
		d.history = emu.debugHistory
		wide := d.history.Wide()
		// Pre-fetch hook records every M1 fetch into the ring. Hot
		// path: ~1M fetches/sec at 28 MHz Z80N, so the entry
		// construction + ring write under a sync.Mutex is the budget
		// we have to fit. The History impl was designed for this —
		// no allocations per push, no map lookups, single mutex.
		emu.cpu.AddPreFetchHook("debugger-history", func(pc uint16) {
			c := emu.cpu
			e := debugger.HistoryEntry{
				PC:         pc,
				SP:         c.SP,
				A:          c.A,
				F:          c.F,
				IFFIM:      debugger.PackIFFIM(c.IFF1, c.IFF2, c.Halted, int(c.IM)),
				Insns:      c.InstructionCount(),
				ROMBnk:     byte(d.currentROMBank()),
				Source:     debugger.PCSource(c.BranchSource),
				SourceFrom: c.BranchFrom,
			}
			// Consume the branch latch so the NEXT M1 sees
			// sequential if no branch fires.
			c.BranchSource = 0
			c.BranchFrom = 0
			if wide {
				e.BC = c.BC()
				e.DE = c.DE()
				e.HL = c.HL()
				e.IX = c.IX
				e.IY = c.IY
			}
			d.history.Push(e)
		})
	}
	if pauseAtStart {
		d.paused.Store(true)
	}
	// BreakpointCheck returns true to pause ExecuteFrame. Hot
	// path — every M1 fetch goes through here. Reads are
	// lock-free against the bpsMap atomic.Pointer.
	emu.cpu.BreakpointCheck = func(pc uint16) bool {
		if d.stepping.Load() {
			return true
		}
		if d.paused.Load() {
			return true
		}
		// Tracepoints: log register state at PC without halting.
		// Hot-path check is a single atomic load + map lookup; the
		// map exists only when the user has armed at least one tp.
		if tpP := d.tpMap.Load(); tpP != nil {
			tp := *tpP
			if _, ok := tp[pc]; ok {
				c := emu.cpu
				slog.Info("tp-hit",
					"pc", fmt.Sprintf("$%04X", pc),
					"sp", fmt.Sprintf("$%04X", c.SP),
					"af", fmt.Sprintf("$%02X%02X", c.A, c.F),
					"bc", fmt.Sprintf("$%04X", c.BC()),
					"de", fmt.Sprintf("$%04X", c.DE()),
					"hl", fmt.Sprintf("$%04X", c.HL()),
					"ix", fmt.Sprintf("$%04X", c.IX),
					"iy", fmt.Sprintf("$%04X", c.IY),
					"iff1", c.IFF1,
					"insns", c.InstructionCount())
				// Bump the hit counter for read-back via `list-tp`.
				next := map[uint16]uint64{}
				for k, v := range tp {
					next[k] = v
				}
				next[pc]++
				d.tpMap.Store(&next)
			}
		}
		// Zero-bank tracer: halt if the next 16 bytes from PC are
		// all $00 (CPU just jumped into uninitialised RAM). Cheap
		// when off; armed only via `watch-zero on`.
		if d.watchZero.Load() {
			zero := true
			for i := uint16(0); i < 16; i++ {
				if emu.mem.Read(pc+i) != 0 {
					zero = false
					break
				}
			}
			if zero {
				d.paused.Store(true)
				slog.Debug("debugger: zero-bank tracer hit",
					"pc", fmt.Sprintf("$%04X", pc))
				d.snapshotOnBPHit(pc, "watch-zero")
				return true
			}
		}
		// One-shot step-over breakpoint. Cleared on fire so the next
		// continue starts clean.
		if sp := d.stepOverPC.Load(); sp != nil && *sp == pc {
			d.stepOverPC.Store(nil)
			d.paused.Store(true)
			d.snapshotOnBPHit(pc, "step-over")
			return true
		}
		// One-shot first-entry-into-range trap (`bp-first-entry`).
		// Halts on the first PC inside [lo, hi] and self-disarms so
		// steady-state re-entry doesn't refire. Hot-path cost is a
		// single atomic load when disarmed.
		if specP := d.bpFirstEntry.Load(); specP != nil {
			s := *specP
			if pc >= s.lo && pc <= s.hi {
				d.fireBPFirstEntry(pc, specP)
				return true
			}
		}
		// One-shot conditional-continue (`cont-until EXPR`). Evaluates
		// the user expression every M1; on first true, pause + clear.
		// Cheap when nil (one atomic load).
		if specP := d.contUntilCond.Load(); specP != nil {
			bank := d.currentROMBank()
			if specP.cond.Eval(d.condState(byte(bank))) {
				d.contUntilCond.Store(nil)
				d.paused.Store(true)
				slog.Debug("debugger: cont-until hit",
					"pc", fmt.Sprintf("$%04X", pc),
					"expr", specP.expr)
				d.snapshotOnBPHit(pc, "cont-until")
				return true
			}
		}
		// Register-change watchpoints. Cheap when empty; State
		// allocation only fires when at least one watch is active.
		if d.regWatches != nil && !d.regWatches.Empty() {
			bank := d.currentROMBank()
			if d.regWatches.Check(d.condState(byte(bank))) {
				d.paused.Store(true)
				slog.Debug("debugger: reg-watch hit", "pc", fmt.Sprintf("$%04X", pc))
				d.snapshotOnBPHit(pc, "reg-watch")
				return true
			}
		}
		entry, ok := d.bps.Lookup(pc)
		if !ok {
			return false
		}
		// Bank-filter check.
		bank := d.currentROMBank()
		if entry.Bank >= 0 && entry.Bank != bank {
			return false
		}
		// Conditional guard. cpuState satisfies debugger.State and
		// is allocated per-fire so it's stack-friendly; the hot path
		// only hits this branch when a guard is set, so the cost is
		// scoped to "user opted in".
		if entry.HasCond {
			if !entry.Cond.Eval(d.condState(byte(bank))) {
				return false
			}
		}
		d.paused.Store(true)
		slog.Debug("debugger: breakpoint hit",
			"pc", fmt.Sprintf("$%04X", pc),
			"rom_bank", bank,
			"cond", entry.Cond.String())
		d.snapshotOnBPHit(pc, "breakpoint")
		// BP-action chain. When `do "cmd1; cmd2"` was given at
		// BP creation, run each command through HandleCommand and
		// emit the response via slog.Info so the operator sees it
		// without having to issue a follow-up read. Empty actions
		// short-circuit cheaply.
		if entry.Action != "" {
			d.runBPAction(pc, entry.Action)
		}
		return true
	}
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("debugger: listen failed", "addr", addr, "err", err)
		return nil
	}
	d.listener = ln
	slog.Info("debugger listening", "port", port, "pauseAtStart", pauseAtStart)
	go d.acceptLoop()
	return d
}

func (d *remoteDebugger) acceptLoop() {
	for {
		c, err := d.listener.Accept()
		if err != nil {
			return
		}
		go d.connLoop(c)
	}
}

// currentROMBank returns the bank index (0-3) currently mapped
// at $0000-$3FFF. Used for bank-filtered breakpoints.
func (d *remoteDebugger) currentROMBank() int {
	mem := d.emu.mem
	port7FFD, port1FFD, _ := mem.GetPortState()
	return int((port7FFD>>4)&1) | int((port1FFD>>1)&2)
}

// WaitIfPaused blocks the calling goroutine (the headless loop)
// while the debugger is in paused state. Returns immediately if
// not paused. Called between ExecuteFrame iterations.
//
// On entry into the paused state, closes pauseAckCh so any TCP
// handlers waiting in waitForPauseAck see the signal. A fresh
// pauseAckCh is installed on every transition into paused so the
// "close" semantics give exactly one wake per pause cycle.
func (d *remoteDebugger) WaitIfPaused() {
	if d == nil {
		return
	}
	if !d.paused.Load() {
		return
	}
	// We're entering paused state. Signal anyone waiting.
	d.signalPauseAck()
	for d.paused.Load() {
		// Single-step: clear paused for one instruction, then re-pause.
		// Signal stepDoneCh so the TCP handler can synchronise.
		if d.stepping.CompareAndSwap(true, false) {
			d.paused.Store(false)
			// Use IRQ-aware step so debugger single-stepping matches
			// bulk-frame execution: the frame-boundary INT pulse
			// fires at the right T-state, IFF1 IRQ acceptance happens
			// at the M1 sample point, HALT can be exited on /INT.
			d.emu.cpu.StepInstructionWithIRQ()
			d.paused.Store(true)
			d.signalPauseAck()
			select {
			case d.stepDoneCh <- struct{}{}:
			default:
			}
			continue
		}
		select {
		case <-d.resumeCh:
			// A continue/resume woke us. If a state command re-paused
			// in the gap (e.g. `get-registers` issued right after
			// `continue`, before this loop got a chance to exit), the
			// loop re-checks paused=true and stays parked — but the
			// one-shot entry signalPauseAck already fired, so that
			// command's waiter would block until timeout. Re-signal so
			// its pauseAndAck (which captured the current ack channel)
			// is acknowledged. Harmless when no one is waiting.
			if d.paused.Load() {
				d.signalPauseAck()
			}
		case <-time.After(50 * time.Millisecond):
			// Tick periodically so a paused debugger doesn't burn
			// CPU spinning, and so signals from non-blocking commands
			// (e.g. set-breakpoint) don't get lost if the channel
			// was already full.
		}
	}
}

// signalPauseAck closes the current pauseAckCh and installs a
// fresh one for the next pause cycle. Called from the headless
// goroutine when the CPU has actually entered paused state.
func (d *remoteDebugger) signalPauseAck() {
	d.pauseAckMu.Lock()
	defer d.pauseAckMu.Unlock()
	close(d.pauseAckCh)
	d.pauseAckCh = make(chan struct{})
}

// waitForPauseAck blocks until the headless loop signals it has
// entered the paused state. Returns the channel that was current
// at call time (already-closed if the CPU was already paused).
// Times out after 2s to avoid deadlocking a misbehaving emulator.
func (d *remoteDebugger) waitForPauseAck() bool {
	d.pauseAckMu.Lock()
	ch := d.pauseAckCh
	d.pauseAckMu.Unlock()
	select {
	case <-ch:
		return true
	case <-time.After(d.pauseAckTimeout()):
		return false
	}
}

// pauseAckTimeout is the configured pause-ack wait, defaulting to 2s
// when unset. Settable at runtime via `set-pause-timeout`.
func (d *remoteDebugger) pauseAckTimeout() time.Duration {
	if ms := d.pauseAckTimeoutMS.Load(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return 2 * time.Second
}

// parseSDBreakSpec parses `break-on-sd` arguments: a "CMDn"/"ACMDn"
// token, optionally followed by a hex/decimal argument to match. Returns
// a non-empty error string on malformed input.
func parseSDBreakSpec(args []string) (*sdBreakSpec, string) {
	tok := strings.ToUpper(args[0])
	isACMD := false
	switch {
	case strings.HasPrefix(tok, "ACMD"):
		isACMD = true
		tok = tok[4:]
	case strings.HasPrefix(tok, "CMD"):
		tok = tok[3:]
	default:
		return nil, "usage: break-on-sd CMDn|ACMDn [arg] | off"
	}
	n, err := strconv.Atoi(tok)
	if err != nil || n < 0 || n > 63 {
		return nil, "break-on-sd: command must be CMD0..CMD63 / ACMD0..ACMD63"
	}
	spec := &sdBreakSpec{cmd: byte(n), isACMD: isACMD}
	if len(args) > 1 {
		v, perr := parseHex32(args[1])
		if perr != "" {
			return nil, "break-on-sd: bad arg: " + perr
		}
		spec.useArg = true
		spec.wantArg = v
	}
	return spec, ""
}

// parseHex32 parses a 32-bit value, accepting a leading $ or 0x for hex
// and a plain decimal otherwise. Returns a non-empty error on failure.
func parseHex32(s string) (uint32, string) {
	t := strings.TrimPrefix(strings.TrimPrefix(s, "$"), "0x")
	t = strings.TrimPrefix(t, "0X")
	base := 16
	if t == s { // no hex marker — treat as decimal
		base = 10
	}
	v, err := strconv.ParseUint(t, base, 32)
	if err != nil {
		return 0, s
	}
	return uint32(v), ""
}

// sdBreakSpec is a one-shot SD-command breakpoint armed via
// `break-on-sd`. It fires when the SD card receives a matching command.
type sdBreakSpec struct {
	cmd     byte   // SD command number (0..63)
	isACMD  bool   // match an ACMD (CMD55-prefixed) rather than a plain CMD
	useArg  bool   // when true, only fire if arg == wantArg
	wantArg uint32 // the 32-bit command argument to match (e.g. an LBA)
}

// checkSDCommand is fed from the SD card's command logger on every
// command the card receives (it carries the live cpu.PC). When an armed
// `break-on-sd` spec matches, it pauses the CPU (one-shot) so the
// debugger can inspect the exact moment that command was issued — which
// works even though the NextZXOS DOS driver's code runs in a paged
// region whose static disassembly is bank-misaligned vs execution.
func (d *remoteDebugger) checkSDCommand(cmd byte, arg uint32, isACMD bool) {
	if d == nil {
		return
	}
	specP := d.sdBreak.Load()
	if specP == nil {
		return
	}
	s := *specP
	if s.cmd != cmd || s.isACMD != isACMD {
		return
	}
	if s.useArg && s.wantArg != arg {
		return
	}
	d.sdBreak.Store(nil) // one-shot
	d.paused.Store(true)
	pc := uint16(0)
	if d.emu != nil && d.emu.cpu != nil {
		pc = d.emu.cpu.PC
	}
	tag := "CMD"
	if isACMD {
		tag = "ACMD"
	}
	slog.Info("debugger: break-on-sd hit",
		"op", fmt.Sprintf("%s%d", tag, cmd),
		"arg", fmt.Sprintf("$%08X", arg),
		"pc", fmt.Sprintf("$%04X", pc))
	d.snapshotOnBPHit(pc, "break-on-sd")
}

// pauseAndAck transitions a RUNNING CPU to paused and blocks until the
// headless loop acknowledges. The ack channel is captured in the SAME
// pauseAckMu critical section as the paused store, so signalPauseAck —
// which closes that very channel and installs a fresh one — cannot slip
// in between and be missed. The old "Store(true) then read pauseAckCh"
// path had that gap: when the CPU was running (and especially when it
// HALTed, widening the window), the ack landed on the now-stale channel
// and the waiter blocked for the full timeout. Returns true on ack,
// false on timeout. If the CPU is already paused, returns true at once.
func (d *remoteDebugger) pauseAndAck() bool {
	d.pauseAckMu.Lock()
	if d.paused.Load() {
		d.pauseAckMu.Unlock()
		return true
	}
	ch := d.pauseAckCh
	d.paused.Store(true)
	d.pauseAckMu.Unlock()
	select {
	case <-ch:
		return true
	case <-time.After(d.pauseAckTimeout()):
		return false
	}
}

func (d *remoteDebugger) connLoop(c net.Conn) {
	defer func() { _ = c.Close() }()
	w := bufio.NewWriter(c)
	_, _ = w.WriteString("OK welcome to zx_go remote debugger\r\n")
	_ = w.Flush()
	scanner := bufio.NewScanner(c)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		resp := d.handleCommand(line)
		_, _ = w.WriteString(resp + "\r\n")
		_ = w.Flush()
		if strings.HasPrefix(strings.ToLower(line), "quit") ||
			strings.HasPrefix(strings.ToLower(line), "exit") {
			return
		}
	}
}

// commandsNeedingPause lists commands that READ CPU/memory state.
// Each of these implicitly pauses the CPU before running so the
// reads are race-free against the running CPU. Breakpoint and
// state-toggle commands either don't touch CPU state (lock-free
// atomic.Pointer for bpsMap) or manage the pause state directly,
// so they're excluded.
var commandsNeedingPause = map[string]bool{
	"get-registers":        true,
	"regs":                 true,
	"get-stack":            true,
	"stack":                true,
	"backtrace":            true,
	"bt":                   true,
	"history":              true,
	"hist":                 true,
	"prev":                 true,
	"p":                    true,
	"get-memory":           true,
	"mem":                  true,
	"hexdump":              true,
	"hd":                   true,
	"read-memory":          true,
	"peek":                 true,
	"write-memory":         true,
	"poke":                 true,
	"set-reg":              true,
	"cold-reset":           true,
	"disassemble":          true,
	"disasm":               true,
	"d":                    true,
	"get-mmu":              true,
	"mmu":                  true,
	"get-divmmc":           true,
	"divmmc":               true,
	"nextreg-read":         true,
	"nr-r":                 true,
	"nextreg-write":        true,
	"nr-w":                 true,
	"bank-peek":            true,
	"pool-scan":            true,
	"bp-peek":              true,
	"bank-poke":            true,
	"bp-poke":              true,
	"load-bin":             true,
	"compare-foreign":      true,
	"watch-reg":            true,
	"wr":                   true,
	"list-watches":         true,
	"clear-watch":          true,
	"step-over":            true,
	"n":                    true,
	"next":                 true,
	"watch-mem":            true,
	"clear-watch-mem":      true,
	"watch-read":           true,
	"clear-watch-read":     true,
	"trace-divmmc-ram":     true,
	"trace-writes":         true,
	"why-pc":               true,
	"snapshot-on-bp":       true,
	"hot":                  true,
	"disasm-bank":          true,
	"watch-zero":           true,
	"tp":                   true,
	"list-tp":              true,
	"clear-tp":             true,
	"watch-port":           true,
	"nr-trace":             true,
	"trace-nextreg-deltas": true,
	"nr-deltas":            true,
	"irq-stats":            true,
	"catch":                true,
	"crash-detect":         true,
	"nr-panel":             true,
	"nrp":                  true,
	"copper-disasm":        true,
	"copper":               true,
	"layer-state":          true,
	"layers":               true,
	"sprite-list":          true,
	"sprites":              true,
	"palette-dump":         true,
	"palette":              true,
	"nr-snap":              true,
	"nr-diff":              true,
	"callgraph":            true,
	"retgraph":             true,
	"rstgraph":             true,
	"nmi":                  true,
	"tt-snap":              true,
	"tt-rewind":            true,
}

func (d *remoteDebugger) handleCommand(line string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "ERR empty"
	}
	cmd := strings.ToLower(fields[0])
	args := fields[1:]
	// State-touching commands implicitly pause the CPU and wait
	// for the headless loop to acknowledge the pause before they
	// run. Without this, reads of cpu.PC / mem race the CPU.
	if commandsNeedingPause[cmd] && !d.paused.Load() {
		if !d.pauseAndAck() {
			return "ERR pause-ack timeout (CPU may be in a tight HALT or non-cooperative loop)"
		}
	}
	switch cmd {
	case "help", "?":
		return "OK pause set-pause-timeout break-on-sd continue step step-over get-registers get-stack backtrace history prev hot callgraph retgraph rstgraph get-memory hexdump read-memory write-memory set-breakpoint clear-breakpoint list-breakpoints bp-first-entry disassemble disasm-bank get-mmu get-divmmc nr-panel copper-disasm layer-state sprite-list palette-dump nextreg-read nextreg-write nr-snap nr-diff bank-peek bank-poke pool-scan load-bin list-banks watch-reg list-watches clear-watch watch-mem clear-watch-mem watch-read clear-watch-read watch-zero watch-port tp list-tp clear-tp nr-trace trace-divmmc-ram trace-writes trace-nextreg-deltas irq-stats catch snapshot-on-bp compare-foreign crash-detect tt-on tt-off tt-status tt-snap tt-rewind tt-find-pc tt-clear quit"
	case "set-pause-timeout":
		// Query form (no arg) reports the current value; otherwise set
		// the pause-ack wait to N seconds. Used to await a `continue`
		// to a deep breakpoint in a long boot (default 2s is too short).
		if len(args) == 0 {
			return fmt.Sprintf("OK pause-timeout=%ds", int(d.pauseAckTimeout()/time.Second))
		}
		secs, err := strconv.Atoi(args[0])
		if err != nil || secs <= 0 {
			return "ERR usage: set-pause-timeout [SECONDS>0]"
		}
		d.pauseAckTimeoutMS.Store(int64(secs) * 1000)
		return fmt.Sprintf("OK pause-timeout=%ds", secs)
	case "break-on-sd", "bsd":
		// Arm a one-shot breakpoint on an SD command the card receives:
		//   break-on-sd            -> show current
		//   break-on-sd off        -> clear
		//   break-on-sd CMD0       -> fire on CMD0 (any arg)
		//   break-on-sd ACMD41     -> fire on ACMD41
		//   break-on-sd CMD17 $3F  -> fire on CMD17 with arg $0000003F
		if len(args) == 0 {
			if p := d.sdBreak.Load(); p != nil {
				tag := "CMD"
				if p.isACMD {
					tag = "ACMD"
				}
				if p.useArg {
					return fmt.Sprintf("OK break-on-sd %s%d arg=$%08X", tag, p.cmd, p.wantArg)
				}
				return fmt.Sprintf("OK break-on-sd %s%d", tag, p.cmd)
			}
			return "OK break-on-sd off"
		}
		if strings.EqualFold(args[0], "off") {
			d.sdBreak.Store(nil)
			return "OK break-on-sd off"
		}
		spec, err := parseSDBreakSpec(args)
		if err != "" {
			return "ERR " + err
		}
		d.sdBreak.Store(spec)
		tag := "CMD"
		if spec.isACMD {
			tag = "ACMD"
		}
		if spec.useArg {
			return fmt.Sprintf("OK break-on-sd %s%d arg=$%08X", tag, spec.cmd, spec.wantArg)
		}
		return fmt.Sprintf("OK break-on-sd %s%d", tag, spec.cmd)
	case "pause":
		if !d.pauseAndAck() {
			return "ERR pause-ack timeout"
		}
		return "OK paused"
	case "continue", "cont", "c", "run", "r":
		// If we're resuming directly from a breakpoint, single-step
		// over the current instruction so the BP doesn't immediately
		// re-fire on the same PC. This is the standard "skip the BP
		// we just hit" pattern; without it `continue` is a no-op.
		if _, ok := d.bpset().Lookup(d.emu.cpu.PC); ok {
			d.emu.cpu.StepInstructionWithIRQ()
		}
		d.paused.Store(false)
		d.stepping.Store(false)
		select {
		case d.resumeCh <- struct{}{}:
		default:
		}
		return "OK continuing"
	case "step-over", "n", "next":
		return d.cmdStepOver()
	case "step", "s":
		// Optional N argument: step N instructions in one ZRCP
		// roundtrip. Default 1. Used by bisect-style tools that
		// need to advance many instructions cheaply.
		n := 1
		if len(args) > 0 {
			if v, err := parseHex(args[0]); err == nil && v > 0 {
				// parseHex returns uint16 so the natural max is 65535;
				// chain successive `step N` calls for larger ranges.
				n = int(v)
			}
		}
		for i := 0; i < n; i++ {
			select {
			case <-d.stepDoneCh:
			default:
			}
			d.stepping.Store(true)
			select {
			case d.resumeCh <- struct{}{}:
			default:
			}
			select {
			case <-d.stepDoneCh:
			case <-time.After(2 * time.Second):
				return fmt.Sprintf("ERR step %d/%d timed out", i+1, n)
			}
		}
		if n == 1 {
			return "OK stepped"
		}
		return fmt.Sprintf("OK stepped %d", n)
	case "get-registers", "regs":
		return "OK " + d.fmtRegisters()
	case "get-stack", "stack":
		return "OK " + d.fmtStackWalk()
	case "backtrace", "bt":
		return d.cmdBacktrace(args)
	case "history", "hist":
		return d.cmdHistory(args)
	case "prev", "p":
		return d.cmdPrev(args)
	case "get-memory", "mem":
		return d.cmdGetMemory(args)
	case "hexdump", "hd":
		return d.cmdHexDump(args)
	case "read-memory", "peek":
		return d.cmdReadMemory(args)
	case "write-memory", "poke":
		return d.cmdWriteMemory(args)
	case "set-reg":
		return d.cmdSetReg(args)
	case "cold-reset":
		return d.cmdColdReset()
	case "set-breakpoint", "set-bp", "bp":
		return d.cmdSetBreakpoint(args)
	case "clear-breakpoint", "clear-bp", "cbp":
		return d.cmdClearBreakpoint(args)
	case "list-breakpoints", "list-bp", "lbp":
		return d.cmdListBreakpoints()
	case "disassemble", "disasm", "d":
		return d.cmdDisasm(args)
	case "get-mmu", "mmu":
		return "OK " + d.fmtMMU()
	case "get-divmmc", "divmmc":
		return "OK " + d.fmtDivMMC()
	case "nr-panel", "nrp":
		return d.cmdNRPanel()
	case "copper-disasm", "copper":
		return d.cmdCopperDisasm()
	case "layer-state", "layers":
		return d.cmdLayerState()
	case "sprite-list", "sprites":
		return d.cmdSpriteList()
	case "palette-dump", "palette":
		return d.cmdPaletteDump()
	case "nextreg-read", "nr-r":
		return d.cmdNextregRead(args)
	case "nextreg-write", "nr-w":
		return d.cmdNextregWrite(args)
	case "bank-peek", "bp-peek":
		return d.cmdBankPeek(args)
	case "bank-poke", "bp-poke":
		return d.cmdBankPoke(args)
	case "pool-scan":
		return d.cmdPoolScan(args)
	case "load-bin":
		return d.cmdLoadBin(args)
	case "bp-first-entry":
		return d.cmdBPFirstEntry(args)
	case "trace-writes":
		return d.cmdTraceWrites(args)
	case "trace-nextreg-deltas", "nr-deltas":
		return d.cmdTraceNRDeltas(args)
	case "compare-foreign":
		return d.cmdCompareForeign(args)
	case "list-banks":
		return d.cmdListBanks()
	case "watch-reg", "wr":
		return d.cmdWatchReg(args)
	case "list-watches":
		return d.cmdListWatches()
	case "clear-watch":
		return d.cmdClearWatch(args)
	case "watch-mem":
		return d.cmdWatchMem(args)
	case "clear-watch-mem":
		return d.cmdClearWatchMem(args)
	case "watch-read":
		return d.cmdWatchRead(args)
	case "clear-watch-read":
		return d.cmdClearWatchRead(args)
	case "provenance", "prov":
		return d.cmdProvenance(args)
	case "provenance-phys", "provp":
		return d.cmdProvenancePhys(args)
	case "why-pc":
		return d.cmdWhyPC(args)
	case "snapshot-on-bp":
		return d.cmdSnapshotOnBP(args)
	case "hot":
		return d.cmdHot(args)
	case "disasm-bank":
		return d.cmdDisasmBank(args)
	case "watch-zero":
		return d.cmdWatchZero(args)
	case "tp":
		return d.cmdTp(args)
	case "list-tp":
		return d.cmdListTp()
	case "clear-tp":
		return d.cmdClearTp(args)
	case "watch-port":
		return d.cmdWatchPort(args)
	case "nr-trace":
		return d.cmdNRTrace(args)
	case "irq-stats":
		return d.cmdIRQStats(args)
	case "nr-snap":
		return d.cmdNRSnap(args)
	case "nr-diff":
		return d.cmdNRDiff(args)
	case "catch":
		if len(args) >= 1 && strings.ToLower(args[0]) == "irq" {
			return d.cmdCatchIRQ(args[1:])
		}
		return "ERR usage: catch irq [off]"
	case "nmi":
		d.emu.cpu.PendingNMI.Store(true)
		return "OK NMI armed (fires at next instruction boundary)"
	case "sym":
		return d.cmdSym(args)
	case "reload-syms":
		return d.cmdReloadSyms(args)
	case "cont-until", "continue-until":
		return d.cmdContUntil(args)
	case "forward", "f":
		return d.cmdForward(args)
	case "trace-divmmc-ram":
		return d.cmdTraceDivMMCRAM(args)
	case "crash-detect":
		return d.cmdCrashDetect(args)
	case "tt", "tt-status", "tt-on", "tt-off", "tt-clear",
		"tt-snap", "tt-rewind", "tt-find-pc":
		return d.cmdTimeTravel(cmd, args)
	case "xref":
		return d.cmdXref(args)
	case "callgraph":
		return d.cmdGraph(args, debugger.SourceCall, "CALL")
	case "retgraph":
		return d.cmdGraph(args, debugger.SourceRet, "RET")
	case "rstgraph":
		return d.cmdGraph(args, debugger.SourceRst, "RST")
	case "quit", "exit":
		return "OK bye"
	}
	return "ERR unknown command (try `help`)"
}

func (d *remoteDebugger) fmtRegisters() string {
	c := d.emu.cpu
	// Annotate PC with the symbol map if one is loaded.
	pc := annotateAddr(c.PC)
	return fmt.Sprintf("PC=%s SP=$%04X AF=$%02X%02X BC=$%02X%02X DE=$%02X%02X HL=$%02X%02X IX=$%04X IY=$%04X I=$%02X R=$%02X IFF1=%v IM=%d Halted=%v insns=%d",
		pc, c.SP, c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L,
		c.IX, c.IY, c.I, c.R, c.IFF1, c.IM, c.Halted, c.InstructionCount())
}

// fmtStackWalk reads up to 16 16-bit words above SP and returns
// them annotated with the symbol map. Used by the snapshot path
// for stack traces; exposed in the debugger via get-stack.
func (d *remoteDebugger) fmtStackWalk() string {
	c := d.emu.cpu
	mem := d.emu.mem
	parts := make([]string, 0, 16)
	for i := 0; i < 16; i++ {
		addr := c.SP + uint16(i*2)
		v := uint16(mem.Read(addr)) | uint16(mem.Read(addr+1))<<8
		parts = append(parts, annotateAddr(v))
	}
	return fmt.Sprintf("sp=$%04X words=%s", c.SP, strings.Join(parts, " "))
}

func (d *remoteDebugger) fmtMMU() string {
	mem := d.emu.mem
	port7FFD, port1FFD, _ := mem.GetPortState()
	romBank := int((port7FFD>>4)&1) | int((port1FFD>>1)&2)
	var parts []string
	for i := byte(0); i < 8; i++ {
		parts = append(parts, fmt.Sprintf("%02X", mem.GetMMU(i)))
	}
	return fmt.Sprintf("rom_bank=%d slots=%s", romBank, strings.Join(parts, " "))
}

func (d *remoteDebugger) fmtDivMMC() string {
	if d.emu.ula == nil {
		return "no-ula"
	}
	pager := d.emu.ula.NextDivMMC()
	if pager == nil {
		return "no-pager"
	}
	type isPagedIn interface{ IsPagedIn() bool }
	type isMAPRAM interface{ MAPRAM() bool }
	type isAutomap interface{ AutomapEnabled() bool }
	type isLastE3 interface{ LastE3() byte }
	p := "unknown"
	m := "unknown"
	a := "unknown"
	e3 := "?"
	conmem := "?"
	if x, ok := pager.(isPagedIn); ok {
		p = fmt.Sprintf("%v", x.IsPagedIn())
	}
	if x, ok := pager.(isMAPRAM); ok {
		m = fmt.Sprintf("%v", x.MAPRAM())
	}
	if x, ok := pager.(isAutomap); ok {
		a = fmt.Sprintf("%v", x.AutomapEnabled())
	}
	if x, ok := pager.(isLastE3); ok {
		v := x.LastE3()
		e3 = fmt.Sprintf("$%02X", v)
		conmem = fmt.Sprintf("%v", v&0x80 != 0)
	}
	return fmt.Sprintf("paged_in=%s mapram=%s automap=%s conmem=%s e3=%s", p, m, a, conmem, e3)
}

func (d *remoteDebugger) cmdGetMemory(args []string) string {
	if len(args) < 2 {
		return "ERR usage: get-memory ADDR LEN"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	n, err := parseHex(args[1])
	if err != nil {
		return "ERR " + err.Error()
	}
	if n > 256 {
		n = 256
	}
	mem := d.emu.mem
	var b strings.Builder
	for i := uint16(0); i < n; i++ {
		fmt.Fprintf(&b, "%02X", mem.Read(addr+i))
		if i < n-1 {
			b.WriteByte(' ')
		}
	}
	return "OK " + b.String()
}

func (d *remoteDebugger) cmdReadMemory(args []string) string {
	if len(args) < 1 {
		return "ERR usage: read-memory ADDR"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	v := d.emu.mem.Read(addr)
	return fmt.Sprintf("OK $%02X", v)
}

func (d *remoteDebugger) cmdWriteMemory(args []string) string {
	if len(args) < 2 {
		return "ERR usage: write-memory ADDR VAL"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	v, err := parseHex(args[1])
	if err != nil {
		return "ERR " + err.Error()
	}
	d.emu.mem.Write(addr, byte(v&0xFF))
	return "OK"
}

// cmdColdReset performs a hard reset equivalent to power-cycling
// the machine — calls emu.reboot() which resets CPU, ULA, NextRegs,
// peripherals, and memory back to power-on defaults. Returns
// instruction counter to zero. Used by bisect-style lockstep tools
// that need to advance from cold start to a specific instruction
// without restarting the whole process. CAUTION: if a warm-boot
// snapshot is auto-applied, it will also re-apply (matching the GUI
// Reboot menu item).
func (d *remoteDebugger) cmdColdReset() string {
	d.emu.reboot()
	d.emu.cpu.ResetInstructionCount()
	return "OK"
}

// cmdSetReg writes a CPU register. Accepts 8-bit (A/B/C/D/E/H/L/F/I/R)
// or 16-bit (AF/BC/DE/HL/IX/IY/SP/PC) register names — case-insensitive.
// Used by lockstep diff tools that need to force ours to match a
// reference emulator's register state before comparison.
func (d *remoteDebugger) cmdSetReg(args []string) string {
	if len(args) < 2 {
		return "ERR usage: set-reg NAME VAL"
	}
	name := strings.ToUpper(args[0])
	v, err := parseHex(args[1])
	if err != nil {
		return "ERR " + err.Error()
	}
	c := d.emu.cpu
	switch name {
	case "A":
		c.A = byte(v & 0xFF)
	case "F":
		c.F = byte(v & 0xFF)
	case "B":
		c.B = byte(v & 0xFF)
	case "C":
		c.C = byte(v & 0xFF)
	case "D":
		c.D = byte(v & 0xFF)
	case "E":
		c.E = byte(v & 0xFF)
	case "H":
		c.H = byte(v & 0xFF)
	case "L":
		c.L = byte(v & 0xFF)
	case "I":
		c.I = byte(v & 0xFF)
	case "R":
		c.R = byte(v & 0xFF)
	case "AF":
		c.A = byte((v >> 8) & 0xFF)
		c.F = byte(v & 0xFF)
	case "BC":
		c.SetBC(uint16(v))
	case "DE":
		c.SetDE(uint16(v))
	case "HL":
		c.SetHL(uint16(v))
	case "IX":
		c.IX = uint16(v)
	case "IY":
		c.IY = uint16(v)
	case "SP":
		c.SP = uint16(v)
	case "PC":
		c.PC = uint16(v)
	case "IM":
		c.IM = byte(v & 0x03)
	default:
		return "ERR unknown register " + name
	}
	return "OK"
}

// bpset returns the shared breakpoint store, lazily allocating one
// if unset. Production always pre-sets d.bps in newRemoteDebugger
// (before any goroutine starts), so the lazy branch is only taken by
// unit tests that construct a bare remoteDebugger{}; there is no
// concurrent first-write in production.
func (d *remoteDebugger) bpset() *debugger.BreakpointSet {
	if d.bps == nil {
		if d.emu != nil {
			d.bps = d.emu.sharedBreakpoints()
		} else {
			d.bps = debugger.NewBreakpointSet()
		}
	}
	return d.bps
}

func (d *remoteDebugger) cmdSetBreakpoint(args []string) string {
	if len(args) < 1 {
		return "ERR usage: set-breakpoint ADDR [bank=N|any-bank] [if EXPR]"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	// Bank-filter behaviour:
	//   - explicit `bank=N` (0..3)           → that bank only
	//   - explicit `any-bank`                → no filter
	//   - omitted, ROM bank known            → default to current bank
	//   - omitted, ROM bank unknown (-1)     → no filter
	// Same-PC-different-code in different banks (e.g. Spectrum Next)
	// is the common case; defaulting to "any bank" surprised every
	// past investigation with spurious hits from unrelated code at
	// the same address.
	currentBank := d.currentROMBank()
	entry := debugger.BPEntry{Bank: currentBank}
	bankExplicit := false
	// Walk forward consuming optional clauses. `if` consumes
	// everything to end-of-args as the expression.
	i := 1
	for i < len(args) {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "bank="):
			b, err := strconv.Atoi(a[5:])
			if err != nil {
				return "ERR bad bank"
			}
			entry.Bank = b
			bankExplicit = true
			i++
		case strings.EqualFold(a, "any-bank") || strings.EqualFold(a, "anybank"):
			entry.Bank = -1
			bankExplicit = true
			i++
		case strings.EqualFold(a, "if"):
			// `if EXPR` runs until a top-level `do` keyword OR end of
			// args, so both `bp $X if foo do "regs"` and `bp $X if foo`
			// parse correctly.
			i++
			if i >= len(args) {
				return "ERR `if` requires a condition expression"
			}
			exprEnd := i
			for exprEnd < len(args) && !strings.EqualFold(args[exprEnd], "do") {
				exprEnd++
			}
			expr := strings.Join(args[i:exprEnd], " ")
			cond, err := debugger.ParseCondition(expr)
			if err != nil {
				return "ERR condition: " + err.Error()
			}
			entry.HasCond = true
			entry.Cond = cond
			entry.Source = expr
			i = exprEnd
		case strings.EqualFold(a, "do"):
			// `do "cmd1; cmd2"` — action to run when the BP fires.
			// The remaining args are joined with spaces; surrounding
			// quotes are stripped if present so users can naturally
			// type `do "regs; mmu; step"`.
			i++
			if i >= len(args) {
				return "ERR `do` requires an action string"
			}
			action := strings.Join(args[i:], " ")
			action = strings.TrimSpace(action)
			if len(action) >= 2 && action[0] == '"' && action[len(action)-1] == '"' {
				action = action[1 : len(action)-1]
			}
			entry.Action = action
			i = len(args)
		default:
			return "ERR unrecognised clause: " + a
		}
	}
	d.bpset().Add(addr, entry)
	suffix := ""
	switch {
	case entry.Bank < 0:
		suffix += " (any bank)"
	case bankExplicit:
		suffix += fmt.Sprintf(" (bank=%d)", entry.Bank)
	default:
		suffix += fmt.Sprintf(" (bank=%d, default)", entry.Bank)
	}
	if entry.HasCond {
		suffix += " if " + entry.Cond.String()
	}
	if entry.Action != "" {
		suffix += " do " + strconv.Quote(entry.Action)
	}
	return fmt.Sprintf("OK breakpoint @$%04X%s", addr, suffix)
}

func (d *remoteDebugger) cmdClearBreakpoint(args []string) string {
	if len(args) < 1 {
		return "ERR usage: clear-breakpoint ADDR"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	d.bpset().Remove(addr)
	return fmt.Sprintf("OK cleared @$%04X", addr)
}

func (d *remoteDebugger) cmdListBreakpoints() string {
	bps := d.bpset().Snapshot()
	if len(bps) == 0 {
		return "OK (no breakpoints set)"
	}
	parts := []string{}
	for _, pc := range debugger.SortedKeys(bps) {
		e := bps[pc]
		suffix := ""
		if e.Bank >= 0 {
			suffix += fmt.Sprintf("[bank=%d]", e.Bank)
		} else {
			// Bank-less BPs fire on any ROM bank — easy footgun on
			// Spectrum Next where the same PC means different code
			// in different banks. Flag it so users notice.
			suffix += "[any bank!]"
		}
		if e.HasCond {
			suffix += " if " + e.Cond.String()
		}
		if e.Action != "" {
			suffix += " do " + strconv.Quote(e.Action)
		}
		parts = append(parts, annotateAddr(pc)+suffix)
	}
	return "OK " + strings.Join(parts, " ")
}

// cmdHistory reports the ring-buffer state: how many entries are
// stored and what fraction of capacity is used. Useful for sanity-
// checking that --debugger-history was set high enough to cover
// the window the user wants to inspect.
func (d *remoteDebugger) cmdHistory(_ []string) string {
	if d.history == nil {
		return "ERR history disabled (relaunch with --debugger-history=N)"
	}
	return fmt.Sprintf("OK entries=%d capacity=%d",
		d.history.Len(), d.history.Capacity())
}

// cmdTp adds a tracepoint at PC. The CPU does NOT halt at PC;
// instead, an slog.Info("tp-hit") record is emitted with full
// register state every time PC fires. A hit counter accrues for
// read-back via `list-tp`.
func (d *remoteDebugger) cmdTp(args []string) string {
	if len(args) < 1 {
		return "ERR usage: tp ADDR"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	old := d.tpMap.Load()
	next := map[uint16]uint64{}
	if old != nil {
		for k, v := range *old {
			next[k] = v
		}
	}
	next[addr] = 0
	d.tpMap.Store(&next)
	return fmt.Sprintf("OK tp @%s", annotateAddr(addr))
}

// cmdListTp prints every active tracepoint with its hit count.
func (d *remoteDebugger) cmdListTp() string {
	p := d.tpMap.Load()
	if p == nil || len(*p) == 0 {
		return "OK (no tracepoints)"
	}
	pcs := make([]uint16, 0, len(*p))
	for k := range *p {
		pcs = append(pcs, k)
	}
	sortUint16s(pcs)
	var b strings.Builder
	b.WriteString("OK\r\n")
	for _, pc := range pcs {
		fmt.Fprintf(&b, "  %s  hits=%d\r\n", annotateAddr(pc), (*p)[pc])
	}
	return strings.TrimRight(b.String(), "\r\n")
}

// cmdClearTp removes one tracepoint (by PC) or all if no arg.
func (d *remoteDebugger) cmdClearTp(args []string) string {
	if len(args) == 0 {
		d.tpMap.Store(nil)
		return "OK all tracepoints cleared"
	}
	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	old := d.tpMap.Load()
	if old == nil {
		return "OK (no tracepoints)"
	}
	next := map[uint16]uint64{}
	for k, v := range *old {
		if k != addr {
			next[k] = v
		}
	}
	if len(next) == 0 {
		d.tpMap.Store(nil)
	} else {
		d.tpMap.Store(&next)
	}
	return fmt.Sprintf("OK removed tp @%s", annotateAddr(addr))
}

// sortUint16s sorts a slice of uint16 in ascending order.
func sortUint16s(s []uint16) {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
}

// cmdWatchZero arms/disarms the zero-bank tracer.
//
//	watch-zero on    — halt on next M1 into a 16-byte-zero region
//	watch-zero off   — disarm
func (d *remoteDebugger) cmdWatchZero(args []string) string {
	if len(args) < 1 {
		state := "off"
		if d.watchZero.Load() {
			state = "on"
		}
		return "OK watch-zero=" + state
	}
	switch strings.ToLower(args[0]) {
	case "on", "1", "true":
		d.watchZero.Store(true)
		return "OK watch-zero=on"
	case "off", "0", "false":
		d.watchZero.Store(false)
		return "OK watch-zero=off"
	}
	return "ERR usage: watch-zero on|off"
}

// cmdGraph walks the History ring, filters entries by the supplied
// branch-source class, aggregates (caller → callee) edges, and
// returns the top-N most-frequent edges. Drives the `callgraph`,
// `retgraph`, and `rstgraph` commands — same logic, different
// filter. Default N=20, cap at 256.
//
// Where `hot` answers "which PCs dominate the inner loop?",
// callgraph answers "which subroutines does the outer loop keep
// calling?" — the structure question `hot` can't resolve on its
// own when the inner copy/fill subroutine sucks up most M1 cycles.
func (d *remoteDebugger) cmdGraph(args []string, filter debugger.PCSource, label string) string {
	if d.history == nil {
		return "ERR history disabled (relaunch with --debugger-history=N)"
	}
	n := 20
	if len(args) >= 1 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}
	if n > 256 {
		n = 256
	}
	entries := d.history.Last(d.history.Capacity())
	edges := debugger.CallGraph(entries, filter, n)
	if len(edges) == 0 {
		return fmt.Sprintf("OK (no %s edges in ring)", label)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "OK %s edges (top %d):\r\n", label, len(edges))
	for _, e := range edges {
		fmt.Fprintf(&b, "  %-10d %s -> %s\r\n",
			e.Count, annotateAddr(e.From), annotateAddr(e.To))
	}
	return strings.TrimRight(b.String(), "\r\n")
}

// cmdHot processes the full History ring and returns the top-N PCs
// by hit count. Identifies tight loops in one command. Default N=20,
// cap at 256.
func (d *remoteDebugger) cmdHot(args []string) string {
	if d.history == nil {
		return "ERR history disabled (relaunch with --debugger-history=N)"
	}
	n := 20
	if len(args) >= 1 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}
	if n > 256 {
		n = 256
	}
	entries := d.history.Last(d.history.Capacity())
	hots := debugger.PCHistogram(entries, n)
	if len(hots) == 0 {
		return "OK (ring empty)"
	}
	var b strings.Builder
	b.WriteString("OK\r\n")
	for _, h := range hots {
		fmt.Fprintf(&b, "  %-10d %s\r\n", h.Count, annotateAddr(h.PC))
	}
	return strings.TrimRight(b.String(), "\r\n")
}

// cmdPrev dumps the last N pre-fetch-recorded entries in
// chronological order (oldest first). One line per entry showing
// PC (annotated with symbol map if loaded), SP, A/F, the packed
// IFF/IM/Halted bits, and the instruction count. Useful at a
// breakpoint to see what code preceded the hit.
func (d *remoteDebugger) cmdPrev(args []string) string {
	if d.history == nil {
		return "ERR history disabled (relaunch with --debugger-history=N)"
	}
	n := 20
	if len(args) >= 1 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			n = v
		}
	}
	entries := d.history.Last(n)
	wide := d.history.Wide()
	var b strings.Builder
	b.WriteString("OK\r\n")
	for _, e := range entries {
		src := ""
		if e.Source != debugger.SourceSequential {
			if e.SourceFrom != 0 {
				src = fmt.Sprintf("  via=%s@$%04X", e.Source, e.SourceFrom)
			} else {
				src = fmt.Sprintf("  via=%s", e.Source)
			}
		}
		if wide {
			fmt.Fprintf(&b,
				"  insn=%-10d %s  SP=$%04X AF=$%02X%02X BC=$%04X DE=$%04X HL=$%04X IX=$%04X IY=$%04X  IFF1=%v IM=%d Halted=%v  bank=%d%s\r\n",
				e.Insns, annotateAddr(e.PC), e.SP, e.A, e.F,
				e.BC, e.DE, e.HL, e.IX, e.IY,
				e.IFF1(), e.IM(), e.Halted(), e.ROMBnk, src)
		} else {
			fmt.Fprintf(&b,
				"  insn=%-10d %s  SP=$%04X AF=$%02X%02X  IFF1=%v IM=%d Halted=%v  bank=%d%s\r\n",
				e.Insns, annotateAddr(e.PC), e.SP, e.A, e.F,
				e.IFF1(), e.IM(), e.Halted(), e.ROMBnk, src)
		}
	}
	return strings.TrimRight(b.String(), "\r\n")
}

// cmdBacktrace is the telnet front-end for the shared
// debugger.Backtrace backend. The classification + disasm logic
// lives in pkg/debugger so the visual debugger can render the
// same Frame slice as a Fyne widget without duplicating the
// CALL / RST heuristic.
func (d *remoteDebugger) cmdBacktrace(args []string) string {
	c := d.emu.cpu
	mem := d.emu.mem
	count := 8
	if len(args) >= 1 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			count = v
			if count > 64 {
				count = 64
			}
		}
	}
	read := func(a uint16) byte { return mem.Read(a) }
	frames := debugger.Backtrace(read, c.SP, count, 3)
	var b strings.Builder
	b.WriteString("OK\r\n")
	fmt.Fprintf(&b, "  SP = $%04X  IFF1=%v  IM=%d  Halted=%v\r\n",
		c.SP, c.IFF1, c.IM, c.Halted)
	for _, f := range frames {
		hint := f.Hint()
		if hint != "" {
			hint = "  " + hint
		}
		fmt.Fprintf(&b, "  $%04X: %s   %s%s\r\n",
			f.StackAddr, annotateAddr(f.ReturnAddr), f.Origin.String(), hint)
		for _, l := range f.Disasm {
			fmt.Fprintf(&b, "          %s\r\n", l.String())
		}
	}
	return strings.TrimRight(b.String(), "\r\n")
}

func (d *remoteDebugger) cmdDisasm(args []string) string {
	addr := d.emu.cpu.PC
	count := 6
	if len(args) >= 1 {
		v, err := parseHex(args[0])
		if err == nil {
			addr = v
		}
	}
	if len(args) >= 2 {
		v, err := strconv.Atoi(args[1])
		if err == nil && v > 0 {
			count = v
			if count > 64 {
				count = 64
			}
		}
	}
	read := func(a uint16) byte { return d.emu.mem.Read(a) }
	lines := debugger.Disassemble(read, addr, count)
	var b strings.Builder
	b.WriteString("OK")
	for _, l := range lines {
		b.WriteString("\r\n  ")
		b.WriteString(l.String())
	}
	return b.String()
}

func (d *remoteDebugger) cmdNextregRead(args []string) string {
	if len(args) < 1 {
		return "ERR usage: nextreg-read REG"
	}
	reg, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	if d.emu.nextRegs == nil {
		return "ERR no nextregs (not a Next model)"
	}
	return fmt.Sprintf("OK $%02X", d.emu.nextRegs.ReadReg(byte(reg)))
}

func (d *remoteDebugger) cmdNextregWrite(args []string) string {
	if len(args) < 2 {
		return "ERR usage: nextreg-write REG VAL"
	}
	reg, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	val, err := parseHex(args[1])
	if err != nil {
		return "ERR " + err.Error()
	}
	if d.emu.nextRegs == nil {
		return "ERR no nextregs (not a Next model)"
	}
	d.emu.nextRegs.WriteReg(byte(reg), byte(val&0xFF))
	return "OK"
}

func parseHex(s string) (uint16, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "$")
	v, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		return 0, fmt.Errorf("bad hex %q: %v", s, err)
	}
	return uint16(v), nil
}
