package debugger

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// NextRegAccessor exposes the raw read / write operations on the
// Spectrum Next's NextReg space. The dispatcher in pkg/next
// satisfies this interface, but the widget doesn't take a direct
// dependency on that package — pass any implementation through
// SetAccessor.
type NextRegAccessor interface {
	ReadReg(reg byte) byte
	WriteReg(reg byte, val byte)
}

// NextRegWidget exposes arbitrary NextReg read / write controls
// equivalent to the telnet `nextreg-read` / `nextreg-write`
// commands. The fixed-set NEXT STATE panel handles the common
// regs; this widget handles "I need to poke $XX = $YY right now".
type NextRegWidget struct {
	regE     *widget.Entry
	valE     *widget.Entry
	readBtn  *widget.Button
	writeBtn *widget.Button
	out      *widget.Entry
	status   *widget.Label
	root     *fyne.Container

	access NextRegAccessor
}

// NewNextRegWidget builds the widget. Pass nil for the accessor
// during construction and wire it later via SetAccessor.
func NewNextRegWidget(a NextRegAccessor) *NextRegWidget {
	w := &NextRegWidget{access: a}

	w.regE = widget.NewEntry()
	w.regE.SetPlaceHolder("$50")
	w.regE.TextStyle = fyne.TextStyle{Monospace: true}

	w.valE = widget.NewEntry()
	w.valE.SetPlaceHolder("$FF")
	w.valE.TextStyle = fyne.TextStyle{Monospace: true}

	w.readBtn = widget.NewButton("Read", w.onRead)
	w.writeBtn = widget.NewButton("Write", w.onWrite)

	w.out = widget.NewMultiLineEntry()
	w.out.TextStyle = fyne.TextStyle{Monospace: true}
	w.out.Wrapping = fyne.TextWrapOff
	w.out.SetText("(read / write any NextReg by index)")

	w.status = widget.NewLabel("")

	regSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(80, 36)), w.regE)
	valSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(80, 36)), w.valE)

	top := container.NewHBox(
		widget.NewLabel("Reg"), regSized,
		widget.NewLabel("Val"), valSized,
		w.readBtn, w.writeBtn,
	)
	w.root = container.NewBorder(
		container.NewVBox(top, w.status),
		nil, nil, nil, container.NewVScroll(w.out),
	)
	return w
}

// SetAccessor rewires the backend. Pass nil to disconnect.
func (w *NextRegWidget) SetAccessor(a NextRegAccessor) { w.access = a }

// Root returns the embeddable canvas object.
func (w *NextRegWidget) Root() fyne.CanvasObject { return w.root }

func (w *NextRegWidget) onRead() {
	if w.access == nil {
		w.status.SetText("no accessor — not a Next model")
		return
	}
	reg, err := parseUserHex(w.regE.Text)
	if err != nil {
		w.status.SetText("bad reg: " + err.Error())
		return
	}
	if reg < 0 || reg > 0xFF {
		w.status.SetText("reg out of range")
		return
	}
	v := w.access.ReadReg(byte(reg))
	w.appendOut(fmt.Sprintf("R  NR$%02X = $%02X  (%08b  %d)", reg, v, v, v))
	w.status.SetText(fmt.Sprintf("read NR$%02X = $%02X", reg, v))
	w.valE.SetText(fmt.Sprintf("$%02X", v))
}

func (w *NextRegWidget) onWrite() {
	if w.access == nil {
		w.status.SetText("no accessor — not a Next model")
		return
	}
	reg, err := parseUserHex(w.regE.Text)
	if err != nil {
		w.status.SetText("bad reg: " + err.Error())
		return
	}
	val, err := parseUserHex(w.valE.Text)
	if err != nil {
		w.status.SetText("bad val: " + err.Error())
		return
	}
	if reg < 0 || reg > 0xFF || val < 0 || val > 0xFF {
		w.status.SetText("reg/val out of range")
		return
	}
	w.access.WriteReg(byte(reg), byte(val))
	w.appendOut(fmt.Sprintf("W  NR$%02X = $%02X", reg, val))
	w.status.SetText(fmt.Sprintf("wrote NR$%02X = $%02X", reg, val))
}

func (w *NextRegWidget) appendOut(line string) {
	cur := w.out.Text
	if strings.HasPrefix(cur, "(") {
		cur = ""
	}
	w.out.SetText(cur + line + "\n")
}
