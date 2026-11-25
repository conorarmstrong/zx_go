package z80

import "testing"

// CPUAccessHook sees each memory read and write the CPU makes, with its data,
// and nothing else: the Next's +3 floating bus keeps the last such access to
// contended memory (zxnext.vhd:4496-4507, gated on cpu_mreq_n).
func TestCPUAccessHookSeesTheCPUsMemoryCycles(t *testing.T) {
	cpu, mem := createTestCPU()
	type acc struct {
		addr uint16
		val  byte
	}
	var got []acc
	cpu.CPUAccessHook = func(a uint16, v byte) { got = append(got, acc{a, v}) }
	mem.Write(0x8000, 0x77) // LD (HL),A, written directly: not a CPU cycle
	mem.Write(0x8001, 0x7E) // LD A,(HL)
	mem.Write(0x9000, 0x42)
	cpu.PC = 0x8000
	cpu.setHL(0x9000)
	cpu.A = 0x5A
	cpu.executeInstruction()
	cpu.executeInstruction()
	want := []acc{{0x8000, 0x77}, {0x9000, 0x5A}, {0x8001, 0x7E}, {0x9000, 0x5A}}
	if len(got) != len(want) {
		t.Fatalf("accesses %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("access %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
