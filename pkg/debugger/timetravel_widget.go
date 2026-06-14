package debugger

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// TTRow is one time-travel snapshot, flattened for display.
type TTRow struct {
	Insn  uint64
	PC    uint16
	Label string
}

// TimeTravelController is the backend the TimeTravelWidget drives.
// cmd/zx_go implements it over the emulator-owned snapshot ring, so
// the GUI tab and the telnet `tt-*` commands operate on the SAME
// buffer.
type TimeTravelController interface {
	Enabled() bool
	Enable(everyInsns, keep int)
	Disable()
	Snap(label string)
	Rewind(insn uint64) error
	Clear()
	Rows() []TTRow
}

// TimeTravelWidget renders the snapshot ring and offers enable/snap/
// rewind/clear — parity with telnet tt-on / tt-snap / tt-rewind /
// tt-clear / tt-status. Tap a row to rewind to it.
type TimeTravelWidget struct {
	ctl TimeTravelController

	enableBtn *widget.Button
	snapBtn   *widget.Button
	clearBtn  *widget.Button
	list      *widget.List
	rows      []TTRow
	status    *canvas.Text
	root      *fyne.Container
}

// NewTimeTravelWidget builds the panel. ctl may be nil; call
// SetController once the backend is available.
func NewTimeTravelWidget(ctl TimeTravelController) *TimeTravelWidget {
	w := &TimeTravelWidget{ctl: ctl}

	w.enableBtn = widget.NewButton("Enable", w.onToggleEnable)
	w.snapBtn = widget.NewButton("Snapshot now", w.onSnap)
	w.clearBtn = widget.NewButton("Clear", w.onClear)

	w.status = canvas.NewText("", color.NRGBA{R: 180, G: 180, B: 200, A: 255})
	w.status.TextStyle = fyne.TextStyle{Monospace: true}
	w.status.TextSize = 11

	w.list = widget.NewList(
		func() int { return len(w.rows) },
		func() fyne.CanvasObject {
			t := canvas.NewText(strings.Repeat(" ", 50), color.NRGBA{R: 220, G: 220, B: 220, A: 255})
			t.TextStyle = fyne.TextStyle{Monospace: true}
			return t
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			if id < len(w.rows) {
				r := w.rows[id]
				label := r.Label
				if label != "" {
					label = "  [" + label + "]"
				}
				t.Text = fmt.Sprintf("  insn %-12d  PC $%04X%s", r.Insn, r.PC, label)
			} else {
				t.Text = ""
			}
			t.Refresh()
		},
	)
	w.list.OnSelected = func(id widget.ListItemID) {
		if id >= len(w.rows) || w.ctl == nil {
			return
		}
		insn := w.rows[id].Insn
		if err := w.ctl.Rewind(insn); err != nil {
			w.setStatus("rewind failed: "+err.Error(), true)
		} else {
			w.setStatus(fmt.Sprintf("rewound to insn %d", insn), false)
		}
		w.list.UnselectAll()
		w.Refresh()
	}

	header := container.NewHBox(w.enableBtn, w.snapBtn, w.clearBtn)
	w.root = container.NewBorder(
		container.NewVBox(header, w.status, widget.NewSeparator()),
		nil, nil, nil, w.list,
	)
	w.Refresh()
	return w
}

// Root returns the embeddable canvas object.
func (w *TimeTravelWidget) Root() fyne.CanvasObject { return w.root }

// SetController wires the backend.
func (w *TimeTravelWidget) SetController(c TimeTravelController) {
	w.ctl = c
	w.Refresh()
}

// Refresh re-pulls the ring and updates labels.
func (w *TimeTravelWidget) Refresh() {
	if w.ctl == nil {
		w.rows = nil
		w.enableBtn.SetText("Enable")
		w.setStatus("time-travel unavailable", false)
		w.list.Refresh()
		return
	}
	if w.ctl.Enabled() {
		w.rows = w.ctl.Rows()
		w.enableBtn.SetText("Disable")
		w.setStatus(fmt.Sprintf("ON — %d snapshots (tap a row to rewind)", len(w.rows)), false)
	} else {
		w.rows = nil
		w.enableBtn.SetText("Enable")
		w.setStatus("OFF — click Enable to start capturing", false)
	}
	w.list.Refresh()
}

func (w *TimeTravelWidget) onToggleEnable() {
	if w.ctl == nil {
		return
	}
	if w.ctl.Enabled() {
		w.ctl.Disable()
	} else {
		// Sensible defaults mirroring tt-on: every 50k insns, keep 16.
		w.ctl.Enable(50000, 16)
	}
	w.Refresh()
}

func (w *TimeTravelWidget) onSnap() {
	if w.ctl == nil || !w.ctl.Enabled() {
		w.setStatus("enable time-travel first", true)
		return
	}
	w.ctl.Snap("manual")
	w.setStatus("snapshot captured", false)
	w.Refresh()
}

func (w *TimeTravelWidget) onClear() {
	if w.ctl != nil {
		w.ctl.Clear()
	}
	w.setStatus("ring cleared", false)
	w.Refresh()
}

func (w *TimeTravelWidget) setStatus(s string, isErr bool) {
	w.status.Text = "  " + s
	if isErr {
		w.status.Color = color.NRGBA{R: 220, G: 80, B: 80, A: 255}
	} else {
		w.status.Color = color.NRGBA{R: 180, G: 200, B: 180, A: 255}
	}
	w.status.Refresh()
}
