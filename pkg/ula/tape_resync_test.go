package ula

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMultiTAP writes a .tap with several blocks. Each input is flag+data; the
// XOR checksum and the 2-byte length prefix are added.
func writeMultiTAP(t *testing.T, blocks [][]byte) string {
	t.Helper()
	var out []byte
	for _, b := range blocks {
		var csum byte
		for _, x := range b {
			csum ^= x
		}
		body := append(append([]byte{}, b...), csum)
		out = append(out, byte(len(body)), byte(len(body)>>8))
		out = append(out, body...)
	}
	p := filepath.Join(t.TempDir(), "multi.tap")
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// After the fast-load trap consumes a block (NextBlock advances blockIdx), a
// real-time loader that takes over must play the block blockIdx now points to —
// regenerated from its pilot — NOT replay the previous block's leftover pulses.
// Regression for the "R Tape loading error" custom-loader games hit: block 0
// short, block 1 long; after trapping block 0, real-time must spend block 1's
// (long) duration, proving it played block 1 and not the short block 0 again.
func TestTrapRealtimeResyncPlaysCorrectBlock(t *testing.T) {
	long := append([]byte{0xFF}, make([]byte, 2000)...) // 2000-byte data block
	path := writeMultiTAP(t, [][]byte{
		{0xFF, 0x01}, // block 0: 1 data byte (short)
		long,         // block 1: 2000 data bytes (long)
		{0xFF, 0x02}, // block 2
	})
	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatal(err)
	}
	tp.Play()

	// Simulate the trap loading block 0 instantly.
	if tp.NextBlock() == nil {
		t.Fatal("NextBlock returned nil")
	}
	if tp.CurrentBlock() != 1 {
		t.Fatalf("blockIdx = %d after trap, want 1", tp.CurrentBlock())
	}

	// Drive the real-time player until it finishes block 1.
	const step = 4000
	var elapsed uint64
	for tp.CurrentBlock() == 1 && elapsed < 120_000_000 {
		tp.Update(step)
		elapsed += step
	}
	if tp.CurrentBlock() == 1 {
		t.Fatal("block 1 never finished — real-time player stuck")
	}
	// A 2000-byte block takes tens of millions of T-states; the 1-byte block 0
	// would take only ~7M. If we finished in under 15M, the desync bug replayed
	// block 0 instead of block 1.
	if elapsed < 15_000_000 {
		t.Errorf("block advanced after only %d T-states — desync replayed the short block 0 instead of block 1", elapsed)
	}
}
