package debugger

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

var (
	colPC      = color.NRGBA{R: 255, G: 220, B: 60, A: 255}
	colBP      = color.NRGBA{R: 255, G: 80, B: 80, A: 255}
	colBPHit   = color.NRGBA{R: 255, G: 140, B: 40, A: 255}
	colAddr    = color.NRGBA{R: 100, G: 180, B: 255, A: 255}
	colHex     = color.NRGBA{R: 190, G: 190, B: 190, A: 255}
	colMnem    = color.NRGBA{R: 180, G: 140, B: 255, A: 255}
	colRegName = color.NRGBA{R: 100, G: 180, B: 255, A: 255}
	colRegVal  = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	colFlagOn  = color.NRGBA{R: 100, G: 255, B: 100, A: 255}
	colRunning = color.NRGBA{R: 100, G: 255, B: 100, A: 255}
	colPaused  = color.NRGBA{R: 255, G: 220, B: 60, A: 255}
	colHalted  = color.NRGBA{R: 255, G: 80, B: 80, A: 255}
	colPanel   = color.NRGBA{R: 30, G: 30, B: 40, A: 255}
)

const (
	hexRows  = 4096 // Full 64KB: 65536 / 16 bytes per row
	dasmRows = 80
	fontSize = 13
)

type Debugger struct {
	cpu *z80.CPU
	mem *memory.Memory

	window fyne.Window
	mu     sync.Mutex

	onPause  func()
	onStep   func()
	onRun    func()
	isPaused func() bool

	hexAddr      uint16
	hexAddrBase  int // 16, 10, or 8
	breakpoints  map[uint16]bool

	// Register display
	regLines [20]*canvas.Text
	flagLine *canvas.Text
	iffLine  *canvas.Text
	haltLine *canvas.Text

	// Lists for scrollable, tappable content
	dasmList      *widget.List
	dasmCache     []DisassembledLine
	dasmLastPC    uint16
	hexList       *widget.List

	statusTxt    *canvas.Text
	hexAddrEntry *widget.Entry

	refreshTicker *time.Ticker
	stopChan      chan struct{}
}

func New(cpu *z80.CPU, mem *memory.Memory, app fyne.App) *Debugger {
	d := &Debugger{
		cpu:         cpu,
		mem:         mem,
		hexAddr:     0x0000,
		hexAddrBase: 16,
		breakpoints: make(map[uint16]bool),
		stopChan:    make(chan struct{}),
	}
	d.window = app.NewWindow("ZX Spectrum Debugger")
	d.window.Resize(fyne.NewSize(1400, 800))
	d.window.SetContent(d.buildUI())
	d.window.SetOnClosed(func() { d.stopRefresh() })
	return d
}

func (d *Debugger) SetCallbacks(onPause func(), onStep func(), onRun func(), isPaused func() bool) {
	d.onPause = onPause
	d.onStep = onStep
	d.onRun = onRun
	d.isPaused = isPaused
}

func (d *Debugger) Show() {
	if d.onPause != nil {
		d.onPause()
	}
	d.Refresh()
	d.startRefresh()
	d.window.Show()
}

func (d *Debugger) Refresh() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refreshRegisters()
	d.refreshDisassembly()
	d.refreshHex()
	d.refreshStatus()
}

func (d *Debugger) CheckBreakpoint() bool {
	return d.breakpoints[d.cpu.PC]
}

func (d *Debugger) startRefresh() {
	d.refreshTicker = time.NewTicker(50 * time.Millisecond)
	go func() {
		for {
			select {
			case <-d.refreshTicker.C:
				fyne.Do(func() { d.Refresh() })
			case <-d.stopChan:
				return
			}
		}
	}()
}

func (d *Debugger) stopRefresh() {
	if d.refreshTicker != nil {
		d.refreshTicker.Stop()
	}
	select {
	case d.stopChan <- struct{}{}:
	default:
	}
}

// --- helpers ---

func (d *Debugger) formatAddr(addr uint16) string {
	switch d.hexAddrBase {
	case 10:
		return fmt.Sprintf("%d", addr)
	case 8:
		return fmt.Sprintf("%o", addr)
	default:
		return fmt.Sprintf("%04X", addr)
	}
}

func mkText(c color.Color) *canvas.Text {
	t := canvas.NewText("", c)
	t.TextStyle = fyne.TextStyle{Monospace: true}
	t.TextSize = fontSize
	return t
}

func panelBG(content fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(colPanel)
	bg.CornerRadius = 6
	return container.NewStack(bg, container.NewPadded(content))
}

func headerText(s string, c color.Color) *canvas.Text {
	t := canvas.NewText(s, c)
	t.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	t.TextSize = 14
	return t
}

// --- UI ---

func (d *Debugger) buildUI() fyne.CanvasObject {
	for i := range d.regLines {
		d.regLines[i] = mkText(colRegVal)
	}
	d.flagLine = mkText(colFlagOn)
	d.iffLine = mkText(colRegVal)
	d.haltLine = mkText(colHalted)
	d.statusTxt = mkText(colPaused)

	// Register panel
	regBox := container.NewVBox()
	for i := range d.regLines {
		regBox.Add(d.regLines[i])
	}
	regBox.Add(widget.NewSeparator())
	regBox.Add(d.flagLine)
	regBox.Add(d.iffLine)
	regBox.Add(d.haltLine)
	regScroll := container.NewVScroll(regBox)
	regScroll.SetMinSize(fyne.NewSize(230, 0))
	regPanel := panelBG(container.NewBorder(
		container.NewVBox(headerText("  REGISTERS", colRegName), widget.NewSeparator()),
		nil, nil, nil, regScroll,
	))

	// Disassembly panel (centre — widget.List for scrolling + tapping)
	d.dasmCache = Disassemble(d.mem.Read, d.cpu.PC, dasmRows)
	d.dasmList = widget.NewList(
		func() int { return dasmRows },
		func() fyne.CanvasObject {
			t := canvas.NewText(strings.Repeat(" ", 60), colMnem)
			t.TextStyle = fyne.TextStyle{Monospace: true}
			t.TextSize = fontSize
			return t
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			if id >= len(d.dasmCache) {
				t.Text = ""
				t.Refresh()
				return
			}
			line := d.dasmCache[id]
			isPC := line.Addr == d.cpu.PC
			isBP := d.breakpoints[line.Addr]

			prefix := "  "
			if isPC && isBP {
				prefix = "*>"
			} else if isPC {
				prefix = "> "
			} else if isBP {
				prefix = "* "
			}

			hexStr := ""
			for _, b := range line.Bytes {
				hexStr += fmt.Sprintf("%02X ", b)
			}
			for len(hexStr) < 12 {
				hexStr += "   "
			}
			op := line.Mnem
			if line.Operand != "" {
				op += " " + line.Operand
			}
			t.Text = fmt.Sprintf("%s%04X  %s%s", prefix, line.Addr, hexStr, op)

			if isPC && isBP {
				t.Color = colBPHit
			} else if isPC {
				t.Color = colPC
			} else if isBP {
				t.Color = colBP
			} else {
				t.Color = colMnem
			}
			t.Refresh()
		},
	)
	d.dasmList.OnSelected = func(id widget.ListItemID) {
		// Toggle breakpoint on tap
		if id < len(d.dasmCache) {
			addr := d.dasmCache[id].Addr
			if d.breakpoints[addr] {
				delete(d.breakpoints, addr)
			} else {
				d.breakpoints[addr] = true
			}
			d.dasmList.UnselectAll()
			d.dasmList.Refresh()
		}
	}

	dasmPanel := panelBG(container.NewBorder(
		container.NewVBox(headerText("  DISASSEMBLY  (tap to toggle breakpoint)", colMnem), widget.NewSeparator()),
		nil, nil, nil, d.dasmList,
	))

	// Hex panel (right — widget.List for scrolling)
	d.hexList = widget.NewList(
		func() int { return hexRows },
		func() fyne.CanvasObject {
			t := canvas.NewText(strings.Repeat(" ", 70), colHex)
			t.TextStyle = fyne.TextStyle{Monospace: true}
			t.TextSize = fontSize
			return t
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			addr := uint16(id) * 16
			var sb strings.Builder
			fmt.Fprintf(&sb, "%04X  ", addr)
			ascii := ""
			hasPC := false
			for col := 0; col < 16; col++ {
				a := addr + uint16(col)
				b := d.mem.Read(a)
				if a == d.cpu.PC {
					hasPC = true
				}
				fmt.Fprintf(&sb, "%02X ", b)
				if col == 7 {
					sb.WriteByte(' ')
				}
				if b >= 0x20 && b < 0x7F {
					ascii += string(rune(b))
				} else {
					ascii += "."
				}
			}
			sb.WriteString(" |" + ascii + "|")
			t.Text = sb.String()
			if hasPC {
				t.Color = colPC
			} else {
				t.Color = colHex
			}
			t.Refresh()
		},
	)

	d.hexAddrEntry = widget.NewEntry()
	d.hexAddrEntry.SetText("0000")
	d.hexAddrEntry.TextStyle = fyne.TextStyle{Monospace: true}

	baseSelect := widget.NewSelect([]string{"Hex", "Dec", "Oct"}, func(val string) {
		switch val {
		case "Dec":
			d.hexAddrBase = 10
			d.hexAddrEntry.SetText(fmt.Sprintf("%d", d.hexAddr))
		case "Oct":
			d.hexAddrBase = 8
			d.hexAddrEntry.SetText(fmt.Sprintf("%o", d.hexAddr))
		default:
			d.hexAddrBase = 16
			d.hexAddrEntry.SetText(fmt.Sprintf("%04X", d.hexAddr))
		}
	})
	baseSelect.SetSelected("Hex")

	d.hexAddrEntry.OnSubmitted = func(s string) {
		if addr, err := strconv.ParseUint(s, d.hexAddrBase, 16); err == nil {
			d.hexAddr = uint16(addr) & 0xFFF0
			row := int(d.hexAddr) / 16
			d.hexList.ScrollTo(widget.ListItemID(row))
		}
	}

	hexAddrSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(100, 36)), d.hexAddrEntry)
	baseSized := container.New(layout.NewGridWrapLayout(fyne.NewSize(70, 36)), baseSelect)

	hexPanel := panelBG(container.NewBorder(
		container.NewVBox(
			container.NewHBox(headerText("  MEMORY", colAddr), widget.NewLabel("  Addr:"), hexAddrSized, baseSized),
			widget.NewSeparator(),
		), nil, nil, nil, d.hexList,
	))

	// Layout: registers | disassembly | hex
	innerSplit := container.NewHSplit(regPanel, dasmPanel)
	innerSplit.SetOffset(0.22)
	split := container.NewHSplit(innerSplit, hexPanel)
	split.SetOffset(0.58)

	return container.NewBorder(
		container.NewVBox(d.buildControls(), widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), panelBG(d.statusTxt)),
		nil, nil, split,
	)
}

func (d *Debugger) buildControls() fyne.CanvasObject {
	pauseBtn := widget.NewButtonWithIcon("Pause", theme.MediaPauseIcon(), func() {
		if d.onPause != nil {
			d.onPause()
		}
		d.Refresh()
	})
	stepBtn := widget.NewButtonWithIcon("Step", theme.MediaSkipNextIcon(), func() {
		if d.onStep != nil {
			d.onStep()
		}
		d.Refresh()
	})
	runBtn := widget.NewButtonWithIcon("Run", theme.MediaPlayIcon(), func() {
		if d.onRun != nil {
			d.onRun()
		}
	})
	frameBtn := widget.NewButton("Step Frame", func() {
		if d.onPause != nil {
			d.onPause()
		}
		d.cpu.ExecuteFrame(69888)
		d.Refresh()
	})
	pcBtn := widget.NewButton("Go to PC", func() {
		d.hexAddr = d.cpu.PC & 0xFFF0
		d.hexAddrEntry.SetText(d.formatAddr(d.hexAddr))
		d.hexList.ScrollTo(widget.ListItemID(int(d.hexAddr) / 16))
		d.Refresh()
	})
	editBtn := widget.NewButton("Edit Regs...", func() { d.showEditDialog() })
	writeBtn := widget.NewButton("Write Mem...", func() { d.showWriteDialog() })
	clearBPBtn := widget.NewButton("Clear BPs", func() {
		d.breakpoints = make(map[uint16]bool)
		d.Refresh()
	})

	return container.NewHBox(
		pauseBtn, stepBtn, frameBtn, runBtn,
		widget.NewSeparator(),
		pcBtn, editBtn, writeBtn,
		widget.NewSeparator(),
		clearBPBtn,
	)
}

// --- Refresh ---

func (d *Debugger) refreshRegisters() {
	set := func(i int, s string) {
		d.regLines[i].Text = s
		d.regLines[i].Refresh()
	}
	setC := func(i int, s string, c color.Color) {
		d.regLines[i].Text = s
		d.regLines[i].Color = c
		d.regLines[i].Refresh()
	}

	setC(0, fmt.Sprintf(" PC   %04X", d.cpu.PC), colPC)
	set(1, fmt.Sprintf(" SP   %04X", d.cpu.SP))
	set(2, fmt.Sprintf(" IX   %04X", d.cpu.IX))
	set(3, fmt.Sprintf(" IY   %04X", d.cpu.IY))
	set(4, "")
	set(5, fmt.Sprintf(" A %02X   F %02X", d.cpu.A, d.cpu.F))
	set(6, fmt.Sprintf(" B %02X   C %02X", d.cpu.B, d.cpu.C))
	set(7, fmt.Sprintf(" D %02X   E %02X", d.cpu.D, d.cpu.E))
	set(8, fmt.Sprintf(" H %02X   L %02X", d.cpu.H, d.cpu.L))
	set(9, "")
	set(10, fmt.Sprintf(" A'%02X   F'%02X", d.cpu.A_, d.cpu.F_))
	set(11, fmt.Sprintf(" B'%02X   C'%02X", d.cpu.B_, d.cpu.C_))
	set(12, fmt.Sprintf(" D'%02X   E'%02X", d.cpu.D_, d.cpu.E_))
	set(13, fmt.Sprintf(" H'%02X   L'%02X", d.cpu.H_, d.cpu.L_))
	set(14, "")
	set(15, fmt.Sprintf(" I  %02X   R  %02X", d.cpu.I, d.cpu.R))
	set(16, "")
	set(17, " Stack:")
	sp := d.cpu.SP
	set(18, fmt.Sprintf("  %04X: %02X%02X  %02X%02X", sp, d.mem.Read(sp+1), d.mem.Read(sp), d.mem.Read(sp+3), d.mem.Read(sp+2)))
	set(19, fmt.Sprintf("  %04X: %02X%02X  %02X%02X", sp+4, d.mem.Read(sp+5), d.mem.Read(sp+4), d.mem.Read(sp+7), d.mem.Read(sp+6)))

	f := d.cpu.F
	flagStr := " "
	for _, fl := range []struct{ n string; b byte }{{"S", 0x80}, {"Z", 0x40}, {"H", 0x10}, {"PV", 0x04}, {"N", 0x02}, {"C", 0x01}} {
		if f&fl.b != 0 {
			flagStr += fl.n + ":1 "
		} else {
			flagStr += fl.n + ":0 "
		}
	}
	d.flagLine.Text = flagStr
	d.flagLine.Color = colRegVal
	d.flagLine.Refresh()

	d.iffLine.Text = fmt.Sprintf(" IFF %v/%v  IM %d", d.cpu.IFF1, d.cpu.IFF2, d.cpu.IM)
	d.iffLine.Refresh()

	if d.cpu.Halted {
		d.haltLine.Text = " ** HALTED **"
		d.haltLine.Color = colHalted
	} else {
		d.haltLine.Text = ""
	}
	d.haltLine.Refresh()
}

func (d *Debugger) refreshDisassembly() {
	pc := d.cpu.PC
	// Only re-disassemble if PC changed
	if pc != d.dasmLastPC || len(d.dasmCache) == 0 {
		d.dasmCache = Disassemble(d.mem.Read, pc, dasmRows)
		d.dasmLastPC = pc
	}
	d.dasmList.Refresh()
	// Scroll to show PC at top
	d.dasmList.ScrollToTop()
}

func (d *Debugger) refreshHex() {
	d.hexList.Refresh()
}

func (d *Debugger) refreshStatus() {
	paused := d.isPaused != nil && d.isPaused()
	state := "RUNNING"
	col := colRunning
	if paused {
		state = "PAUSED"
		col = colPaused
	}
	if d.cpu.Halted {
		state += " [HALTED]"
		col = colHalted
	}

	bps := ""
	if len(d.breakpoints) > 0 {
		addrs := []string{}
		for a := range d.breakpoints {
			addrs = append(addrs, fmt.Sprintf("%04X", a))
		}
		bps = "  BPs:" + strings.Join(addrs, ",")
	}

	d.statusTxt.Text = fmt.Sprintf(
		" %s  PC:%04X SP:%04X AF:%02X%02X BC:%02X%02X DE:%02X%02X HL:%02X%02X IM:%d IFF:%v%s",
		state, d.cpu.PC, d.cpu.SP,
		d.cpu.A, d.cpu.F, d.cpu.B, d.cpu.C, d.cpu.D, d.cpu.E, d.cpu.H, d.cpu.L,
		d.cpu.IM, d.cpu.IFF1, bps)
	d.statusTxt.Color = col
	d.statusTxt.Refresh()
}

// --- Dialogs ---

func (d *Debugger) showEditDialog() {
	entries := make(map[string]*widget.Entry)
	regs := []struct{ name, val string }{
		{"PC", fmt.Sprintf("%04X", d.cpu.PC)}, {"SP", fmt.Sprintf("%04X", d.cpu.SP)},
		{"IX", fmt.Sprintf("%04X", d.cpu.IX)}, {"IY", fmt.Sprintf("%04X", d.cpu.IY)},
		{"A", fmt.Sprintf("%02X", d.cpu.A)}, {"F", fmt.Sprintf("%02X", d.cpu.F)},
		{"B", fmt.Sprintf("%02X", d.cpu.B)}, {"C", fmt.Sprintf("%02X", d.cpu.C)},
		{"D", fmt.Sprintf("%02X", d.cpu.D)}, {"E", fmt.Sprintf("%02X", d.cpu.E)},
		{"H", fmt.Sprintf("%02X", d.cpu.H)}, {"L", fmt.Sprintf("%02X", d.cpu.L)},
		{"I", fmt.Sprintf("%02X", d.cpu.I)}, {"R", fmt.Sprintf("%02X", d.cpu.R)},
	}
	items := []*widget.FormItem{}
	for _, r := range regs {
		e := widget.NewEntry()
		e.SetText(r.val)
		e.TextStyle = fyne.TextStyle{Monospace: true}
		entries[r.name] = e
		items = append(items, widget.NewFormItem(r.name, e))
	}
	form := widget.NewForm(items...)
	content := container.NewVBox(form)
	dlg := widget.NewModalPopUp(content, d.window.Canvas())

	p16 := func(n string) uint16 { v, _ := strconv.ParseUint(entries[n].Text, 16, 16); return uint16(v) }
	p8 := func(n string) byte { v, _ := strconv.ParseUint(entries[n].Text, 16, 8); return byte(v) }

	content.Add(container.NewHBox(layout.NewSpacer(),
		widget.NewButton("Cancel", func() { dlg.Hide() }),
		widget.NewButton("Apply", func() {
			d.cpu.PC = p16("PC"); d.cpu.SP = p16("SP"); d.cpu.IX = p16("IX"); d.cpu.IY = p16("IY")
			d.cpu.A = p8("A"); d.cpu.F = p8("F"); d.cpu.B = p8("B"); d.cpu.C = p8("C")
			d.cpu.D = p8("D"); d.cpu.E = p8("E"); d.cpu.H = p8("H"); d.cpu.L = p8("L")
			d.cpu.I = p8("I"); d.cpu.R = p8("R")
			dlg.Hide(); d.Refresh()
		}),
	))
	dlg.Resize(fyne.NewSize(300, 500))
	dlg.Show()
}

func (d *Debugger) showWriteDialog() {
	addrE := widget.NewEntry()
	addrE.SetText(fmt.Sprintf("%04X", d.hexAddr))
	addrE.TextStyle = fyne.TextStyle{Monospace: true}
	valE := widget.NewEntry()
	valE.SetPlaceHolder("00 01 FF ...")
	valE.TextStyle = fyne.TextStyle{Monospace: true}

	form := widget.NewForm(widget.NewFormItem("Address", addrE), widget.NewFormItem("Hex bytes", valE))
	content := container.NewVBox(form)
	dlg := widget.NewModalPopUp(content, d.window.Canvas())
	content.Add(container.NewHBox(layout.NewSpacer(),
		widget.NewButton("Cancel", func() { dlg.Hide() }),
		widget.NewButton("Write", func() {
			addr, err := strconv.ParseUint(addrE.Text, 16, 16)
			if err == nil {
				for _, p := range strings.Fields(valE.Text) {
					if v, err := strconv.ParseUint(p, 16, 8); err == nil {
						d.mem.Write(uint16(addr), byte(v))
						addr++
					}
				}
			}
			dlg.Hide(); d.Refresh()
		}),
	))
	dlg.Resize(fyne.NewSize(400, 200))
	dlg.Show()
}
