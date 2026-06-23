package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// Clip-window registers NR$18 (Layer2), $19 (sprite), $1A (ULA), $1B
// (tilemap) are NOT single bytes: each holds four sub-coordinates
// (x1,x2,y1,y2) cycled by a 2-bit index. Per zxnext.vhd:
//   - write stores coord[idx] then idx = (idx+1) & 3   (5242-5276)
//   - read returns coord[idx] WITHOUT incrementing      (5947-5975)
//   - reset defaults: $18/$19/$1A = {00,FF,00,BF}; $1B = {00,9F,00,FF}
//   - NR$1C write: bit0/1/2/3 reset the L2/sprite/ULA/tilemap index
//   - NR$1C read: packed tm<<6 | ula<<4 | spr<<2 | l2          (5975)
// Reading an un-written NR$18 must return x1 = $00 (the boot reads these
// via IN D,(C); our old single-byte model returned the wrong coordinate).

func clipDisp() *nextregs.Dispatcher {
	d := nextregs.New()
	WireClipWindows(d, nil, nil)
	return d
}

func TestClipWindow_DefaultReadIsX1(t *testing.T) {
	d := clipDisp()
	for _, reg := range []byte{0x18, 0x19, 0x1A, 0x1B} {
		if got := d.ReadReg(reg); got != 0x00 {
			t.Errorf("NR$%02X default read = $%02X, want $00 (x1)", reg, got)
		}
	}
}

func TestClipWindow_WriteCyclesThenReadX1(t *testing.T) {
	d := clipDisp()
	// Four writes fill x1,x2,y1,y2 and wrap idx back to 0.
	for _, v := range []byte{0x11, 0x22, 0x33, 0x44} {
		d.WriteReg(0x18, v)
	}
	if got := d.ReadReg(0x18); got != 0x11 { // idx wrapped to 0 → x1
		t.Errorf("NR$18 after 4 writes reads $%02X, want $11 (x1)", got)
	}
}

func TestClipWindow_PartialWriteReadsNextCoord(t *testing.T) {
	d := clipDisp()
	d.WriteReg(0x18, 0x11) // x1=$11, idx→1
	if got := d.ReadReg(0x18); got != 0xFF {
		t.Errorf("NR$18 after 1 write reads $%02X, want $FF (x2 default)", got)
	}
}

func TestClipWindow_TilemapDefaults(t *testing.T) {
	d := clipDisp()
	d.WriteReg(0x1B, 0x10) // x1=$10, idx→1
	if got := d.ReadReg(0x1B); got != 0x9F {
		t.Errorf("NR$1B after 1 write reads $%02X, want $9F (x2 default)", got)
	}
}

func TestClipWindow_NR1CReadsPackedIndices(t *testing.T) {
	d := clipDisp()
	d.WriteReg(0x18, 0x00) // l2 idx → 1
	d.WriteReg(0x19, 0x00) // spr idx → 1
	// packed = tm(0)<<6 | ula(0)<<4 | spr(1)<<2 | l2(1) = 0b0000_0101
	if got := d.ReadReg(0x1C); got != 0x05 {
		t.Errorf("NR$1C packed indices = $%02X, want $05", got)
	}
}

func TestClipWindow_NR1CWriteResetsIndex(t *testing.T) {
	d := clipDisp()
	d.WriteReg(0x18, 0x00) // l2 idx → 1
	d.WriteReg(0x1C, 0x01) // bit0 resets l2 idx
	if got := d.ReadReg(0x1C); got != 0x00 {
		t.Errorf("NR$1C after reset-bit0 = $%02X, want $00", got)
	}
}

func TestClipWindow_DispatcherResetRestoresDefaults(t *testing.T) {
	d := clipDisp()
	d.WriteReg(0x18, 0x77) // x1=$77, idx→1
	d.WriteReg(0x18, 0x88) // x2=$88, idx→2
	d.Reset()
	if got := d.ReadReg(0x18); got != 0x00 {
		t.Errorf("NR$18 after Reset reads $%02X, want $00 (x1 default, idx 0)", got)
	}
}
