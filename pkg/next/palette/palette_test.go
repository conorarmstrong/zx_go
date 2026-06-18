package palette

import "testing"

func TestSetGetMasksTo9Bits(t *testing.T) {
	p := New()
	p.Set(5, 0xFFFF)
	if got := p.Get(5); got != 0x01FF {
		t.Errorf("Set(0xFFFF) Get = %#x, want 0x01FF (low 9 bits only)", got)
	}
}

func TestRGBExtractsThreeBitChannels(t *testing.T) {
	p := New()
	// Pure red at max: bits 8..6 = 111
	p.Set(0, 0b1_1100_0000)
	r, g, b := p.RGB(0)
	if r == 0 || g != 0 || b != 0 {
		t.Errorf("max red: got r=%d g=%d b=%d", r, g, b)
	}
	// Pure green at max: bits 5..3 = 111
	p.Set(1, 0b0_0011_1000)
	r, g, b = p.RGB(1)
	if r != 0 || g == 0 || b != 0 {
		t.Errorf("max green: got r=%d g=%d b=%d", r, g, b)
	}
	// Pure blue at max: bits 2..0 = 111 (excluding the low blue bit)
	p.Set(2, 0b0_0000_0111)
	r, g, b = p.RGB(2)
	if r != 0 || g != 0 || b == 0 {
		t.Errorf("max blue: got r=%d g=%d b=%d", r, g, b)
	}
}

func TestBankSelectAndActive(t *testing.T) {
	b := NewBank()
	if b.Selected() != 0 {
		t.Errorf("default Selected = %d, want 0", b.Selected())
	}
	b.Select(PaletteLayer2First)
	if b.Selected() != PaletteLayer2First {
		t.Errorf("Selected = %d, want PaletteLayer2First (%d)", b.Selected(), PaletteLayer2First)
	}
	// Active() returns a different *Palette than the default-selected one.
	def := b.Palette(int(PaletteULAFirst))
	b.Select(PaletteLayer2First)
	if b.Active() == def {
		t.Errorf("Active() returned the same palette after selection change")
	}
}

func TestBankWrite8AutoIncrements(t *testing.T) {
	b := NewBank()
	b.SetIndex(0x10)
	b.Write8(0xAB)
	b.Write8(0xCD)

	if b.Index() != 0x12 {
		t.Errorf("Index after two Write8 = %#x, want 0x12", b.Index())
	}
	// Write8 shifts the input left by 1 and sets the low blue bit to
	// (byte bit1 | bit0) per zxnext.vhd:4919. $AB has bit1|bit0 = 1.
	if got, want := b.Active().Get(0x10), (uint16(0xAB)<<1|1)&0x1FF; got != want {
		t.Errorf("entry 0x10 = %#x, want %#x", got, want)
	}
}

func TestBankWrite9CombinesBytes(t *testing.T) {
	b := NewBank()
	b.SetIndex(0x20)
	// hi=0xFF, lo=0x01 -> 9-bit = 0x1FF
	b.Write9(0xFF, 0x01)
	if got := b.Active().Get(0x20); got != 0x01FF {
		t.Errorf("Write9(0xFF, 0x01): entry = %#x, want 0x01FF", got)
	}
	// hi=0x00, lo=0x01 -> 9-bit = 0x001
	b.Write9(0x00, 0x01)
	if got := b.Active().Get(0x21); got != 0x001 {
		t.Errorf("Write9(0x00, 0x01): entry = %#x, want 0x001", got)
	}
	if b.Index() != 0x22 {
		t.Errorf("Index after two Write9 = %#x, want 0x22", b.Index())
	}
}

func TestBankPaletteOutOfRange(t *testing.T) {
	b := NewBank()
	if b.Palette(-1) != nil || b.Palette(8) != nil {
		t.Errorf("Palette out-of-range should return nil")
	}
}

// TestWriteNR44ProtocolTwoStep verifies that WriteNR44 implements
// the FPGA's NextReg 0x44 two-byte palette write: the first call
// arms the latch (no palette change), the second call commits the
// pair as a 9-bit entry and advances the auto-increment cursor.
func TestWriteNR44ProtocolTwoStep(t *testing.T) {
	b := NewBank()
	b.Select(PaletteTilemapFirst)
	b.SetIndex(0)
	// Pair 1: 9-bit entry = (0x55 << 1) | 1 = $0AB
	b.WriteNR44(0x55)
	b.WriteNR44(0x01)
	// Pair 2: 9-bit entry = (0x33 << 1) | 0 = $066, auto-incremented
	// to index 1.
	b.WriteNR44(0x33)
	b.WriteNR44(0x00)

	if got := b.PaletteForLayer(LayerTilemap).Get(0); got != 0x0AB {
		t.Errorf("entry 0 = %#x, want $0AB", got)
	}
	if got := b.PaletteForLayer(LayerTilemap).Get(1); got != 0x066 {
		t.Errorf("entry 1 = %#x, want $066", got)
	}
}

// TestResetWriteLatchesAfterHalfPair pins the Reboot-corruption fix.
// nextRegs.Reset writes a single 0 byte to NR$44 as part of its
// "zero every register" Phase 1, leaving the latch in "got high,
// awaiting low" state. Without ResetWriteLatches the first NR$44
// write from boot.bin in the next session would be consumed as the
// LOW byte of a (0, guest) pair, corrupting palette index 0 and
// shifting every subsequent write off by one. ResetWriteLatches
// drops the half-pair so the next protocol cycle starts clean.
func TestResetWriteLatchesAfterHalfPair(t *testing.T) {
	b := NewBank()
	b.Select(PaletteTilemapFirst)
	b.SetIndex(0)
	// Half-pair: only the first write of an NR$44 pair.
	b.WriteNR44(0xAA)
	// Without reset, the next two writes would commit (0xAA, 0xCD)
	// then arm again with 0xEF — sending entry 1's intended high
	// byte into the LOW slot of the previous pair.
	b.ResetWriteLatches()
	// A fresh pair must now write to index 0 cleanly: $CD high,
	// $01 low → entry 0 = ($CD << 1) | 1 = $19B.
	b.WriteNR44(0xCD)
	b.WriteNR44(0x01)
	if got := b.PaletteForLayer(LayerTilemap).Get(0); got != 0x19B {
		t.Errorf("entry 0 after ResetWriteLatches = %#x, want $19B "+
			"(pre-reset 0xAA half-pair must be discarded)", got)
	}
}

// ====================================================================
// Additional palette tests (iter 211).
// ====================================================================

// TestRGBChannelMidValues exercises non-maximum RGB values to verify
// the 3-bit-channel extraction returns proportional outputs (not
// just on/off).
func TestRGBChannelMidValues(t *testing.T) {
	p := New()
	// Half-red (R=4, G=0, B=0): 9-bit value = 100 000 000b = $100.
	p.Set(0, 0x100)
	r, g, b := p.RGB(0)
	if r == 0 || r == 255 {
		t.Errorf("half-red: r = %d (should be mid-range)", r)
	}
	if g != 0 || b != 0 {
		t.Errorf("half-red: g=%d b=%d (should be 0)", g, b)
	}
}

// TestBankSetActiveTracksPerLayer verifies that SetActive routes per-
// layer active-palette selection (NR$43 bits 3, 2, 1) correctly.
func TestBankSetActiveTracksPerLayer(t *testing.T) {
	b := NewBank()
	// Active ULA → first, Layer 2 → second, Sprite → second.
	b.SetActive(LayerULA, 0)
	b.SetActive(LayerLayer2, 1)
	b.SetActive(LayerSprites, 1)

	if got := b.PaletteForLayer(LayerULA); got == nil {
		t.Error("ULA layer palette nil")
	}
	if got := b.PaletteForLayer(LayerLayer2); got == nil {
		t.Error("Layer2 layer palette nil")
	}
	// Spot-check: ULA palette and L2 palette are different banks.
	pULA := b.PaletteForLayer(LayerULA)
	pULA.Set(0, 0x100)
	pL2 := b.PaletteForLayer(LayerLayer2)
	if pL2.Get(0) == 0x100 {
		t.Error("ULA write leaked into L2 palette (per-layer routing broken)")
	}
}

// TestBankIndexAfter256Wraps verifies the 8-bit index counter wraps
// from $FF back to $00 on auto-increment.
func TestBankIndexAfter256Wraps(t *testing.T) {
	b := NewBank()
	b.SetIndex(0xFF)
	b.Write8(0xAA) // index advances $FF → $00
	if b.Index() != 0x00 {
		t.Errorf("Index after wrap = $%02X, want $00", b.Index())
	}
}

// TestBankWrite8BlueLowBitFromBits10 verifies the 8-bit write path sets
// the 9-bit entry's low blue bit (bit 0) to (byte bit1 | bit0), per
// zxnext.vhd:4919 — NOT forced to 0 (the previous behaviour, which a stale
// comment claimed and which broke NextZXOS's palette read-back).
func TestBankWrite8BlueLowBitFromBits10(t *testing.T) {
	b := NewBank()
	b.Write8(0xFF) // bits 1:0 = 11 → low blue bit 1
	if got := b.Active().Get(0); got != 0x1FF {
		t.Errorf("Write8(0xFF) → $%03X, want $1FF (9th bit = bit1|bit0 = 1)", got)
	}
	b.SetIndex(0)
	b.Write8(0xFC) // bits 1:0 = 00 → low blue bit 0
	if got := b.Active().Get(0); got != 0x1F8 {
		t.Errorf("Write8(0xFC) → $%03X, want $1F8 (9th bit = bit1|bit0 = 0)", got)
	}
}

// TestBankWrite9LowBitFromBit0 verifies Write9's low-byte semantics:
// only bit 0 of the low byte matters (high 7 bits ignored).
func TestBankWrite9LowBitFromBit0(t *testing.T) {
	b := NewBank()
	b.Write9(0x80, 0xFE) // hi has bit 7 only; lo has bit 0 = 0
	if got := b.Active().Get(0); got != 0x100 {
		t.Errorf("Write9(0x80, 0xFE) → $%03X, want $100 (lo bits 7:1 ignored)",
			got)
	}
}

// TestBankSelectOutOfRange — Select with bank index > 7 must not
// crash. The implementation should clamp or no-op.
func TestBankSelectOutOfRange(t *testing.T) {
	b := NewBank()
	b.Select(99) // way out of range
	// Subsequent writes should not panic. Test passes if we reach
	// this assertion.
	b.Write8(0xAA)
}

// TestBankReadBackNoAutoInc pins the NextReg $41/$44 read-back semantics
// from zxnext.vhd: $41 read = palette value bits 8:1 (:6038); $44 read =
// value bit 0 in bit 0 (:6047, plus 2-bit priority in 7:6 which we don't
// store yet). Reading does NOT auto-increment the index — only writes do
// (:5380/:5401), unlike Write8/Write9.
func TestBankReadBackNoAutoInc(t *testing.T) {
	b := NewBank()
	b.SetIndex(0x10)
	// Store a 9-bit value with a set LSB at index $10 of the selected palette.
	b.Active().Set(0x10, 0x1A5)        // 1_1010_0101
	if got := b.Read8(); got != 0xD2 { // (0x1A5 >> 1) & 0xFF = 0xD2
		t.Errorf("Read8 = $%02X, want $D2 (bits 8:1)", got)
	}
	if got := b.ReadNR44(); got != 0x01 { // value bit 0 = 1
		t.Errorf("ReadNR44 = $%02X, want $01 (value bit 0)", got)
	}
	if b.Index() != 0x10 {
		t.Errorf("read auto-incremented index to $%02X; reads must NOT auto-inc", b.Index())
	}
	// Even LSB → ReadNR44 bit 0 clear.
	b.Active().Set(0x10, 0x1A4)
	if got := b.ReadNR44(); got != 0x00 {
		t.Errorf("ReadNR44 = $%02X, want $00 (value bit 0 clear)", got)
	}
}

// TestWrite8NinthBit pins the NextReg $41 write 9th-bit rule from
// zxnext.vhd:4919: nr_palette_value <= nr_wr_dat & (nr_wr_dat(1) or
// nr_wr_dat(0)). The low blue bit (bit 0 of the 9-bit value) is the OR of
// the written byte's bits 1 and 0 — NOT forced to 0. This is what NR$44
// read-back returns, and NextZXOS's palette read-back loop depends on it.
func TestWrite8NinthBit(t *testing.T) {
	cases := []struct {
		in   byte
		want uint16
	}{
		{0x00, 0x000}, // bits1:0=00 → 9th bit 0
		{0x01, 0x003}, // bit0=1 → 9th bit 1  ((0x01<<1)|1)
		{0x02, 0x005}, // bit1=1 → 9th bit 1  ((0x02<<1)|1)
		{0x03, 0x007}, // bits1:0=11 → 9th bit 1
		{0x04, 0x008}, // bits1:0=00 (bit2 set) → 9th bit 0
		{0xFF, 0x1FF}, // all bits → 9th bit 1
	}
	for _, c := range cases {
		b := NewBank()
		b.SetIndex(0x20)
		b.Write8(c.in)
		if got := b.Active().Get(0x20); got != c.want {
			t.Errorf("Write8($%02X): entry=$%03X, want $%03X (9th bit = byte bit1|bit0)", c.in, got, c.want)
		}
	}
}
