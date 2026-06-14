package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestConfigModeWindowReadFiresRAMReadHook — the read-tape relies on
// SetRAMReadHook seeing EVERY guest RAM read. The config-mode window
// at $0000-$3FFF (NR$03 config mode + NR$04 RAMPAGE) routes through
// configModeReadByte, which historically bypassed the hook and made
// value-tapes under-report against oracle emulators (the development log).
func TestConfigModeWindowReadFiresRAMReadHook(t *testing.T) {
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	m.EnterConfigMode()
	m.SetConfigModeRAMPage(0x00)

	// Write a marker through the window, then read it back.
	m.Write(0x0123, 0xA5)

	var got []struct {
		bank int
		off  uint16
		val  byte
	}
	m.SetRAMReadHook(func(bank int, off uint16, val byte) {
		got = append(got, struct {
			bank int
			off  uint16
			val  byte
		}{bank, off, val})
	})
	if v := m.Read(0x0123); v != 0xA5 {
		t.Fatalf("config-window read = %#02x, want 0xA5", v)
	}
	if len(got) == 0 {
		t.Fatal("RAM-read hook did not fire for a config-mode window read")
	}
	if got[0].val != 0xA5 {
		t.Errorf("hook val = %#02x, want 0xA5", got[0].val)
	}
}
