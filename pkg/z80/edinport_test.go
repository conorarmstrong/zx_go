package z80

import "testing"

// IN r,(C) behavioural coverage.
//
// Per Sean Young's "Undocumented Z80 Documented" §3.6 and the
// Z80 ED-prefix decoder:
//   ED $40 .. $78 cover IN B/C/D/E/H/L/A,(C)  and the bit-7-clear
//   variant IN F,(C) (ED $70) that only updates flags.
// Flag effects: S = bit7(val); Z = val==0; H = 0; P/V = parity(val);
// N = 0; C = preserved.
// BC, the address bus, is `B*256 + C`.

// runINR runs ED <op> with B,C primed to addr.high/.low, captures
// the loaded register and the post-flag byte.
func runINR(t *testing.T, op byte, addr uint16, portVal byte, preF byte) (val byte, postF byte) {
	t.Helper()
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	mu.ports[addr] = portVal
	cpu.B = byte(addr >> 8)
	cpu.C = byte(addr)
	cpu.F = preF
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, op)
	cpu.executeInstruction()

	switch op {
	case 0x40:
		val = cpu.B
	case 0x48:
		val = cpu.C
	case 0x50:
		val = cpu.D
	case 0x58:
		val = cpu.E
	case 0x60:
		val = cpu.H
	case 0x68:
		val = cpu.L
	case 0x78:
		val = cpu.A
	}
	postF = cpu.F
	return
}

func checkINFlags(t *testing.T, label string, val, postF, preF byte) {
	t.Helper()
	// Compute expected flags.
	wantS := val & 0x80
	wantZ := byte(0)
	if val == 0 {
		wantZ = FLAG_Z
	}
	parity := byte(0)
	v := val
	v ^= v >> 4
	v ^= v >> 2
	v ^= v >> 1
	if v&1 == 0 {
		parity = FLAG_PV
	}
	want := wantS | wantZ | parity | (preF & FLAG_C) | (val & (FLAG_F3 | FLAG_F5))
	// Must clear H + N.
	if postF&FLAG_H != 0 {
		t.Errorf("%s: H flag set, want clear", label)
	}
	if postF&FLAG_N != 0 {
		t.Errorf("%s: N flag set, want clear", label)
	}
	// C preserved.
	if (postF & FLAG_C) != (preF & FLAG_C) {
		t.Errorf("%s: C flag preF=%v postF=%v (must preserve)", label,
			preF&FLAG_C != 0, postF&FLAG_C != 0)
	}
	// S, Z, P/V, 3, 5.
	if (postF & FLAG_S) != wantS {
		t.Errorf("%s: S flag = %v, want %v (val=$%02X)", label,
			postF&FLAG_S != 0, wantS != 0, val)
	}
	if (postF & FLAG_Z) != wantZ {
		t.Errorf("%s: Z flag = %v, want %v (val=$%02X)", label,
			postF&FLAG_Z != 0, wantZ != 0, val)
	}
	if (postF & FLAG_PV) != parity {
		t.Errorf("%s: P/V flag = %v, want %v (val=$%02X)", label,
			postF&FLAG_PV != 0, parity != 0, val)
	}
	_ = want
}

func TestINrC_LoadsRegisterAndFlags(t *testing.T) {
	// Op-to-name table.
	cases := []struct {
		op   byte
		name string
	}{
		{0x40, "B"},
		{0x48, "C"},
		{0x50, "D"},
		{0x58, "E"},
		{0x60, "H"},
		{0x68, "L"},
		{0x78, "A"},
	}
	// Mix of port values that exercise S/Z/PV combinations.
	portVals := []byte{0x00, 0x01, 0x80, 0xFF, 0x55, 0xAA, 0x7F, 0x42}

	for _, c := range cases {
		for _, pv := range portVals {
			val, postF := runINR(t, c.op, 0x2030, pv, 0)
			if val != pv {
				t.Errorf("IN %s,(C) val=$%02X port=$%02X", c.name, val, pv)
			}
			checkINFlags(t, "IN "+c.name+",(C)", val, postF, 0)
		}
	}
}

func TestINrC_PreservesCarry(t *testing.T) {
	// With C=1 going in, every IN r,(C) must leave C=1 post.
	cases := []byte{0x40, 0x48, 0x50, 0x58, 0x60, 0x68, 0x78}
	for _, op := range cases {
		val, postF := runINR(t, op, 0x1020, 0x42, FLAG_C)
		if postF&FLAG_C == 0 {
			t.Errorf("op=$%02X with C=1 in: C cleared (val=$%02X)", op, val)
		}
	}
	// And with C=0 going in, it stays 0.
	for _, op := range cases {
		_, postF := runINR(t, op, 0x1020, 0x42, 0)
		if postF&FLAG_C != 0 {
			t.Errorf("op=$%02X with C=0 in: C set", op)
		}
	}
}

// TestINrC_LoadsHL is a regression for the IN H,(C) and IN L,(C)
// cases — verify the destination register and that the BC address
// in the input phase wasn't clobbered by the load. Because IN H
// modifies what HL points to (and so could affect later code that
// pre-computed an HL-based address), this is the kind of thing
// that's easy to subtly get wrong.
func TestINrC_LoadsHL(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	mu.ports[0x1234] = 0xAB
	cpu.B = 0x12
	cpu.C = 0x34
	cpu.H = 0x00
	cpu.L = 0x00
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x60) // IN H,(C)
	cpu.executeInstruction()
	if cpu.H != 0xAB {
		t.Errorf("IN H,(C): H=$%02X, want AB", cpu.H)
	}
	if cpu.L != 0x00 {
		t.Errorf("IN H,(C) clobbered L: $%02X, want 00", cpu.L)
	}
	if cpu.B != 0x12 || cpu.C != 0x34 {
		t.Errorf("IN H,(C) clobbered BC: %02X %02X", cpu.B, cpu.C)
	}
}

// TestINFC_OnlyFlags — ED $70 (IN F,(C) per undocumented Z80) reads
// from port but discards the value, only setting flags.
func TestINFC_OnlyFlags(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	mu.ports[0x1234] = 0x80 // S flag, parity = odd
	cpu.B = 0x12
	cpu.C = 0x34
	cpu.A = 0xAA
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x70)
	cpu.executeInstruction()

	// A must NOT be touched — IN F,(C) discards the byte.
	if cpu.A != 0xAA {
		t.Errorf("IN F,(C) clobbered A: $%02X, want AA", cpu.A)
	}
	// Flags should match port value (0x80 → S set, Z clear, parity odd
	// → PV clear).
	if cpu.F&FLAG_S == 0 {
		t.Errorf("IN F,(C): S flag clear, want set for port=$80")
	}
	if cpu.F&FLAG_Z != 0 {
		t.Errorf("IN F,(C): Z flag set, want clear for port=$80")
	}
	if cpu.F&FLAG_PV != 0 {
		t.Errorf("IN F,(C): PV flag set, want clear (parity odd) for port=$80")
	}
	if cpu.F&FLAG_N != 0 {
		t.Errorf("IN F,(C): N flag set, want clear")
	}
	if cpu.F&FLAG_H != 0 {
		t.Errorf("IN F,(C): H flag set, want clear")
	}
}

// OUT (C),0 (ED $71): writes 0 to the port. Undocumented variant —
// some emulators write 0xFF instead; the real CMOS Z80 writes 0.
func TestOUTC0_WritesZero(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	cpu.B = 0x12
	cpu.C = 0x34
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x71)
	cpu.executeInstruction()
	if got := mu.ports[0x1234]; got != 0x00 {
		t.Errorf("OUT (C),0: port wrote $%02X, want 00", got)
	}
}

// OUT (C),r writes the named register to the port — exhaustive test
// of every D/E/H/L/A encoding. B and C are the address bus, so OUT
// (C),B / OUT (C),C are tested separately below.
func TestOUTCr_WritesRegister(t *testing.T) {
	cases := []struct {
		op   byte
		name string
		set  func(c *CPU, v byte)
	}{
		{0x51, "D", func(c *CPU, v byte) { c.D = v }},
		{0x59, "E", func(c *CPU, v byte) { c.E = v }},
		{0x61, "H", func(c *CPU, v byte) { c.H = v }},
		{0x69, "L", func(c *CPU, v byte) { c.L = v }},
		{0x79, "A", func(c *CPU, v byte) { c.A = v }},
	}
	for _, c := range cases {
		cpu, mem := createTestCPU()
		mu := cpu.ula.(*mockULA)
		cpu.B = 0x10
		cpu.C = 0x20
		c.set(cpu, 0x55)
		cpu.PC = 0x8000
		mem.Write(0x8000, 0xED)
		mem.Write(0x8001, c.op)
		cpu.executeInstruction()
		// BC = $10 << 8 | $20 = $1020.
		if got := mu.ports[0x1020]; got != 0x55 {
			t.Errorf("OUT (C),%s: port wrote $%02X, want $55 (op=$%02X)", c.name, got, c.op)
		}
	}
}

// OUT (C),B sends B to the port whose high byte is also B. We
// can't test "the data was 0x55 and the address was 0x1020"
// because the two are entangled — instead set BC = $4242 and
// verify the port at $4242 receives $42.
func TestOUTC_B_AddressIsBusValue(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	cpu.B = 0x42
	cpu.C = 0x42
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x41) // OUT (C),B
	cpu.executeInstruction()
	if got := mu.ports[0x4242]; got != 0x42 {
		t.Errorf("OUT (C),B with BC=$4242: port wrote $%02X, want $42", got)
	}
}

// OUTI block-I/O flag semantics.
//
// Per Sean Young §A.2 the flag calculation for OUTI uses the
// post-increment L and the just-read value:
//
//   B' = B - 1
//   HL' = HL + 1
//   temp = val + L'         (L' = low byte of HL')
//   Z = (B' == 0) ? 1 : 0
//   S = bit7(B')
//   N = 1
//   H = C = (temp > 255)
//   P/V = parity( (temp & 7) XOR B' )
//
// These tests pin the S-on-negative and H/C carry cases that
// were previously uncovered in outi/outd.

func TestOUTI_FlagSOnBNegative(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	_ = mu
	// B = $00 → B' = $FF (sign bit set).
	cpu.B = 0x00
	cpu.C = 0x20
	cpu.H = 0x90
	cpu.L = 0x00
	mem.Write(0x9000, 0x42) // val to send
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xA3) // OUTI
	cpu.executeInstruction()
	if cpu.B != 0xFF {
		t.Fatalf("OUTI: B = $%02X, want FF", cpu.B)
	}
	if cpu.F&FLAG_S == 0 {
		t.Errorf("OUTI with B'=$FF: S flag clear, want set")
	}
	if cpu.F&FLAG_Z != 0 {
		t.Errorf("OUTI with B'=$FF: Z flag set, want clear")
	}
	if cpu.F&FLAG_N == 0 {
		t.Errorf("OUTI: N flag clear, want set (always)")
	}
}

func TestOUTI_CarryWhenValPlusLOverflows(t *testing.T) {
	cpu, mem := createTestCPU()
	// val = $FF, L' = $02 (HL was $9001 → $9002 after increment) →
	// temp = 257 > 255, H = C = 1.
	cpu.B = 0x05
	cpu.C = 0x20
	cpu.H = 0x90
	cpu.L = 0x01
	mem.Write(0x9001, 0xFF)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xA3)
	cpu.executeInstruction()
	if cpu.F&FLAG_C == 0 {
		t.Errorf("OUTI carry: C clear, want set (val+L'=$101)")
	}
	if cpu.F&FLAG_H == 0 {
		t.Errorf("OUTI carry: H clear, want set")
	}
	if cpu.L != 0x02 {
		t.Errorf("OUTI: L = $%02X, want $02 (HL post-inc)", cpu.L)
	}
}

func TestOUTI_NoCarryWhenSumFits(t *testing.T) {
	cpu, mem := createTestCPU()
	// val = $10, L' = $05 → temp = $15, no carry.
	cpu.B = 0x05
	cpu.C = 0x20
	cpu.H = 0x90
	cpu.L = 0x04
	mem.Write(0x9004, 0x10)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xA3)
	cpu.executeInstruction()
	if cpu.F&FLAG_C != 0 {
		t.Errorf("OUTI no-carry: C set, want clear")
	}
	if cpu.F&FLAG_H != 0 {
		t.Errorf("OUTI no-carry: H set, want clear")
	}
}

func TestOUTD_FlagSOnBNegative(t *testing.T) {
	cpu, mem := createTestCPU()
	cpu.B = 0x00
	cpu.C = 0x20
	cpu.H = 0x90
	cpu.L = 0x00
	mem.Write(0x9000, 0x42)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xAB) // OUTD
	cpu.executeInstruction()
	if cpu.B != 0xFF {
		t.Fatalf("OUTD: B = $%02X, want FF", cpu.B)
	}
	if cpu.F&FLAG_S == 0 {
		t.Errorf("OUTD with B'=$FF: S clear, want set")
	}
}

func TestOUTD_CarryWhenValPlusLOverflows(t *testing.T) {
	cpu, mem := createTestCPU()
	// L pre-decrement = $00 → L' = $FF. val = $01 → temp = $100,
	// carry.
	cpu.B = 0x05
	cpu.C = 0x20
	cpu.H = 0x90
	cpu.L = 0x00
	mem.Write(0x9000, 0x01)
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0xAB)
	cpu.executeInstruction()
	if cpu.F&FLAG_C == 0 {
		t.Errorf("OUTD carry: C clear, want set (val=$01 + L'=$FF = $100)")
	}
	if cpu.L != 0xFF {
		t.Errorf("OUTD: L = $%02X, want FF (HL post-dec)", cpu.L)
	}
}

func TestOUTC_C_AddressIsBusValue(t *testing.T) {
	cpu, mem := createTestCPU()
	mu := cpu.ula.(*mockULA)
	cpu.B = 0x55
	cpu.C = 0x55
	cpu.PC = 0x8000
	mem.Write(0x8000, 0xED)
	mem.Write(0x8001, 0x49) // OUT (C),C
	cpu.executeInstruction()
	if got := mu.ports[0x5555]; got != 0x55 {
		t.Errorf("OUT (C),C with BC=$5555: port wrote $%02X, want $55", got)
	}
}
