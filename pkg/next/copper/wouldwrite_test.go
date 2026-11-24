package copper

import (
	"math/rand"
	"testing"
)

// WouldWrite lets a render loop finish painting the pixels before a write
// while the old state is still in place. It must predict Step exactly where it
// says no: a missed write would paint pixels after the write under the old
// state. It must also not change anything, or the prediction alters what it
// predicts.

// TestWouldWriteMatchesStep drives random programs, including mode changes
// made by the list itself, VBL restarts, WAITs that release and park, NOOPs,
// wrap-around and budgets from zero to six clocks, and checks before every
// Step that WouldWrite agrees with the Wrote that Step then reports, and that
// asking twice gives the same answer.
func TestWouldWriteMatchesStep(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for prog := 0; prog < 200; prog++ {
		c := New()
		c.SetWritePtrLow(0)
		n := 4 + rng.Intn(40)
		for i := 0; i < n; i++ {
			var w uint16
			switch rng.Intn(4) {
			case 0: // MOVE to an ordinary register
				w = uint16(1+rng.Intn(0x5F))<<8 | uint16(rng.Intn(256))
			case 1: // WAIT on one of a few lines, at some column
				w = 0x8000 | uint16(rng.Intn(64))<<9 | uint16(rng.Intn(4))
			case 2: // NOOP
				w = uint16(rng.Intn(256))
			case 3: // MOVE NR$62, which changes the Copper's own mode
				w = 0x62<<8 | uint16(rng.Intn(4))<<6
			}
			c.WriteData(byte(w >> 8))
			c.WriteData(byte(w))
		}
		c.SetRegWriter(selfWriter{c})
		c.SetWritePtrHighAndMode(byte(1+rng.Intn(3)) << 6)
		line, hc := uint16(0), uint16(0)
		for step := 0; step < 2000; step++ {
			budget := rng.Intn(7)
			want := c.WouldWrite(line, hc, budget)
			if again := c.WouldWrite(line, hc, budget); again != want {
				t.Fatalf("prog %d step %d: WouldWrite changed its answer on a second call", prog, step)
			}
			c.Step(line, hc, budget)
			if c.Wrote() != want {
				t.Fatalf("prog %d step %d (line %d hcount %d budget %d): WouldWrite said %v, Step wrote %v",
					prog, step, line, hc, budget, want, c.Wrote())
			}
			hc += uint16(1 + rng.Intn(40))
			if hc >= 456 {
				hc = 0
				line = (line + 1) % 5
			}
		}
	}
}

// selfWriter routes NR$62 back to the Copper, as pkg/next/wire.go does.
type selfWriter struct{ c *Copper }

func (s selfWriter) WriteReg(reg, val byte) {
	if reg == 0x62 {
		s.c.SetWritePtrHighAndMode(val)
	}
}
