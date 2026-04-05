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
	pulses := tp.generatePulses(headerData)
	if pulses == nil {
		t.Fatal("generatePulses returned nil")
	}
	// Should start with pilot pulses of 2168 T-states
	if pulses[0] != 2168 {
		t.Errorf("First pilot pulse: expected 2168, got %d", pulses[0])
	}

	// Data block (flag >= 128): should have 3223 pilot pulses
	dataBlock := []byte{0xFF, 0x01}
	dataPulses := tp.generatePulses(dataBlock)
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
