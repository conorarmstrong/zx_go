package palette

import "testing"

// TestPalettePriority covers the per-entry priority storage + the Layer 2
// promote bit (NR$44 bit 7).
func TestPalettePriority(t *testing.T) {
	p := New()
	p.SetPriority(5, 0x03) // both priority bits → bit 7 set
	if !p.HasPriority(5) || p.Priority(5) != 0x03 {
		t.Errorf("entry 5: HasPriority=%v Priority=%d, want true/3", p.HasPriority(5), p.Priority(5))
	}
	p.SetPriority(6, 0x01) // only bit 6 → NOT the L2 promote bit
	if p.HasPriority(6) {
		t.Error("entry 6: HasPriority=true, want false (bit 7 clear)")
	}
	// Write9 captures lo bits 7:6 as the priority.
	b := NewBank()
	b.Write9(0x00, 0x80) // lo bit 7 set → promote bit
	if !b.Active().HasPriority(0) {
		t.Error("Write9 lo=0x80: entry 0 HasPriority=false, want true")
	}
	b.SetIndex(0) // Write9 auto-incremented; point back at the entry we wrote
	if got := b.ReadNR44(); got&0x80 == 0 {
		t.Errorf("ReadNR44 = %#x, want bit 7 set", got)
	}
}
