package compositor

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
)

// What a row costs to compose, against how many Copper writes land on it.
//
// ComposeScanlineRange re-renders the whole Layer 2, tilemap and sprite
// scanline before it consults x0/x1, so re-composing a row's tail once per
// Copper write is linear in the writes. Measured on an M-series laptop at 192
// displayed rows against a 20 ms frame budget:
//
//	writes/row   ns/row    ms/frame   % of budget
//	         0     1_639        0.31          1.6
//	         8     8_527        1.64          8.2
//	       256   228_573       43.90        219.5
//
// That was how pkg/ula drew a Copper row until 2026-11-24. It now paints
// each segment of the row once, through ComposeFrameRowTail, and renders the
// layers again only after a write in LayerRenderReads; see
// BenchmarkSaturatedCopperFrame in cmd/zx_go for the whole-frame figure.
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

// The same 256-write row through ComposeFrameRowTail, which is what the ULA
// does now for a write that names no register in LayerRenderReads: the layers
// are rendered once and only the paint loop runs per write.
func BenchmarkComposeRowWith256CachedCopperWrites(b *testing.B) {
	c, _, _ := benchCompositor(b)
	ula := make([]byte, FullWidth*4)
	dst := make([]byte, FullWidth*4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.ComposeFrameRowRange(64, ula, dst, 1, 0, FullWidth)
		for p := 0; p < Width; p++ {
			c.ComposeFrameRowTail(64, ula, dst, 1, BorderOffsetX+p, FullWidth)
		}
	}
}

// A row's tail compose reuses the layer renders until they are invalidated,
// and only for the row they were made for.
func TestComposeFrameRowTailReusesRendersUntilInvalidated(t *testing.T) {
	pal := palette.NewBank()
	banks := &fakeBanks{banks: map[int][]byte{}}
	for i := 0; i < 16; i++ {
		banks.banks[i] = make([]byte, 8192)
	}
	l2 := layer2.New(banks)
	l2.SetEnabled(true)
	c := New(pal, l2)
	// A second layer that stays on, so turning Layer 2 off below leaves a
	// layer to compose and the cache valid: only the key's record of which
	// layers it rendered can then tell the cache is short of Layer 2.
	tm := tilemap.New(banks)
	tm.SetEnabled(true)
	c.SetTilemap(tm)
	ula := make([]byte, FullWidth*4)
	dst := make([]byte, FullWidth*4)

	c.ComposeFrameRowRange(64, ula, dst, 1, 0, FullWidth)
	if c.renders != 1 {
		t.Fatalf("a full compose made %d renders, want 1", c.renders)
	}
	for p := 1; p < 100; p++ {
		c.ComposeFrameRowTail(64, ula, dst, 1, p, FullWidth)
	}
	if c.renders != 1 {
		t.Errorf("99 tail composes of the same row made %d renders in all, want 1", c.renders)
	}
	c.InvalidateLayerRenders()
	c.ComposeFrameRowTail(64, ula, dst, 1, 100, FullWidth)
	if c.renders != 2 {
		t.Errorf("a tail compose after InvalidateLayerRenders made %d renders in all, want 2", c.renders)
	}
	c.ComposeFrameRowTail(65, ula, dst, 1, 100, FullWidth)
	if c.renders != 3 {
		t.Errorf("a tail compose of another row made %d renders in all, want 3", c.renders)
	}
	c.ComposeFrameRowRange(65, ula, dst, 1, 0, FullWidth)
	if c.renders != 4 {
		t.Errorf("a full compose made %d renders in all, want 4: it never reuses", c.renders)
	}
	l2.SetEnabled(false)
	c.ComposeFrameRowTail(65, ula, dst, 1, 100, FullWidth)
	l2.SetEnabled(true)
	c.ComposeFrameRowTail(65, ula, dst, 1, 100, FullWidth)
	// Each change to the set of enabled layers re-renders: off (5), on (6).
	if c.renders != 6 {
		t.Errorf("turning Layer 2 off and on made %d renders in all, want 6: the cache "+
			"must not be reused for a set of layers it was not rendered for", c.renders)
	}
}
