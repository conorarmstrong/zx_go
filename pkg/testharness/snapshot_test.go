package testharness

import (
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// TestLoadSnapshotAppliesCPUState builds a minimal .z80 snapshot in
// memory, saves it to a temp file, and confirms LoadSnapshot pokes
// its register state into the harness CPU — including resetting any
// stale Halted flag the harness CPU had before the load (loaded
// snapshots always resume running, matching cmd/zx_go/main.go's
// applySnapshotToEmulator).
func TestLoadSnapshotAppliesCPUState(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Simulate the CPU having been left mid-HALT before the load.
	h.cpu.Halted = true

	s := snapshot.New()
	s.CPU.A = 0x42
	s.CPU.PC = 0x8000
	s.CPU.SP = 0xFF00
	s.CPU.BorderColor = 3
	s.Memory.Is128K = false

	path := filepath.Join(t.TempDir(), "snap.z80")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := h.LoadSnapshot(path); err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}

	if h.cpu.A != 0x42 {
		t.Errorf("A = %#02x, want 0x42", h.cpu.A)
	}
	if h.cpu.PC != 0x8000 {
		t.Errorf("PC = %#04x, want 0x8000", h.cpu.PC)
	}
	if h.cpu.Halted {
		t.Error("Halted = true after LoadSnapshot, want false (loaded snapshots resume running)")
	}
}
