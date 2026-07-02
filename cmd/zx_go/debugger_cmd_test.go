package main

import (
	"strings"
	"testing"
)

// Covers the remoteDebugger memory/register/breakpoint command
// handlers and the fmt* state formatters. All operate on a wired
// cpu+mem fixture (newRemoteWithCPU) and return protocol strings, so
// they unit-test without the TCP loop.

// TestCommandsNeedingPause_HookInstallers checks that commands which
// install a live callback on state the CPU-execution goroutine reads
// without synchronization (prefetch hooks, write loggers) require an
// implicit pause before running, matching their structurally
// identical siblings (watch-mem, watch-read, catch).
func TestCommandsNeedingPause_HookInstallers(t *testing.T) {
	// trace-writes installs mem.WriteObserver; trace-nextreg-deltas (and
	// its nr-deltas alias) installs the NextReg tracer — the same
	// SetTracer mechanism as nr-trace. All are read by the CPU-execution
	// goroutine without synchronization, so each must implicitly pause.
	for _, cmd := range []string{
		"crash-detect", "trace-divmmc-ram",
		"trace-writes", "trace-nextreg-deltas", "nr-deltas",
	} {
		if !commandsNeedingPause[cmd] {
			t.Errorf("commandsNeedingPause[%q] = false, want true (installs a hook the CPU goroutine reads unsynchronized)", cmd)
		}
	}
}

func TestCpuState_RegReadMem(t *testing.T) {
	d := newRemoteWithCPU(t)
	c := d.emu.cpu
	c.PC, c.SP = 0x1234, 0xFFEE
	c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L = 1, 2, 3, 4, 5, 6, 7, 8
	c.IX, c.IY = 0x1111, 0x2222
	c.IFF1, c.IFF2, c.IM, c.Halted = true, false, 2, true
	s := cpuState{cpu: c, mem: d.emu.mem, bank: 1}
	want := map[string]int64{
		"pc": 0x1234, "sp": 0xFFEE, "a": 1, "f": 2, "b": 3, "c": 4,
		"d": 5, "e": 6, "h": 7, "l": 8, "ix": 0x1111, "iy": 0x2222,
		"iff1": 1, "iff2": 0, "im": 2, "halted": 1, "bank": 1,
	}
	for name, exp := range want {
		got, ok := s.Reg(name)
		if !ok || got != exp {
			t.Errorf("Reg(%q) = %d,%v want %d,true", name, got, ok, exp)
		}
	}
	if _, ok := s.Reg("zzz"); ok {
		t.Error("Reg(unknown) should report ok=false")
	}
	d.emu.mem.Write(0x9000, 0x5A)
	if got := s.ReadMem(0x9000); got != 0x5A {
		t.Errorf("ReadMem = $%02X, want $5A", got)
	}
}

func TestFmtRegisters(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.emu.cpu.PC = 0x8000
	got := d.fmtRegisters()
	if !strings.HasPrefix(got, "PC=") || !strings.Contains(got, "SP=$") || !strings.Contains(got, "insns=") {
		t.Errorf("fmtRegisters = %q", got)
	}
}

func TestFmtStackWalk(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.emu.cpu.SP = 0x8000
	got := d.fmtStackWalk()
	if !strings.HasPrefix(got, "sp=$8000 words=") {
		t.Errorf("fmtStackWalk = %q", got)
	}
	// 16 words expected.
	words := strings.Fields(strings.SplitN(got, "words=", 2)[1])
	if len(words) != 16 {
		t.Errorf("stack walk produced %d words, want 16", len(words))
	}
}

func TestFmtMMU(t *testing.T) {
	d := newRemoteWithCPU(t)
	got := d.fmtMMU()
	if !strings.HasPrefix(got, "rom_bank=") || !strings.Contains(got, "slots=") {
		t.Errorf("fmtMMU = %q", got)
	}
}

func TestFmtDivMMC_NoULA(t *testing.T) {
	d := newRemoteWithCPU(t) // emu.ula is nil
	if got := d.fmtDivMMC(); got != "no-ula" {
		t.Errorf("fmtDivMMC no-ula = %q", got)
	}
}

func TestCmdGetReadWriteMemory(t *testing.T) {
	d := newRemoteWithCPU(t)

	if got := d.cmdGetMemory([]string{"8000"}); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("get-memory too-few = %q", got)
	}
	if got := d.cmdGetMemory([]string{"zz", "4"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("get-memory bad addr = %q", got)
	}
	if got := d.cmdGetMemory([]string{"8000", "zz"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("get-memory bad len = %q", got)
	}
	// write then read back.
	if got := d.cmdWriteMemory([]string{"8000", "AB"}); got != "OK" {
		t.Errorf("write-memory = %q", got)
	}
	if got := d.cmdReadMemory([]string{"8000"}); got != "OK $AB" {
		t.Errorf("read-memory = %q", got)
	}
	if got := d.cmdGetMemory([]string{"8000", "1"}); got != "OK AB" {
		t.Errorf("get-memory = %q", got)
	}
	// length clamp: request 0x400 → 256 bytes.
	got := d.cmdGetMemory([]string{"8000", "400"})
	if n := len(strings.Fields(strings.TrimPrefix(got, "OK "))); n != 256 {
		t.Errorf("get-memory clamp = %d bytes, want 256", n)
	}
	if got := d.cmdReadMemory(nil); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("read-memory no-arg = %q", got)
	}
	if got := d.cmdWriteMemory([]string{"8000"}); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("write-memory too-few = %q", got)
	}
}

func TestCmdSetReg(t *testing.T) {
	d := newRemoteWithCPU(t)
	c := d.emu.cpu

	if got := d.cmdSetReg([]string{"A"}); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("set-reg too-few = %q", got)
	}
	if got := d.cmdSetReg([]string{"A", "zz"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("set-reg bad val = %q", got)
	}
	if got := d.cmdSetReg([]string{"ZZ", "1"}); !strings.HasPrefix(got, "ERR unknown register") {
		t.Errorf("set-reg unknown = %q", got)
	}

	d.cmdSetReg([]string{"A", "42"})
	if c.A != 0x42 {
		t.Errorf("A = $%02X, want $42", c.A)
	}
	d.cmdSetReg([]string{"BC", "1234"})
	if c.B != 0x12 || c.C != 0x34 {
		t.Errorf("BC = $%02X%02X, want $1234", c.B, c.C)
	}
	d.cmdSetReg([]string{"af", "5566"})
	if c.A != 0x55 || c.F != 0x66 {
		t.Errorf("AF = $%02X%02X, want $5566", c.A, c.F)
	}
	d.cmdSetReg([]string{"PC", "C000"})
	if c.PC != 0xC000 {
		t.Errorf("PC = $%04X, want $C000", c.PC)
	}
	d.cmdSetReg([]string{"IM", "2"})
	if c.IM != 2 {
		t.Errorf("IM = %d, want 2", c.IM)
	}
}

func TestCmdBreakpoints(t *testing.T) {
	d := newRemoteWithCPU(t)

	if got := d.cmdSetBreakpoint(nil); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("set-bp no-arg = %q", got)
	}
	if got := d.cmdSetBreakpoint([]string{"zz"}); !strings.HasPrefix(got, "ERR") {
		t.Errorf("set-bp bad addr = %q", got)
	}
	// Default bank (current ROM bank).
	if got := d.cmdSetBreakpoint([]string{"8000"}); !strings.Contains(got, "breakpoint @$8000") {
		t.Errorf("set-bp default = %q", got)
	}
	// Explicit any-bank.
	if got := d.cmdSetBreakpoint([]string{"9000", "any-bank"}); !strings.Contains(got, "(any bank)") {
		t.Errorf("set-bp any-bank = %q", got)
	}
	// Explicit bank + condition.
	got := d.cmdSetBreakpoint([]string{"A000", "bank=2", "if", "a", "==", "$42"})
	if !strings.Contains(got, "(bank=2)") || !strings.Contains(got, "if") {
		t.Errorf("set-bp bank+cond = %q", got)
	}
	// Bad bank value.
	if got := d.cmdSetBreakpoint([]string{"B000", "bank=xx"}); !strings.HasPrefix(got, "ERR bad bank") {
		t.Errorf("set-bp bad bank = %q", got)
	}
	// Unrecognised clause.
	if got := d.cmdSetBreakpoint([]string{"B000", "frobnicate"}); !strings.HasPrefix(got, "ERR unrecognised") {
		t.Errorf("set-bp bad clause = %q", got)
	}

	// list should show the three valid BPs.
	list := d.cmdListBreakpoints()
	if !strings.HasPrefix(list, "OK ") || !strings.Contains(list, "any bank!") {
		t.Errorf("list = %q", list)
	}

	// clear one.
	if got := d.cmdClearBreakpoint([]string{"8000"}); got != "OK cleared @$8000" {
		t.Errorf("clear = %q", got)
	}
	if got := d.cmdClearBreakpoint(nil); !strings.HasPrefix(got, "ERR usage") {
		t.Errorf("clear no-arg = %q", got)
	}

	// clear remaining → empty list.
	d.cmdClearBreakpoint([]string{"9000"})
	d.cmdClearBreakpoint([]string{"A000"})
	if got := d.cmdListBreakpoints(); got != "OK (no breakpoints set)" {
		t.Errorf("empty list = %q", got)
	}
}
