package ula

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// LoadTZX used to parse every one of these block types and then throw the
// content away: 0x12 pure tone, 0x13 pulse sequence and 0x15 direct
// recording never reached the pulse stream at all, and 0x20 / 0x23 / 0x24 /
// 0x25 / 0x2A / 0x2B were consumed as no-ops so a tape's flow control was
// silently ignored. These tests pin the playback behaviour of each.

// loadTZXBytes writes a TZX image and loads it into a fresh player.
func loadTZXBytes(t *testing.T, b *tzxBuilder) *TapePlayer {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.tzx")
	if err := os.WriteFile(p, b.buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := NewTapePlayer()
	if err := tp.LoadTZX(p); err != nil {
		t.Fatalf("LoadTZX: %v", err)
	}
	return tp
}

// playbackOrder walks the loaded tape the way Update does — executing control
// blocks and stopping on each block that produces pulses — and returns the
// indices of the playable blocks in the order they are reached. Bounded so a
// mis-implemented jump or loop fails the test instead of hanging it.
func playbackOrder(tp *TapePlayer, limit int) []int {
	var order []int
	for len(order) < limit && tp.advance() {
		order = append(order, tp.blockIdx)
		tp.blockIdx++
	}
	return order
}

// TestTZXPureTonePlaysItsPulses pins block 0x12: pulse-length (word) +
// pulse-count (word), emitted verbatim with no pilot, sync or trailing pause.
func TestTZXPureTonePlaysItsPulses(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x12, 0xD0, 0x07, 0x05, 0x00) // 2000 T-states, 5 pulses
	tp := loadTZXBytes(t, b)

	if tp.BlockCount() != 1 {
		t.Fatalf("block count = %d, want 1", tp.BlockCount())
	}
	pulses := tp.generatePulses(tp.blocks[0])
	want := []uint32{2000, 2000, 2000, 2000, 2000}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Errorf("pulse %d = %d, want %d", i, pulses[i], want[i])
		}
	}
}

// TestTZXPulseSequencePlaysEachPulse pins block 0x13: a count byte followed
// by that many individual pulse lengths, played exactly as given.
func TestTZXPulseSequencePlaysEachPulse(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x13, 0x03, 0x64, 0x00, 0xC8, 0x00, 0x2C, 0x01) // 100, 200, 300
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	want := []uint32{100, 200, 300}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Errorf("pulse %d = %d, want %d", i, pulses[i], want[i])
		}
	}
}

// TestTZXDirectRecordingRunLengthsSamples pins block 0x15. Direct recording
// stores the EAR LEVEL of each sample, not pulse lengths, so a run of equal
// samples is one pulse whose length is runLength * T-states-per-sample.
func TestTZXDirectRecordingRunLengthsSamples(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x15,
		0x64, 0x00, // 100 T-states per sample
		0x00, 0x00, // no pause
		0x08,             // 8 used bits in the last byte
		0x01, 0x00, 0x00, // length = 1 byte
		0xCC, // 1100 1100 -> four runs of two samples
	)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	want := []uint32{200, 200, 200, 200}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Errorf("pulse %d = %d, want %d", i, pulses[i], want[i])
		}
	}
}

// TestTZXDirectRecordingHonoursUsedBits pins that the last byte's unused
// low bits are not played.
func TestTZXDirectRecordingHonoursUsedBits(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x15,
		0x64, 0x00, // 100 T-states per sample
		0x00, 0x00,
		0x02,             // only the top 2 bits of the last byte are valid
		0x01, 0x00, 0x00, // length = 1
		0xC0, // 11xx xxxx -> a single run of two samples
	)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	if len(pulses) != 1 || pulses[0] != 200 {
		t.Fatalf("pulses = %v, want [200]", pulses)
	}
}

// TestTZXPauseBlockEmitsSilence pins block 0x20 with a non-zero duration:
// a pure silence of that many milliseconds.
func TestTZXPauseBlockEmitsSilence(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x20, 0x64, 0x00) // 100 ms
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	var total uint64
	for _, p := range pulses {
		total += uint64(p)
	}
	if want := uint64(100 * 3500); total != want {
		t.Errorf("silence = %d T-states, want %d", total, want)
	}
}

// TestTZXPauseZeroStopsTape pins block 0x20 with duration 0, which the spec
// defines as "stop the tape".
func TestTZXPauseZeroStopsTape(t *testing.T) {
	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA})
	b.raw(0x20, 0x00, 0x00) // stop
	b.standardBlock([]byte{0xBB})
	tp := loadTZXBytes(t, b)

	if got := playbackOrder(tp, 5); len(got) != 1 || got[0] != 0 {
		t.Errorf("playback order = %v, want [0] (the stop block ends playback)", got)
	}
}

// TestTZXJumpSkipsBlock pins block 0x23. The jump value is relative to the
// jump block itself: 1 is the following block, 2 skips one.
func TestTZXJumpSkipsBlock(t *testing.T) {
	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA}) // 0
	b.raw(0x23, 0x02, 0x00)       // 1: jump +2 -> block 3
	b.standardBlock([]byte{0xBB}) // 2 (skipped)
	b.standardBlock([]byte{0xCC}) // 3
	tp := loadTZXBytes(t, b)

	got := playbackOrder(tp, 5)
	want := []int{0, 3}
	if len(got) != len(want) {
		t.Fatalf("playback order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("playback order = %v, want %v", got, want)
		}
	}
}

// TestTZXLoopRepeatsBlocks pins blocks 0x24 / 0x25: the blocks between them
// play loopCount times in total.
func TestTZXLoopRepeatsBlocks(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x24, 0x03, 0x00)       // 0: loop start, 3 iterations
	b.standardBlock([]byte{0xAA}) // 1
	b.raw(0x25)                   // 2: loop end
	b.standardBlock([]byte{0xBB}) // 3
	tp := loadTZXBytes(t, b)

	got := playbackOrder(tp, 8)
	want := []int{1, 1, 1, 3}
	if len(got) != len(want) {
		t.Fatalf("playback order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("playback order = %v, want %v", got, want)
		}
	}
}

// TestTZXStopIn48KOnlyStopsOn48K pins block 0x2A: it halts the tape on a 48K
// machine and is a no-op on a 128K one.
func TestTZXStopIn48KOnlyStopsOn48K(t *testing.T) {
	build := func() *tzxBuilder {
		b := newTZXBuilder()
		b.standardBlock([]byte{0xAA})       // 0
		b.raw(0x2A, 0x00, 0x00, 0x00, 0x00) // 1: stop if 48K
		b.standardBlock([]byte{0xBB})       // 2
		return b
	}

	t.Run("48K stops", func(t *testing.T) {
		tp := loadTZXBytes(t, build())
		tp.SetIs48K(func() bool { return true })
		if got := playbackOrder(tp, 5); len(got) != 1 || got[0] != 0 {
			t.Errorf("playback order = %v, want [0]", got)
		}
	})
	t.Run("128K continues", func(t *testing.T) {
		tp := loadTZXBytes(t, build())
		tp.SetIs48K(func() bool { return false })
		got := playbackOrder(tp, 5)
		if len(got) != 2 || got[0] != 0 || got[1] != 2 {
			t.Errorf("playback order = %v, want [0 2]", got)
		}
	})
}

// TestTZXSetSignalLevelSetsEarBit pins block 0x2B, which forces the EAR line
// to a given level rather than toggling it.
func TestTZXSetSignalLevelSetsEarBit(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x2B, 0x01, 0x00, 0x00, 0x00, 0x01) // level = 1
	b.standardBlock([]byte{0xAA})
	tp := loadTZXBytes(t, b)

	tp.earBit = false
	if !tp.advance() {
		t.Fatal("advance stopped at the signal-level block")
	}
	if !tp.earBit {
		t.Error("EAR bit = false after a set-signal-level 1 block, want true")
	}
}

// TestTZXControlBlocksAreNotFastLoadable guards the fast-load ROM trap: it
// hands the guest raw block bytes, so it must walk past the control blocks
// rather than feed them to the loader as data.
func TestTZXControlBlocksAreNotFastLoadable(t *testing.T) {
	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA})
	b.raw(0x20, 0x64, 0x00) // pause
	b.raw(0x24, 0x02, 0x00) // loop start
	b.standardBlock([]byte{0xBB})
	tp := loadTZXBytes(t, b)

	first := tp.NextBlock()
	if len(first) != 1 || first[0] != 0xAA {
		t.Fatalf("first fast-load block = %v, want [0xAA]", first)
	}
	second := tp.NextBlock()
	if len(second) != 1 || second[0] != 0xBB {
		t.Fatalf("second fast-load block = %v, want [0xBB]", second)
	}
}

// TestTZXLongRunIsOnePulse pins that a same-level run is never split. The
// player toggles the EAR line at every pulse boundary, so emitting a long run
// as several chunks would come back as alternating levels instead of the one
// level the samples recorded. 900 samples of 100 T-states is 90000 T — past
// what a uint16 pulse could have held.
func TestTZXLongRunIsOnePulse(t *testing.T) {
	const samples = 904 // 113 bytes of 0xFF
	body := []byte{0x15, 0x64, 0x00, 0x00, 0x00, 0x08}
	body = append(body, byte(samples/8), 0x00, 0x00)
	for i := 0; i < samples/8; i++ {
		body = append(body, 0xFF)
	}
	b := newTZXBuilder()
	b.raw(body...)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	if len(pulses) != 1 {
		t.Fatalf("a single %d-sample run produced %d pulses, want 1 (a split run toggles the EAR line mid-run)",
			samples, len(pulses))
	}
	if want := uint32(samples * 100); pulses[0] != want {
		t.Errorf("run length = %d T-states, want %d", pulses[0], want)
	}
}

// TestTZXLongPauseIsOnePulse pins the same property for a silence: a
// one-second gap holds one level throughout and must not be chunked into
// dozens of spurious edges.
func TestTZXLongPauseIsOnePulse(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x20, 0xE8, 0x03) // 1000 ms = 3,500,000 T-states
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	if len(pulses) != 1 {
		t.Fatalf("a 1s pause produced %d pulses, want 1", len(pulses))
	}
	if want := uint32(1000 * 3500); pulses[0] != want {
		t.Errorf("pause = %d T-states, want %d", pulses[0], want)
	}
}

// TestNextBlockTerminatesOnACyclicTape guards the fast-load trap against a
// tape whose flow control loops forever over blocks that carry no data — a
// jump backwards over a pause, say. NextBlock must give up and report the
// tape spent rather than spinning.
func TestNextBlockTerminatesOnACyclicTape(t *testing.T) {
	b := newTZXBuilder()
	b.raw(0x20, 0x64, 0x00) // 0: pause (playable, but no data for the trap)
	b.raw(0x23, 0xFF, 0xFF) // 1: jump -1, back to the pause
	tp := loadTZXBytes(t, b)

	done := make(chan []byte, 1)
	go func() { done <- tp.NextBlock() }()
	select {
	case got := <-done:
		if got != nil {
			t.Errorf("NextBlock = %v, want nil (no data block on this tape)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NextBlock did not terminate on a cyclic tape")
	}
}
