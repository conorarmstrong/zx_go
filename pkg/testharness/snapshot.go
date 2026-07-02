package testharness

import (
	"fmt"

	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// applySnapshotFile loads a snapshot from disk and pokes its state
// into the harness's CPU + memory. Mirrors cmd/zx_go/main.go's
// applySnapshotToEmulator helper but without the togglePause / Fyne
// dance.
func applySnapshotFile(h *Harness, path string) error {
	s := snapshot.New()
	if err := s.Load(path); err != nil {
		return fmt.Errorf("testharness: load snapshot %s: %w", path, err)
	}

	c := h.cpu
	c.A = s.CPU.A
	c.F = s.CPU.F
	c.B = s.CPU.B
	c.C = s.CPU.C
	c.D = s.CPU.D
	c.E = s.CPU.E
	c.H = s.CPU.H
	c.L = s.CPU.L
	c.A_ = s.CPU.A_
	c.F_ = s.CPU.F_
	c.B_ = s.CPU.B_
	c.C_ = s.CPU.C_
	c.D_ = s.CPU.D_
	c.E_ = s.CPU.E_
	c.H_ = s.CPU.H_
	c.L_ = s.CPU.L_
	c.IX = s.CPU.IX
	c.IY = s.CPU.IY
	c.SP = s.CPU.SP
	c.PC = s.CPU.PC
	c.I = s.CPU.I
	c.R = s.CPU.R
	c.IFF1 = s.CPU.IFF1
	c.Halted = s.CPU.Halted
	c.IFF2 = s.CPU.IFF2
	c.IM = s.CPU.IM

	for i := 0; i < 8; i++ {
		bank := h.mem.GetPage(i)
		copy(bank, s.Memory.RAM[i])
	}
	if s.Memory.Is128K {
		h.mem.PageMemory(s.Memory.Port7FFD)
	}
	h.ula.BorderColour = s.CPU.BorderColor
	return nil
}
