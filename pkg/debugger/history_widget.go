package debugger

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// HistoryWidget renders an M1-fetch ring (the same one feeding the
// telnet `history` / `prev` commands). If no History is wired, the
// widget reports "history disabled".
type HistoryWidget struct {
	countE  *widget.Entry
	allBtn  *widget.Button
	prevBtn *widget.Button
	out     *widget.Entry
	root    *fyne.Container

	history *History
}

// NewHistoryWidget builds the widget. history may be nil; supply
// later via SetHistory.
func NewHistoryWidget(h *History) *HistoryWidget {
	w := &HistoryWidget{history: h}
	w.countE = widget.NewEntry()
	w.countE.SetText("20")
	w.countE.TextStyle = fyne.TextStyle{Monospace: true}

	w.allBtn = widget.NewButton("Full", w.renderAll)
	w.prevBtn = widget.NewButton("Prev N", w.renderPrev)

	w.out = widget.NewMultiLineEntry()
	w.out.TextStyle = fyne.TextStyle{Monospace: true}
	w.out.Wrapping = fyne.TextWrapOff
	w.out.SetText("(press Full / Prev N while paused)")

	top := container.NewHBox(widget.NewLabel("N"), w.countE, w.prevBtn, w.allBtn)
	w.root = container.NewBorder(top, nil, nil, nil, container.NewVScroll(w.out))
	return w
}

// SetHistory rewires the ring. Pass nil to disconnect.
func (w *HistoryWidget) SetHistory(h *History) { w.history = h }

// Root returns the embeddable canvas object.
func (w *HistoryWidget) Root() fyne.CanvasObject { return w.root }

func (w *HistoryWidget) renderAll() { w.render(0) }

func (w *HistoryWidget) renderPrev() {
	n := 20
	if v, err := parseUserHex(w.countE.Text); err == nil && v > 0 {
		n = v
	}
	w.render(n)
}

func (w *HistoryWidget) render(limit int) {
	if w.history == nil {
		w.out.SetText("history disabled — start with --debugger-history > 0")
		return
	}
	if limit <= 0 {
		limit = w.history.Capacity()
	}
	entries := w.history.Last(limit)
	if len(entries) == 0 {
		w.out.SetText("(ring empty)")
		return
	}
	wide := w.history.Wide()
	var sb strings.Builder
	if wide {
		sb.WriteString("insn        PC    SP    AF      BC    DE    HL    IX    IY    IFF/IM/H bank  via\n")
	} else {
		sb.WriteString("insn        PC    SP    AF      IFF/IM/H  bank  via\n")
	}
	for _, e := range entries {
		via := ""
		if e.Source != SourceSequential {
			if e.SourceFrom != 0 {
				via = fmt.Sprintf("  %s@$%04X", e.Source, e.SourceFrom)
			} else {
				via = "  " + e.Source.String()
			}
		}
		if wide {
			fmt.Fprintf(&sb, "%-10d $%04X $%04X $%02X%02X    $%04X $%04X $%04X $%04X $%04X %v/%d/%v   %d%s\n",
				e.Insns, e.PC, e.SP, e.A, e.F, e.BC, e.DE, e.HL, e.IX, e.IY,
				e.IFF1(), e.IM(), e.Halted(), e.ROMBnk, via)
		} else {
			fmt.Fprintf(&sb, "%-10d $%04X $%04X $%02X%02X    %v/%d/%v    %d%s\n",
				e.Insns, e.PC, e.SP, e.A, e.F, e.IFF1(), e.IM(), e.Halted(), e.ROMBnk, via)
		}
	}
	w.out.SetText(sb.String())
}
