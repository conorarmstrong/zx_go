package sam

import "testing"

// synthROM builds a 32K SAM ROM image whose first byte is DI (0xF3, the real
// ROM's reset entry) with recognisable markers at the start of each 16K half.
func synthROM() []byte {
	r := make([]byte, ROMSize)
	r[0] = 0xF3         // DI — ROM0 reset entry
	r[1] = 0xAA         // ROM0 marker
	r[PageSize] = 0x55  // ROM1 marker
	r[ROMSize-1] = 0x99 // tail marker
	return r
}

func TestSplitROM_Valid(t *testing.T) {
	rom0, rom1, err := SplitROM(synthROM())
	if err != nil {
		t.Fatalf("SplitROM: %v", err)
	}
	if len(rom0) != PageSize || len(rom1) != PageSize {
		t.Fatalf("halves must be %d bytes, got %d/%d", PageSize, len(rom0), len(rom1))
	}
	if rom0[0] != 0xF3 || rom0[1] != 0xAA {
		t.Errorf("ROM0 not the low 16K (got %02x %02x)", rom0[0], rom0[1])
	}
	if rom1[0] != 0x55 || rom1[PageSize-1] != 0x99 {
		t.Errorf("ROM1 not the high 16K (got %02x ... %02x)", rom1[0], rom1[PageSize-1])
	}
}

func TestSplitROM_StripsZX82Header(t *testing.T) {
	data := append([]byte("ZX82"), make([]byte, ZX82HeaderLen-4)...)
	data = append(data, synthROM()...)
	rom0, _, err := SplitROM(data)
	if err != nil {
		t.Fatalf("SplitROM with ZX82 header: %v", err)
	}
	if rom0[0] != 0xF3 || rom0[1] != 0xAA {
		t.Errorf("ZX82 header not stripped (ROM0 got %02x %02x)", rom0[0], rom0[1])
	}
}

func TestSplitROM_TooSmall(t *testing.T) {
	if _, _, err := SplitROM(make([]byte, ROMSize-1)); err == nil {
		t.Error("expected error for an undersized image")
	}
}

func TestSplitROM_OversizedNeedsDIEntry(t *testing.T) {
	// Larger than 32K is only accepted when it starts with DI (a raw ROM dump
	// with trailing junk); otherwise it's rejected as not-a-ROM.
	big := append(synthROM(), make([]byte, 1024)...) // first byte is DI → ok
	if _, _, err := SplitROM(big); err != nil {
		t.Errorf("oversized DI-led image should be accepted: %v", err)
	}
	bad := make([]byte, ROMSize+1024)
	bad[0] = 0x00 // not DI, and not exactly 32K → reject
	if _, _, err := SplitROM(bad); err == nil {
		t.Error("expected error for an oversized non-DI image")
	}
}
