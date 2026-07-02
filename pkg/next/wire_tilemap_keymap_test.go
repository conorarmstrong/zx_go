package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/keymap"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
)

// Covers WireTilemap (NR$6B/$6C/$6E/$6F) + WireKeymap
// (NR$28/$29/$2B) dispatcher wiring.

type tilemapBanks struct{ banks [16][]byte }

func (b *tilemapBanks) GetPage(bank int) []byte {
	if bank < 0 || bank >= len(b.banks) {
		return nil
	}
	if b.banks[bank] == nil {
		b.banks[bank] = make([]byte, 16384)
	}
	return b.banks[bank]
}

func TestWireTilemap_NR6B_EnablesAndStores(t *testing.T) {
	disp := nextregs.New()
	tm := tilemap.New(&tilemapBanks{})
	pal := palette.NewBank()
	WireTilemap(disp, tm, pal)
	// Bit 7 set → enabled. Bit 4 set → tilemap second palette.
	disp.WriteReg(0x6B, 0x90)
	if !tm.Enabled() {
		t.Error("NR$6B bit 7 set: tilemap not enabled")
	}
	if got := disp.ReadReg(0x6B); got != 0x90 {
		t.Errorf("ReadReg($6B) = $%02X, want $90", got)
	}
}

func TestWireTilemap_NR6E_BitsMasked(t *testing.T) {
	disp := nextregs.New()
	tm := tilemap.New(&tilemapBanks{})
	WireTilemap(disp, tm, nil)
	// Write $FF — bit 6 should be dropped on store.
	disp.WriteReg(0x6E, 0xFF)
	if got := disp.ReadReg(0x6E); got != 0xBF {
		t.Errorf("NR$6E bit-6 mask: read = $%02X, want $BF", got)
	}
}

func TestWireTilemap_NR6F_BitsMasked(t *testing.T) {
	disp := nextregs.New()
	tm := tilemap.New(&tilemapBanks{})
	WireTilemap(disp, tm, nil)
	disp.WriteReg(0x6F, 0xFF)
	if got := disp.ReadReg(0x6F); got != 0xBF {
		t.Errorf("NR$6F bit-6 mask: read = $%02X, want $BF", got)
	}
}

func TestWireKeymap_RoutesAllRegs(t *testing.T) {
	disp := nextregs.New()
	km := keymap.New()
	WireKeymap(disp, km)
	// Write address-high (bit 0 = bit 8 of 9-bit addr).
	disp.WriteReg(0x28, 0x01)
	// Write address-low.
	disp.WriteReg(0x29, 0x42)
	// Write data — auto-increments addr.
	disp.WriteReg(0x2B, 0x77)
	// We can't easily inspect keymap internals without a public API,
	// but the call sequence must succeed and the dispatcher must
	// store all three values.
	if got := disp.ReadReg(0x28); got != 0x01 {
		t.Errorf("ReadReg($28) = $%02X, want $01", got)
	}
	if got := disp.ReadReg(0x29); got != 0x42 {
		t.Errorf("ReadReg($29) = $%02X, want $42", got)
	}
	if got := disp.ReadReg(0x2B); got != 0x77 {
		t.Errorf("ReadReg($2B) = $%02X, want $77", got)
	}
}
