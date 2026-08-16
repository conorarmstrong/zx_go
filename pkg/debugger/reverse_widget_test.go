package debugger

import (
	"errors"
	"strings"
	"testing"
)

// The control panel is deliberately dumb: it drives the controller and shows
// what came back. Everything it must not do — guess, retry, or swallow a
// refusal — is what these tests pin, because a refusal that never reaches the
// screen is the same as no refusal at all.

type fakeReverseCtl struct {
	available bool
	inReverse bool
	status    string

	enterErr, backErr, fwdErr, runErr error

	enters, exits, backs, fwds int
	lastExpr                   string
}

func (c *fakeReverseCtl) ReverseAvailable() bool { return c.available }
func (c *fakeReverseCtl) InReverse() bool        { return c.inReverse }
func (c *fakeReverseCtl) ReverseStatus() string  { return c.status }

func (c *fakeReverseCtl) EnterReverse() error {
	c.enters++
	if c.enterErr != nil {
		return c.enterErr
	}
	c.inReverse = true
	return nil
}

func (c *fakeReverseCtl) ExitReverse() {
	c.exits++
	c.inReverse = false
}

func (c *fakeReverseCtl) ReverseStepBack() error {
	c.backs++
	return c.backErr
}

func (c *fakeReverseCtl) ReverseStepForward() error {
	c.fwds++
	return c.fwdErr
}

func (c *fakeReverseCtl) ReverseRunBack(expr string) error {
	c.lastExpr = expr
	return c.runErr
}

// The Debugger is the real backend; if it stops satisfying the interface the
// panel silently loses its wiring.
var _ ReverseController = (*Debugger)(nil)

func TestReverseWidgetConstructsAndRefreshesWithNoBackend(t *testing.T) {
	w := NewReverseWidget(nil)
	if w == nil || w.Root() == nil {
		t.Fatal("NewReverseWidget(nil) must build a usable panel")
	}
	w.Refresh()
	// Every control must be safe to press with nothing wired behind it.
	w.enterBtn.OnTapped()
	w.backBtn.OnTapped()
	w.fwdBtn.OnTapped()
	w.runBackBtn.OnTapped()
}

func TestReverseWidgetButtonsDriveTheCursor(t *testing.T) {
	c := &fakeReverseCtl{available: true}
	w := NewReverseWidget(c)

	w.enterBtn.OnTapped()
	if c.enters != 1 {
		t.Fatalf("EnterReverse called %d times, want 1", c.enters)
	}
	w.backBtn.OnTapped()
	w.backBtn.OnTapped()
	if c.backs != 2 {
		t.Errorf("ReverseStepBack called %d times, want 2", c.backs)
	}
	w.fwdBtn.OnTapped()
	if c.fwds != 1 {
		t.Errorf("ReverseStepForward called %d times, want 1", c.fwds)
	}
	// The same control returns to the present once reverse mode is on:
	// one obvious way out, always in the same place.
	w.enterBtn.OnTapped()
	if c.exits != 1 {
		t.Errorf("ExitReverse called %d times, want 1", c.exits)
	}
}

func TestReverseWidgetLabelsTheToggleByMode(t *testing.T) {
	c := &fakeReverseCtl{available: true}
	w := NewReverseWidget(c)
	live := w.enterBtn.Text

	c.inReverse = true
	w.Refresh()
	if w.enterBtn.Text == live {
		t.Errorf("the toggle reads %q both live and reversing; it must say which way it goes", live)
	}
	if !strings.Contains(strings.ToLower(w.enterBtn.Text), "present") {
		t.Errorf("toggle while reversing = %q, want it to offer the way back to the present", w.enterBtn.Text)
	}
}

// A refusal the user never sees is worse than useless: they conclude the
// button is broken and keep pressing it.
func TestReverseWidgetShowsTheControllersRefusal(t *testing.T) {
	c := &fakeReverseCtl{available: true, inReverse: true}
	c.backErr = errors.New("start of recorded history")
	w := NewReverseWidget(c)

	w.backBtn.OnTapped()
	if !strings.Contains(w.status.Text, "start of recorded history") {
		t.Errorf("status = %q, want the controller's refusal shown verbatim", w.status.Text)
	}
}

func TestReverseWidgetRunBackPassesTheTypedCondition(t *testing.T) {
	c := &fakeReverseCtl{available: true, inReverse: true}
	w := NewReverseWidget(c)

	w.condE.SetText("a == $42")
	w.runBackBtn.OnTapped()
	if c.lastExpr != "a == $42" {
		t.Errorf("ReverseRunBack got %q, want the condition as typed", c.lastExpr)
	}
}

// An empty condition would parse to one that matches everything, which lands
// the cursor one instruction back and looks like a broken search.
func TestReverseWidgetRefusesAnEmptyRunBackCondition(t *testing.T) {
	c := &fakeReverseCtl{available: true, inReverse: true}
	w := NewReverseWidget(c)

	w.condE.SetText("   ")
	w.runBackBtn.OnTapped()
	if c.lastExpr != "" {
		t.Errorf("ReverseRunBack was called with %q; an empty condition matches everything and must not be run", c.lastExpr)
	}
	if w.status.Text == "" {
		t.Error("refusing the empty condition must say why")
	}
}

// With no recording there is nothing to enter, and the panel should say that
// before the user presses anything.
func TestReverseWidgetExplainsItselfWithNoRecording(t *testing.T) {
	c := &fakeReverseCtl{available: false, status: "live machine — no recording"}
	w := NewReverseWidget(c)
	if !strings.Contains(w.status.Text, "no recording") {
		t.Errorf("status = %q, want the controller's explanation of why there is nothing to step back through", w.status.Text)
	}
}
