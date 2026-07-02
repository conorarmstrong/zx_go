package debugger

import (
	"fmt"
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// pagemapColors carries the colour palette for memory-map cells.
// Picked for readable contrast against the debugger's dark panel
// background and for at-a-glance meaning:
//
//   - ROM      : steel blue   (system code)
//   - screen   : forest green (visible to the user)
//   - shadow   : pale green   (off-screen second screen)
//   - contend  : muted red    (contended RAM, slots 1+5 on 128K)
//   - normal   : neutral gray
//   - high     : purple       (Spectrum Next high banks $20+)
//   - divMMC   : amber        (overlay paged in)
//   - unknown  : dark gray    (uninitialised / sentinel)
var pagemapColors = struct {
	rom, screen, shadow, contend, normal, high, divmmc, unknown color.NRGBA
}{
	rom:     color.NRGBA{R: 60, G: 90, B: 150, A: 255},
	screen:  color.NRGBA{R: 50, G: 130, B: 60, A: 255},
	shadow:  color.NRGBA{R: 80, G: 140, B: 80, A: 255},
	contend: color.NRGBA{R: 150, G: 60, B: 60, A: 255},
	normal:  color.NRGBA{R: 80, G: 80, B: 90, A: 255},
	high:    color.NRGBA{R: 120, G: 70, B: 150, A: 255},
	divmmc:  color.NRGBA{R: 200, G: 140, B: 50, A: 255},
	unknown: color.NRGBA{R: 50, G: 50, B: 55, A: 255},
}

// pagemapCell is one rectangle on the memory map: a coloured
// background with two stacked text lines (address range + bank
// label). Stored so refresh can mutate text and colour without
// rebuilding the container tree.
type pagemapCell struct {
	bg     *canvas.Rectangle
	rangeT *canvas.Text
	bankT  *canvas.Text
	root   *fyne.Container
}

func newPagemapCell() *pagemapCell {
	bg := canvas.NewRectangle(pagemapColors.unknown)
	bg.StrokeColor = color.NRGBA{R: 30, G: 30, B: 40, A: 255}
	bg.StrokeWidth = 1
	// NOTE: deliberately not Bold — Fyne's test theme lacks a
	// monospace+bold font and panics tests that exercise this
	// widget's layout. Distinguish rangeT visually via TextSize
	// alone if needed later.
	rangeT := canvas.NewText("$0000-$0000", color.White)
	rangeT.TextStyle = fyne.TextStyle{Monospace: true}
	rangeT.TextSize = 11
	bankT := canvas.NewText("—", color.White)
	bankT.TextStyle = fyne.TextStyle{Monospace: true}
	bankT.TextSize = 11
	stack := container.NewVBox(rangeT, bankT)
	// Background + text layered.
	root := container.NewStack(bg, container.NewPadded(stack))
	return &pagemapCell{bg: bg, rangeT: rangeT, bankT: bankT, root: root}
}

// set updates the cell's labels and background colour.
func (c *pagemapCell) set(rangeStr, bankStr string, bg color.Color) {
	c.rangeT.Text = rangeStr
	c.bankT.Text = bankStr
	c.bg.FillColor = bg
	c.bg.Refresh()
	c.rangeT.Refresh()
	c.bankT.Refresh()
}

// PageMapWidget is the live memory-paging diagram. Hands the
// debugger a container.Object plus a Refresh method that pulls
// fresh state from the wired providers.
type PageMapWidget struct {
	classic    []*pagemapCell // 4 cells for classic models
	next       []*pagemapCell // 8 cells for Next mode (replaces classic)
	classicBox *fyne.Container
	nextBox    *fyne.Container
	root       *fyne.Container

	mem          *memory.Memory
	nextProvider NextProvider // optional; nil for non-Next emulators
}

// NewPageMapWidget builds a fresh diagram bound to mem. Pass a
// non-nil NextProvider to enable the 8-slot view; passing nil
// leaves the diagram in 4-slot classic mode permanently.
func NewPageMapWidget(mem *memory.Memory) *PageMapWidget {
	w := &PageMapWidget{mem: mem}

	w.classic = make([]*pagemapCell, 4)
	classicRows := make([]fyne.CanvasObject, 4)
	for i := range w.classic {
		w.classic[i] = newPagemapCell()
		classicRows[i] = w.classic[i].root
	}
	w.classicBox = container.NewGridWithRows(4, classicRows...)

	w.next = make([]*pagemapCell, 8)
	nextRows := make([]fyne.CanvasObject, 8)
	for i := range w.next {
		w.next[i] = newPagemapCell()
		nextRows[i] = w.next[i].root
	}
	w.nextBox = container.NewGridWithRows(8, nextRows...)

	// Outer container holds both layouts; only one is visible at
	// a time (selected by SetNextProvider / model).
	w.root = container.NewStack(w.classicBox)
	return w
}

// SetNextProvider switches the widget to 8-slot Next mode. Pass
// nil to switch back to classic 4-slot.
func (w *PageMapWidget) SetNextProvider(p NextProvider) {
	w.nextProvider = p
	w.root.RemoveAll()
	if p != nil {
		w.root.Add(w.nextBox)
	} else {
		w.root.Add(w.classicBox)
	}
	w.root.Refresh()
}

// Root returns the CanvasObject to embed in the debugger layout.
func (w *PageMapWidget) Root() fyne.CanvasObject { return w.root }

// Refresh pulls fresh state and updates every cell. Called from
// the debugger's per-tick refresh.
func (w *PageMapWidget) Refresh() {
	if w.mem == nil {
		return
	}
	if w.nextProvider != nil && w.mem.GetCurrentModel() == roms.ModelNext {
		w.refreshNext()
	} else {
		w.refreshClassic()
	}
}

// refreshClassic populates the 4-cell view for classic models.
// Reads the page map and screen-page sysvar; reports which RAM
// bank is in each 16K slot and colour-codes by role.
func (w *PageMapWidget) refreshClassic() {
	readMap, _ := w.mem.GetPageMap()
	model := w.mem.GetCurrentModel()
	is128K := model == roms.Model128K || model == roms.ModelPlus2 ||
		model == roms.ModelPlus2A || model == roms.ModelPlus3 ||
		model == roms.ModelNext
	for i := 0; i < 4; i++ {
		base := uint16(i) * 0x4000
		end := base + 0x3FFF
		rangeStr := fmt.Sprintf("$%04X-$%04X", base, end)
		bankIdx := readMap[i]
		var bankStr string
		var bg color.Color
		switch {
		case bankIdx >= 16:
			// ROM. 16=ROM0, 17=ROM1, 18=ROM2, 19=ROM3.
			bankStr = fmt.Sprintf("ROM %d", bankIdx-16)
			bg = pagemapColors.rom
		case bankIdx == w.mem.ScreenPage:
			// The bank holding the visible screen.
			bankStr = fmt.Sprintf("RAM %d (screen)", bankIdx)
			bg = pagemapColors.screen
		case bankIdx == 7 && is128K:
			// Shadow screen bank (selected by 7FFD bit 3).
			bankStr = "RAM 7 (shadow)"
			bg = pagemapColors.shadow
		case (bankIdx&1) == 1 && is128K:
			// On 128K, odd-numbered RAM banks (1, 3, 5, 7) are
			// contended. Slots 1 (bank 5) and 3 (the paged bank)
			// most commonly hit the contended set.
			bankStr = fmt.Sprintf("RAM %d (contended)", bankIdx)
			bg = pagemapColors.contend
		default:
			bankStr = fmt.Sprintf("RAM %d", bankIdx)
			bg = pagemapColors.normal
		}
		w.classic[i].set(rangeStr, bankStr, bg)
	}
}

// refreshNext populates the 8-cell view for Next mode. Reads the
// 8K MMU shadow plus the divMMC overlay state; reports the
// 8K-bank index in each slot and colour-codes by role.
func (w *PageMapWidget) refreshNext() {
	mmu := w.nextProvider.MMUSlots()
	divmmcState := w.nextProvider.DivMMCState()
	// divMMCState is a formatted string like "paged=true mapram=true automap=true".
	// We just want the paged-in flag for colour-coding the low slots.
	overlayActive := containsField(divmmcState, "paged=true")

	for i := 0; i < 8; i++ {
		base := uint16(i) * 0x2000
		end := base + 0x1FFF
		rangeStr := fmt.Sprintf("$%04X-$%04X", base, end)
		bankIdx := mmu[i]
		var bankStr string
		var bg color.Color
		switch {
		case overlayActive && i < 2:
			// divMMC overlay covers slots 0 and 1 (the bottom 16K)
			// when paged in. Mark in amber regardless of underlying
			// bank — the user is reading from the overlay, not the
			// MMU value.
			bankStr = "divMMC overlay"
			bg = pagemapColors.divmmc
		case bankIdx == 0xFF:
			// 0xFF is the ROM sentinel in the 8K shadow.
			bankStr = "ROM"
			bg = pagemapColors.rom
		case bankIdx == 10 || bankIdx == 11:
			// 8K halves of classic bank 5 — the default screen RAM.
			bankStr = fmt.Sprintf("bank %d (screen)", bankIdx)
			bg = pagemapColors.screen
		case bankIdx == 14 || bankIdx == 15:
			// 8K halves of classic bank 7 — shadow screen RAM.
			bankStr = fmt.Sprintf("bank %d (shadow)", bankIdx)
			bg = pagemapColors.shadow
		case bankIdx >= 0x20:
			// Spectrum Next high banks (above the classic 128K
			// allocation). Distinct colour so it's obvious when
			// .NEX files / NEXTBASIC are using extended memory.
			bankStr = fmt.Sprintf("bank %d ($%02X)", bankIdx, bankIdx)
			bg = pagemapColors.high
		default:
			bankStr = fmt.Sprintf("bank %d", bankIdx)
			bg = pagemapColors.normal
		}
		w.next[i].set(rangeStr, bankStr, bg)
	}
}

// containsField reports whether needle appears anywhere in
// haystack. Used to test for a "key=value" pair inside the
// formatted divMMC state string.
func containsField(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
