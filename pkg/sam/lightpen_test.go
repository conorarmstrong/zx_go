package sam

import "testing"

// The SAM ASIC exposes the raster position through two read-only "light pen"
// registers on port 0xF8: LPEN (port & 0x1FF == 0x0F8, horizontal) and HPEN
// (port & 0x1FF == 0x1F8, the current display line). The boot ROM busy-waits on
// HPEN to synchronise to the raster, so it must track the beam, never a constant.

func samAt(t *testing.T, line, lineCycle int) *Machine {
	t.Helper()
	m := New(make([]byte, PageSize), make([]byte, PageSize))
	m.frameStart = 0
	m.CPU.SetTstates(uint64(line*samCyclesPerLine + lineCycle))
	return m
}

func TestHPENTracksRasterLine(t *testing.T) {
	// Mid-screen: HPEN reports the line relative to the top of the display.
	for _, line := range []int{samTopBorderLines, samTopBorderLines + 50, samTopBorderLines + 191} {
		m := samAt(t, line, 200)
		got, _ := m.ReadPort(0x01F8) // HPEN
		want := byte(line - samTopBorderLines)
		if got != want {
			t.Errorf("HPEN at line %d = %#02x, want %#02x", line, got, want)
		}
	}
}

func TestHPENOutsideScreenReportsScreenHeight(t *testing.T) {
	// In the top/bottom border HPEN parks at the screen height (192), not 0xFF.
	for _, line := range []int{0, 10, samTopBorderLines + samScreenLines, 300} {
		m := samAt(t, line, 200)
		got, _ := m.ReadPort(0x01F8)
		if got != byte(samScreenLines) {
			t.Errorf("HPEN at border line %d = %#02x, want %#02x", line, got, byte(samScreenLines))
		}
	}
}

func TestHPENNotConstantOverFrame(t *testing.T) {
	// The boot ROM busy-waits on HPEN to lock to the raster, so a value that
	// stayed constant across the frame would hang it.
	seen := map[byte]bool{}
	for line := 0; line < 312; line++ {
		m := samAt(t, line, 200)
		got, _ := m.ReadPort(0x01F8)
		seen[got] = true
	}
	if len(seen) < 10 {
		t.Fatalf("HPEN only produced %d distinct values over a frame (looks constant)", len(seen))
	}
}

func TestLPENTracksHorizontalPosition(t *testing.T) {
	// LPEN's high bits track the horizontal beam position within the line.
	a := samAt(t, samTopBorderLines+10, 2*samSideBorderCycles+0)
	b := samAt(t, samTopBorderLines+10, 2*samSideBorderCycles+100)
	la, _ := a.ReadPort(0x00F8)
	lb, _ := b.ReadPort(0x00F8)
	if la&0xFC == lb&0xFC {
		t.Errorf("LPEN did not advance with the beam: %#02x vs %#02x", la, lb)
	}
}
