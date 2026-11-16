package debugger

import (
	"testing"

	"fyne.io/fyne/v2/test"
)

func TestTextViewShowsLinesAndAbsence(t *testing.T) {
	test.NewApp()
	v := NewTextView("(none)")
	if v.Text() != "(none)" {
		t.Errorf("empty view shows %q", v.Text())
	}
	lines := []string{"a", "b"}
	v.SetLines(func() []string { return lines })
	if v.Text() != "a\nb" {
		t.Errorf("view shows %q", v.Text())
	}
	lines = nil
	v.Refresh()
	if v.Text() != "(none)" {
		t.Errorf("absent block shows %q", v.Text())
	}
}
