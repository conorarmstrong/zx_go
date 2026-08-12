package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
)

// Fyne's default file browser shows only a handful of entries at a time, which
// is painful in a directory of hundreds of snapshots or games. These are the
// smallest dimensions worth opening one at, in pixels.
const (
	minFileDialogWidth  = 900
	minFileDialogHeight = 700
)

// fileDialogSize picks a size for a file browser opened over a window of the
// given size. It uses most of the window, but never more than all of it: Fyne
// cannot show a dialog larger than the window it belongs to.
func fileDialogSize(win fyne.Size) fyne.Size {
	w := win.Width * 0.9
	if w < minFileDialogWidth {
		w = minFileDialogWidth
	}
	if w > win.Width {
		w = win.Width
	}

	h := win.Height * 0.9
	if h < minFileDialogHeight {
		h = minFileDialogHeight
	}
	if h > win.Height {
		h = win.Height
	}

	return fyne.NewSize(w, h)
}

// showFileDialog opens a file browser sized to the window it belongs to,
// rather than at Fyne's cramped default.
func showFileDialog(fd *dialog.FileDialog, w fyne.Window) {
	fd.Resize(fileDialogSize(w.Canvas().Size()))
	fd.Show()
}
