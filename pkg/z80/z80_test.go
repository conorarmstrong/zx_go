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
	os.RemoveAll(dir)
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