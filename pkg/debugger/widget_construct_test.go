package debugger

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

// Regression: opening the visual debugger panicked because
// NewHeatmapWidget called mode.SetSelectedIndex(0) — which fires the
// select's onChange → Refresh() → setStatus() — BEFORE status/list
// were constructed. The GUI smoke tests launched the app but never
// opened the debugger window (the menu-click path NewWithBreakpoints
// → buildUI → these widgets), so they missed it.
//
// Each parity widget must construct with nil/empty backends without
// panicking (the heatmap one regressed; pin all three). Constructing
// the FULL Debugger can't be unit-tested here — buildUI → SetContent
// forces a text-measuring layout that fyne's headless test driver
// panics on (painter/font.go) — but these directly exercise the same
// premature-callback construction path that crashed.
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
