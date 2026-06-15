package zx8x

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func TestZX80BootsWithoutPanic(t *testing.T) {
	m, err := NewMachine(roms.ModelZX80, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		m.RunFrame()
	}
	t.Logf("ZX80 PC=%04X I=%02X ink=%d", m.CPU.PC, m.CPU.I, inkCount(m))
}
