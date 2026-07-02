package debugger

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// BankInspectWidget is the visual surface for the BankAccessor.
// Lets the user pick a kind / bank / offset and either dump bytes
// or write a comma-separated list back into the bank. Constructed
// once at debugger build time; backed by a BankAccessor that may be
// nil until SetAccessor is called.
type BankInspectWidget struct {
	kinds   []string
	kindSel *widget.Select
	bankE   *widget.Entry
	offE    *widget.Entry
	lenE    *widget.Entry
	dumpBtn *widget.Button
	pokeE   *widget.Entry
	pokeBtn *widget.Button
	out     *widget.Entry
	status  *canvas.Text
	root    *fyne.Container

	accessor BankAccessor
}

// NewBankInspectWidget builds the panel. Pass a non-nil
// BankAccessor to make the controls live; pass nil and call
// SetAccessor later (e.g. once the emulator has been wired).
//
// The kind drop-down is fixed to the canonical names; callers that
// expose additional kinds via their BankAccessor can pass the
// extended list via SetKinds.
func NewBankInspectWidget(a BankAccessor) *BankInspectWidget {
	w := &BankInspectWidget{
		accessor: a,
		kinds:    []string{"ram", "rom", "altrom", "divmmc-ram"},
	}
	w.kindSel = widget.NewSelect(w.kinds, func(string) {})
	w.kindSel.SetSelected("ram")

	w.bankE = widget.NewEntry()
	w.bankE.SetPlaceHolder("$05 / 5")
	w.bankE.TextStyle = fyne.TextStyle{Monospace: true}

	w.offE = widget.NewEntry()
	w.offE.SetPlaceHolder("$0000")
	w.offE.TextStyle = fyne.TextStyle{Monospace: true}

	w.lenE = widget.NewEntry()
	w.lenE.SetText("$80")
	w.lenE.TextStyle = fyne.TextStyle{Monospace: true}

	w.dumpBtn = widget.NewButton("Dump", w.onDump)

	w.pokeE = widget.NewEntry()
	w.pokeE.SetPlaceHolder("AA BB CC...")
	w.pokeE.TextStyle = fyne.TextStyle{Monospace: true}

	w.pokeBtn = widget.NewButton("Poke", w.onPoke)

	w.out = widget.NewMultiLineEntry()
	w.out.TextStyle = fyne.TextStyle{Monospace: true}
	w.out.SetText("")
	w.out.Wrapping = fyne.TextWrapOff

	w.status = canvas.NewText("ready", color.NRGBA{R: 180, G: 180, B: 200, A: 255})
	w.status.TextStyle = fyne.TextStyle{Monospace: true}
	w.status.TextSize = 11

	// Top row: kind + bank + off + len + Dump
	kindSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(120, 36)), w.kindSel)
	bankSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(70, 36)), w.bankE)
	offSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(90, 36)), w.offE)
	lenSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(70, 36)), w.lenE)
	dumpRow := container.NewHBox(
		widget.NewLabel("Kind"), kindSized,
		widget.NewLabel("Bank"), bankSized,
		widget.NewLabel("Off"), offSized,
		widget.NewLabel("Len"), lenSized,
		w.dumpBtn,
	)

	// Poke row: bytes input + Poke button
	pokeSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(380, 36)), w.pokeE)
	pokeRow := container.NewHBox(
		widget.NewLabel("Bytes"), pokeSized, w.pokeBtn,
	)

	scrollOut := container.NewVScroll(w.out)

	w.root = container.NewBorder(
		container.NewVBox(dumpRow, pokeRow, widget.NewSeparator(), w.status),
		nil, nil, nil, scrollOut,
	)
	return w
}

// SetAccessor replaces the live BankAccessor. Passing nil leaves
// the widget in a "no backend" state where dump/poke just report
// the error.
func (w *BankInspectWidget) SetAccessor(a BankAccessor) { w.accessor = a }

// SetKinds replaces the kind drop-down options. Useful when the
// emulator exposes machine-specific kinds beyond the canonical four.
func (w *BankInspectWidget) SetKinds(k []string) {
	if len(k) == 0 {
		return
	}
	w.kinds = k
	w.kindSel.Options = k
	w.kindSel.Refresh()
}

// Root returns the CanvasObject to embed in the debugger layout.
func (w *BankInspectWidget) Root() fyne.CanvasObject { return w.root }

func (w *BankInspectWidget) onDump() {
	if w.accessor == nil {
		w.setStatus("no bank accessor", true)
		return
	}
	kind := w.kindSel.Selected
	bank, err := parseUserHex(w.bankE.Text)
	if err != nil {
		w.setStatus("bad bank: "+err.Error(), true)
		return
	}
	off, err := parseUserHex(w.offE.Text)
	if err != nil {
		w.setStatus("bad off: "+err.Error(), true)
		return
	}
	n, err := parseUserHex(w.lenE.Text)
	if err != nil {
		w.setStatus("bad len: "+err.Error(), true)
		return
	}
	if n <= 0 {
		w.setStatus("len must be > 0", true)
		return
	}
	if n > 4096 {
		n = 4096
	}
	out, perr := w.accessor.BankPeek(kind, bank, off, n)
	if perr != nil {
		w.setStatus(perr.Error(), true)
		return
	}
	w.out.SetText(hexDump(uint32(off), out))
	w.setStatus(fmt.Sprintf("%d bytes from %s bank %d @ $%04X", len(out), kind, bank, off), false)
}

func (w *BankInspectWidget) onPoke() {
	if w.accessor == nil {
		w.setStatus("no bank accessor", true)
		return
	}
	kind := w.kindSel.Selected
	bank, err := parseUserHex(w.bankE.Text)
	if err != nil {
		w.setStatus("bad bank: "+err.Error(), true)
		return
	}
	off, err := parseUserHex(w.offE.Text)
	if err != nil {
		w.setStatus("bad off: "+err.Error(), true)
		return
	}
	data, err := parseByteList(w.pokeE.Text)
	if err != nil {
		w.setStatus(err.Error(), true)
		return
	}
	if len(data) == 0 {
		w.setStatus("nothing to poke", true)
		return
	}
	if perr := w.accessor.BankPoke(kind, bank, off, data); perr != nil {
		w.setStatus(perr.Error(), true)
		return
	}
	w.setStatus(fmt.Sprintf("wrote %d bytes to %s bank %d @ $%04X", len(data), kind, bank, off), false)
}

func (w *BankInspectWidget) setStatus(s string, isErr bool) {
	w.status.Text = s
	if isErr {
		w.status.Color = color.NRGBA{R: 220, G: 80, B: 80, A: 255}
	} else {
		w.status.Color = color.NRGBA{R: 180, G: 200, B: 180, A: 255}
	}
	w.status.Refresh()
}

// parseUserHex accepts $XX, 0xXX, bare hex, or decimal-with-d
// suffix. The default is hex because the debugger's primary
// audience reads memory in hex. The decimal-suffix form only
// applies to unprefixed input — an explicit $/0x marker always
// means hex, even when its last digit is 'd' (e.g. $1D, $FD).
func parseUserHex(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	hasHexPrefix := strings.HasPrefix(s, "$") ||
		strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")
	if !hasHexPrefix && (strings.HasSuffix(s, "d") || strings.HasSuffix(s, "D")) {
		v, err := strconv.ParseInt(s[:len(s)-1], 10, 32)
		if err != nil {
			return 0, err
		}
		return int(v), nil
	}
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	v, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		return 0, err
	}
	return int(v), nil
}

// parseByteList parses a whitespace- or comma-separated list of
// hex bytes (with optional $ / 0x prefix) into a slice. Useful for
// "AA BB CC" or "$10,$20,$30" input.
func parseByteList(s string) ([]byte, error) {
	repl := strings.NewReplacer(",", " ", ";", " ")
	s = repl.Replace(s)
	fields := strings.Fields(s)
	out := make([]byte, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimPrefix(strings.TrimPrefix(f, "$"), "0x")
		v, err := strconv.ParseUint(f, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("bad byte %q: %v", f, err)
		}
		out = append(out, byte(v))
	}
	return out, nil
}

// hexDump renders bytes as a 16-column address-prefixed hexdump.
// Format mirrors `xxd` so the output is easy to eyeball.
func hexDump(start uint32, data []byte) string {
	var sb strings.Builder
	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}
		fmt.Fprintf(&sb, "%06X: ", start+uint32(i))
		for j := i; j < i+16; j++ {
			if j < end {
				fmt.Fprintf(&sb, "%02X ", data[j])
			} else {
				sb.WriteString("   ")
			}
		}
		sb.WriteString(" ")
		for j := i; j < end; j++ {
			c := data[j]
			if c >= 0x20 && c < 0x7F {
				sb.WriteByte(c)
			} else {
				sb.WriteByte('.')
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
