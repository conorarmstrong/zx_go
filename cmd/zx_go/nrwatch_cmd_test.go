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
