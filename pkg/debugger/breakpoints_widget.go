package debugger

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// BreakpointsStore is the interface the BreakpointsWidget talks
// to. Lets the parent Debugger plug in its own store
// implementation (whatever sync / atomic discipline it prefers)
// while keeping the widget free of locking concerns.
type BreakpointsStore interface {
	List() map[uint16]BPEntry
	Add(pc uint16, e BPEntry)
	Remove(pc uint16)
	Clear()
}

// BreakpointsWidget renders the list of active breakpoints and
// hosts a small "Add" form supporting bank filter + condition.
// Matches the surface of the telnet `set-breakpoint` /
// `clear-breakpoint` / `list-breakpoints` commands.
type BreakpointsWidget struct {
	store BreakpointsStore

	pcE      *widget.Entry
	bankE    *widget.Entry
	condE    *widget.Entry
	addBtn   *widget.Button
	clearBtn *widget.Button

	list   *widget.List
	keys   []uint16
	snap   map[uint16]BPEntry
	status *canvas.Text
	root   *fyne.Container
}

// NewBreakpointsWidget builds the panel. store may not be nil —
// pass an in-memory implementation if no shared store is needed.
func NewBreakpointsWidget(store BreakpointsStore) *BreakpointsWidget {
	w := &BreakpointsWidget{store: store}

	w.pcE = widget.NewEntry()
	w.pcE.SetPlaceHolder("$6010")
	w.pcE.TextStyle = fyne.TextStyle{Monospace: true}

	w.bankE = widget.NewEntry()
	w.bankE.SetPlaceHolder("-1")
	w.bankE.TextStyle = fyne.TextStyle{Monospace: true}

	w.condE = widget.NewEntry()
	w.condE.SetPlaceHolder("iff1 == 0  (optional)")
	w.condE.TextStyle = fyne.TextStyle{Monospace: true}

	w.addBtn = widget.NewButton("Add", w.onAdd)
	w.clearBtn = widget.NewButton("Clear all", w.onClearAll)

	w.list = widget.NewList(
		func() int { return len(w.keys) },
		func() fyne.CanvasObject {
			t := canvas.NewText(strings.Repeat(" ", 80), color.NRGBA{R: 220, G: 220, B: 220, A: 255})
			t.TextStyle = fyne.TextStyle{Monospace: true}
			return t
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			t.Text = w.rowText(id)
			t.Refresh()
		},
	)
	w.list.OnSelected = func(id widget.ListItemID) {
		if id >= len(w.keys) {
			return
		}
		pc := w.keys[id]
		w.store.Remove(pc)
		w.setStatus(fmt.Sprintf("removed $%04X", pc), false)
		w.list.UnselectAll()
		w.Refresh()
	}

	w.status = canvas.NewText("ready", color.NRGBA{R: 180, G: 180, B: 200, A: 255})
	w.status.TextStyle = fyne.TextStyle{Monospace: true}
	w.status.TextSize = 11

	pcSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(90, 36)), w.pcE)
	bankSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(60, 36)), w.bankE)
	condSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(260, 36)), w.condE)

	form := container.NewHBox(
		widget.NewLabel("PC"), pcSized,
		widget.NewLabel("Bank"), bankSized,
		widget.NewLabel("If"), condSized,
		w.addBtn, w.clearBtn,
	)
	w.root = container.NewBorder(
		container.NewVBox(form, w.status, widget.NewSeparator()),
		nil, nil, nil, w.list,
	)
	w.refreshKeys()
	return w
}

// Root returns the embeddable canvas object.
func (w *BreakpointsWidget) Root() fyne.CanvasObject { return w.root }

// Refresh re-pulls the store and triggers a list redraw. Cheap
// enough to call from the debugger's periodic tick.
func (w *BreakpointsWidget) Refresh() {
	w.refreshKeys()
	w.list.Refresh()
}

func (w *BreakpointsWidget) refreshKeys() {
	w.snap = w.store.List()
	w.keys = SortedKeys(w.snap)
}

// rowText returns the formatted line for row id, or empty if id is
// out of range. Reads from the snapshot refreshKeys() cached rather
// than re-querying the store, so rendering N visible rows doesn't
// re-copy the whole breakpoint map N times.
func (w *BreakpointsWidget) rowText(id widget.ListItemID) string {
	if id < 0 || id >= len(w.keys) {
		return ""
	}
	pc := w.keys[id]
	return w.snap[pc].FormatLine(pc)
}

func (w *BreakpointsWidget) onAdd() {
	pc, err := parseUserHex(w.pcE.Text)
	if err != nil {
		w.setStatus("bad pc: "+err.Error(), true)
		return
	}
	if pc < 0 || pc > 0xFFFF {
		w.setStatus("pc out of range", true)
		return
	}
	bank := -1
	if s := strings.TrimSpace(w.bankE.Text); s != "" && s != "-1" {
		v, err := parseUserHex(s)
		if err != nil {
			w.setStatus("bad bank: "+err.Error(), true)
			return
		}
		bank = v
	}
	entry := BPEntry{Bank: bank}
	if s := strings.TrimSpace(w.condE.Text); s != "" {
		c, err := ParseCondition(s)
		if err != nil {
			w.setStatus("bad cond: "+err.Error(), true)
			return
		}
		entry.HasCond = true
		entry.Cond = c
		entry.Source = s
	}
	w.store.Add(uint16(pc), entry)
	w.setStatus(fmt.Sprintf("added %s", entry.FormatLine(uint16(pc))), false)
	w.Refresh()
}

func (w *BreakpointsWidget) onClearAll() {
	w.store.Clear()
	w.setStatus("all breakpoints cleared", false)
	w.Refresh()
}

func (w *BreakpointsWidget) setStatus(s string, isErr bool) {
	w.status.Text = s
	if isErr {
		w.status.Color = color.NRGBA{R: 220, G: 80, B: 80, A: 255}
	} else {
		w.status.Color = color.NRGBA{R: 180, G: 200, B: 180, A: 255}
	}
	w.status.Refresh()
}
