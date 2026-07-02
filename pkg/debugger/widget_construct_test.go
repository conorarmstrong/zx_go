package debugger

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

// Each parity widget must construct with nil/empty backends without
// panicking. This matters because a Select's SetSelectedIndex fires
// its onChange callback immediately, which can reach into fields
// (status/list) that must already be initialized — an ordering hazard
// that's easy to reintroduce silently, so all three widgets are pinned
// here. Constructing the FULL Debugger can't be unit-tested here —
// buildUI → SetContent forces a text-measuring layout that fyne's
// headless test driver panics on (painter/font.go) — but these
// directly exercise the same premature-callback construction path.
func TestParityWidgetsConstruct(t *testing.T) {
	_ = test.NewApp()
	if w := NewHeatmapWidget(nil); w == nil || w.Root() == nil {
		t.Error("NewHeatmapWidget(nil) failed")
	}
	if w := NewWatchpointsWidget(nil); w == nil || w.Root() == nil {
		t.Error("NewWatchpointsWidget(nil) failed")
	}
	if w := NewTimeTravelWidget(nil); w == nil || w.Root() == nil {
		t.Error("NewTimeTravelWidget(nil) failed")
	}
	// Refresh with backends still nil must also be safe.
	NewHeatmapWidget(nil).Refresh()
	NewWatchpointsWidget(nil).Refresh()
	NewTimeTravelWidget(nil).Refresh()
}
