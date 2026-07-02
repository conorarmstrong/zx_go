package multiface

import "testing"

// Multiface3 + Multiface128 variant-specific HandlePortRead branches:
// MF3 inverts A7 (page-OUT on A7=1, page-IN on A7=0); MF128 accepts
// both MF1 and MF128 port shapes.

func TestHandlePortRead_MF3_A7Set_PagesOut(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, err := NewMultiface(Multiface3, dir, mem)
	if err != nil {
		t.Fatal(err)
	}
	mf.HandleNMI() // Page in first.
	if !mf.IsROMPaged() {
		t.Fatal("ROM should be paged in after NMI")
	}
	// MF3 port: $32 satisfies the mask. A7=1 → page out per MF3 spec.
	port := uint16(0x00B2) // bit 7 set, $B2 & $72 = $32
	_, handled := mf.HandlePortRead(port)
	if !handled {
		t.Fatal("HandlePortRead should accept MF3 port")
	}
	if mf.IsROMPaged() {
		t.Error("MF3 with A7=1 should page OUT")
	}
}

func TestHandlePortRead_MF3_A7Clear_PagesIn(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface3, dir, mem)
	port := uint16(0x0032) // bit 7 clear, MF3 port
	_, handled := mf.HandlePortRead(port)
	if !handled {
		t.Fatal("HandlePortRead should accept MF3 port")
	}
	if !mf.IsROMPaged() {
		t.Error("MF3 with A7=0 should page IN")
	}
}

func TestHandlePortRead_MF128_AcceptsBothPortShapes(t *testing.T) {
	mem, _ := newTestMemory(t)
	dir := t.TempDir()
	mf, _ := NewMultiface(Multiface128, dir, mem)
	// MF128 should accept BOTH the MF1 port AND the MF128 port shape.
	for _, port := range []uint16{0x009F, 0x00B2} {
		if _, ok := mf.HandlePortRead(port); !ok {
			t.Errorf("MF128 should accept port $%04X", port)
		}
	}
}
