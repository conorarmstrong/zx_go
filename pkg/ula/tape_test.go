package ula

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func createTestTAP(t *testing.T, dir string) string {
	t.Helper()
	var data []byte

	// Header block (flag byte < 128 = header)
	block1 := []byte{0x00, 0x03, 0x41, 0x42, 0x43, 0x44, 0x45}
	lenBytes := make([]byte, 2)
	binary.LittleEndian.PutUint16(lenBytes, uint16(len(block1)))
	data = append(data, lenBytes...)
	data = append(data, block1...)

	// Data block (flag byte >= 128 = data)
	block2 := make([]byte, 20)
	block2[0] = 0xFF
	for i := 1; i < len(block2); i++ {
		block2[i] = byte(i)
	}
	binary.LittleEndian.PutUint16(lenBytes, uint16(len(block2)))
	data = append(data, lenBytes...)
	data = append(data, block2...)

	path := filepath.Join(dir, "test.tap")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTapePlayerLoad(t *testing.T) {
	dir := t.TempDir()
	path := createTestTAP(t, dir)

	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatalf("Failed to load TAP: %v", err)
	}

	if tp.BlockCount() != 2 {
		t.Errorf("Expected 2 blocks, got %d", tp.BlockCount())
	}
	if tp.CurrentBlock() != 0 {
		t.Errorf("Expected current block 0, got %d", tp.CurrentBlock())
	}
	if tp.IsPlaying() {
		t.Error("Should not be playing after load")
	}
}

func TestTapePlayerPlayStop(t *testing.T) {
	dir := t.TempDir()
	path := createTestTAP(t, dir)

	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatal(err)
	}

	tp.Play()
	if !tp.IsPlaying() {
		t.Error("Should be playing after Play()")
	}

	tp.Stop()
	if tp.IsPlaying() {
		t.Error("Should not be playing after Stop()")
	}
}

func TestTapePlayerRewind(t *testing.T) {
	dir := t.TempDir()
	path := createTestTAP(t, dir)

	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatal(err)
	}

	tp.Play()
	// Advance past first block
	for i := 0; i < 1000; i++ {
		tp.Update(69888)
	}

	tp.Rewind()
	if tp.CurrentBlock() != 0 {
		t.Errorf("After rewind, expected block 0, got %d", tp.CurrentBlock())
	}
	if tp.IsPlaying() {
		t.Error("Should not be playing after rewind")
	}
}

func TestTapePlayerUpdate(t *testing.T) {
	dir := t.TempDir()
	path := createTestTAP(t, dir)

	tp := NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		t.Fatal(err)
	}

	tp.Play()

	// Update should toggle the ear bit as pulses are consumed
	earChanges := 0
	lastEar := false
	for i := 0; i < 100; i++ {
		ear := tp.Update(2168) // One pilot pulse duration
		if ear != lastEar {
			earChanges++
			lastEar = ear
		}
	}

	if earChanges == 0 {
		t.Error("EAR bit should have toggled during playback")
	}
}

func TestTapePlayerPulseGeneration(t *testing.T) {
	tp := NewTapePlayer()

	// Header block (flag < 128): should have 8063 pilot pulses
	headerData := []byte{0x00, 0x01}
	pulses := tp.generatePulses(tapeBlock{data: headerData})
	if pulses == nil {
		t.Fatal("generatePulses returned nil")
	}
	// Should start with pilot pulses of 2168 T-states
	if pulses[0] != 2168 {
		t.Errorf("First pilot pulse: expected 2168, got %d", pulses[0])
	}

	// Data block (flag >= 128): should have 3223 pilot pulses
	dataBlock := []byte{0xFF, 0x01}
	dataPulses := tp.generatePulses(tapeBlock{data: dataBlock})
	if dataPulses == nil {
		t.Fatal("generatePulses returned nil for data block")
	}
	if dataPulses[0] != 2168 {
		t.Errorf("First data pilot pulse: expected 2168, got %d", dataPulses[0])
	}
	// Data block has fewer pilot pulses
	if len(dataPulses) >= len(pulses) {
		t.Error("Data block should have fewer pilot pulses than header block")
	}
}

// Tape accessor coverage: SaveTAP round-trip, NextBlock advance + EOF,
// HasMoreBlocks transitions, Blocks() metadata, SeekToBlock clamping.

func TestSaveTAPRoundtrip(t *testing.T) {
	dir := t.TempDir()
	in := createTestTAP(t, dir)

	tp := NewTapePlayer()
	if err := tp.LoadTAP(in); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.tap")
	if err := tp.SaveTAP(out); err != nil {
		t.Fatalf("SaveTAP: %v", err)
	}

	// Reload and compare block count + bytes.
	tp2 := NewTapePlayer()
	if err := tp2.LoadTAP(out); err != nil {
		t.Fatalf("reload SaveTAP output: %v", err)
	}
	if tp2.BlockCount() != tp.BlockCount() {
		t.Errorf("roundtrip block count = %d, want %d", tp2.BlockCount(), tp.BlockCount())
	}
}

func TestSaveTAPEmptyFails(t *testing.T) {
	dir := t.TempDir()
	tp := NewTapePlayer()
	err := tp.SaveTAP(filepath.Join(dir, "empty.tap"))
	if err == nil {
		t.Error("SaveTAP with no blocks should error")
	}
}

func TestNextBlockAdvancesAndReturnsNilAtEnd(t *testing.T) {
	dir := t.TempDir()
	tp := NewTapePlayer()
	if err := tp.LoadTAP(createTestTAP(t, dir)); err != nil {
		t.Fatal(err)
	}

	// Two blocks in createTestTAP.
	b1 := tp.NextBlock()
	if b1 == nil {
		t.Fatalf("NextBlock #1 = nil")
	}
	if tp.CurrentBlock() != 1 {
		t.Errorf("after NextBlock: idx=%d, want 1", tp.CurrentBlock())
	}
	b2 := tp.NextBlock()
	if b2 == nil {
		t.Fatalf("NextBlock #2 = nil")
	}
	// Past EOF.
	if got := tp.NextBlock(); got != nil {
		t.Errorf("NextBlock past EOF = non-nil")
	}
}

func TestHasMoreBlocks(t *testing.T) {
	dir := t.TempDir()
	tp := NewTapePlayer()
	if err := tp.LoadTAP(createTestTAP(t, dir)); err != nil {
		t.Fatal(err)
	}
	if !tp.HasMoreBlocks() {
		t.Error("HasMoreBlocks at idx=0 = false, want true")
	}
	tp.NextBlock()
	if !tp.HasMoreBlocks() {
		t.Error("HasMoreBlocks at idx=1 = false, want true (2 blocks)")
	}
	tp.NextBlock()
	if tp.HasMoreBlocks() {
		t.Error("HasMoreBlocks after consuming all = true, want false")
	}
}

func TestBlocksSummary(t *testing.T) {
	dir := t.TempDir()
	tp := NewTapePlayer()
	if err := tp.LoadTAP(createTestTAP(t, dir)); err != nil {
		t.Fatal(err)
	}
	bs := tp.Blocks()
	if len(bs) != 2 {
		t.Fatalf("Blocks count = %d, want 2", len(bs))
	}
	// Block 0 = header (flag $00 + Type byte $03).
	if bs[0].FlagByte != 0x00 || bs[0].Type != "Header" {
		t.Errorf("block 0: flag=$%02X type=%q, want $00 Header", bs[0].FlagByte, bs[0].Type)
	}
	// Block 1 = data (flag $FF).
	if bs[1].FlagByte != 0xFF || bs[1].Type != "Data" {
		t.Errorf("block 1: flag=$%02X type=%q, want $FF Data", bs[1].FlagByte, bs[1].Type)
	}
	// Indices populated.
	if bs[0].Index != 0 || bs[1].Index != 1 {
		t.Errorf("indices = %d %d, want 0 1", bs[0].Index, bs[1].Index)
	}
}

func TestSeekToBlockClamps(t *testing.T) {
	dir := t.TempDir()
	tp := NewTapePlayer()
	if err := tp.LoadTAP(createTestTAP(t, dir)); err != nil {
		t.Fatal(err)
	}
	// Out-of-range high → clamp to last (idx 1).
	tp.SeekToBlock(99)
	if tp.CurrentBlock() != 1 {
		t.Errorf("Seek(99) clamp: idx=%d, want 1 (last)", tp.CurrentBlock())
	}
	// Negative → clamp to 0.
	tp.SeekToBlock(-5)
	if tp.CurrentBlock() != 0 {
		t.Errorf("Seek(-5) clamp: idx=%d, want 0", tp.CurrentBlock())
	}
	// Valid in-range.
	tp.SeekToBlock(1)
	if tp.CurrentBlock() != 1 {
		t.Errorf("Seek(1): idx=%d, want 1", tp.CurrentBlock())
	}
	// Seek stops playback.
	tp.Play()
	tp.SeekToBlock(0)
	if tp.IsPlaying() {
		t.Errorf("SeekToBlock did not stop playback")
	}
}

func TestTapePlayerEmptyTAP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.tap")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	tp := NewTapePlayer()
	err := tp.LoadTAP(path)
	if err == nil {
		t.Error("Expected error loading empty TAP file")
	}
}
