package debugger

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// HardwareProvider is an OPTIONAL companion to NextProvider that feeds the
// Next hardware tabs with text views of the machine's blocks. view is one of
// the HardwareView* names; nil means the block is not present.
type HardwareProvider interface {
	HardwareView(view string) []string
}

// The hardware views the GUI asks for.
const (
	HardwareViewNextRegs   = "nextregs"
	HardwareViewInterrupts = "interrupts"
	HardwareViewDMA        = "dma"
	HardwareViewSerial     = "serial"
	HardwareViewCopper     = "copper"
)

// TextView is a scrollable monospace text panel filled from a lines func. It
// repaints only when the text changes.
type TextView struct {
	label  *widget.Label
	lines  func() []string
	last   string
	root   fyne.CanvasObject
	absent string
}

// NewTextView builds the panel; absent is shown when lines is nil or returns
// nil.
func NewTextView(absent string) *TextView {
	v := &TextView{absent: absent}
	v.label = widget.NewLabel(absent)
	v.label.TextStyle = fyne.TextStyle{Monospace: true}
	v.label.Wrapping = fyne.TextWrapOff
	v.root = container.NewScroll(v.label)
	return v
}

// SetLines swaps the source and repaints.
func (v *TextView) SetLines(lines func() []string) {
	v.lines = lines
	v.Refresh()
}

// Root returns the panel's top-level object.
func (v *TextView) Root() fyne.CanvasObject { return v.root }

// Text returns what the panel shows.
func (v *TextView) Text() string { return v.label.Text }

// Refresh repaints from the source if the text changed.
func (v *TextView) Refresh() {
	text := v.absent
	if v.lines != nil {
		if l := v.lines(); l != nil {
			text = strings.Join(l, "\n")
		}
	}
	if text == v.last {
		return
	}
	v.last = text
	v.label.SetText(text)
}
