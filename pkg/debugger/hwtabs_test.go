package debugger

import (
	"testing"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

type fakeHW struct {
	fakeNext
	calls map[string]int
}

type fakeNext struct{}

func (fakeNext) MMUSlots() [8]int         { return [8]int{} }
func (fakeNext) DivMMCState() string      { return "" }
func (fakeNext) NextRegs() map[uint8]byte { return nil }

func (f *fakeHW) HardwareView(view string) []string {
	f.calls[view]++
	return []string{"view " + view}
}

// Only the hardware tab on screen is refreshed, and only while the Next group
// is selected.
func TestHardwareTabsRefreshOnlyTheVisibleOne(t *testing.T) {
	test.NewApp()
	d := &Debugger{hwViews: map[string]*TextView{}, hwTabs: map[*container.TabItem]*TextView{}}
	var items []*container.TabItem
	for _, name := range []string{HardwareViewInterrupts, HardwareViewDMA} {
		v := NewTextView("(none)")
		d.hwViews[name] = v
		item := container.NewTabItem(name, v.Root())
		d.hwTabs[item] = v
		items = append(items, item)
	}
	d.nextTabs = container.NewAppTabs(items...)
	d.nextTab = container.NewTabItem("Next", d.nextTabs)
	cpu := container.NewTabItem("CPU", widget.NewLabel(""))
	d.toolTabs = container.NewAppTabs(cpu, d.nextTab)

	hw := &fakeHW{calls: map[string]int{}}
	d.SetNextProvider(hw) // wiring repaints every view once
	if d.hwViews[HardwareViewDMA].Text() != "view dma" {
		t.Fatalf("DMA view shows %q", d.hwViews[HardwareViewDMA].Text())
	}
	hw.calls = map[string]int{}

	d.toolTabs.Select(cpu)
	d.refreshHardwareTab()
	if len(hw.calls) != 0 {
		t.Errorf("refreshed with the CPU group on screen: %v", hw.calls)
	}
	d.toolTabs.Select(d.nextTab)
	d.nextTabs.Select(items[1])
	hw.calls = map[string]int{}
	d.refreshHardwareTab()
	if hw.calls[HardwareViewDMA] != 1 || hw.calls[HardwareViewInterrupts] != 0 {
		t.Errorf("refresh calls %v, want the DMA view only", hw.calls)
	}
}
