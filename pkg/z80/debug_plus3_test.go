package z80

import (
	"fmt"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// tracingULA wraps port I/O and logs every port write, with proper +3 decode.
type tracingULA struct {
	mem       *memory.Memory
	portLog   []portEvent
	portReads map[uint16]byte
}

type portEvent struct {
	addr uint16
	val  byte
	desc string
}

func newTracingULA(mem *memory.Memory) *tracingULA {
	return &tracingULA{
		mem:       mem,
		portReads: make(map[uint16]byte),
	}
}

func (u *tracingULA) ReadPort(addr uint16) (byte, bool) {
	if addr&0x01 == 0 { // Port 0xFE
		return 0xBF, true // No keys pressed
	}
	// FDC ports: return 0x80 (not ready) for status, 0xFF for data
	if addr&0xF002 == 0x2000 { // +3 FDC status port 0x2FFD
		return 0x80, true
	}
	if addr&0xF002 == 0x3000 { // +3 FDC data port 0x3FFD
		return 0xFF, true
	}
	return 0xFF, true
}

func (u *tracingULA) WritePort(addr uint16, val byte) {
	model := u.mem.GetCurrentModel()

	if addr&0x01 == 0 { // Port 0xFE
		u.portLog = append(u.portLog, portEvent{addr, val, "FE (border/speaker)"})
		return
	}

	if model == roms.ModelPlus3 || model == roms.ModelPlus2A {
		// +3/+2A: stricter port decoding
		if addr&0xC002 == 0x4000 { // Port 0x7FFD
			u.portLog = append(u.portLog, portEvent{addr, val,
				fmt.Sprintf("7FFD (RAM page=%d, screen=%d, ROM low=%d, lock=%v)",
					val&0x07, 5+2*((val>>3)&1), (val>>4)&1, val&0x20 != 0)})
			u.mem.PageMemory(val)
		} else if addr&0xF002 == 0x1000 { // Port 0x1FFD
			u.portLog = append(u.portLog, portEvent{addr, val,
				fmt.Sprintf("1FFD (special=%v, mode=%d, ROM high=%d, motor=%v)",
					val&0x01 != 0, (val>>1)&0x03, (val>>2)&1, val&0x08 != 0)})
			u.mem.PageMemoryPlus3(val)
		}
	} else if model != roms.Model48K {
		if addr&0x8002 == 0 { // Port 0x7FFD
			u.portLog = append(u.portLog, portEvent{addr, val, "7FFD (128K paging)"})
			u.mem.PageMemory(val)
		}
	}
}

// TestPlus3BootTrampoline boots the +3 ROM and traces execution until the
// trampoline at 0x5B00 is called, logging every CALL/RET/JP/JR/OUT and
// checking stack integrity.
func TestPlus3BootTrampoline(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	ula := newTracingULA(mem)
	cpu := New(mem, ula)

	// Trace control-flow instructions up to 2M instructions
	const maxInstructions = 2_000_000
	type flowEvent struct {
		pc     uint16
		opcode byte
		target uint16
		sp     uint16
		desc   string
	}
	var flowLog []flowEvent
	var trampolineHit bool
	var trampolineRetAddr uint16

	for i := 0; i < maxInstructions; i++ {
		pc := cpu.PC
		sp := cpu.SP

		// Check if we're at the trampoline entry point
		if pc == 0x5B00 {
			trampolineHit = true
			// Read the return address from the stack
			lo := mem.Read(sp)
			hi := mem.Read(sp + 1)
			trampolineRetAddr = uint16(hi)<<8 | uint16(lo)
			flowLog = append(flowLog, flowEvent{pc, 0, trampolineRetAddr, sp,
				fmt.Sprintf("TRAMPOLINE at 0x5B00, SP=0x%04X, return addr on stack=0x%04X, stack[SP+2..+3]=0x%02X%02X",
					sp, trampolineRetAddr, mem.Read(sp+3), mem.Read(sp+2))})
			break
		}

		// Peek at current opcode without advancing PC
		opcode := mem.Read(pc)

		// Execute the instruction
		cpu.executeInstruction()

		// Log control flow instructions
		switch opcode {
		case 0xC3: // JP nn
			flowLog = append(flowLog, flowEvent{pc, opcode, cpu.PC, sp,
				fmt.Sprintf("JP 0x%04X", cpu.PC)})
		case 0xCD: // CALL nn
			flowLog = append(flowLog, flowEvent{pc, opcode, cpu.PC, cpu.SP,
				fmt.Sprintf("CALL 0x%04X (SP now 0x%04X)", cpu.PC, cpu.SP)})
		case 0xC9: // RET
			flowLog = append(flowLog, flowEvent{pc, opcode, cpu.PC, cpu.SP,
				fmt.Sprintf("RET to 0x%04X (SP now 0x%04X)", cpu.PC, cpu.SP)})
		case 0x18: // JR e
			flowLog = append(flowLog, flowEvent{pc, opcode, cpu.PC, sp,
				fmt.Sprintf("JR 0x%04X", cpu.PC)})
		case 0xD3: // OUT (n),A
			port := uint16(mem.Read(pc+1)) | (uint16(cpu.A) << 8)
			flowLog = append(flowLog, flowEvent{pc, opcode, 0, sp,
				fmt.Sprintf("OUT (0x%04X),A [A=0x%02X]", port, cpu.A)})
		case 0xED: // ED prefix -- check for OUT (C),A
			op2 := mem.Read(pc + 1)
			if op2 == 0x79 { // OUT (C),A
				flowLog = append(flowLog, flowEvent{pc, opcode, 0, sp,
					fmt.Sprintf("OUT (C=0x%04X),A [A=0x%02X]", cpu.bc(), cpu.A)})
			}
		}
	}

	// Report port writes
	t.Logf("=== Port writes during +3 boot ===")
	for i, ev := range ula.portLog {
		if i < 50 { // Limit output
			t.Logf("  OUT 0x%04X <- 0x%02X  [%s]", ev.addr, ev.val, ev.desc)
		}
	}
	if len(ula.portLog) > 50 {
		t.Logf("  ... and %d more port writes", len(ula.portLog)-50)
	}

	// Report last 30 flow events leading up to the trampoline
	t.Logf("=== Flow log (last entries before trampoline) ===")
	start := 0
	if len(flowLog) > 30 {
		start = len(flowLog) - 30
	}
	for i := start; i < len(flowLog); i++ {
		ev := flowLog[i]
		t.Logf("  [%d] PC=0x%04X SP=0x%04X: %s", i, ev.pc, ev.sp, ev.desc)
	}

	if !trampolineHit {
		t.Logf("Trampoline at 0x5B00 not reached in %d instructions", maxInstructions)
		t.Logf("Final PC=0x%04X SP=0x%04X", cpu.PC, cpu.SP)
		// This is informational, not necessarily a failure (ROM might not use trampoline)
	} else {
		t.Logf("Trampoline reached. Return address on stack: 0x%04X", trampolineRetAddr)
	}
}

// TestPlus3PortDecodeIsolation verifies that writing to port 0x1FFD does NOT
// trigger the 0x7FFD handler. This was the root cause of the +3/+2A black screen.
func TestPlus3PortDecodeIsolation(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	ula := newTracingULA(mem)
	_ = New(mem, ula) // Initialize CPU (sets up contention)

	// Write marker bytes to RAM pages so we can detect which page is mapped
	for bank := 0; bank < 8; bank++ {
		page := mem.GetPage(bank)
		page[0] = byte(0xA0 + bank)
	}

	// Step 1: Set port 0x7FFD to select RAM page 4 in slot 3
	ula.WritePort(0x7FFD, 0x04) // bits 0-2 = 4 -> RAM page 4

	slot3val := mem.Read(0xC000)
	if slot3val != 0xA4 {
		t.Fatalf("After 7FFD write, slot 3 should be RAM page 4 (marker 0xA4), got 0x%02X", slot3val)
	}

	// Step 2: Write to port 0x1FFD with value 0x04 (bit 0=0 -> normal mode,
	// bit 2=1 -> ROM high bit set). This should NOT change the RAM page in slot 3.
	ula.WritePort(0x1FFD, 0x04)

	slot3valAfter := mem.Read(0xC000)
	if slot3valAfter != 0xA4 {
		t.Errorf("BUG: Writing to port 0x1FFD changed slot 3 from RAM page 4 (0xA4) to 0x%02X.\n"+
			"Port 0x1FFD should NOT trigger the 0x7FFD handler.", slot3valAfter)
	} else {
		t.Logf("PASS: Port 0x1FFD write did not corrupt slot 3 RAM page selection")
	}

	// Verify port log shows separate handling
	var has7FFD, has1FFD bool
	for _, ev := range ula.portLog {
		if strings.Contains(ev.desc, "7FFD") {
			has7FFD = true
		}
		if strings.Contains(ev.desc, "1FFD") {
			has1FFD = true
		}
	}
	if !has7FFD || !has1FFD {
		t.Errorf("Expected both 7FFD and 1FFD port events, got 7FFD=%v 1FFD=%v", has7FFD, has1FFD)
	}
}

// TestPlus3SpecialPagingSlot3Restore verifies that exiting special paging mode
// properly restores slot 3 from port 0x7FFD bits 0-2.
func TestPlus3SpecialPagingSlot3Restore(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	ula := newTracingULA(mem)
	_ = New(mem, ula)

	// Write marker bytes to RAM pages
	for bank := 0; bank < 8; bank++ {
		page := mem.GetPage(bank)
		page[0] = byte(0xB0 + bank)
	}

	// Set port 0x7FFD to select RAM page 3 in slot 3
	ula.WritePort(0x7FFD, 0x03)
	if got := mem.Read(0xC000); got != 0xB3 {
		t.Fatalf("Setup: slot 3 should be RAM page 3, got marker 0x%02X", got)
	}

	// Enter special paging mode 1 (banks 4,5,6,7)
	ula.WritePort(0x1FFD, 0x03)
	if got := mem.Read(0xC000); got != 0xB7 {
		t.Fatalf("Special mode 1: slot 3 should be RAM page 7, got marker 0x%02X", got)
	}

	// Exit special paging mode (bit 0 = 0)
	ula.WritePort(0x1FFD, 0x00)

	// Slot 3 should be restored to RAM page 3 (from port7FFD value)
	got := mem.Read(0xC000)
	if got != 0xB3 {
		t.Errorf("BUG: After exiting special paging, slot 3 should be RAM page 3 (marker 0xB3), got 0x%02X.\n"+
			"PageMemoryPlus3 must restore slot 3 from port7FFD bits 0-2.", got)
	} else {
		t.Logf("PASS: Slot 3 correctly restored to RAM page 3 after exiting special paging")
	}
}

// TestPlus3PageMemoryIgnoredInSpecialMode verifies that writing to port 0x7FFD
// while in special paging mode does NOT change the memory layout.
func TestPlus3PageMemoryIgnoredInSpecialMode(t *testing.T) {
	romPath := "../../roms"
	mem, err := memory.New(romPath, roms.ModelPlus3)
	if err != nil {
		t.Skipf("Skipping: cannot load +3 ROMs: %v", err)
	}

	ula := newTracingULA(mem)
	_ = New(mem, ula)

	// Write marker bytes
	for bank := 0; bank < 8; bank++ {
		page := mem.GetPage(bank)
		page[0] = byte(0xC0 + bank)
	}

	// Enter special paging mode 0 (banks 0,1,2,3)
	ula.WritePort(0x1FFD, 0x01)

	// Verify special mode layout
	if got := mem.Read(0xC000); got != 0xC3 {
		t.Fatalf("Special mode 0: slot 3 should be RAM page 3, got 0x%02X", got)
	}

	// Now write to 0x7FFD to select RAM page 6 -- this should NOT change slot 3
	// because we're in special paging mode
	ula.WritePort(0x7FFD, 0x06)

	got := mem.Read(0xC000)
	if got != 0xC3 {
		t.Errorf("BUG: Writing to 0x7FFD in special mode changed slot 3 to 0x%02X (should stay 0xC3).\n"+
			"PageMemory must not modify slot 3 while in special paging mode.", got)
	} else {
		t.Logf("PASS: 0x7FFD write in special mode did not alter slot 3")
	}

	// But when we exit special mode, the 0x7FFD=0x06 value should take effect
	ula.WritePort(0x1FFD, 0x00)
	got = mem.Read(0xC000)
	if got != 0xC6 {
		t.Errorf("After exiting special mode, slot 3 should reflect 7FFD page 6 (marker 0xC6), got 0x%02X", got)
	} else {
		t.Logf("PASS: After exiting special mode, slot 3 correctly reflects 7FFD=6")
	}
}
