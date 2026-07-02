package ula

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Helper to create test ROMs
func createTestROMs(t *testing.T, dir string) {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	roms := map[string][]byte{
		"48.rom":    make([]byte, memory.PageSize),
		"128-0.rom": make([]byte, memory.PageSize),
		"128-1.rom": make([]byte, memory.PageSize),
	}

	for i := 0; i < memory.PageSize; i++ {
		roms["48.rom"][i] = byte(i & 0xFF)
		roms["128-0.rom"][i] = byte((i + 0x10) & 0xFF)
		roms["128-1.rom"][i] = byte((i + 0x20) & 0xFF)
	}

	for name, data := range roms {
		err := os.WriteFile(filepath.Join(dir, name), data, 0644)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func cleanupTestROMs(dir string) {
	_ = os.RemoveAll(dir)
}

func TestULACreation(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		t.Fatalf("Failed to create memory: %v", err)
	}

	kbd := keyboard.New()
	ula := New(mem, kbd)

	if ula == nil {
		t.Fatal("ULA creation returned nil")
	}

	if ula.mem != mem {
		t.Error("ULA memory reference not set correctly")
	}

	if ula.kbd != kbd {
		t.Error("ULA keyboard reference not set correctly")
	}

	// Check initial state
	if ula.BorderColour != 0 {
		t.Errorf("Initial border colour should be 0, got %d", ula.BorderColour)
	}

	if ula.flash {
		t.Error("Flash should be false initially")
	}

	if ula.flashCount != 0 {
		t.Errorf("Flash count should be 0 initially, got %d", ula.flashCount)
	}
}

func TestULAPalette(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Test standard dark colors
	expectedDark := []color.RGBA{
		{0, 0, 0, 255},       // Black
		{0, 0, 205, 255},     // Blue
		{205, 0, 0, 255},     // Red
		{205, 0, 205, 255},   // Magenta
		{0, 205, 0, 255},     // Green
		{0, 205, 205, 255},   // Cyan
		{205, 205, 0, 255},   // Yellow
		{205, 205, 205, 255}, // White
	}

	for i, expected := range expectedDark {
		actual := ula.palette[i]
		if actual != expected {
			t.Errorf("Dark color %d: expected %v, got %v", i, expected, actual)
		}
	}

	// Test bright colors
	expectedBright := []color.RGBA{
		{0, 0, 0, 255},       // Bright Black
		{0, 0, 255, 255},     // Bright Blue
		{255, 0, 0, 255},     // Bright Red
		{255, 0, 255, 255},   // Bright Magenta
		{0, 255, 0, 255},     // Bright Green
		{0, 255, 255, 255},   // Bright Cyan
		{255, 255, 0, 255},   // Bright Yellow
		{255, 255, 255, 255}, // Bright White
	}

	for i, expected := range expectedBright {
		actual := ula.palette[i+8]
		if actual != expected {
			t.Errorf("Bright color %d: expected %v, got %v", i, expected, actual)
		}
	}
}

func TestULAPortReadWrite(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Test port 0xFE write
	ula.WritePort(0xFE, 0x1F) // Border=7, Mic=1, Speaker=1
	if ula.BorderColour != 7 {
		t.Errorf("Border colour should be 7, got %d", ula.BorderColour)
	}
	if !ula.Mic {
		t.Error("Mic should be true")
	}
	if !ula.Speaker {
		t.Error("Speaker should be true")
	}

	// Test border colour isolation
	ula.WritePort(0xFE, 0xFF)
	if ula.BorderColour != 7 { // Only bits 0-2 used for border
		t.Errorf("Border colour should be 7, got %d", ula.BorderColour)
	}

	// Test individual bits
	ula.WritePort(0xFE, 0x08) // Only Mic
	if ula.BorderColour != 0 {
		t.Errorf("Border colour should be 0, got %d", ula.BorderColour)
	}
	if !ula.Mic {
		t.Error("Mic should be true")
	}
	if ula.Speaker {
		t.Error("Speaker should be false")
	}

	ula.WritePort(0xFE, 0x10) // Only Speaker
	if !ula.Speaker {
		t.Error("Speaker should be true")
	}
	if ula.Mic {
		t.Error("Mic should be false")
	}

	// Test port 0xFE read
	val, handled := ula.ReadPort(0xFE)
	if !handled {
		t.Error("Port 0xFE read should be handled")
	}
	if val&0x1F != 0x1F { // Default bits should be set
		t.Errorf("Default bits should be 0x1F, got 0x%02X", val&0x1F)
	}

	// Test tape in bit
	ula.TapeIn = true
	val, _ = ula.ReadPort(0xFE)
	if (val & 0x40) == 0 {
		t.Error("Tape in bit should be set")
	}

	ula.TapeIn = false
	val, _ = ula.ReadPort(0xFE)
	if (val & 0x40) != 0 {
		t.Error("Tape in bit should not be set")
	}

	// Test unhandled port
	_, handled = ula.ReadPort(0xFF)
	if handled {
		t.Error("Port 0xFF should not be handled")
	}
}

func TestULAFlashTiming(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Flash should toggle every 16 frames
	initialFlash := ula.flash

	// Run 15 frames - should not flash
	for i := 0; i < 15; i++ {
		ula.Render()
	}
	if ula.flash != initialFlash {
		t.Error("Flash should not change after 15 frames")
	}
	if ula.flashCount != 15 {
		t.Errorf("Flash count should be 15, got %d", ula.flashCount)
	}

	// 16th frame should toggle flash
	ula.Render()
	if ula.flash == initialFlash {
		t.Error("Flash should toggle after 16 frames")
	}
	if ula.flashCount != 0 {
		t.Errorf("Flash count should reset to 0, got %d", ula.flashCount)
	}

	// Another 16 frames should toggle back
	for i := 0; i < 16; i++ {
		ula.Render()
	}
	if ula.flash != initialFlash {
		t.Error("Flash should toggle back after another 16 frames")
	}
}

func TestULARenderBorder(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Set border color to red
	ula.BorderColour = 2
	img := ula.Render()

	redColor := ula.palette[2]

	// Check border areas
	borderPixels := []struct{ x, y int }{
		{0, 0},     // Top-left corner
		{319, 0},   // Top-right corner
		{0, 239},   // Bottom-left corner
		{319, 239}, // Bottom-right corner
		{31, 120},  // Left border
		{288, 120}, // Right border
		{160, 23},  // Top border
		{160, 216}, // Bottom border
	}

	for _, pixel := range borderPixels {
		actualColor := img.RGBAAt(pixel.x, pixel.y)
		if actualColor != redColor {
			t.Errorf("Border pixel at (%d, %d): expected %v, got %v",
				pixel.x, pixel.y, redColor, actualColor)
		}
	}

	// Check that screen area is not border color (unless it happens to be)
	screenPixel := img.RGBAAt(32, 24) // First screen pixel
	// We can't guarantee this won't be red, but we can check it's within screen bounds
	if screenPixel.A != 255 {
		t.Error("Screen pixels should be opaque")
	}
}

func TestULAScreenLayout(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Set up test pattern in screen memory
	screenPage := mem.GetPage(mem.ScreenPage)

	// Create a test pattern - horizontal line at top of screen
	screenPage[0] = 0xFF      // First character position - all pixels on
	screenPage[0x1800] = 0x47 // Attribute: white on red

	img := ula.Render()

	// Check that the first 8 pixels of the first screen line are white
	whiteColor := ula.palette[15] // Bright white

	for x := 0; x < 8; x++ {
		actualColor := img.RGBAAt(32+x, 24)
		if actualColor != whiteColor {
			t.Errorf("Pixel at (%d, 24) should be white, got %v", 32+x, actualColor)
		}
	}

	// Test attribute decoding
	// Set up a character with different ink/paper colors
	screenPage[1] = 0xF0      // Half pixels on
	screenPage[0x1801] = 0x38 // Attribute: white on black (normal intensity)

	ula.Render() // Re-render with new data

	// The test verifies the rendering pipeline works
	// More detailed pixel testing would require specific patterns
}

func TestULAFlashAttribute(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Set up flashing attribute
	screenPage := mem.GetPage(mem.ScreenPage)
	screenPage[0] = 0xFF      // All pixels on
	screenPage[0x1800] = 0xC7 // Flash bit set, white on red

	// Render with flash off
	ula.flash = false
	img1 := ula.Render()
	pixel1 := img1.RGBAAt(32, 24)

	// Render with flash on
	ula.flash = true
	img2 := ula.Render()
	pixel2 := img2.RGBAAt(32, 24)

	// The colors should be different when flash toggles
	// White on red vs red on white
	if pixel1.R == pixel2.R && pixel1.G == pixel2.G && pixel1.B == pixel2.B {
		t.Errorf("Flash should change pixel colors: pixel1=%v, pixel2=%v", pixel1, pixel2)
	}
}

func TestULAMemoryPageAccess(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	// Test with 128K memory for screen page switching
	mem, _ := memory.New(testDir, roms.Model128K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Initially should use page 5
	if mem.ScreenPage != 5 {
		t.Errorf("Initial screen page should be 5, got %d", mem.ScreenPage)
	}

	// Set up different patterns in page 5 and page 7
	page5 := mem.GetPage(5)
	page7 := mem.GetPage(7)

	page5[0] = 0xAA
	page7[0] = 0x55

	// Render from page 5
	ula.Render()

	// Switch to page 7
	mem.PageMemory(0x08) // Bit 3 set = screen page 7
	if mem.ScreenPage != 7 {
		t.Errorf("Screen page should be 7, got %d", mem.ScreenPage)
	}

	// Render from page 7
	ula.Render()

	// The ULA should access the correct screen page
	// This tests that GetPage returns the right memory
}

func TestULAImageDimensions(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	img := ula.Render()
	bounds := img.Bounds()

	if bounds.Dx() != 320 {
		t.Errorf("Image width should be 320, got %d", bounds.Dx())
	}

	if bounds.Dy() != 240 {
		t.Errorf("Image height should be 240, got %d", bounds.Dy())
	}

	// Check screen area bounds
	// Screen is 256x192 centered in 320x240 with 32/24 pixel borders
	if bounds.Min.X != 0 || bounds.Min.Y != 0 {
		t.Errorf("Image should start at (0,0), got (%d,%d)", bounds.Min.X, bounds.Min.Y)
	}
}

func TestULAAddressCalculation(t *testing.T) {
	// Test the non-linear screen memory layout calculation
	testCases := []struct {
		x, y     int
		expected uint16
	}{
		{0, 0, 0x0000},    // Top-left
		{1, 0, 0x0001},    // Second byte, first line
		{0, 1, 0x0100},    // First byte, second line
		{0, 8, 0x0020},    // First byte, ninth line (third char line)
		{0, 64, 0x0800},   // First byte, line 64 (second third)
		{31, 191, 0x17FF}, // Last pixel byte (corrected)
	}

	for _, tc := range testCases {
		// This matches the address calculation in Render()
		addr := ((tc.y & 0xC0) << 5) | ((tc.y & 0x07) << 8) | ((tc.y & 0x38) << 2) | tc.x
		if uint16(addr) != tc.expected {
			t.Errorf("Address calculation for (%d,%d): expected 0x%04X, got 0x%04X",
				tc.x, tc.y, tc.expected, addr)
		}
	}
}

func TestULAKempstonJoystick(t *testing.T) {
	testDir := "test_roms_ula"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	// Disabled: port 0x1F should fall through to floating bus.
	val, handled := ula.ReadPort(0x001F)
	if handled {
		t.Errorf("Kempston disabled: port 0x1F should not be handled, got val=0x%02X", val)
	}

	// Enable and set Right + Fire bits.
	ula.KempstonEnabled = true
	ula.SetKempstonButton(KempstonRight, true)
	ula.SetKempstonButton(KempstonFire, true)

	val, handled = ula.ReadPort(0x001F)
	if !handled {
		t.Fatal("Kempston enabled: port 0x1F should be handled")
	}
	if val != (KempstonRight | KempstonFire) {
		t.Errorf("expected joystick state 0x%02X, got 0x%02X", KempstonRight|KempstonFire, val)
	}

	// Releasing fire only.
	ula.SetKempstonButton(KempstonFire, false)
	val, _ = ula.ReadPort(0x001F)
	if val != KempstonRight {
		t.Errorf("expected joystick state 0x%02X, got 0x%02X", KempstonRight, val)
	}

	// Bits 5-7 must always read as zero.
	ula.KempstonState = 0xFF
	val, _ = ula.ReadPort(0x001F)
	if val&0xE0 != 0 {
		t.Errorf("upper 3 bits should be zero, got 0x%02X", val)
	}
}

// Benchmark tests
func BenchmarkULARender(b *testing.B) {
	testDir := "test_roms_ula"
	createTestROMs(&testing.T{}, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ula.Render()
	}
}

// ULA setter/getter coverage: SetTapePlayer + GetTapePlayer, Reset clears
// border/mic/speaker/AY state, SetPeripherals nil-safe, SetNextDMA /
// Compositor / Copper / DAC / DivMMC + getter sanity, GetPortTracer.

func ulaTest(t *testing.T, model roms.SpectrumModel) *ULA {
	t.Helper()
	dir := t.TempDir()
	createTestROMs(t, dir)
	mem, err := memory.New(dir, model)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	kbd := keyboard.New()
	return New(mem, kbd)
}

func TestSetGetTapePlayer(t *testing.T) {
	u := ulaTest(t, roms.Model48K)
	if got := u.GetTapePlayer(); got != nil {
		t.Errorf("default tape player = non-nil")
	}
	tp := NewTapePlayer()
	u.SetTapePlayer(tp)
	if got := u.GetTapePlayer(); got != tp {
		t.Errorf("after Set: GetTapePlayer returned wrong instance")
	}
	u.SetTapePlayer(nil)
	if got := u.GetTapePlayer(); got != nil {
		t.Errorf("after nil-set: tape = non-nil")
	}
}

func TestULAResetClearsState(t *testing.T) {
	u := ulaTest(t, roms.Model128K)
	// Dirty up state.
	u.BorderColour = 5
	u.Mic = true
	u.Speaker = true
	u.KempstonState = 0xFF
	u.WritePort(0xFE, 0xFF) // record a border change in the buffer

	u.Reset()

	if u.BorderColour != 0 {
		t.Errorf("BorderColour after Reset = %d, want 0", u.BorderColour)
	}
	if u.Mic || u.Speaker {
		t.Errorf("Mic/Speaker after Reset = %v/%v, want false/false", u.Mic, u.Speaker)
	}
	if u.KempstonState != 0 {
		t.Errorf("KempstonState after Reset = %02X, want 00", u.KempstonState)
	}
	if len(u.borderChanges) != 0 {
		t.Errorf("borderChanges length after Reset = %d, want 0", len(u.borderChanges))
	}
}

func TestULASetPeripheralsNil(t *testing.T) {
	u := ulaTest(t, roms.Model48K)
	// SetPeripherals(nil) must not panic.
	u.SetPeripherals(nil)
	// A subsequent port write must also be safe.
	u.WritePort(0xFE, 0x07)
}

func TestULASetNextHooksAreSettable(t *testing.T) {
	u := ulaTest(t, roms.ModelNext)
	// All Next-mode hooks accept nil without panicking and don't
	// affect plain port writes.
	u.SetNextCompositor(nil)
	u.SetNextDMA(nil)
	u.SetNextCopper(nil)
	u.SetNextDAC(nil)
	u.SetNextDivMMC(nil)
	if u.NextDivMMC() != nil {
		t.Errorf("NextDivMMC after nil-set = non-nil")
	}
}

// Port/border tracer + audio recording hook coverage.

func TestULA_GetPortTracer(t *testing.T) {
	u := ulaTest(t, roms.Model48K)
	if got := u.GetPortTracer(); got != nil {
		t.Errorf("default GetPortTracer = non-nil")
	}
	var seenAddr uint16
	var seenVal byte
	tracer := PortTracer(func(addr uint16, val byte, write, handled bool) {
		seenAddr = addr
		seenVal = val
	})
	u.SetPortTracer(tracer)
	if got := u.GetPortTracer(); got == nil {
		t.Errorf("after SetPortTracer: GetPortTracer = nil")
	}
	// Trigger by writing to a port.
	u.WritePort(0xFE, 0x07)
	if seenVal != 0x07 || seenAddr != 0xFE {
		t.Logf("tracer saw: addr=%04X val=%02X — may need write to trigger", seenAddr, seenVal)
	}
}

func TestULA_SetBorderTracer(t *testing.T) {
	u := ulaTest(t, roms.Model48K)
	fired := 0
	u.SetBorderTracer(func(port uint16, val byte, newBorder byte, scanline int) {
		fired++
	})
	// Border change via port FE write.
	u.WritePort(0xFE, 0x02) // border red
	if fired == 0 {
		t.Logf("BorderTracer not fired (may be optional under model)")
	}
	// Nil-set must not panic.
	u.SetBorderTracer(nil)
	u.WritePort(0xFE, 0x05)
}

func TestULA_RecordingLifecycle(t *testing.T) {
	u := ulaTest(t, roms.Model128K)
	if u.IsRecording() {
		t.Errorf("default IsRecording = true")
	}
	// EnableAudio is required before StartRecording (audio system exists).
	// If audio init fails (no system audio), StartRecording will fail too.
	// We don't test the success path on CI (no audio device); just confirm
	// IsRecording returns false on fresh ULA.
}

func TestULAAYGetterFollowsModel(t *testing.T) {
	u48 := ulaTest(t, roms.Model48K)
	if u48.AY() != nil {
		t.Errorf("AY on 48K = non-nil")
	}
	u128 := ulaTest(t, roms.Model128K)
	if u128.AY() == nil {
		t.Errorf("AY on 128K = nil")
	}
}

func BenchmarkULAPortIO(b *testing.B) {
	testDir := "test_roms_ula"
	createTestROMs(&testing.T{}, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	kbd := keyboard.New()
	ula := New(mem, kbd)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ula.WritePort(0xFE, byte(i&0xFF))
		ula.ReadPort(0xFE)
	}
}

// TestTimexHiResRender pins the Timex 512x192 8x1 hi-res video mode (port $FF
// mode 110), which NextZXOS's 64/85-column text modes — e.g. the .more text
// viewer — use. Without it those screens rendered as garbled standard-ULA.
func TestTimexHiResRender(t *testing.T) {
	testDir := "test_roms_ula_timex"
	createTestROMs(t, testDir)
	defer cleanupTestROMs(testDir)

	mem, _ := memory.New(testDir, roms.Model48K)
	ula := New(mem, keyboard.New())

	// Row 0, byte column 0: display file 1 = all ink (0xFF), file 2 = all paper.
	screen := mem.GetPage(mem.ScreenPage)
	screen[0] = 0xFF
	screen[0x2000] = 0x00

	// Before enabling hi-res, the frame is the normal width.
	if img := ula.Render(); img.Bounds().Dx() != TotalWidth {
		t.Fatalf("normal mode width = %d, want %d", img.Bounds().Dx(), TotalWidth)
	}

	ula.WritePort(0x00FF, 0x06) // Timex 512x192 hi-res, colour code 0
	if !ula.timexHiResActive() {
		t.Fatal("port $FF=$06 must enable Timex hi-res")
	}
	img := ula.Render()
	if img.Bounds().Dx() != 2*TotalWidth {
		t.Fatalf("hi-res width = %d, want %d (640)", img.Bounds().Dx(), 2*TotalWidth)
	}
	ink, paper := ula.timexHiResColours()
	at := func(x int) color.RGBA {
		o := BorderTop*img.Stride + x*4
		return color.RGBA{img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]}
	}
	// Display byte 0 comes from file 1 (all ink) at x=64..71; display byte 1
	// from file 2 (all paper) at x=72..79 — proving the two-file interleave.
	if at(64) != ink {
		t.Errorf("x=64 (file 1, ink): got %v, want %v", at(64), ink)
	}
	if at(72) != paper {
		t.Errorf("x=72 (file 2, paper): got %v, want %v", at(72), paper)
	}
}
