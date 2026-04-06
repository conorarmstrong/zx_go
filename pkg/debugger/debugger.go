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

// Colour palette for the debugger
var (
	colPC       = color.NRGBA{R: 255, G: 220, B: 60, A: 255}  // Yellow — current PC
	colBP       = color.NRGBA{R: 255, G: 80, B: 80, A: 255}   // Red — breakpoint
	colBPHit    = color.NRGBA{R: 255, G: 140, B: 40, A: 255}  // Orange — breakpoint + PC
	colAddr     = color.NRGBA{R: 100, G: 180, B: 255, A: 255} // Blue — addresses
	colHex      = color.NRGBA{R: 200, G: 200, B: 200, A: 255} // Light grey — hex bytes
	colASCII    = color.NRGBA{R: 140, G: 220, B: 140, A: 255} // Green — ASCII
	colMnem     = color.NRGBA{R: 180, G: 140, B: 255, A: 255} // Purple — mnemonics
	colOperand  = color.NRGBA{R: 255, G: 200, B: 140, A: 255} // Peach — operands
	colRegName  = color.NRGBA{R: 100, G: 180, B: 255, A: 255} // Blue — register names
	colRegVal   = color.NRGBA{R: 255, G: 255, B: 255, A: 255} // White — register values
	colFlagSet  = color.NRGBA{R: 100, G: 255, B: 100, A: 255} // Green — flag set
	colFlagClr  = color.NRGBA{R: 120, G: 120, B: 120, A: 255} // Dim grey — flag clear
	colStatus   = color.NRGBA{R: 200, G: 200, B: 200, A: 255} // Light grey — status
	colRunning  = color.NRGBA{R: 100, G: 255, B: 100, A: 255} // Green — running
	colPaused   = color.NRGBA{R: 255, G: 220, B: 60, A: 255}  // Yellow — paused
	colHalted   = color.NRGBA{R: 255, G: 80, B: 80, A: 255}   // Red — halted
	colPanelBG  = color.NRGBA{R: 30, G: 30, B: 40, A: 255}    // Dark — panel background
)

// Debugger provides a debugging window for the ZX Spectrum emulator.
type Debugger struct {
	cpu *z80.CPU
	mem *memory.Memory

	window fyne.Window
	mu     sync.Mutex

	onPause  func()
	onStep   func()
	onRun    func()
	isPaused func() bool

	hexAddr     uint16
	breakpoints map[uint16]bool

	// Canvas objects for coloured rendering
	regContainer  *fyne.Container
	hexContainer  *fyne.Container
	dasmContainer *fyne.Container
	statusBar     *fyne.Container

	hexAddrEntry  *widget.Entry
	dasmAddrEntry *widget.Entry

	refreshTicker *time.Ticker
	stopChan      chan struct{}
}

// New creates a new debugger.
func New(cpu *z80.CPU, mem *memory.Memory, app fyne.App) *Debugger {
	d := &Debugger{
		cpu:         cpu,
		mem:         mem,
		hexAddr:     0x4000,
		breakpoints: make(map[uint16]bool),
		stopChan:    make(chan struct{}),
	}

	d.window = app.NewWindow("ZX Spectrum Debugger")
	d.window.Resize(fyne.NewSize(1300, 780))

	content := d.buildUI()
	d.window.SetContent(content)
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
	d.refreshHexDump()
	d.refreshDisassembly()
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

// --- Coloured text helpers ---

func colorText(s string, c color.Color, mono bool, size float32) *canvas.Text {
	t := canvas.NewText(s, c)
	t.TextStyle = fyne.TextStyle{Monospace: mono}
	if size > 0 {
		t.TextSize = size
	}
	return t
}

func monoC(s string, c color.Color) *canvas.Text {
	return colorText(s, c, true, 13)
}

func panelBG() *canvas.Rectangle {
	r := canvas.NewRectangle(colPanelBG)
	r.CornerRadius = 6
	return r
}

func panelWithBG(content fyne.CanvasObject) fyne.CanvasObject {
	bg := panelBG()
	return container.NewStack(bg, container.NewPadded(content))
}

// --- UI Building ---

func (d *Debugger) buildUI() fyne.CanvasObject {
	controls := d.buildControls()

	d.regContainer = container.NewVBox()
	d.hexContainer = container.NewVBox()
	d.dasmContainer = container.NewVBox()
	d.statusBar = container.NewHBox()

	regScroll := container.NewVScroll(d.regContainer)
	regScroll.SetMinSize(fyne.NewSize(260, 0))

	hexScroll := container.NewVScroll(d.hexContainer)
	dasmScroll := container.NewVScroll(d.dasmContainer)

	regPanel := panelWithBG(container.NewBorder(
		container.NewVBox(
			colorText("  REGISTERS", colRegName, true, 14),
			widget.NewSeparator(),
		), nil, nil, nil,
		regScroll,
	))

	hexPanel := panelWithBG(container.NewBorder(
		container.NewVBox(
			container.NewHBox(
				colorText("  MEMORY", colAddr, true, 14),
				layout.NewSpacer(),
				d.buildHexAddrBar(),
			),
			widget.NewSeparator(),
		), nil, nil, nil,
		hexScroll,
	))

	dasmPanel := panelWithBG(container.NewBorder(
		container.NewVBox(
			container.NewHBox(
				colorText("  DISASSEMBLY", colMnem, true, 14),
				layout.NewSpacer(),
				d.buildDasmAddrBar(),
			),
			widget.NewSeparator(),
		), nil, nil, nil,
		dasmScroll,
	))

	mainSplit := container.NewHSplit(
		container.NewHSplit(regPanel, hexPanel),
		dasmPanel,
	)
	mainSplit.SetOffset(0.50)

	return container.NewBorder(
		container.NewVBox(controls, widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), panelWithBG(d.statusBar)),
		nil, nil,
		mainSplit,
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
	stepFrameBtn := widget.NewButton("Step Frame", func() {
		if d.onPause != nil {
			d.onPause()
		}
		d.cpu.ExecuteFrame(69888)
		d.Refresh()
	})
	goToPCBtn := widget.NewButton("Go to PC", func() {
		d.hexAddr = d.cpu.PC & 0xFFF0
		d.hexAddrEntry.SetText(fmt.Sprintf("%04X", d.hexAddr))
		d.Refresh()
	})

	// Breakpoint controls
	bpEntry := widget.NewEntry()
	bpEntry.SetPlaceHolder("ADDR")
	bpEntry.TextStyle = fyne.TextStyle{Monospace: true}
	bpBtn := widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
		if addr, err := strconv.ParseUint(bpEntry.Text, 16, 16); err == nil {
			d.breakpoints[uint16(addr)] = true
			bpEntry.SetText("")
			d.Refresh()
		}
	})
	clearBPBtn := widget.NewButton("Clear All", func() {
		d.breakpoints = make(map[uint16]bool)
		d.Refresh()
	})
	editRegBtn := widget.NewButton("Edit Regs...", func() { d.showEditDialog() })
	writeMemBtn := widget.NewButton("Write Mem...", func() { d.showWriteMemDialog() })

	return container.NewHBox(
		pauseBtn, stepBtn, stepFrameBtn, runBtn,
		widget.NewSeparator(),
		goToPCBtn, editRegBtn, writeMemBtn,
		widget.NewSeparator(),
		colorText("BP:", colBP, false, 0), bpEntry, bpBtn, clearBPBtn,
	)
}

func (d *Debugger) buildHexAddrBar() fyne.CanvasObject {
	d.hexAddrEntry = widget.NewEntry()
	d.hexAddrEntry.SetText(fmt.Sprintf("%04X", d.hexAddr))
	d.hexAddrEntry.TextStyle = fyne.TextStyle{Monospace: true}
	d.hexAddrEntry.OnSubmitted = func(s string) {
		if addr, err := strconv.ParseUint(s, 16, 16); err == nil {
			d.hexAddr = uint16(addr) & 0xFFF0
			d.Refresh()
		}
	}
	return container.NewHBox(widget.NewLabel("Addr:"), d.hexAddrEntry)
}

func (d *Debugger) buildDasmAddrBar() fyne.CanvasObject {
	d.dasmAddrEntry = widget.NewEntry()
	d.dasmAddrEntry.TextStyle = fyne.TextStyle{Monospace: true}
	d.dasmAddrEntry.SetPlaceHolder("follows PC")
	return container.NewHBox(widget.NewLabel("PC:"), d.dasmAddrEntry)
}

// --- Register panel ---

func (d *Debugger) refreshRegisters() {
	d.regContainer.RemoveAll()

	addReg16 := func(name string, val uint16) {
		d.regContainer.Add(container.NewHBox(
			monoC(fmt.Sprintf("%-3s ", name), colRegName),
			monoC(fmt.Sprintf("%04X", val), colRegVal),
		))
	}
	addReg8Pair := func(n1 string, v1 byte, n2 string, v2 byte) {
		d.regContainer.Add(container.NewHBox(
			monoC(fmt.Sprintf("%-2s ", n1), colRegName),
			monoC(fmt.Sprintf("%02X", v1), colRegVal),
			monoC("  ", colRegVal),
			monoC(fmt.Sprintf("%-2s ", n2), colRegName),
			monoC(fmt.Sprintf("%02X", v2), colRegVal),
		))
	}

	addReg16("PC", d.cpu.PC)
	addReg16("SP", d.cpu.SP)
	addReg16("IX", d.cpu.IX)
	addReg16("IY", d.cpu.IY)
	d.regContainer.Add(widget.NewSeparator())
	addReg8Pair("A", d.cpu.A, "F", d.cpu.F)
	addReg8Pair("B", d.cpu.B, "C", d.cpu.C)
	addReg8Pair("D", d.cpu.D, "E", d.cpu.E)
	addReg8Pair("H", d.cpu.H, "L", d.cpu.L)
	d.regContainer.Add(widget.NewSeparator())
	addReg8Pair("A'", d.cpu.A_, "F'", d.cpu.F_)
	addReg8Pair("B'", d.cpu.B_, "C'", d.cpu.C_)
	addReg8Pair("D'", d.cpu.D_, "E'", d.cpu.E_)
	addReg8Pair("H'", d.cpu.H_, "L'", d.cpu.L_)
	d.regContainer.Add(widget.NewSeparator())
	addReg8Pair("I", d.cpu.I, "R", d.cpu.R)
	d.regContainer.Add(widget.NewSeparator())

	// Flags with colour coding
	f := d.cpu.F
	flagRow := container.NewHBox()
	addFlag := func(name string, bit byte) {
		set := f&bit != 0
		col := colFlagClr
		val := "0"
		if set {
			col = colFlagSet
			val = "1"
		}
		flagRow.Add(monoC(name+":", colRegName))
		flagRow.Add(monoC(val+" ", col))
	}
	addFlag("S", 0x80)
	addFlag("Z", 0x40)
	addFlag("H", 0x10)
	addFlag("PV", 0x04)
	addFlag("N", 0x02)
	addFlag("C", 0x01)
	d.regContainer.Add(flagRow)
	d.regContainer.Add(widget.NewSeparator())

	// IFF/IM
	iffCol := colFlagClr
	if d.cpu.IFF1 {
		iffCol = colFlagSet
	}
	d.regContainer.Add(container.NewHBox(
		monoC("IFF ", colRegName),
		monoC(fmt.Sprintf("%v/%v", d.cpu.IFF1, d.cpu.IFF2), iffCol),
		monoC("  IM ", colRegName),
		monoC(fmt.Sprintf("%d", d.cpu.IM), colRegVal),
	))

	if d.cpu.Halted {
		d.regContainer.Add(monoC("  ** HALTED **", colHalted))
	}
}

// --- Hex dump ---

func (d *Debugger) refreshHexDump() {
	d.hexContainer.RemoveAll()

	addr := d.hexAddr
	for row := 0; row < 24; row++ {
		line := container.NewHBox()
		line.Add(monoC(fmt.Sprintf("%04X  ", addr), colAddr))

		asciiStr := ""
		for col := 0; col < 16; col++ {
			b := d.mem.Read(addr)

			hexCol := colHex
			// Highlight current PC address
			if addr == d.cpu.PC {
				hexCol = colPC
			} else if d.breakpoints[addr] {
				hexCol = colBP
			}

			sep := ""
			if col == 8 {
				sep = " "
			}
			line.Add(monoC(sep+fmt.Sprintf("%02X ", b), hexCol))

			if b >= 0x20 && b < 0x7F {
				asciiStr += string(rune(b))
			} else {
				asciiStr += "."
			}
			addr++
		}
		line.Add(monoC(" |"+asciiStr+"|", colASCII))
		d.hexContainer.Add(line)
	}
}

// --- Disassembly ---

func (d *Debugger) refreshDisassembly() {
	d.dasmContainer.RemoveAll()

	pc := d.cpu.PC
	d.dasmAddrEntry.SetText(fmt.Sprintf("%04X", pc))

	lines := Disassemble(d.mem.Read, pc, 30)

	for _, line := range lines {
		row := container.NewHBox()

		isPC := line.Addr == pc
		isBP := d.breakpoints[line.Addr]

		// Prefix marker
		if isPC && isBP {
			row.Add(monoC("*>", colBPHit))
		} else if isPC {
			row.Add(monoC("> ", colPC))
		} else if isBP {
			row.Add(monoC("* ", colBP))
		} else {
			row.Add(monoC("  ", colHex))
		}

		// Address
		addrCol := colAddr
		if isPC {
			addrCol = colPC
		}
		row.Add(monoC(fmt.Sprintf("%04X ", line.Addr), addrCol))

		// Hex bytes
		hexStr := ""
		for _, b := range line.Bytes {
			hexStr += fmt.Sprintf("%02X ", b)
		}
		for len(hexStr) < 12 {
			hexStr += "   "
		}
		row.Add(monoC(hexStr, colHex))

		// Mnemonic
		mnemCol := colMnem
		if strings.HasPrefix(line.Mnem, "DB") {
			mnemCol = colFlagClr
		}
		row.Add(monoC(fmt.Sprintf("%-5s", line.Mnem), mnemCol))

		// Operand
		if line.Operand != "" {
			row.Add(monoC(line.Operand, colOperand))
		}

		d.dasmContainer.Add(row)
	}
}

// --- Status bar ---

func (d *Debugger) refreshStatus() {
	d.statusBar.RemoveAll()

	paused := d.isPaused != nil && d.isPaused()
	if paused {
		d.statusBar.Add(monoC(" PAUSED ", colPaused))
	} else {
		d.statusBar.Add(monoC(" RUNNING ", colRunning))
	}

	d.statusBar.Add(monoC(fmt.Sprintf(
		"  PC:%04X  SP:%04X  AF:%02X%02X  BC:%02X%02X  DE:%02X%02X  HL:%02X%02X  IM:%d  IFF:%v",
		d.cpu.PC, d.cpu.SP,
		d.cpu.A, d.cpu.F, d.cpu.B, d.cpu.C, d.cpu.D, d.cpu.E, d.cpu.H, d.cpu.L,
		d.cpu.IM, d.cpu.IFF1), colStatus))

	if d.cpu.Halted {
		d.statusBar.Add(monoC("  HALTED", colHalted))
	}

	if len(d.breakpoints) > 0 {
		addrs := []string{}
		for addr := range d.breakpoints {
			addrs = append(addrs, fmt.Sprintf("%04X", addr))
		}
		d.statusBar.Add(monoC(fmt.Sprintf("  BPs: %s", strings.Join(addrs, ",")), colBP))
	}
}

// --- Dialogs ---

func (d *Debugger) showEditDialog() {
	entries := make(map[string]*widget.Entry)
	regs := []struct{ name, val string }{
		{"PC", fmt.Sprintf("%04X", d.cpu.PC)},
		{"SP", fmt.Sprintf("%04X", d.cpu.SP)},
		{"IX", fmt.Sprintf("%04X", d.cpu.IX)},
		{"IY", fmt.Sprintf("%04X", d.cpu.IY)},
		{"A", fmt.Sprintf("%02X", d.cpu.A)},
		{"F", fmt.Sprintf("%02X", d.cpu.F)},
		{"B", fmt.Sprintf("%02X", d.cpu.B)},
		{"C", fmt.Sprintf("%02X", d.cpu.C)},
		{"D", fmt.Sprintf("%02X", d.cpu.D)},
		{"E", fmt.Sprintf("%02X", d.cpu.E)},
		{"H", fmt.Sprintf("%02X", d.cpu.H)},
		{"L", fmt.Sprintf("%02X", d.cpu.L)},
		{"I", fmt.Sprintf("%02X", d.cpu.I)},
		{"R", fmt.Sprintf("%02X", d.cpu.R)},
	}

	formItems := []*widget.FormItem{}
	for _, r := range regs {
		e := widget.NewEntry()
		e.SetText(r.val)
		e.TextStyle = fyne.TextStyle{Monospace: true}
		entries[r.name] = e
		formItems = append(formItems, widget.NewFormItem(r.name, e))
	}

	form := widget.NewForm(formItems...)
	content := container.NewVBox(form)
	dlg := widget.NewModalPopUp(content, d.window.Canvas())

	p16 := func(name string) uint16 {
		if v, err := strconv.ParseUint(entries[name].Text, 16, 16); err == nil {
			return uint16(v)
		}
		return 0
	}
	p8 := func(name string) byte {
		if v, err := strconv.ParseUint(entries[name].Text, 16, 8); err == nil {
			return byte(v)
		}
		return 0
	}

	applyBtn := widget.NewButton("Apply", func() {
		d.cpu.PC = p16("PC")
		d.cpu.SP = p16("SP")
		d.cpu.IX = p16("IX")
		d.cpu.IY = p16("IY")
		d.cpu.A = p8("A")
		d.cpu.F = p8("F")
		d.cpu.B = p8("B")
		d.cpu.C = p8("C")
		d.cpu.D = p8("D")
		d.cpu.E = p8("E")
		d.cpu.H = p8("H")
		d.cpu.L = p8("L")
		d.cpu.I = p8("I")
		d.cpu.R = p8("R")
		dlg.Hide()
		d.Refresh()
	})
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	content.Add(container.NewHBox(layout.NewSpacer(), cancelBtn, applyBtn))
	dlg.Resize(fyne.NewSize(300, 500))
	dlg.Show()
}

func (d *Debugger) showWriteMemDialog() {
	addrEntry := widget.NewEntry()
	addrEntry.SetText(fmt.Sprintf("%04X", d.hexAddr))
	addrEntry.TextStyle = fyne.TextStyle{Monospace: true}
	valEntry := widget.NewEntry()
	valEntry.SetPlaceHolder("00 01 FF ...")
	valEntry.TextStyle = fyne.TextStyle{Monospace: true}

	form := widget.NewForm(
		widget.NewFormItem("Address", addrEntry),
		widget.NewFormItem("Hex bytes", valEntry),
	)
	content := container.NewVBox(form)
	dlg := widget.NewModalPopUp(content, d.window.Canvas())

	applyBtn := widget.NewButton("Write", func() {
		addr, err := strconv.ParseUint(addrEntry.Text, 16, 16)
		if err != nil {
			dlg.Hide()
			return
		}
		for _, p := range strings.Fields(valEntry.Text) {
			if v, err := strconv.ParseUint(p, 16, 8); err == nil {
				d.mem.Write(uint16(addr), byte(v))
				addr++
			}
		}
		dlg.Hide()
		d.Refresh()
	})
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	content.Add(container.NewHBox(layout.NewSpacer(), cancelBtn, applyBtn))
	dlg.Resize(fyne.NewSize(400, 200))
	dlg.Show()
}
