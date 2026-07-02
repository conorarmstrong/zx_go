package sam

import "testing"

// TestEmbeddedROM verifies the bundled SAM ROM 3.0 is a valid 32K image that
// splits cleanly and starts with the genuine reset entry (DI; JP), and that
// NewDefault boots a machine from it at PC=$0000.
func TestEmbeddedROM(t *testing.T) {
	rom := SystemROM()
	if len(rom) != ROMSize {
		t.Fatalf("embedded ROM is %d bytes, want %d", len(rom), ROMSize)
	}
	rom0, _, err := SplitROM(rom)
	if err != nil {
		t.Fatalf("embedded ROM rejected by SplitROM: %v", err)
	}
	if rom0[0] != 0xF3 || rom0[1] != 0xC3 {
		t.Errorf("reset entry = %02x %02x, want F3 C3 (DI; JP)", rom0[0], rom0[1])
	}

	m, err := NewDefault()
	if err != nil {
		t.Fatalf("NewDefault: %v", err)
	}
	if m.CPU.PC != 0x0000 || m.Mem.Read(0) != 0xF3 {
		t.Errorf("machine should reset at $0000 reading DI, PC=%#04x byte=%#02x", m.CPU.PC, m.Mem.Read(0))
	}

	// Smoke: the genuine ROM 3.0 runs a frame of its power-on code without
	// faulting and advances past the reset entry.
	m.RunFrame()
	if m.CPU.InstructionCount() == 0 {
		t.Error("ROM 3.0 did not execute any instructions")
	}
	if m.CPU.PC == 0x0000 {
		t.Error("PC stuck at reset entry after a frame")
	}
}
