package debugger

import (
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// WatchpointsWidget renders the active register watchpoints and a
// small Add form (reg + optional from/to), mirroring the telnet
// `watch-reg` / `list-watches` / `clear-watch` commands. It talks to
// the SHARED *RegWatchSet, so a watch added here is visible and
// firing in the telnet debugger and vice versa.
type WatchpointsWidget struct {
	store *RegWatchSet

	regE     *widget.Entry
	fromE    *widget.Entry
	toE      *widget.Entry
	addBtn   *widget.Button
	clearBtn *widget.Button

	list   *widget.List
	rows   []RegWatch
	status *canvas.Text
	root   *fyne.Container
}

// NewWatchpointsWidget builds the panel. store may be nil; call
// SetStore once the shared set is available.
func NewWatchpointsWidget(store *RegWatchSet) *WatchpointsWidget {
	w := &WatchpointsWidget{store: store}

	w.regE = widget.NewEntry()
	w.regE.SetPlaceHolder("a / hl / iff1 / im …")
	w.regE.TextStyle = fyne.TextStyle{Monospace: true}

	w.fromE = widget.NewEntry()
	w.fromE.SetPlaceHolder("from $XX (opt)")
	w.fromE.TextStyle = fyne.TextStyle{Monospace: true}

	w.toE = widget.NewEntry()
	w.toE.SetPlaceHolder("to $XX (opt)")
	w.toE.TextStyle = fyne.TextStyle{Monospace: true}

	w.addBtn = widget.NewButton("Watch", w.onAdd)
	w.clearBtn = widget.NewButton("Clear all", w.onClearAll)

	w.list = widget.NewList(
		func() int { return len(w.rows) },
		func() fyne.CanvasObject {
			t := canvas.NewText(strings.Repeat(" ", 60), color.NRGBA{R: 220, G: 220, B: 220, A: 255})
			t.TextStyle = fyne.TextStyle{Monospace: true}
			return t
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			if id >= len(w.rows) {
				t.Text = ""
				t.Refresh()
				return
			}
			t.Text = w.rows[id].FormatLine()
			t.Refresh()
		},
	)
	w.list.OnSelected = func(id widget.ListItemID) {
		if id >= len(w.rows) || w.store == nil {
			return
		}
		reg := w.rows[id].Reg
		w.store.Remove(reg)
		w.setStatus("removed "+reg, false)
		w.list.UnselectAll()
		w.Refresh()
	}

	w.status = canvas.NewText("ready", color.NRGBA{R: 180, G: 180, B: 200, A: 255})
	w.status.TextStyle = fyne.TextStyle{Monospace: true}
	w.status.TextSize = 11

	regSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(90, 36)), w.regE)
	fromSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(110, 36)), w.fromE)
	toSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(110, 36)), w.toE)

	form := container.NewHBox(
		widget.NewLabel("Reg"), regSized,
		fromSized, toSized,
		w.addBtn, w.clearBtn,
	)
	w.root = container.NewBorder(
		container.NewVBox(form, w.status, widget.NewSeparator()),
		nil, nil, nil, w.list,
	)
	w.refreshRows()
	return w
}

// Root returns the embeddable canvas object.
func (w *WatchpointsWidget) Root() fyne.CanvasObject { return w.root }

// SetStore swaps in the shared register-watch set (used when the
// emulator's set arrives after construction).
func (w *WatchpointsWidget) SetStore(s *RegWatchSet) {
	w.store = s
	w.Refresh()
}

// Refresh re-pulls the set and redraws.
func (w *WatchpointsWidget) Refresh() {
	w.refreshRows()
	w.list.Refresh()
}

func (w *WatchpointsWidget) refreshRows() {
	if w.store == nil {
		w.rows = nil
		return
	}
	w.rows = w.store.List()
}

func (w *WatchpointsWidget) onAdd() {
	if w.store == nil {
		w.setStatus("no watch set wired", true)
		return
	}
	reg := strings.ToLower(strings.TrimSpace(w.regE.Text))
	if reg == "" {
		w.setStatus("enter a register name", true)
		return
	}
	wp := RegWatch{Reg: reg}
	if s := strings.TrimSpace(w.fromE.Text); s != "" {
		v, err := parseWatchVal(s)
		if err != nil {
			w.setStatus("bad from: "+err.Error(), true)
			return
		}
		wp.HasFrom = true
		wp.From = v
	}
	if s := strings.TrimSpace(w.toE.Text); s != "" {
		v, err := parseWatchVal(s)
		if err != nil {
			w.setStatus("bad to: "+err.Error(), true)
			return
		}
		wp.HasTo = true
		wp.To = v
	}
	w.store.Add(wp)
	w.setStatus("watching "+wp.FormatLine(), false)
	w.Refresh()
}

func (w *WatchpointsWidget) onClearAll() {
	if w.store != nil {
		w.store.Clear()
	}
	w.setStatus("all watches cleared", false)
	w.Refresh()
}

func (w *WatchpointsWidget) setStatus(s string, isErr bool) {
	w.status.Text = s
	if isErr {
		w.status.Color = color.NRGBA{R: 220, G: 80, B: 80, A: 255}
	} else {
		w.status.Color = color.NRGBA{R: 180, G: 200, B: 180, A: 255}
	}
	w.status.Refresh()
}

// parseWatchVal accepts $hex, 0x-hex, bare hex, or decimal.
func parseWatchVal(s string) (int64, error) {
	s = strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	var v int64
	var err error
	switch {
	case strings.HasPrefix(s, "$"):
		var u uint64
		u, err = strconv.ParseUint(s[1:], 16, 64)
		v = int64(u)
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		var u uint64
		u, err = strconv.ParseUint(s[2:], 16, 64)
		v = int64(u)
	default:
		v, err = strconv.ParseInt(s, 10, 64)
	}
	if neg {
		v = -v
	}
	return v, err
}
