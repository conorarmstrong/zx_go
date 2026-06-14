package debugger

import (
	"image"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// PaletteProvider supplies the 256 active palette colours for the
// visual palette viewer. Returns fully-expanded 8-bit-per-channel
// RGBA so the widget stays decoupled from the Next palette package.
type PaletteProvider func() [256]color.RGBA

// PaletteView is a graphical palette inspector: a 16×16 grid of the
// active 256-entry palette rendered as real colour swatches (the
// visual counterpart to the `palette-dump` text command, matching
// jnext's palette viewer).
type PaletteView struct {
	provider PaletteProvider
	img      *canvas.Image
	root     fyne.CanvasObject
}

const paletteCell = 18 // px per swatch cell → 288×288 grid

// NewPaletteView builds the widget. provider may be nil (renders an
// all-black grid until one is set).
func NewPaletteView(provider PaletteProvider) *PaletteView {
	p := &PaletteView{provider: provider}
	p.img = canvas.NewImageFromImage(p.renderImage())
	p.img.FillMode = canvas.ImageFillOriginal
	p.img.ScaleMode = canvas.ImageScalePixels // crisp, no blur between cells
	p.img.SetMinSize(fyne.NewSize(16*paletteCell, 16*paletteCell))

	title := widget.NewLabelWithStyle("Active palette — 256 entries (16×16)",
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	p.root = container.NewBorder(
		container.NewVBox(title, widget.NewSeparator()),
		nil, nil, nil,
		container.NewCenter(p.img),
	)
	return p
}

// renderImage produces the swatch image from the current provider.
func (p *PaletteView) renderImage() image.Image {
	var cols [256]color.RGBA
	if p.provider != nil {
		cols = p.provider()
	}
	return RenderSwatch(func(i int) color.RGBA { return cols[i] }, 256, paletteCell, 16)
}

// Root returns the widget's top-level canvas object for embedding in
// a tab.
func (p *PaletteView) Root() fyne.CanvasObject { return p.root }

// SetProvider swaps the colour source and repaints.
func (p *PaletteView) SetProvider(provider PaletteProvider) {
	p.provider = provider
	p.Refresh()
}

// Refresh repaints the swatch from the current provider.
func (p *PaletteView) Refresh() {
	if p.img == nil {
		return
	}
	p.img.Image = p.renderImage()
	p.img.Refresh()
}
