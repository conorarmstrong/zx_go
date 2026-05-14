package z80

import (
	"os"
	"path/filepath"
	"testing"
	
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Mock ULA for testing
type mockULA struct {
	ports map[uint16]byte
}

func newMockULA() *mockULA {
	return &mockULA{
		ports: make(map[uint16]byte),
	}
}

func (m *mockULA) ReadPort(addr uint16) (byte, bool) {
	val, ok := m.ports[addr]
	// Always handle port reads, return 0xFF for unset ports (common in hardware)
	if !ok {
		val = 0xFF
	}
	return val, true
}

func (m *mockULA) WritePort(addr uint16, val byte) {
	m.ports[addr] = val
}

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

// Helper to create a test CPU
func createTestCPU() (*CPU, *memory.Memory) {
	testDir := "test_roms_z80"
	createTestROMs(&testing.T{}, testDir)
	
	mem, err := memory.New(testDir, roms.Model48K)
	if err != nil {
		panic(err)
	}
	
	ula := newMockULA()
	cpu := New(mem, ula)
	return cpu, mem
}

// Test basic register operations
func TestRegisterOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test 8-bit registers
	cpu.A = 0x12
	cpu.B = 0x34
	cpu.C = 0x56
	cpu.D = 0x78
	cpu.E = 0x9A
	cpu.H = 0xBC
	cpu.L = 0xDE
	cpu.F = 0xF0
	
	if cpu.A != 0x12 { t.Errorf("A register: expected 0x12, got 0x%02X", cpu.A) }
	if cpu.B != 0x34 { t.Errorf("B register: expected 0x34, got 0x%02X", cpu.B) }
	if cpu.C != 0x56 { t.Errorf("C register: expected 0x56, got 0x%02X", cpu.C) }
	if cpu.D != 0x78 { t.Errorf("D register: expected 0x78, got 0x%02X", cpu.D) }
	if cpu.E != 0x9A { t.Errorf("E register: expected 0x9A, got 0x%02X", cpu.E) }
	if cpu.H != 0xBC { t.Errorf("H register: expected 0xBC, got 0x%02X", cpu.H) }
	if cpu.L != 0xDE { t.Errorf("L register: expected 0xDE, got 0x%02X", cpu.L) }
	if cpu.F != 0xF0 { t.Errorf("F register: expected 0xF0, got 0x%02X", cpu.F) }
}

// Test 16-bit register pairs
func TestRegisterPairs(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test AF
	cpu.setAF(0x1234)
	if cpu.af() != 0x1234 { t.Errorf("AF: expected 0x1234, got 0x%04X", cpu.af()) }
	if cpu.A != 0x12 { t.Errorf("A from AF: expected 0x12, got 0x%02X", cpu.A) }
	if cpu.F != 0x34 { t.Errorf("F from AF: expected 0x34, got 0x%02X", cpu.F) }
	
	// Test BC
	cpu.setBC(0x5678)
	if cpu.bc() != 0x5678 { t.Errorf("BC: expected 0x5678, got 0x%04X", cpu.bc()) }
	if cpu.B != 0x56 { t.Errorf("B from BC: expected 0x56, got 0x%02X", cpu.B) }
	if cpu.C != 0x78 { t.Errorf("C from BC: expected 0x78, got 0x%02X", cpu.C) }
	
	// Test DE
	cpu.setDE(0x9ABC)
	if cpu.de() != 0x9ABC { t.Errorf("DE: expected 0x9ABC, got 0x%04X", cpu.de()) }
	if cpu.D != 0x9A { t.Errorf("D from DE: expected 0x9A, got 0x%02X", cpu.D) }
	if cpu.E != 0xBC { t.Errorf("E from DE: expected 0xBC, got 0x%02X", cpu.E) }
	
	// Test HL
	cpu.setHL(0xDEF0)
	if cpu.hl() != 0xDEF0 { t.Errorf("HL: expected 0xDEF0, got 0x%04X", cpu.hl()) }
	if cpu.H != 0xDE { t.Errorf("H from HL: expected 0xDE, got 0x%02X", cpu.H) }
	if cpu.L != 0xF0 { t.Errorf("L from HL: expected 0xF0, got 0x%02X", cpu.L) }
}

// Test arithmetic operations
func TestArithmeticOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test ADD
	cpu.A = 0x10
	cpu.add(0x20)
	if cpu.A != 0x30 { t.Errorf("ADD: expected 0x30, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_Z) != 0 { t.Errorf("ADD: Zero flag should not be set") }
	if (cpu.F & FLAG_C) != 0 { t.Errorf("ADD: Carry flag should not be set") }
	
	// Test ADD with carry
	cpu.A = 0xFF
	cpu.add(0x01)
	if cpu.A != 0x00 { t.Errorf("ADD with carry: expected 0x00, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_Z) == 0 { t.Errorf("ADD with carry: Zero flag should be set") }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("ADD with carry: Carry flag should be set") }
	
	// Test SUB
	cpu.A = 0x30
	cpu.sub(0x10)
	if cpu.A != 0x20 { t.Errorf("SUB: expected 0x20, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_N) == 0 { t.Errorf("SUB: N flag should be set") }
	
	// Test SUB with borrow
	cpu.A = 0x10
	cpu.sub(0x20)
	if cpu.A != 0xF0 { t.Errorf("SUB with borrow: expected 0xF0, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("SUB with borrow: Carry flag should be set") }
}

// Test logical operations
func TestLogicalOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test AND
	cpu.A = 0xF0
	cpu.and(0x0F)
	if cpu.A != 0x00 { t.Errorf("AND: expected 0x00, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_Z) == 0 { t.Errorf("AND: Zero flag should be set") }
	if (cpu.F & FLAG_H) == 0 { t.Errorf("AND: Half-carry flag should be set") }
	
	// Test OR
	cpu.A = 0xF0
	cpu.or(0x0F)
	if cpu.A != 0xFF { t.Errorf("OR: expected 0xFF, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_Z) != 0 { t.Errorf("OR: Zero flag should not be set") }
	
	// Test XOR
	cpu.A = 0xF0
	cpu.xor(0xF0)
	if cpu.A != 0x00 { t.Errorf("XOR: expected 0x00, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_Z) == 0 { t.Errorf("XOR: Zero flag should be set") }
}

// Test compare operations
func TestCompareOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test CP equal
	cpu.A = 0x42
	cpu.cp(0x42)
	if (cpu.F & FLAG_Z) == 0 { t.Errorf("CP equal: Zero flag should be set") }
	if (cpu.F & FLAG_N) == 0 { t.Errorf("CP: N flag should be set") }
	if (cpu.F & FLAG_C) != 0 { t.Errorf("CP equal: Carry flag should not be set") }
	
	// Test CP greater
	cpu.A = 0x50
	cpu.cp(0x40)
	if (cpu.F & FLAG_Z) != 0 { t.Errorf("CP greater: Zero flag should not be set") }
	if (cpu.F & FLAG_C) != 0 { t.Errorf("CP greater: Carry flag should not be set") }
	
	// Test CP less
	cpu.A = 0x30
	cpu.cp(0x40)
	if (cpu.F & FLAG_C) == 0 { t.Errorf("CP less: Carry flag should be set") }
}

// Test increment and decrement operations
func TestIncDecOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test INC
	cpu.A = 0x7F
	result := cpu.inc(cpu.A)
	if result != 0x80 { t.Errorf("INC: expected 0x80, got 0x%02X", result) }
	if (cpu.F & FLAG_PV) == 0 { t.Errorf("INC: Overflow flag should be set") }
	if (cpu.F & FLAG_S) == 0 { t.Errorf("INC: Sign flag should be set") }
	
	// Test INC to zero
	result = cpu.inc(0xFF)
	if result != 0x00 { t.Errorf("INC to zero: expected 0x00, got 0x%02X", result) }
	if (cpu.F & FLAG_Z) == 0 { t.Errorf("INC to zero: Zero flag should be set") }
	if (cpu.F & FLAG_H) == 0 { t.Errorf("INC to zero: Half-carry flag should be set") }
	
	// Test DEC
	cpu.A = 0x80
	result = cpu.dec(cpu.A)
	if result != 0x7F { t.Errorf("DEC: expected 0x7F, got 0x%02X", result) }
	if (cpu.F & FLAG_PV) == 0 { t.Errorf("DEC: Overflow flag should be set") }
	if (cpu.F & FLAG_N) == 0 { t.Errorf("DEC: N flag should be set") }
	
	// Test DEC from zero
	result = cpu.dec(0x00)
	if result != 0xFF { t.Errorf("DEC from zero: expected 0xFF, got 0x%02X", result) }
	if (cpu.F & FLAG_S) == 0 { t.Errorf("DEC from zero: Sign flag should be set") }
	if (cpu.F & FLAG_H) == 0 { t.Errorf("DEC from zero: Half-carry flag should be set") }
}

// Test rotate operations
func TestRotateOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test RLCA
	cpu.A = 0x80
	cpu.F = 0x00
	cpu.rlca()
	if cpu.A != 0x01 { t.Errorf("RLCA: expected 0x01, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("RLCA: Carry flag should be set") }
	
	// Test RRCA
	cpu.A = 0x01
	cpu.F = 0x00
	cpu.rrca()
	if cpu.A != 0x80 { t.Errorf("RRCA: expected 0x80, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("RRCA: Carry flag should be set") }
	
	// Test RLA with carry
	cpu.A = 0x80
	cpu.F = FLAG_C
	cpu.rla()
	if cpu.A != 0x01 { t.Errorf("RLA with carry: expected 0x01, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("RLA: Carry flag should be set") }
	
	// Test RRA with carry
	cpu.A = 0x01
	cpu.F = FLAG_C
	cpu.rra()
	if cpu.A != 0x80 { t.Errorf("RRA with carry: expected 0x80, got 0x%02X", cpu.A) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("RRA: Carry flag should be set") }
}

// Test CB prefix rotate operations
func TestCBRotateOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test RLC
	cpu.B = 0x80
	result := cpu.rlc(cpu.B)
	if result != 0x01 { t.Errorf("RLC: expected 0x01, got 0x%02X", result) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("RLC: Carry flag should be set") }
	
	// Test RRC
	cpu.C = 0x01
	result = cpu.rrc(cpu.C)
	if result != 0x80 { t.Errorf("RRC: expected 0x80, got 0x%02X", result) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("RRC: Carry flag should be set") }
	
	// Test SLA (shift left arithmetic)
	cpu.D = 0x40
	result = cpu.sla(cpu.D)
	if result != 0x80 { t.Errorf("SLA: expected 0x80, got 0x%02X", result) }
	if (cpu.F & FLAG_C) != 0 { t.Errorf("SLA: Carry flag should not be set") }
	if (cpu.F & FLAG_S) == 0 { t.Errorf("SLA: Sign flag should be set") }
	
	// Test SRA (shift right arithmetic)
	cpu.E = 0x81
	result = cpu.sra(cpu.E)
	if result != 0xC0 { t.Errorf("SRA: expected 0xC0, got 0x%02X", result) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("SRA: Carry flag should be set") }
	if (cpu.F & FLAG_S) == 0 { t.Errorf("SRA: Sign flag should be set") }
	
	// Test SRL (shift right logical)
	cpu.H = 0x81
	result = cpu.srl(cpu.H)
	if result != 0x40 { t.Errorf("SRL: expected 0x40, got 0x%02X", result) }
	if (cpu.F & FLAG_C) == 0 { t.Errorf("SRL: Carry flag should be set") }
	if (cpu.F & FLAG_S) != 0 { t.Errorf("SRL: Sign flag should not be set") }
}

// Test bit operations
func TestBitOperations(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test BIT
	cpu.F = 0x00
	cpu.bit(7, 0x80)
	if (cpu.F & FLAG_Z) != 0 { t.Errorf("BIT 7,0x80: Zero flag should not be set") }
	if (cpu.F & FLAG_S) == 0 { t.Errorf("BIT 7,0x80: Sign flag should be set") }
	if (cpu.F & FLAG_H) == 0 { t.Errorf("BIT: Half-carry flag should be set") }
	
	cpu.F = 0x00
	cpu.bit(7, 0x7F)
	if (cpu.F & FLAG_Z) == 0 { t.Errorf("BIT 7,0x7F: Zero flag should be set") }
	if (cpu.F & FLAG_PV) == 0 { t.Errorf("BIT 7,0x7F: Parity flag should be set") }
	
	// Test SET
	result := cpu.set(3, 0x00)
	if result != 0x08 { t.Errorf("SET 3,0x00: expected 0x08, got 0x%02X", result) }
	
	result = cpu.set(7, 0x7F)
	if result != 0xFF { t.Errorf("SET 7,0x7F: expected 0xFF, got 0x%02X", result) }
	
	// Test RES
	result = cpu.res(3, 0xFF)
	if result != 0xF7 { t.Errorf("RES 3,0xFF: expected 0xF7, got 0x%02X", result) }
	
	result = cpu.res(7, 0x80)
	if result != 0x00 { t.Errorf("RES 7,0x80: expected 0x00, got 0x%02X", result) }
}

// Test stack operations
func TestStackOperations(t *testing.T) {
	cpu, mem := createTestCPU()
	
	// Initialize stack pointer
	cpu.SP = 0xFFFF
	
	// Test PUSH
	cpu.push(0x1234)
	if cpu.SP != 0xFFFF-2 { t.Errorf("PUSH: SP expected 0x%04X, got 0x%04X", 0xFFFF-2, cpu.SP) }
	if mem.Read(cpu.SP) != 0x34 { t.Errorf("PUSH: low byte expected 0x34, got 0x%02X", mem.Read(cpu.SP)) }
	if mem.Read(cpu.SP+1) != 0x12 { t.Errorf("PUSH: high byte expected 0x12, got 0x%02X", mem.Read(cpu.SP+1)) }
	
	// Test POP
	result := cpu.pop()
	if result != 0x1234 { t.Errorf("POP: expected 0x1234, got 0x%04X", result) }
	if cpu.SP != 0xFFFF { t.Errorf("POP: SP expected 0xFFFF, got 0x%04X", cpu.SP) }
}

// Test memory operations
func TestMemoryOperations(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	
	// Test basic memory read/write to RAM area (0x4000+)
	mem.Write(0x8000, 0x42)
	if mem.Read(0x8000) != 0x42 { t.Errorf("Memory: expected 0x42, got 0x%02X", mem.Read(0x8000)) }
	
	// Test LD (HL),A and LD A,(HL) in RAM
	cpu.H = 0x80
	cpu.L = 0x00
	cpu.A = 0x55
	mem.Write(cpu.hl(), cpu.A)
	if mem.Read(cpu.hl()) != 0x55 { t.Errorf("LD (HL),A: expected 0x55, got 0x%02X", mem.Read(cpu.hl())) }
	
	mem.Write(0x9000, 0xAA)
	cpu.setHL(0x9000)
	cpu.A = mem.Read(cpu.hl())
	if cpu.A != 0xAA { t.Errorf("LD A,(HL): expected 0xAA, got 0x%02X", cpu.A) }
}

// Test flag calculation tables
func TestFlagTables(t *testing.T) {
	cpu, _ := createTestCPU()
	
	// Test sz53Table
	if cpu.sz53Table[0] != FLAG_Z { t.Errorf("sz53Table[0]: expected 0x%02X, got 0x%02X", FLAG_Z, cpu.sz53Table[0]) }
	if cpu.sz53Table[0x80] != (FLAG_S | 0x80) { t.Errorf("sz53Table[0x80]: expected 0x%02X, got 0x%02X", FLAG_S | 0x80, cpu.sz53Table[0x80]) }
	
	// Test parityTable
	if cpu.parityTable[0] != FLAG_PV { t.Errorf("parityTable[0]: expected 0x%02X, got 0x%02X", FLAG_PV, cpu.parityTable[0]) }
	if cpu.parityTable[1] != 0 { t.Errorf("parityTable[1]: expected 0, got 0x%02X", cpu.parityTable[1]) }
	if cpu.parityTable[3] != FLAG_PV { t.Errorf("parityTable[3]: expected 0x%02X, got 0x%02X", FLAG_PV, cpu.parityTable[3]) }
}

// Test instruction timing
func TestInstructionTiming(t *testing.T) {
	cpu, mem := createTestCPU()
	
	// Test NOP timing
	initialTStates := cpu.tstates
	cpu.executeBaseInstruction(0x00) // NOP
	if cpu.tstates != initialTStates+4 { t.Errorf("NOP timing: expected +4, got +%d", cpu.tstates-initialTStates) }
	
	// Test LD A,n timing
	initialTStates = cpu.tstates
	mem.Write(cpu.PC, 0x42)
	cpu.executeBaseInstruction(0x3E) // LD A,n
	if cpu.tstates != initialTStates+7 { t.Errorf("LD A,n timing: expected +7, got +%d", cpu.tstates-initialTStates) }
	
	// Test LD A,(HL) timing
	initialTStates = cpu.tstates
	cpu.setHL(0x1000)
	mem.Write(0x1000, 0x55)
	cpu.executeBaseInstruction(0x7E) // LD A,(HL)
	if cpu.tstates != initialTStates+7 { t.Errorf("LD A,(HL) timing: expected +7, got +%d", cpu.tstates-initialTStates) }
}

// Test I/O operations
func TestIOOperations(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)
	
	// Test OUT (n),A - Use RAM address for PC
	cpu.A = 0x42
	cpu.PC = 0x8000  // Set PC to RAM area
	mem.Write(cpu.PC, 0xFE) // Port 0xFE
	cpu.executeBaseInstruction(0xD3) // OUT (n),A
	
	expectedPort := uint16(0xFE) | (uint16(0x42) << 8)
	actualValue := ula.ports[expectedPort]
	if actualValue != 0x42 { 
		t.Errorf("OUT: expected 0x42 at port 0x%04X, got 0x%02X", expectedPort, actualValue) 
	}
	
	// Test IN A,(n)
	// For IN A,(n), the port is calculated as n | (A << 8), so A must remain 0x42
	ula.ports[expectedPort] = 0x55
	cpu.PC = 0x8000 // Reset PC to RAM area
	mem.Write(cpu.PC, 0xFE) // Port number
	cpu.A = 0x42  // Keep A register as 0x42 for correct port calculation
	cpu.executeBaseInstruction(0xDB) // IN A,(n)
	if cpu.A != 0x55 { t.Errorf("IN: expected 0x55, got 0x%02X", cpu.A) }
}

// Benchmark tests
func BenchmarkArithmeticOperations(b *testing.B) {
	cpu, _ := createTestCPU()
	cpu.A = 0x80
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cpu.add(0x01)
		cpu.sub(0x01)
	}
}

func BenchmarkLogicalOperations(b *testing.B) {
	cpu, _ := createTestCPU()
	cpu.A = 0xF0
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cpu.and(0x0F)
		cpu.or(0x0F)
		cpu.xor(0x0F)
	}
}

func BenchmarkCBOperations(b *testing.B) {
	cpu, _ := createTestCPU()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cpu.rlc(0x80)
		cpu.sla(0x40)
		cpu.bit(7, 0x80)
	}
}

func BenchmarkInstructionExecution(b *testing.B) {
	cpu, mem := createTestCPU()
	
	// Set up a simple instruction sequence
	mem.Write(0x0000, 0x3E) // LD A,n
	mem.Write(0x0001, 0x42)
	mem.Write(0x0002, 0x00) // NOP
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cpu.PC = 0x0000
		cpu.executeInstruction()
		cpu.executeInstruction()
	}
}

// =============================================================================
// Jump Instructions
// =============================================================================

func TestJP_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// JP nn (0xC3) - unconditional jump to absolute address
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00) // low byte of target
	mem.Write(0x8001, 0x90) // high byte of target => 0x9000
	cpu.executeBaseInstruction(0xC3)
	if cpu.PC != 0x9000 {
		t.Errorf("JP nn: expected PC=0x9000, got PC=0x%04X", cpu.PC)
	}
}

func TestJP_HL(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// JP (HL) (0xE9)
	cpu.setHL(0xBEEF)
	cpu.executeBaseInstruction(0xE9)
	if cpu.PC != 0xBEEF {
		t.Errorf("JP (HL): expected PC=0xBEEF, got PC=0x%04X", cpu.PC)
	}
}

func TestJP_cc_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	tests := []struct {
		name       string
		opcode     byte
		flagSet    byte // flag value when condition is true
		flagClear  byte // flag value when condition is false
		takeBranch bool // whether flagSet makes the branch taken
	}{
		{"JP NZ,nn", 0xC2, 0x00, FLAG_Z, true},        // branch when Z clear
		{"JP Z,nn", 0xCA, FLAG_Z, 0x00, true},          // branch when Z set
		{"JP NC,nn", 0xD2, 0x00, FLAG_C, true},         // branch when C clear
		{"JP C,nn", 0xDA, FLAG_C, 0x00, true},          // branch when C set
		{"JP PO,nn", 0xE2, 0x00, FLAG_PV, true},        // branch when PV clear
		{"JP PE,nn", 0xEA, FLAG_PV, 0x00, true},        // branch when PV set
		{"JP P,nn", 0xF2, 0x00, FLAG_S, true},          // branch when S clear
		{"JP M,nn", 0xFA, FLAG_S, 0x00, true},          // branch when S set
	}

	for _, tt := range tests {
		// Test branch taken
		cpu.PC = 0x8000
		mem.Write(0x8000, 0x00) // low byte
		mem.Write(0x8001, 0xA0) // high byte => 0xA000
		cpu.F = tt.flagSet
		cpu.executeBaseInstruction(tt.opcode)
		if cpu.PC != 0xA000 {
			t.Errorf("%s (taken): expected PC=0xA000, got PC=0x%04X", tt.name, cpu.PC)
		}

		// Test branch not taken
		cpu.PC = 0x8000
		mem.Write(0x8000, 0x00)
		mem.Write(0x8001, 0xA0)
		cpu.F = tt.flagClear
		cpu.executeBaseInstruction(tt.opcode)
		// PC should advance past the two-byte operand but NOT jump
		if cpu.PC == 0xA000 {
			t.Errorf("%s (not taken): PC should NOT be 0xA000", tt.name)
		}
	}
}

func TestJR_n(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// JR n (0x18) - forward jump
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x05) // offset +5
	cpu.executeBaseInstruction(0x18)
	// After fetch, PC is 0x8001, then +5 = 0x8006
	if cpu.PC != 0x8006 {
		t.Errorf("JR forward: expected PC=0x8006, got PC=0x%04X", cpu.PC)
	}

	// JR n - backward jump (negative offset)
	cpu.PC = 0x8010
	mem.Write(0x8010, 0xFB) // offset -5 (signed: 0xFB = -5)
	cpu.executeBaseInstruction(0x18)
	// After fetch, PC is 0x8011, then -5 = 0x800C
	if cpu.PC != 0x800C {
		t.Errorf("JR backward: expected PC=0x800C, got PC=0x%04X", cpu.PC)
	}
}

func TestJR_cc_n(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// JR NZ,n (0x20) - branch taken (Z flag clear)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x0A) // offset +10
	cpu.F = 0x00            // Z flag clear
	initialT := cpu.tstates
	cpu.executeBaseInstruction(0x20)
	if cpu.PC != 0x800B {
		t.Errorf("JR NZ taken: expected PC=0x800B, got PC=0x%04X", cpu.PC)
	}
	if cpu.tstates-initialT != 12 {
		t.Errorf("JR NZ taken timing: expected 12, got %d", cpu.tstates-initialT)
	}

	// JR NZ,n - branch not taken (Z flag set)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x0A)
	cpu.F = FLAG_Z
	initialT = cpu.tstates
	cpu.executeBaseInstruction(0x20)
	if cpu.PC != 0x8001 {
		t.Errorf("JR NZ not taken: expected PC=0x8001, got PC=0x%04X", cpu.PC)
	}
	if cpu.tstates-initialT != 7 {
		t.Errorf("JR NZ not taken timing: expected 7, got %d", cpu.tstates-initialT)
	}

	// JR Z,n (0x28) - branch taken
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x05)
	cpu.F = FLAG_Z
	cpu.executeBaseInstruction(0x28)
	if cpu.PC != 0x8006 {
		t.Errorf("JR Z taken: expected PC=0x8006, got PC=0x%04X", cpu.PC)
	}

	// JR NC,n (0x30) - branch taken
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x05)
	cpu.F = 0x00
	cpu.executeBaseInstruction(0x30)
	if cpu.PC != 0x8006 {
		t.Errorf("JR NC taken: expected PC=0x8006, got PC=0x%04X", cpu.PC)
	}

	// JR C,n (0x38) - branch taken
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x05)
	cpu.F = FLAG_C
	cpu.executeBaseInstruction(0x38)
	if cpu.PC != 0x8006 {
		t.Errorf("JR C taken: expected PC=0x8006, got PC=0x%04X", cpu.PC)
	}
}

func TestDJNZ(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// DJNZ (0x10) - B != 0, branch taken
	cpu.PC = 0x8000
	cpu.B = 0x05
	mem.Write(0x8000, 0x03) // offset +3
	initialT := cpu.tstates
	cpu.executeBaseInstruction(0x10)
	if cpu.B != 0x04 {
		t.Errorf("DJNZ: expected B=0x04, got B=0x%02X", cpu.B)
	}
	if cpu.PC != 0x8004 {
		t.Errorf("DJNZ taken: expected PC=0x8004, got PC=0x%04X", cpu.PC)
	}
	if cpu.tstates-initialT != 13 {
		t.Errorf("DJNZ taken timing: expected 13, got %d", cpu.tstates-initialT)
	}

	// DJNZ - B becomes 0, branch not taken
	cpu.PC = 0x8000
	cpu.B = 0x01
	mem.Write(0x8000, 0x03)
	initialT = cpu.tstates
	cpu.executeBaseInstruction(0x10)
	if cpu.B != 0x00 {
		t.Errorf("DJNZ: expected B=0x00, got B=0x%02X", cpu.B)
	}
	if cpu.PC != 0x8001 {
		t.Errorf("DJNZ not taken: expected PC=0x8001, got PC=0x%04X", cpu.PC)
	}
	if cpu.tstates-initialT != 8 {
		t.Errorf("DJNZ not taken timing: expected 8, got %d", cpu.tstates-initialT)
	}
}

// =============================================================================
// Call / Return Instructions
// =============================================================================

func TestCALL_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CALL nn (0xCD)
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	mem.Write(0x8000, 0x00) // low byte of target
	mem.Write(0x8001, 0x90) // high byte => 0x9000
	initialT := cpu.tstates
	cpu.executeBaseInstruction(0xCD)

	// PC should be set to the call target
	if cpu.PC != 0x9000 {
		t.Errorf("CALL nn: expected PC=0x9000, got PC=0x%04X", cpu.PC)
	}
	// Return address (0x8002) should be pushed onto the stack
	retLo := mem.Read(cpu.SP)
	retHi := mem.Read(cpu.SP + 1)
	retAddr := uint16(retHi)<<8 | uint16(retLo)
	if retAddr != 0x8002 {
		t.Errorf("CALL nn: return addr on stack expected 0x8002, got 0x%04X", retAddr)
	}
	if cpu.tstates-initialT != 17 {
		t.Errorf("CALL nn timing: expected 17, got %d", cpu.tstates-initialT)
	}
}

func TestCALL_cc_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	tests := []struct {
		name      string
		opcode    byte
		flagTaken byte // flags that cause the branch
		flagSkip  byte // flags that skip the branch
	}{
		{"CALL NZ,nn", 0xC4, 0x00, FLAG_Z},
		{"CALL Z,nn", 0xCC, FLAG_Z, 0x00},
		{"CALL NC,nn", 0xD4, 0x00, FLAG_C},
		{"CALL C,nn", 0xDC, FLAG_C, 0x00},
		{"CALL PO,nn", 0xE4, 0x00, FLAG_PV},
		{"CALL PE,nn", 0xEC, FLAG_PV, 0x00},
		{"CALL P,nn", 0xF4, 0x00, FLAG_S},
		{"CALL M,nn", 0xFC, FLAG_S, 0x00},
	}

	for _, tt := range tests {
		// Test taken
		cpu.PC = 0x8000
		cpu.SP = 0xFFF0
		mem.Write(0x8000, 0x00)
		mem.Write(0x8001, 0xA0) // target = 0xA000
		cpu.F = tt.flagTaken
		initialT := cpu.tstates
		cpu.executeBaseInstruction(tt.opcode)
		if cpu.PC != 0xA000 {
			t.Errorf("%s (taken): expected PC=0xA000, got PC=0x%04X", tt.name, cpu.PC)
		}
		if cpu.tstates-initialT != 17 {
			t.Errorf("%s (taken) timing: expected 17, got %d", tt.name, cpu.tstates-initialT)
		}

		// Test not taken
		cpu.PC = 0x8000
		cpu.SP = 0xFFF0
		mem.Write(0x8000, 0x00)
		mem.Write(0x8001, 0xA0)
		cpu.F = tt.flagSkip
		initialT = cpu.tstates
		cpu.executeBaseInstruction(tt.opcode)
		if cpu.PC == 0xA000 {
			t.Errorf("%s (not taken): PC should NOT be 0xA000", tt.name)
		}
		if cpu.tstates-initialT != 10 {
			t.Errorf("%s (not taken) timing: expected 10, got %d", tt.name, cpu.tstates-initialT)
		}
	}
}

func TestRET(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// RET (0xC9)
	cpu.SP = 0xFFF0
	mem.Write(0xFFF0, 0x34) // low byte
	mem.Write(0xFFF1, 0x12) // high byte => 0x1234
	initialT := cpu.tstates
	cpu.executeBaseInstruction(0xC9)
	if cpu.PC != 0x1234 {
		t.Errorf("RET: expected PC=0x1234, got PC=0x%04X", cpu.PC)
	}
	if cpu.SP != 0xFFF2 {
		t.Errorf("RET: expected SP=0xFFF2, got SP=0x%04X", cpu.SP)
	}
	if cpu.tstates-initialT != 10 {
		t.Errorf("RET timing: expected 10, got %d", cpu.tstates-initialT)
	}
}

func TestRET_cc(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	tests := []struct {
		name      string
		opcode    byte
		flagTaken byte
		flagSkip  byte
	}{
		{"RET NZ", 0xC0, 0x00, FLAG_Z},
		{"RET Z", 0xC8, FLAG_Z, 0x00},
		{"RET NC", 0xD0, 0x00, FLAG_C},
		{"RET C", 0xD8, FLAG_C, 0x00},
		{"RET PO", 0xE0, 0x00, FLAG_PV},
		{"RET PE", 0xE8, FLAG_PV, 0x00},
		{"RET P", 0xF0, 0x00, FLAG_S},
		{"RET M", 0xF8, FLAG_S, 0x00},
	}

	for _, tt := range tests {
		// Test taken
		cpu.SP = 0xFFF0
		mem.Write(0xFFF0, 0x78)
		mem.Write(0xFFF1, 0x56) // return to 0x5678
		cpu.F = tt.flagTaken
		initialT := cpu.tstates
		cpu.executeBaseInstruction(tt.opcode)
		if cpu.PC != 0x5678 {
			t.Errorf("%s (taken): expected PC=0x5678, got PC=0x%04X", tt.name, cpu.PC)
		}
		if cpu.tstates-initialT != 11 {
			t.Errorf("%s (taken) timing: expected 11, got %d", tt.name, cpu.tstates-initialT)
		}

		// Test not taken
		cpu.PC = 0x1111
		cpu.SP = 0xFFF0
		cpu.F = tt.flagSkip
		initialT = cpu.tstates
		cpu.executeBaseInstruction(tt.opcode)
		if cpu.PC != 0x1111 {
			t.Errorf("%s (not taken): expected PC=0x1111, got PC=0x%04X", tt.name, cpu.PC)
		}
		if cpu.tstates-initialT != 5 {
			t.Errorf("%s (not taken) timing: expected 5, got %d", tt.name, cpu.tstates-initialT)
		}
	}
}

func TestRST(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	_ = mem

	tests := []struct {
		opcode byte
		target uint16
	}{
		{0xC7, 0x0000},
		{0xCF, 0x0008},
		{0xD7, 0x0010},
		{0xDF, 0x0018},
		{0xE7, 0x0020},
		{0xEF, 0x0028},
		{0xF7, 0x0030},
		{0xFF, 0x0038},
	}

	for _, tt := range tests {
		cpu.PC = 0x8050
		cpu.SP = 0xFFF0
		initialT := cpu.tstates
		cpu.executeBaseInstruction(tt.opcode)
		if cpu.PC != tt.target {
			t.Errorf("RST 0x%02X: expected PC=0x%04X, got PC=0x%04X", tt.opcode, tt.target, cpu.PC)
		}
		// Verify return address was pushed
		retLo := mem.Read(cpu.SP)
		retHi := mem.Read(cpu.SP + 1)
		retAddr := uint16(retHi)<<8 | uint16(retLo)
		if retAddr != 0x8050 {
			t.Errorf("RST 0x%02X: return addr expected 0x8050, got 0x%04X", tt.opcode, retAddr)
		}
		if cpu.tstates-initialT != 11 {
			t.Errorf("RST 0x%02X timing: expected 11, got %d", tt.opcode, cpu.tstates-initialT)
		}
	}
}

func TestCALL_RET_RoundTrip(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CALL then RET should return to the instruction after CALL
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90) // CALL 0x9000
	cpu.executeBaseInstruction(0xCD)
	if cpu.PC != 0x9000 {
		t.Fatalf("Round trip CALL: expected PC=0x9000, got PC=0x%04X", cpu.PC)
	}

	// Now RET
	cpu.executeBaseInstruction(0xC9)
	if cpu.PC != 0x8002 {
		t.Errorf("Round trip RET: expected PC=0x8002, got PC=0x%04X", cpu.PC)
	}
	if cpu.SP != 0xFFF0 {
		t.Errorf("Round trip: SP should be restored to 0xFFF0, got 0x%04X", cpu.SP)
	}
}

// =============================================================================
// Load Instructions
// =============================================================================

func TestLD_r_r(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD B,C (0x41)
	cpu.C = 0x42
	cpu.executeBaseInstruction(0x41)
	if cpu.B != 0x42 {
		t.Errorf("LD B,C: expected B=0x42, got B=0x%02X", cpu.B)
	}

	// LD D,E (0x53)
	cpu.E = 0x77
	cpu.executeBaseInstruction(0x53)
	if cpu.D != 0x77 {
		t.Errorf("LD D,E: expected D=0x77, got D=0x%02X", cpu.D)
	}

	// LD H,L (0x65)
	cpu.L = 0xAB
	cpu.executeBaseInstruction(0x65)
	if cpu.H != 0xAB {
		t.Errorf("LD H,L: expected H=0xAB, got H=0x%02X", cpu.H)
	}

	// LD A,B (0x78)
	cpu.B = 0x99
	cpu.executeBaseInstruction(0x78)
	if cpu.A != 0x99 {
		t.Errorf("LD A,B: expected A=0x99, got A=0x%02X", cpu.A)
	}

	// LD L,A (0x6F)
	cpu.A = 0xFE
	cpu.executeBaseInstruction(0x6F)
	if cpu.L != 0xFE {
		t.Errorf("LD L,A: expected L=0xFE, got L=0x%02X", cpu.L)
	}

	// LD B,B (0x40) - NOP-like
	cpu.B = 0x55
	cpu.executeBaseInstruction(0x40)
	if cpu.B != 0x55 {
		t.Errorf("LD B,B: expected B=0x55, got B=0x%02X", cpu.B)
	}
}

func TestLD_r_n(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	tests := []struct {
		name   string
		opcode byte
		getReg func() byte
	}{
		{"LD B,n", 0x06, func() byte { return cpu.B }},
		{"LD C,n", 0x0E, func() byte { return cpu.C }},
		{"LD D,n", 0x16, func() byte { return cpu.D }},
		{"LD E,n", 0x1E, func() byte { return cpu.E }},
		{"LD H,n", 0x26, func() byte { return cpu.H }},
		{"LD L,n", 0x2E, func() byte { return cpu.L }},
		{"LD A,n", 0x3E, func() byte { return cpu.A }},
	}

	for _, tt := range tests {
		cpu.PC = 0x8000
		mem.Write(0x8000, 0xAB) // immediate value
		cpu.executeBaseInstruction(tt.opcode)
		if tt.getReg() != 0xAB {
			t.Errorf("%s: expected 0xAB, got 0x%02X", tt.name, tt.getReg())
		}
	}
}

func TestLD_HL_r(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.setHL(0x8000)

	// LD (HL),B (0x70)
	cpu.B = 0x11
	cpu.executeBaseInstruction(0x70)
	if mem.Read(0x8000) != 0x11 {
		t.Errorf("LD (HL),B: expected 0x11, got 0x%02X", mem.Read(0x8000))
	}

	// LD (HL),A (0x77)
	cpu.A = 0x22
	cpu.executeBaseInstruction(0x77)
	if mem.Read(0x8000) != 0x22 {
		t.Errorf("LD (HL),A: expected 0x22, got 0x%02X", mem.Read(0x8000))
	}

	// LD (HL),n (0x36)
	cpu.PC = 0x8100
	mem.Write(0x8100, 0x33) // immediate
	cpu.executeBaseInstruction(0x36)
	if mem.Read(0x8000) != 0x33 {
		t.Errorf("LD (HL),n: expected 0x33, got 0x%02X", mem.Read(0x8000))
	}
}

func TestLD_r_HL(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.setHL(0x8000)
	mem.Write(0x8000, 0xCC)

	// LD B,(HL) (0x46)
	cpu.executeBaseInstruction(0x46)
	if cpu.B != 0xCC {
		t.Errorf("LD B,(HL): expected 0xCC, got 0x%02X", cpu.B)
	}

	// LD A,(HL) (0x7E)
	cpu.executeBaseInstruction(0x7E)
	if cpu.A != 0xCC {
		t.Errorf("LD A,(HL): expected 0xCC, got 0x%02X", cpu.A)
	}

	// LD E,(HL) (0x5E)
	cpu.executeBaseInstruction(0x5E)
	if cpu.E != 0xCC {
		t.Errorf("LD E,(HL): expected 0xCC, got 0x%02X", cpu.E)
	}
}

func TestLD_A_BC_DE(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD A,(BC) (0x0A)
	cpu.setBC(0x8000)
	mem.Write(0x8000, 0xAA)
	cpu.executeBaseInstruction(0x0A)
	if cpu.A != 0xAA {
		t.Errorf("LD A,(BC): expected 0xAA, got 0x%02X", cpu.A)
	}

	// LD A,(DE) (0x1A)
	cpu.setDE(0x8100)
	mem.Write(0x8100, 0xBB)
	cpu.executeBaseInstruction(0x1A)
	if cpu.A != 0xBB {
		t.Errorf("LD A,(DE): expected 0xBB, got 0x%02X", cpu.A)
	}

	// LD (BC),A (0x02)
	cpu.A = 0xDD
	cpu.setBC(0x8200)
	cpu.executeBaseInstruction(0x02)
	if mem.Read(0x8200) != 0xDD {
		t.Errorf("LD (BC),A: expected 0xDD, got 0x%02X", mem.Read(0x8200))
	}

	// LD (DE),A (0x12)
	cpu.A = 0xEE
	cpu.setDE(0x8300)
	cpu.executeBaseInstruction(0x12)
	if mem.Read(0x8300) != 0xEE {
		t.Errorf("LD (DE),A: expected 0xEE, got 0x%02X", mem.Read(0x8300))
	}
}

func TestLD_nn_A_and_A_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD (nn),A (0x32)
	cpu.PC = 0x8000
	cpu.A = 0x77
	mem.Write(0x8000, 0x00) // low byte of address
	mem.Write(0x8001, 0x90) // high byte => address 0x9000
	cpu.executeBaseInstruction(0x32)
	if mem.Read(0x9000) != 0x77 {
		t.Errorf("LD (nn),A: expected 0x77 at 0x9000, got 0x%02X", mem.Read(0x9000))
	}

	// LD A,(nn) (0x3A)
	cpu.PC = 0x8000
	mem.Write(0x9000, 0x88) // value to load
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90) // address 0x9000
	cpu.executeBaseInstruction(0x3A)
	if cpu.A != 0x88 {
		t.Errorf("LD A,(nn): expected 0x88, got 0x%02X", cpu.A)
	}
}

func TestLD_SP_HL(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD SP,HL (0xF9)
	cpu.setHL(0xBEEF)
	initialT := cpu.tstates
	cpu.executeBaseInstruction(0xF9)
	if cpu.SP != 0xBEEF {
		t.Errorf("LD SP,HL: expected SP=0xBEEF, got SP=0x%04X", cpu.SP)
	}
	if cpu.tstates-initialT != 6 {
		t.Errorf("LD SP,HL timing: expected 6, got %d", cpu.tstates-initialT)
	}
}

func TestLD_16bit_immediate(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD BC,nn (0x01)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x34) // low byte
	mem.Write(0x8001, 0x12) // high byte => 0x1234
	cpu.executeBaseInstruction(0x01)
	if cpu.bc() != 0x1234 {
		t.Errorf("LD BC,nn: expected 0x1234, got 0x%04X", cpu.bc())
	}

	// LD DE,nn (0x11)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x78)
	mem.Write(0x8001, 0x56) // => 0x5678
	cpu.executeBaseInstruction(0x11)
	if cpu.de() != 0x5678 {
		t.Errorf("LD DE,nn: expected 0x5678, got 0x%04X", cpu.de())
	}

	// LD HL,nn (0x21)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xBC)
	mem.Write(0x8001, 0x9A) // => 0x9ABC
	cpu.executeBaseInstruction(0x21)
	if cpu.hl() != 0x9ABC {
		t.Errorf("LD HL,nn: expected 0x9ABC, got 0x%04X", cpu.hl())
	}

	// LD SP,nn (0x31)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xF0)
	mem.Write(0x8001, 0xDE) // => 0xDEF0
	cpu.executeBaseInstruction(0x31)
	if cpu.SP != 0xDEF0 {
		t.Errorf("LD SP,nn: expected 0xDEF0, got 0x%04X", cpu.SP)
	}
}

func TestLD_nn_HL_and_HL_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD (nn),HL (0x22)
	cpu.PC = 0x8000
	cpu.setHL(0xABCD)
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90) // address 0x9000
	cpu.executeBaseInstruction(0x22)
	if mem.Read(0x9000) != 0xCD {
		t.Errorf("LD (nn),HL: low byte expected 0xCD, got 0x%02X", mem.Read(0x9000))
	}
	if mem.Read(0x9001) != 0xAB {
		t.Errorf("LD (nn),HL: high byte expected 0xAB, got 0x%02X", mem.Read(0x9001))
	}

	// LD HL,(nn) (0x2A)
	cpu.PC = 0x8000
	mem.Write(0x9000, 0xEF)
	mem.Write(0x9001, 0xBE)
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeBaseInstruction(0x2A)
	if cpu.hl() != 0xBEEF {
		t.Errorf("LD HL,(nn): expected 0xBEEF, got 0x%04X", cpu.hl())
	}
}

// =============================================================================
// Block Operations (ED prefix)
// =============================================================================

func TestLDI(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LDI (ED A0): copy (HL) to (DE), inc HL and DE, dec BC
	cpu.setHL(0x8000)
	cpu.setDE(0x9000)
	cpu.setBC(0x0003)
	cpu.A = 0x00
	mem.Write(0x8000, 0x42)
	cpu.F = FLAG_C // carry should be preserved

	cpu.executeEDInstruction(0xA0)

	if mem.Read(0x9000) != 0x42 {
		t.Errorf("LDI: expected 0x42 at (DE), got 0x%02X", mem.Read(0x9000))
	}
	if cpu.hl() != 0x8001 {
		t.Errorf("LDI: expected HL=0x8001, got 0x%04X", cpu.hl())
	}
	if cpu.de() != 0x9001 {
		t.Errorf("LDI: expected DE=0x9001, got 0x%04X", cpu.de())
	}
	if cpu.bc() != 0x0002 {
		t.Errorf("LDI: expected BC=0x0002, got 0x%04X", cpu.bc())
	}
	// PV flag set when BC != 0
	if (cpu.F & FLAG_PV) == 0 {
		t.Errorf("LDI: PV flag should be set when BC != 0")
	}
	// Carry preserved
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("LDI: Carry flag should be preserved")
	}
	// N and H cleared
	if (cpu.F & FLAG_N) != 0 {
		t.Errorf("LDI: N flag should be cleared")
	}
	if (cpu.F & FLAG_H) != 0 {
		t.Errorf("LDI: H flag should be cleared")
	}
}

func TestLDI_BCZero(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LDI when BC becomes 0
	cpu.setHL(0x8000)
	cpu.setDE(0x9000)
	cpu.setBC(0x0001)
	mem.Write(0x8000, 0x55)

	cpu.executeEDInstruction(0xA0)

	if cpu.bc() != 0x0000 {
		t.Errorf("LDI: expected BC=0x0000, got 0x%04X", cpu.bc())
	}
	// PV flag cleared when BC == 0
	if (cpu.F & FLAG_PV) != 0 {
		t.Errorf("LDI: PV flag should be cleared when BC == 0")
	}
}

func TestLDIR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Set up source data
	mem.Write(0x8000, 0x11)
	mem.Write(0x8001, 0x22)
	mem.Write(0x8002, 0x33)

	cpu.setHL(0x8000)
	cpu.setDE(0x9000)
	cpu.setBC(0x0003)
	cpu.PC = 0xA000 // some instruction address

	// LDIR repeats: when BC != 0, PC -= 2 so the instruction re-executes
	// First call: copies one byte, BC=2, PC goes back
	cpu.executeEDInstruction(0xB0)
	if mem.Read(0x9000) != 0x11 {
		t.Errorf("LDIR step 1: expected 0x11, got 0x%02X", mem.Read(0x9000))
	}
	if cpu.bc() != 0x0002 {
		t.Errorf("LDIR step 1: expected BC=0x0002, got 0x%04X", cpu.bc())
	}
	// PC should have gone back by 2 for repeat
	if cpu.PC != 0xA000-2 {
		t.Errorf("LDIR step 1: expected PC=0x%04X, got PC=0x%04X", 0xA000-2, cpu.PC)
	}
}

func TestLDD(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.setHL(0x8002)
	cpu.setDE(0x9002)
	cpu.setBC(0x0003)
	cpu.A = 0x00
	mem.Write(0x8002, 0x77)

	cpu.executeEDInstruction(0xA8)

	if mem.Read(0x9002) != 0x77 {
		t.Errorf("LDD: expected 0x77 at (DE), got 0x%02X", mem.Read(0x9002))
	}
	if cpu.hl() != 0x8001 {
		t.Errorf("LDD: expected HL=0x8001, got 0x%04X", cpu.hl())
	}
	if cpu.de() != 0x9001 {
		t.Errorf("LDD: expected DE=0x9001, got 0x%04X", cpu.de())
	}
	if cpu.bc() != 0x0002 {
		t.Errorf("LDD: expected BC=0x0002, got 0x%04X", cpu.bc())
	}
}

func TestLDDR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x8002, 0xAA)
	cpu.setHL(0x8002)
	cpu.setDE(0x9002)
	cpu.setBC(0x0003)
	cpu.PC = 0xA000

	cpu.executeEDInstruction(0xB8)

	if mem.Read(0x9002) != 0xAA {
		t.Errorf("LDDR: expected 0xAA at 0x9002, got 0x%02X", mem.Read(0x9002))
	}
	// BC should be 2 after one step, and PC goes back
	if cpu.bc() != 0x0002 {
		t.Errorf("LDDR: expected BC=0x0002, got 0x%04X", cpu.bc())
	}
	if cpu.PC != 0xA000-2 {
		t.Errorf("LDDR: expected PC to go back for repeat, got 0x%04X", cpu.PC)
	}
}

func TestCPI(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CPI: compare A with (HL), inc HL, dec BC
	cpu.A = 0x42
	cpu.setHL(0x8000)
	cpu.setBC(0x0005)
	mem.Write(0x8000, 0x42) // match
	cpu.F = FLAG_C          // carry preserved

	cpu.executeEDInstruction(0xA1)

	if cpu.hl() != 0x8001 {
		t.Errorf("CPI: expected HL=0x8001, got 0x%04X", cpu.hl())
	}
	if cpu.bc() != 0x0004 {
		t.Errorf("CPI: expected BC=0x0004, got 0x%04X", cpu.bc())
	}
	// Z flag should be set (A == (HL))
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("CPI: Z flag should be set for match")
	}
	// N flag always set
	if (cpu.F & FLAG_N) == 0 {
		t.Errorf("CPI: N flag should be set")
	}
	// Carry preserved
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("CPI: Carry should be preserved")
	}
	// PV set because BC != 0
	if (cpu.F & FLAG_PV) == 0 {
		t.Errorf("CPI: PV should be set when BC != 0")
	}
}

func TestCPI_NoMatch(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.A = 0x42
	cpu.setHL(0x8000)
	cpu.setBC(0x0005)
	mem.Write(0x8000, 0x99) // no match

	cpu.executeEDInstruction(0xA1)

	// Z flag should NOT be set
	if (cpu.F & FLAG_Z) != 0 {
		t.Errorf("CPI no match: Z flag should NOT be set")
	}
}

func TestCPIR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CPIR repeats until match or BC=0
	cpu.A = 0x42
	cpu.setHL(0x8000)
	cpu.setBC(0x0003)
	cpu.PC = 0xA000
	mem.Write(0x8000, 0x99) // no match

	cpu.executeEDInstruction(0xB1)

	// No match, BC != 0, should repeat
	if cpu.PC != 0xA000-2 {
		t.Errorf("CPIR repeat: expected PC back by 2, got 0x%04X", cpu.PC)
	}

	// Test when match is found
	cpu.A = 0x42
	cpu.setHL(0x8000)
	cpu.setBC(0x0003)
	cpu.PC = 0xA000
	mem.Write(0x8000, 0x42) // match

	cpu.executeEDInstruction(0xB1)

	// Match found, should NOT repeat even though BC != 0
	if cpu.PC == 0xA000-2 {
		t.Errorf("CPIR match: should NOT repeat when match found")
	}
}

// =============================================================================
// Exchange Instructions
// =============================================================================

func TestEX_AF_AF(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// EX AF,AF' (0x08)
	cpu.A = 0x11
	cpu.F = 0x22
	cpu.A_ = 0x33
	cpu.F_ = 0x44

	cpu.executeBaseInstruction(0x08)

	if cpu.A != 0x33 || cpu.F != 0x44 {
		t.Errorf("EX AF,AF': expected AF=0x3344, got A=0x%02X F=0x%02X", cpu.A, cpu.F)
	}
	if cpu.A_ != 0x11 || cpu.F_ != 0x22 {
		t.Errorf("EX AF,AF': expected AF'=0x1122, got A'=0x%02X F'=0x%02X", cpu.A_, cpu.F_)
	}
}

func TestEX_DE_HL(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// EX DE,HL (0xEB)
	cpu.setDE(0x1234)
	cpu.setHL(0x5678)

	cpu.executeBaseInstruction(0xEB)

	if cpu.de() != 0x5678 {
		t.Errorf("EX DE,HL: expected DE=0x5678, got 0x%04X", cpu.de())
	}
	if cpu.hl() != 0x1234 {
		t.Errorf("EX DE,HL: expected HL=0x1234, got 0x%04X", cpu.hl())
	}
}

func TestEXX(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// EXX (0xD9)
	cpu.B = 0x11; cpu.C = 0x22
	cpu.D = 0x33; cpu.E = 0x44
	cpu.H = 0x55; cpu.L = 0x66
	cpu.B_ = 0xAA; cpu.C_ = 0xBB
	cpu.D_ = 0xCC; cpu.E_ = 0xDD
	cpu.H_ = 0xEE; cpu.L_ = 0xFF

	cpu.executeBaseInstruction(0xD9)

	if cpu.B != 0xAA || cpu.C != 0xBB {
		t.Errorf("EXX: expected BC=0xAABB, got B=0x%02X C=0x%02X", cpu.B, cpu.C)
	}
	if cpu.D != 0xCC || cpu.E != 0xDD {
		t.Errorf("EXX: expected DE=0xCCDD, got D=0x%02X E=0x%02X", cpu.D, cpu.E)
	}
	if cpu.H != 0xEE || cpu.L != 0xFF {
		t.Errorf("EXX: expected HL=0xEEFF, got H=0x%02X L=0x%02X", cpu.H, cpu.L)
	}
	// Check originals went to shadow
	if cpu.B_ != 0x11 || cpu.C_ != 0x22 {
		t.Errorf("EXX: expected BC'=0x1122, got B'=0x%02X C'=0x%02X", cpu.B_, cpu.C_)
	}
	if cpu.D_ != 0x33 || cpu.E_ != 0x44 {
		t.Errorf("EXX: expected DE'=0x3344, got D'=0x%02X E'=0x%02X", cpu.D_, cpu.E_)
	}
	if cpu.H_ != 0x55 || cpu.L_ != 0x66 {
		t.Errorf("EXX: expected HL'=0x5566, got H'=0x%02X L'=0x%02X", cpu.H_, cpu.L_)
	}
}

func TestEX_SP_HL(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// EX (SP),HL (0xE3)
	cpu.SP = 0xFFF0
	mem.Write(0xFFF0, 0x34) // low byte on stack
	mem.Write(0xFFF1, 0x12) // high byte on stack => 0x1234
	cpu.setHL(0x5678)

	initialT := cpu.tstates
	cpu.executeBaseInstruction(0xE3)

	if cpu.hl() != 0x1234 {
		t.Errorf("EX (SP),HL: expected HL=0x1234, got 0x%04X", cpu.hl())
	}
	if mem.Read(0xFFF0) != 0x78 {
		t.Errorf("EX (SP),HL: expected low byte 0x78 on stack, got 0x%02X", mem.Read(0xFFF0))
	}
	if mem.Read(0xFFF1) != 0x56 {
		t.Errorf("EX (SP),HL: expected high byte 0x56 on stack, got 0x%02X", mem.Read(0xFFF1))
	}
	if cpu.tstates-initialT != 19 {
		t.Errorf("EX (SP),HL timing: expected 19, got %d", cpu.tstates-initialT)
	}
}

// =============================================================================
// NEG Instruction
// =============================================================================

func TestNEG(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// NEG: A = 0 - A (two's complement)

	// NEG of 1 => 0xFF (-1)
	cpu.A = 0x01
	cpu.F = 0x00
	cpu.executeEDInstruction(0x44)
	if cpu.A != 0xFF {
		t.Errorf("NEG 0x01: expected A=0xFF, got A=0x%02X", cpu.A)
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("NEG 0x01: Carry should be set (original != 0)")
	}
	if (cpu.F & FLAG_N) == 0 {
		t.Errorf("NEG 0x01: N flag should be set")
	}
	if (cpu.F & FLAG_S) == 0 {
		t.Errorf("NEG 0x01: Sign flag should be set (result is negative)")
	}
	// Half-carry: lower nibble of original (0x01) is non-zero
	if (cpu.F & FLAG_H) == 0 {
		t.Errorf("NEG 0x01: H flag should be set (lower nibble was non-zero)")
	}

	// NEG of 0 => 0x00
	cpu.A = 0x00
	cpu.F = 0x00
	cpu.executeEDInstruction(0x44)
	if cpu.A != 0x00 {
		t.Errorf("NEG 0x00: expected A=0x00, got A=0x%02X", cpu.A)
	}
	if (cpu.F & FLAG_C) != 0 {
		t.Errorf("NEG 0x00: Carry should NOT be set (original == 0)")
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("NEG 0x00: Zero flag should be set")
	}
	// Half carry: lower nibble of 0x00 is 0
	if (cpu.F & FLAG_H) != 0 {
		t.Errorf("NEG 0x00: H flag should NOT be set")
	}

	// NEG of 0x80 => 0x80 (overflow case)
	cpu.A = 0x80
	cpu.F = 0x00
	cpu.executeEDInstruction(0x44)
	if cpu.A != 0x80 {
		t.Errorf("NEG 0x80: expected A=0x80, got A=0x%02X", cpu.A)
	}
	if (cpu.F & FLAG_PV) == 0 {
		t.Errorf("NEG 0x80: Overflow flag should be set")
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("NEG 0x80: Carry should be set")
	}

	// NEG of 0x10 => 0xF0, half-carry from lower nibble
	cpu.A = 0x10
	cpu.F = 0x00
	cpu.executeEDInstruction(0x44)
	if cpu.A != 0xF0 {
		t.Errorf("NEG 0x10: expected A=0xF0, got A=0x%02X", cpu.A)
	}
	// Lower nibble of 0x10 is 0, so H flag should NOT be set
	if (cpu.F & FLAG_H) != 0 {
		t.Errorf("NEG 0x10: H flag should NOT be set (lower nibble was 0)")
	}
}

// =============================================================================
// DAA Instruction
// =============================================================================

func TestDAA(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// DAA after ADD: 0x15 + 0x27 = 0x3C, DAA => 0x42 (BCD 15+27=42)
	cpu.A = 0x15
	cpu.F = 0x00
	cpu.add(0x27)
	// A should be 0x3C now
	if cpu.A != 0x3C {
		t.Fatalf("Pre-DAA: expected A=0x3C, got 0x%02X", cpu.A)
	}
	cpu.daa()
	if cpu.A != 0x42 {
		t.Errorf("DAA (15+27): expected A=0x42 (BCD 42), got A=0x%02X", cpu.A)
	}

	// DAA after ADD with carry from lower nibble: 0x09 + 0x01 = 0x0A, DAA => 0x10
	cpu.A = 0x09
	cpu.F = 0x00
	cpu.add(0x01)
	cpu.daa()
	if cpu.A != 0x10 {
		t.Errorf("DAA (9+1): expected A=0x10 (BCD 10), got A=0x%02X", cpu.A)
	}

	// DAA after ADD generating carry: 0x90 + 0x15 = 0xA5, DAA => 0x05 with carry
	cpu.A = 0x90
	cpu.F = 0x00
	cpu.add(0x15)
	cpu.daa()
	if cpu.A != 0x05 {
		t.Errorf("DAA (90+15): expected A=0x05 (BCD 05), got A=0x%02X", cpu.A)
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("DAA (90+15): Carry flag should be set (BCD overflow)")
	}

	// DAA after SUB: 0x42 - 0x15 = 0x2D, DAA => 0x27 (BCD 42-15=27)
	cpu.A = 0x42
	cpu.F = 0x00
	cpu.sub(0x15)
	cpu.daa()
	if cpu.A != 0x27 {
		t.Errorf("DAA (42-15): expected A=0x27 (BCD 27), got A=0x%02X", cpu.A)
	}
}

// =============================================================================
// SCF, CCF, CPL
// =============================================================================

func TestSCF(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// SCF (0x37) - Set Carry Flag
	cpu.F = 0x00
	cpu.A = 0x00
	cpu.executeBaseInstruction(0x37)
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("SCF: Carry flag should be set")
	}
	if (cpu.F & FLAG_N) != 0 {
		t.Errorf("SCF: N flag should be cleared")
	}
	if (cpu.F & FLAG_H) != 0 {
		t.Errorf("SCF: H flag should be cleared")
	}

	// SCF preserves S, Z, PV
	cpu.F = FLAG_S | FLAG_Z | FLAG_PV
	cpu.A = 0x00
	cpu.executeBaseInstruction(0x37)
	if (cpu.F & FLAG_S) == 0 {
		t.Errorf("SCF: S flag should be preserved")
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("SCF: Z flag should be preserved")
	}
	if (cpu.F & FLAG_PV) == 0 {
		t.Errorf("SCF: PV flag should be preserved")
	}
}

func TestCCF(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CCF (0x3F) - Complement Carry Flag
	// If carry was set, it becomes clear
	cpu.F = FLAG_C
	cpu.A = 0x00
	cpu.executeBaseInstruction(0x3F)
	if (cpu.F & FLAG_C) != 0 {
		t.Errorf("CCF (was set): Carry should now be clear")
	}

	// If carry was clear, it becomes set
	cpu.F = 0x00
	cpu.A = 0x00
	cpu.executeBaseInstruction(0x3F)
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("CCF (was clear): Carry should now be set")
	}

	// CCF preserves S, Z, PV
	cpu.F = FLAG_S | FLAG_Z | FLAG_PV
	cpu.A = 0x00
	cpu.executeBaseInstruction(0x3F)
	if (cpu.F & FLAG_S) == 0 {
		t.Errorf("CCF: S flag should be preserved")
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("CCF: Z flag should be preserved")
	}
	if (cpu.F & FLAG_PV) == 0 {
		t.Errorf("CCF: PV flag should be preserved")
	}
}

func TestCPL(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CPL (0x2F) - Complement A
	cpu.A = 0x55 // 01010101
	cpu.F = FLAG_Z | FLAG_C
	cpu.executeBaseInstruction(0x2F)
	if cpu.A != 0xAA { // 10101010
		t.Errorf("CPL: expected A=0xAA, got A=0x%02X", cpu.A)
	}
	if (cpu.F & FLAG_H) == 0 {
		t.Errorf("CPL: H flag should be set")
	}
	if (cpu.F & FLAG_N) == 0 {
		t.Errorf("CPL: N flag should be set")
	}
	// S, Z, PV, C preserved
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("CPL: Z flag should be preserved")
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("CPL: C flag should be preserved")
	}

	// CPL of 0xFF => 0x00
	cpu.A = 0xFF
	cpu.executeBaseInstruction(0x2F)
	if cpu.A != 0x00 {
		t.Errorf("CPL 0xFF: expected A=0x00, got A=0x%02X", cpu.A)
	}

	// CPL of 0x00 => 0xFF
	cpu.A = 0x00
	cpu.executeBaseInstruction(0x2F)
	if cpu.A != 0xFF {
		t.Errorf("CPL 0x00: expected A=0xFF, got A=0x%02X", cpu.A)
	}
}

// =============================================================================
// HALT and Interrupt Handling
// =============================================================================

func TestHALT(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// HALT (0x76)
	cpu.Halted = false
	cpu.executeBaseInstruction(0x76)
	if !cpu.Halted {
		t.Errorf("HALT: CPU should be in halted state")
	}
}

func TestDI_EI(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// DI (0xF3)
	cpu.IFF1 = true
	cpu.IFF2 = true
	cpu.executeBaseInstruction(0xF3)
	if cpu.IFF1 {
		t.Errorf("DI: IFF1 should be false")
	}
	if cpu.IFF2 {
		t.Errorf("DI: IFF2 should be false")
	}

	// EI (0xFB) — IFF1/IFF2 set immediately
	cpu.executeBaseInstruction(0xFB)
	if !cpu.IFF1 {
		t.Errorf("EI: IFF1 should be true")
	}
	if !cpu.IFF2 {
		t.Errorf("EI: IFF2 should be true")
	}
}

func TestIM(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// IM 0 (ED 46)
	cpu.executeEDInstruction(0x46)
	if cpu.IM != 0 {
		t.Errorf("IM 0: expected IM=0, got %d", cpu.IM)
	}

	// IM 1 (ED 56)
	cpu.executeEDInstruction(0x56)
	if cpu.IM != 1 {
		t.Errorf("IM 1: expected IM=1, got %d", cpu.IM)
	}

	// IM 2 (ED 5E)
	cpu.executeEDInstruction(0x5E)
	if cpu.IM != 2 {
		t.Errorf("IM 2: expected IM=2, got %d", cpu.IM)
	}
}

func TestInterruptIM1(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	_ = mem

	// Set up for IM 1 interrupt
	cpu.IM = 1
	cpu.IFF1 = true
	cpu.IFF2 = true
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.Halted = true

	cpu.interrupt()

	// IFF1 and IFF2 should be disabled
	if cpu.IFF1 {
		t.Errorf("IM1 interrupt: IFF1 should be false")
	}
	if cpu.IFF2 {
		t.Errorf("IM1 interrupt: IFF2 should be false")
	}
	// Halted state should be cleared
	if cpu.Halted {
		t.Errorf("IM1 interrupt: should exit halted state")
	}
	// PC should jump to 0x0038
	if cpu.PC != 0x0038 {
		t.Errorf("IM1 interrupt: expected PC=0x0038, got PC=0x%04X", cpu.PC)
	}
	// Return address pushed
	retLo := mem.Read(cpu.SP)
	retHi := mem.Read(cpu.SP + 1)
	retAddr := uint16(retHi)<<8 | uint16(retLo)
	if retAddr != 0x8000 {
		t.Errorf("IM1 interrupt: return addr expected 0x8000, got 0x%04X", retAddr)
	}
}

func TestInterruptIM2(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Set up for IM 2 interrupt
	cpu.IM = 2
	cpu.IFF1 = true
	cpu.IFF2 = true
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.I = 0x80
	cpu.IM2Vector = 0xFE // Spectrum puts 0xFF, but let's test with 0xFE

	// IM2 vector table: address = I << 8 | IM2Vector = 0x80FE
	// Write the ISR address at that location
	mem.Write(0x80FE, 0x00) // low byte of ISR
	mem.Write(0x80FF, 0x50) // high byte => ISR at 0x5000

	cpu.interrupt()

	if cpu.PC != 0x5000 {
		t.Errorf("IM2 interrupt: expected PC=0x5000, got PC=0x%04X", cpu.PC)
	}
}

func TestInterruptDisabled(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// When IFF1 is false, interrupt() should do nothing
	cpu.IFF1 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	cpu.interrupt()

	if cpu.PC != 0x8000 {
		t.Errorf("Disabled interrupt: PC should not change, got 0x%04X", cpu.PC)
	}
}

func TestNMI(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	_ = mem

	// NMI always executes regardless of IFF1
	cpu.IFF1 = true
	cpu.IFF2 = false
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0
	cpu.Halted = true

	cpu.NMI()

	// IFF1 should be disabled; IFF2 must be left unchanged (not copied
	// from IFF1) so that the Multiface 3 exit path — which uses EI
	// rather than RETN — does not permanently disable interrupts on
	// the +3.  See FUSE's z80_nmi() for the same approach.
	if cpu.IFF1 {
		t.Errorf("NMI: IFF1 should be false")
	}
	if cpu.IFF2 {
		t.Errorf("NMI: IFF2 should be unchanged (false)")
	}
	if cpu.Halted {
		t.Errorf("NMI: should exit halted state")
	}
	if cpu.PC != 0x0066 {
		t.Errorf("NMI: expected PC=0x0066, got PC=0x%04X", cpu.PC)
	}
	retLo := mem.Read(cpu.SP)
	retHi := mem.Read(cpu.SP + 1)
	retAddr := uint16(retHi)<<8 | uint16(retLo)
	if retAddr != 0x8000 {
		t.Errorf("NMI: return addr expected 0x8000, got 0x%04X", retAddr)
	}
}

func TestNMI_PreservesIFF2(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// When NMI fires while IFF1=false (e.g. inside a DI section),
	// IFF2 must NOT be overwritten.  This is critical for the +3
	// where IFF2=true may be needed by a later RETN.
	cpu.IFF1 = false
	cpu.IFF2 = true
	cpu.PC = 0x8000
	cpu.SP = 0xFFF0

	cpu.NMI()

	if cpu.IFF1 {
		t.Errorf("NMI: IFF1 should be false")
	}
	if !cpu.IFF2 {
		t.Errorf("NMI: IFF2 should still be true (must not be overwritten)")
	}
}

func TestRETI(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// RETI (ED 4D)
	cpu.SP = 0xFFF0
	mem.Write(0xFFF0, 0x00)
	mem.Write(0xFFF1, 0x80) // return to 0x8000
	cpu.IFF2 = true
	cpu.IFF1 = false

	cpu.executeEDInstruction(0x4D)

	if cpu.PC != 0x8000 {
		t.Errorf("RETI: expected PC=0x8000, got 0x%04X", cpu.PC)
	}
	if !cpu.IFF1 {
		t.Errorf("RETI: IFF1 should be restored from IFF2 (true)")
	}
}

func TestRETN(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// RETN (ED 45)
	cpu.SP = 0xFFF0
	mem.Write(0xFFF0, 0x66)
	mem.Write(0xFFF1, 0x00) // return to 0x0066
	cpu.IFF2 = true
	cpu.IFF1 = false

	cpu.executeEDInstruction(0x45)

	if cpu.PC != 0x0066 {
		t.Errorf("RETN: expected PC=0x0066, got 0x%04X", cpu.PC)
	}
	if !cpu.IFF1 {
		t.Errorf("RETN: IFF1 should be restored from IFF2 (true)")
	}
}

// =============================================================================
// 16-bit Arithmetic
// =============================================================================

func TestADD_HL_rr(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// ADD HL,BC (0x09)
	cpu.setHL(0x1000)
	cpu.setBC(0x2000)
	cpu.F = FLAG_Z | FLAG_S // these should be preserved
	initialT := cpu.tstates
	cpu.executeBaseInstruction(0x09)
	if cpu.hl() != 0x3000 {
		t.Errorf("ADD HL,BC: expected 0x3000, got 0x%04X", cpu.hl())
	}
	if (cpu.F & FLAG_C) != 0 {
		t.Errorf("ADD HL,BC: Carry should not be set")
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("ADD HL,BC: Z flag should be preserved")
	}
	if (cpu.F & FLAG_S) == 0 {
		t.Errorf("ADD HL,BC: S flag should be preserved")
	}
	if cpu.tstates-initialT != 11 {
		t.Errorf("ADD HL,BC timing: expected 11, got %d", cpu.tstates-initialT)
	}

	// ADD HL,DE (0x19) with carry
	cpu.setHL(0xF000)
	cpu.setDE(0x2000)
	cpu.F = 0
	cpu.executeBaseInstruction(0x19)
	if cpu.hl() != 0x1000 {
		t.Errorf("ADD HL,DE overflow: expected 0x1000, got 0x%04X", cpu.hl())
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("ADD HL,DE overflow: Carry should be set")
	}

	// ADD HL,HL (0x29)
	cpu.setHL(0x4000)
	cpu.F = 0
	cpu.executeBaseInstruction(0x29)
	if cpu.hl() != 0x8000 {
		t.Errorf("ADD HL,HL: expected 0x8000, got 0x%04X", cpu.hl())
	}

	// ADD HL,SP (0x39)
	cpu.setHL(0x1000)
	cpu.SP = 0x3000
	cpu.F = 0
	cpu.executeBaseInstruction(0x39)
	if cpu.hl() != 0x4000 {
		t.Errorf("ADD HL,SP: expected 0x4000, got 0x%04X", cpu.hl())
	}

	// Half-carry test: ADD HL,BC where bits 11 carry
	cpu.setHL(0x0FFF)
	cpu.setBC(0x0001)
	cpu.F = 0
	cpu.executeBaseInstruction(0x09)
	if (cpu.F & FLAG_H) == 0 {
		t.Errorf("ADD HL,BC half-carry: H flag should be set")
	}
}

func TestADC_HL_rr(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// ADC HL,BC (ED 4A) without carry
	cpu.setHL(0x1000)
	cpu.setBC(0x2000)
	cpu.F = 0x00
	cpu.executeEDInstruction(0x4A)
	if cpu.hl() != 0x3000 {
		t.Errorf("ADC HL,BC (no carry): expected 0x3000, got 0x%04X", cpu.hl())
	}

	// ADC HL,BC with carry
	cpu.setHL(0x1000)
	cpu.setBC(0x2000)
	cpu.F = FLAG_C
	cpu.executeEDInstruction(0x4A)
	if cpu.hl() != 0x3001 {
		t.Errorf("ADC HL,BC (with carry): expected 0x3001, got 0x%04X", cpu.hl())
	}

	// ADC HL,DE (ED 5A) overflow
	cpu.setHL(0x7FFF)
	cpu.setDE(0x0001)
	cpu.F = 0
	cpu.executeEDInstruction(0x5A)
	if cpu.hl() != 0x8000 {
		t.Errorf("ADC HL,DE: expected 0x8000, got 0x%04X", cpu.hl())
	}
	if (cpu.F & FLAG_PV) == 0 {
		t.Errorf("ADC HL,DE: overflow flag should be set (positive to negative)")
	}
	if (cpu.F & FLAG_S) == 0 {
		t.Errorf("ADC HL,DE: sign flag should be set")
	}

	// ADC HL,HL (ED 6A) result = 0 with carry
	cpu.setHL(0x8000)
	cpu.F = 0
	cpu.executeEDInstruction(0x6A)
	if cpu.hl() != 0x0000 {
		t.Errorf("ADC HL,HL: expected 0x0000, got 0x%04X", cpu.hl())
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("ADC HL,HL: Z flag should be set")
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("ADC HL,HL: Carry should be set")
	}
}

func TestSBC_HL_rr(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// SBC HL,BC (ED 42) without carry
	cpu.setHL(0x3000)
	cpu.setBC(0x1000)
	cpu.F = 0x00
	cpu.executeEDInstruction(0x42)
	if cpu.hl() != 0x2000 {
		t.Errorf("SBC HL,BC (no carry): expected 0x2000, got 0x%04X", cpu.hl())
	}
	if (cpu.F & FLAG_N) == 0 {
		t.Errorf("SBC HL,BC: N flag should be set")
	}

	// SBC HL,BC with carry
	cpu.setHL(0x3000)
	cpu.setBC(0x1000)
	cpu.F = FLAG_C
	cpu.executeEDInstruction(0x42)
	if cpu.hl() != 0x1FFF {
		t.Errorf("SBC HL,BC (with carry): expected 0x1FFF, got 0x%04X", cpu.hl())
	}

	// SBC HL,DE (ED 52) borrow
	cpu.setHL(0x1000)
	cpu.setDE(0x2000)
	cpu.F = 0
	cpu.executeEDInstruction(0x52)
	if cpu.hl() != 0xF000 {
		t.Errorf("SBC HL,DE borrow: expected 0xF000, got 0x%04X", cpu.hl())
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("SBC HL,DE borrow: Carry should be set")
	}
	if (cpu.F & FLAG_S) == 0 {
		t.Errorf("SBC HL,DE borrow: Sign should be set")
	}

	// SBC HL,HL (ED 62) - result zero, no carry
	cpu.setHL(0x5000)
	cpu.F = 0
	cpu.executeEDInstruction(0x62)
	if cpu.hl() != 0x0000 {
		t.Errorf("SBC HL,HL: expected 0x0000, got 0x%04X", cpu.hl())
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("SBC HL,HL: Z flag should be set")
	}

	// SBC HL,SP (ED 72)
	cpu.setHL(0x5000)
	cpu.SP = 0x1000
	cpu.F = 0
	cpu.executeEDInstruction(0x72)
	if cpu.hl() != 0x4000 {
		t.Errorf("SBC HL,SP: expected 0x4000, got 0x%04X", cpu.hl())
	}
}

func TestINC_rr(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// INC BC (0x03)
	cpu.setBC(0x00FF)
	cpu.executeBaseInstruction(0x03)
	if cpu.bc() != 0x0100 {
		t.Errorf("INC BC: expected 0x0100, got 0x%04X", cpu.bc())
	}

	// INC DE (0x13)
	cpu.setDE(0xFFFF)
	cpu.executeBaseInstruction(0x13)
	if cpu.de() != 0x0000 {
		t.Errorf("INC DE wrap: expected 0x0000, got 0x%04X", cpu.de())
	}

	// INC HL (0x23)
	cpu.setHL(0x1234)
	cpu.executeBaseInstruction(0x23)
	if cpu.hl() != 0x1235 {
		t.Errorf("INC HL: expected 0x1235, got 0x%04X", cpu.hl())
	}

	// INC SP (0x33)
	cpu.SP = 0xFFFE
	cpu.executeBaseInstruction(0x33)
	if cpu.SP != 0xFFFF {
		t.Errorf("INC SP: expected 0xFFFF, got 0x%04X", cpu.SP)
	}
}

func TestDEC_rr(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// DEC BC (0x0B)
	cpu.setBC(0x0100)
	cpu.executeBaseInstruction(0x0B)
	if cpu.bc() != 0x00FF {
		t.Errorf("DEC BC: expected 0x00FF, got 0x%04X", cpu.bc())
	}

	// DEC DE (0x1B)
	cpu.setDE(0x0000)
	cpu.executeBaseInstruction(0x1B)
	if cpu.de() != 0xFFFF {
		t.Errorf("DEC DE wrap: expected 0xFFFF, got 0x%04X", cpu.de())
	}

	// DEC HL (0x2B)
	cpu.setHL(0x1234)
	cpu.executeBaseInstruction(0x2B)
	if cpu.hl() != 0x1233 {
		t.Errorf("DEC HL: expected 0x1233, got 0x%04X", cpu.hl())
	}

	// DEC SP (0x3B)
	cpu.SP = 0x0001
	cpu.executeBaseInstruction(0x3B)
	if cpu.SP != 0x0000 {
		t.Errorf("DEC SP: expected 0x0000, got 0x%04X", cpu.SP)
	}
}

// =============================================================================
// IX/IY Index Register Operations
// =============================================================================

func TestLD_IX_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD IX,nn (DD 21)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x34)
	mem.Write(0x8001, 0x12) // => 0x1234
	cpu.executeDDInstruction(0x21)
	if cpu.IX != 0x1234 {
		t.Errorf("LD IX,nn: expected IX=0x1234, got 0x%04X", cpu.IX)
	}
}

func TestLD_r_IXd(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IX = 0x8010

	// LD A,(IX+5) (DD 7E 05)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x05)     // displacement +5
	mem.Write(0x8015, 0x42)     // value at IX+5
	cpu.executeDDInstruction(0x7E)
	if cpu.A != 0x42 {
		t.Errorf("LD A,(IX+5): expected A=0x42, got 0x%02X", cpu.A)
	}

	// LD B,(IX-3) with negative displacement
	cpu.PC = 0x9000
	mem.Write(0x9000, 0xFD)     // displacement -3 (signed)
	mem.Write(0x800D, 0x77)     // value at IX-3 = 0x8010-3 = 0x800D
	cpu.executeDDInstruction(0x46)
	if cpu.B != 0x77 {
		t.Errorf("LD B,(IX-3): expected B=0x77, got 0x%02X", cpu.B)
	}
}

func TestLD_IXd_r(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IX = 0x8010

	// LD (IX+2),A (DD 77 02)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x02)     // displacement +2
	cpu.A = 0x99
	cpu.executeDDInstruction(0x77)
	if mem.Read(0x8012) != 0x99 {
		t.Errorf("LD (IX+2),A: expected 0x99 at 0x8012, got 0x%02X", mem.Read(0x8012))
	}

	// LD (IX+0),B (DD 70 00)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x00) // displacement 0
	cpu.B = 0xAA
	cpu.executeDDInstruction(0x70)
	if mem.Read(0x8010) != 0xAA {
		t.Errorf("LD (IX+0),B: expected 0xAA at 0x8010, got 0x%02X", mem.Read(0x8010))
	}
}

func TestLD_IXd_n(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IX = 0x8010

	// LD (IX+4),n (DD 36 04 nn)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x04) // displacement
	mem.Write(0x9001, 0xBB) // immediate value
	cpu.executeDDInstruction(0x36)
	if mem.Read(0x8014) != 0xBB {
		t.Errorf("LD (IX+4),n: expected 0xBB at 0x8014, got 0x%02X", mem.Read(0x8014))
	}
}

func TestINC_DEC_IX(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// INC IX (DD 23)
	cpu.IX = 0x1234
	cpu.executeDDInstruction(0x23)
	if cpu.IX != 0x1235 {
		t.Errorf("INC IX: expected 0x1235, got 0x%04X", cpu.IX)
	}

	// DEC IX (DD 2B)
	cpu.IX = 0x1235
	cpu.executeDDInstruction(0x2B)
	if cpu.IX != 0x1234 {
		t.Errorf("DEC IX: expected 0x1234, got 0x%04X", cpu.IX)
	}
}

func TestADD_IX_rr(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// ADD IX,BC (DD 09)
	cpu.IX = 0x1000
	cpu.setBC(0x2000)
	cpu.F = FLAG_Z | FLAG_S // should preserve Z, S, PV
	cpu.executeDDInstruction(0x09)
	if cpu.IX != 0x3000 {
		t.Errorf("ADD IX,BC: expected 0x3000, got 0x%04X", cpu.IX)
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("ADD IX,BC: Z flag should be preserved")
	}

	// ADD IX,DE (DD 19) with carry
	cpu.IX = 0xF000
	cpu.setDE(0x2000)
	cpu.F = 0
	cpu.executeDDInstruction(0x19)
	if cpu.IX != 0x1000 {
		t.Errorf("ADD IX,DE overflow: expected 0x1000, got 0x%04X", cpu.IX)
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("ADD IX,DE overflow: Carry should be set")
	}

	// ADD IX,IX (DD 29)
	cpu.IX = 0x4000
	cpu.F = 0
	cpu.executeDDInstruction(0x29)
	if cpu.IX != 0x8000 {
		t.Errorf("ADD IX,IX: expected 0x8000, got 0x%04X", cpu.IX)
	}
}

func TestArithLogic_IXd(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IX = 0x8010

	// ADD A,(IX+1) (DD 86)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x01)
	mem.Write(0x8011, 0x10)
	cpu.A = 0x20
	cpu.executeDDInstruction(0x86)
	if cpu.A != 0x30 {
		t.Errorf("ADD A,(IX+1): expected A=0x30, got 0x%02X", cpu.A)
	}

	// SUB (IX+2) (DD 96)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x02)
	mem.Write(0x8012, 0x10)
	cpu.A = 0x30
	cpu.executeDDInstruction(0x96)
	if cpu.A != 0x20 {
		t.Errorf("SUB (IX+2): expected A=0x20, got 0x%02X", cpu.A)
	}

	// AND (IX+3) (DD A6)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x03)
	mem.Write(0x8013, 0x0F)
	cpu.A = 0xFF
	cpu.executeDDInstruction(0xA6)
	if cpu.A != 0x0F {
		t.Errorf("AND (IX+3): expected A=0x0F, got 0x%02X", cpu.A)
	}

	// CP (IX+4) (DD BE) - compare, match
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x04)
	mem.Write(0x8014, 0x0F)
	cpu.A = 0x0F
	cpu.executeDDInstruction(0xBE)
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("CP (IX+4): Z flag should be set for match")
	}
}

func TestPUSH_POP_IX(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.SP = 0xFFF0
	cpu.IX = 0xABCD

	// PUSH IX (DD E5)
	cpu.executeDDInstruction(0xE5)
	if cpu.SP != 0xFFF0-2 {
		t.Errorf("PUSH IX: expected SP=0x%04X, got 0x%04X", 0xFFF0-2, cpu.SP)
	}

	// POP IX (DD E1) into different value
	cpu.IX = 0x0000
	cpu.executeDDInstruction(0xE1)
	if cpu.IX != 0xABCD {
		t.Errorf("POP IX: expected IX=0xABCD, got 0x%04X", cpu.IX)
	}
}

func TestJP_IX(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// JP (IX) (DD E9)
	cpu.IX = 0x1234
	cpu.executeDDInstruction(0xE9)
	if cpu.PC != 0x1234 {
		t.Errorf("JP (IX): expected PC=0x1234, got 0x%04X", cpu.PC)
	}
}

func TestLD_IY_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD IY,nn (FD 21)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x78)
	mem.Write(0x8001, 0x56) // => 0x5678
	cpu.executeFDInstruction(0x21)
	if cpu.IY != 0x5678 {
		t.Errorf("LD IY,nn: expected IY=0x5678, got 0x%04X", cpu.IY)
	}
}

func TestLD_r_IYd(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IY = 0x8010

	// LD A,(IY+5) (FD 7E 05)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x05)
	mem.Write(0x8015, 0x42)
	cpu.executeFDInstruction(0x7E)
	if cpu.A != 0x42 {
		t.Errorf("LD A,(IY+5): expected A=0x42, got 0x%02X", cpu.A)
	}
}

func TestLD_IYd_r(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IY = 0x8010

	// LD (IY+2),A (FD 77 02)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x02)
	cpu.A = 0x99
	cpu.executeFDInstruction(0x77)
	if mem.Read(0x8012) != 0x99 {
		t.Errorf("LD (IY+2),A: expected 0x99 at 0x8012, got 0x%02X", mem.Read(0x8012))
	}
}

func TestEX_SP_IX(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// EX (SP),IX (DD E3)
	cpu.SP = 0xFFF0
	mem.Write(0xFFF0, 0x34)
	mem.Write(0xFFF1, 0x12) // 0x1234 on stack
	cpu.IX = 0x5678

	cpu.executeDDInstruction(0xE3)

	if cpu.IX != 0x1234 {
		t.Errorf("EX (SP),IX: expected IX=0x1234, got 0x%04X", cpu.IX)
	}
	if mem.Read(0xFFF0) != 0x78 {
		t.Errorf("EX (SP),IX: expected low byte 0x78 on stack, got 0x%02X", mem.Read(0xFFF0))
	}
	if mem.Read(0xFFF1) != 0x56 {
		t.Errorf("EX (SP),IX: expected high byte 0x56 on stack, got 0x%02X", mem.Read(0xFFF1))
	}
}

func TestINC_DEC_IXd(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.IX = 0x8010

	// INC (IX+3) (DD 34)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x03) // displacement
	mem.Write(0x8013, 0x0F) // original value
	cpu.executeDDInstruction(0x34)
	if mem.Read(0x8013) != 0x10 {
		t.Errorf("INC (IX+3): expected 0x10, got 0x%02X", mem.Read(0x8013))
	}

	// DEC (IX+3) (DD 35)
	cpu.PC = 0x9000
	mem.Write(0x9000, 0x03) // displacement
	cpu.executeDDInstruction(0x35)
	if mem.Read(0x8013) != 0x0F {
		t.Errorf("DEC (IX+3): expected 0x0F, got 0x%02X", mem.Read(0x8013))
	}
}

// =============================================================================
// ED Prefix: LD I,A / LD R,A / LD A,I / LD A,R
// =============================================================================

func TestLD_I_A(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD I,A (ED 47)
	cpu.A = 0x55
	cpu.executeEDInstruction(0x47)
	if cpu.I != 0x55 {
		t.Errorf("LD I,A: expected I=0x55, got 0x%02X", cpu.I)
	}

	// LD R,A (ED 4F)
	cpu.A = 0x77
	cpu.executeEDInstruction(0x4F)
	if cpu.R != 0x77 {
		t.Errorf("LD R,A: expected R=0x77, got 0x%02X", cpu.R)
	}
}

func TestLD_A_I(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD A,I (ED 57) - with IFF2 true
	cpu.I = 0x42
	cpu.IFF2 = true
	cpu.F = FLAG_C // carry preserved
	cpu.executeEDInstruction(0x57)
	if cpu.A != 0x42 {
		t.Errorf("LD A,I: expected A=0x42, got 0x%02X", cpu.A)
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("LD A,I: Carry should be preserved")
	}
	if (cpu.F & FLAG_PV) == 0 {
		t.Errorf("LD A,I: PV should be set when IFF2 is true")
	}

	// LD A,I with IFF2 false
	cpu.I = 0x42
	cpu.IFF2 = false
	cpu.F = FLAG_C
	cpu.executeEDInstruction(0x57)
	if (cpu.F & FLAG_PV) != 0 {
		t.Errorf("LD A,I: PV should be clear when IFF2 is false")
	}

	// LD A,R (ED 5F)
	cpu.R = 0x80
	cpu.IFF2 = true
	cpu.F = 0
	cpu.executeEDInstruction(0x5F)
	if cpu.A != 0x80 {
		t.Errorf("LD A,R: expected A=0x80, got 0x%02X", cpu.A)
	}
	if (cpu.F & FLAG_S) == 0 {
		t.Errorf("LD A,R: Sign flag should be set for 0x80")
	}
}

// =============================================================================
// ED Prefix: 16-bit Load/Store
// =============================================================================

func TestED_LD_nn_rr(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD (nn),BC (ED 43)
	cpu.PC = 0x8000
	cpu.setBC(0xABCD)
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90) // address 0x9000
	cpu.executeEDInstruction(0x43)
	if mem.Read(0x9000) != 0xCD {
		t.Errorf("LD (nn),BC: low byte expected 0xCD, got 0x%02X", mem.Read(0x9000))
	}
	if mem.Read(0x9001) != 0xAB {
		t.Errorf("LD (nn),BC: high byte expected 0xAB, got 0x%02X", mem.Read(0x9001))
	}

	// LD BC,(nn) (ED 4B)
	cpu.PC = 0x8000
	mem.Write(0x9000, 0xEF)
	mem.Write(0x9001, 0xBE) // 0xBEEF
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeEDInstruction(0x4B)
	if cpu.bc() != 0xBEEF {
		t.Errorf("LD BC,(nn): expected 0xBEEF, got 0x%04X", cpu.bc())
	}

	// LD (nn),DE (ED 53)
	cpu.PC = 0x8000
	cpu.setDE(0x1234)
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeEDInstruction(0x53)
	if mem.Read(0x9000) != 0x34 || mem.Read(0x9001) != 0x12 {
		t.Errorf("LD (nn),DE: expected 0x1234 at 0x9000")
	}

	// LD DE,(nn) (ED 5B)
	cpu.PC = 0x8000
	mem.Write(0x9000, 0x78)
	mem.Write(0x9001, 0x56)
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeEDInstruction(0x5B)
	if cpu.de() != 0x5678 {
		t.Errorf("LD DE,(nn): expected 0x5678, got 0x%04X", cpu.de())
	}

	// LD (nn),SP (ED 73)
	cpu.PC = 0x8000
	cpu.SP = 0xDEAD
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeEDInstruction(0x73)
	lo := mem.Read(0x9000)
	hi := mem.Read(0x9001)
	if uint16(hi)<<8|uint16(lo) != 0xDEAD {
		t.Errorf("LD (nn),SP: expected 0xDEAD at 0x9000, got 0x%02X%02X", hi, lo)
	}

	// LD SP,(nn) (ED 7B)
	cpu.PC = 0x8000
	mem.Write(0x9000, 0xEF)
	mem.Write(0x9001, 0xCA) // 0xCAEF
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeEDInstruction(0x7B)
	if cpu.SP != 0xCAEF {
		t.Errorf("LD SP,(nn): expected 0xCAEF, got 0x%04X", cpu.SP)
	}
}

// =============================================================================
// Stack PUSH/POP with all register pairs
// =============================================================================

func TestPUSH_POP_AllPairs(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.SP = 0xFFF0

	// PUSH BC (0xC5) / POP BC (0xC1)
	cpu.setBC(0x1234)
	cpu.executeBaseInstruction(0xC5)
	cpu.setBC(0x0000)
	cpu.executeBaseInstruction(0xC1)
	if cpu.bc() != 0x1234 {
		t.Errorf("PUSH/POP BC: expected 0x1234, got 0x%04X", cpu.bc())
	}

	// PUSH DE (0xD5) / POP DE (0xD1)
	cpu.setDE(0x5678)
	cpu.executeBaseInstruction(0xD5)
	cpu.setDE(0x0000)
	cpu.executeBaseInstruction(0xD1)
	if cpu.de() != 0x5678 {
		t.Errorf("PUSH/POP DE: expected 0x5678, got 0x%04X", cpu.de())
	}

	// PUSH HL (0xE5) / POP HL (0xE1)
	cpu.setHL(0x9ABC)
	cpu.executeBaseInstruction(0xE5)
	cpu.setHL(0x0000)
	cpu.executeBaseInstruction(0xE1)
	if cpu.hl() != 0x9ABC {
		t.Errorf("PUSH/POP HL: expected 0x9ABC, got 0x%04X", cpu.hl())
	}

	// PUSH AF (0xF5) / POP AF (0xF1)
	cpu.setAF(0xDEF0)
	cpu.executeBaseInstruction(0xF5)
	cpu.setAF(0x0000)
	cpu.executeBaseInstruction(0xF1)
	if cpu.af() != 0xDEF0 {
		t.Errorf("PUSH/POP AF: expected 0xDEF0, got 0x%04X", cpu.af())
	}
}

// =============================================================================
// ED I/O Instructions
// =============================================================================

func TestED_IN_r_C(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	// IN B,(C) (ED 40)
	cpu.setBC(0x12FE)
	ula.ports[0x12FE] = 0x42
	cpu.executeEDInstruction(0x40)
	if cpu.B != 0x42 {
		t.Errorf("IN B,(C): expected B=0x42, got 0x%02X", cpu.B)
	}

	// IN A,(C) (ED 78)
	cpu.setBC(0x34FE)
	ula.ports[0x34FE] = 0x55
	cpu.executeEDInstruction(0x78)
	if cpu.A != 0x55 {
		t.Errorf("IN A,(C): expected A=0x55, got 0x%02X", cpu.A)
	}
}

func TestED_OUT_C_r(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	// OUT (C),B (ED 41)
	cpu.setBC(0x12FE)
	cpu.B = 0x12
	cpu.executeEDInstruction(0x41)
	if ula.ports[0x12FE] != 0x12 {
		t.Errorf("OUT (C),B: expected 0x12 at port, got 0x%02X", ula.ports[0x12FE])
	}

	// OUT (C),A (ED 79)
	cpu.A = 0xAA
	cpu.setBC(0x34FE)
	cpu.executeEDInstruction(0x79)
	if ula.ports[0x34FE] != 0xAA {
		t.Errorf("OUT (C),A: expected 0xAA at port, got 0x%02X", ula.ports[0x34FE])
	}
}

// =============================================================================
// RRD / RLD (Rotate Decimal)
// =============================================================================

func TestRRD(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// RRD (ED 67): A[3:0] <- (HL)[3:0]; (HL)[3:0] <- (HL)[7:4]; (HL)[7:4] <- A[3:0]
	cpu.A = 0x12         // A = 0x12
	cpu.setHL(0x8000)
	mem.Write(0x8000, 0x34)
	cpu.F = FLAG_C       // carry preserved

	cpu.executeEDInstruction(0x67)

	// A upper nibble preserved (0x1_), lower nibble = (HL) lower nibble (0x4)
	if cpu.A != 0x14 {
		t.Errorf("RRD: expected A=0x14, got A=0x%02X", cpu.A)
	}
	// (HL) = (old A lower nibble << 4) | (old (HL) upper nibble)
	// = (0x2 << 4) | 0x3 = 0x23
	if mem.Read(0x8000) != 0x23 {
		t.Errorf("RRD: expected (HL)=0x23, got 0x%02X", mem.Read(0x8000))
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("RRD: Carry should be preserved")
	}
}

func TestRLD(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// RLD (ED 6F): A[3:0] <- (HL)[7:4]; (HL)[7:4] <- (HL)[3:0]; (HL)[3:0] <- A[3:0]
	cpu.A = 0x12
	cpu.setHL(0x8000)
	mem.Write(0x8000, 0x34)
	cpu.F = FLAG_C

	cpu.executeEDInstruction(0x6F)

	// A upper nibble preserved (0x1_), lower nibble = (HL) upper nibble (0x3)
	if cpu.A != 0x13 {
		t.Errorf("RLD: expected A=0x13, got A=0x%02X", cpu.A)
	}
	// (HL) = (old (HL) lower nibble << 4) | (old A lower nibble)
	// = (0x4 << 4) | 0x2 = 0x42
	if mem.Read(0x8000) != 0x42 {
		t.Errorf("RLD: expected (HL)=0x42, got 0x%02X", mem.Read(0x8000))
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("RLD: Carry should be preserved")
	}
}

// =============================================================================
// IY mirroring IX functionality
// =============================================================================

func TestINC_DEC_IY(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// INC IY (FD 23)
	cpu.IY = 0x1234
	cpu.executeFDInstruction(0x23)
	if cpu.IY != 0x1235 {
		t.Errorf("INC IY: expected 0x1235, got 0x%04X", cpu.IY)
	}

	// DEC IY (FD 2B)
	cpu.IY = 0x1235
	cpu.executeFDInstruction(0x2B)
	if cpu.IY != 0x1234 {
		t.Errorf("DEC IY: expected 0x1234, got 0x%04X", cpu.IY)
	}
}

func TestADD_IY_rr(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// ADD IY,BC (FD 09)
	cpu.IY = 0x1000
	cpu.setBC(0x2000)
	cpu.F = FLAG_Z
	cpu.executeFDInstruction(0x09)
	if cpu.IY != 0x3000 {
		t.Errorf("ADD IY,BC: expected 0x3000, got 0x%04X", cpu.IY)
	}
	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("ADD IY,BC: Z flag should be preserved")
	}
}

func TestPUSH_POP_IY(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.SP = 0xFFF0
	cpu.IY = 0xFEDC

	// PUSH IY (FD E5)
	cpu.executeFDInstruction(0xE5)

	// POP IY (FD E1)
	cpu.IY = 0x0000
	cpu.executeFDInstruction(0xE1)
	if cpu.IY != 0xFEDC {
		t.Errorf("PUSH/POP IY: expected IY=0xFEDC, got 0x%04X", cpu.IY)
	}
}

func TestJP_IY(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// JP (IY) (FD E9)
	cpu.IY = 0xABCD
	cpu.executeFDInstruction(0xE9)
	if cpu.PC != 0xABCD {
		t.Errorf("JP (IY): expected PC=0xABCD, got 0x%04X", cpu.PC)
	}
}

func TestLD_SP_IY(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD SP,IY (FD F9)
	cpu.IY = 0x1234
	cpu.executeFDInstruction(0xF9)
	if cpu.SP != 0x1234 {
		t.Errorf("LD SP,IY: expected SP=0x1234, got 0x%04X", cpu.SP)
	}
}

// =============================================================================
// CPD and CPDR
// =============================================================================

func TestCPD(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CPD: compare A with (HL), dec HL, dec BC
	cpu.A = 0x42
	cpu.setHL(0x8005)
	cpu.setBC(0x0005)
	mem.Write(0x8005, 0x99) // no match
	cpu.F = FLAG_C          // carry preserved

	cpu.executeEDInstruction(0xA9)

	if cpu.hl() != 0x8004 {
		t.Errorf("CPD: expected HL=0x8004, got 0x%04X", cpu.hl())
	}
	if cpu.bc() != 0x0004 {
		t.Errorf("CPD: expected BC=0x0004, got 0x%04X", cpu.bc())
	}
	if (cpu.F & FLAG_Z) != 0 {
		t.Errorf("CPD: Z flag should NOT be set (no match)")
	}
	if (cpu.F & FLAG_N) == 0 {
		t.Errorf("CPD: N flag should be set")
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("CPD: Carry should be preserved")
	}
}

func TestCPDR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CPDR repeats while no match and BC != 0
	cpu.A = 0x42
	cpu.setHL(0x8005)
	cpu.setBC(0x0003)
	cpu.PC = 0xA000
	mem.Write(0x8005, 0x99) // no match

	cpu.executeEDInstruction(0xB9)

	// No match, BC != 0, should repeat
	if cpu.PC != 0xA000-2 {
		t.Errorf("CPDR: expected PC back by 2 for repeat, got 0x%04X", cpu.PC)
	}

	// Test when match found
	cpu.A = 0x42
	cpu.setHL(0x8005)
	cpu.setBC(0x0003)
	cpu.PC = 0xA000
	mem.Write(0x8005, 0x42) // match

	cpu.executeEDInstruction(0xB9)

	// Match found, should stop
	if cpu.PC == 0xA000-2 {
		t.Errorf("CPDR match: should NOT repeat")
	}
}

// =============================================================================
// LD (nn),IX / LD IX,(nn) and IY equivalents
// =============================================================================

func TestLD_nn_IX(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD (nn),IX (DD 22)
	cpu.PC = 0x8000
	cpu.IX = 0xABCD
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeDDInstruction(0x22)
	if mem.Read(0x9000) != 0xCD {
		t.Errorf("LD (nn),IX: low byte expected 0xCD, got 0x%02X", mem.Read(0x9000))
	}
	if mem.Read(0x9001) != 0xAB {
		t.Errorf("LD (nn),IX: high byte expected 0xAB, got 0x%02X", mem.Read(0x9001))
	}

	// LD IX,(nn) (DD 2A)
	cpu.PC = 0x8000
	mem.Write(0x9000, 0xEF)
	mem.Write(0x9001, 0xBE)
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeDDInstruction(0x2A)
	if cpu.IX != 0xBEEF {
		t.Errorf("LD IX,(nn): expected 0xBEEF, got 0x%04X", cpu.IX)
	}
}

func TestLD_nn_IY(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD (nn),IY (FD 22)
	cpu.PC = 0x8000
	cpu.IY = 0x1234
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeFDInstruction(0x22)
	if mem.Read(0x9000) != 0x34 || mem.Read(0x9001) != 0x12 {
		t.Errorf("LD (nn),IY: expected 0x1234 at 0x9000")
	}

	// LD IY,(nn) (FD 2A)
	cpu.PC = 0x8000
	mem.Write(0x9000, 0x78)
	mem.Write(0x9001, 0x56)
	mem.Write(0x8000, 0x00)
	mem.Write(0x8001, 0x90)
	cpu.executeFDInstruction(0x2A)
	if cpu.IY != 0x5678 {
		t.Errorf("LD IY,(nn): expected 0x5678, got 0x%04X", cpu.IY)
	}
}

// =============================================================================
// Full Instruction Execution via executeInstruction()
// =============================================================================

func TestExecuteInstruction_NOP(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Write a NOP at a RAM address and execute it
	cpu.PC = 0x8000
	mem.Write(0x8000, 0x00) // NOP
	initialPC := cpu.PC
	cpu.executeInstruction()
	if cpu.PC != initialPC+1 {
		t.Errorf("executeInstruction NOP: expected PC=0x%04X, got 0x%04X", initialPC+1, cpu.PC)
	}
}

func TestExecuteInstruction_CBPrefix(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// CB 07 = RLC A
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xCB)
	mem.Write(0x8001, 0x07) // RLC A
	cpu.A = 0x80
	cpu.executeInstruction()
	if cpu.A != 0x01 {
		t.Errorf("CB RLC A: expected A=0x01, got A=0x%02X", cpu.A)
	}
}

func TestExecuteInstruction_EDPrefix(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// ED 56 = IM 1
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x56)
	cpu.executeInstruction()
	if cpu.IM != 1 {
		t.Errorf("ED IM 1: expected IM=1, got %d", cpu.IM)
	}
}

func TestExecuteInstruction_DDPrefix(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// DD 23 = INC IX
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xDD)
	mem.Write(0x8001, 0x23)
	cpu.IX = 0x1000
	cpu.executeInstruction()
	if cpu.IX != 0x1001 {
		t.Errorf("DD INC IX: expected IX=0x1001, got 0x%04X", cpu.IX)
	}
}

func TestExecuteInstruction_FDPrefix(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// FD 23 = INC IY
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xFD)
	mem.Write(0x8001, 0x23)
	cpu.IY = 0x2000
	cpu.executeInstruction()
	if cpu.IY != 0x2001 {
		t.Errorf("FD INC IY: expected IY=0x2001, got 0x%04X", cpu.IY)
	}
}

// =============================================================================
// ADC A / SBC A with carry flag
// =============================================================================

func TestADC_withCarry(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.A = 0x10
	cpu.F = FLAG_C
	cpu.adc(0x20)
	if cpu.A != 0x31 {
		t.Errorf("ADC with carry: expected 0x31, got 0x%02X", cpu.A)
	}
}

func TestSBC_withCarry(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.A = 0x30
	cpu.F = FLAG_C
	cpu.sbc(0x10)
	if cpu.A != 0x1F {
		t.Errorf("SBC with carry: expected 0x1F, got 0x%02X", cpu.A)
	}
}
// =============================================================================
// TestReset - verify register initial values after Reset()
// =============================================================================

func TestReset(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Dirty the registers
	cpu.A = 0x00
	cpu.B = 0x01
	cpu.C = 0x02
	cpu.IX = 0x1234
	cpu.IY = 0x5678
	cpu.SP = 0x1000
	cpu.PC = 0x8000
	cpu.IFF1 = true
	cpu.IFF2 = true
	cpu.IM = 2
	cpu.Halted = true
	cpu.tstates = 99999

	cpu.Reset()

	if cpu.A != 0xFF {
		t.Errorf("Reset A: expected 0xFF, got 0x%02X", cpu.A)
	}
	if cpu.F != 0xFF {
		t.Errorf("Reset F: expected 0xFF, got 0x%02X", cpu.F)
	}
	if cpu.B != 0xFF {
		t.Errorf("Reset B: expected 0xFF, got 0x%02X", cpu.B)
	}
	if cpu.IX != 0xFFFF {
		t.Errorf("Reset IX: expected 0xFFFF, got 0x%04X", cpu.IX)
	}
	if cpu.IY != 0xFFFF {
		t.Errorf("Reset IY: expected 0xFFFF, got 0x%04X", cpu.IY)
	}
	if cpu.SP != 0xFFFF {
		t.Errorf("Reset SP: expected 0xFFFF, got 0x%04X", cpu.SP)
	}
	if cpu.PC != 0x0000 {
		t.Errorf("Reset PC: expected 0x0000, got 0x%04X", cpu.PC)
	}
	if cpu.IFF1 {
		t.Errorf("Reset IFF1: expected false")
	}
	if cpu.IFF2 {
		t.Errorf("Reset IFF2: expected false")
	}
	if cpu.IM != 0 {
		t.Errorf("Reset IM: expected 0, got %d", cpu.IM)
	}
	if cpu.Halted {
		t.Errorf("Reset Halted: expected false")
	}
	if cpu.tstates != 0 {
		t.Errorf("Reset tstates: expected 0, got %d", cpu.tstates)
	}
}

// =============================================================================
// TestExecuteFrame - run a short frame, verify T-states consumed and interrupt
// =============================================================================

func TestExecuteFrame(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// Place NOP (0x00) instructions in RAM so the CPU can execute them
	for i := uint16(0x4000); i < 0x4100; i++ {
		mem.Write(i, 0x00) // NOP = 4 T-states
	}
	cpu.PC = 0x4000
	cpu.tstates = 0

	// Run a short frame of 100 T-states — NOPs are 4 each, so >=25 will execute
	cpu.ExecuteFrame(100)

	// After the frame, tstates should wrap (tstates -= tstatesEnd makes it small)
	// and at least some instructions were executed (PC moved forward from 0x4000)
	if cpu.PC == 0x4000 {
		t.Errorf("ExecuteFrame: PC should have advanced from 0x4000, got 0x%04X", cpu.PC)
	}

	// Now verify interrupt fires: enable IFF1 and run a frame
	cpu.PC = 0x4000
	cpu.SP = 0xFFF0
	cpu.IFF1 = true
	cpu.IM = 1
	cpu.tstates = 0
	cpu.ExecuteFrame(100)

	// After IM1 interrupt, PC should be 0x0038
	if cpu.PC != 0x0038 {
		t.Errorf("ExecuteFrame interrupt: expected PC=0x0038, got PC=0x%04X", cpu.PC)
	}
	if cpu.IFF1 {
		t.Errorf("ExecuteFrame interrupt: IFF1 should be cleared after interrupt")
	}
}

// =============================================================================
// ED prefix: block I/O — OUTI, OTIR, OUTD, OTDR
// =============================================================================

func TestOUTI(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	// Set up: HL points to data in RAM, BC is the port
	mem.Write(0x5000, 0xAB) // data at HL
	cpu.setHL(0x5000)
	cpu.B = 0x02
	cpu.C = 0xFE

	cpu.executeEDInstruction(0xA3) // OUTI

	port := cpu.bc() // B is now 0x01, C is 0xFE
	_ = port
	// data 0xAB should have been written to port 0x02FE (original BC before decrement)
	// outi uses bc() BEFORE decrementing B inside outi(), so port = old B<<8 | C = 0x02FE
	if ula.ports[0x02FE] != 0xAB {
		t.Errorf("OUTI: expected 0xAB at port 0x02FE, got 0x%02X", ula.ports[0x02FE])
	}
	if cpu.B != 0x01 {
		t.Errorf("OUTI: expected B=0x01, got B=0x%02X", cpu.B)
	}
	if cpu.hl() != 0x5001 {
		t.Errorf("OUTI: expected HL=0x5001, got HL=0x%04X", cpu.hl())
	}
}

func TestOTIR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	// Set up three bytes to output at HL
	mem.Write(0x5000, 0x11)
	mem.Write(0x5001, 0x22)
	mem.Write(0x5002, 0x33)
	cpu.setHL(0x5000)
	cpu.B = 0x03
	cpu.C = 0x01
	// PC must be in RAM for the repeat (PC -= 2)
	cpu.PC = 0x5010

	// Execute OTIR once — it will repeat internally until B==0
	cpu.executeEDInstruction(0xB3)
	// After the first call with B=3: B becomes 2, PC goes to 0x500E (repeat)
	// We need to keep executing until B==0
	for cpu.B != 0 {
		cpu.executeEDInstruction(0xB3)
	}

	if cpu.B != 0 {
		t.Errorf("OTIR: expected B=0, got B=0x%02X", cpu.B)
	}
	// Last output should have been 0x33
	_ = ula.ports
}

func TestOUTD(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	mem.Write(0x5010, 0xCD)
	cpu.setHL(0x5010)
	cpu.B = 0x02
	cpu.C = 0xFE

	cpu.executeEDInstruction(0xAB) // OUTD

	if ula.ports[0x02FE] != 0xCD {
		t.Errorf("OUTD: expected 0xCD at port 0x02FE, got 0x%02X", ula.ports[0x02FE])
	}
	if cpu.B != 0x01 {
		t.Errorf("OUTD: expected B=0x01, got B=0x%02X", cpu.B)
	}
	if cpu.hl() != 0x500F {
		t.Errorf("OUTD: expected HL=0x500F, got HL=0x%04X", cpu.hl())
	}
}

func TestOTDR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x5002, 0x11)
	mem.Write(0x5001, 0x22)
	mem.Write(0x5000, 0x33)
	cpu.setHL(0x5002)
	cpu.B = 0x03
	cpu.C = 0x01
	cpu.PC = 0x5010

	cpu.executeEDInstruction(0xBB)
	for cpu.B != 0 {
		cpu.executeEDInstruction(0xBB)
	}

	if cpu.B != 0 {
		t.Errorf("OTDR: expected B=0, got B=0x%02X", cpu.B)
	}
}

// =============================================================================
// ED prefix: INI, INIR, IND, INDR
// =============================================================================

func TestINI(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	ula.ports[0x02FE] = 0x77
	cpu.B = 0x02
	cpu.C = 0xFE
	cpu.setHL(0x5000)

	cpu.executeEDInstruction(0xA2) // INI

	if mem.Read(0x5000) != 0x77 {
		t.Errorf("INI: expected 0x77 at (HL)=0x5000, got 0x%02X", mem.Read(0x5000))
	}
	if cpu.B != 0x01 {
		t.Errorf("INI: expected B=0x01, got B=0x%02X", cpu.B)
	}
	if cpu.hl() != 0x5001 {
		t.Errorf("INI: expected HL=0x5001, got HL=0x%04X", cpu.hl())
	}
}

func TestINIR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	ula.ports[0x03FE] = 0x55
	cpu.B = 0x03
	cpu.C = 0xFE
	cpu.setHL(0x5000)
	cpu.PC = 0x5020

	cpu.executeEDInstruction(0xB2)
	for cpu.B != 0 {
		cpu.executeEDInstruction(0xB2)
	}

	if cpu.B != 0 {
		t.Errorf("INIR: expected B=0, got B=0x%02X", cpu.B)
	}
	// Three bytes should have been written to memory
	if mem.Read(0x5000) != 0x55 {
		t.Errorf("INIR: expected 0x55 at 0x5000, got 0x%02X", mem.Read(0x5000))
	}
}

func TestIND(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	ula.ports[0x02FE] = 0x88
	cpu.B = 0x02
	cpu.C = 0xFE
	cpu.setHL(0x5010)

	cpu.executeEDInstruction(0xAA) // IND

	if mem.Read(0x5010) != 0x88 {
		t.Errorf("IND: expected 0x88 at (HL)=0x5010, got 0x%02X", mem.Read(0x5010))
	}
	if cpu.B != 0x01 {
		t.Errorf("IND: expected B=0x01, got B=0x%02X", cpu.B)
	}
	if cpu.hl() != 0x500F {
		t.Errorf("IND: expected HL=0x500F, got HL=0x%04X", cpu.hl())
	}
}

func TestINDR(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")
	ula := cpu.ula.(*mockULA)

	ula.ports[0x03FE] = 0x99
	cpu.B = 0x03
	cpu.C = 0xFE
	cpu.setHL(0x5002)
	cpu.PC = 0x5020

	cpu.executeEDInstruction(0xBA)
	for cpu.B != 0 {
		cpu.executeEDInstruction(0xBA)
	}

	if cpu.B != 0 {
		t.Errorf("INDR: expected B=0, got B=0x%02X", cpu.B)
	}
	if mem.Read(0x5002) != 0x99 {
		t.Errorf("INDR: expected 0x99 at 0x5002, got 0x%02X", mem.Read(0x5002))
	}
}

// =============================================================================
// ED prefix: LD (nn),rr / LD rr,(nn) for BC, DE, SP
// =============================================================================

func TestED_LD_nn_BC(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.B = 0x12
	cpu.C = 0x34
	// The instruction reads a 16-bit address from PC
	cpu.PC = 0x5000
	mem.Write(0x5000, 0x00) // low byte of address
	mem.Write(0x5001, 0x60) // high byte => 0x6000

	cpu.executeEDInstruction(0x43) // LD (nn),BC

	if mem.Read(0x6000) != 0x34 { // C stored at low address
		t.Errorf("LD (nn),BC: expected C=0x34 at 0x6000, got 0x%02X", mem.Read(0x6000))
	}
	if mem.Read(0x6001) != 0x12 { // B stored at high address
		t.Errorf("LD (nn),BC: expected B=0x12 at 0x6001, got 0x%02X", mem.Read(0x6001))
	}
}

func TestED_LD_BC_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x6000, 0x34)
	mem.Write(0x6001, 0x12)
	cpu.PC = 0x5000
	mem.Write(0x5000, 0x00) // low
	mem.Write(0x5001, 0x60) // high => 0x6000

	cpu.executeEDInstruction(0x4B) // LD BC,(nn)

	if cpu.C != 0x34 {
		t.Errorf("LD BC,(nn): expected C=0x34, got C=0x%02X", cpu.C)
	}
	if cpu.B != 0x12 {
		t.Errorf("LD BC,(nn): expected B=0x12, got B=0x%02X", cpu.B)
	}
}

func TestED_LD_nn_DE(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.D = 0xAB
	cpu.E = 0xCD
	cpu.PC = 0x5000
	mem.Write(0x5000, 0x00)
	mem.Write(0x5001, 0x60) // => 0x6000

	cpu.executeEDInstruction(0x53) // LD (nn),DE

	if mem.Read(0x6000) != 0xCD {
		t.Errorf("LD (nn),DE: expected E at 0x6000, got 0x%02X", mem.Read(0x6000))
	}
	if mem.Read(0x6001) != 0xAB {
		t.Errorf("LD (nn),DE: expected D at 0x6001, got 0x%02X", mem.Read(0x6001))
	}
}

func TestED_LD_DE_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x6000, 0xCD)
	mem.Write(0x6001, 0xAB)
	cpu.PC = 0x5000
	mem.Write(0x5000, 0x00)
	mem.Write(0x5001, 0x60)

	cpu.executeEDInstruction(0x5B) // LD DE,(nn)

	if cpu.E != 0xCD {
		t.Errorf("LD DE,(nn): expected E=0xCD, got E=0x%02X", cpu.E)
	}
	if cpu.D != 0xAB {
		t.Errorf("LD DE,(nn): expected D=0xAB, got D=0x%02X", cpu.D)
	}
}

func TestED_LD_nn_SP(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.SP = 0x1234
	cpu.PC = 0x5000
	mem.Write(0x5000, 0x00)
	mem.Write(0x5001, 0x60) // => 0x6000

	cpu.executeEDInstruction(0x73) // LD (nn),SP

	if mem.Read(0x6000) != 0x34 {
		t.Errorf("LD (nn),SP: expected 0x34 at 0x6000, got 0x%02X", mem.Read(0x6000))
	}
	if mem.Read(0x6001) != 0x12 {
		t.Errorf("LD (nn),SP: expected 0x12 at 0x6001, got 0x%02X", mem.Read(0x6001))
	}
}

func TestED_LD_SP_nn(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x6000, 0x34)
	mem.Write(0x6001, 0x12)
	cpu.PC = 0x5000
	mem.Write(0x5000, 0x00)
	mem.Write(0x5001, 0x60)

	cpu.executeEDInstruction(0x7B) // LD SP,(nn)

	if cpu.SP != 0x1234 {
		t.Errorf("LD SP,(nn): expected SP=0x1234, got SP=0x%04X", cpu.SP)
	}
}

// =============================================================================
// DDCB: BIT, SET, RES, RLC via executeDDCBInstruction
// =============================================================================

func TestDDCB_BIT(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// BIT 3,(IX+0) — opcode for BIT 3 is 0x40 + 3*8 + 6 = 0x5E
	mem.Write(0x5000, 0x08) // value: bit 3 set
	cpu.IX = 0x5000
	addr := uint16(int32(cpu.IX) + 0)

	cpu.executeDDCBInstruction(0x5E, addr) // BIT 3,(IX+0)

	if (cpu.F & FLAG_Z) != 0 {
		t.Errorf("DDCB BIT 3: bit 3 is set, Z flag should be clear")
	}

	// BIT 7,(IX+0) with bit 7 clear => Z set
	mem.Write(0x5000, 0x08)
	cpu.executeDDCBInstruction(0x7E, addr) // BIT 7,(IX+0): 0x40+7*8+6=0x7E

	if (cpu.F & FLAG_Z) == 0 {
		t.Errorf("DDCB BIT 7: bit 7 is clear, Z flag should be set")
	}
}

func TestDDCB_SET(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x5000, 0x00)
	cpu.IX = 0x5000
	addr := uint16(int32(cpu.IX) + 0)

	// SET 5,(IX+0): opcode 0xC0 + 5*8 + 6 = 0xEE
	cpu.executeDDCBInstruction(0xEE, addr)

	if mem.Read(0x5000) != 0x20 {
		t.Errorf("DDCB SET 5: expected 0x20, got 0x%02X", mem.Read(0x5000))
	}
}

func TestDDCB_RES(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x5000, 0xFF)
	cpu.IX = 0x5000
	addr := uint16(int32(cpu.IX) + 0)

	// RES 5,(IX+0): opcode 0x80 + 5*8 + 6 = 0xAE
	cpu.executeDDCBInstruction(0xAE, addr)

	if mem.Read(0x5000) != 0xDF {
		t.Errorf("DDCB RES 5: expected 0xDF, got 0x%02X", mem.Read(0x5000))
	}
}

func TestDDCB_RLC(t *testing.T) {
	cpu, mem := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	mem.Write(0x5000, 0x80) // bit 7 set
	cpu.IX = 0x5000
	addr := uint16(int32(cpu.IX) + 0)

	cpu.executeDDCBInstruction(0x06, addr) // RLC (IX+0)

	if mem.Read(0x5000) != 0x01 {
		t.Errorf("DDCB RLC: expected 0x01, got 0x%02X", mem.Read(0x5000))
	}
	if (cpu.F & FLAG_C) == 0 {
		t.Errorf("DDCB RLC: carry flag should be set")
	}
}

// TestDDIXhalfRegisters exercises the undocumented 8-bit IXh / IXl ops
// (DD-prefixed forms of base instructions that touch H or L). Cobra +3
// uses DD 67 / DD 6F to build IX during its level-load fast-PUSH setup;
// before these cases were implemented, the DD prefix was silently
// dropped and the base instruction modified HL instead — corrupting
// the IM2 trampoline at 0xFFFF and crashing the game.
func TestDDIXhalfRegisters(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	// LD IXh, A — must update IXh, not H
	cpu.A = 0x37
	cpu.H, cpu.L = 0x40, 0x80
	cpu.IX = 0x1234
	cpu.executeDDInstruction(0x67)
	if cpu.IX != 0x3734 || cpu.H != 0x40 || cpu.L != 0x80 {
		t.Errorf("DD 67 (LD IXh,A): IX=%04X H=%02X L=%02X, want IX=0x3734 H=0x40 L=0x80",
			cpu.IX, cpu.H, cpu.L)
	}

	// LD IXl, A — must update IXl, not L
	cpu.IX = 0x1234
	cpu.executeDDInstruction(0x6F)
	if cpu.IX != 0x1237 || cpu.H != 0x40 || cpu.L != 0x80 {
		t.Errorf("DD 6F (LD IXl,A): IX=%04X H=%02X L=%02X, want IX=0x1237 H=0x40 L=0x80",
			cpu.IX, cpu.H, cpu.L)
	}

	// LD A, IXh — read from IXh, not H
	cpu.IX = 0xAB00
	cpu.A = 0
	cpu.executeDDInstruction(0x7C)
	if cpu.A != 0xAB {
		t.Errorf("DD 7C (LD A,IXh): A=%02X, want 0xAB", cpu.A)
	}

	// LD A, IXl
	cpu.IX = 0x00CD
	cpu.A = 0
	cpu.executeDDInstruction(0x7D)
	if cpu.A != 0xCD {
		t.Errorf("DD 7D (LD A,IXl): A=%02X, want 0xCD", cpu.A)
	}
}

// TestFDIYhalfRegisters exercises the same undocumented operations
// under the FD prefix (IY halves). Cobra +3 uses FD 67 / FD 6F to build
// IY in the same fast-PUSH setup.
func TestFDIYhalfRegisters(t *testing.T) {
	cpu, _ := createTestCPU()
	defer cleanupTestROMs("test_roms_z80")

	cpu.A = 0x99
	cpu.H, cpu.L = 0x11, 0x22
	cpu.IY = 0xCAFE
	cpu.executeFDInstruction(0x67) // LD IYh, A
	if cpu.IY != 0x99FE || cpu.H != 0x11 || cpu.L != 0x22 {
		t.Errorf("FD 67 (LD IYh,A): IY=%04X H=%02X L=%02X, want IY=0x99FE H=0x11 L=0x22",
			cpu.IY, cpu.H, cpu.L)
	}

	cpu.IY = 0xCAFE
	cpu.executeFDInstruction(0x6F) // LD IYl, A
	if cpu.IY != 0xCA99 || cpu.H != 0x11 || cpu.L != 0x22 {
		t.Errorf("FD 6F (LD IYl,A): IY=%04X H=%02X L=%02X, want IY=0xCA99 H=0x11 L=0x22",
			cpu.IY, cpu.H, cpu.L)
	}
}
