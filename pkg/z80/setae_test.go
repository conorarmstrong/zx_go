package z80

import "testing"

// SETAE (ED 95) builds the per-pixel bit mask for the X coordinate in E:
// A = $80 >> (E & 7) — the leftmost pixel (E&7==0) is bit 7 ($80), the
// rightmost (E&7==7) is bit 0 ($01). ours had the bit order reversed
// (1 << (E&7)), which silently corrupted NextBASIC's DEFPROC integer
// parameter binding: NextBASIC uses SETAE to build the dirty-var bitmap, so
// a reversed mask marked the wrong int-var, the param never reached the
// var-cache the body reads, and "%i" leaked its global value
// ("Integer out of range, 2550:1" in NextBASIC Invaders).
func TestSETAE_Z80N_PixelMask(t *testing.T) {
	cases := []struct {
		e    byte
		want byte
	}{
		{0x00, 0x80}, // leftmost pixel -> bit 7
		{0x01, 0x40},
		{0x03, 0x10},
		{0x07, 0x01}, // rightmost pixel -> bit 0
		{0x08, 0x80}, // only low 3 bits of E matter
		{0xFF, 0x01},
	}
	for _, tc := range cases {
		cpu, mem := createTestCPU()
		cpu.Variant = VariantZ80N
		cpu.PC = 0x8000
		cpu.E = tc.e
		mem.Write(0x8000, 0xED)
		mem.Write(0x8001, 0x95) // SETAE
		cpu.StepInstruction()
		cleanupTestROMs("test_roms_z80")
		if cpu.A != tc.want {
			t.Errorf("SETAE E=$%02X: A=$%02X, want $%02X (A = $80 >> (E&7))", tc.e, cpu.A, tc.want)
		}
	}
}
