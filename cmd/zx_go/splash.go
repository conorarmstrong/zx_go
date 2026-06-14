package main

import (
	"bytes"
	_ "embed"
	"image/png"
	"log/slog"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

//go:embed splash.png
var splashPNG []byte

// splashDuration is how long the splash image stays up before the
// real emulator content is swapped in. Short enough not to feel
// like an obstacle, long enough that a passing glance registers
// the artwork.
const splashDuration = 2500 * time.Millisecond

// showSplash sets the window content to the embedded splash image
// for splashDuration, then calls onDone on the Fyne main thread.
// onDone is where the caller swaps in the real emulator UI and
// gives the keyboard widget focus.
//
// Falls back to calling onDone immediately if the splash image
// can't be decoded — the binary should still launch.
func showSplash(w fyne.Window, onDone func()) {
	img, err := png.Decode(bytes.NewReader(splashPNG))
	if err != nil {
		slog.Warn("splash: decode embedded splash.png", "err", err)
		onDone()
		return
	}
	canvasImg := canvas.NewImageFromImage(img)
	canvasImg.FillMode = canvas.ImageFillContain
	canvasImg.ScaleMode = canvas.ImageScaleSmooth
	// Match the splash window size to the image's aspect, scaled
	// to the current window size so the user sees the full art.
	w.SetContent(container.NewStack(canvasImg))

	// Schedule the handoff. fyne.Do is required because we're
	// switching content from a non-UI goroutine.
	go func() {
		time.Sleep(splashDuration)
		fyne.Do(onDone)
	}()
}
