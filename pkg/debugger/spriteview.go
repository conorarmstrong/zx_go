package debugger

import (
	"fmt"
	"image"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// SpriteSnapshot is one visible sprite, pre-resolved to RGBA pixels by
// the provider (so the widget needn't know 4bpp/palette internals).
type SpriteSnapshot struct {
	Index  int
	Pixels [SpritePixels]color.RGBA // row-major 16×16
}

// SpriteVizProvider is the OPTIONAL companion interface that feeds the
// graphical Sprite viewer. A NextProvider implementing it supplies the
// currently-visible sprites' resolved pixels.
type SpriteVizProvider interface {
	VisibleSprites() []SpriteSnapshot
}

// SpriteView renders the visible sprites' 16×16 patterns as an actual
// pixel sheet (jnext's marquee inspector — the visual counterpart to
// the sprite-list text command).
type SpriteView struct {
	provider func() []SpriteSnapshot
	img      *canvas.Image
	info     *widget.Label
	root     fyne.CanvasObject
}

const spriteScale = 3 // 16px → 48px cells
const spriteCols = 8

// NewSpriteView builds the widget. provider may be nil.
func NewSpriteView(provider func() []SpriteSnapshot) *SpriteView {
	s := &SpriteView{provider: provider}
	s.info = widget.NewLabelWithStyle("Visible sprites", fyne.TextAlignLeading,
		fyne.TextStyle{Bold: true})
	s.img = canvas.NewImageFromImage(s.renderImage())
	s.img.FillMode = canvas.ImageFillOriginal
	s.img.ScaleMode = canvas.ImageScalePixels
	s.img.SetMinSize(fyne.NewSize(spriteCols*SpriteDim*spriteScale, 4*SpriteDim*spriteScale))
	s.root = container.NewBorder(
		container.NewVBox(s.info, widget.NewSeparator()),
		nil, nil, nil,
		container.NewScroll(container.NewCenter(s.img)),
	)
	return s
}

func (s *SpriteView) renderImage() image.Image {
	var snaps []SpriteSnapshot
	if s.provider != nil {
		snaps = s.provider()
	}
	if len(snaps) == 0 {
		// 1×1 transparent placeholder; info label carries the message.
		return image.NewRGBA(image.Rect(0, 0, SpriteDim, SpriteDim))
	}
	blocks := make([][SpritePixels]color.RGBA, len(snaps))
	for i, sn := range snaps {
		blocks[i] = sn.Pixels
	}
	return RenderSpriteSheet(blocks, spriteScale, spriteCols)
}

// Root returns the widget's top-level object.
func (s *SpriteView) Root() fyne.CanvasObject { return s.root }

// SetProvider swaps the sprite source and repaints.
func (s *SpriteView) SetProvider(provider func() []SpriteSnapshot) {
	s.provider = provider
	s.Refresh()
}

// Refresh repaints from the current provider.
func (s *SpriteView) Refresh() {
	if s.img == nil {
		return
	}
	n := 0
	if s.provider != nil {
		n = len(s.provider())
	}
	if n == 0 {
		s.info.SetText("Visible sprites: none")
	} else {
		s.info.SetText(fmt.Sprintf("Visible sprites: %d", n))
	}
	s.img.Image = s.renderImage()
	s.img.Refresh()
}
