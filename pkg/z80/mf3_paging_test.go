package z80

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
	"github.com/conorarmstrong/zx_go/pkg/peripherals"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// mf3TestULA is a test ULA that handles +3 paging ports and delegates to a
// peripheral manager for Multiface port I/O.  It also lets the test control
// the keyboard matrix (ENTER pressed or not).
type mf3TestULA struct {
	mem          *memory.Memory
	pm           *peripherals.PeripheralManager
	enterPressed bool
}

func newMF3TestULA(mem *memory.Memory, pm *peripherals.PeripheralManager) *mf3TestULA {
	return &mf3TestULA{mem: mem, pm: pm}
}

func (u *mf3TestULA) ReadPort(addr uint16) (byte, bool) {
	// Peripheral port reads (Multiface page-in/page-out).
	if u.pm != nil {
		if val, handled := u.pm.HandlePortRead(addr); handled {
			return val, true
		}
	}

	// Keyboard — port 0xFE (bit 0 of addr = 0).
	if addr&0x01 == 0 {
		hi := byte(addr >> 8)
		result := byte(0xFF)
		// Row 6 holds ENTER (bit 0). Selected when hi bit 6 = 0.
		if u.enterPressed && (hi&0x40) == 0 {
			result &^= 0x01 // clear bit 0 = ENTER pressed
		}
		return result, true
	}

	// FDC status (0x2FFD): not ready.
	if addr&0xF002 == 0x2000 {
		return 0x80, true
	}
	// FDC data (0x3FFD): floating bus.
	if addr&0xF002 == 0x3000 {
		return 0xFF, true
	}
	// AY register read (0xFFFD).
	if addr&0xC002 == 0xC000 {
		return 0xFF, true
	}

	return 0xFF, false
}

func (u *mf3TestULA) WritePort(addr uint16, val byte) {
	// Border / speaker — port 0xFE.
	if addr&0x01 == 0 {
		return
	}

	// Peripheral port writes (Multiface lockout control).
	if u.pm != nil {
		u.pm.HandlePortWrite(addr, val)
	}

	// +3 paging ports.
	model := u.mem.GetCurrentModel()
	if model == roms.ModelPlus3 || model == roms.ModelPlus2A {
		if addr&0xC002 == 0x4000 { // Port 0x7FFD
			u.mem.PageMemory(val)
		} else if addr&0xF002 == 0x1000 { // Port 0x1FFD
			u.mem.PageMemoryPlus3(val)
		}
	}
}

// TestMF3PagingStatePreserved boots the +3, triggers an MF3 NMI, lets the
// MF3 menu run, presses ENTER to return, and verifies the +3's paging state
// is correctly restored so the machine doesn't crash.
func TestMF3PagingStatePreserved(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	pm := peripherals.NewPeripheralManager(mem, romPath)
	if err := pm.EnableMultiface(multiface.Multiface3, romPath); err != nil {
		t.Fatalf("Failed to enable MF3: %v", err)
	}

	ula := newMF3TestULA(mem, pm)
	cpu := New(mem, ula)

	// Wire peripheral ROM overlay into memory reads/writes.
	mem.PeripheralRead = pm.HandleMemoryRead
	mem.PeripheralWrite = pm.HandleMemoryWrite

	// NMI callback — same as main.go.
	cpu.NMICallback = func() {
		pm.HandleNMI()
	}

	// ---------------------------------------------------------------
	// Step 1: Boot the +3 for 200 frames — the menu should appear.
	// ---------------------------------------------------------------
	for frame := 0; frame < 200; frame++ {
		cpu.ExecuteFrame(69888)
	}

	bootPixels := countScreenPixels(mem)
	t.Logf("After boot: %d pixels, PC=0x%04X IFF1=%v Halted=%v",
		bootPixels, cpu.PC, cpu.IFF1, cpu.Halted)

	if bootPixels < 200 {
		t.Fatalf("Expected the +3 menu to be drawn after 200 frames, got %d pixels", bootPixels)
	}

	// Record the paging state before triggering NMI.
	port7ffdBefore, port1ffdBefore, specialBefore := mem.GetPortState()
	readMapBefore, writeMapBefore := mem.GetPageMap()
	screenBefore := mem.ScreenPage
	t.Logf("Paging before NMI: 7FFD=0x%02X 1FFD=0x%02X special=%v screen=%d readMap=%v writeMap=%v",
		port7ffdBefore, port1ffdBefore, specialBefore, screenBefore, readMapBefore, writeMapBefore)

	// ---------------------------------------------------------------
	// Step 2: Trigger MF3 NMI.
	// ---------------------------------------------------------------
	cpu.PendingNMI.Store(true)

	// Run a few frames so the MF3 ROM code has time to display its menu.
	for frame := 0; frame < 20; frame++ {
		cpu.ExecuteFrame(69888)
	}

	mfPixels := countScreenPixels(mem)
	t.Logf("MF3 active: %d pixels, PC=0x%04X IFF1=%v romPaged=%v",
		mfPixels, cpu.PC, cpu.IFF1, pm.GetMultiface().IsROMPaged())

	// The MF3 menu should have drawn some pixels (different from the boot menu).
	if mfPixels < 200 {
		t.Logf("Warning: MF3 menu produced only %d pixels (may be using placeholder ROM)", mfPixels)
	}

	// ---------------------------------------------------------------
	// Step 3: Press ENTER to exit the MF3 menu.
	// ---------------------------------------------------------------
	ula.enterPressed = true
	for frame := 0; frame < 20; frame++ {
		cpu.ExecuteFrame(69888)
	}

	// Release ENTER.
	ula.enterPressed = false
	for frame := 0; frame < 20; frame++ {
		cpu.ExecuteFrame(69888)
	}

	// ---------------------------------------------------------------
	// Step 4: Run 200 more frames — the +3 should recover normally.
	// ---------------------------------------------------------------
	for frame := 0; frame < 200; frame++ {
		cpu.ExecuteFrame(69888)
	}

	// Verify paging state restored.
	port7ffdAfter, port1ffdAfter, specialAfter := mem.GetPortState()
	readMapAfter, writeMapAfter := mem.GetPageMap()
	screenAfter := mem.ScreenPage
	t.Logf("Paging after MF3: 7FFD=0x%02X 1FFD=0x%02X special=%v screen=%d readMap=%v writeMap=%v",
		port7ffdAfter, port1ffdAfter, specialAfter, screenAfter, readMapAfter, writeMapAfter)

	afterPixels := countScreenPixels(mem)
	t.Logf("After MF3 exit: %d pixels, PC=0x%04X IFF1=%v Halted=%v",
		afterPixels, cpu.PC, cpu.IFF1, cpu.Halted)

	// The +3 should have its menu back with roughly the same number of pixels
	// as before the NMI.  Allow some tolerance since the cursor may blink.
	if afterPixels < 200 {
		t.Errorf("Expected the +3 menu to be visible after MF3 exit (%d pixels), got %d",
			bootPixels, afterPixels)
	}

	// The pixel count after MF3 exit should be close to what it was before
	// the NMI, indicating the screen was not corrupted by paging changes.
	// Allow 20% tolerance for cursor blink and timing differences.
	pixelDiff := afterPixels - bootPixels
	if pixelDiff < 0 {
		pixelDiff = -pixelDiff
	}
	if pixelDiff > bootPixels/5 {
		t.Errorf("Screen pixel count changed significantly: before=%d after=%d (diff=%d)",
			bootPixels, afterPixels, pixelDiff)
	}

	// Verify the paging registers match what they were before the NMI.
	if port7ffdAfter != port7ffdBefore {
		t.Errorf("port7FFD changed: before=0x%02X after=0x%02X", port7ffdBefore, port7ffdAfter)
	}
	if port1ffdAfter != port1ffdBefore {
		t.Errorf("port1FFD changed: before=0x%02X after=0x%02X", port1ffdBefore, port1ffdAfter)
	}
	if specialAfter != specialBefore {
		t.Errorf("specialPaging changed: before=%v after=%v", specialBefore, specialAfter)
	}
	if readMapAfter != readMapBefore {
		t.Errorf("memoryPageReadMap changed: before=%v after=%v", readMapBefore, readMapAfter)
	}
	if writeMapAfter != writeMapBefore {
		t.Errorf("memoryPageWriteMap changed: before=%v after=%v", writeMapBefore, writeMapAfter)
	}
	if screenAfter != screenBefore {
		t.Errorf("ScreenPage changed: before=%d after=%d", screenBefore, screenAfter)
	}
}

// TestMF3PagingStateSaveRestore is a lower-level unit test that verifies the
// save/restore mechanism works correctly in isolation.
func TestMF3PagingStateSaveRestore(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	// Set up a known paging state.
	mem.PageMemory(0x04)          // RAM page 4 in slot 3, screen 5
	mem.PageMemoryPlus3(0x04)     // ROM 2 selected (bit 2 of 1FFD), normal mode

	port7ffdBefore, port1ffdBefore, specialBefore := mem.GetPortState()
	readMapBefore, writeMapBefore := mem.GetPageMap()
	screenBefore := mem.ScreenPage
	pagingBefore := mem.PagingEnabled

	// Save state.
	state := mem.SavePagingState()

	// Now corrupt the paging state as the MF3 ROM would.
	mem.PageMemory(0x17)          // Different RAM page, different screen, ROM 1
	mem.PageMemoryPlus3(0x03)     // Special paging mode 1

	// Verify state actually changed.
	port7ffdDuring, _, _ := mem.GetPortState()
	if port7ffdDuring == port7ffdBefore {
		t.Fatal("paging state did not change during corruption phase")
	}

	// Restore state.
	mem.RestorePagingState(state)

	// Verify everything is back.
	port7ffdAfter, port1ffdAfter, specialAfter := mem.GetPortState()
	readMapAfter, writeMapAfter := mem.GetPageMap()
	screenAfter := mem.ScreenPage
	pagingAfter := mem.PagingEnabled

	if port7ffdAfter != port7ffdBefore {
		t.Errorf("port7FFD: want 0x%02X, got 0x%02X", port7ffdBefore, port7ffdAfter)
	}
	if port1ffdAfter != port1ffdBefore {
		t.Errorf("port1FFD: want 0x%02X, got 0x%02X", port1ffdBefore, port1ffdAfter)
	}
	if specialAfter != specialBefore {
		t.Errorf("specialPaging: want %v, got %v", specialBefore, specialAfter)
	}
	if readMapAfter != readMapBefore {
		t.Errorf("readMap: want %v, got %v", readMapBefore, readMapAfter)
	}
	if writeMapAfter != writeMapBefore {
		t.Errorf("writeMap: want %v, got %v", writeMapBefore, writeMapAfter)
	}
	if screenAfter != screenBefore {
		t.Errorf("ScreenPage: want %d, got %d", screenBefore, screenAfter)
	}
	if pagingAfter != pagingBefore {
		t.Errorf("PagingEnabled: want %v, got %v", pagingBefore, pagingAfter)
	}
}

// TestMF3PagingStateNilRestore verifies that RestorePagingState with nil is safe.
func TestMF3PagingStateNilRestore(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	// Should not panic.
	mem.RestorePagingState(nil)
}
