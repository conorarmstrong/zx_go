package main

import (
	"testing"

	"fyne.io/fyne/v2"
)

func TestFileDialogSize(t *testing.T) {
	tests := []struct {
		name  string
		win   fyne.Size
		wantW float32
		wantH float32
	}{
		{
			name:  "large window uses most of it",
			win:   fyne.NewSize(1600, 1200),
			wantW: 1440,
			wantH: 1080,
		},
		{
			name:  "medium window falls back to the minimum",
			win:   fyne.NewSize(960, 760),
			wantW: minFileDialogWidth,
			wantH: minFileDialogHeight,
		},
		{
			name:  "small window is never exceeded",
			win:   fyne.NewSize(400, 320),
			wantW: 400,
			wantH: 320,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fileDialogSize(tt.win)
			if got.Width != tt.wantW || got.Height != tt.wantH {
				t.Errorf("fileDialogSize(%v) = %v, want %gx%g", tt.win, got, tt.wantW, tt.wantH)
			}
		})
	}
}

// A dialog must never be asked for more room than its parent window has:
// Fyne cannot show one larger than the window it belongs to.
func TestFileDialogSizeNeverExceedsWindow(t *testing.T) {
	for _, win := range []fyne.Size{
		fyne.NewSize(320, 256),
		fyne.NewSize(640, 480),
		fyne.NewSize(800, 600),
		fyne.NewSize(1024, 768),
		fyne.NewSize(2560, 1440),
	} {
		got := fileDialogSize(win)
		if got.Width > win.Width || got.Height > win.Height {
			t.Errorf("fileDialogSize(%v) = %v, larger than the window", win, got)
		}
	}
}
