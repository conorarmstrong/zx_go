package rzx

import (
	"errors"
	"testing"
)

// makePlaybackFile builds a tiny RZX with one snapshot block followed
// by an IRB containing three frames: a normal one with two IN bytes, a
// repeat-last frame, and a frame with no IN bytes.
func makePlaybackFile() *File {
	return &File{
		Blocks: []Block{
			{
				Type: BlockSnapshot,
				Snapshot: &SnapshotBlock{
					Format: SnapshotFormatSZX,
					Data:   []byte{0xDE, 0xAD},
				},
			},
			{
				Type: BlockInput,
				Input: &InputBlock{
					Tstates: 1234,
					Frames: []Frame{
						{Instructions: 100, InBytes: []byte{0xAA, 0xBB}},
						{Instructions: 110, RepeatLast: true},
						{Instructions: 50, InBytes: nil},
					},
				},
			},
		},
	}
}

func TestPlaybackStartReturnsSnapshot(t *testing.T) {
	p := NewPlayback(makePlaybackFile())
	snap, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if snap == nil {
		t.Fatal("Start returned nil snapshot, want SnapshotBlock")
	}
	if snap.Format != SnapshotFormatSZX {
		t.Errorf("snap.Format = %q, want %q", snap.Format, SnapshotFormatSZX)
	}
}

func TestPlaybackStartNoSnapshot(t *testing.T) {
	// IRB with no preceding snapshot — Start should still succeed
	// and return nil for the snapshot pointer.
	file := &File{
		Blocks: []Block{
			{Type: BlockInput, Input: &InputBlock{Frames: []Frame{
				{Instructions: 1, InBytes: []byte{0x01}},
			}}},
		},
	}
	p := NewPlayback(file)
	snap, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if snap != nil {
		t.Errorf("Start returned snap=%+v, want nil", snap)
	}
}

func TestPlaybackInstructionsAndTstates(t *testing.T) {
	p := NewPlayback(makePlaybackFile())
	if _, err := p.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := p.Instructions(); got != 100 {
		t.Errorf("frame 0 Instructions = %d, want 100", got)
	}
	if got := p.Tstates(); got != 1234 {
		t.Errorf("Tstates = %d, want 1234", got)
	}
}

func TestPlaybackNextByteSequence(t *testing.T) {
	p := NewPlayback(makePlaybackFile())
	if _, err := p.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b1, err := p.NextByte()
	if err != nil || b1 != 0xAA {
		t.Errorf("byte 1: got %02X (err=%v), want 0xAA", b1, err)
	}
	b2, err := p.NextByte()
	if err != nil || b2 != 0xBB {
		t.Errorf("byte 2: got %02X (err=%v), want 0xBB", b2, err)
	}
	// Third call should underflow.
	if _, err := p.NextByte(); !errors.Is(err, ErrPlaybackUnderflow) {
		t.Errorf("byte 3: err = %v, want ErrPlaybackUnderflow", err)
	}
}

func TestPlaybackFrameAdvance(t *testing.T) {
	p := NewPlayback(makePlaybackFile())
	if _, err := p.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Frame 0 — consume both IN bytes, then advance.
	_, _ = p.NextByte()
	_, _ = p.NextByte()
	if _, err := p.Frame(); err != nil {
		t.Fatalf("Frame 0 → 1: %v", err)
	}
	if got := p.Instructions(); got != 110 {
		t.Errorf("after advance: frame 1 Instructions = %d, want 110", got)
	}

	// Frame 1 is RepeatLast — its dataFrame still points at frame 0,
	// so NextByte should yield the same two bytes.
	b1, _ := p.NextByte()
	b2, _ := p.NextByte()
	if b1 != 0xAA || b2 != 0xBB {
		t.Errorf("RepeatLast frame: got [%02X %02X], want [AA BB]", b1, b2)
	}
	if _, err := p.Frame(); err != nil {
		t.Fatalf("Frame 1 → 2: %v", err)
	}
	if got := p.Instructions(); got != 50 {
		t.Errorf("frame 2 Instructions = %d, want 50", got)
	}

	// Frame 2 has zero IN bytes — Frame() with no NextByte calls
	// should still succeed and then report ErrPlaybackFinished.
	if _, err := p.Frame(); !errors.Is(err, ErrPlaybackFinished) {
		t.Errorf("after final frame: err = %v, want ErrPlaybackFinished", err)
	}
	if !p.Finished() {
		t.Error("Finished() = false after end-of-stream")
	}
}

func TestPlaybackDesyncDetected(t *testing.T) {
	// Frame has 2 recorded IN bytes; if the CPU only consumes 1
	// and we call Frame, that's a desync.
	p := NewPlayback(makePlaybackFile())
	if _, err := p.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, _ = p.NextByte() // only one of the two
	if _, err := p.Frame(); err == nil {
		t.Error("Frame after partial IN consumption: want desync error, got nil")
	}
}

func TestPlaybackMultipleInputBlocks(t *testing.T) {
	// Two IRBs separated by an automatic snapshot — Frame should
	// return that snapshot when crossing the boundary so the
	// driver can re-apply it before continuing.
	file := &File{
		Blocks: []Block{
			{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}},
			{Type: BlockInput, Input: &InputBlock{Frames: []Frame{
				{Instructions: 10, InBytes: []byte{0x11}},
			}}},
			{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x02}, Automatic: true}},
			{Type: BlockInput, Input: &InputBlock{Frames: []Frame{
				{Instructions: 20, InBytes: []byte{0x22}},
			}}},
		},
	}
	p := NewPlayback(file)
	first, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if first == nil || first.Data[0] != 0x01 {
		t.Fatalf("first snapshot data = %v, want [0x01]", first.Data)
	}

	// Frame 0 of IRB 0.
	_, _ = p.NextByte()
	mid, err := p.Frame()
	if err != nil {
		t.Fatalf("crossing IRB boundary: %v", err)
	}
	if mid == nil || mid.Data[0] != 0x02 {
		t.Fatalf("intermediate snapshot data = %v, want [0x02]", mid)
	}
	if got := p.Instructions(); got != 20 {
		t.Errorf("after boundary: Instructions = %d, want 20", got)
	}
}

func TestPlaybackStartUnknownIndex(t *testing.T) {
	p := NewPlayback(makePlaybackFile())
	if _, err := p.Start(5); err == nil {
		t.Error("Start(5) on file with 1 IRB: want error, got nil")
	}
}
