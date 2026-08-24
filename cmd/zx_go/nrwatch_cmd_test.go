package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/copper"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// `watch-nextreg` is the halting counterpart to `nr-trace`. nr-trace names
// registers and logs their writes; watch-port halts but sees only $243B and
// $253B, which carry a select and a value rather than a register. These tests
// pin the join: a watch keyed on the register number that halts where nr-trace
// logs.

func newRemoteForNRWatch(t *testing.T) *remoteDebugger {
	t.Helper()
	mem, err := memory.New(nextTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	cpu := z80.New(mem, nil)
	return &remoteDebugger{emu: &emulator{cpu: cpu, mem: mem, nextRegs: nextregs.New()}}
}

func TestCmdWatchNextReg_ListEmpty(t *testing.T) {
	d := newRemoteForNRWatch(t)
	if got := d.cmdWatchNextReg(nil); got != "OK (no nextreg watches)" {
		t.Errorf("empty status = %q", got)
	}
}

// Arming accepts the same comma- and space-separated hex lists nr-trace
// takes, so a user moving from one to the other does not have to retype.
func TestCmdWatchNextReg_ArmListsEveryReg(t *testing.T) {
	d := newRemoteForNRWatch(t)

	if got := d.cmdWatchNextReg([]string{"56,57"}); !strings.HasPrefix(got, "OK") {
		t.Fatalf("arm = %q", got)
	}
	if got := d.cmdWatchNextReg([]string{"12"}); !strings.HasPrefix(got, "OK") {
		t.Fatalf("arm second = %q", got)
	}

	got := d.cmdWatchNextReg(nil)
	for _, want := range []string{"$56", "$57", "$12"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q missing %s", got, want)
		}
	}
}

func TestCmdWatchNextReg_Off(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.cmdWatchNextReg([]string{"56"})
	if got := d.cmdWatchNextReg([]string{"off"}); got != "OK nextreg watches cleared" {
		t.Errorf("off = %q", got)
	}
	if got := d.cmdWatchNextReg(nil); got != "OK (no nextreg watches)" {
		t.Errorf("status after off = %q", got)
	}
}

func TestCmdWatchNextReg_RejectsBadReg(t *testing.T) {
	d := newRemoteForNRWatch(t)
	if got := d.cmdWatchNextReg([]string{"banana"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("bad reg = %q, want an error", got)
	}
}

// The core behaviour: a guest write through the $243B/$253B port pair to an
// armed register halts the CPU. The two ports carry a select and a value
// separately, which is exactly what watch-port could not join.
func TestWatchNextReg_PortPairWriteHalts(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.cmdWatchNextReg([]string{"12"})

	d.emu.nextRegs.Select(0x12)
	d.emu.nextRegs.WriteData(0x2A)

	if !d.paused.Load() {
		t.Error("write to NR$12 did not halt the CPU")
	}
}

func TestWatchNextReg_UnwatchedRegDoesNotHalt(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.cmdWatchNextReg([]string{"12"})

	d.emu.nextRegs.Select(0x13)
	d.emu.nextRegs.WriteData(0x2A)

	if d.paused.Load() {
		t.Error("write to NR$13 halted a watch armed on NR$12")
	}
}

// A read of an armed register is not a write and must not fire. Reads run
// through the same tracer with isWrite false.
func TestWatchNextReg_ReadDoesNotHalt(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.cmdWatchNextReg([]string{"12"})

	d.emu.nextRegs.Select(0x12)
	_ = d.emu.nextRegs.ReadData()

	if d.paused.Load() {
		t.Error("a read of NR$12 halted a write watch")
	}
}

func TestWatchNextReg_ValueMatch(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.cmdWatchNextReg([]string{"12", "=", "FF"})

	d.emu.nextRegs.WriteReg(0x12, 0x01)
	if d.paused.Load() {
		t.Fatal("val=$FF watch halted on a write of $01")
	}

	d.emu.nextRegs.WriteReg(0x12, 0xFF)
	if !d.paused.Load() {
		t.Error("val=$FF watch did not halt on a write of $FF")
	}
}

// log mode is for a register written every frame: it reports without freezing
// the machine on the first write.
func TestWatchNextReg_LogOnlyDoesNotHalt(t *testing.T) {
	d := newRemoteForNRWatch(t)
	if got := d.cmdWatchNextReg([]string{"12", "log"}); !strings.Contains(got, "log-only") {
		t.Fatalf("arm log = %q, want it to say log-only", got)
	}

	d.emu.nextRegs.WriteReg(0x12, 0x2A)

	if d.paused.Load() {
		t.Error("a log-only watch halted the CPU")
	}
}

// Arming a watch must not silence a tracer that is already installed. The
// startup env-var tracer and nr-trace both live on the same hook.
func TestWatchNextReg_ChainsPriorTracer(t *testing.T) {
	d := newRemoteForNRWatch(t)

	priorCalls := 0
	d.emu.nextRegs.SetTracer(func(reg, val byte, isWrite bool) { priorCalls++ })

	d.cmdWatchNextReg([]string{"12"})
	d.emu.nextRegs.WriteReg(0x12, 0x2A)

	if priorCalls == 0 {
		t.Error("arming a watch replaced the tracer that was already installed")
	}
	if !d.paused.Load() {
		t.Error("chained watch did not halt")
	}
}

// A single register may carry more than one watch. A halting watch anywhere in
// the set wins over a log-only one, so arming a narrow halt alongside a broad
// log does not silently downgrade the halt.
func TestWatchNextReg_HaltWinsOverLogOnly(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.cmdWatchNextReg([]string{"12", "log"})
	d.cmdWatchNextReg([]string{"12", "=", "FF"})

	d.emu.nextRegs.WriteReg(0x12, 0x01)
	if d.paused.Load() {
		t.Fatal("halted on $01 when only the log-only watch matched")
	}

	d.emu.nextRegs.WriteReg(0x12, 0xFF)
	if !d.paused.Load() {
		t.Error("the halting watch lost to the log-only one")
	}
}

// A second arm must not install a second chained tracer: that would double
// every hit line and re-run the prior tracer once per install.
func TestWatchNextReg_HookInstalledOnce(t *testing.T) {
	d := newRemoteForNRWatch(t)

	priorCalls := 0
	d.emu.nextRegs.SetTracer(func(reg, val byte, isWrite bool) { priorCalls++ })

	d.cmdWatchNextReg([]string{"12"})
	d.cmdWatchNextReg([]string{"13"})
	d.emu.nextRegs.WriteReg(0x12, 0x2A)

	if priorCalls != 1 {
		t.Errorf("prior tracer ran %d times for one write, want 1", priorCalls)
	}
}

// A copper MOVE reaches the NextReg dispatcher through the same WriteReg the
// CPU reaches through ports $243B/$253B. Reporting the Z80's PC for one would
// name an instruction that had nothing to do with the write, so the hit line
// asks the copper whether the write is its own.

// writerProbe stands in for the watch hook: it asks the same question, at the
// same instant the hook would.
type writerProbe struct {
	d         *remoteDebugger
	by, where string
	calls     int
}

func (w *writerProbe) WriteReg(reg, val byte) {
	w.by, w.where = w.d.nextRegWriter()
	w.calls++
}

func TestNextRegWriter_NamesTheCPUWhenNoCopperIsRunning(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.emu.cpu.PC = 0x8123

	by, where := d.nextRegWriter()
	if by != "cpu" {
		t.Errorf("by = %q, want \"cpu\"", by)
	}
	if !strings.Contains(where, "8123") {
		t.Errorf("where = %q, want it to carry the PC $8123", where)
	}
}

func TestNextRegWriter_NamesTheCopperDuringAMove(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.emu.cpu.PC = 0x8123

	cop := copper.New()
	d.emu.nextCopper = cop
	probe := &writerProbe{d: d}
	cop.SetRegWriter(probe)

	// NOOP then MOVE NR$12,$34 then HALT, high byte first as NR$60 takes it.
	for _, inst := range []uint16{0x0000, 0x1234, 0xFFFF} {
		cop.WriteData(byte(inst >> 8))
		cop.WriteData(byte(inst & 0xFF))
	}
	cop.SetWritePtrLow(0)
	cop.SetWritePtrHighAndMode(byte(copper.StartFromZero) << 6)
	cop.Step(0, 511, 64)

	if probe.calls != 1 {
		t.Fatalf("MOVEs executed = %d, want 1", probe.calls)
	}
	if probe.by != "copper" {
		t.Errorf("by = %q, want \"copper\"", probe.by)
	}
	if strings.Contains(probe.where, "8123") {
		t.Errorf("where = %q: a copper MOVE was reported against the Z80's PC", probe.where)
	}
	if !strings.Contains(probe.where, "1") {
		t.Errorf("where = %q, want it to name copper instruction 1", probe.where)
	}
}

// Once the copper has stopped, a CPU write must go back to being reported
// against the CPU. A latched flag would misattribute every later write.
func TestNextRegWriter_ReturnsToTheCPUAfterTheMove(t *testing.T) {
	d := newRemoteForNRWatch(t)
	d.emu.cpu.PC = 0x8123

	cop := copper.New()
	d.emu.nextCopper = cop
	cop.SetRegWriter(&writerProbe{d: d})
	for _, inst := range []uint16{0x1234, 0xFFFF} {
		cop.WriteData(byte(inst >> 8))
		cop.WriteData(byte(inst & 0xFF))
	}
	cop.SetWritePtrLow(0)
	cop.SetWritePtrHighAndMode(byte(copper.StartFromZero) << 6)
	cop.Step(0, 511, 64)

	if by, _ := d.nextRegWriter(); by != "cpu" {
		t.Errorf("after the copper stopped, by = %q, want \"cpu\"", by)
	}
}

// watch-nextreg installs a dispatcher tracer that the CPU-execution goroutine
// reads without synchronization, exactly as nr-trace and trace-nextreg-deltas
// do, so it must implicitly pause before arming.
func TestWatchNextReg_NeedsPause(t *testing.T) {
	if !commandsNeedingPause["watch-nextreg"] {
		t.Error("commandsNeedingPause[\"watch-nextreg\"] = false: arming installs a " +
			"tracer the CPU goroutine reads unsynchronized")
	}
}

// The command has to be reachable through the telnet dispatcher and listed in
// the help line, or it exists only to its own unit tests.
func TestWatchNextReg_IsDispatchedAndAdvertised(t *testing.T) {
	d := newRemoteForNRWatch(t)
	// Already paused, so the implicit pause-and-ack has nothing to wait for.
	d.paused.Store(true)

	if got := d.handleCommand("watch-nextreg"); !strings.HasPrefix(got, "OK") {
		t.Errorf("watch-nextreg = %q, want the empty-list reply", got)
	}
	if got := d.handleCommand("watch-nextreg 12"); !strings.HasPrefix(got, "OK") {
		t.Errorf("watch-nextreg 12 = %q, want an OK", got)
	}
	if !strings.Contains(d.handleCommand("help"), "watch-nextreg") {
		t.Error("help does not list watch-nextreg")
	}
}

// NextRegs exist only on the Next, and cmd/zx_go/next.go nils e.nextRegs for
// every other model. watch-nextreg has to say so rather than dereference it:
// the command is inherently Next-only, so there is nothing to make work
// elsewhere, but a debugger command must not be able to kill the emulator.
func TestCmdWatchNextRegRefusesANonNextMachine(t *testing.T) {
	d := &remoteDebugger{emu: &emulator{}} // no nextRegs, as on a 48K session
	got := d.cmdWatchNextReg([]string{"43"})
	if !strings.HasPrefix(got, "ERR") {
		t.Errorf("watch-nextreg on a non-Next machine = %q, want an ERR: "+
			"NextRegs do not exist there", got)
	}
	if !strings.Contains(got, "Next") {
		t.Errorf("watch-nextreg refusal = %q, want it to say the command is "+
			"Next-only, so the user knows it is the machine and not the syntax", got)
	}
}

// The same guard has to hold for the paths that reach the hook without adding a
// watch, so listing and clearing stay usable on any machine.
func TestCmdWatchNextRegListAndClearAreSafeOnANonNextMachine(t *testing.T) {
	d := &remoteDebugger{emu: &emulator{}}
	if got := d.cmdWatchNextReg(nil); !strings.HasPrefix(got, "OK") {
		t.Errorf("no-arg listing on a non-Next machine = %q, want OK", got)
	}
	if got := d.cmdWatchNextReg([]string{"off"}); !strings.HasPrefix(got, "OK") {
		t.Errorf("watch-nextreg off on a non-Next machine = %q, want OK", got)
	}
}

// nr-trace and trace-nextreg-deltas arm the same NextReg tracer chain and had
// the same unguarded dereference. Both are Next-only for the same reason, and
// both are documented in DEBUGGER.md as refusing rather than crashing.
func TestNextRegTracerCommandsRefuseANonNextMachine(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(*remoteDebugger) string
	}{
		{"nr-trace", func(d *remoteDebugger) string { return d.cmdNRTrace([]string{"43"}) }},
		{"trace-nextreg-deltas", func(d *remoteDebugger) string {
			return d.cmdTraceNRDeltas([]string{"43"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := &remoteDebugger{emu: &emulator{}}
			got := c.run(d)
			if !strings.HasPrefix(got, "ERR") {
				t.Errorf("%s on a non-Next machine = %q, want an ERR", c.name, got)
			}
		})
	}
}

// trace-nextreg-deltas has two arming paths, and "all" is the one that skips
// the register-parsing loop, so it needs the same guard.
func TestTraceNRDeltasAllRefusesANonNextMachine(t *testing.T) {
	d := &remoteDebugger{emu: &emulator{}}
	if got := d.cmdTraceNRDeltas([]string{"all"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("trace-nextreg-deltas all on a non-Next machine = %q, want an ERR", got)
	}
}

// A register list is armed all at once or not at all. Adding each register as
// it parses means a list that fails halfway leaves the earlier ones armed while
// the command reports an error and never installs the hook: the user believes
// nothing happened, and some later command that does install the hook makes the
// machine halt on a register they were never told was being watched.
func TestABadRegisterInTheListArmsNothing(t *testing.T) {
	d := newRemoteForNRWatch(t)
	if got := d.cmdWatchNextReg([]string{"12,ZZ"}); !strings.HasPrefix(got, "ERR") {
		t.Fatalf("watch-nextreg 12,ZZ = %q, want an ERR", got)
	}
	if ws := d.nrWatches.list(); len(ws) != 0 {
		t.Errorf("watches after a failed parse = %+v, want none: the registers "+
			"before the bad one must not survive the error", ws)
	}
}

// `log` on its own is not a register. It has to be recognised wherever it is
// the last argument, so the command reports the real problem instead of trying
// to parse "log" as hex.
func TestBareLogIsNotParsedAsARegister(t *testing.T) {
	d := newRemoteForNRWatch(t)
	got := d.cmdWatchNextReg([]string{"log"})
	if strings.Contains(got, "bad reg") {
		t.Errorf("watch-nextreg log = %q, want the no-register error, not a hex "+
			"parse failure on the word \"log\"", got)
	}
	if !strings.HasPrefix(got, "ERR") {
		t.Errorf("watch-nextreg log = %q, want an ERR naming the missing register", got)
	}
}

// A model switch builds a fresh emulator and hands the old one its parts, so
// emu.nextRegs becomes a different dispatcher. A hook remembered as "installed"
// by a bare flag is then installed on a dispatcher nobody is using: the watches
// still list as armed and never fire again, which is worse than losing them,
// because the user is told they are watching something they are not.
//
// Keying the memo on the dispatcher it was installed on makes it self-heal.
func TestWatchNextRegSurvivesTheDispatcherBeingReplaced(t *testing.T) {
	d := newRemoteForNRWatch(t)
	if got := d.cmdWatchNextReg([]string{"12"}); !strings.HasPrefix(got, "OK") {
		t.Fatalf("arm = %q", got)
	}

	// What a model switch does to the emulator.
	d.emu.nextRegs = nextregs.New()
	d.paused.Store(false)

	// Re-arming is the natural moment to notice, but so is any later arm.
	if got := d.cmdWatchNextReg([]string{"12"}); !strings.HasPrefix(got, "OK") {
		t.Fatalf("re-arm = %q", got)
	}
	d.emu.nextRegs.Select(0x12)
	d.emu.nextRegs.WriteData(0x2A)

	if !d.paused.Load() {
		t.Error("a write to NR$12 did not halt after the dispatcher was replaced: " +
			"the hook is still on the old one")
	}
}

// The same parse-then-arm rule as watch-nextreg, for the same reason: a list
// that fails halfway, or a command refused because the machine has no NextRegs,
// must leave nothing armed. Both were wrong here in the opposite order.
func TestNRTraceArmsNothingWhenItRefuses(t *testing.T) {
	t.Run("non-Next machine", func(t *testing.T) {
		d := &remoteDebugger{emu: &emulator{}}
		if got := d.cmdNRTrace([]string{"10,20"}); !strings.HasPrefix(got, "ERR") {
			t.Fatalf("nr-trace on a non-Next machine = %q, want an ERR", got)
		}
		if regs := d.nrTraces.list(); len(regs) != 0 {
			t.Errorf("traces after a refused command = %v, want none", regs)
		}
	})
	t.Run("bad register in the list", func(t *testing.T) {
		d := newRemoteForNRWatch(t)
		if got := d.cmdNRTrace([]string{"10,ZZ"}); !strings.HasPrefix(got, "ERR") {
			t.Fatalf("nr-trace 10,ZZ = %q, want an ERR", got)
		}
		if regs := d.nrTraces.list(); len(regs) != 0 {
			t.Errorf("traces after a failed parse = %v, want none: the registers "+
				"before the bad one must not survive the error", regs)
		}
	})
}
