package debugger

import (
	"image"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// ImageView is a titled, scrollable image panel with a status
// subtitle. Used by the Layer-2 framebuffer and tilemap viewers; the
// render func returns the current frame plus a one-line status (e.g.
// "256x192" or "disabled"). render may be nil (shows a 1×1 blank).
type ImageView struct {
	title  *widget.Label
	status *widget.Label
	img    *canvas.Image
	render func() (image.Image, string)
	root   fyne.CanvasObject
}

// NewImageView builds the panel. minW/minH set the image's minimum
// display size (it scales with crisp nearest-neighbour).
func NewImageView(titleText string, minW, minH float32, render func() (image.Image, string)) *ImageView {
	v := &ImageView{render: render}
	v.title = widget.NewLabelWithStyle(titleText, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.status = widget.NewLabel("")
	img, st := v.frame()
	v.status.SetText(st)
	v.img = canvas.NewImageFromImage(img)
	v.img.FillMode = canvas.ImageFillContain
	v.img.ScaleMode = canvas.ImageScalePixels
	v.img.SetMinSize(fyne.NewSize(minW, minH))
	v.root = container.NewBorder(
		container.NewVBox(v.title, v.status, widget.NewSeparator()),
		nil, nil, nil,
		container.NewScroll(v.img),
	)
	return v
}

func (v *ImageView) frame() (image.Image, string) {
	if v.render == nil {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), "(no source)"
	}
	img, st := v.render()
	if img == nil {
		img = image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	return img, st
}

// Root returns the panel's top-level object.
func (v *ImageView) Root() fyne.CanvasObject { return v.root }

// SetRender swaps the frame source and repaints.
func (v *ImageView) SetRender(render func() (image.Image, string)) {
	v.render = render
	v.Refresh()
}

// Refresh repaints from the current render func.
func (v *ImageView) Refresh() {
	if v.img == nil {
		return
	}
	img, st := v.frame()
	v.status.SetText(st)
	v.img.Image = img
	v.img.Refresh()
}
