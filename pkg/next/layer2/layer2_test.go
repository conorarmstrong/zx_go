package layer2

import "testing"

// fakeBanks is a fixed-size 256-bank fake BankReader, with bank 0
// initialised to a recognisable pattern: bank[i].byte[j] = (i*256+j) & 0xFF
// so cross-bank reads can be verified.
type fakeBanks struct {
	banks [256][16384]byte
}

func (f *fakeBanks) GetPage(bank int) []byte {
	if bank < 0 || bank >= 256 {
		return nil
	}
	return f.banks[bank][:]
}

func newFakeBanks() *fakeBanks {
	f := &fakeBanks{}
	for b := 0; b < 256; b++ {
		for j := 0; j < 16384; j++ {
			f.banks[b][j] = byte((b*7 + j) & 0xFF) // distinctive per-bank pattern
		}
	}
	return f
}

func TestLayer2DisabledFillsZeros(t *testing.T) {
	f := newFakeBanks()
	l := New(f)
	l.SetActiveBank(9) // some non-zero bank
	dst := make([]byte, Width)
	for i := range dst {
		dst[i] = 0xFF
	}
	l.RenderScanline(0, dst)
	for i, b := range dst {
		if b != 0 {
			t.Errorf("dst[%d] = %#x after disabled render, want 0", i, b)
			break
		}
	}
}

func TestLayer2RenderScanlineCopiesFromActiveBank(t *testing.T) {
	f := newFakeBanks()
	l := New(f)
	l.SetActiveBank(9)
	l.SetEnabled(true)

	// Row 0: bank 9, offset 0, 256 bytes.
	dst := make([]byte, Width)
	l.RenderScanline(0, dst)
	for i := 0; i < Width; i++ {
		want := byte((9*7 + i) & 0xFF)
		if dst[i] != want {
			t.Errorf("row 0 col %d: got %#x, want %#x", i, dst[i], want)
		}
	}

	// Row 64: spills into bank 10, offset 0.
	l.RenderScanline(64, dst)
	for i := 0; i < Width; i++ {
		want := byte((10*7 + i) & 0xFF)
		if dst[i] != want {
			t.Errorf("row 64 col %d: got %#x, want %#x", i, dst[i], want)
			break
		}
	}

	// Row 128: bank 11.
	l.RenderScanline(128, dst)
	for i := 0; i < Width; i++ {
		want := byte((11*7 + i) & 0xFF)
		if dst[i] != want {
			t.Errorf("row 128 col %d: got %#x, want %#x", i, dst[i], want)
			break
		}
	}
}

func TestLayer2RenderScanlineRejectsBadInputs(t *testing.T) {
	f := newFakeBanks()
	l := New(f)
	l.SetEnabled(true)
	dst := make([]byte, Width)

	// Y out of range — no-op.
	for _, y := range []int{-1, Height, 1000} {
		for i := range dst {
			dst[i] = 0xAB
		}
		l.RenderScanline(y, dst)
		for _, b := range dst {
			if b != 0xAB {
				t.Errorf("RenderScanline(y=%d) modified dst", y)
				break
			}
		}
	}

	// Short dst — no-op.
	short := make([]byte, 10)
	l.RenderScanline(0, short)
}

func TestLayer2SetActiveBankMasksHighBit(t *testing.T) {
	l := New(newFakeBanks())
	l.SetActiveBank(0xFF)
	if l.ActiveBank() != 0x7F {
		t.Errorf("SetActiveBank(0xFF): ActiveBank = %#x, want 0x7F", l.ActiveBank())
	}
}

func TestLayer2ShadowBank(t *testing.T) {
	l := New(newFakeBanks())
	l.SetShadowBank(42)
	if l.ShadowBank() != 42 {
		t.Errorf("ShadowBank = %d, want 42", l.ShadowBank())
	}
	// Render still uses ACTIVE bank.
	l.SetEnabled(true)
	dst := make([]byte, Width)
	l.RenderScanline(0, dst)
	// Default activeBank=0; bank 0 byte 0 = 0.
	if dst[0] != 0 {
		t.Errorf("dst[0] = %#x; render should use active bank not shadow", dst[0])
	}
}

// iter 364: Enabled() getter mirrors SetEnabled().
func TestLayer2EnabledGetter(t *testing.T) {
	l := New(newFakeBanks())
	if l.Enabled() {
		t.Error("new Layer2 should be disabled by default")
	}
	l.SetEnabled(true)
	if !l.Enabled() {
		t.Error("Enabled() = false after SetEnabled(true)")
	}
	l.SetEnabled(false)
	if l.Enabled() {
		t.Error("Enabled() = true after SetEnabled(false)")
	}
}

// TestLayer2_640ColumnMajor4bpp verifies the 640×256 4bpp column-major
// render: byte(c,y)=base+c*256+y, high nibble = left pixel, low = right.
func TestLayer2_640ColumnMajor4bpp(t *testing.T) {
	f := newFakeBanks()
	l := New(f)
	l.SetEnabled(true)
	l.SetResolution(2) // 640×256 4bpp
	l.SetActiveBank(0)
	if l.LineWidth() != 640 || l.LineHeight() != 256 {
		t.Fatalf("dims = %d×%d, want 640×256", l.LineWidth(), l.LineHeight())
	}
	dst := make([]byte, 640)
	l.RenderScanline(171, dst)
	// Col 0, y=171: byte=(0*7+171)&0xFF=0xAB → pixels 0xA, 0xB.
	if dst[0] != 0xA || dst[1] != 0xB {
		t.Errorf("col 0: dst[0,1]=%#x,%#x, want 0xA,0xB", dst[0], dst[1])
	}
	// Col 64 (bank 1), y=171: byte=(1*7+171)&0xFF=0xB2 → 0xB, 0x2.
	if dst[128] != 0xB || dst[129] != 0x2 {
		t.Errorf("col 64: dst[128,129]=%#x,%#x, want 0xB,0x2", dst[128], dst[129])
	}
}

// TestLayer2_PaletteOffset640 verifies the NR$70 palette offset is added
// to each pixel's high nibble (FPGA layer2.vhd:203).
func TestLayer2_PaletteOffset640(t *testing.T) {
	f := newFakeBanks()
	l := New(f)
	l.SetEnabled(true)
	l.SetResolution(2) // 640×256 4bpp
	l.SetPaletteOffset(3)
	l.SetActiveBank(0)
	dst := make([]byte, 640)
	l.RenderScanline(171, dst) // col 0 byte 0xAB → nibbles 0xA, 0xB
	// offset 3 → high nibble: applyOffset(0x0A)=0x3A, applyOffset(0x0B)=0x3B.
	if dst[0] != 0x3A || dst[1] != 0x3B {
		t.Errorf("640 offset 3: dst[0,1]=%#x,%#x, want 0x3A,0x3B", dst[0], dst[1])
	}
}
