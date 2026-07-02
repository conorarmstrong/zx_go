package debugger

import (
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Covers the non-GUI logic in debugger.go — CheckBreakpoint
// (plain / bank-filtered / conditional), cpuBank, visualCPUState,
// and cpuStackSource. These all run without the Fyne window, so the
// Debugger is built field-by-field rather than via New.

// newLogicDebugger builds a Debugger with just the fields the
// breakpoint-evaluation paths read: cpu, mem, and the breakpoint map.
func newLogicDebugger(t *testing.T, model roms.SpectrumModel) (*Debugger, *z80.CPU, *memory.Memory) {
	t.Helper()
	mem := makeTestMemory(t, model)
	cpu := z80.New(mem, nil)
	d := &Debugger{
		cpu: cpu,
		mem: mem,
		bps: NewBreakpointSet(),
	}
	return d, cpu, mem
}

func TestCheckBreakpoint_NoEntry(t *testing.T) {
	d, cpu, _ := newLogicDebugger(t, roms.Model48K)
	cpu.PC = 0x8000
	if d.CheckBreakpoint() {
		t.Error("CheckBreakpoint with empty set should not fire")
	}
}

func TestCheckBreakpoint_PlainHit(t *testing.T) {
	d, cpu, _ := newLogicDebugger(t, roms.Model48K)
	cpu.PC = 0x8000
	d.bps.Add(0x8000, BPEntry{Bank: -1})
	if !d.CheckBreakpoint() {
		t.Error("CheckBreakpoint at matching PC (any-bank) should fire")
	}
}

func TestCheckBreakpoint_BankMismatch(t *testing.T) {
	d, cpu, _ := newLogicDebugger(t, roms.Model128K)
	cpu.PC = 0x0100
	// Default port state → cpuBank() == 0. Demand bank 1.
	d.bps.Add(0x0100, BPEntry{Bank: 1})
	if d.CheckBreakpoint() {
		t.Error("CheckBreakpoint with bank filter mismatch should not fire")
	}
}

func TestCheckBreakpoint_BankMatch(t *testing.T) {
	d, cpu, mem := newLogicDebugger(t, roms.Model128K)
	cpu.PC = 0x0100
	// Page ROM 1 in: 7FFD bit 4 set → cpuBank() == 1.
	mem.PageMemory(0x10)
	if got := d.cpuBank(); got != 1 {
		t.Fatalf("cpuBank after PageMemory(0x10) = %d, want 1", got)
	}
	d.bps.Add(0x0100, BPEntry{Bank: 1})
	if !d.CheckBreakpoint() {
		t.Error("CheckBreakpoint with matching bank filter should fire")
	}
}

func TestCheckBreakpoint_CondFalse(t *testing.T) {
	d, cpu, _ := newLogicDebugger(t, roms.Model48K)
	cpu.PC = 0x6000
	cpu.A = 0x00
	cond, err := ParseCondition("a == $42")
	if err != nil {
		t.Fatal(err)
	}
	d.bps.Add(0x6000, BPEntry{Bank: -1, HasCond: true, Cond: cond, Source: "a == $42"})
	if d.CheckBreakpoint() {
		t.Error("CheckBreakpoint with false guard should not fire")
	}
}

func TestCheckBreakpoint_CondTrue(t *testing.T) {
	d, cpu, _ := newLogicDebugger(t, roms.Model48K)
	cpu.PC = 0x6000
	cpu.A = 0x42
	cond, err := ParseCondition("a == $42")
	if err != nil {
		t.Fatal(err)
	}
	d.bps.Add(0x6000, BPEntry{Bank: -1, HasCond: true, Cond: cond, Source: "a == $42"})
	if !d.CheckBreakpoint() {
		t.Error("CheckBreakpoint with true guard should fire")
	}
}

func TestVisualCPUState_Reg(t *testing.T) {
	_, cpu, mem := newLogicDebugger(t, roms.Model48K)
	cpu.PC = 0x1234
	cpu.SP = 0xFFF0
	cpu.A, cpu.F = 0x11, 0x22
	cpu.B, cpu.C = 0x33, 0x44
	cpu.D, cpu.E = 0x55, 0x66
	cpu.H, cpu.L = 0x77, 0x88
	cpu.IX, cpu.IY = 0x9999, 0xAAAA
	cpu.IFF1, cpu.IFF2 = true, false
	cpu.IM = 2
	cpu.Halted = true
	s := visualCPUState{cpu: cpu, mem: mem, bank: 3}

	cases := []struct {
		name string
		want int64
	}{
		{"pc", 0x1234}, {"sp", 0xFFF0}, {"a", 0x11}, {"f", 0x22},
		{"b", 0x33}, {"c", 0x44}, {"d", 0x55}, {"e", 0x66},
		{"h", 0x77}, {"l", 0x88}, {"ix", 0x9999}, {"iy", 0xAAAA},
		{"iff1", 1}, {"iff2", 0}, {"im", 2}, {"halted", 1}, {"bank", 3},
	}
	for _, c := range cases {
		got, ok := s.Reg(c.name)
		if !ok {
			t.Errorf("Reg(%q) ok=false, want true", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("Reg(%q) = %d, want %d", c.name, got, c.want)
		}
	}
	if _, ok := s.Reg("nonsense"); ok {
		t.Error("Reg(unknown) ok=true, want false")
	}
}

func TestVisualCPUState_ReadMem(t *testing.T) {
	_, cpu, mem := newLogicDebugger(t, roms.Model48K)
	mem.Write(0x8000, 0xBE)
	s := visualCPUState{cpu: cpu, mem: mem}
	if got := s.ReadMem(0x8000); got != 0xBE {
		t.Errorf("ReadMem($8000) = $%02X, want $BE", got)
	}
}

func TestBpStoreAdapter_RoundTrip(t *testing.T) {
	// The shared BreakpointSet is the BreakpointsStore the GUI and
	// telnet both use; this pins its List/Add/Remove/Clear contract
	// (copy-on-read, etc.).
	a := NewBreakpointSet()

	a.Add(0x4000, BPEntry{Bank: 2})
	a.Add(0x8000, BPEntry{Bank: -1})
	list := a.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	if list[0x4000].Bank != 2 {
		t.Errorf("entry $4000 Bank = %d, want 2", list[0x4000].Bank)
	}
	// List returns a copy — mutating it must not touch the store.
	delete(list, 0x4000)
	if _, ok := a.List()[0x4000]; !ok {
		t.Error("mutating List() result corrupted the store")
	}

	a.Remove(0x4000)
	if _, ok := a.List()[0x4000]; ok {
		t.Error("Remove did not delete entry")
	}

	a.Clear()
	if len(a.List()) != 0 {
		t.Error("Clear did not empty the store")
	}
}

func TestFormatAddr(t *testing.T) {
	d := &Debugger{}
	d.hexAddrBase = 16
	if got := d.formatAddr(0x1A2B); got != "1A2B" {
		t.Errorf("base16 = %q, want 1A2B", got)
	}
	d.hexAddrBase = 10
	if got := d.formatAddr(0x1A2B); got != "6699" {
		t.Errorf("base10 = %q, want 6699", got)
	}
	d.hexAddrBase = 8
	if got := d.formatAddr(0x1A2B); got != "15053" {
		t.Errorf("base8 = %q, want 15053", got)
	}
}

// newDasmListStub builds a minimal *widget.List usable as
// Debugger.dasmList in tests that exercise refreshDisassembly
// without going through buildUI (which requires a shown window).
func newDasmListStub() *widget.List {
	return widget.NewList(
		func() int { return 0 },
		func() fyne.CanvasObject { return canvas.NewText("", color.White) },
		func(widget.ListItemID, fyne.CanvasObject) {},
	)
}

func TestRefreshDisassembly_PicksUpMemoryEditAtSamePC(t *testing.T) {
	d, cpu, mem := newLogicDebugger(t, roms.Model48K)
	d.dasmList = newDasmListStub()
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00) // NOP

	d.refreshDisassembly()
	if got := d.dasmCache[0].Mnem; got != "NOP" {
		t.Fatalf("initial disassembly Mnem = %q, want NOP", got)
	}

	// Self-modify the byte at PC without moving PC — a paused-debugger
	// memory write (e.g. the Write Mem dialog) must be reflected on
	// the next refresh even though PC is unchanged.
	mem.Write(0x8000, 0xF3) // DI
	d.refreshDisassembly()
	if got := d.dasmCache[0].Mnem; got != "DI" {
		t.Errorf("refreshDisassembly kept stale bytes: Mnem = %q, want DI", got)
	}
}

// TestStopRefresh_DoesNotDropSignalWhenReceiverBusy pins that
// stopRefresh's stop signal is never lost. startRefresh's goroutine
// isn't always parked in its select — it can be off running
// fyne.Do(d.Refresh) when the window closes — so the signal must be
// durable (e.g. closing the channel) rather than a single
// best-effort non-blocking send.
func TestStopRefresh_DoesNotDropSignalWhenReceiverBusy(t *testing.T) {
	d := &Debugger{stopChan: make(chan struct{})}
	busy := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(busy)
		// Simulate the refresh goroutine being busy elsewhere (e.g.
		// inside fyne.Do) at the moment stopRefresh runs, so it isn't
		// yet parked on the receive.
		time.Sleep(20 * time.Millisecond)
		<-d.stopChan
		close(done)
	}()
	<-busy
	d.stopRefresh()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop signal was dropped: receiver never woke up")
	}
}

// TestStopRefresh_SafeToCallMultipleTimes guards against a panic if
// the window's OnClosed callback (or a caller) invokes stopRefresh
// more than once on the same Debugger.
func TestStopRefresh_SafeToCallMultipleTimes(t *testing.T) {
	d := &Debugger{stopChan: make(chan struct{})}
	d.stopRefresh()
	d.stopRefresh()
}

func TestCpuStackSource(t *testing.T) {
	_, cpu, _ := newLogicDebugger(t, roms.Model48K)
	cpu.SP = 0xFF00
	cpu.IFF1 = true
	cpu.IM = 1
	cpu.Halted = true
	s := cpuStackSource{cpu: cpu}
	if s.SP() != 0xFF00 {
		t.Errorf("SP() = $%04X, want $FF00", s.SP())
	}
	if !s.IFF1() {
		t.Error("IFF1() = false, want true")
	}
	if s.IM() != 1 {
		t.Errorf("IM() = %d, want 1", s.IM())
	}
	if !s.Halted() {
		t.Error("Halted() = false, want true")
	}
}
