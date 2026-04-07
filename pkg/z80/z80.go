package z80

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// Flag bit positions
const (
	FLAG_C  = 0x01 // Carry flag
	FLAG_N  = 0x02 // Add/Subtract flag
	FLAG_PV = 0x04 // Parity/Overflow flag
	FLAG_F3 = 0x08 // Undocumented flag 3
	FLAG_H  = 0x10 // Half-carry flag
	FLAG_F5 = 0x20 // Undocumented flag 5
	FLAG_Z  = 0x40 // Zero flag
	FLAG_S  = 0x80 // Sign flag
)

// CPU represents the Z80 processor.
type CPU struct {
	// 8-bit registers
	A, F, B, C, D, E, H, L byte
	// Alternate registers
	A_, F_, B_, C_, D_, E_, H_, L_ byte
	// Index registers
	IX, IY uint16
	// Stack Pointer and Program Counter
	SP, PC uint16
	// Interrupt Vector and Memory Refresh registers
	I, R byte
	// Interrupt flip-flops
	IFF1, IFF2 bool
	// Interrupt mode
	IM byte
	// Halted state
	Halted bool

	// Memory and ULA interfaces
	mem *memory.Memory
	ula ULA

	// T-state counter for timing
	tstates uint64

	// IM2 interrupt vector low byte (0xFF on ZX Spectrum)
	IM2Vector byte

	// Breakpoint callback: called before each instruction during ExecuteFrame.
	// If it returns true, execution stops immediately (breakpoint hit).
	BreakpointCheck func(pc uint16) bool

	// TrapCheck is called before each instruction during ExecuteFrame.
	// If it returns true, the trap has handled the instruction (typically by
	// modifying CPU state and PC) and the normal fetch/execute cycle is
	// skipped. Used to short-circuit known ROM routines such as the 48K
	// LD-BYTES tape loader at 0x0556.
	TrapCheck func(pc uint16) bool

	// PendingNMI is set from any goroutine to signal an NMI. The CPU
	// processes it at the next safe point in ExecuteFrame.
	PendingNMI atomic.Bool

	// NMICallback is called just before the NMI is executed, allowing
	// peripherals to page in their ROM at the exact right moment.
	NMICallback func()

	// For debugging
	logEnabled bool

	// Pre-computed lookup tables for flag calculation
	sz53Table         [256]byte // Sign, Zero, and undocumented flags table
	parityTable       [256]byte // Parity table
	halfcarryAddTable [8]byte   // Half-carry flag for ADD operations
	halfcarrySubTable [8]byte   // Half-carry flag for SUB operations
	overflowAddTable  [8]byte   // Overflow flag for ADD operations
	overflowSubTable  [8]byte   // Overflow flag for SUB operations
}

// ULA is an interface that the CPU uses to interact with the ULA.
type ULA interface {
	ReadPort(addr uint16) (byte, bool)
	WritePort(addr uint16, val byte)
}

// New creates a new Z80 CPU instance.
func New(mem *memory.Memory, ula ULA) *CPU {
	c := &CPU{
		mem: mem,
		ula: ula,
	}
	c.initTables()
	c.Reset()
	// Enable contention by sharing T-state counter with memory
	mem.TStates = &c.tstates
	mem.ContentionEnabled = true
	return c
}

// Reset resets the CPU to its initial state.
func (c *CPU) Reset() {
	c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L = 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF
	c.A_, c.F_, c.B_, c.C_, c.D_, c.E_, c.H_, c.L_ = 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF
	c.IX, c.IY = 0xFFFF, 0xFFFF
	c.SP, c.PC = 0xFFFF, 0x0000
	c.I, c.R = 0, 0
	c.IFF1, c.IFF2 = false, false
	c.IM = 0
	c.Halted = false
	c.tstates = 0
	c.IM2Vector = 0xFF // ZX Spectrum ULA puts 0xFF on data bus during INTA
}

// Run starts a standalone emulation loop at 50Hz.
// Note: the main application uses its own run loop instead of this method.
func (c *CPU) Run() {
	ticker := time.NewTicker(20 * time.Millisecond) // 50Hz
	defer ticker.Stop()

	for range ticker.C {
		c.ExecuteFrame(69888) // 48K: 69888, 128K+: 70908
	}
}

func (c *CPU) ExecuteFrame(tstatesPerFrame int) {
	tstatesEnd := c.tstates + uint64(tstatesPerFrame)
	for c.tstates < tstatesEnd {
		if c.Halted {
			// NMI during HALT: the CPU is waiting with IFF1=true (after EI).
			// This is the ideal time for NMI — IFF2 captures IFF1=true so
			// RETN can restore interrupts correctly.
			if c.PendingNMI.CompareAndSwap(true, false) {
				if c.NMICallback != nil {
					c.NMICallback()
				}
				c.NMI()
				continue
			}
			c.tstates++
			continue
		}
		if c.BreakpointCheck != nil && c.BreakpointCheck(c.PC) {
			return
		}
		if c.TrapCheck != nil && c.TrapCheck(c.PC) {
			// Trap handled the "instruction" (typically a ROM routine);
			// skip the real fetch/execute and continue the loop.
			continue
		}
		c.executeInstruction()
		// NMI check after each instruction (NMI is non-maskable)
		if c.PendingNMI.CompareAndSwap(true, false) {
			if c.NMICallback != nil {
				c.NMICallback()
			}
			c.NMI()
		}
	}
	
	// Process interrupt at end of frame (50Hz for ZX Spectrum)
	if c.IFF1 {
		c.interrupt()
	}
	
	c.tstates -= tstatesEnd
}

func (c *CPU) executeInstruction() {
	opcode := c.fetch()
	c.executeBaseInstruction(opcode)
}

// StepInstruction executes exactly one Z80 instruction without
// checking for interrupts. Used by the debugger for single-stepping.
func (c *CPU) StepInstruction() {
	if c.Halted {
		c.tstates += 4
		return
	}
	c.executeInstruction()
}

func (c *CPU) executeBaseInstruction(opcode byte) {
	switch opcode {
	// 8-bit load group
	case 0x40: // LD B,B
		c.tstates += 4
	case 0x41: // LD B,C
		c.B = c.C; c.tstates += 4
	case 0x42: // LD B,D
		c.B = c.D; c.tstates += 4
	case 0x43: // LD B,E
		c.B = c.E; c.tstates += 4
	case 0x44: // LD B,H
		c.B = c.H; c.tstates += 4
	case 0x45: // LD B,L
		c.B = c.L; c.tstates += 4
	case 0x46: // LD B,(HL)
		c.B = c.mem.Read(c.hl()); c.tstates += 7
	case 0x47: // LD B,A
		c.B = c.A; c.tstates += 4
	case 0x48: // LD C,B
		c.C = c.B; c.tstates += 4
	case 0x49: // LD C,C
		c.tstates += 4
	case 0x4A: // LD C,D
		c.C = c.D; c.tstates += 4
	case 0x4B: // LD C,E
		c.C = c.E; c.tstates += 4
	case 0x4C: // LD C,H
		c.C = c.H; c.tstates += 4
	case 0x4D: // LD C,L
		c.C = c.L; c.tstates += 4
	case 0x4E: // LD C,(HL)
		c.C = c.mem.Read(c.hl()); c.tstates += 7
	case 0x4F: // LD C,A
		c.C = c.A; c.tstates += 4
	case 0x50: // LD D,B
		c.D = c.B; c.tstates += 4
	case 0x51: // LD D,C
		c.D = c.C; c.tstates += 4
	case 0x52: // LD D,D
		c.tstates += 4
	case 0x53: // LD D,E
		c.D = c.E; c.tstates += 4
	case 0x54: // LD D,H
		c.D = c.H; c.tstates += 4
	case 0x55: // LD D,L
		c.D = c.L; c.tstates += 4
	case 0x56: // LD D,(HL)
		c.D = c.mem.Read(c.hl()); c.tstates += 7
	case 0x57: // LD D,A
		c.D = c.A; c.tstates += 4
	case 0x58: // LD E,B
		c.E = c.B; c.tstates += 4
	case 0x59: // LD E,C
		c.E = c.C; c.tstates += 4
	case 0x5A: // LD E,D
		c.E = c.D; c.tstates += 4
	case 0x5B: // LD E,E
		c.tstates += 4
	case 0x5C: // LD E,H
		c.E = c.H; c.tstates += 4
	case 0x5D: // LD E,L
		c.E = c.L; c.tstates += 4
	case 0x5E: // LD E,(HL)
		c.E = c.mem.Read(c.hl()); c.tstates += 7
	case 0x5F: // LD E,A
		c.E = c.A; c.tstates += 4
	case 0x60: // LD H,B
		c.H = c.B; c.tstates += 4
	case 0x61: // LD H,C
		c.H = c.C; c.tstates += 4
	case 0x62: // LD H,D
		c.H = c.D; c.tstates += 4
	case 0x63: // LD H,E
		c.H = c.E; c.tstates += 4
	case 0x64: // LD H,H
		c.tstates += 4
	case 0x65: // LD H,L
		c.H = c.L; c.tstates += 4
	case 0x66: // LD H,(HL)
		c.H = c.mem.Read(c.hl()); c.tstates += 7
	case 0x67: // LD H,A
		c.H = c.A; c.tstates += 4
	case 0x68: // LD L,B
		c.L = c.B; c.tstates += 4
	case 0x69: // LD L,C
		c.L = c.C; c.tstates += 4
	case 0x6A: // LD L,D
		c.L = c.D; c.tstates += 4
	case 0x6B: // LD L,E
		c.L = c.E; c.tstates += 4
	case 0x6C: // LD L,H
		c.L = c.H; c.tstates += 4
	case 0x6D: // LD L,L
		c.tstates += 4
	case 0x6E: // LD L,(HL)
		c.L = c.mem.Read(c.hl()); c.tstates += 7
	case 0x6F: // LD L,A
		c.L = c.A; c.tstates += 4
	case 0x70: // LD (HL),B
		c.mem.Write(c.hl(), c.B); c.tstates += 7
	case 0x71: // LD (HL),C
		c.mem.Write(c.hl(), c.C); c.tstates += 7
	case 0x72: // LD (HL),D
		c.mem.Write(c.hl(), c.D); c.tstates += 7
	case 0x73: // LD (HL),E
		c.mem.Write(c.hl(), c.E); c.tstates += 7
	case 0x74: // LD (HL),H
		c.mem.Write(c.hl(), c.H); c.tstates += 7
	case 0x75: // LD (HL),L
		c.mem.Write(c.hl(), c.L); c.tstates += 7
	case 0x76: // HALT
		c.Halted = true; c.tstates += 4
	case 0x77: // LD (HL),A
		c.mem.Write(c.hl(), c.A); c.tstates += 7
	case 0x78: // LD A,B
		c.A = c.B; c.tstates += 4
	case 0x79: // LD A,C
		c.A = c.C; c.tstates += 4
	case 0x7A: // LD A,D
		c.A = c.D; c.tstates += 4
	case 0x7B: // LD A,E
		c.A = c.E; c.tstates += 4
	case 0x7C: // LD A,H
		c.A = c.H; c.tstates += 4
	case 0x7D: // LD A,L
		c.A = c.L; c.tstates += 4
	case 0x7E: // LD A,(HL)
		c.A = c.mem.Read(c.hl()); c.tstates += 7
	case 0x7F: // LD A,A
		c.tstates += 4

	// 8-bit load immediate
	case 0x06: // LD B,n
		c.B = c.readOperand(); c.tstates += 7
	case 0x07: // RLCA
		c.rlca(); c.tstates += 4
	case 0x0E: // LD C,n
		c.C = c.readOperand(); c.tstates += 7
	case 0x0F: // RRCA
		c.rrca(); c.tstates += 4
	case 0x16: // LD D,n
		c.D = c.readOperand(); c.tstates += 7
	case 0x17: // RLA
		c.rla(); c.tstates += 4
	case 0x1E: // LD E,n
		c.E = c.readOperand(); c.tstates += 7
	case 0x1F: // RRA
		c.rra(); c.tstates += 4
	case 0x26: // LD H,n
		c.H = c.readOperand(); c.tstates += 7
	case 0x2E: // LD L,n
		c.L = c.readOperand(); c.tstates += 7
	case 0x36: // LD (HL),n
		c.mem.Write(c.hl(), c.readOperand()); c.tstates += 10
	case 0x3E: // LD A,n
		c.A = c.readOperand(); c.tstates += 7

	// 16-bit load immediate
	case 0x01: // LD BC,nn
		c.setBC(c.fetch16()); c.tstates += 10
	case 0x11: // LD DE,nn
		c.setDE(c.fetch16()); c.tstates += 10
	case 0x21: // LD HL,nn
		c.setHL(c.fetch16()); c.tstates += 10
	case 0x31: // LD SP,nn
		c.SP = c.fetch16(); c.tstates += 10

	// Memory load/store
	case 0x02: // LD (BC),A
		c.mem.Write(c.bc(), c.A); c.tstates += 7
	case 0x0A: // LD A,(BC)
		c.A = c.mem.Read(c.bc()); c.tstates += 7
	case 0x12: // LD (DE),A
		c.mem.Write(c.de(), c.A); c.tstates += 7
	case 0x1A: // LD A,(DE)
		c.A = c.mem.Read(c.de()); c.tstates += 7
	case 0x22: // LD (nn),HL
		addr := c.fetch16()
		c.mem.Write(addr, c.L)
		c.mem.Write(addr+1, c.H)
		c.tstates += 16
	case 0x2A: // LD HL,(nn)
		addr := c.fetch16()
		c.L = c.mem.Read(addr)
		c.H = c.mem.Read(addr + 1)
		c.tstates += 16
	case 0x32: // LD (nn),A
		c.mem.Write(c.fetch16(), c.A); c.tstates += 13
	case 0x3A: // LD A,(nn)
		c.A = c.mem.Read(c.fetch16()); c.tstates += 13

	// Arithmetic
	case 0x80: // ADD A,B
		c.add(c.B); c.tstates += 4
	case 0x81: // ADD A,C
		c.add(c.C); c.tstates += 4
	case 0x82: // ADD A,D
		c.add(c.D); c.tstates += 4
	case 0x83: // ADD A,E
		c.add(c.E); c.tstates += 4
	case 0x84: // ADD A,H
		c.add(c.H); c.tstates += 4
	case 0x85: // ADD A,L
		c.add(c.L); c.tstates += 4
	case 0x86: // ADD A,(HL)
		c.add(c.mem.Read(c.hl())); c.tstates += 7
	case 0x87: // ADD A,A
		c.add(c.A); c.tstates += 4
	case 0xC6: // ADD A,n
		c.add(c.readOperand()); c.tstates += 7

	case 0x88: // ADC A,B
		c.adc(c.B); c.tstates += 4
	case 0x89: // ADC A,C
		c.adc(c.C); c.tstates += 4
	case 0x8A: // ADC A,D
		c.adc(c.D); c.tstates += 4
	case 0x8B: // ADC A,E
		c.adc(c.E); c.tstates += 4
	case 0x8C: // ADC A,H
		c.adc(c.H); c.tstates += 4
	case 0x8D: // ADC A,L
		c.adc(c.L); c.tstates += 4
	case 0x8E: // ADC A,(HL)
		c.adc(c.mem.Read(c.hl())); c.tstates += 7
	case 0x8F: // ADC A,A
		c.adc(c.A); c.tstates += 4
	case 0xCE: // ADC A,n
		c.adc(c.readOperand()); c.tstates += 7

	case 0x90: // SUB B
		c.sub(c.B); c.tstates += 4
	case 0x91: // SUB C
		c.sub(c.C); c.tstates += 4
	case 0x92: // SUB D
		c.sub(c.D); c.tstates += 4
	case 0x93: // SUB E
		c.sub(c.E); c.tstates += 4
	case 0x94: // SUB H
		c.sub(c.H); c.tstates += 4
	case 0x95: // SUB L
		c.sub(c.L); c.tstates += 4
	case 0x96: // SUB (HL)
		c.sub(c.mem.Read(c.hl())); c.tstates += 7
	case 0x97: // SUB A
		c.sub(c.A); c.tstates += 4
	case 0xD6: // SUB n
		c.sub(c.readOperand()); c.tstates += 7

	case 0x98: // SBC A,B
		c.sbc(c.B); c.tstates += 4
	case 0x99: // SBC A,C
		c.sbc(c.C); c.tstates += 4
	case 0x9A: // SBC A,D
		c.sbc(c.D); c.tstates += 4
	case 0x9B: // SBC A,E
		c.sbc(c.E); c.tstates += 4
	case 0x9C: // SBC A,H
		c.sbc(c.H); c.tstates += 4
	case 0x9D: // SBC A,L
		c.sbc(c.L); c.tstates += 4
	case 0x9E: // SBC A,(HL)
		c.sbc(c.mem.Read(c.hl())); c.tstates += 7
	case 0x9F: // SBC A,A
		c.sbc(c.A); c.tstates += 4
	case 0xDE: // SBC A,n
		c.sbc(c.readOperand()); c.tstates += 7

	// Logical
	case 0xA0: // AND B
		c.and(c.B); c.tstates += 4
	case 0xA1: // AND C
		c.and(c.C); c.tstates += 4
	case 0xA2: // AND D
		c.and(c.D); c.tstates += 4
	case 0xA3: // AND E
		c.and(c.E); c.tstates += 4
	case 0xA4: // AND H
		c.and(c.H); c.tstates += 4
	case 0xA5: // AND L
		c.and(c.L); c.tstates += 4
	case 0xA6: // AND (HL)
		c.and(c.mem.Read(c.hl())); c.tstates += 7
	case 0xA7: // AND A
		c.and(c.A); c.tstates += 4
	case 0xE6: // AND n
		c.and(c.readOperand()); c.tstates += 7

	case 0xA8: // XOR B
		c.xor(c.B); c.tstates += 4
	case 0xA9: // XOR C
		c.xor(c.C); c.tstates += 4
	case 0xAA: // XOR D
		c.xor(c.D); c.tstates += 4
	case 0xAB: // XOR E
		c.xor(c.E); c.tstates += 4
	case 0xAC: // XOR H
		c.xor(c.H); c.tstates += 4
	case 0xAD: // XOR L
		c.xor(c.L); c.tstates += 4
	case 0xAE: // XOR (HL)
		c.xor(c.mem.Read(c.hl())); c.tstates += 7
	case 0xAF: // XOR A
		c.xor(c.A); c.tstates += 4
	case 0xEE: // XOR n
		c.xor(c.readOperand()); c.tstates += 7

	case 0xB0: // OR B
		c.or(c.B); c.tstates += 4
	case 0xB1: // OR C
		c.or(c.C); c.tstates += 4
	case 0xB2: // OR D
		c.or(c.D); c.tstates += 4
	case 0xB3: // OR E
		c.or(c.E); c.tstates += 4
	case 0xB4: // OR H
		c.or(c.H); c.tstates += 4
	case 0xB5: // OR L
		c.or(c.L); c.tstates += 4
	case 0xB6: // OR (HL)
		c.or(c.mem.Read(c.hl())); c.tstates += 7
	case 0xB7: // OR A
		c.or(c.A); c.tstates += 4
	case 0xF6: // OR n
		c.or(c.readOperand()); c.tstates += 7

	case 0xB8: // CP B
		c.cp(c.B); c.tstates += 4
	case 0xB9: // CP C
		c.cp(c.C); c.tstates += 4
	case 0xBA: // CP D
		c.cp(c.D); c.tstates += 4
	case 0xBB: // CP E
		c.cp(c.E); c.tstates += 4
	case 0xBC: // CP H
		c.cp(c.H); c.tstates += 4
	case 0xBD: // CP L
		c.cp(c.L); c.tstates += 4
	case 0xBE: // CP (HL)
		c.cp(c.mem.Read(c.hl())); c.tstates += 7
	case 0xBF: // CP A
		c.cp(c.A); c.tstates += 4
	case 0xFE: // CP n
		c.cp(c.readOperand()); c.tstates += 7

	// Inc/Dec
	case 0x03: // INC BC
		c.setBC(c.bc() + 1); c.tstates += 6
	case 0x04: // INC B
		c.B = c.inc(c.B); c.tstates += 4
	case 0x05: // DEC B
		c.B = c.dec(c.B); c.tstates += 4
	case 0x0B: // DEC BC
		c.setBC(c.bc() - 1); c.tstates += 6
	case 0x0C: // INC C
		c.C = c.inc(c.C); c.tstates += 4
	case 0x0D: // DEC C
		c.C = c.dec(c.C); c.tstates += 4
	case 0x13: // INC DE
		c.setDE(c.de() + 1); c.tstates += 6
	case 0x14: // INC D
		c.D = c.inc(c.D); c.tstates += 4
	case 0x15: // DEC D
		c.D = c.dec(c.D); c.tstates += 4
	case 0x1B: // DEC DE
		c.setDE(c.de() - 1); c.tstates += 6
	case 0x1C: // INC E
		c.E = c.inc(c.E); c.tstates += 4
	case 0x1D: // DEC E
		c.E = c.dec(c.E); c.tstates += 4
	case 0x23: // INC HL
		c.setHL(c.hl() + 1); c.tstates += 6
	case 0x24: // INC H
		c.H = c.inc(c.H); c.tstates += 4
	case 0x25: // DEC H
		c.H = c.dec(c.H); c.tstates += 4
	case 0x2B: // DEC HL
		c.setHL(c.hl() - 1); c.tstates += 6
	case 0x2C: // INC L
		c.L = c.inc(c.L); c.tstates += 4
	case 0x2D: // DEC L
		c.L = c.dec(c.L); c.tstates += 4
	case 0x33: // INC SP
		c.SP++; c.tstates += 6
	case 0x34: // INC (HL)
		addr := c.hl()
		c.mem.Write(addr, c.inc(c.mem.Read(addr))); c.tstates += 11
	case 0x35: // DEC (HL)
		addr := c.hl()
		c.mem.Write(addr, c.dec(c.mem.Read(addr))); c.tstates += 11
	case 0x3B: // DEC SP
		c.SP--; c.tstates += 6
	case 0x3C: // INC A
		c.A = c.inc(c.A); c.tstates += 4
	case 0x3D: // DEC A
		c.A = c.dec(c.A); c.tstates += 4

	// 16-bit arithmetic
	case 0x09: // ADD HL,BC
		c.setHL(c.add16(c.hl(), c.bc())); c.tstates += 11
	case 0x19: // ADD HL,DE
		c.setHL(c.add16(c.hl(), c.de())); c.tstates += 11
	case 0x29: // ADD HL,HL
		c.setHL(c.add16(c.hl(), c.hl())); c.tstates += 11
	case 0x39: // ADD HL,SP
		c.setHL(c.add16(c.hl(), c.SP)); c.tstates += 11

	// Jumps
	case 0x18: // JR n
		offset := int8(c.readOperand())
		c.PC = uint16(int32(c.PC) + int32(offset))
		c.tstates += 12
	case 0x20: // JR NZ,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_Z) == 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.tstates += 12
		} else {
			c.tstates += 7
		}
	case 0x28: // JR Z,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_Z) != 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.tstates += 12
		} else {
			c.tstates += 7
		}
	case 0x30: // JR NC,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_C) == 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.tstates += 12
		} else {
			c.tstates += 7
		}
	case 0x38: // JR C,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_C) != 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.tstates += 12
		} else {
			c.tstates += 7
		}

	case 0xC2: // JP NZ,nn
		addr := c.fetch16()
		if (c.F & FLAG_Z) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xC3: // JP nn
		c.PC = c.fetch16(); c.tstates += 10
	case 0xCA: // JP Z,nn
		addr := c.fetch16()
		if (c.F & FLAG_Z) != 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xD2: // JP NC,nn
		addr := c.fetch16()
		if (c.F & FLAG_C) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xDA: // JP C,nn
		addr := c.fetch16()
		if (c.F & FLAG_C) != 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xE2: // JP PO,nn
		addr := c.fetch16()
		if (c.F & FLAG_PV) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xE9: // JP (HL)
		c.PC = c.hl(); c.tstates += 4
	case 0xEA: // JP PE,nn
		addr := c.fetch16()
		if (c.F & FLAG_PV) != 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xF2: // JP P,nn
		addr := c.fetch16()
		if (c.F & FLAG_S) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xFA: // JP M,nn
		addr := c.fetch16()
		if (c.F & FLAG_S) != 0 {
			c.PC = addr
		}
		c.tstates += 10

	// Calls and returns
	case 0xC4: // CALL NZ,nn
		addr := c.fetch16()
		if (c.F & FLAG_Z) == 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xC9: // RET
		c.PC = c.pop(); c.tstates += 10
	case 0xCC: // CALL Z,nn
		addr := c.fetch16()
		if (c.F & FLAG_Z) != 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xCD: // CALL nn
		addr := c.fetch16()
		c.push(c.PC)
		c.PC = addr
		c.tstates += 17
	case 0xD4: // CALL NC,nn
		addr := c.fetch16()
		if (c.F & FLAG_C) == 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xDC: // CALL C,nn
		addr := c.fetch16()
		if (c.F & FLAG_C) != 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xE4: // CALL PO,nn
		addr := c.fetch16()
		if (c.F & FLAG_PV) == 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xEC: // CALL PE,nn
		addr := c.fetch16()
		if (c.F & FLAG_PV) != 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xF4: // CALL P,nn
		addr := c.fetch16()
		if (c.F & FLAG_S) == 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xFC: // CALL M,nn
		addr := c.fetch16()
		if (c.F & FLAG_S) != 0 {
			c.push(c.PC)
			c.PC = addr
			c.tstates += 17
		} else {
			c.tstates += 10
		}
	case 0xC0: // RET NZ
		if (c.F & FLAG_Z) == 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}
	case 0xC8: // RET Z
		if (c.F & FLAG_Z) != 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}
	case 0xD0: // RET NC
		if (c.F & FLAG_C) == 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}
	case 0xD8: // RET C
		if (c.F & FLAG_C) != 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}
	case 0xE0: // RET PO
		if (c.F & FLAG_PV) == 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}
	case 0xE8: // RET PE
		if (c.F & FLAG_PV) != 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}
	case 0xF0: // RET P
		if (c.F & FLAG_S) == 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}
	case 0xF8: // RET M
		if (c.F & FLAG_S) != 0 {
			c.PC = c.pop()
			c.tstates += 11
		} else {
			c.tstates += 5
		}

	// Stack operations
	case 0xC1: // POP BC
		c.setBC(c.pop()); c.tstates += 10
	case 0xC5: // PUSH BC
		c.push(c.bc()); c.tstates += 11
	case 0xD1: // POP DE
		c.setDE(c.pop()); c.tstates += 10
	case 0xD5: // PUSH DE
		c.push(c.de()); c.tstates += 11
	case 0xE1: // POP HL
		c.setHL(c.pop()); c.tstates += 10
	case 0xE5: // PUSH HL
		c.push(c.hl()); c.tstates += 11
	case 0xF1: // POP AF
		c.setAF(c.pop()); c.tstates += 10
	case 0xF5: // PUSH AF
		c.push(c.af()); c.tstates += 11

	// Miscellaneous
	case 0x00: // NOP
		c.tstates += 4
	case 0x08: // EX AF,AF'
		c.A, c.A_ = c.A_, c.A
		c.F, c.F_ = c.F_, c.F
		c.tstates += 4
	case 0x10: // DJNZ n
		c.B--
		offset := int8(c.readOperand())
		if c.B != 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.tstates += 13
		} else {
			c.tstates += 8
		}
	case 0x27: // DAA
		c.daa(); c.tstates += 4
	case 0x2F: // CPL
		c.A = ^c.A
		c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV | FLAG_C)) | (c.A & (FLAG_F5 | FLAG_F3)) | FLAG_H | FLAG_N
		c.tstates += 4
	case 0x37: // SCF
		c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & (FLAG_F5 | FLAG_F3)) | FLAG_C
		c.tstates += 4
	case 0x3F: // CCF
		c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV | FLAG_C)) ^ FLAG_C
		if (c.F & FLAG_C) != 0 {
			c.F |= FLAG_H
		}
		c.F |= c.A & (FLAG_F5 | FLAG_F3)
		c.tstates += 4

	// Interrupts
	case 0xF3: // DI
		c.IFF1, c.IFF2 = false, false; c.tstates += 4
	case 0xFB: // EI
		c.IFF1, c.IFF2 = true, true; c.tstates += 4

	// I/O
	case 0xD3: // OUT (n),A
		port := uint16(c.readOperand()) | (uint16(c.A) << 8)
		c.mem.ContendPort(port)
		c.ula.WritePort(port, c.A)
		c.tstates += 11
	case 0xDB: // IN A,(n)
		port := uint16(c.readOperand()) | (uint16(c.A) << 8)
		c.mem.ContendPort(port)
		val, _ := c.ula.ReadPort(port)
		c.A = val
		c.tstates += 11

	// Exchange
	case 0xD9: // EXX
		c.B, c.B_ = c.B_, c.B
		c.C, c.C_ = c.C_, c.C
		c.D, c.D_ = c.D_, c.D
		c.E, c.E_ = c.E_, c.E
		c.H, c.H_ = c.H_, c.H
		c.L, c.L_ = c.L_, c.L
		c.tstates += 4
	case 0xE3: // EX (SP),HL
		temp := c.mem.Read(c.SP)
		c.mem.Write(c.SP, c.L)
		c.L = temp
		temp = c.mem.Read(c.SP + 1)
		c.mem.Write(c.SP+1, c.H)
		c.H = temp
		c.tstates += 19
	case 0xEB: // EX DE,HL
		c.D, c.H = c.H, c.D
		c.E, c.L = c.L, c.E
		c.tstates += 4
	case 0xF9: // LD SP,HL
		c.SP = c.hl(); c.tstates += 6

	// RST instructions
	case 0xC7: // RST 0x00
		c.push(c.PC); c.PC = 0x00; c.tstates += 11
	case 0xCF: // RST 0x08
		c.push(c.PC); c.PC = 0x08; c.tstates += 11
	case 0xD7: // RST 0x10
		c.push(c.PC); c.PC = 0x10; c.tstates += 11
	case 0xDF: // RST 0x18
		c.push(c.PC); c.PC = 0x18; c.tstates += 11
	case 0xE7: // RST 0x20
		c.push(c.PC); c.PC = 0x20; c.tstates += 11
	case 0xEF: // RST 0x28
		c.push(c.PC); c.PC = 0x28; c.tstates += 11
	case 0xF7: // RST 0x30
		c.push(c.PC); c.PC = 0x30; c.tstates += 11
	case 0xFF: // RST 0x38
		c.push(c.PC); c.PC = 0x38; c.tstates += 11

	// Extended instruction sets
	case 0xCB: // CB prefix
		c.executeCBInstruction(c.fetch())
	case 0xED: // ED prefix
		c.executeEDInstruction(c.fetch())
	case 0xDD: // DD prefix (IX instructions)
		c.executeDDInstruction(c.fetch())
	case 0xFD: // FD prefix (IY instructions)
		c.executeFDInstruction(c.fetch())

	default:
		if !c.logEnabled {
			log.Printf("Unknown opcode: 0x%02X at PC: 0x%04X\n", opcode, c.PC-1)
			c.logEnabled = true
		}
		c.tstates += 4
	}
}

// DAA (Decimal Adjust Accumulator) implementation
func (c *CPU) daa() {
	a := c.A
	correction := byte(0)
	carry := false

	if (c.F&FLAG_H) != 0 || (a&0x0F) > 9 {
		correction = 0x06
	}

	if (c.F&FLAG_C) != 0 || a > 0x99 || (a > 0x8F && (a&0x0F) > 9) {
		correction |= 0x60
		carry = true
	}

	if (c.F & FLAG_N) != 0 {
		c.A = a - correction
	} else {
		c.A = a + correction
	}

	c.F = (c.F & FLAG_N) | c.sz53Table[c.A] | c.parityTable[c.A]
	if carry {
		c.F |= FLAG_C
	}
	if ((a ^ c.A) & FLAG_H) != 0 {
		c.F |= FLAG_H
	}
}

// CB prefix instructions (bit manipulation, shifts, rotates)
func (c *CPU) executeCBInstruction(opcode byte) {
	switch opcode {
	// Rotate left circular
	case 0x00: // RLC B
		c.B = c.rlc(c.B); c.tstates += 8
	case 0x01: // RLC C
		c.C = c.rlc(c.C); c.tstates += 8
	case 0x02: // RLC D
		c.D = c.rlc(c.D); c.tstates += 8
	case 0x03: // RLC E
		c.E = c.rlc(c.E); c.tstates += 8
	case 0x04: // RLC H
		c.H = c.rlc(c.H); c.tstates += 8
	case 0x05: // RLC L
		c.L = c.rlc(c.L); c.tstates += 8
	case 0x06: // RLC (HL)
		addr := c.hl()
		c.mem.Write(addr, c.rlc(c.mem.Read(addr))); c.tstates += 15
	case 0x07: // RLC A
		c.A = c.rlc(c.A); c.tstates += 8

	// Rotate right circular
	case 0x08: // RRC B
		c.B = c.rrc(c.B); c.tstates += 8
	case 0x09: // RRC C
		c.C = c.rrc(c.C); c.tstates += 8
	case 0x0A: // RRC D
		c.D = c.rrc(c.D); c.tstates += 8
	case 0x0B: // RRC E
		c.E = c.rrc(c.E); c.tstates += 8
	case 0x0C: // RRC H
		c.H = c.rrc(c.H); c.tstates += 8
	case 0x0D: // RRC L
		c.L = c.rrc(c.L); c.tstates += 8
	case 0x0E: // RRC (HL)
		addr := c.hl()
		c.mem.Write(addr, c.rrc(c.mem.Read(addr))); c.tstates += 15
	case 0x0F: // RRC A
		c.A = c.rrc(c.A); c.tstates += 8

	// Rotate left through carry
	case 0x10: // RL B
		c.B = c.rl(c.B); c.tstates += 8
	case 0x11: // RL C
		c.C = c.rl(c.C); c.tstates += 8
	case 0x12: // RL D
		c.D = c.rl(c.D); c.tstates += 8
	case 0x13: // RL E
		c.E = c.rl(c.E); c.tstates += 8
	case 0x14: // RL H
		c.H = c.rl(c.H); c.tstates += 8
	case 0x15: // RL L
		c.L = c.rl(c.L); c.tstates += 8
	case 0x16: // RL (HL)
		addr := c.hl()
		c.mem.Write(addr, c.rl(c.mem.Read(addr))); c.tstates += 15
	case 0x17: // RL A
		c.A = c.rl(c.A); c.tstates += 8

	// Rotate right through carry
	case 0x18: // RR B
		c.B = c.rr(c.B); c.tstates += 8
	case 0x19: // RR C
		c.C = c.rr(c.C); c.tstates += 8
	case 0x1A: // RR D
		c.D = c.rr(c.D); c.tstates += 8
	case 0x1B: // RR E
		c.E = c.rr(c.E); c.tstates += 8
	case 0x1C: // RR H
		c.H = c.rr(c.H); c.tstates += 8
	case 0x1D: // RR L
		c.L = c.rr(c.L); c.tstates += 8
	case 0x1E: // RR (HL)
		addr := c.hl()
		c.mem.Write(addr, c.rr(c.mem.Read(addr))); c.tstates += 15
	case 0x1F: // RR A
		c.A = c.rr(c.A); c.tstates += 8

	// Shift left arithmetic
	case 0x20: // SLA B
		c.B = c.sla(c.B); c.tstates += 8
	case 0x21: // SLA C
		c.C = c.sla(c.C); c.tstates += 8
	case 0x22: // SLA D
		c.D = c.sla(c.D); c.tstates += 8
	case 0x23: // SLA E
		c.E = c.sla(c.E); c.tstates += 8
	case 0x24: // SLA H
		c.H = c.sla(c.H); c.tstates += 8
	case 0x25: // SLA L
		c.L = c.sla(c.L); c.tstates += 8
	case 0x26: // SLA (HL)
		addr := c.hl()
		c.mem.Write(addr, c.sla(c.mem.Read(addr))); c.tstates += 15
	case 0x27: // SLA A
		c.A = c.sla(c.A); c.tstates += 8

	// Shift right arithmetic
	case 0x28: // SRA B
		c.B = c.sra(c.B); c.tstates += 8
	case 0x29: // SRA C
		c.C = c.sra(c.C); c.tstates += 8
	case 0x2A: // SRA D
		c.D = c.sra(c.D); c.tstates += 8
	case 0x2B: // SRA E
		c.E = c.sra(c.E); c.tstates += 8
	case 0x2C: // SRA H
		c.H = c.sra(c.H); c.tstates += 8
	case 0x2D: // SRA L
		c.L = c.sra(c.L); c.tstates += 8
	case 0x2E: // SRA (HL)
		addr := c.hl()
		c.mem.Write(addr, c.sra(c.mem.Read(addr))); c.tstates += 15
	case 0x2F: // SRA A
		c.A = c.sra(c.A); c.tstates += 8

	// Shift left logical (undocumented)
	case 0x30: // SLL B
		c.B = c.sll(c.B); c.tstates += 8
	case 0x31: // SLL C
		c.C = c.sll(c.C); c.tstates += 8
	case 0x32: // SLL D
		c.D = c.sll(c.D); c.tstates += 8
	case 0x33: // SLL E
		c.E = c.sll(c.E); c.tstates += 8
	case 0x34: // SLL H
		c.H = c.sll(c.H); c.tstates += 8
	case 0x35: // SLL L
		c.L = c.sll(c.L); c.tstates += 8
	case 0x36: // SLL (HL)
		addr := c.hl()
		c.mem.Write(addr, c.sll(c.mem.Read(addr))); c.tstates += 15
	case 0x37: // SLL A
		c.A = c.sll(c.A); c.tstates += 8

	// Shift right logical
	case 0x38: // SRL B
		c.B = c.srl(c.B); c.tstates += 8
	case 0x39: // SRL C
		c.C = c.srl(c.C); c.tstates += 8
	case 0x3A: // SRL D
		c.D = c.srl(c.D); c.tstates += 8
	case 0x3B: // SRL E
		c.E = c.srl(c.E); c.tstates += 8
	case 0x3C: // SRL H
		c.H = c.srl(c.H); c.tstates += 8
	case 0x3D: // SRL L
		c.L = c.srl(c.L); c.tstates += 8
	case 0x3E: // SRL (HL)
		addr := c.hl()
		c.mem.Write(addr, c.srl(c.mem.Read(addr))); c.tstates += 15
	case 0x3F: // SRL A
		c.A = c.srl(c.A); c.tstates += 8

	// Bit test operations (0x40-0x7F)
	default:
		if opcode >= 0x40 && opcode <= 0x7F {
			bit := int((opcode - 0x40) / 8)
			reg := int((opcode - 0x40) % 8)
			c.bit(bit, c.getRegister8(reg))
			if reg == 6 { // (HL)
				c.tstates += 12
			} else {
				c.tstates += 8
			}
		} else if opcode >= 0x80 && opcode <= 0xBF {
			// Reset bit operations (0x80-0xBF)
			bit := int((opcode - 0x80) / 8)
			reg := int((opcode - 0x80) % 8)
			c.setRegister8(reg, c.res(bit, c.getRegister8(reg)))
			if reg == 6 { // (HL)
				c.tstates += 15
			} else {
				c.tstates += 8
			}
		} else { // opcode >= 0xC0
			// Set bit operations (0xC0-0xFF)
			bit := int((opcode - 0xC0) / 8)
			reg := int((opcode - 0xC0) % 8)
			c.setRegister8(reg, c.set(bit, c.getRegister8(reg)))
			if reg == 6 { // (HL)
				c.tstates += 15
			} else {
				c.tstates += 8
			}
		}
	}
}

func (c *CPU) executeEDInstruction(opcode byte) {
	switch opcode {
	// 16-bit loads
	case 0x43: // LD (nn),BC
		addr := c.fetch16()
		c.mem.Write(addr, c.C)
		c.mem.Write(addr+1, c.B)
		c.tstates += 20
	case 0x53: // LD (nn),DE
		addr := c.fetch16()
		c.mem.Write(addr, c.E)
		c.mem.Write(addr+1, c.D)
		c.tstates += 20
	case 0x4B: // LD BC,(nn)
		addr := c.fetch16()
		c.C = c.mem.Read(addr)
		c.B = c.mem.Read(addr + 1)
		c.tstates += 20
	case 0x5B: // LD DE,(nn)
		addr := c.fetch16()
		c.E = c.mem.Read(addr)
		c.D = c.mem.Read(addr + 1)
		c.tstates += 20
	case 0x63: // LD (nn),HL
		addr := c.fetch16()
		c.mem.Write(addr, c.L)
		c.mem.Write(addr+1, c.H)
		c.tstates += 20
	case 0x6B: // LD HL,(nn)
		addr := c.fetch16()
		c.L = c.mem.Read(addr)
		c.H = c.mem.Read(addr + 1)
		c.tstates += 20

	// ALU operations with carry/borrow
	case 0x42: // SBC HL,BC
		c.sbc16(c.hl(), c.bc())
		c.tstates += 15
	case 0x52: // SBC HL,DE
		c.sbc16(c.hl(), c.de())
		c.tstates += 15
	case 0x62: // SBC HL,HL
		c.sbc16(c.hl(), c.hl())
		c.tstates += 15
	case 0x4A: // ADC HL,BC
		c.adc16(c.hl(), c.bc())
		c.tstates += 15
	case 0x5A: // ADC HL,DE
		c.adc16(c.hl(), c.de())
		c.tstates += 15
	case 0x6A: // ADC HL,HL
		c.adc16(c.hl(), c.hl())
		c.tstates += 15
	case 0x7A: // ADC HL,SP
		c.adc16(c.hl(), c.SP)
		c.tstates += 15
	case 0x72: // SBC HL,SP
		c.sbc16(c.hl(), c.SP)
		c.tstates += 15
	case 0x73: // LD (nn),SP
		addr := c.fetch16()
		c.mem.Write(addr, byte(c.SP))
		c.mem.Write(addr+1, byte(c.SP>>8))
		c.tstates += 20
	case 0x7B: // LD SP,(nn)
		addr := c.fetch16()
		low := c.mem.Read(addr)
		high := c.mem.Read(addr + 1)
		c.SP = uint16(high)<<8 | uint16(low)
		c.tstates += 20

	// Block operations
	case 0xB0: // LDIR - Load, increment and repeat
		c.ldir()
		c.tstates += 16
	case 0xB8: // LDDR - Load, decrement and repeat  
		c.lddr()
		c.tstates += 16
	case 0xA0: // LDI - Load and increment
		c.ldi()
		c.tstates += 16
	case 0xA8: // LDD - Load and decrement
		c.ldd()
		c.tstates += 16
	case 0xA1: // CPI - Compare and increment
		c.cpi()
		c.tstates += 16
	case 0xA9: // CPD - Compare and decrement
		c.cpd()
		c.tstates += 16
	case 0xB1: // CPIR - Compare, increment and repeat
		c.cpir()
		c.tstates += 16
	case 0xB9: // CPDR - Compare, decrement and repeat
		c.cpdr()
		c.tstates += 16
	case 0xA3: // OUTI - Output and increment
		c.outi()
		c.tstates += 16
	case 0xAB: // OUTD - Output and decrement
		c.outd()
		c.tstates += 16
	case 0xB3: // OTIR - Output, increment and repeat
		c.otir()
		c.tstates += 16
	case 0xBB: // OTDR - Output, decrement and repeat
		c.otdr()
		c.tstates += 16
	case 0xA2: // INI - Input and increment
		c.ini()
		c.tstates += 16
	case 0xAA: // IND - Input and decrement
		c.ind()
		c.tstates += 16
	case 0xB2: // INIR - Input, increment and repeat
		c.inir()
		c.tstates += 16
	case 0xBA: // INDR - Input, decrement and repeat
		c.indr()
		c.tstates += 16
	case 0x67: // RRD - Rotate right decimal
		c.rrd()
		c.tstates += 18
	case 0x6F: // RLD - Rotate left decimal
		c.rld()
		c.tstates += 18

	// NOP instructions (undocumented but present in hardware)
	case 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C: // NOPs
		c.tstates += 8

	// Arithmetic operations
	case 0x44: // NEG - Negate accumulator (two's complement)
		orig := c.A
		c.F = 0
		if orig == 0x80 {
			c.F |= FLAG_PV // Overflow if A was 0x80
		}
		if orig != 0x00 {
			c.F |= FLAG_C // Carry if A was not zero
		}
		c.A = -orig // Two's complement negation
		c.F |= c.sz53Table[c.A]
		if (orig & 0x0F) != 0 {
			c.F |= FLAG_H // Half-carry if lower nibble of original was non-zero
		}
		c.F |= FLAG_N // Subtraction flag is always set for NEG
		c.tstates += 8

	// Interrupt mode and register operations
	case 0x47: // LD I,A
		c.I = c.A
		c.tstates += 9
	case 0x4F: // LD R,A
		c.R = c.A
		c.tstates += 9
	case 0x57: // LD A,I
		c.A = c.I
		c.F = (c.F & FLAG_C) | c.sz53Table[c.A]
		if c.IFF2 {
			c.F |= FLAG_PV
		}
		c.tstates += 9
	case 0x5F: // LD A,R
		c.A = c.R
		c.F = (c.F & FLAG_C) | c.sz53Table[c.A]
		if c.IFF2 {
			c.F |= FLAG_PV
		}
		c.tstates += 9

	// Interrupt modes
	case 0x46: // IM 0
		c.IM = 0
		c.tstates += 8
	case 0x56: // IM 1
		c.IM = 1
		c.tstates += 8
	case 0x5E: // IM 2
		c.IM = 2
		c.tstates += 8

	// I/O operations
	case 0x40: // IN B,(C)
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.B = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.B] | c.parityTable[c.B]
		c.tstates += 12
	case 0x48: // IN C,(C)
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.C = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.C] | c.parityTable[c.C]
		c.tstates += 12
	case 0x50: // IN D,(C)
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.D = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.D] | c.parityTable[c.D]
		c.tstates += 12
	case 0x58: // IN E,(C)
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.E = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.E] | c.parityTable[c.E]
		c.tstates += 12
	case 0x60: // IN H,(C)
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.H = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.H] | c.parityTable[c.H]
		c.tstates += 12
	case 0x68: // IN L,(C)
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.L = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.L] | c.parityTable[c.L]
		c.tstates += 12
	case 0x78: // IN A,(C)
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.A = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.A] | c.parityTable[c.A]
		c.tstates += 12
	case 0x70: // IN F,(C) - special case, only affects flags
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.F = (c.F & FLAG_C) | c.sz53Table[val] | c.parityTable[val]
		c.tstates += 12

	// Output instructions
	case 0x41: // OUT (C), B
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.B)
		c.tstates += 12
	case 0x49: // OUT (C), C
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.C)
		c.tstates += 12
	case 0x51: // OUT (C), D
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.D)
		c.tstates += 12
	case 0x59: // OUT (C), E
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.E)
		c.tstates += 12
	case 0x61: // OUT (C), H
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.H)
		c.tstates += 12
	case 0x69: // OUT (C), L
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.L)
		c.tstates += 12
	case 0x71: // OUT (C), 0 - outputs zero
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), 0)
		c.tstates += 12
	case 0x79: // OUT (C), A
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.A)
		c.tstates += 12

	// Return from interrupt
	case 0x4D: // RETI
		c.PC = c.pop()
		c.IFF1 = c.IFF2
		c.tstates += 14
	case 0x45: // RETN
		c.PC = c.pop()
		c.IFF1 = c.IFF2
		c.tstates += 14

	default:
		log.Printf("ED instruction not implemented: 0x%02X\n", opcode)
		c.tstates += 8
	}
}

func (c *CPU) executeDDInstruction(opcode byte) {
	switch opcode {
	// ADD IX,rr instructions
	case 0x09: // ADD IX,BC
		c.addIX(c.bc())
	case 0x19: // ADD IX,DE  
		c.addIX(c.de())
	case 0x29: // ADD IX,IX
		c.addIX(c.IX)
	case 0x39: // ADD IX,SP
		c.addIX(c.SP)
		
	// LD IX,nn / LD (nn),IX / LD IX,(nn)
	case 0x21: // LD IX,nn
		c.IX = c.fetch16()
		c.tstates += 14
	case 0x22: // LD (nn),IX
		addr := c.fetch16()
		c.mem.Write(addr, byte(c.IX))
		c.mem.Write(addr+1, byte(c.IX>>8))
		c.tstates += 20
	case 0x2A: // LD IX,(nn)
		addr := c.fetch16()
		c.IX = uint16(c.mem.Read(addr)) | (uint16(c.mem.Read(addr+1)) << 8)
		c.tstates += 20
		
	// INC/DEC IX
	case 0x23: // INC IX
		c.IX++
		c.tstates += 10
	case 0x2B: // DEC IX
		c.IX--
		c.tstates += 10
		
	// INC/DEC (IX+d)
	case 0x34: // INC (IX+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IX) + int32(d))
		val := c.inc(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 23
	case 0x35: // DEC (IX+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IX) + int32(d))
		val := c.dec(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 23
		
	// LD (IX+d),n
	case 0x36: // LD (IX+d),n
		d := int8(c.readOperand())
		n := c.readOperand()
		addr := uint16(int32(c.IX) + int32(d))
		c.mem.Write(addr, n)
		c.tstates += 19
		
	// LD r,(IX+d) instructions - Load from (IX+d) to register
	case 0x46: // LD B,(IX+d)
		c.B = c.loadIXd()
	case 0x4E: // LD C,(IX+d)
		c.C = c.loadIXd()
	case 0x56: // LD D,(IX+d)
		c.D = c.loadIXd()
	case 0x5E: // LD E,(IX+d)
		c.E = c.loadIXd()
	case 0x66: // LD H,(IX+d)
		c.H = c.loadIXd()
	case 0x6E: // LD L,(IX+d)
		c.L = c.loadIXd()
	case 0x7E: // LD A,(IX+d)
		c.A = c.loadIXd()
		
	// LD (IX+d),r instructions - Store register to (IX+d)
	case 0x70: // LD (IX+d),B
		c.storeIXd(c.B)
	case 0x71: // LD (IX+d),C
		c.storeIXd(c.C)
	case 0x72: // LD (IX+d),D
		c.storeIXd(c.D)
	case 0x73: // LD (IX+d),E
		c.storeIXd(c.E)
	case 0x74: // LD (IX+d),H
		c.storeIXd(c.H)
	case 0x75: // LD (IX+d),L
		c.storeIXd(c.L)
	case 0x77: // LD (IX+d),A
		c.storeIXd(c.A)
		
	// Arithmetic/Logic operations with (IX+d)
	case 0x86: // ADD A,(IX+d)
		c.add(c.loadIXd())
		c.tstates += 4 // loadIXd already adds 15, need 4 more for total 19
	case 0x8E: // ADC A,(IX+d)
		c.adc(c.loadIXd())
		c.tstates += 4
	case 0x96: // SUB (IX+d)
		c.sub(c.loadIXd())
		c.tstates += 4
	case 0x9E: // SBC A,(IX+d)
		c.sbc(c.loadIXd())
		c.tstates += 4
	case 0xA6: // AND (IX+d)
		c.and(c.loadIXd())
		c.tstates += 4
	case 0xAE: // XOR (IX+d)
		c.xor(c.loadIXd())
		c.tstates += 4
	case 0xB6: // OR (IX+d)
		c.or(c.loadIXd())
		c.tstates += 4
	case 0xBE: // CP (IX+d)
		c.cp(c.loadIXd())
		c.tstates += 4
		
	// Stack operations
	case 0xE1: // POP IX
		c.IX = c.pop()
		c.tstates += 14
	case 0xE5: // PUSH IX
		c.push(c.IX)
		c.tstates += 15
		
	// Jump
	case 0xE9: // JP (IX)
		c.PC = c.IX
		c.tstates += 8
		
	// Exchange
	case 0xE3: // EX (SP),IX
		temp := c.IX
		c.IX = uint16(c.mem.Read(c.SP)) | (uint16(c.mem.Read(c.SP+1)) << 8)
		c.mem.Write(c.SP, byte(temp))
		c.mem.Write(c.SP+1, byte(temp>>8))
		c.tstates += 23
		
	// DD CB prefix (IX bit operations)
	case 0xCB: // DD CB prefix
		d := int8(c.readOperand()) // Displacement comes first
		opcode := c.fetch()  // Then the CB opcode
		addr := uint16(int32(c.IX) + int32(d))
		c.executeDDCBInstruction(opcode, addr)
		c.tstates += 8 // Base timing, individual instructions add more

	default:
		// On real Z80 hardware, a DD prefix followed by an opcode that
		// doesn't use IX simply executes the base opcode (the prefix is
		// ignored, acting only as a NOP-like delay already counted by the
		// fetch above).
		c.executeBaseInstruction(opcode)
	}
}

// Helper functions for IX operations
func (c *CPU) addIX(value uint16) {
	result := uint32(c.IX) + uint32(value)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) // Preserve S, Z, PV
	if result > 0xFFFF {
		c.F |= FLAG_C
	}
	if ((c.IX & 0x0FFF) + (value & 0x0FFF)) > 0x0FFF {
		c.F |= FLAG_H
	}
	c.IX = uint16(result)
	c.F |= byte(c.IX>>8) & (FLAG_F3 | FLAG_F5) // Set undocumented flags
	c.tstates += 15
}

func (c *CPU) loadIXd() byte {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IX) + int32(d))
	c.tstates += 15 // Base timing for IX+d operations
	return c.mem.Read(addr)
}

func (c *CPU) storeIXd(value byte) {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IX) + int32(d))
	c.mem.Write(addr, value)
	c.tstates += 19
}

// Helper functions for IY operations
func (c *CPU) addIY(value uint16) {
	result := uint32(c.IY) + uint32(value)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) // Preserve S, Z, PV
	if result > 0xFFFF {
		c.F |= FLAG_C
	}
	if ((c.IY & 0x0FFF) + (value & 0x0FFF)) > 0x0FFF {
		c.F |= FLAG_H
	}
	c.IY = uint16(result)
	c.F |= byte(c.IY>>8) & (FLAG_F3 | FLAG_F5) // Set undocumented flags
	c.tstates += 15
}

func (c *CPU) loadIYd() byte {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IY) + int32(d))
	c.tstates += 15 // Base timing for IY+d operations
	return c.mem.Read(addr)
}

func (c *CPU) storeIYd(value byte) {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IY) + int32(d))
	c.mem.Write(addr, value)
	c.tstates += 19
}

// executeDDCBInstruction handles DD CB prefixed instructions (IX bit operations)
func (c *CPU) executeDDCBInstruction(opcode byte, addr uint16) {
	switch opcode {
	// Rotate left circular
	case 0x06: // RLC (IX+d)
		val := c.rlc(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Rotate right circular
	case 0x0E: // RRC (IX+d)
		val := c.rrc(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Rotate left through carry
	case 0x16: // RL (IX+d)
		val := c.rl(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Rotate right through carry
	case 0x1E: // RR (IX+d)
		val := c.rr(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Shift left arithmetic
	case 0x26: // SLA (IX+d)
		val := c.sla(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Shift right arithmetic
	case 0x2E: // SRA (IX+d)
		val := c.sra(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Shift left logical (same as SLA)
	case 0x36: // SLL (IX+d) - undocumented
		val := c.sla(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Shift right logical
	case 0x3E: // SRL (IX+d)
		val := c.srl(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 15
	// Bit test instructions
	default:
		if opcode >= 0x40 && opcode <= 0x7F { // BIT n,(IX+d)
			bit := (opcode - 0x40) / 8
			val := c.mem.Read(addr)
			c.F = (c.F & FLAG_C) | FLAG_H | (val & (FLAG_F3 | FLAG_F5))
			if (val & (1 << bit)) == 0 {
				c.F |= FLAG_Z | FLAG_PV
			}
			if bit == 7 && (val&0x80) != 0 {
				c.F |= FLAG_S
			}
			c.tstates += 12
		} else if opcode >= 0x80 && opcode <= 0xBF { // RES n,(IX+d)
			bit := (opcode - 0x80) / 8
			val := c.mem.Read(addr) & ^(1 << bit)
			c.mem.Write(addr, val)
			c.tstates += 15
		} else { // opcode >= 0xC0: SET n,(IX+d)
			bit := (opcode - 0xC0) / 8
			val := c.mem.Read(addr) | (1 << bit)
			c.mem.Write(addr, val)
			c.tstates += 15
		}
	}
}

func (c *CPU) executeFDInstruction(opcode byte) {
	switch opcode {
	// ADD IY,rr instructions
	case 0x09: // ADD IY,BC
		c.addIY(c.bc())
	case 0x19: // ADD IY,DE  
		c.addIY(c.de())
	case 0x29: // ADD IY,IY
		c.addIY(c.IY)
	case 0x39: // ADD IY,SP
		c.addIY(c.SP)
		
	// LD IY,nn / LD (nn),IY / LD IY,(nn)
	case 0x21: // LD IY,nn
		c.IY = c.fetch16()
		c.tstates += 14
	case 0x22: // LD (nn),IY
		addr := c.fetch16()
		c.mem.Write(addr, byte(c.IY))
		c.mem.Write(addr+1, byte(c.IY>>8))
		c.tstates += 20
	case 0x2A: // LD IY,(nn)
		addr := c.fetch16()
		c.IY = uint16(c.mem.Read(addr)) | (uint16(c.mem.Read(addr+1)) << 8)
		c.tstates += 20
		
	// INC/DEC IY
	case 0x23: // INC IY
		c.IY++
		c.tstates += 10
	case 0x2B: // DEC IY
		c.IY--
		c.tstates += 10
		
	// INC/DEC (IY+d)
	case 0x34: // INC (IY+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IY) + int32(d))
		val := c.inc(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 23
	case 0x35: // DEC (IY+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IY) + int32(d))
		val := c.dec(c.mem.Read(addr))
		c.mem.Write(addr, val)
		c.tstates += 23
		
	// LD (IY+d),n
	case 0x36: // LD (IY+d),n
		d := int8(c.readOperand())
		n := c.readOperand()
		addr := uint16(int32(c.IY) + int32(d))
		c.mem.Write(addr, n)
		c.tstates += 19
		
	// LD r,(IY+d) instructions - Load from (IY+d) to register
	case 0x46: // LD B,(IY+d)
		c.B = c.loadIYd()
	case 0x4E: // LD C,(IY+d)
		c.C = c.loadIYd()
	case 0x56: // LD D,(IY+d)
		c.D = c.loadIYd()
	case 0x5E: // LD E,(IY+d)
		c.E = c.loadIYd()
	case 0x66: // LD H,(IY+d)
		c.H = c.loadIYd()
	case 0x6E: // LD L,(IY+d)
		c.L = c.loadIYd()
	case 0x7E: // LD A,(IY+d)
		c.A = c.loadIYd()
		
	// LD (IY+d),r instructions - Store register to (IY+d)
	case 0x70: // LD (IY+d),B
		c.storeIYd(c.B)
	case 0x71: // LD (IY+d),C
		c.storeIYd(c.C)
	case 0x72: // LD (IY+d),D
		c.storeIYd(c.D)
	case 0x73: // LD (IY+d),E
		c.storeIYd(c.E)
	case 0x74: // LD (IY+d),H
		c.storeIYd(c.H)
	case 0x75: // LD (IY+d),L
		c.storeIYd(c.L)
	case 0x77: // LD (IY+d),A
		c.storeIYd(c.A)
		
	// Arithmetic/Logic operations with (IY+d)
	case 0x86: // ADD A,(IY+d)
		c.add(c.loadIYd())
		c.tstates += 4 // loadIYd already adds 15, need 4 more for total 19
	case 0x8E: // ADC A,(IY+d)
		c.adc(c.loadIYd())
		c.tstates += 4
	case 0x96: // SUB (IY+d)
		c.sub(c.loadIYd())
		c.tstates += 4
	case 0x9E: // SBC A,(IY+d)
		c.sbc(c.loadIYd())
		c.tstates += 4
	case 0xA6: // AND (IY+d)
		c.and(c.loadIYd())
		c.tstates += 4
	case 0xAE: // XOR (IY+d)
		c.xor(c.loadIYd())
		c.tstates += 4
	case 0xB6: // OR (IY+d)
		c.or(c.loadIYd())
		c.tstates += 4
	case 0xBE: // CP (IY+d)
		c.cp(c.loadIYd())
		c.tstates += 4
		
	// Stack operations
	case 0xE1: // POP IY
		c.IY = c.pop()
		c.tstates += 14
	case 0xE5: // PUSH IY
		c.push(c.IY)
		c.tstates += 15
		
	// Jump
	case 0xE9: // JP (IY)
		c.PC = c.IY
		c.tstates += 8
		
	// Exchange
	case 0xE3: // EX (SP),IY
		temp := c.IY
		c.IY = uint16(c.mem.Read(c.SP)) | (uint16(c.mem.Read(c.SP+1)) << 8)
		c.mem.Write(c.SP, byte(temp))
		c.mem.Write(c.SP+1, byte(temp>>8))
		c.tstates += 23
		
	// Special
	case 0xF9: // LD SP,IY
		c.SP = c.IY
		c.tstates += 10
		
	// FD CB prefix (IY bit operations)
	case 0xCB: // FD CB prefix
		d := int8(c.readOperand()) // Displacement comes first
		opcode := c.fetch()  // Then the CB opcode
		c.executeFDCBInstruction(opcode, d)
		c.tstates += 8 // Base timing, individual instructions add more

	default:
		// On real Z80 hardware, an FD prefix followed by an opcode that
		// doesn't use IY simply executes the base opcode (the prefix is
		// ignored, acting only as a NOP-like delay already counted by the
		// fetch above).
		c.executeBaseInstruction(opcode)
	}
}

func (c *CPU) executeFDCBInstruction(opcode byte, d int8) {
	// FD CB prefix instructions - IY indexed bit operations
	addr := uint16(int32(c.IY) + int32(d))
	val := c.mem.Read(addr)
	
	bit := int((opcode >> 3) & 7)
	reg := int(opcode & 7)
	
	switch opcode >> 6 {
	case 0: // Rotate/shift operations
		switch (opcode >> 3) & 7 {
		case 0: val = c.rlc(val)  // RLC (IY+d)
		case 1: val = c.rrc(val)  // RRC (IY+d)
		case 2: val = c.rl(val)   // RL (IY+d)
		case 3: val = c.rr(val)   // RR (IY+d)
		case 4: val = c.sla(val)  // SLA (IY+d)
		case 5: val = c.sra(val)  // SRA (IY+d)
		case 6: val = c.sll(val)  // SLL (IY+d)
		case 7: val = c.srl(val)  // SRL (IY+d)
		}
		c.mem.Write(addr, val)
		if reg != 6 { // Copy to register if not (HL)
			c.setRegister8(reg, val)
		}
	case 1: // BIT operations
		c.bit(bit, val)
	case 2: // RES operations
		val &= ^(1 << bit)
		c.mem.Write(addr, val)
		if reg != 6 {
			c.setRegister8(reg, val)
		}
	case 3: // SET operations
		val |= (1 << bit)
		c.mem.Write(addr, val)
		if reg != 6 {
			c.setRegister8(reg, val)
		}
	}
	c.tstates += 23
}

// fetch reads the next byte at PC and increments R (opcode fetch only).
func (c *CPU) fetch() byte {
	val := c.mem.Read(c.PC)
	c.PC++
	c.R = (c.R & 0x80) | ((c.R + 1) & 0x7f)
	return val
}

// readOperand reads the next byte at PC without incrementing R (for immediate operands).
func (c *CPU) readOperand() byte {
	val := c.mem.Read(c.PC)
	c.PC++
	return val
}

// readOperand16 reads 2 bytes (little-endian) without incrementing R.
func (c *CPU) readOperand16() uint16 {
	lo := uint16(c.readOperand())
	hi := uint16(c.readOperand())
	return (hi << 8) | lo
}

func (c *CPU) fetch16() uint16 {
	return c.readOperand16()
}

// Helper functions for register pairs
func (c *CPU) af() uint16 { return (uint16(c.A) << 8) | uint16(c.F) }
func (c *CPU) bc() uint16 { return (uint16(c.B) << 8) | uint16(c.C) }
func (c *CPU) de() uint16 { return (uint16(c.D) << 8) | uint16(c.E) }
func (c *CPU) hl() uint16 { return (uint16(c.H) << 8) | uint16(c.L) }

func (c *CPU) setAF(val uint16) { c.A = byte(val >> 8); c.F = byte(val) }
func (c *CPU) setBC(val uint16) { c.B = byte(val >> 8); c.C = byte(val) }
func (c *CPU) setDE(val uint16) { c.D = byte(val >> 8); c.E = byte(val) }
func (c *CPU) setHL(val uint16) { c.H = byte(val >> 8); c.L = byte(val) }

// initTables initializes the lookup tables for flag calculation
func (c *CPU) initTables() {
	// Initialize sz53Table: Sign, Zero, and undocumented flags (bits 3 and 5)
	for i := 0; i < 256; i++ {
		val := byte(i)
		flags := val & (FLAG_S | FLAG_F5 | FLAG_F3) // Copy S, F5, F3
		if val == 0 {
			flags |= FLAG_Z // Set Zero flag
		}
		c.sz53Table[i] = flags
	}

	// Initialize parity table
	for i := 0; i < 256; i++ {
		parity := 0
		val := i
		for val != 0 {
			parity ^= 1
			val &= val - 1 // Clear lowest set bit
		}
		if parity == 0 {
			c.parityTable[i] = FLAG_PV
		} else {
			c.parityTable[i] = 0
		}
	}

	// Initialize halfcarry tables for ADD operations
	c.halfcarryAddTable = [8]byte{0, 0, FLAG_H, FLAG_H, 0, 0, FLAG_H, FLAG_H}
	c.halfcarrySubTable = [8]byte{0, FLAG_H, FLAG_H, FLAG_H, 0, 0, 0, FLAG_H}

	// Initialize overflow tables for ADD operations
	c.overflowAddTable = [8]byte{0, 0, 0, FLAG_PV, FLAG_PV, 0, 0, 0}
	c.overflowSubTable = [8]byte{0, FLAG_PV, 0, 0, 0, 0, FLAG_PV, 0}
}

// Stack operations
func (c *CPU) push(val uint16) {
	c.SP--
	c.mem.Write(c.SP, byte(val>>8))
	c.SP--
	c.mem.Write(c.SP, byte(val))
}

func (c *CPU) pop() uint16 {
	lo := uint16(c.mem.Read(c.SP))
	c.SP++
	hi := uint16(c.mem.Read(c.SP))
	c.SP++
	return (hi << 8) | lo
}

// Inc/Dec operations with proper flag handling
func (c *CPU) inc(val byte) byte {
	result := val + 1
	c.F = (c.F & FLAG_C) | c.sz53Table[result]
	if result == 0x80 {
		c.F |= FLAG_PV // Set overflow if went from 0x7F to 0x80
	}
	if (val & 0x0F) == 0x0F {
		c.F |= FLAG_H // Set half-carry if carry from bit 3
	}
	return result
}

func (c *CPU) dec(val byte) byte {
	result := val - 1
	c.F = (c.F & FLAG_C) | FLAG_N | c.sz53Table[result]
	if val == 0x80 {
		c.F |= FLAG_PV // Set overflow if went from 0x80 to 0x7F
	}
	if (val & 0x0F) == 0x00 {
		c.F |= FLAG_H // Set half-carry if borrow from bit 4
	}
	return result
}

// 16-bit arithmetic
func (c *CPU) add16(reg1, reg2 uint16) uint16 {
	result := uint32(reg1) + uint32(reg2)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | byte((result>>8)&(FLAG_F5|FLAG_F3))
	if result > 0xFFFF {
		c.F |= FLAG_C
	}
	if ((reg1 & 0x0FFF) + (reg2 & 0x0FFF)) > 0x0FFF {
		c.F |= FLAG_H
	}
	return uint16(result)
}

// Add operation with carry handling
func (c *CPU) add(val byte) {
	a := uint32(c.A)
	result := a + uint32(val)
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = 0
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarryAddTable[lookup&0x07] | c.overflowAddTable[lookup>>4] | c.sz53Table[c.A]
}

// Add with carry
func (c *CPU) adc(val byte) {
	a := uint32(c.A)
	carry := uint32(c.F & FLAG_C)
	result := a + uint32(val) + carry
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = 0
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarryAddTable[lookup&0x07] | c.overflowAddTable[lookup>>4] | c.sz53Table[c.A]
}

// Subtract operation
func (c *CPU) sub(val byte) {
	a := uint32(c.A)
	result := a - uint32(val)
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = FLAG_N
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarrySubTable[lookup&0x07] | c.overflowSubTable[lookup>>4] | c.sz53Table[c.A]
}

// Subtract with carry (borrow)
func (c *CPU) sbc(val byte) {
	a := uint32(c.A)
	carry := uint32(c.F & FLAG_C)
	result := a - uint32(val) - carry
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = FLAG_N
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarrySubTable[lookup&0x07] | c.overflowSubTable[lookup>>4] | c.sz53Table[c.A]
}

// Compare operation
func (c *CPU) cp(val byte) {
	a := uint32(c.A)
	result := a - uint32(val)
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.F = FLAG_N | (val & (FLAG_F5 | FLAG_F3)) // Keep original value's F5, F3
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarrySubTable[lookup&0x07] | c.overflowSubTable[lookup>>4]
	if byte(result) == 0 {
		c.F |= FLAG_Z
	}
	if (result & 0x80) != 0 {
		c.F |= FLAG_S
	}
}

// Logical operations
func (c *CPU) and(val byte) {
	c.A &= val
	c.F = FLAG_H | c.sz53Table[c.A] | c.parityTable[c.A]
}

func (c *CPU) or(val byte) {
	c.A |= val
	c.F = c.sz53Table[c.A] | c.parityTable[c.A]
}

func (c *CPU) xor(val byte) {
	c.A ^= val
	c.F = c.sz53Table[c.A] | c.parityTable[c.A]
}

// Rotate and shift operations
func (c *CPU) rlca() {
	c.A = (c.A << 1) | (c.A >> 7)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & (FLAG_F5 | FLAG_F3 | FLAG_C))
}

func (c *CPU) rrca() {
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & FLAG_C)
	c.A = (c.A >> 1) | (c.A << 7)
	c.F |= c.A & (FLAG_F5 | FLAG_F3)
}

func (c *CPU) rla() {
	carry := c.F & FLAG_C
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | ((c.A >> 7) & FLAG_C)
	c.A = (c.A << 1) | carry
	c.F |= c.A & (FLAG_F5 | FLAG_F3)
}

func (c *CPU) rra() {
	carry := c.F & FLAG_C
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & FLAG_C)
	c.A = (c.A >> 1) | (carry << 7)
	c.F |= c.A & (FLAG_F5 | FLAG_F3)
}

// CB instruction helper methods
func (c *CPU) rlc(val byte) byte {
	result := (val << 1) | (val >> 7)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) rrc(val byte) byte {
	result := (val >> 1) | (val << 7)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) rl(val byte) byte {
	carry := c.F & FLAG_C
	result := (val << 1) | carry
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) rr(val byte) byte {
	carry := c.F & FLAG_C
	result := (val >> 1) | (carry << 7)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) sla(val byte) byte {
	result := val << 1
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) sra(val byte) byte {
	result := (val >> 1) | (val & 0x80)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) sll(val byte) byte {
	result := (val << 1) | 1
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) srl(val byte) byte {
	result := val >> 1
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) bit(bit int, val byte) {
	mask := byte(1 << bit)
	result := val & mask
	c.F = (c.F & FLAG_C) | FLAG_H | (val & (FLAG_F5 | FLAG_F3))
	if result == 0 {
		c.F |= FLAG_Z | FLAG_PV
	}
	if bit == 7 && result != 0 {
		c.F |= FLAG_S
	}
}

func (c *CPU) res(bit int, val byte) byte {
	return val & ^(1 << bit)
}

func (c *CPU) set(bit int, val byte) byte {
	return val | (1 << bit)
}

// Register access helpers for CB instructions
func (c *CPU) getRegister8(reg int) byte {
	switch reg {
	case 0: return c.B
	case 1: return c.C
	case 2: return c.D
	case 3: return c.E
	case 4: return c.H
	case 5: return c.L
	case 6: return c.mem.Read(c.hl()) // (HL)
	case 7: return c.A
	}
	return 0
}

func (c *CPU) setRegister8(reg int, val byte) {
	switch reg {
	case 0: c.B = val
	case 1: c.C = val
	case 2: c.D = val
	case 3: c.E = val
	case 4: c.H = val
	case 5: c.L = val
	case 6: c.mem.Write(c.hl(), val) // (HL)
	case 7: c.A = val
	}
}

// 16-bit subtract with carry
func (c *CPU) sbc16(hl, value uint16) {
	result := uint32(hl) - uint32(value)
	if (c.F & FLAG_C) != 0 {
		result--
	}
	
	c.F = FLAG_N
	if (result & 0x10000) != 0 {
		c.F |= FLAG_C
	}
	if ((hl ^ value ^ uint16(result)) & 0x1000) != 0 {
		c.F |= FLAG_H
	}
	if (hl ^ value) & (hl ^ uint16(result)) & 0x8000 != 0 {
		c.F |= FLAG_PV
	}
	if (result & 0x8000) != 0 {
		c.F |= FLAG_S
	}
	if uint16(result) == 0 {
		c.F |= FLAG_Z
	}
	c.F |= byte(result) & (FLAG_F5 | FLAG_F3)
	
	c.setHL(uint16(result))
}

func (c *CPU) adc16(hl, value uint16) {
	result := uint32(hl) + uint32(value)
	if (c.F & FLAG_C) != 0 {
		result++
	}
	
	c.F = 0
	if (result & 0x10000) != 0 {
		c.F |= FLAG_C
	}
	if ((hl ^ value ^ uint16(result)) & 0x1000) != 0 {
		c.F |= FLAG_H
	}
	if ^(hl ^ value) & (hl ^ uint16(result)) & 0x8000 != 0 {
		c.F |= FLAG_PV
	}
	if (result & 0x8000) != 0 {
		c.F |= FLAG_S
	}
	if uint16(result) == 0 {
		c.F |= FLAG_Z
	}
	c.F |= byte(result) & (FLAG_F5 | FLAG_F3)
	
	c.setHL(uint16(result))
}

// Block load operations
func (c *CPU) ldi() {
	// Load and increment
	val := c.mem.Read(c.hl())
	c.mem.Write(c.de(), val)
	c.setHL(c.hl() + 1)
	c.setDE(c.de() + 1)
	c.setBC(c.bc() - 1)
	
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_C))
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	val += c.A
	c.F |= val & FLAG_F3
	c.F |= ((val << 4) & FLAG_F5)
}

func (c *CPU) ldd() {
	// Load and decrement
	val := c.mem.Read(c.hl())
	c.mem.Write(c.de(), val)
	c.setHL(c.hl() - 1)
	c.setDE(c.de() - 1)
	c.setBC(c.bc() - 1)
	
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_C))
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	val += c.A
	c.F |= val & FLAG_F3
	c.F |= ((val << 4) & FLAG_F5)
}

func (c *CPU) ldir() {
	// Load, increment and repeat
	c.ldi()
	if c.bc() != 0 {
		c.PC -= 2 // Repeat the instruction
		c.tstates += 5 // Extra cycles for repeat
	}
}

func (c *CPU) lddr() {
	// Load, decrement and repeat
	c.ldd()
	if c.bc() != 0 {
		c.PC -= 2 // Repeat the instruction
		c.tstates += 5 // Extra cycles for repeat
	}
}

// Block search operations
func (c *CPU) cpi() {
	// Compare and increment
	val := c.mem.Read(c.hl())
	result := c.A - val
	
	c.setHL(c.hl() + 1)
	c.setBC(c.bc() - 1)
	
	c.F = (c.F & FLAG_C) | FLAG_N
	if result == 0 {
		c.F |= FLAG_Z
	}
	if (result & 0x80) != 0 {
		c.F |= FLAG_S
	}
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	if ((c.A ^ val ^ result) & 0x10) != 0 {
		c.F |= FLAG_H
	}
	
	// F3 and F5 flags are set from (A - (HL) - H flag)
	temp := result
	if (c.F & FLAG_H) != 0 {
		temp--
	}
	c.F |= temp & FLAG_F3
	c.F |= ((temp << 4) & FLAG_F5)
}

func (c *CPU) cpd() {
	// Compare and decrement
	val := c.mem.Read(c.hl())
	result := c.A - val
	
	c.setHL(c.hl() - 1)
	c.setBC(c.bc() - 1)
	
	c.F = (c.F & FLAG_C) | FLAG_N
	if result == 0 {
		c.F |= FLAG_Z
	}
	if (result & 0x80) != 0 {
		c.F |= FLAG_S
	}
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	if ((c.A ^ val ^ result) & 0x10) != 0 {
		c.F |= FLAG_H
	}
	
	// F3 and F5 flags are set from (A - (HL) - H flag)
	temp := result
	if (c.F & FLAG_H) != 0 {
		temp--
	}
	c.F |= temp & FLAG_F3
	c.F |= ((temp << 4) & FLAG_F5)
}

func (c *CPU) cpir() {
	// Compare, increment and repeat
	c.cpi()
	if c.bc() != 0 && (c.F & FLAG_Z) == 0 {
		c.PC -= 2 // Repeat the instruction
		c.tstates += 5 // Extra cycles for repeat
	}
}

func (c *CPU) cpdr() {
	// Compare, decrement and repeat
	c.cpd()
	if c.bc() != 0 && (c.F & FLAG_Z) == 0 {
		c.PC -= 2 // Repeat the instruction
		c.tstates += 5 // Extra cycles for repeat
	}
}

// Block output operations
func (c *CPU) outi() {
	// Output and increment
	val := c.mem.Read(c.hl())
	c.mem.ContendPort(c.bc())
	c.ula.WritePort(c.bc(), val)
	c.setHL(c.hl() + 1)
	c.B--
	
	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	if (c.B & 0x80) != 0 {
		c.F |= FLAG_S
	}
	c.F |= FLAG_N
	
	// Complex flag calculation for block I/O
	temp := uint16(val) + uint16(c.L)
	if temp > 255 {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[(byte(temp) & 7) ^ c.B]
}

func (c *CPU) outd() {
	// Output and decrement
	val := c.mem.Read(c.hl())
	c.mem.ContendPort(c.bc())
	c.ula.WritePort(c.bc(), val)
	c.setHL(c.hl() - 1)
	c.B--
	
	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	if (c.B & 0x80) != 0 {
		c.F |= FLAG_S
	}
	c.F |= FLAG_N
	
	// Complex flag calculation for block I/O
	temp := uint16(val) + uint16(c.L)
	if temp > 255 {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[(byte(temp) & 7) ^ c.B]
}

func (c *CPU) otir() {
	// Output, increment and repeat
	c.outi()
	if c.B != 0 {
		c.PC -= 2 // Repeat the instruction
		c.tstates += 5 // Extra cycles for repeat
	}
}

func (c *CPU) otdr() {
	// Output, decrement and repeat
	c.outd()
	if c.B != 0 {
		c.PC -= 2 // Repeat the instruction
		c.tstates += 5 // Extra cycles for repeat
	}
}

// Block input operations
func (c *CPU) ini() {
	// Input and increment
	c.mem.ContendPort(c.bc())
	val, _ := c.ula.ReadPort(c.bc())
	c.mem.Write(c.hl(), val)
	c.setHL(c.hl() + 1)
	c.B--

	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	if (c.B & 0x80) != 0 {
		c.F |= FLAG_S
	}
	c.F |= FLAG_N

	temp := uint16(val) + uint16(c.C) + 1
	if temp > 255 {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[(byte(temp)&7)^c.B]
}

func (c *CPU) ind() {
	// Input and decrement
	c.mem.ContendPort(c.bc())
	val, _ := c.ula.ReadPort(c.bc())
	c.mem.Write(c.hl(), val)
	c.setHL(c.hl() - 1)
	c.B--

	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	if (c.B & 0x80) != 0 {
		c.F |= FLAG_S
	}
	c.F |= FLAG_N

	temp := uint16(val) + uint16(c.C) - 1
	if temp > 255 {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[(byte(temp)&7)^c.B]
}

func (c *CPU) inir() {
	// Input, increment and repeat
	c.ini()
	if c.B != 0 {
		c.PC -= 2
		c.tstates += 5
	}
}

func (c *CPU) indr() {
	// Input, decrement and repeat
	c.ind()
	if c.B != 0 {
		c.PC -= 2
		c.tstates += 5
	}
}

// Rotate decimal operations
func (c *CPU) rrd() {
	// Rotate right decimal
	val := c.mem.Read(c.hl())
	temp := c.A
	c.A = (c.A & 0xF0) | (val & 0x0F)
	c.mem.Write(c.hl(), (val >> 4) | (temp << 4))
	
	c.F = (c.F & FLAG_C) | c.sz53Table[c.A] | c.parityTable[c.A]
}

func (c *CPU) rld() {
	// Rotate left decimal
	val := c.mem.Read(c.hl())
	temp := c.A
	c.A = (c.A & 0xF0) | (val >> 4)
	c.mem.Write(c.hl(), (val << 4) | (temp & 0x0F))
	
	c.F = (c.F & FLAG_C) | c.sz53Table[c.A] | c.parityTable[c.A]
}

// Interrupt handling
func (c *CPU) interrupt() {
	if !c.IFF1 {
		return
	}
	
	// Disable interrupts
	c.IFF1 = false
	c.IFF2 = false
	
	// Exit halt state if in it
	c.Halted = false
	
	switch c.IM {
	case 0:
		// IM0: Execute RST 38h (like RST instruction)
		c.push(c.PC)
		c.PC = 0x38
		c.tstates += 13
	case 1:
		// IM1: Execute RST 38h (same as IM0)
		c.push(c.PC)
		c.PC = 0x38
		c.tstates += 13
	case 2:
		// IM2: Indirect jump using I register and data bus value.
		// On the ZX Spectrum, the ULA places 0xFF on the bus during INTA.
		addr := uint16(c.I)<<8 | uint16(c.IM2Vector)
		low := c.mem.Read(addr)
		high := c.mem.Read(addr + 1)
		c.push(c.PC)
		c.PC = uint16(high)<<8 | uint16(low)
		c.tstates += 19
	}
}

// NMI (Non-Maskable Interrupt) handling - for Multiface red button
func (c *CPU) NMI() {
	// NMI cannot be disabled and always executes.
	//
	// Standard Z80 documentation says IFF2 = IFF1 before clearing IFF1,
	// but we deliberately leave IFF2 unchanged (matching FUSE's approach).
	// Rationale: the Multiface 3 ROM re-enables interrupts with EI and
	// never uses RETN, so it does not rely on IFF2 to restore IFF1.
	// If we copied IFF1 into IFF2 here, an NMI that fires while IFF1 is
	// false (e.g. inside a DI section) would set IFF2=false, and any
	// subsequent RETN in the interrupted program would restore IFF1=false
	// — permanently disabling maskable interrupts and crashing the +3
	// (which needs IM2 interrupts for keyboard scanning).
	c.IFF1 = false

	// Exit halt state if in it
	c.Halted = false

	// Bump the R register like a real Z80 M1 cycle
	c.R = (c.R & 0x80) | ((c.R + 1) & 0x7F)

	// NMI always jumps to 0x0066
	c.push(c.PC)
	c.PC = 0x0066
	c.tstates += 11
}