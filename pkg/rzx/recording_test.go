package rzx

import (
	"bytes"
	"errors"
	"testing"
)

func TestRecordingStoreFrameRequiresActiveInput(t *testing.T) {
	r := NewRecording()
	if err := r.StoreFrame(10); !errors.Is(err, ErrNoActiveInput) {
		t.Errorf("StoreFrame without StartInput: err = %v, want ErrNoActiveInput", err)
	}
}

func TestRecordingFrameAccumulation(t *testing.T) {
	r := NewRecording()
	r.StartInput(0)

	// Frame 1: two IN reads, then store.
	r.RecordIN(0x10)
	r.RecordIN(0x20)
	if err := r.StoreFrame(100); err != nil {
		t.Fatalf("StoreFrame 1: %v", err)
	}

	// Frame 2: one IN read.
	r.RecordIN(0x30)
	if err := r.StoreFrame(200); err != nil {
		t.Fatalf("StoreFrame 2: %v", err)
	}

	// Frame 3: zero IN reads.
	if err := r.StoreFrame(50); err != nil {
		t.Fatalf("StoreFrame 3: %v", err)
	}

	frames := r.file.Blocks[0].Input.Frames
	if len(frames) != 3 {
		t.Fatalf("frame count = %d, want 3", len(frames))
	}
	if !bytes.Equal(frames[0].InBytes, []byte{0x10, 0x20}) {
		t.Errorf("frame 0 InBytes = %v, want [10 20]", frames[0].InBytes)
	}
	if frames[0].Instructions != 100 {
		t.Errorf("frame 0 Instructions = %d, want 100", frames[0].Instructions)
	}
	if !bytes.Equal(frames[1].InBytes, []byte{0x30}) {
		t.Errorf("frame 1 InBytes = %v, want [30]", frames[1].InBytes)
	}
	if frames[2].InBytes != nil {
		t.Errorf("frame 2 InBytes = %v, want nil", frames[2].InBytes)
	}
}

func TestRecordingRepeatLastCollapsing(t *testing.T) {
	r := NewRecording()
	r.StartInput(0)

	// Two frames with identical IN sequences — second should
	// collapse to RepeatLast.
	r.RecordIN(0xAA)
	r.RecordIN(0xBB)
	mustStoreFrame(t, r, 100)

	r.RecordIN(0xAA)
	r.RecordIN(0xBB)
	mustStoreFrame(t, r, 101)

	// Third frame with different bytes — should NOT collapse.
	r.RecordIN(0xCC)
	mustStoreFrame(t, r, 102)

	// Fourth frame matches the THIRD (most recent non-repeated).
	r.RecordIN(0xCC)
	mustStoreFrame(t, r, 103)

	frames := r.file.Blocks[0].Input.Frames
	if len(frames) != 4 {
		t.Fatalf("frame count = %d, want 4", len(frames))
	}
	if frames[0].RepeatLast {
		t.Error("frame 0: RepeatLast set, want false")
	}
	if !frames[1].RepeatLast {
		t.Error("frame 1: RepeatLast clear, want true")
	}
	if frames[1].InBytes != nil {
		t.Errorf("frame 1: InBytes = %v, want nil", frames[1].InBytes)
	}
	if frames[2].RepeatLast {
		t.Error("frame 2: RepeatLast set, want false")
	}
	if !frames[3].RepeatLast {
		t.Error("frame 3: RepeatLast clear, want true")
	}
}

func TestRecordingAddSnapClosesInput(t *testing.T) {
	r := NewRecording()
	r.StartInput(0)
	r.RecordIN(0x01)
	mustStoreFrame(t, r, 10)
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0xDE, 0xAD}}, false)

	// Active input should be cleared.
	if err := r.StoreFrame(20); !errors.Is(err, ErrNoActiveInput) {
		t.Errorf("StoreFrame after AddSnap: err = %v, want ErrNoActiveInput", err)
	}

	// Block list: input, snapshot.
	blocks := r.file.Blocks
	if len(blocks) != 2 || blocks[0].Type != BlockInput || blocks[1].Type != BlockSnapshot {
		t.Errorf("block sequence = %v", blockTypes(blocks))
	}
}

func TestRecordingPlaybackRoundTrip(t *testing.T) {
	// End-to-end: record some frames, serialise, parse, play back,
	// and verify the IN bytes come out in the same order.
	r := NewRecording()
	r.AddCreator(&CreatorBlock{Program: "zx_go"})
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{1, 2, 3}}, false)
	r.StartInput(1000)
	r.RecordIN(0x11)
	r.RecordIN(0x22)
	mustStoreFrame(t, r, 100)
	r.RecordIN(0x33)
	mustStoreFrame(t, r, 200)

	data, err := r.Bytes(WriteOptions{Compress: true})
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	parsed, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	p := NewPlayback(parsed)
	snap, err := p.Start(0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if snap == nil || !bytes.Equal(snap.Data, []byte{1, 2, 3}) {
		t.Fatalf("snap = %+v, want Data [1 2 3]", snap)
	}

	// Frame 0: instruction count + 2 IN bytes.
	if got := p.Instructions(); got != 100 {
		t.Errorf("frame 0 Instructions = %d, want 100", got)
	}
	b1, _ := p.NextByte()
	b2, _ := p.NextByte()
	if b1 != 0x11 || b2 != 0x22 {
		t.Errorf("frame 0 bytes = [%02X %02X], want [11 22]", b1, b2)
	}
	if _, err := p.Frame(); err != nil {
		t.Fatalf("Frame: %v", err)
	}

	// Frame 1.
	if got := p.Instructions(); got != 200 {
		t.Errorf("frame 1 Instructions = %d, want 200", got)
	}
	b3, _ := p.NextByte()
	if b3 != 0x33 {
		t.Errorf("frame 1 byte = %02X, want 33", b3)
	}
	if _, err := p.Frame(); !errors.Is(err, ErrPlaybackFinished) {
		t.Errorf("after final frame: err = %v, want ErrPlaybackFinished", err)
	}
}

func TestRecordingRollback(t *testing.T) {
	r := NewRecording()
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}, false)
	r.StartInput(0)
	r.RecordIN(0xAA)
	mustStoreFrame(t, r, 10)
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x02}}, true)
	r.StartInput(100)
	r.RecordIN(0xBB)
	mustStoreFrame(t, r, 20)

	snap, err := r.Rollback()
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if snap.Data[0] != 0x02 {
		t.Errorf("Rollback returned snap data %v, want [0x02]", snap.Data)
	}

	// File should now end at the autosave snapshot — no input
	// blocks after it.
	blocks := r.file.Blocks
	if blocks[len(blocks)-1].Type != BlockSnapshot {
		t.Errorf("after Rollback, last block type = %02X, want BlockSnapshot", blocks[len(blocks)-1].Type)
	}
}

func TestRecordingFinaliseMergesInputBlocks(t *testing.T) {
	r := NewRecording()
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}, false)
	r.StartInput(0)
	r.RecordIN(0xAA)
	mustStoreFrame(t, r, 10)
	// Insert intermediate (autosave) snapshot — Finalise should drop it.
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x02}}, true)
	r.StartInput(100)
	r.RecordIN(0xBB)
	mustStoreFrame(t, r, 20)

	r.Finalise()

	// Expect: 1 snap + 1 merged input (containing both frames).
	blocks := r.file.Blocks
	wantTypes := []BlockType{BlockSnapshot, BlockInput}
	if got := blockTypes(blocks); !typesEqual(got, wantTypes) {
		t.Fatalf("after Finalise, types = %v, want %v", got, wantTypes)
	}
	if got := len(blocks[1].Input.Frames); got != 2 {
		t.Errorf("merged input frame count = %d, want 2", got)
	}
}

// TestRecordingFinaliseMergeWithEmptyBlockKeepsNonRepeatInBounds covers
// merging an input block that has zero frames (StartInput called with
// no StoreFrame before the next block) into a preceding non-empty one.
// The merge must leave the survivor's frames untouched — including its
// nonRepeat index, which must stay a valid index into Frames so a later
// StoreFrame's repeat-collapsing check doesn't index past the end.
func TestRecordingFinaliseMergeWithEmptyBlockKeepsNonRepeatInBounds(t *testing.T) {
	r := NewRecording()
	r.StartInput(0)
	r.RecordIN(0xAA)
	mustStoreFrame(t, r, 10) // one frame, non-repeat, in the first block

	r.StartInput(100) // second input block, left with zero frames

	r.Finalise()

	blocks := r.file.Blocks
	if len(blocks) != 1 || blocks[0].Type != BlockInput {
		t.Fatalf("after Finalise, blocks = %+v, want a single merged input block", blocks)
	}
	merged := blocks[0].Input
	if merged.nonRepeat >= len(merged.Frames) {
		t.Fatalf("nonRepeat = %d, out of bounds for %d frames", merged.nonRepeat, len(merged.Frames))
	}

	// Resume recording against the merged block: StoreFrame must not
	// panic when its repeat-collapsing check consults nonRepeat.
	r.activeInput = merged
	r.RecordIN(0xBB)
	if err := r.StoreFrame(20); err != nil {
		t.Fatalf("StoreFrame after merge: %v", err)
	}
}

func TestRecordingSnapshotPoints(t *testing.T) {
	r := NewRecording()
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}, false)
	r.StartInput(0)
	mustStoreFrame(t, r, 1)
	mustStoreFrame(t, r, 1)
	mustStoreFrame(t, r, 1)
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x02}}, true)
	r.StartInput(0)
	mustStoreFrame(t, r, 1)
	mustStoreFrame(t, r, 1)

	pts := r.SnapshotPoints()
	wantPts := []int{0, 3}
	if len(pts) != len(wantPts) {
		t.Fatalf("SnapshotPoints = %v, want %v", pts, wantPts)
	}
	for i := range pts {
		if pts[i] != wantPts[i] {
			t.Errorf("point %d = %d, want %d", i, pts[i], wantPts[i])
		}
	}
}

func TestAutosaveDueAfterInterval(t *testing.T) {
	r := NewRecording()
	r.AutosavesEnabled = true
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}, false)
	r.StartInput(0)
	for i := 0; i < AutosaveInterval-1; i++ {
		_ = r.StoreFrame(1)
	}
	if r.AutosaveDue() {
		t.Errorf("AutosaveDue after %d frames: true, want false", AutosaveInterval-1)
	}
	_ = r.StoreFrame(1)
	if !r.AutosaveDue() {
		t.Errorf("AutosaveDue after %d frames: false, want true", AutosaveInterval)
	}
}

func TestAutosaveDueDisabledByDefault(t *testing.T) {
	r := NewRecording()
	r.StartInput(0)
	for i := 0; i < AutosaveInterval*2; i++ {
		_ = r.StoreFrame(1)
	}
	if r.AutosaveDue() {
		t.Error("AutosaveDue with AutosavesEnabled=false: true, want false")
	}
}

func TestAddAutosaveResetsCounter(t *testing.T) {
	r := NewRecording()
	r.AutosavesEnabled = true
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}, false)
	r.StartInput(0)
	for i := 0; i < AutosaveInterval; i++ {
		_ = r.StoreFrame(1)
	}
	r.AddAutosave(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x02}}, 100)
	if r.AutosaveDue() {
		t.Error("AutosaveDue immediately after AddAutosave: true, want false")
	}
	// AddAutosave should also have re-opened an active input block
	// so subsequent StoreFrame calls don't error.
	if err := r.StoreFrame(1); err != nil {
		t.Errorf("StoreFrame after AddAutosave: %v", err)
	}
}

func TestPruneAutosavesDropsRedundantTier(t *testing.T) {
	// FUSE pruning walks newest-to-oldest. The predicate fires
	// when the *newer* (save1) is at a tier boundary AND the
	// *older* (save2) is less than 2× as old, in which case the
	// newer one is dropped. (Intuition: the older snapshot is
	// "close enough" to subsume the newer tier-aligned one.)
	//
	// Layout:
	//   Autosave 1 (older) — age 1250 frames
	//   Autosave 2 (newer) — age  750 frames (= tier-15s)
	//   1250 < 2*750 = 1500 → predicate fires, drop Autosave 2.
	r := NewRecording()
	r.file.Blocks = []Block{
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Automatic: false}}, // initial
		{Type: BlockInput, Input: &InputBlock{Frames: makeFrames(750)}},
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Automatic: true}}, // captured at frame 750, age = 1250
		{Type: BlockInput, Input: &InputBlock{Frames: makeFrames(500)}},
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Automatic: true}}, // captured at frame 1250, age = 750
		{Type: BlockInput, Input: &InputBlock{Frames: makeFrames(750)}},
	}
	// total = 2000, so autosave 1 age = 2000 - 750 = 1250,
	// autosave 2 age = 2000 - 1250 = 750.
	r.pruneAutosaves()

	autosaveCount := 0
	for _, b := range r.file.Blocks {
		if b.Type == BlockSnapshot && b.Snapshot.Automatic {
			autosaveCount++
		}
	}
	if autosaveCount != 1 {
		t.Errorf("after pruning: %d automatic snapshots remain, want 1", autosaveCount)
	}
}

func TestPruneAutosavesKeepsNonTierAutosaves(t *testing.T) {
	// All autosaves have ages that don't fall on a tier — pruning
	// should leave the list untouched.
	r := NewRecording()
	r.file.Blocks = []Block{
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Automatic: false}},
		{Type: BlockInput, Input: &InputBlock{Frames: makeFrames(100)}},
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Automatic: true}},
		{Type: BlockInput, Input: &InputBlock{Frames: makeFrames(200)}},
		{Type: BlockSnapshot, Snapshot: &SnapshotBlock{Format: SnapshotFormatSZX, Automatic: true}},
		{Type: BlockInput, Input: &InputBlock{Frames: makeFrames(50)}},
	}
	before := countAutosaves(r.file)
	r.pruneAutosaves()
	after := countAutosaves(r.file)
	if before != after {
		t.Errorf("pruning non-tier autosaves: before=%d after=%d, want equal", before, after)
	}
}

func TestCompetitionRollbackForbidden(t *testing.T) {
	r := NewRecording()
	r.CompetitionMode = true
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}, false)
	if _, err := r.Rollback(); !errors.Is(err, ErrCompetitionRollbackForbidden) {
		t.Errorf("Rollback in competition mode: err = %v, want ErrCompetitionRollbackForbidden", err)
	}
	if _, err := r.RollbackTo(0); !errors.Is(err, ErrCompetitionRollbackForbidden) {
		t.Errorf("RollbackTo in competition mode: err = %v, want ErrCompetitionRollbackForbidden", err)
	}
}

func TestCompetitionSpeedTolerance(t *testing.T) {
	r := NewRecording()
	r.CompetitionMode = true

	// Within tolerance — should pass.
	for _, pct := range []float64{100, 95, 105, 99.9, 100.1} {
		if err := r.CheckCompetitionSpeed(pct); err != nil {
			t.Errorf("CheckCompetitionSpeed(%.1f): err = %v, want nil", pct, err)
		}
	}
	// Outside tolerance — should fail.
	for _, pct := range []float64{94.9, 105.1, 50, 200} {
		if err := r.CheckCompetitionSpeed(pct); !errors.Is(err, ErrCompetitionSpeedExceeded) {
			t.Errorf("CheckCompetitionSpeed(%.1f): err = %v, want ErrCompetitionSpeedExceeded", pct, err)
		}
	}
}

func TestCompetitionSpeedNoOpWithoutMode(t *testing.T) {
	r := NewRecording()
	r.CompetitionMode = false
	if err := r.CheckCompetitionSpeed(50); err != nil {
		t.Errorf("CheckCompetitionSpeed without competition mode: err = %v, want nil", err)
	}
}

func TestCompetitionBytesSetsSignedFlag(t *testing.T) {
	r := NewRecording()
	r.CompetitionMode = true
	r.AddSnap(&SnapshotBlock{Format: SnapshotFormatSZX, Data: []byte{0x01}}, false)
	r.StartInput(0)
	mustStoreFrame(t, r, 1)

	// Caller passes Signed=false; competition mode should override.
	data, err := r.Bytes(WriteOptions{Signed: false})
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if data[5] != versionMinorSign {
		t.Errorf("competition Bytes: minor version = %d, want %d", data[5], versionMinorSign)
	}
	if data[6]&0x01 == 0 {
		t.Errorf("competition Bytes: signed flag not set in header (got %02X)", data[6])
	}
}

func makeFrames(n int) []Frame {
	frames := make([]Frame, n)
	for i := range frames {
		frames[i].Instructions = 1
	}
	return frames
}

func countAutosaves(f *File) int {
	n := 0
	for _, b := range f.Blocks {
		if b.Type == BlockSnapshot && b.Snapshot.Automatic {
			n++
		}
	}
	return n
}

func blockTypes(blocks []Block) []BlockType {
	out := make([]BlockType, len(blocks))
	for i, b := range blocks {
		out[i] = b.Type
	}
	return out
}

func typesEqual(a, b []BlockType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mustStoreFrame is a test helper that calls StoreFrame and fails the
// test on any error. Used to keep test setup uncluttered while still
// satisfying errcheck.
func mustStoreFrame(t *testing.T, r *Recording, instructions uint16) {
	t.Helper()
	if err := r.StoreFrame(instructions); err != nil {
		t.Fatalf("StoreFrame(%d): %v", instructions, err)
	}
}
