package debugger

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Debugger provides a debugging window for the ZX Spectrum emulator.
type Debugger struct {
	cpu *z80.CPU
	mem *memory.Memory

	window fyne.Window
	mu     sync.Mutex

	// Emulator control callbacks
	onPause func()
	onStep  func()
	onRun   func()

	// State
	paused   bool
	hexAddr  uint16
	dasmAddr uint16

	// Widgets that need updating
	regEntries    map[string]*widget.Entry
	flagLabels    map[string]*widget.Check
	hexGrid       *widget.TextGrid
	dasmList      *widget.List
	dasmLines     []DisassembledLine
	statusLabel   *widget.Label
	hexAddrEntry  *widget.Entry
	dasmAddrEntry *widget.Entry
}

// New creates a new debugger attached to the given CPU and memory.
func New(cpu *z80.CPU, mem *memory.Memory, app fyne.App) *Debugger {
	d := &Debugger{
		cpu:        cpu,
		mem:        mem,
		regEntries: make(map[string]*widget.Entry),
		flagLabels: make(map[string]*widget.Check),
		hexAddr:    0x4000,
	}

	d.window = app.NewWindow("ZX Spectrum Debugger")
	d.window.Resize(fyne.NewSize(1100, 700))

	content := d.buildUI()
	d.window.SetContent(content)

	return d
}

// SetCallbacks sets the emulator control callbacks.
func (d *Debugger) SetCallbacks(onPause, onStep, onRun func()) {
	d.onPause = onPause
	d.onStep = onStep
	d.onRun = onRun
}

// Show displays the debugger window and pauses the emulator.
func (d *Debugger) Show() {
	d.paused = true
	if d.onPause != nil {
		d.onPause()
	}
	d.Refresh()
	d.window.Show()
}

// Refresh updates all debugger panels from CPU/memory state.
func (d *Debugger) Refresh() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.refreshRegisters()
	d.refreshFlags()
	d.refreshHexDump()
	d.refreshDisassembly()
	d.refreshStatus()
}

func (d *Debugger) buildUI() fyne.CanvasObject {
	// Control buttons
	controls := d.buildControls()

	// Register panel
	regPanel := d.buildRegisterPanel()

	// Hex dump panel
	hexPanel := d.buildHexPanel()

	// Disassembly panel
	dasmPanel := d.buildDisassemblyPanel()

	// Status bar
	d.statusLabel = widget.NewLabel("Paused")
	d.statusLabel.TextStyle = fyne.TextStyle{Monospace: true}

	// Layout: top controls, then left=regs, center=hex, right=disasm
	leftPanel := container.NewVBox(
		widget.NewLabel("Registers"),
		widget.NewSeparator(),
		regPanel,
	)

	centerPanel := container.NewVBox(
		widget.NewLabel("Memory"),
		widget.NewSeparator(),
		hexPanel,
	)

	rightPanel := container.NewVBox(
		widget.NewLabel("Disassembly"),
		widget.NewSeparator(),
		dasmPanel,
	)

	mainArea := container.NewHSplit(
		container.NewHSplit(leftPanel, centerPanel),
		rightPanel,
	)
	mainArea.SetOffset(0.55)

	return container.NewBorder(
		container.NewVBox(controls, widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), d.statusLabel),
		nil, nil,
		mainArea,
	)
}

func (d *Debugger) buildControls() fyne.CanvasObject {
	pauseBtn := widget.NewButtonWithIcon("Pause", theme.MediaPauseIcon(), func() {
		d.paused = true
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
		d.paused = false
		if d.onRun != nil {
			d.onRun()
		}
		d.statusLabel.SetText("Running")
	})

	stepFrameBtn := widget.NewButton("Step Frame", func() {
		// Execute one full frame
		d.cpu.ExecuteFrame(69888)
		d.Refresh()
	})

	goToPCBtn := widget.NewButton("Go to PC", func() {
		d.hexAddr = d.cpu.PC
		d.dasmAddr = d.cpu.PC
		d.hexAddrEntry.SetText(fmt.Sprintf("%04X", d.hexAddr))
		d.dasmAddrEntry.SetText(fmt.Sprintf("%04X", d.dasmAddr))
		d.Refresh()
	})

	return container.NewHBox(
		pauseBtn, stepBtn, stepFrameBtn, runBtn,
		layout.NewSpacer(),
		goToPCBtn,
	)
}

func (d *Debugger) buildRegisterPanel() fyne.CanvasObject {
	regs16 := []string{"PC", "SP", "IX", "IY"}
	regs8pair := [][2]string{{"A", "F"}, {"B", "C"}, {"D", "E"}, {"H", "L"}}
	regs8pairAlt := [][2]string{{"A'", "F'"}, {"B'", "C'"}, {"D'", "E'"}, {"H'", "L'"}}
	specialRegs := []string{"I", "R"}

	items := container.NewVBox()

	// 16-bit registers
	for _, name := range regs16 {
		entry := widget.NewEntry()
		entry.TextStyle = fyne.TextStyle{Monospace: true}
		d.regEntries[name] = entry
		row := container.NewHBox(
			widget.NewLabelWithStyle(fmt.Sprintf("%-3s", name), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
			entry,
		)
		items.Add(row)
	}

	items.Add(widget.NewSeparator())

	// 8-bit register pairs
	for _, pair := range regs8pair {
		e1 := widget.NewEntry()
		e1.TextStyle = fyne.TextStyle{Monospace: true}
		e2 := widget.NewEntry()
		e2.TextStyle = fyne.TextStyle{Monospace: true}
		d.regEntries[pair[0]] = e1
		d.regEntries[pair[1]] = e2
		row := container.NewHBox(
			widget.NewLabelWithStyle(pair[0], fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
			e1,
			widget.NewLabelWithStyle(pair[1], fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
			e2,
		)
		items.Add(row)
	}

	items.Add(widget.NewSeparator())

	// Alternate registers
	for _, pair := range regs8pairAlt {
		e1 := widget.NewEntry()
		e1.TextStyle = fyne.TextStyle{Monospace: true}
		e2 := widget.NewEntry()
		e2.TextStyle = fyne.TextStyle{Monospace: true}
		d.regEntries[pair[0]] = e1
		d.regEntries[pair[1]] = e2
		row := container.NewHBox(
			widget.NewLabelWithStyle(fmt.Sprintf("%-2s", pair[0]), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
			e1,
			widget.NewLabelWithStyle(fmt.Sprintf("%-2s", pair[1]), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
			e2,
		)
		items.Add(row)
	}

	items.Add(widget.NewSeparator())

	// Special registers
	for _, name := range specialRegs {
		entry := widget.NewEntry()
		entry.TextStyle = fyne.TextStyle{Monospace: true}
		d.regEntries[name] = entry
		row := container.NewHBox(
			widget.NewLabelWithStyle(fmt.Sprintf("%-3s", name), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
			entry,
		)
		items.Add(row)
	}

	items.Add(widget.NewSeparator())

	// Flags
	flags := []string{"S", "Z", "H", "P/V", "N", "C"}
	flagRow := container.NewHBox()
	for _, f := range flags {
		chk := widget.NewCheck(f, nil)
		d.flagLabels[f] = chk
		flagRow.Add(chk)
	}
	items.Add(widget.NewLabel("Flags"))
	items.Add(flagRow)

	// IFF/IM
	iffEntry := widget.NewEntry()
	iffEntry.TextStyle = fyne.TextStyle{Monospace: true}
	d.regEntries["IFF"] = iffEntry
	imEntry := widget.NewEntry()
	imEntry.TextStyle = fyne.TextStyle{Monospace: true}
	d.regEntries["IM"] = imEntry
	items.Add(container.NewHBox(
		widget.NewLabelWithStyle("IFF", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
		iffEntry,
		widget.NewLabelWithStyle("IM", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true}),
		imEntry,
	))

	// Apply button
	applyBtn := widget.NewButton("Apply Registers", func() {
		d.applyRegisters()
	})
	items.Add(applyBtn)

	return container.NewVScroll(items)
}

func (d *Debugger) buildHexPanel() fyne.CanvasObject {
	d.hexAddrEntry = widget.NewEntry()
	d.hexAddrEntry.SetText(fmt.Sprintf("%04X", d.hexAddr))
	d.hexAddrEntry.TextStyle = fyne.TextStyle{Monospace: true}
	d.hexAddrEntry.OnSubmitted = func(s string) {
		if addr, err := strconv.ParseUint(s, 16, 16); err == nil {
			d.hexAddr = uint16(addr) & 0xFFF0 // Align to 16
			d.refreshHexDump()
		}
	}

	d.hexGrid = widget.NewTextGrid()

	addrBar := container.NewHBox(
		widget.NewLabel("Addr:"),
		d.hexAddrEntry,
		widget.NewButton("Go", func() {
			d.hexAddrEntry.OnSubmitted(d.hexAddrEntry.Text)
		}),
	)

	return container.NewBorder(addrBar, nil, nil, nil,
		container.NewVScroll(d.hexGrid),
	)
}

func (d *Debugger) buildDisassemblyPanel() fyne.CanvasObject {
	d.dasmAddrEntry = widget.NewEntry()
	d.dasmAddrEntry.TextStyle = fyne.TextStyle{Monospace: true}

	d.dasmList = widget.NewList(
		func() int { return len(d.dasmLines) },
		func() fyne.CanvasObject {
			return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			label := o.(*widget.Label)
			if i < len(d.dasmLines) {
				line := d.dasmLines[i]
				prefix := "  "
				if line.Addr == d.cpu.PC {
					prefix = "> "
				}
				label.SetText(prefix + line.String())
			}
		},
	)

	addrBar := container.NewHBox(
		widget.NewLabel("Addr:"),
		d.dasmAddrEntry,
		widget.NewButton("Go", func() {
			s := d.dasmAddrEntry.Text
			if addr, err := strconv.ParseUint(s, 16, 16); err == nil {
				d.dasmAddr = uint16(addr)
				d.refreshDisassembly()
			}
		}),
	)

	return container.NewBorder(addrBar, nil, nil, nil, d.dasmList)
}

func (d *Debugger) refreshRegisters() {
	set := func(name, val string) {
		if e, ok := d.regEntries[name]; ok {
			e.SetText(val)
		}
	}

	set("PC", fmt.Sprintf("%04X", d.cpu.PC))
	set("SP", fmt.Sprintf("%04X", d.cpu.SP))
	set("IX", fmt.Sprintf("%04X", d.cpu.IX))
	set("IY", fmt.Sprintf("%04X", d.cpu.IY))
	set("A", fmt.Sprintf("%02X", d.cpu.A))
	set("F", fmt.Sprintf("%02X", d.cpu.F))
	set("B", fmt.Sprintf("%02X", d.cpu.B))
	set("C", fmt.Sprintf("%02X", d.cpu.C))
	set("D", fmt.Sprintf("%02X", d.cpu.D))
	set("E", fmt.Sprintf("%02X", d.cpu.E))
	set("H", fmt.Sprintf("%02X", d.cpu.H))
	set("L", fmt.Sprintf("%02X", d.cpu.L))
	set("A'", fmt.Sprintf("%02X", d.cpu.A_))
	set("F'", fmt.Sprintf("%02X", d.cpu.F_))
	set("B'", fmt.Sprintf("%02X", d.cpu.B_))
	set("C'", fmt.Sprintf("%02X", d.cpu.C_))
	set("D'", fmt.Sprintf("%02X", d.cpu.D_))
	set("E'", fmt.Sprintf("%02X", d.cpu.E_))
	set("H'", fmt.Sprintf("%02X", d.cpu.H_))
	set("L'", fmt.Sprintf("%02X", d.cpu.L_))
	set("I", fmt.Sprintf("%02X", d.cpu.I))
	set("R", fmt.Sprintf("%02X", d.cpu.R))

	iff := "0/0"
	if d.cpu.IFF1 {
		iff = "1/"
	} else {
		iff = "0/"
	}
	if d.cpu.IFF2 {
		iff += "1"
	} else {
		iff += "0"
	}
	set("IFF", iff)
	set("IM", fmt.Sprintf("%d", d.cpu.IM))
}

func (d *Debugger) refreshFlags() {
	f := d.cpu.F
	if chk, ok := d.flagLabels["S"]; ok {
		chk.SetChecked(f&0x80 != 0)
	}
	if chk, ok := d.flagLabels["Z"]; ok {
		chk.SetChecked(f&0x40 != 0)
	}
	if chk, ok := d.flagLabels["H"]; ok {
		chk.SetChecked(f&0x10 != 0)
	}
	if chk, ok := d.flagLabels["P/V"]; ok {
		chk.SetChecked(f&0x04 != 0)
	}
	if chk, ok := d.flagLabels["N"]; ok {
		chk.SetChecked(f&0x02 != 0)
	}
	if chk, ok := d.flagLabels["C"]; ok {
		chk.SetChecked(f&0x01 != 0)
	}
}

func (d *Debugger) refreshHexDump() {
	var sb strings.Builder
	addr := d.hexAddr
	for row := 0; row < 32; row++ {
		sb.WriteString(fmt.Sprintf("%04X  ", addr))
		ascii := ""
		for col := 0; col < 16; col++ {
			b := d.mem.Read(addr)
			sb.WriteString(fmt.Sprintf("%02X ", b))
			if col == 7 {
				sb.WriteString(" ")
			}
			if b >= 0x20 && b < 0x7F {
				ascii += string(rune(b))
			} else {
				ascii += "."
			}
			addr++
		}
		sb.WriteString(" |" + ascii + "|\n")
	}
	d.hexGrid.SetText(sb.String())
}

func (d *Debugger) refreshDisassembly() {
	d.dasmAddr = d.cpu.PC
	d.dasmAddrEntry.SetText(fmt.Sprintf("%04X", d.dasmAddr))
	d.dasmLines = Disassemble(d.mem.Read, d.dasmAddr, 40)
	d.dasmList.Refresh()
}

func (d *Debugger) refreshStatus() {
	halted := ""
	if d.cpu.Halted {
		halted = " [HALTED]"
	}
	d.statusLabel.SetText(fmt.Sprintf("Paused  PC=%04X  SP=%04X  IM=%d  IFF1=%v%s",
		d.cpu.PC, d.cpu.SP, d.cpu.IM, d.cpu.IFF1, halted))
}

func (d *Debugger) applyRegisters() {
	parse16 := func(name string) uint16 {
		if e, ok := d.regEntries[name]; ok {
			if v, err := strconv.ParseUint(e.Text, 16, 16); err == nil {
				return uint16(v)
			}
		}
		return 0
	}
	parse8 := func(name string) byte {
		if e, ok := d.regEntries[name]; ok {
			if v, err := strconv.ParseUint(e.Text, 16, 8); err == nil {
				return byte(v)
			}
		}
		return 0
	}

	d.cpu.PC = parse16("PC")
	d.cpu.SP = parse16("SP")
	d.cpu.IX = parse16("IX")
	d.cpu.IY = parse16("IY")
	d.cpu.A = parse8("A")
	d.cpu.F = parse8("F")
	d.cpu.B = parse8("B")
	d.cpu.C = parse8("C")
	d.cpu.D = parse8("D")
	d.cpu.E = parse8("E")
	d.cpu.H = parse8("H")
	d.cpu.L = parse8("L")
	d.cpu.A_ = parse8("A'")
	d.cpu.F_ = parse8("F'")
	d.cpu.B_ = parse8("B'")
	d.cpu.C_ = parse8("C'")
	d.cpu.D_ = parse8("D'")
	d.cpu.E_ = parse8("E'")
	d.cpu.H_ = parse8("H'")
	d.cpu.L_ = parse8("L'")
	d.cpu.I = parse8("I")
	d.cpu.R = parse8("R")

	d.Refresh()
}
