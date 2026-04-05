package debugger

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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
	onPause    func()
	onStep     func() // Execute exactly one Z80 instruction (no interrupt check)
	onRun      func()
	isPaused   func() bool

	// State
	hexAddr     uint16
	breakpoints map[uint16]bool

	// Widgets
	regLabels     map[string]*widget.RichText
	flagLabels    map[string]*widget.RichText
	hexText       *widget.RichText
	dasmText      *widget.RichText
	statusLabel   *widget.RichText
	hexAddrEntry  *widget.Entry
	dasmAddrEntry *widget.Entry

	// Live refresh
	refreshTicker *time.Ticker
	stopChan      chan struct{}
}

// New creates a new debugger attached to the given CPU and memory.
func New(cpu *z80.CPU, mem *memory.Memory, app fyne.App) *Debugger {
	d := &Debugger{
		cpu:         cpu,
		mem:         mem,
		hexAddr:     0x4000,
		breakpoints: make(map[uint16]bool),
		regLabels:   make(map[string]*widget.RichText),
		flagLabels:  make(map[string]*widget.RichText),
		stopChan:    make(chan struct{}),
	}

	d.window = app.NewWindow("ZX Spectrum Debugger")
	d.window.Resize(fyne.NewSize(1200, 750))

	content := d.buildUI()
	d.window.SetContent(content)

	d.window.SetOnClosed(func() {
		d.stopRefresh()
	})

	return d
}

// SetCallbacks sets the emulator control callbacks.
func (d *Debugger) SetCallbacks(onPause func(), onStep func(), onRun func(), isPaused func() bool) {
	d.onPause = onPause
	d.onStep = onStep
	d.onRun = onRun
	d.isPaused = isPaused
}

// Show displays the debugger window and pauses the emulator.
func (d *Debugger) Show() {
	if d.onPause != nil {
		d.onPause()
	}
	d.Refresh()
	d.startRefresh()
	d.window.Show()
}

// Refresh updates all debugger panels from CPU/memory state.
func (d *Debugger) Refresh() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.refreshRegisters()
	d.refreshHexDump()
	d.refreshDisassembly()
	d.refreshStatus()
}

func (d *Debugger) startRefresh() {
	d.refreshTicker = time.NewTicker(50 * time.Millisecond) // 20Hz refresh
	go func() {
		for {
			select {
			case <-d.refreshTicker.C:
				fyne.Do(func() {
					d.Refresh()
				})
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

func monoText(s string) *widget.RichText {
	return widget.NewRichText(
		&widget.TextSegment{
			Text:  s,
			Style: widget.RichTextStyle{TextStyle: fyne.TextStyle{Monospace: true}},
		},
	)
}

func (d *Debugger) setMonoText(rt *widget.RichText, s string) {
	rt.Segments = []widget.RichTextSegment{
		&widget.TextSegment{
			Text:  s,
			Style: widget.RichTextStyle{TextStyle: fyne.TextStyle{Monospace: true}},
		},
	}
	rt.Refresh()
}

func (d *Debugger) buildUI() fyne.CanvasObject {
	controls := d.buildControls()
	regPanel := d.buildRegisterPanel()
	hexPanel := d.buildHexPanel()
	dasmPanel := d.buildDisassemblyPanel()

	d.statusLabel = monoText("Paused")

	// Left: registers (narrow), Center: hex dump, Right: disassembly
	leftCol := container.NewVBox(
		widget.NewLabelWithStyle("Registers", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		regPanel,
	)

	centerCol := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Memory", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			d.buildHexAddrBar(),
		),
		nil, nil, nil,
		container.NewVScroll(hexPanel),
	)

	rightCol := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Disassembly", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			d.buildDasmAddrBar(),
		),
		nil, nil, nil,
		container.NewVScroll(dasmPanel),
	)

	// Use fixed proportions: registers ~250px, rest split evenly
	split := container.NewHSplit(
		container.NewHSplit(leftCol, centerCol),
		rightCol,
	)
	split.SetOffset(0.55)

	return container.NewBorder(
		container.NewVBox(controls, widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), d.statusLabel),
		nil, nil,
		split,
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

	// Breakpoint entry
	bpEntry := widget.NewEntry()
	bpEntry.SetPlaceHolder("ADDR")
	bpEntry.TextStyle = fyne.TextStyle{Monospace: true}
	bpBtn := widget.NewButton("Set BP", func() {
		if addr, err := strconv.ParseUint(bpEntry.Text, 16, 16); err == nil {
			d.breakpoints[uint16(addr)] = true
			bpEntry.SetText("")
			d.Refresh()
		}
	})
	clearBPBtn := widget.NewButton("Clear BPs", func() {
		d.breakpoints = make(map[uint16]bool)
		d.Refresh()
	})

	return container.NewHBox(
		pauseBtn, stepBtn, stepFrameBtn, runBtn,
		widget.NewSeparator(),
		goToPCBtn,
		widget.NewSeparator(),
		widget.NewLabel("BP:"), bpEntry, bpBtn, clearBPBtn,
	)
}

func (d *Debugger) buildRegisterPanel() fyne.CanvasObject {
	items := container.NewVBox()

	addReg := func(name string) {
		rt := monoText("0000")
		d.regLabels[name] = rt
		items.Add(container.NewHBox(
			widget.NewLabelWithStyle(fmt.Sprintf("%-3s", name), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true}),
			rt,
		))
	}

	addRegPair := func(n1, n2 string) {
		rt1 := monoText("00")
		rt2 := monoText("00")
		d.regLabels[n1] = rt1
		d.regLabels[n2] = rt2
		items.Add(container.NewHBox(
			widget.NewLabelWithStyle(fmt.Sprintf("%-2s", n1), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true}),
			rt1,
			layout.NewSpacer(),
			widget.NewLabelWithStyle(fmt.Sprintf("%-2s", n2), fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true}),
			rt2,
		))
	}

	addReg("PC")
	addReg("SP")
	addReg("IX")
	addReg("IY")
	items.Add(widget.NewSeparator())
	addRegPair("A", "F")
	addRegPair("B", "C")
	addRegPair("D", "E")
	addRegPair("H", "L")
	items.Add(widget.NewSeparator())
	addRegPair("A'", "F'")
	addRegPair("B'", "C'")
	addRegPair("D'", "E'")
	addRegPair("H'", "L'")
	items.Add(widget.NewSeparator())
	addRegPair("I", "R")
	items.Add(widget.NewSeparator())

	// Flags as a compact row
	flagNames := []string{"S", "Z", "H", "P/V", "N", "C"}
	flagRow := container.NewHBox()
	for _, f := range flagNames {
		rt := monoText("-")
		d.flagLabels[f] = rt
		flagRow.Add(widget.NewLabelWithStyle(f, fyne.TextAlignCenter, fyne.TextStyle{Monospace: true, Bold: true}))
		flagRow.Add(rt)
	}
	items.Add(flagRow)
	items.Add(widget.NewSeparator())

	// IFF/IM/Halted
	d.regLabels["IFF"] = monoText("0/0")
	d.regLabels["IM"] = monoText("0")
	d.regLabels["HALT"] = monoText("-")
	items.Add(container.NewHBox(
		widget.NewLabelWithStyle("IFF", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true}),
		d.regLabels["IFF"],
		layout.NewSpacer(),
		widget.NewLabelWithStyle("IM", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true}),
		d.regLabels["IM"],
		layout.NewSpacer(),
		d.regLabels["HALT"],
	))

	// Edit registers
	items.Add(widget.NewSeparator())
	editBtn := widget.NewButton("Edit Registers...", func() {
		d.showEditDialog()
	})
	items.Add(editBtn)

	return items
}

func (d *Debugger) showEditDialog() {
	pcEntry := widget.NewEntry()
	pcEntry.SetText(fmt.Sprintf("%04X", d.cpu.PC))
	pcEntry.TextStyle = fyne.TextStyle{Monospace: true}
	spEntry := widget.NewEntry()
	spEntry.SetText(fmt.Sprintf("%04X", d.cpu.SP))
	spEntry.TextStyle = fyne.TextStyle{Monospace: true}
	aEntry := widget.NewEntry()
	aEntry.SetText(fmt.Sprintf("%02X", d.cpu.A))
	aEntry.TextStyle = fyne.TextStyle{Monospace: true}
	fEntry := widget.NewEntry()
	fEntry.SetText(fmt.Sprintf("%02X", d.cpu.F))
	fEntry.TextStyle = fyne.TextStyle{Monospace: true}

	form := widget.NewForm(
		widget.NewFormItem("PC", pcEntry),
		widget.NewFormItem("SP", spEntry),
		widget.NewFormItem("A", aEntry),
		widget.NewFormItem("F", fEntry),
	)

	content := container.NewVBox(form)
	dlg := widget.NewModalPopUp(content, d.window.Canvas())

	applyBtn := widget.NewButton("Apply", func() {
		if v, err := strconv.ParseUint(pcEntry.Text, 16, 16); err == nil {
			d.cpu.PC = uint16(v)
		}
		if v, err := strconv.ParseUint(spEntry.Text, 16, 16); err == nil {
			d.cpu.SP = uint16(v)
		}
		if v, err := strconv.ParseUint(aEntry.Text, 16, 8); err == nil {
			d.cpu.A = byte(v)
		}
		if v, err := strconv.ParseUint(fEntry.Text, 16, 8); err == nil {
			d.cpu.F = byte(v)
		}
		dlg.Hide()
		d.Refresh()
	})
	cancelBtn := widget.NewButton("Cancel", func() { dlg.Hide() })
	content.Add(container.NewHBox(layout.NewSpacer(), cancelBtn, applyBtn))
	dlg.Resize(fyne.NewSize(300, 250))
	dlg.Show()
}

func (d *Debugger) buildHexPanel() fyne.CanvasObject {
	d.hexText = monoText("")
	return d.hexText
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

	writeBtn := widget.NewButton("Write...", func() {
		d.showWriteMemDialog()
	})

	return container.NewHBox(
		widget.NewLabel("Addr:"), d.hexAddrEntry,
		widget.NewButton("Go", func() { d.hexAddrEntry.OnSubmitted(d.hexAddrEntry.Text) }),
		writeBtn,
	)
}

func (d *Debugger) showWriteMemDialog() {
	addrEntry := widget.NewEntry()
	addrEntry.SetText(fmt.Sprintf("%04X", d.hexAddr))
	addrEntry.TextStyle = fyne.TextStyle{Monospace: true}
	valEntry := widget.NewEntry()
	valEntry.SetPlaceHolder("00 01 02 ...")
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
		parts := strings.Fields(valEntry.Text)
		for _, p := range parts {
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
	dlg.Resize(fyne.NewSize(400, 180))
	dlg.Show()
}

func (d *Debugger) buildDisassemblyPanel() fyne.CanvasObject {
	d.dasmText = monoText("")
	return d.dasmText
}

func (d *Debugger) buildDasmAddrBar() fyne.CanvasObject {
	d.dasmAddrEntry = widget.NewEntry()
	d.dasmAddrEntry.TextStyle = fyne.TextStyle{Monospace: true}
	d.dasmAddrEntry.OnSubmitted = func(s string) {
		d.Refresh()
	}

	return container.NewHBox(
		widget.NewLabel("From PC:"), d.dasmAddrEntry,
	)
}

func (d *Debugger) refreshRegisters() {
	set := func(name, val string) {
		if rt, ok := d.regLabels[name]; ok {
			d.setMonoText(rt, val)
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
	if d.cpu.IFF1 && d.cpu.IFF2 {
		iff = "1/1"
	} else if d.cpu.IFF1 {
		iff = "1/0"
	} else if d.cpu.IFF2 {
		iff = "0/1"
	}
	set("IFF", iff)
	set("IM", fmt.Sprintf("%d", d.cpu.IM))
	if d.cpu.Halted {
		set("HALT", "HALTED")
	} else {
		set("HALT", "")
	}

	// Flags
	f := d.cpu.F
	setFlag := func(name string, bit byte) {
		if rt, ok := d.flagLabels[name]; ok {
			if f&bit != 0 {
				d.setMonoText(rt, "1")
			} else {
				d.setMonoText(rt, "0")
			}
		}
	}
	setFlag("S", 0x80)
	setFlag("Z", 0x40)
	setFlag("H", 0x10)
	setFlag("P/V", 0x04)
	setFlag("N", 0x02)
	setFlag("C", 0x01)
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
	d.setMonoText(d.hexText, sb.String())
}

func (d *Debugger) refreshDisassembly() {
	pc := d.cpu.PC
	d.dasmAddrEntry.SetText(fmt.Sprintf("%04X", pc))

	lines := Disassemble(d.mem.Read, pc, 40)

	var sb strings.Builder
	for _, line := range lines {
		prefix := "  "
		if line.Addr == pc {
			prefix = "> "
		}
		if d.breakpoints[line.Addr] {
			prefix = "* "
		}
		if line.Addr == pc && d.breakpoints[line.Addr] {
			prefix = "*>"
		}
		sb.WriteString(prefix + line.String() + "\n")
	}
	d.setMonoText(d.dasmText, sb.String())
}

func (d *Debugger) refreshStatus() {
	paused := "Running"
	if d.isPaused != nil && d.isPaused() {
		paused = "Paused"
	}
	halted := ""
	if d.cpu.Halted {
		halted = " [HALTED]"
	}
	bpStr := ""
	if len(d.breakpoints) > 0 {
		addrs := []string{}
		for addr := range d.breakpoints {
			addrs = append(addrs, fmt.Sprintf("%04X", addr))
		}
		bpStr = fmt.Sprintf("  BPs: %s", strings.Join(addrs, ","))
	}

	d.setMonoText(d.statusLabel, fmt.Sprintf(
		"%s  PC=%04X  SP=%04X  AF=%02X%02X  BC=%02X%02X  DE=%02X%02X  HL=%02X%02X  IM=%d  IFF=%v%s%s",
		paused, d.cpu.PC, d.cpu.SP,
		d.cpu.A, d.cpu.F, d.cpu.B, d.cpu.C, d.cpu.D, d.cpu.E, d.cpu.H, d.cpu.L,
		d.cpu.IM, d.cpu.IFF1, halted, bpStr))
}

// CheckBreakpoint returns true if the CPU PC is at a breakpoint.
func (d *Debugger) CheckBreakpoint() bool {
	return d.breakpoints[d.cpu.PC]
}
