package debugger

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Reverse mode is the one debugger feature that can actively mislead. Every
// panel normally reads the live CPU; while the cursor is away from the present
// they must read the recorded instant instead, and they must say so loudly
// enough that nobody reads a historical register dump as the machine's current
// state. These tests pin both halves: the redirect, and the announcement.

// newReverseDebugger builds a Debugger with only the fields the register /
// disassembly / hex / status renderers touch. buildUI can't run headlessly
// (SetContent forces a text-measuring layout fyne's test driver panics on), so
// the panels are assembled field-by-field exactly as debugger_logic_test.go
// does.
func newReverseDebugger(t *testing.T, h *History) (*Debugger, *z80.CPU) {
	t.Helper()
	_ = test.NewApp()
	mem := makeTestMemory(t, roms.Model48K)
	cpu := z80.New(mem, nil)
	d := &Debugger{
		cpu:         cpu,
		mem:         mem,
		bps:         NewBreakpointSet(),
		hexAddrBase: 16,
	}
	for i := range d.regLines {
		d.regLines[i] = mkText(colRegVal)
	}
	d.flagLine = mkText(colFlagOn)
	d.iffLine = mkText(colRegVal)
	d.irqLine = mkText(colRegVal)
	d.haltLine = mkText(colHalted)
	d.statusTxt = mkText(colPaused)
	d.regHeader = headerText("  REGISTERS", colRegName)
	d.dasmHeader = headerText("  DISASSEMBLY", colMnem)
	d.hexHeader = headerText("  MEMORY", colAddr)
	d.revBanner = mkText(colPast)
	d.dasmList = newDasmListStub()
	d.backtrace = NewBacktraceWidget(cpuStackSource{cpu: cpu}, mem.Read)
	d.SetHistory(h)
	return d, cpu
}

// wideRing is a populated wide ring: every field a wide producer fills.
func wideRing(n int) *History {
	h := NewHistoryWide(n)
	for i := 0; i < n; i++ {
		var st [StackWindow]uint16
		for j := range st {
			st[j] = uint16(0xC000 + i*0x100 + j)
		}
		h.Push(HistoryEntry{
			PC: uint16(0x8000 + i), SP: uint16(0xFF00 + i),
			A: byte(i), F: 0x41,
			BC: uint16(0x1200 + i), DE: 0x5678, HL: 0x9ABC,
			IX: 0xDEAD, IY: 0xBEEF,
			AltAF: 0x1111, AltBC: 0x2222, AltDE: 0x3333, AltHL: 0x4444,
			IFFIM: PackIFFIM(true, false, false, 2),
			Insns: uint64(1000 + i),
			Stack: st,
		})
	}
	return h
}

// narrowRing records only what a narrow producer fills. The pair registers are
// deliberately left at zero, because zero is exactly the value a panel would
// print if it forgot they were never recorded.
func narrowRing(n int) *History {
	h := NewHistory(n)
	for i := 0; i < n; i++ {
		h.Push(HistoryEntry{
			PC: uint16(0x9000 + i), SP: uint16(0xFE00 + i),
			A: byte(0x20 + i), F: 0x41,
			IFFIM: PackIFFIM(true, true, false, 1),
			Insns: uint64(2000 + i),
		})
	}
	return h
}

func TestEnteringReverseModeWithoutAHistoryRingIsRefused(t *testing.T) {
	d, _ := newReverseDebugger(t, nil)
	if err := d.EnterReverse(); err == nil {
		t.Fatal("with no history ring there is no past to show; entering reverse must be refused")
	}
	if d.InReverse() {
		t.Error("a refused EnterReverse must leave the debugger showing the live machine")
	}
}

func TestEnteringReverseModeWithAnEmptyRingIsRefused(t *testing.T) {
	d, _ := newReverseDebugger(t, NewHistoryWide(8))
	if err := d.EnterReverse(); err == nil {
		t.Fatal("an empty ring has recorded nothing; entering reverse must be refused")
	}
}

// The redirect. This is the whole feature: while the cursor is in the past the
// register panel must show the recorded instant, and must not show the live
// CPU under any circumstance.
func TestRegisterPanelShowsTheRecordedInstantWhileReversing(t *testing.T) {
	d, cpu := newReverseDebugger(t, wideRing(8))
	cpu.PC, cpu.SP, cpu.A, cpu.F = 0x1234, 0x0100, 0x99, 0x00
	cpu.B, cpu.C = 0xAA, 0xBB

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseStepBack(); err != nil { // newest is insn 1007; now 1006
		t.Fatalf("ReverseStepBack: %v", err)
	}
	d.refreshRegisters()

	if !strings.Contains(d.regLines[0].Text, "8006") {
		t.Errorf("PC line = %q, want the recorded PC $8006", d.regLines[0].Text)
	}
	if strings.Contains(d.regLines[0].Text, "1234") {
		t.Errorf("PC line = %q: it is showing the LIVE PC while the cursor is in the past", d.regLines[0].Text)
	}
	if !strings.Contains(d.regLines[1].Text, "FF06") {
		t.Errorf("SP line = %q, want the recorded SP $FF06", d.regLines[1].Text)
	}
	if !strings.Contains(d.regLines[5].Text, "06") || strings.Contains(d.regLines[5].Text, "99") {
		t.Errorf("A/F line = %q, want the recorded A $06, not the live $99", d.regLines[5].Text)
	}
	if !strings.Contains(d.regLines[6].Text, "12") || strings.Contains(d.regLines[6].Text, "AA") {
		t.Errorf("B/C line = %q, want the recorded BC $1206, not the live BC", d.regLines[6].Text)
	}
}

// Returning to the present is the escape hatch, and it has to actually restore
// the live view — a stuck panel showing the past is the exact failure this
// feature must not have.
func TestReturningToThePresentRestoresTheLiveRegisterView(t *testing.T) {
	d, cpu := newReverseDebugger(t, wideRing(8))
	cpu.PC = 0x1234

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseStepBack(); err != nil {
		t.Fatalf("ReverseStepBack: %v", err)
	}
	d.ExitReverse()

	if d.InReverse() {
		t.Fatal("ExitReverse must leave reverse mode")
	}
	d.refreshRegisters()
	if !strings.Contains(d.regLines[0].Text, "1234") {
		t.Errorf("PC line = %q, want the live PC $1234 back", d.regLines[0].Text)
	}
}

// Stepping forward off the newest recorded instruction is a return to the live
// machine, not an error the user has to understand.
func TestSteppingForwardPastTheNewestRecordedInstructionReturnsToThePresent(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseStepBack(); err != nil {
		t.Fatalf("ReverseStepBack: %v", err)
	}
	if err := d.ReverseStepForward(); err != nil {
		t.Fatalf("ReverseStepForward to the newest entry: %v", err)
	}
	if !d.InReverse() {
		t.Fatal("the newest recorded instruction is still a recorded instant, not the live machine")
	}
	if err := d.ReverseStepForward(); err != nil {
		t.Fatalf("stepping forward off the newest entry: %v", err)
	}
	if d.InReverse() {
		t.Error("stepping forward past the newest recorded instruction hands control back to the live machine")
	}
}

// Stepping back off the oldest survivor is reported, not clamped: the
// instruction existed, the record of it did not survive.
func TestSteppingBackPastTheOldestRecordedInstructionIsReported(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(3))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := d.ReverseStepBack(); err != nil {
			t.Fatalf("ReverseStepBack %d: %v", i, err)
		}
	}
	if err := d.ReverseStepBack(); err == nil {
		t.Fatal("the ring does not reach back further; that must be reported rather than silently clamped")
	}
	e, ok := d.ReverseEntry()
	if !ok || e.PC != 0x8000 {
		t.Errorf("after a refused step the cursor is at PC $%04X, want it unmoved at $8000", e.PC)
	}
}

func TestRunBackMovesTheCursorToTheMatchingInstant(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseRunBack("a == 3"); err != nil {
		t.Fatalf("ReverseRunBack: %v", err)
	}
	e, _ := d.ReverseEntry()
	if e.A != 3 {
		t.Errorf("RunBack stopped at A=$%02X, want 3", e.A)
	}
}

// A condition reading memory has no answer going backwards, and the refusal
// has to reach the user through the same path the button uses.
func TestRunBackRefusesAConditionThatReadsMemoryThatWasNeverRecorded(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	err := d.ReverseRunBack("read $5C78 == 0")
	if err == nil {
		t.Fatal("memory is not recorded per instruction, so this condition must be refused, " +
			"not evaluated against an invented zero")
	}
	if e, _ := d.ReverseEntry(); e.Insns != 1007 {
		t.Error("a refused RunBack must not move the cursor")
	}
}

// A narrow ring recorded PC, SP, A, F and the interrupt state and nothing
// else. The fields it did not record are still present in the struct, holding
// zero, and rendering that zero would state that BC was $0000 at an
// instruction where nobody ever looked. Every such field must read as
// unavailable instead.
func TestNarrowRingRendersUnrecordedRegistersAsUnavailableNotZero(t *testing.T) {
	d, _ := newReverseDebugger(t, narrowRing(4))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	d.refreshRegisters()

	// Recorded: these must show real values.
	for _, tc := range []struct {
		line int
		want string
	}{
		{0, "9003"}, {1, "FE03"}, {5, "23"},
	} {
		if !strings.Contains(d.regLines[tc.line].Text, tc.want) {
			t.Errorf("regLines[%d] = %q, want it to contain the recorded %s",
				tc.line, d.regLines[tc.line].Text, tc.want)
		}
	}

	// Not recorded: never a zero.
	for _, line := range []int{2, 3, 6, 7, 8, 10, 11, 12, 13, 18, 19} {
		got := d.regLines[line].Text
		if !strings.Contains(got, unavailable) {
			t.Errorf("regLines[%d] = %q: a narrow ring never recorded this, so it must read %q, not a value",
				line, got, unavailable)
		}
		if strings.Contains(got, "00") {
			t.Errorf("regLines[%d] = %q: rendering the unrecorded field's zero states a value nobody recorded",
				line, got)
		}
	}
}

// A wide ring records the pairs and the shadow set, so those must show; I and
// R are recorded by no ring at all, so they must not.
func TestWideRingShowsPairsAndShadowsButStillHidesWhatNoRingRecords(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(4))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	d.refreshRegisters()

	for _, tc := range []struct {
		line int
		want string
	}{
		{2, "DEAD"}, {3, "BEEF"},
		{6, "12"}, {7, "56"}, {8, "9A"},
		{10, "11"}, {11, "22"}, {12, "33"}, {13, "44"},
	} {
		if !strings.Contains(d.regLines[tc.line].Text, tc.want) {
			t.Errorf("regLines[%d] = %q, want the recorded %s", tc.line, d.regLines[tc.line].Text, tc.want)
		}
	}
	// I and R are in no HistoryEntry; a wide ring does not change that.
	if got := d.regLines[15].Text; !strings.Contains(got, unavailable) {
		t.Errorf("I/R line = %q: neither register is recorded by any ring, so both must read %q", got, unavailable)
	}
	if got := d.regLines[15].Text; strings.Contains(got, "00") {
		t.Errorf("I/R line = %q: showing 00 claims a value the recording never held", got)
	}
	// The interrupt counters are live totals, not per-instruction state.
	if got := d.irqLine.Text; !strings.Contains(got, unavailable) {
		t.Errorf("IRQ line = %q, want %q: the counters are live totals, not part of the recorded instant", got, unavailable)
	}
}

// The stack window is what makes a backwards step through a CALL legible, so a
// wide ring's recorded words must actually appear.
func TestWideRingShowsTheRecordedStackWindowInTheRegisterPanel(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(4))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	d.refreshRegisters()

	// Newest entry is i=3: stack words $C300, $C301, $C302, $C303.
	if !strings.Contains(d.regLines[18].Text, "C300") || !strings.Contains(d.regLines[18].Text, "C301") {
		t.Errorf("stack line = %q, want the recorded words $C300/$C301", d.regLines[18].Text)
	}
	if !strings.Contains(d.regLines[19].Text, "C302") {
		t.Errorf("stack line = %q, want the recorded word $C302", d.regLines[19].Text)
	}
}

// The unavailable marker must be visually distinct as well as textually
// distinct: it is the absence of information, not information.
func TestUnavailableValuesAreNotColouredLikeRecordedOnes(t *testing.T) {
	d, _ := newReverseDebugger(t, narrowRing(4))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	d.refreshRegisters()

	if d.regLines[6].Color == d.regLines[0].Color {
		t.Error("an unrecorded register is drawn in the same colour as a recorded one, " +
			"so nothing on screen distinguishes a value from its absence")
	}
}

// The disassembly has to follow the cursor, otherwise the register panel shows
// one instruction and the code window another.
func TestDisassemblyFollowsTheCursorRatherThanTheLivePC(t *testing.T) {
	d, cpu := newReverseDebugger(t, wideRing(8))
	cpu.PC = 0x1234

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseStepBack(); err != nil { // PC $8006
		t.Fatalf("ReverseStepBack: %v", err)
	}
	d.refreshDisassembly()

	if len(d.dasmCache) == 0 {
		t.Fatal("no disassembly produced")
	}
	if d.dasmCache[0].Addr != 0x8006 {
		t.Errorf("disassembly starts at $%04X, want the recorded PC $8006", d.dasmCache[0].Addr)
	}
	if d.displayPC() != 0x8006 {
		t.Errorf("displayPC() = $%04X, want the recorded PC $8006", d.displayPC())
	}
	// The PC marker must sit on the recorded instruction.
	row, _ := d.dasmRowText(0)
	if !strings.HasPrefix(row, ">") && !strings.HasPrefix(row, "*>") {
		t.Errorf("row 0 = %q, want the PC marker on the recorded instruction", row)
	}
}

// The bytes behind that disassembly come from memory as it is NOW. The
// recording holds no memory, so the panel has to admit which of the two it is
// showing rather than let the user assume both are historical.
func TestDisassemblyHeaderAdmitsItsBytesAreFromCurrentMemory(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	got := strings.ToLower(d.dasmHeader.Text)
	if !strings.Contains(got, "memory") || !strings.Contains(got, "current") {
		t.Errorf("disassembly header = %q, want it to say the bytes come from current memory, "+
			"since the recording holds none", d.dasmHeader.Text)
	}
}

// Memory is not recorded per instruction. The hex dump therefore cannot follow
// the cursor, and showing the CURRENT bytes beside a historical register dump
// is the single most misleading thing this feature could do.
func TestMemoryViewShowsUnavailableRatherThanTheLiveBytesWhileReversing(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	d.mem.Write(0x4000, 0xBE)

	const row = 0x4000 / 16
	live, _ := d.hexRowText(row)
	if !strings.Contains(live, "BE") {
		t.Fatalf("live hex row = %q, want the byte $BE that is actually in memory", live)
	}

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	past, _ := d.hexRowText(row)
	if strings.Contains(past, "BE") {
		t.Errorf("hex row while reversing = %q: it is showing memory as it is NOW beside registers "+
			"as they WERE, which reads as one consistent instant and is not", past)
	}
	if !strings.Contains(past, unavailable) {
		t.Errorf("hex row while reversing = %q, want %q: memory is not recorded per instruction", past, unavailable)
	}
	if !strings.Contains(strings.ToLower(d.hexHeader.Text), "not recorded") {
		t.Errorf("memory header = %q, want it to say memory is not recorded per instruction", d.hexHeader.Text)
	}
}

// The announcement. Whatever else is on screen, the status line and the banner
// have to make "this is the past" unmissable.
func TestStatusLineAndBannerAnnounceTheRecordedView(t *testing.T) {
	d, cpu := newReverseDebugger(t, wideRing(8))
	cpu.PC = 0x1234

	d.refreshStatus()
	liveCol := d.statusTxt.Color
	if d.revBanner.Text != "" {
		t.Errorf("banner = %q, want it empty while the live machine is on screen", d.revBanner.Text)
	}

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseStepBack(); err != nil {
		t.Fatalf("ReverseStepBack: %v", err)
	}
	d.refreshStatus()

	if !strings.Contains(d.statusTxt.Text, "PAST") {
		t.Errorf("status = %q, want it to say the view is the recorded past", d.statusTxt.Text)
	}
	if !strings.Contains(d.statusTxt.Text, "8006") {
		t.Errorf("status = %q, want the recorded PC $8006", d.statusTxt.Text)
	}
	if strings.Contains(d.statusTxt.Text, "1234") {
		t.Errorf("status = %q: it is reporting the LIVE PC while every panel shows the past", d.statusTxt.Text)
	}
	if d.statusTxt.Color == liveCol {
		t.Error("the status line is the same colour reversing as live; the change of machine must be visible, not only readable")
	}
	if !strings.Contains(d.revBanner.Text, "PAST") {
		t.Errorf("banner = %q, want an unmissable statement that this is recorded state", d.revBanner.Text)
	}
	if !strings.Contains(strings.ToLower(d.revBanner.Text), "memory") {
		t.Errorf("banner = %q, want it to name memory as the thing the recording does not hold", d.revBanner.Text)
	}
	if !strings.Contains(d.regHeader.Text, "PAST") {
		t.Errorf("registers header = %q, want it to mark the panel as historical for a user who scrolled past the banner",
			d.regHeader.Text)
	}
}

// Leaving reverse mode has to take every one of those marks off again.
func TestReturningToThePresentClearsTheHistoricalChrome(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	d.ExitReverse()

	if d.revBanner.Text != "" {
		t.Errorf("banner = %q, want it cleared once the live machine is back on screen", d.revBanner.Text)
	}
	if strings.Contains(d.regHeader.Text, "PAST") {
		t.Errorf("registers header = %q, want the historical mark removed", d.regHeader.Text)
	}
	if strings.Contains(strings.ToLower(d.hexHeader.Text), "not recorded") {
		t.Errorf("memory header = %q, want the memory panel back to normal", d.hexHeader.Text)
	}
	d.mem.Write(0x4000, 0xBE)
	got, _ := d.hexRowText(0x4000 / 16)
	if !strings.Contains(got, "BE") {
		t.Errorf("hex row = %q, want live memory back", got)
	}
}

// The stack view is what makes a backwards step through a CALL legible: the
// return address is on the stack, not in a register. The ring records a window
// at SP for exactly that reason, and the panel must show it rather than walk
// the stack as it stands now.
func TestBacktraceShowsTheRecordedStackWindowWhileReversing(t *testing.T) {
	d, cpu := newReverseDebugger(t, wideRing(4))
	cpu.SP = 0x0200

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	got := d.backtrace.out.Text

	// Newest entry i=3: SP $FF03, stack words $C300..$C307.
	if !strings.Contains(got, "FF03") {
		t.Errorf("backtrace = %q, want the recorded SP $FF03", got)
	}
	if !strings.Contains(got, "C300") || !strings.Contains(got, "C307") {
		t.Errorf("backtrace = %q, want the recorded stack window $C300..$C307", got)
	}
	if strings.Contains(got, "0200") {
		t.Errorf("backtrace = %q: it walked the LIVE stack at SP $0200 instead of the recorded window", got)
	}
	// Classifying a frame as "from CALL nn" means reading the bytes before
	// the return address, and those bytes are memory the recording does not
	// hold. The panel must say so rather than classify from current memory.
	if !strings.Contains(strings.ToLower(got), "not recorded") {
		t.Errorf("backtrace = %q, want it to say frame origins cannot be classified without recorded memory", got)
	}
}

// A narrow ring recorded no stack window at all, so the panel has nothing to
// show and must say that instead of printing eight zeros.
func TestBacktraceReportsThatANarrowRingRecordedNoStackWindow(t *testing.T) {
	d, _ := newReverseDebugger(t, narrowRing(4))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	got := d.backtrace.out.Text

	if !strings.Contains(got, unavailable) || !strings.Contains(strings.ToLower(got), "stack window") {
		t.Errorf("backtrace = %q, want %q and a statement that no stack window was recorded", got, unavailable)
	}
	if strings.Contains(got, "0000") {
		t.Errorf("backtrace = %q: printing the unrecorded window's zeros claims the stack held them", got)
	}
}

func TestBacktraceReturnsToTheLiveStackOnExit(t *testing.T) {
	d, cpu := newReverseDebugger(t, wideRing(4))
	cpu.SP = 0x0200

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	d.ExitReverse()

	got := d.backtrace.out.Text
	if !strings.Contains(got, "0200") {
		t.Errorf("backtrace = %q, want the live stack at SP $0200 back", got)
	}
	if strings.Contains(got, "C300") {
		t.Errorf("backtrace = %q: it is still showing the recorded window after returning to the present", got)
	}
}

// Every toolbar control that moves or edits the LIVE machine — step, run,
// edit registers, write memory — has to drop the cursor first. Otherwise the
// user single-steps the present while reading the past, and the panels answer
// a question about one instant with the state of another.
func TestActingOnTheLiveMachineLeavesReverseModeFirst(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseStepBack(); err != nil {
		t.Fatalf("ReverseStepBack: %v", err)
	}

	d.leaveReverseForLiveAction()

	if d.InReverse() {
		t.Error("a control that moves the live machine must return the view to the present first, " +
			"or the panels describe an instant the user is no longer acting on")
	}
	if d.revBanner.Text != "" {
		t.Errorf("banner = %q, want it cleared once the view is live again", d.revBanner.Text)
	}
}

// The cursor is a distance back from the NEWEST recorded instruction, and a
// running machine writes a new newest entry a million times a second. Left
// running, "three instructions back" would name a different instruction on
// every refresh and the panels would flicker through unrelated state. Entering
// reverse therefore stops the machine first.
func TestEnteringReverseModeStopsTheMachineFirst(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	pauses := 0
	d.onPause = func() { pauses++ }

	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if pauses == 0 {
		t.Error("the ring is still being written while the machine runs, so the cursor's target " +
			"moves under the user; entering reverse must pause first")
	}
}

// Nothing recorded means nothing to show, and stopping the machine for a mode
// that cannot be entered would be a surprise with no benefit.
func TestARefusedEnterReverseDoesNotStopTheMachine(t *testing.T) {
	d, _ := newReverseDebugger(t, nil)
	pauses := 0
	d.onPause = func() { pauses++ }

	if err := d.EnterReverse(); err == nil {
		t.Fatal("EnterReverse with no ring must be refused")
	}
	if pauses != 0 {
		t.Error("a refused EnterReverse must leave the machine running")
	}
}

func TestRunBackRejectsAConditionItCannotParse(t *testing.T) {
	d, _ := newReverseDebugger(t, wideRing(8))
	if err := d.EnterReverse(); err != nil {
		t.Fatalf("EnterReverse: %v", err)
	}
	if err := d.ReverseRunBack("not a condition"); err == nil {
		t.Error("an unparseable condition must be reported, not treated as one that matches everything")
	}
}
