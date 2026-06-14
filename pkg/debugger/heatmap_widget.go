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

// HeatmapWidget renders the four M1-history analyses the telnet
// debugger exposes as `hot` / `callgraph` / `retgraph` / `rstgraph`,
// computed from the SAME shared *History ring. A mode selector picks
// which view; Refresh recomputes from the live ring.
type HeatmapWidget struct {
	hist *History

	mode   *widget.Select
	topN   int
	list   *widget.List
	rows   []string
	status *canvas.Text
	root   *fyne.Container
}

// NewHeatmapWidget builds the panel. hist may be nil; call
// SetHistory once the shared ring is available.
func NewHeatmapWidget(hist *History) *HeatmapWidget {
	w := &HeatmapWidget{hist: hist, topN: 20}

	// status + list MUST be built before the mode selector below:
	// SetSelectedIndex fires the select's onChange callback, which
	// calls Refresh() → setStatus()/list.Refresh(); doing it before
	// these fields exist nil-derefs (crash on opening the debugger).
	w.status = canvas.NewText("", color.NRGBA{R: 180, G: 180, B: 200, A: 255})
	w.status.TextStyle = fyne.TextStyle{Monospace: true}
	w.status.TextSize = 11

	w.list = widget.NewList(
		func() int { return len(w.rows) },
		func() fyne.CanvasObject {
			t := canvas.NewText(strings.Repeat(" ", 48), color.NRGBA{R: 220, G: 220, B: 220, A: 255})
			t.TextStyle = fyne.TextStyle{Monospace: true}
			return t
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			if id < len(w.rows) {
				t.Text = w.rows[id]
			} else {
				t.Text = ""
			}
			t.Refresh()
		},
	)

	// Now safe to build the selector (its SetSelectedIndex callback
	// touches status/list, which now exist).
	w.mode = widget.NewSelect(
		[]string{"Hot PCs", "Call graph", "Ret graph", "Rst graph"},
		func(string) { w.Refresh() },
	)
	w.mode.SetSelectedIndex(0)

	refreshBtn := widget.NewButton("Refresh", w.Refresh)
	header := container.NewHBox(widget.NewLabel("View"), w.mode, refreshBtn)
	w.root = container.NewBorder(
		container.NewVBox(header, w.status, widget.NewSeparator()),
		nil, nil, nil, w.list,
	)
	w.Refresh()
	return w
}

// Root returns the embeddable canvas object.
func (w *HeatmapWidget) Root() fyne.CanvasObject { return w.root }

// SetHistory wires the shared M1-history ring.
func (w *HeatmapWidget) SetHistory(h *History) {
	w.hist = h
	w.Refresh()
}

// Refresh recomputes the selected analysis from the live ring.
func (w *HeatmapWidget) Refresh() {
	if w.hist == nil {
		w.rows = []string{"  (history disabled — relaunch with --debugger-history=N)"}
		w.setStatus("no history ring")
		w.list.Refresh()
		return
	}
	entries := w.hist.Last(w.hist.Len())
	w.rows = w.rows[:0]
	switch w.mode.SelectedIndex() {
	case 0: // Hot PCs
		for _, h := range PCHistogram(entries, w.topN) {
			w.rows = append(w.rows, fmt.Sprintf("  $%04X   ×%d", h.PC, h.Count))
		}
	case 1:
		w.appendEdges(CallGraph(entries, SourceCall, w.topN))
	case 2:
		w.appendEdges(CallGraph(entries, SourceRet, w.topN))
	case 3:
		w.appendEdges(CallGraph(entries, SourceRst, w.topN))
	}
	if len(w.rows) == 0 {
		w.rows = append(w.rows, "  (no samples yet — run the CPU)")
	}
	w.setStatus(fmt.Sprintf("%d ring entries", len(entries)))
	w.list.Refresh()
}

func (w *HeatmapWidget) appendEdges(edges []CallEdge) {
	for _, e := range edges {
		w.rows = append(w.rows, fmt.Sprintf("  $%04X → $%04X   ×%d", e.From, e.To, e.Count))
	}
}

func (w *HeatmapWidget) setStatus(s string) {
	w.status.Text = "  " + s
	w.status.Refresh()
}
