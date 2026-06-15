package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func TestMixInt16Saturates(t *testing.T) {
	dst := []int16{0, 30000, -30000, 100}
	src := []int16{0, 10000, -10000, -50}
	mixInt16(dst, src)
	want := []int16{0, 32767, -32768, 50} // clamps at the ±range, adds in the middle
	for i := range want {
		if dst[i] != want[i] {
			t.Errorf("mixInt16[%d] = %d, want %d", i, dst[i], want[i])
		}
	}
}

type fakeSpeccyDAC struct {
	handlesCalls int
}

func (f *fakeSpeccyDAC) Handles(low byte) bool {
	f.handlesCalls++
	return low == 0xDF || low == 0xFB
}
func (f *fakeSpeccyDAC) Record(int, byte)               {}
func (f *fakeSpeccyDAC) Enabled() bool                  { return true }
func (f *fakeSpeccyDAC) GenerateFrame(s, _ int) []int16 { return make([]int16, s) }

// A write to a DAC port (low byte $DF / $FB) must be routed to the attached
// SpeccyDAC.
func TestSpeccyDACPortRouting(t *testing.T) {
	mem, err := memory.New("", roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	u := New(mem, keyboard.New())
	fake := &fakeSpeccyDAC{}
	u.SetSpeccyDAC(fake)

	u.WritePort(0x00DF, 0x42) // SpecDrum port
	if fake.handlesCalls == 0 {
		t.Error("write to $DF did not reach the SpeccyDAC")
	}
}
