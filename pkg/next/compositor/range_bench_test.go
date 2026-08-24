package compositor

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
)

// What a row costs to compose, against how many Copper writes land on it.
//
// ComposeScanlineRange re-renders the whole Layer 2, tilemap and sprite
// scanline before it consults x0/x1, and pkg/ula calls it once per Copper
// write, so the per-row cost is linear in the writes. Measured on an M-series
// laptop at 192 displayed rows against a 20 ms frame budget:
//
//	writes/row   ns/row    ms/frame   % of budget
//	         0     1_639        0.31          1.6
//	         8     8_527        1.64          8.2
//	       256   228_573       43.90        219.5
//
// So an ordinary Copper list is affordable and a saturated one is not: at one
// MOVE per column the compositor alone needs twice the frame budget, and a MOVE
// costs two of a column's four copper clocks, so twice that density is
// reachable. See ROADMAP item 3.
func benchCompositor(b *testing.B) (*Compositor, []byte, []byte) {
	b.Helper()
	pal := palette.NewBank()
	banks := &fakeBanks{banks: map[int][]byte{}}
	for i := 0; i < 16; i++ {
		banks.banks[i] = make([]byte, 8192)
	}
	l2 := layer2.New(banks)
	l2.SetEnabled(true)
	c := New(pal, l2)
	ula := make([]byte, Width*4)
	dst := make([]byte, Width*4)
	return c, ula, dst
}

// One compose of a whole row: what a row costs with no Copper writes on it.
func BenchmarkComposeRowOnce(b *testing.B) {
	c, ula, dst := benchCompositor(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.ComposeScanlineRange(0, ula, dst, 0, Width)
	}
}

// The same row when a Copper write lands in every displayed column, which is
// what the ULA does today: one full ComposeScanlineRange per write, each
// re-rendering the whole Layer 2 / tilemap / sprite scanline before it looks at
// the range it was given.
func BenchmarkComposeRowWith256CopperWrites(b *testing.B) {
	c, ula, dst := benchCompositor(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.ComposeScanlineRange(0, ula, dst, 0, Width)
		for p := 0; p < Width; p++ {
			c.ComposeScanlineRange(0, ula, dst, p, Width)
		}
	}
}

// A more realistic Copper list: a handful of writes per row rather than one per
// column.
func BenchmarkComposeRowWith8CopperWrites(b *testing.B) {
	c, ula, dst := benchCompositor(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.ComposeScanlineRange(0, ula, dst, 0, Width)
		for k := 1; k <= 8; k++ {
			c.ComposeScanlineRange(0, ula, dst, k*Width/9, Width)
		}
	}
}
