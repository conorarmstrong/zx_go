package next

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/ay"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/copper"
	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/rtc"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/uart"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

func wireUmbrellaTestROMs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"48.rom", "128-0.rom", "128-1.rom"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 0x4000), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stubULA satisfies z80.ULA for the smoke test.
type stubULA struct{}

func (stubULA) ReadPort(_ uint16) (byte, bool) { return 0xFF, false }
func (stubULA) WritePort(_ uint16, _ byte)     {}

// TestWireUmbrellaInstallsAllHandlers verifies the Wire umbrella
// installs every NextReg handler. Each register is poked through
// the dispatcher and the side-effect is observed on its target
// subsystem.
//
// Test must be updated whenever a new subsystem is added to Wire —
// forgetting to invoke a new Wire* helper from the umbrella is
// exactly the regression this test exists to catch.
func TestWireUmbrellaInstallsAllHandlers(t *testing.T) {
	installtest.RedirectConfig(t)
	mem, err := memory.New(wireUmbrellaTestROMs(t), roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	cpu := z80.New(mem, stubULA{})
	disp := nextregs.New()
	engine := ay.NewEngine()
	l2 := layer2.New(mem)
	pal := palette.NewBank()
	prio := NewLayerPriority()
	sprites := sprite.New()
	cop := copper.New()
	rtcEngine := rtc.New()
	uartEngine := uart.New()

	Wire(WireOpts{
		Dispatcher: disp,
		Memory:     mem,
		CPU:        cpu,
		AYEngine:   engine,
		Layer2:     l2,
		Palette:    pal,
		Priority:   prio,
		Sprites:    sprites,
		Copper:     cop,
		RTC:        rtcEngine,
		UART:       uartEngine,
	})

	// 0x07: CPU speed -> cpu.SpeedSelect
	disp.Select(0x07)
	disp.WriteData(0x02)
	if cpu.SpeedSelect() != 0x02 {
		t.Errorf("after NextReg 0x07: cpu.SpeedSelect = %#x, want 0x02", cpu.SpeedSelect())
	}

	// 0x08 bit 6: memory.RAMContentionDisabled (per zxnext.vhd:5176)
	disp.Select(0x08)
	disp.WriteData(0x40)
	if !mem.RAMContentionDisabled {
		t.Errorf("after NextReg 0x08=0x40: RAMContentionDisabled should be true")
	}

	// 0x50: MMU slot 0
	disp.Select(0x50)
	disp.WriteData(0x42)
	if mem.GetMMU(0) != 0x42 {
		t.Errorf("after NextReg 0x50=0x42: MMU slot 0 = %#x, want 0x42", mem.GetMMU(0))
	}

	// 0x06: AY audio-chip mode (bits 1-0). Only 11 = "hold all AY in reset"
	// disables; bit 2 is PS/2, NOT AY-disable. (It does not select the chip.)
	disp.Select(0x06)
	disp.WriteData(0x03) // bits 1-0 = 11 → hold AY in reset
	if !engine.Disabled() {
		t.Errorf("after NextReg 0x06=0x03: engine should be disabled (hold in reset)")
	}
	disp.WriteData(0x04) // bit 2 = PS/2 mode, bits 1-0 = 00 → AY active again
	if engine.Disabled() {
		t.Errorf("after NextReg 0x06=0x04: bit 2 is PS/2, must NOT disable AY")
	}

	// 0x12: Layer 2 active bank
	disp.Select(0x12)
	disp.WriteData(0x09)
	if l2.ActiveBank() != 0x09 {
		t.Errorf("after NextReg 0x12=0x09: layer2 active bank = %#x", l2.ActiveBank())
	}

	// 0x69 bit 7: Layer 2 enable
	disp.Select(0x69)
	disp.WriteData(0x80)
	if !l2.Enabled() {
		t.Errorf("after NextReg 0x69=0x80: layer2 should be enabled")
	}

	// 0x40 / 0x41: palette index + value
	disp.Select(0x40)
	disp.WriteData(0x10)
	disp.Select(0x41)
	disp.WriteData(0xAA)
	// $AA has bit1|bit0 = 1, so the 9-bit value's low blue bit is 1
	// (zxnext.vhd:4919), giving ($AA<<1)|1 = $155.
	if pal.Palette(int(palette.PaletteULAFirst)).Get(0x10) != (uint16(0xAA)<<1)|1 {
		t.Errorf("after NextReg 0x40=0x10 + 0x41=0xAA: palette entry mismatch")
	}

	// 0x15: layer priority
	disp.Select(0x15)
	disp.WriteData(0x55)
	if prio.Get() != 0x55 {
		t.Errorf("after NextReg 0x15=0x55: priority.Get = %#x", prio.Get())
	}

	// 0x44: 9-bit palette write (two-byte state machine)
	disp.Select(0x40)
	disp.WriteData(0x20)
	disp.Select(0x44)
	disp.WriteData(0xFF)
	disp.WriteData(0x01)
	if got := pal.Palette(int(palette.PaletteULAFirst)).Get(0x20); got != 0x01FF {
		t.Errorf("after NextReg 0x44 two-byte sequence: entry = %#x, want 0x01FF", got)
	}

	// memory.SpeedMultiplier installed
	if mem.SpeedMultiplier == nil {
		t.Errorf("memory.SpeedMultiplier should be wired")
	} else if mem.SpeedMultiplier() != cpu.SpeedMultiplier() {
		t.Errorf("memory.SpeedMultiplier() = %d, want cpu.SpeedMultiplier() = %d",
			mem.SpeedMultiplier(), cpu.SpeedMultiplier())
	}

	// --- sprite handlers ---

	// 0x34: sprite-select
	disp.Select(0x34)
	disp.WriteData(0x05)
	if sprites.SelectedSprite() != 0x05 {
		t.Errorf("after NextReg 0x34=5: SelectedSprite = %d, want 5", sprites.SelectedSprite())
	}

	// 0x35 / 0x36 / 0x37 / 0x38: attribute path on the currently
	// selected sprite. We pick sprite 5 (already selected above).
	disp.Select(0x35)
	disp.WriteData(0x42)
	disp.Select(0x36)
	disp.WriteData(0x50)
	disp.Select(0x37)
	disp.WriteData(0x21) // palette nibble 2 + X9 bit 0
	disp.Select(0x38)
	disp.WriteData(0xC5) // visible + extended (bit 6) + pattern 0x05

	s := sprites.Sprite(5)
	if s == nil {
		t.Fatal("Sprite(5) returned nil")
	}
	if s.X != 0x142 {
		t.Errorf("Sprite 5 X = %#x, want 0x142", s.X)
	}
	if s.Y != 0x50 {
		t.Errorf("Sprite 5 Y = %#x, want 0x50", s.Y)
	}
	if !s.Visible {
		t.Errorf("Sprite 5 not visible after 0x38 = 0xC5")
	}
	if !s.Extended {
		t.Errorf("Sprite 5 Extended not set after 0x38 bit 6")
	}
	if s.Pattern != 0x05 {
		t.Errorf("Sprite 5 Pattern[5:0] = %#x, want 0x05", s.Pattern)
	}

	// 0x39: 5th attribute byte. Bit 6 = pattern N6 (FPGA
	// sprites.vhd:437), which extends the pattern index into the
	// upper 64 patterns at render time. Write 0x40 to set N6.
	disp.Select(0x39)
	disp.WriteData(0x40)
	if s.Byte4 != 0x40 {
		t.Errorf("Sprite 5 Byte4 after 0x39=0x40: %#x, want 0x40", s.Byte4)
	}

	// --- Copper / RTC / UART handlers ---

	// 0x60 / 0x61 / 0x62: Copper instruction upload + control.
	disp.Select(0x61)
	disp.WriteData(0x00) // cursor low = 0
	disp.Select(0x62)
	disp.WriteData(0x00) // cursor high = 0, mode = Stop
	disp.Select(0x60)
	disp.WriteData(0xFF) // start the two-byte HALT
	disp.WriteData(0xFF)
	// Verify instruction 0 = HALT
	if got := cop.Instruction(0); got.Op != copper.OpHALT {
		t.Errorf("Copper instruction 0 = %+v, want HALT", got)
	}

	// 0x10 / 0x11: RTC i2c lines. Storage-only — just confirm
	// the dispatcher accepts writes without panicking.
	disp.Select(0x10)
	disp.WriteData(0xAA)
	disp.Select(0x11)
	disp.WriteData(0x55)

	// 0xA8 / 0xA9: UART. Send "AT\r" through the dispatcher
	// and read back "OK\r\n".
	for _, ch := range []byte("AT\r") {
		disp.Select(0xA8)
		disp.WriteData(ch)
	}
	disp.Select(0xA9)
	if status := disp.ReadData(); status&uart.StatusRXReady == 0 {
		t.Errorf("UART after AT\\r: status %#x, want RXReady set", status)
	}
	disp.Select(0xA8)
	want := []byte("OK\r\n")
	for i, w := range want {
		if got := disp.ReadData(); got != w {
			t.Errorf("UART response byte %d = %#x, want %#x", i, got, w)
		}
	}
}

// TestWirePalette44StateMachine pins the two-byte 0x44 latch
// behaviour: alternate writes form (hi, lo) pairs that land in the
// active palette via Bank.Write9.
func TestWirePalette44StateMachine(t *testing.T) {
	disp := nextregs.New()
	pal := palette.NewBank()
	WirePalette(disp, pal)

	// Walk through three two-byte writes — confirm each pair
	// lands at the right index with auto-increment between pairs.
	disp.Select(0x40)
	disp.WriteData(0x00)
	disp.Select(0x44)

	pairs := []struct {
		hi, lo byte
		want   uint16
	}{
		{0xFF, 0x01, 0x01FF},
		{0x80, 0x00, 0x100},
		{0x00, 0x01, 0x001},
	}
	for i, p := range pairs {
		disp.WriteData(p.hi)
		disp.WriteData(p.lo)
		if got := pal.Active().Get(byte(i)); got != p.want {
			t.Errorf("pair %d: entry %d = %#x, want %#x", i, i, got, p.want)
		}
	}
	// Cursor should now be at 3.
	if pal.Index() != 3 {
		t.Errorf("Index after three 9-bit writes = %d, want 3", pal.Index())
	}
}
