package debugger

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// ReverseController is the backend the ReverseWidget drives. The Debugger
// implements it over the shared M1 history ring; the interface exists so the
// panel can be exercised without a window.
type ReverseController interface {
	// ReverseAvailable reports whether anything has been recorded to walk.
	ReverseAvailable() bool
	// InReverse reports whether the panels are showing a recorded instant.
	InReverse() bool
	// EnterReverse moves the view to the newest recorded instruction.
	EnterReverse() error
	// ExitReverse returns the view to the live machine.
	ExitReverse()
	// ReverseStepBack moves one instruction into the past.
	ReverseStepBack() error
	// ReverseStepForward moves one instruction towards the present, and
	// stepping off the newest recorded instruction returns to the live
	// machine.
	ReverseStepForward() error
	// ReverseRunBack searches backwards for the most recent earlier instant
	// matching a breakpoint-syntax condition.
	ReverseRunBack(expr string) error
	// ReverseStatus describes where the cursor is, for display.
	ReverseStatus() string
}

// ReverseWidget is the reverse-debugging control panel: enter and leave
// reverse mode, step the cursor either way, and run backwards to a condition.
//
// It holds no state of its own beyond the typed condition. Every question it
// can be asked — where is the cursor, is there anything recorded, did that
// step work — is answered by the controller, so the panel cannot drift out of
// step with what the other panels are rendering.
type ReverseWidget struct {
	ctl ReverseController

	enterBtn   *widget.Button
	backBtn    *widget.Button
	fwdBtn     *widget.Button
	runBackBtn *widget.Button
	condE      *widget.Entry
	status     *canvas.Text
	note       *canvas.Text
	root       *fyne.Container
}

// NewReverseWidget builds the panel. ctl may be nil; call SetController once
// the backend exists.
func NewReverseWidget(ctl ReverseController) *ReverseWidget {
	w := &ReverseWidget{ctl: ctl}

	w.status = canvas.NewText("", color.NRGBA{R: 180, G: 180, B: 200, A: 255})
	w.status.TextStyle = fyne.TextStyle{Monospace: true}
	w.status.TextSize = 11

	// The standing caveat, always on screen rather than only after a
	// refusal: what reverse mode can and cannot show is a property of the
	// recording, not of the last thing the user pressed.
	w.note = canvas.NewText(
		"  the recording holds registers, flags and a stack window — NOT memory. "+
			"Memory views and memory conditions are unavailable while reversing.",
		colUnavail)
	w.note.TextStyle = fyne.TextStyle{Monospace: true}
	w.note.TextSize = 11

	w.enterBtn = widget.NewButton("Enter reverse", w.onToggle)
	w.backBtn = widget.NewButton("< Step back", w.onBack)
	w.fwdBtn = widget.NewButton("Step forward >", w.onForward)
	w.runBackBtn = widget.NewButton("Run back to...", w.onRunBack)

	w.condE = widget.NewEntry()
	w.condE.SetPlaceHolder("condition, e.g. a == $42 (registers only)")
	w.condE.TextStyle = fyne.TextStyle{Monospace: true}
	w.condE.OnSubmitted = func(string) { w.onRunBack() }

	buttons := container.NewHBox(w.enterBtn, w.backBtn, w.fwdBtn)
	runRow := container.NewBorder(nil, nil, nil, w.runBackBtn, w.condE)
	w.root = container.NewVBox(
		buttons,
		runRow,
		w.status,
		w.note,
	)
	w.Refresh()
	return w
}

// Root returns the embeddable canvas object.
func (w *ReverseWidget) Root() fyne.CanvasObject { return w.root }

// SetController wires the backend.
func (w *ReverseWidget) SetController(c ReverseController) {
	w.ctl = c
	w.Refresh()
}

// Refresh re-reads the controller and relabels the controls.
func (w *ReverseWidget) Refresh() {
	if w.ctl == nil {
		w.enterBtn.SetText("Enter reverse")
		w.setStatus("reverse debugging unavailable", false)
		return
	}
	if w.ctl.InReverse() {
		w.enterBtn.SetText("Return to the present")
	} else {
		w.enterBtn.SetText("Enter reverse")
	}
	w.setStatus(w.ctl.ReverseStatus(), false)
}

func (w *ReverseWidget) onToggle() {
	if w.ctl == nil {
		return
	}
	if w.ctl.InReverse() {
		w.ctl.ExitReverse()
		w.Refresh()
		return
	}
	if err := w.ctl.EnterReverse(); err != nil {
		w.setStatus(err.Error(), true)
		return
	}
	w.Refresh()
}

func (w *ReverseWidget) onBack() {
	if w.ctl == nil {
		return
	}
	if err := w.ctl.ReverseStepBack(); err != nil {
		w.setStatus(err.Error(), true)
		return
	}
	w.Refresh()
}

func (w *ReverseWidget) onForward() {
	if w.ctl == nil {
		return
	}
	if err := w.ctl.ReverseStepForward(); err != nil {
		w.setStatus(err.Error(), true)
		return
	}
	w.Refresh()
}

func (w *ReverseWidget) onRunBack() {
	if w.ctl == nil {
		return
	}
	// An empty condition parses to one that matches everything, which would
	// land on the immediately preceding instruction and look like a search
	// that silently failed.
	if strings.TrimSpace(w.condE.Text) == "" {
		w.setStatus("type a condition to run back to, e.g. a == $42", true)
		return
	}
	if err := w.ctl.ReverseRunBack(w.condE.Text); err != nil {
		w.setStatus(err.Error(), true)
		return
	}
	w.Refresh()
}

func (w *ReverseWidget) setStatus(s string, isErr bool) {
	w.status.Text = "  " + s
	if isErr {
		w.status.Color = color.NRGBA{R: 220, G: 80, B: 80, A: 255}
	} else {
		w.status.Color = color.NRGBA{R: 180, G: 200, B: 180, A: 255}
	}
	w.status.Refresh()
}
