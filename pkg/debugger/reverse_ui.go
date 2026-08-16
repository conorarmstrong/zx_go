package debugger

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2/canvas"
)

// Reverse debugging in the GUI: the controller the toolbar drives, and the
// rendering of a recorded instant into the panels that normally read the live
// CPU.
//
// The cursor is the whole state. d.rev is non-nil exactly while the debugger is
// showing the past, so every panel's question — "am I looking at history?" — is
// one nil check, and there is no second flag to fall out of step with it.
//
// All of this runs on the Fyne UI goroutine: the buttons call it directly, and
// the periodic refresh reaches it through fyne.Do. It carries no lock of its
// own for the same reason the other panel fields do not.

var (
	// colPast marks values read out of the recording rather than the live
	// machine. A different colour from every live panel on purpose: reading a
	// historical register dump as the machine's current state is the one way
	// this feature does real harm.
	colPast = color.NRGBA{R: 255, G: 150, B: 80, A: 255}
	// colUnavail marks a value the recording does not hold. Deliberately dim:
	// it is the absence of information, not information.
	colUnavail = color.NRGBA{R: 130, G: 130, B: 145, A: 255}
)

// unavailable is what every panel prints where the recording holds nothing.
// Never a zero: a zero is a value, and a value that was never recorded is a
// claim about the machine that nobody made.
const unavailable = "—"

// EnterReverse points the debugger at the newest recorded instruction and
// switches every panel over to reading the recording.
//
// The ring is the shared one the History tab and the telnet `history` command
// read; reverse mode deliberately owns no buffer of its own, so what the user
// can step back through is exactly what those surfaces list.
func (d *Debugger) EnterReverse() error {
	if d.histRing == nil {
		return ErrNoHistory
	}
	c := NewReverseCursor(d.histRing)
	if !c.Active() {
		return ErrNoHistory
	}
	// Stop the machine before taking the cursor. The cursor's position is a
	// distance back from the NEWEST recorded instruction, and a running
	// machine writes a new newest entry roughly a million times a second, so
	// "three instructions back" would name a different instruction on every
	// refresh. Pausing is done here, after the ring is known to hold
	// something, so a refused entry does not stop the machine for nothing.
	if d.onPause != nil {
		d.onPause()
	}
	d.rev = c
	d.applyReverseView()
	return nil
}

// ExitReverse returns every panel to the live machine.
func (d *Debugger) ExitReverse() {
	d.rev = nil
	d.applyReverseView()
}

// InReverse reports whether the panels are showing a recorded instant.
func (d *Debugger) InReverse() bool { return d.rev != nil }

// ReverseAvailable reports whether there is a recording to walk at all, so the
// control can explain itself before it is pressed.
func (d *Debugger) ReverseAvailable() bool {
	return d.histRing != nil && d.histRing.Len() > 0
}

// ReverseEntry is the instant under the cursor, if reverse mode is active.
func (d *Debugger) ReverseEntry() (HistoryEntry, bool) {
	if d.rev == nil {
		return HistoryEntry{}, false
	}
	return d.rev.Current()
}

// ReverseStepBack moves the cursor one instruction into the past.
func (d *Debugger) ReverseStepBack() error {
	if d.rev == nil {
		if err := d.EnterReverse(); err != nil {
			return err
		}
	}
	if err := d.rev.StepBack(); err != nil {
		return err
	}
	d.applyReverseView()
	return nil
}

// ReverseStepForward moves the cursor one instruction towards the present, and
// stepping off the newest recorded instruction leaves reverse mode.
//
// Leaving is not an error. The cursor's own StepForward reports ErrAtPresent
// because it is a library that must not guess what the caller wants; here the
// caller is the UI, and what the user asking to move forward off the end wants
// is the live machine back.
func (d *Debugger) ReverseStepForward() error {
	if d.rev == nil {
		return ErrAtPresent
	}
	if d.rev.AtPresent() {
		d.ExitReverse()
		return nil
	}
	if err := d.rev.StepForward(); err != nil {
		return err
	}
	d.applyReverseView()
	return nil
}

// ReverseRunBack searches backwards for the most recent earlier instant
// matching expr — the same condition syntax as breakpoints and watchpoints.
//
// A condition reaching for memory, or for a register this ring never recorded,
// is refused by the cursor rather than answered from a zero. That refusal is
// passed straight through to the user.
func (d *Debugger) ReverseRunBack(expr string) error {
	if d.rev == nil {
		if err := d.EnterReverse(); err != nil {
			return err
		}
	}
	cond, err := ParseCondition(expr)
	if err != nil {
		return err
	}
	if err := d.rev.RunBack(cond); err != nil {
		return err
	}
	d.applyReverseView()
	return nil
}

// ReverseStatus is a one-line description of where the cursor is, for the
// control panel to show.
func (d *Debugger) ReverseStatus() string {
	if d.rev == nil {
		if !d.ReverseAvailable() {
			return "live machine — no recording (start with --debugger-history > 0)"
		}
		return fmt.Sprintf("live machine — %d instructions recorded", d.histRing.Len())
	}
	e, ok := d.rev.Current()
	if !ok {
		return "live machine"
	}
	return fmt.Sprintf("PAST: %d instruction(s) back — insn %d, PC $%04X (of %d recorded)",
		d.rev.Distance(), e.Insns, e.PC, d.histRing.Len())
}

// leaveReverseForLiveAction returns the view to the present before a control
// that moves or edits the live machine runs.
//
// Stepping the CPU while the panels show a recorded instant would leave the
// user acting on one machine and reading another. Rather than disable those
// controls, pressing one is taken to mean "I want the live machine", which is
// what it means everywhere else in the debugger.
func (d *Debugger) leaveReverseForLiveAction() {
	if d.rev == nil {
		return
	}
	d.ExitReverse()
}

// applyReverseView pushes a cursor move out to every panel that renders it.
//
// The stack view is driven from here rather than from Refresh because it is
// rendered on demand (its own Refresh button), not on the periodic tick, and
// re-walking a 64-deep stack every 50ms to keep it in step would be paying a
// lot for a panel most sessions never open.
func (d *Debugger) applyReverseView() {
	if d.revW != nil {
		d.revW.Refresh()
	}
	d.refreshBacktrace()
	d.Refresh()
}

// reportReverse surfaces the result of a toolbar reverse control.
//
// The message goes to the Reverse panel's status line rather than the window's
// status bar, which the 50ms refresh would overwrite before it could be read.
// That does mean a refusal lands on a tab the user may not have open, so the
// toolbar carries only the two actions whose failures are self-evident on
// screen (the cursor visibly does not move).
func (d *Debugger) reportReverse(err error) {
	if d.revW == nil {
		return
	}
	if err != nil {
		d.revW.setStatus(err.Error(), true)
		return
	}
	d.revW.Refresh()
}

// refreshBacktrace re-renders the stack view against whichever machine the
// panels are currently showing.
func (d *Debugger) refreshBacktrace() {
	if d.backtrace == nil {
		return
	}
	if e, ok := d.ReverseEntry(); ok {
		d.backtrace.SetHistorical(&e, d.histRing.Wide())
	} else {
		d.backtrace.SetHistorical(nil, false)
	}
	d.backtrace.Render()
}

// displayPC is the PC every panel highlights: the recorded one while the
// cursor is in the past, the live one otherwise.
func (d *Debugger) displayPC() uint16 {
	if e, ok := d.ReverseEntry(); ok {
		return e.PC
	}
	return d.cpu.PC
}

// Panel header labels. The live ones are what buildUI installs; the reverse
// ones replace them for as long as the cursor is in the past, so a user who has
// scrolled the banner off screen can still tell from the panel itself.
const (
	hdrRegistersLive = "  REGISTERS"
	hdrRegistersPast = "  REGISTERS — RECORDED PAST"
	hdrDasmLive      = "  DISASSEMBLY  (tap to toggle breakpoint)"
	hdrDasmPast      = "  DISASSEMBLY at the recorded PC — bytes read from CURRENT memory"
	hdrMemoryLive    = "  MEMORY"
	hdrMemoryPast    = "  MEMORY — not recorded per instruction; unavailable in reverse"
)

// refreshReverseChrome relabels the panels and raises or clears the banner.
//
// Run on every refresh rather than only on a cursor move: a label that is set
// once can be left saying "past" after the view has gone back to live, and a
// stale label on this particular feature is the failure mode the whole design
// is arranged around.
func (d *Debugger) refreshReverseChrome() {
	e, past := d.ReverseEntry()

	setHeader := func(t *canvas.Text, live, hist string, liveCol color.Color) {
		if t == nil {
			return
		}
		if past {
			t.Text = hist
			t.Color = colPast
		} else {
			t.Text = live
			t.Color = liveCol
		}
		t.Refresh()
	}
	setHeader(d.regHeader, hdrRegistersLive, hdrRegistersPast, colRegName)
	setHeader(d.dasmHeader, hdrDasmLive, hdrDasmPast, colMnem)
	setHeader(d.hexHeader, hdrMemoryLive, hdrMemoryPast, colAddr)

	if d.revBanner == nil {
		return
	}
	if !past {
		d.revBanner.Text = ""
	} else {
		d.revBanner.Text = fmt.Sprintf(
			"  << SHOWING THE RECORDED PAST — %d instruction(s) back (insn %d, PC $%04X).  "+
				"Registers and flags are as they WERE.  Memory is not recorded and is not shown.  "+
				"The machine has not moved.",
			d.rev.Distance(), e.Insns, e.PC)
	}
	d.revBanner.Color = colPast
	d.revBanner.Refresh()
}

// refreshReverseStatus renders the status line for a recorded instant.
func (d *Debugger) refreshReverseStatus(e HistoryEntry) {
	wide := d.histRing != nil && d.histRing.Wide()
	d.statusTxt.Text = fmt.Sprintf(
		" PAST -%d  insn %d  PC:%04X SP:%04X AF:%02X%02X BC:%s DE:%s HL:%s IM:%d IFF:%v  (memory: %s)",
		d.rev.Distance(), e.Insns, e.PC, e.SP, e.A, e.F,
		hist16(e.BC, wide), hist16(e.DE, wide), hist16(e.HL, wide),
		e.IM(), e.IFF1(), unavailable)
	d.statusTxt.Color = colPast
	d.statusTxt.Refresh()
}

// hist8 renders a recorded byte, or the unavailable marker padded to the same
// width so the columns still line up beside the values that were recorded.
func hist8(v byte, have bool) string {
	if !have {
		return " " + unavailable
	}
	return fmt.Sprintf("%02X", v)
}

// hist16 is hist8 for a 16-bit register.
func hist16(v uint16, have bool) string {
	if !have {
		return "   " + unavailable
	}
	return fmt.Sprintf("%04X", v)
}

// pastCol picks the colour for a line: recorded values read as the past,
// unrecorded ones read as absence.
func pastCol(have bool) color.Color {
	if have {
		return colPast
	}
	return colUnavail
}

// refreshReverseRegisters renders a recorded instant into the register panel.
//
// It writes every line the live renderer writes, because a line left alone
// would keep showing the live machine's value next to the recorded ones, which
// is precisely the mix-up this feature has to avoid.
//
// Availability is decided by the ring's wide flag, never by the field's value.
// A narrow producer leaves BC at zero, and zero is also a perfectly ordinary
// value for BC to hold, so the two are indistinguishable once you are looking
// at the entry. Only the ring knows which fields it was filling.
func (d *Debugger) refreshReverseRegisters(e HistoryEntry) {
	set := func(i int, s string, c color.Color) {
		d.regLines[i].Text = s
		d.regLines[i].Color = c
		d.regLines[i].Refresh()
	}

	// A narrow ring recorded only PC, SP, A, F and the interrupt state.
	// The pair registers, the shadow set and the stack window come with
	// a wide ring or not at all.
	wide := d.histRing != nil && d.histRing.Wide()
	wideCol := pastCol(wide)

	set(0, fmt.Sprintf(" PC   %04X", e.PC), colPast)
	set(1, fmt.Sprintf(" SP   %04X", e.SP), colPast)
	set(2, " IX   "+hist16(e.IX, wide), wideCol)
	set(3, " IY   "+hist16(e.IY, wide), wideCol)
	set(4, "", colPast)
	set(5, fmt.Sprintf(" A %02X   F %02X", e.A, e.F), colPast)
	set(6, " B "+hist8(byte(e.BC>>8), wide)+"   C "+hist8(byte(e.BC), wide), wideCol)
	set(7, " D "+hist8(byte(e.DE>>8), wide)+"   E "+hist8(byte(e.DE), wide), wideCol)
	set(8, " H "+hist8(byte(e.HL>>8), wide)+"   L "+hist8(byte(e.HL), wide), wideCol)
	set(9, "", colPast)
	set(10, " A'"+hist8(byte(e.AltAF>>8), wide)+"   F'"+hist8(byte(e.AltAF), wide), wideCol)
	set(11, " B'"+hist8(byte(e.AltBC>>8), wide)+"   C'"+hist8(byte(e.AltBC), wide), wideCol)
	set(12, " D'"+hist8(byte(e.AltDE>>8), wide)+"   E'"+hist8(byte(e.AltDE), wide), wideCol)
	set(13, " H'"+hist8(byte(e.AltHL>>8), wide)+"   L'"+hist8(byte(e.AltHL), wide), wideCol)
	set(14, "", colPast)
	// I and R are in no HistoryEntry at all, wide or narrow.
	set(15, " I  "+hist8(0, false)+"   R  "+hist8(0, false), colUnavail)
	set(16, "", colPast)
	set(17, " Stack:", colPast)
	set(18, fmt.Sprintf("  %04X: %s  %s", e.SP, hist16(e.Stack[0], wide), hist16(e.Stack[1], wide)), wideCol)
	set(19, fmt.Sprintf("  %04X: %s  %s", e.SP+4, hist16(e.Stack[2], wide), hist16(e.Stack[3], wide)), wideCol)

	flagStr := " "
	for _, fl := range []struct {
		n string
		b byte
	}{{"S", 0x80}, {"Z", 0x40}, {"H", 0x10}, {"PV", 0x04}, {"N", 0x02}, {"C", 0x01}} {
		if e.F&fl.b != 0 {
			flagStr += fl.n + ":1 "
		} else {
			flagStr += fl.n + ":0 "
		}
	}
	d.flagLine.Text = flagStr
	d.flagLine.Color = colPast
	d.flagLine.Refresh()

	d.iffLine.Text = fmt.Sprintf(" IFF %v/%v  IM %d", e.IFF1(), e.IFF2(), e.IM())
	d.iffLine.Color = colPast
	d.iffLine.Refresh()

	d.irqLine.Text = " IRQ " + unavailable + " (not recorded)"
	d.irqLine.Color = colUnavail
	d.irqLine.Refresh()

	if e.Halted() {
		d.haltLine.Text = " ** HALTED **"
		d.haltLine.Color = colHalted
	} else {
		d.haltLine.Text = ""
	}
	d.haltLine.Refresh()
}
