package debugger

import (
	"fmt"
	"image"
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

// NextProvider exposes Spectrum Next state the debugger should
// show alongside CPU registers. The cmd/zx_go GUI wires this in
// when ModelNext is active; classic emulators leave it nil and
// the Next panel stays hidden.
type NextProvider interface {
	// MMUSlots returns the bank index visible in each of the 8K
	// MMU slots. The Z80 has 8 slots of 8 KB; -1 marks "ROM" or
	// "unmapped".
	MMUSlots() [8]int
	// DivMMCState returns a one-line summary of the divMMC pager
	// state: paged-in, MAPRAM, selected bank, automap on/off.
	DivMMCState() string
	// NextRegs returns a snapshot of NextReg values for the
	// registers listed in NextRegsOfInterest(). The map is keyed
	// by NextReg number.
	NextRegs() map[uint8]byte
}

// PaletteRGBAProvider is an OPTIONAL companion to NextProvider: a
// provider that also implements it feeds the graphical Palette tab.
// SetNextProvider type-asserts for it, so providers that don't supply
// palette colours simply leave the swatch all-black (non-breaking).
type PaletteRGBAProvider interface {
	// PaletteRGBA returns the 256 active palette entries as
	// fully-expanded 8-bit-per-channel RGBA.
	PaletteRGBA() [256]color.RGBA
}

// Layer2FrameProvider is an OPTIONAL companion that feeds the Layer-2
// framebuffer viewer: the live Layer-2 image plus a status string
// (e.g. "256x192" or "disabled"). img may be nil when unavailable.
type Layer2FrameProvider interface {
	Layer2Frame() (img image.Image, status string)
}

// TilemapFrameProvider is an OPTIONAL companion that feeds the tilemap
// viewer: the rendered tilemap image plus a status string.
type TilemapFrameProvider interface {
	TilemapFrame() (img image.Image, status string)
}

// NextRegsOfInterest is the fixed set of NextReg numbers the
// debugger panel displays. Picked for boot-debugging value:
//
//	0x00 machine ID
//	0x01 version
//	0x02 reset reason / state
//	0x07 turbo control
//	0x08 peripheral 1
//	0x09 peripheral 4
//	0x0A peripheral 5
//	0x14 transparency colour
//	0x15 sprite & Layers system
//	0x18 Layer 2 control
//	0x19 sprite control
//	0x43 palette control
//	0x50..0x57 MMU slots
//	0x69 Layer 2 + ULA enables
//	0x6B tilemap control
//	0x80..0x87 internal port decode flags
//	0xB8 divMMC enable / automap mask
var NextRegsOfInterest = []uint8{
	0x00, 0x01, 0x02, 0x07, 0x08, 0x09, 0x0A, 0x14, 0x15, 0x18, 0x19,
	0x43, 0x50, 0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57,
	0x69, 0x6B, 0x80, 0x82, 0x83, 0x84, 0x85, 0xB8,
}

type Debugger struct {
	cpu *z80.CPU
	mem *memory.Memory

	window fyne.Window
	mu     sync.Mutex

	onPause    func()
	onStep     func()
	onStepOver func() // optional; falls back to onStep when nil
	onRun      func()
	isPaused   func() bool

	hexAddr     uint16
	hexAddrBase int // 16, 10, or 8
	// bps is the SHARED breakpoint store. The emulator hands the
	// same *BreakpointSet to both this visual debugger and the
	// telnet debugger, so a breakpoint set on one surface shows up
	// (and fires) on the other. When the GUI is used standalone
	// (no emulator-provided set), New() allocates a private one.
	bps *BreakpointSet

	// regWatches is the SHARED register-watchpoint set (same
	// instance the telnet debugger uses). CheckBreakpoint consults
	// it so a register watch added from either surface halts the
	// CPU; the Watchpoints tab lists/edits it. Nil until
	// SetRegWatches wires the shared set.
	regWatches *RegWatchSet

	// Register display
	regLines [20]*canvas.Text
	flagLine *canvas.Text
	iffLine  *canvas.Text
	irqLine  *canvas.Text
	haltLine *canvas.Text

	// Lists for scrollable, tappable content
	dasmList  *widget.List
	dasmCache []DisassembledLine
	hexList   *widget.List

	statusTxt    *canvas.Text
	hexAddrEntry *widget.Entry

	refreshTicker *time.Ticker
	stopChan      chan struct{}
	stopOnce      sync.Once

	// Spectrum Next state (optional). When non-nil, the
	// debugger renders the Next panel and refreshes it each tick.
	nextProvider NextProvider
	nextPanelBox *fyne.Container
	nextLines    map[string]*canvas.Text

	// paletteView is the graphical 16×16 active-palette swatch tab.
	// Fed from a NextProvider that also implements PaletteRGBAProvider.
	paletteView *PaletteView

	// spriteView is the graphical sprite-pattern viewer tab. Fed from
	// a NextProvider that also implements SpriteVizProvider.
	spriteView *SpriteView

	// layer2View / tilemapView are the graphical framebuffer viewers,
	// fed from Layer2FrameProvider / TilemapFrameProvider.
	layer2View  *ImageView
	tilemapView *ImageView

	// pageMap is the graphical 4-cell (classic) / 8-cell (Next)
	// memory-paging diagram. Constructed at debugger build time;
	// SetNextProvider toggles it between classic and Next layout.
	pageMap *PageMapWidget

	// bankInspect is the physical-bank inspector. Hits the same
	// BankAccessor backend as the telnet bank-peek / bank-poke
	// commands. Always present in the UI; controls report "no
	// accessor" until SetBankAccessor is called.
	bankInspect *BankInspectWidget

	// backtrace is the stack-walk widget — same backend as the
	// telnet `backtrace` command. Refresh either on demand or
	// from the periodic debugger refresh tick.
	backtrace *BacktraceWidget

	// history is the M1-fetch ring viewer. Same backend as the
	// telnet `history` / `prev` commands; backed by a *History
	// that is wired separately via SetHistory.
	history *HistoryWidget

	// nextRegW is the arbitrary NextReg read/write panel. Same
	// backend as the telnet `nextreg-read` / `nextreg-function
	// commands; wired via SetNextRegAccessor.
	nextRegW *NextRegWidget

	// bpW is the conditional/bank-filtered breakpoints panel. It
	// reads/writes the same shared d.bps store that CheckBreakpoint
	// and the telnet debugger consult, so disassembly-tap, panel-add
	// and telnet `set-breakpoint` all round-trip.
	bpW *BreakpointsWidget

	// watchW is the register-watchpoints panel, backed by the shared
	// regWatches set (telnet `watch-reg` parity).
	watchW *WatchpointsWidget

	// heatmapW renders hot-PC / call / ret / rst analyses over the
	// shared M1-history ring (telnet `hot`/`callgraph`/… parity).
	heatmapW *HeatmapWidget

	// ttW drives the shared time-travel snapshot ring (telnet
	// `tt-*` parity), wired via SetTimeTravel.
	ttW *TimeTravelWidget
}

func New(cpu *z80.CPU, mem *memory.Memory, app fyne.App) *Debugger {
	return NewWithBreakpoints(cpu, mem, app, nil)
}

// NewWithBreakpoints is New with an externally-supplied shared
// breakpoint set (pass the emulator's so the telnet and visual
// debuggers share one store). A nil bps allocates a private set.
func NewWithBreakpoints(cpu *z80.CPU, mem *memory.Memory, app fyne.App, bps *BreakpointSet) *Debugger {
	if bps == nil {
		bps = NewBreakpointSet()
	}
	d := &Debugger{
		cpu:         cpu,
		mem:         mem,
		hexAddr:     0x0000,
		hexAddrBase: 16,
		bps:         bps,
		stopChan:    make(chan struct{}),
	}
	d.window = app.NewWindow("ZX Spectrum Debugger")
	// Default size fits a standard laptop display (1366×768) with
	// room for window chrome; the panel min-sizes below are kept
	// small enough that the user can shrink it further and drag the
	// split dividers freely.
	d.window.Resize(fyne.NewSize(1180, 760))
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

// SetStepOver wires the "Step Over" toolbar button: run past a
// CALL/RST/PUSH-NN to its return, or single-step otherwise. Pass
// nil to hide nothing — the button falls back to onStep.
func (d *Debugger) SetStepOver(fn func()) { d.onStepOver = fn }

// SetTimeTravel wires the Time-Travel tab to the shared snapshot
// ring controller (cmd/zx_go provides one over the emulator-owned
// buffer, so the GUI and telnet tt-* commands share it).
func (d *Debugger) SetTimeTravel(c TimeTravelController) {
	if d.ttW != nil {
		d.ttW.SetController(c)
	}
}

// SetNextRegAccessor wires the NextReg read/write panel to a
// backend (typically the nextregs dispatcher). Pass nil on
// classic-model emulators so the panel reports "no accessor".
func (d *Debugger) SetNextRegAccessor(a NextRegAccessor) {
	if d.nextRegW == nil {
		return
	}
	d.nextRegW.SetAccessor(a)
}

// SetHistory wires the M1-fetch ring buffer to the History tab.
// Pass nil to disconnect (the tab then reports "history disabled").
// Typically called from cmd/zx_go with the same *History that
// backs the telnet `history` / `prev` commands.
func (d *Debugger) SetHistory(h *History) {
	if d.history != nil {
		d.history.SetHistory(h)
	}
	if d.heatmapW != nil {
		d.heatmapW.SetHistory(h)
	}
}

// SetBankAccessor wires the visual bank-inspector widget to a
// physical-bank backend. Same interface the telnet bank-peek /
// bank-poke commands hit, so visual + telnet share semantics. Pass
// nil to clear (the widget then reports "no accessor" on actions).
// Optionally pass extraKinds to extend the kind drop-down beyond
// the canonical ram/rom/altrom/divmmc-ram set.
func (d *Debugger) SetBankAccessor(a BankAccessor, extraKinds ...string) {
	if d.bankInspect == nil {
		return
	}
	d.bankInspect.SetAccessor(a)
	if len(extraKinds) > 0 {
		merged := append([]string{}, d.bankInspect.kinds...)
		merged = append(merged, extraKinds...)
		d.bankInspect.SetKinds(merged)
	}
}

// SetNextProvider attaches a Spectrum Next state source. When set,
// the debugger renders an extra panel showing MMU slot map,
// divMMC state, and selected NextReg values, and switches the
// memory-paging diagram to 8-slot Next mode. Pass nil to detach
// (e.g. when switching back to a classic model).
func (d *Debugger) SetNextProvider(p NextProvider) {
	d.nextProvider = p
	if d.nextPanelBox != nil {
		d.refreshNextPanel()
	}
	if d.pageMap != nil {
		d.pageMap.SetNextProvider(p)
	}
	// Feed the graphical palette tab if the provider supplies colours.
	if d.paletteView != nil {
		if pp, ok := p.(PaletteRGBAProvider); ok && pp != nil {
			d.paletteView.SetProvider(pp.PaletteRGBA)
		} else {
			d.paletteView.SetProvider(nil)
		}
	}
	// Feed the graphical sprite tab if the provider supplies sprites.
	if d.spriteView != nil {
		if sp, ok := p.(SpriteVizProvider); ok && sp != nil {
			d.spriteView.SetProvider(sp.VisibleSprites)
		} else {
			d.spriteView.SetProvider(nil)
		}
	}
	// Feed the Layer-2 framebuffer viewer.
	if d.layer2View != nil {
		if lp, ok := p.(Layer2FrameProvider); ok && lp != nil {
			d.layer2View.SetRender(lp.Layer2Frame)
		} else {
			d.layer2View.SetRender(nil)
		}
	}
	// Feed the tilemap viewer.
	if d.tilemapView != nil {
		if tp, ok := p.(TilemapFrameProvider); ok && tp != nil {
			d.tilemapView.SetRender(tp.TilemapFrame)
		} else {
			d.tilemapView.SetRender(nil)
		}
	}
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
	d.refreshNextPanel()
	if d.pageMap != nil {
		d.pageMap.Refresh()
	}
	if d.paletteView != nil {
		d.paletteView.Refresh()
	}
	if d.spriteView != nil {
		d.spriteView.Refresh()
	}
	if d.layer2View != nil {
		d.layer2View.Refresh()
	}
	if d.tilemapView != nil {
		d.tilemapView.Refresh()
	}
	if d.bpW != nil {
		d.bpW.Refresh()
	}
	if d.watchW != nil {
		d.watchW.Refresh()
	}
	if d.ttW != nil {
		d.ttW.Refresh()
	}
}

// CheckBreakpoint fires for the visual debugger's bp set. Matches
// the telnet evaluator: optional ROM-bank filter + optional guard
// condition. Returns true to halt; false to keep running.
func (d *Debugger) CheckBreakpoint() bool {
	entry, ok := d.bps.Lookup(d.cpu.PC)
	if !ok {
		return false
	}
	if entry.Bank >= 0 {
		bank := d.cpuBank()
		if entry.Bank != bank {
			return false
		}
	}
	if entry.HasCond {
		if !entry.Cond.Eval(visualCPUState{cpu: d.cpu, mem: d.mem, bank: byte(d.cpuBank())}) {
			return false
		}
	}
	return true
}

// CheckWatchpoints reports whether any shared register watch fired
// at the current CPU state. Called from the GUI run loop alongside
// CheckBreakpoint so register watches added from either surface
// halt the CPU even when only the GUI is driving. No-op (false)
// when no watch set is wired or it's empty.
func (d *Debugger) CheckWatchpoints() bool {
	if d.regWatches == nil || d.regWatches.Empty() {
		return false
	}
	return d.regWatches.Check(visualCPUState{cpu: d.cpu, mem: d.mem, bank: byte(d.cpuBank())})
}

// SetRegWatches wires the shared register-watchpoint set (call with
// the emulator's so telnet and GUI share one). Safe to call once
// after construction, before the window is shown.
func (d *Debugger) SetRegWatches(s *RegWatchSet) {
	d.regWatches = s
	if d.watchW != nil {
		d.watchW.SetStore(s)
	}
}

// cpuBank returns the 16K ROM bank currently mapped at $0000 on
// 128K-class machines. Mirrors cmd/zx_go/debugger.go's lookup so
// the cond.bank reference resolves the same way in both surfaces.
func (d *Debugger) cpuBank() int {
	port7FFD, port1FFD, _ := d.mem.GetPortState()
	return int((port7FFD>>4)&1) | int((port1FFD>>1)&2)
}

// visualCPUState satisfies the condition.State interface inside
// pkg/debugger so condition-evaluation paths can be exercised
// without the cmd/zx_go cpuState wrapper.
type visualCPUState struct {
	cpu  *z80.CPU
	mem  *memory.Memory
	bank byte
}

func (s visualCPUState) Reg(name string) (int64, bool) {
	c := s.cpu
	switch name {
	case "pc":
		return int64(c.PC), true
	case "sp":
		return int64(c.SP), true
	case "a":
		return int64(c.A), true
	case "f":
		return int64(c.F), true
	case "b":
		return int64(c.B), true
	case "c":
		return int64(c.C), true
	case "d":
		return int64(c.D), true
	case "e":
		return int64(c.E), true
	case "h":
		return int64(c.H), true
	case "l":
		return int64(c.L), true
	case "ix":
		return int64(c.IX), true
	case "iy":
		return int64(c.IY), true
	case "iff1":
		if c.IFF1 {
			return 1, true
		}
		return 0, true
	case "iff2":
		if c.IFF2 {
			return 1, true
		}
		return 0, true
	case "im":
		return int64(c.IM), true
	case "halted":
		if c.Halted {
			return 1, true
		}
		return 0, true
	case "bank":
		return int64(s.bank), true
	}
	return 0, false
}

func (s visualCPUState) ReadMem(addr uint16) byte { return s.mem.Read(addr) }

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
	// Close rather than send: the refresh goroutine isn't always
	// parked on the receive (it can be off running fyne.Do), so a
	// single best-effort send could be missed and leak the
	// goroutine forever. Closing wakes it unconditionally, whenever
	// it next reaches the select. sync.Once guards repeat calls
	// (e.g. the window's OnClosed firing more than once).
	d.stopOnce.Do(func() { close(d.stopChan) })
}

// cpuStackSource adapts a *z80.CPU to the StackSource interface
// consumed by BacktraceWidget. Kept local because both this file
// and BacktraceWidget live in the debugger package; pulls a single
// pkg/z80 import in for the type.
type cpuStackSource struct{ cpu *z80.CPU }

func (s cpuStackSource) SP() uint16   { return s.cpu.SP }
func (s cpuStackSource) IFF1() bool   { return s.cpu.IFF1 }
func (s cpuStackSource) IM() int      { return int(s.cpu.IM) }
func (s cpuStackSource) Halted() bool { return s.cpu.Halted }

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
	d.irqLine = mkText(colRegVal)
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
	regBox.Add(d.irqLine)
	regBox.Add(d.haltLine)
	regScroll := container.NewVScroll(regBox)
	regScroll.SetMinSize(fyne.NewSize(150, 0))
	regPanel := panelBG(container.NewBorder(
		container.NewVBox(headerText("  REGISTERS", colRegName), widget.NewSeparator()),
		nil, nil, nil, regScroll,
	))

	// Disassembly panel (centre — widget.List for scrolling + tapping)
	d.dasmCache = Disassemble(d.mem.Read, d.cpu.PC, dasmRows)
	d.dasmList = widget.NewList(
		func() int { return dasmRows },
		func() fyne.CanvasObject {
			t := canvas.NewText(strings.Repeat(" ", 44), colMnem)
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
			_, isBP := d.bps.Lookup(line.Addr)

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
		// Toggle a plain (no-cond, any-bank) breakpoint on tap.
		// Conditional / bank-filtered breakpoints are added from the
		// Breakpoints tab.
		if id < len(d.dasmCache) {
			addr := d.dasmCache[id].Addr
			if _, ok := d.bps.Lookup(addr); ok {
				d.bps.Remove(addr)
			} else {
				d.bps.Add(addr, BPEntry{Bank: -1})
			}
			d.dasmList.UnselectAll()
			d.dasmList.Refresh()
			if d.bpW != nil {
				d.bpW.Refresh()
			}
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
			// 74 = full hex row width: "AAAA  " + 16×"BB " + gap +
			// " |" + 16 ascii + "|". Sized to content so the ASCII
			// column isn't clipped.
			t := canvas.NewText(strings.Repeat(" ", 74), colHex)
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

	// Spectrum Next state panel — empty until SetNextProvider
	// installs a provider. Stays in the layout permanently to
	// avoid recomputing the split offsets when a Next provider
	// arrives mid-session.
	d.nextPanelBox = container.NewVBox()
	d.nextLines = map[string]*canvas.Text{}
	nextScroll := container.NewVScroll(d.nextPanelBox)
	nextScroll.SetMinSize(fyne.NewSize(150, 0))

	// Memory-paging diagram lives ABOVE the NEXT STATE text. 4
	// cells for classic (16K each), 8 cells for Next (8K each).
	// The widget swaps between them automatically when
	// SetNextProvider is called.
	d.pageMap = NewPageMapWidget(d.mem)
	pageMapPanel := container.NewBorder(
		container.NewVBox(headerText("  PAGING", colRegName), widget.NewSeparator()),
		nil, nil, nil, d.pageMap.Root(),
	)

	// Build the bank-inspect widget; SetBankAccessor wires it later.
	d.bankInspect = NewBankInspectWidget(nil)

	// Backtrace widget — pulls live state from the CPU each render.
	// The closure adapter keeps us out of any z80 import here.
	d.backtrace = NewBacktraceWidget(
		cpuStackSource{cpu: d.cpu},
		func(a uint16) byte { return d.mem.Read(a) },
	)

	// History widget — SetHistory wires the ring later.
	d.history = NewHistoryWidget(nil)

	// Heatmap widget — same shared ring, wired by SetHistory.
	d.heatmapW = NewHeatmapWidget(nil)

	// Time-travel widget — wired by SetTimeTravel.
	d.ttW = NewTimeTravelWidget(nil)

	// NextReg arbitrary read/write — SetNextRegAccessor later.
	d.nextRegW = NewNextRegWidget(nil)

	// Breakpoints panel — talks to the SAME shared store that
	// CheckBreakpoint and the telnet debugger use, so the list is
	// unified across both surfaces.
	d.bpW = NewBreakpointsWidget(d.bps)

	// Watchpoints panel — register watches via the shared set
	// (wired by SetRegWatches; nil-safe until then).
	d.watchW = NewWatchpointsWidget(d.regWatches)

	// Graphical palette swatch — fed by SetNextProvider when the
	// provider implements PaletteRGBAProvider.
	d.paletteView = NewPaletteView(nil)

	// Graphical sprite viewer — fed by SetNextProvider when the
	// provider implements SpriteVizProvider.
	d.spriteView = NewSpriteView(nil)

	// Graphical framebuffer viewers — fed by SetNextProvider when the
	// provider implements Layer2FrameProvider / TilemapFrameProvider.
	d.layer2View = NewImageView("Layer 2 framebuffer", 256, 192, nil)
	d.tilemapView = NewImageView("Tilemap", 320, 240, nil)

	// Tabbed tools area replaces the single NEXT STATE panel so we
	// can host parity surfaces (bank inspector, NextReg explorer,
	// backtrace, history) without crowding the four-pane layout.
	toolsTabs := container.NewAppTabs(
		container.NewTabItemWithIcon("Next State", theme.ComputerIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  NEXT STATE", colRegName), widget.NewSeparator()),
			nil, nil, nil, nextScroll,
		))),
		container.NewTabItemWithIcon("Bank Inspect", theme.StorageIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  BANK INSPECT", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.bankInspect.Root(),
		))),
		container.NewTabItemWithIcon("Backtrace", theme.NavigateBackIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  BACKTRACE", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.backtrace.Root(),
		))),
		container.NewTabItemWithIcon("History", theme.HistoryIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  M1 HISTORY", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.history.Root(),
		))),
		container.NewTabItemWithIcon("NextReg", theme.SettingsIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  NEXTREG R/W", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.nextRegW.Root(),
		))),
		container.NewTabItemWithIcon("Breakpoints", theme.MediaRecordIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  BREAKPOINTS", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.bpW.Root(),
		))),
		container.NewTabItemWithIcon("Watchpoints", theme.VisibilityIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  REGISTER WATCHPOINTS", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.watchW.Root(),
		))),
		container.NewTabItemWithIcon("Heatmap", theme.GridIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  M1 HEATMAP", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.heatmapW.Root(),
		))),
		container.NewTabItemWithIcon("Time Travel", theme.HistoryIcon(), panelBG(container.NewBorder(
			container.NewVBox(headerText("  TIME TRAVEL", colRegName), widget.NewSeparator()),
			nil, nil, nil, d.ttW.Root(),
		))),
		container.NewTabItemWithIcon("Palette", theme.ColorPaletteIcon(),
			panelBG(d.paletteView.Root())),
		container.NewTabItemWithIcon("Sprites", theme.GridIcon(),
			panelBG(d.spriteView.Root())),
		container.NewTabItemWithIcon("Layer 2", theme.MediaPhotoIcon(),
			panelBG(d.layer2View.Root())),
		container.NewTabItemWithIcon("Tilemap", theme.ViewFullScreenIcon(),
			panelBG(d.tilemapView.Root())),
	)
	toolsTabs.SetTabLocation(container.TabLocationTop)

	// Two-row layout so the window fits a standard display and every
	// divider is draggable. Previously all four panes sat side-by-side
	// (registers | disasm | hex | tools), which forced a >1400px
	// minimum width — wider than many laptop screens — and pinned the
	// split dividers. Now:
	//   TOP row : registers | disassembly | hex   (the live CPU view)
	//   BOTTOM  : paging diagram | tools tabs       (full width)
	// The tools tabs (with their wide poke/condition forms) no longer
	// compete for horizontal space, so the top row's minimum width is
	// just reg + disasm + hex.
	dasmHex := container.NewHSplit(dasmPanel, hexPanel)
	dasmHex.SetOffset(0.40) // give hex the wider share (its row is 74 cols)
	topRow := container.NewHSplit(regPanel, dasmHex)
	topRow.SetOffset(0.16)

	bottomRow := container.NewHSplit(pageMapPanel, toolsTabs)
	bottomRow.SetOffset(0.22)

	mainSplit := container.NewVSplit(topRow, bottomRow)
	mainSplit.SetOffset(0.52)

	return container.NewBorder(
		container.NewVBox(d.buildControls(), widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), panelBG(d.statusTxt)),
		nil, nil, mainSplit,
	)
}

// refreshNextPanel updates the Next state panel from the current
// provider. No-op when no provider is set.
func (d *Debugger) refreshNextPanel() {
	if d.nextPanelBox == nil {
		return
	}
	d.nextPanelBox.RemoveAll()
	if d.nextProvider == nil {
		t := canvas.NewText("  (no Next state — classic model)", colRegName)
		t.TextStyle = fyne.TextStyle{Monospace: true}
		t.TextSize = fontSize
		d.nextPanelBox.Add(t)
		d.nextPanelBox.Refresh()
		return
	}
	addLine := func(s string, c color.Color) {
		t := canvas.NewText(s, c)
		t.TextStyle = fyne.TextStyle{Monospace: true}
		t.TextSize = fontSize
		d.nextPanelBox.Add(t)
	}
	addLine("MMU slots (8K windows)", colRegName)
	slots := d.nextProvider.MMUSlots()
	for i, b := range slots {
		base := uint16(i) * 0x2000
		val := fmt.Sprintf("$%04X-$%04X ", base, base+0x1FFF)
		if b < 0 {
			val += "  --"
		} else {
			val += fmt.Sprintf("bank %3d", b)
		}
		addLine("  "+val, colRegVal)
	}
	addLine("", colRegVal)
	addLine("divMMC", colRegName)
	addLine("  "+d.nextProvider.DivMMCState(), colRegVal)
	addLine("", colRegVal)
	addLine("NextRegs", colRegName)
	regs := d.nextProvider.NextRegs()
	for _, r := range NextRegsOfInterest {
		v := regs[r]
		addLine(fmt.Sprintf("  $%02X = $%02X", r, v), colRegVal)
	}
	d.nextPanelBox.Refresh()
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
	stepOverBtn := widget.NewButtonWithIcon("Step Over", theme.MediaFastForwardIcon(), func() {
		switch {
		case d.onStepOver != nil:
			d.onStepOver()
		case d.onStep != nil:
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
		d.bps.Clear()
		d.Refresh()
	})

	return container.NewHBox(
		pauseBtn, stepBtn, stepOverBtn, frameBtn, runBtn,
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
	for _, fl := range []struct {
		n string
		b byte
	}{{"S", 0x80}, {"Z", 0x40}, {"H", 0x10}, {"PV", 0x04}, {"N", 0x02}, {"C", 0x01}} {
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

	d.irqLine.Text = fmt.Sprintf(" IRQ %d/%d (taken/rej)", z80.IntFireCount, z80.IntRejectCount)
	d.irqLine.Refresh()

	if d.cpu.Halted {
		d.haltLine.Text = " ** HALTED **"
		d.haltLine.Color = colHalted
	} else {
		d.haltLine.Text = ""
	}
	d.haltLine.Refresh()
}

func (d *Debugger) refreshDisassembly() {
	// Always re-disassemble from live memory: PC is the common case
	// that moves, but a paused-debugger memory write (Write Mem
	// dialog, a poke via the bank inspector, self-modifying code)
	// can change the bytes at the current PC without moving it, and
	// the view must not show stale mnemonics.
	d.dasmCache = Disassemble(d.mem.Read, d.cpu.PC, dasmRows)
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
	if active := d.bps.Snapshot(); len(active) > 0 {
		addrs := []string{}
		for a := range active {
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
			dlg.Hide()
			d.Refresh()
		}),
	))
	dlg.Resize(fyne.NewSize(400, 200))
	dlg.Show()
}
