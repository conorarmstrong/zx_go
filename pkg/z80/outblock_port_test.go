package z80

import "testing"

// OUTI/OUTD/OTIR/OTDR output to the port whose high byte is B *after* it is
// decremented (Sean Young, "Undocumented Z80 Documented" §3.4: the OUT block
// ops decrement B before the I/O write — the opposite of the IN block ops). The
// SAM Coupé boot ROM relies on this to load its 16-entry CLUT via OTDR with
// B=0x10 counting down to 0x00; getting it wrong shifts the whole palette by one
// (e.g. BASIC paper renders white instead of black).
func TestOUTI_PortUsesPostDecrementB(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	cpu.B, cpu.C = 0x10, 0xF8
	cpu.H, cpu.L = 0x90, 0x00
	mem.Write(0x9000, 0x55)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xA3) // OUTI
	cpu.executeInstruction()
	if v, ok := mu.ports[0x0FF8]; !ok || v != 0x55 {
		t.Errorf("OUTI: want port 0x0FF8=0x55 (B post-decrement); got ports=%v", mu.ports)
	}
	if _, ok := mu.ports[0x10F8]; ok {
		t.Errorf("OUTI: wrote to pre-decrement port 0x10F8 (should be 0x0FF8)")
	}
}

func TestOUTD_PortUsesPostDecrementB(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	cpu.B, cpu.C = 0x10, 0xF8
	cpu.H, cpu.L = 0x90, 0x00
	mem.Write(0x9000, 0x66)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xAB) // OUTD
	cpu.executeInstruction()
	if v, ok := mu.ports[0x0FF8]; !ok || v != 0x66 {
		t.Errorf("OUTD: want port 0x0FF8=0x66 (B post-decrement); got ports=%v", mu.ports)
	}
}
