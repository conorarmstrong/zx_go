package debugger

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// StackSource is the minimal slice of CPU state the BacktraceWidget
// needs to render. Decoupled so the widget doesn't carry a
// pkg/z80 dependency.
type StackSource interface {
	SP() uint16
	IFF1() bool
	IM() int
	Halted() bool
}

// BacktraceWidget renders the result of debugger.Backtrace() in a
// scrollable text area. The depth is user-controllable via a small
// entry field; Refresh re-pulls the stack from the wired
// StackSource + read closure.
type BacktraceWidget struct {
	depthE  *widget.Entry
	refresh *widget.Button
	out     *widget.Entry
	root    *fyne.Container

	source StackSource
	read   readFunc
}

// NewBacktraceWidget builds the panel. source/read may be nil at
// construction time and supplied later via SetSource. Render
// reports "no source" until both are non-nil.
func NewBacktraceWidget(source StackSource, read readFunc) *BacktraceWidget {
	w := &BacktraceWidget{source: source, read: read}
	w.depthE = widget.NewEntry()
	w.depthE.SetText("8")
	w.depthE.TextStyle = fyne.TextStyle{Monospace: true}

	w.refresh = widget.NewButton("Refresh", w.Render)

	w.out = widget.NewMultiLineEntry()
	w.out.TextStyle = fyne.TextStyle{Monospace: true}
	w.out.Wrapping = fyne.TextWrapOff
	w.out.SetText("(paused-only — press Refresh)")

	top := container.NewHBox(widget.NewLabel("Depth"), w.depthE, w.refresh)
	w.root = container.NewBorder(top, nil, nil, nil, container.NewVScroll(w.out))
	return w
}

// SetSource wires a fresh CPU-state source and memory-read closure.
// Either may be nil to disconnect (Render then reports "no source").
func (w *BacktraceWidget) SetSource(s StackSource, r readFunc) {
	w.source = s
	w.read = r
}

// Root returns the embeddable canvas object.
func (w *BacktraceWidget) Root() fyne.CanvasObject { return w.root }

// Render fetches the current stack and renders it into the output.
// Designed to be called from the debugger's periodic refresh as well
// as on demand via the Refresh button.
func (w *BacktraceWidget) Render() {
	if w.source == nil || w.read == nil {
		w.out.SetText("no source — open while paused")
		return
	}
	depth := 8
	if v, err := parseUserHex(w.depthE.Text); err == nil && v > 0 {
		depth = v
	}
	if depth > 64 {
		depth = 64
	}
	src := w.source
	frames := Backtrace(w.read, src.SP(), depth, 3)
	var sb strings.Builder
	fmt.Fprintf(&sb, "SP=$%04X  IFF1=%v  IM=%d  Halted=%v\n",
		src.SP(), src.IFF1(), src.IM(), src.Halted())
	for _, f := range frames {
		hint := f.Hint()
		if hint != "" {
			hint = "  " + hint
		}
		fmt.Fprintf(&sb, "  $%04X: $%04X   %s%s\n",
			f.StackAddr, f.ReturnAddr, f.Origin.String(), hint)
		for _, l := range f.Disasm {
			fmt.Fprintf(&sb, "          %s\n", l.String())
		}
	}
	w.out.SetText(sb.String())
}
