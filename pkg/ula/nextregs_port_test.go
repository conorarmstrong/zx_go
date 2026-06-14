package ula

import "testing"

// fakeNextRegs is a minimal stand-in for the nextregs dispatcher,
// capturing every method call so the port-routing tests can assert
// on dispatch order and values without dragging pkg/next/nextregs
// into the ula test build.
type fakeNextRegs struct {
	selected byte
	writes   []byte
	reads    int
	dataVal  byte
	regs     map[byte]byte
}

func (f *fakeNextRegs) Select(reg byte)    { f.selected = reg }
func (f *fakeNextRegs) Selected() byte     { return f.selected }
func (f *fakeNextRegs) WriteData(val byte) { f.writes = append(f.writes, val) }
func (f *fakeNextRegs) ReadData() byte     { f.reads++; return f.dataVal }
func (f *fakeNextRegs) WriteReg(reg, val byte) {
	if f.regs == nil {
		f.regs = make(map[byte]byte)
	}
	f.regs[reg] = val
}
func (f *fakeNextRegs) ReadReg(reg byte) byte {
	if f.regs == nil {
		return 0
	}
	return f.regs[reg]
}

func TestNextRegPortRouting(t *testing.T) {
	u := newTestULA(t)
	f := &fakeNextRegs{dataVal: 0xAB}
	u.SetNextRegs(f)

	// Write to 0x243B should land in Select.
	u.WritePort(0x243B, 0x42)
	if f.selected != 0x42 {
		t.Errorf("WritePort(0x243B, 0x42): selected = %#x, want 0x42", f.selected)
	}

	// Write to 0x253B should land in WriteData.
	u.WritePort(0x253B, 0x99)
	if len(f.writes) != 1 || f.writes[0] != 0x99 {
		t.Errorf("WritePort(0x253B, 0x99): writes = %+v, want [0x99]", f.writes)
	}

	// Read from 0x253B should route through ReadData.
	val, ok := u.ReadPort(0x253B)
	if !ok || val != 0xAB {
		t.Errorf("ReadPort(0x253B): val=%#x ok=%v, want 0xAB true", val, ok)
	}
	if f.reads != 1 {
		t.Errorf("ReadPort(0x253B) call count = %d, want 1", f.reads)
	}
}

func TestNextRegPortsInertWhenUnset(t *testing.T) {
	// With no dispatcher wired (SetNextRegs not called), port
	// 0x253B reads must NOT claim to be handled — the floating
	// bus / peripheral path should run.
	u := newTestULA(t)
	val, ok := u.ReadPort(0x253B)
	// Default ULA returns 0xFF, handled=false for unknown odd ports.
	if ok {
		t.Errorf("ReadPort(0x253B) with no NextRegs: ok=true, want false; val=%#x", val)
	}

	// Writes to 0x243B / 0x253B with no dispatcher should be silent
	// (no panic, no state change). Verifying no crash here is the
	// assertion.
	u.WritePort(0x243B, 0x00)
	u.WritePort(0x253B, 0x00)
}
