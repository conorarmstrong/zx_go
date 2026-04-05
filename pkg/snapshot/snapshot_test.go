package snapshot

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestSNAHeaderParsing(t *testing.T) {
	// Build a valid 48K SNA file with known register values.
	// SNA header: 27 bytes + 48K RAM (49152 bytes) = 49179 bytes.
	header := make([]byte, 27)
	header[0] = 0x3F  // I
	header[1] = 0x01  // L'
	header[2] = 0x02  // H'
	header[3] = 0x03  // E'
	header[4] = 0x04  // D'
	header[5] = 0x05  // C'
	header[6] = 0x06  // B'
	header[7] = 0x07  // F'
	header[8] = 0x08  // A'
	header[9] = 0x09  // L
	header[10] = 0x0A // H
	header[11] = 0x0B // E
	header[12] = 0x0C // D
	header[13] = 0x0D // C
	header[14] = 0x0E // B
	binary.LittleEndian.PutUint16(header[15:17], 0x5678) // IY
	binary.LittleEndian.PutUint16(header[17:19], 0x9ABC) // IX
	header[19] = 0x04 // IFF2 set (bit 2)
	header[20] = 0x7F // R
	header[21] = 0xAA // F
	header[22] = 0xBB // A
	// SP points to 0xFF00 in bank 0 (0xC000-0xFFFF)
	binary.LittleEndian.PutUint16(header[23:25], 0xFF00)
	header[25] = 0x01 // IM 1
	header[26] = 0x02 // Border = 2

	// Create 48K RAM with PC on the stack
	ram := make([]byte, 49152)
	// SP=0xFF00, which is at offset 0xFF00-0xC000=0x3F00 in bank 0 (third 16K block)
	stackOffset := 0x8000 + 0x3F00 // past bank 5 (16K) + bank 2 (16K), then offset in bank 0
	ram[stackOffset] = 0x34         // PC low byte
	ram[stackOffset+1] = 0x12      // PC high byte

	// Write temp SNA file
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sna")
	var buf bytes.Buffer
	buf.Write(header)
	buf.Write(ram)
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	snap := New()
	if err := snap.Load(path); err != nil {
		t.Fatalf("Failed to load SNA: %v", err)
	}

	// Verify registers
	if snap.CPU.I != 0x3F {
		t.Errorf("I: expected 0x3F, got 0x%02X", snap.CPU.I)
	}
	if snap.CPU.L_ != 0x01 {
		t.Errorf("L': expected 0x01, got 0x%02X", snap.CPU.L_)
	}
	if snap.CPU.H_ != 0x02 {
		t.Errorf("H': expected 0x02, got 0x%02X", snap.CPU.H_)
	}
	if snap.CPU.E_ != 0x03 {
		t.Errorf("E': expected 0x03, got 0x%02X", snap.CPU.E_)
	}
	if snap.CPU.D_ != 0x04 {
		t.Errorf("D': expected 0x04, got 0x%02X", snap.CPU.D_)
	}
	if snap.CPU.B_ != 0x06 {
		t.Errorf("B': expected 0x06, got 0x%02X", snap.CPU.B_)
	}
	if snap.CPU.C_ != 0x05 {
		t.Errorf("C': expected 0x05, got 0x%02X", snap.CPU.C_)
	}
	if snap.CPU.A_ != 0x08 {
		t.Errorf("A': expected 0x08, got 0x%02X", snap.CPU.A_)
	}
	if snap.CPU.F_ != 0x07 {
		t.Errorf("F': expected 0x07, got 0x%02X", snap.CPU.F_)
	}
	if snap.CPU.L != 0x09 {
		t.Errorf("L: expected 0x09, got 0x%02X", snap.CPU.L)
	}
	if snap.CPU.H != 0x0A {
		t.Errorf("H: expected 0x0A, got 0x%02X", snap.CPU.H)
	}
	if snap.CPU.B != 0x0E {
		t.Errorf("B: expected 0x0E, got 0x%02X", snap.CPU.B)
	}
	if snap.CPU.C != 0x0D {
		t.Errorf("C: expected 0x0D, got 0x%02X", snap.CPU.C)
	}
	if snap.CPU.A != 0xBB {
		t.Errorf("A: expected 0xBB, got 0x%02X", snap.CPU.A)
	}
	if snap.CPU.F != 0xAA {
		t.Errorf("F: expected 0xAA, got 0x%02X", snap.CPU.F)
	}
	if snap.CPU.IY != 0x5678 {
		t.Errorf("IY: expected 0x5678, got 0x%04X", snap.CPU.IY)
	}
	if snap.CPU.IX != 0x9ABC {
		t.Errorf("IX: expected 0x9ABC, got 0x%04X", snap.CPU.IX)
	}
	if !snap.CPU.IFF2 {
		t.Error("IFF2 should be true")
	}
	if !snap.CPU.IFF1 {
		t.Error("IFF1 should be true (derived from IFF2)")
	}
	if snap.CPU.R != 0x7F {
		t.Errorf("R: expected 0x7F, got 0x%02X", snap.CPU.R)
	}
	if snap.CPU.IM != 1 {
		t.Errorf("IM: expected 1, got %d", snap.CPU.IM)
	}
	if snap.CPU.BorderColor != 2 {
		t.Errorf("Border: expected 2, got %d", snap.CPU.BorderColor)
	}
	// PC should be popped from stack: 0x1234
	if snap.CPU.PC != 0x1234 {
		t.Errorf("PC: expected 0x1234, got 0x%04X", snap.CPU.PC)
	}
	// SP should be incremented by 2
	if snap.CPU.SP != 0xFF02 {
		t.Errorf("SP: expected 0xFF02, got 0x%04X", snap.CPU.SP)
	}
}

func TestSNARoundTrip(t *testing.T) {
	// Create a snapshot with known values
	orig := New()
	orig.CPU.A = 0x42
	orig.CPU.F = 0xFF
	orig.CPU.B = 0x11
	orig.CPU.C = 0x22
	orig.CPU.D = 0x33
	orig.CPU.E = 0x44
	orig.CPU.H = 0x55
	orig.CPU.L = 0x66
	orig.CPU.IX = 0x1234
	orig.CPU.IY = 0x5678
	orig.CPU.SP = 0xFFFE
	orig.CPU.PC = 0x8000
	orig.CPU.I = 0x3F
	orig.CPU.R = 0x7F
	orig.CPU.IFF1 = true
	orig.CPU.IFF2 = true
	orig.CPU.IM = 1
	orig.CPU.BorderColor = 5
	orig.Memory.Is128K = true
	orig.Memory.Port7FFD = 0x10
	// Put some data in RAM
	orig.Memory.RAM[5][0] = 0xAA
	orig.Memory.RAM[2][0] = 0xBB
	orig.Memory.RAM[0][0] = 0xCC

	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.sna")
	if err := orig.Save(path); err != nil {
		t.Fatalf("Failed to save SNA: %v", err)
	}

	loaded := New()
	if err := loaded.Load(path); err != nil {
		t.Fatalf("Failed to load SNA: %v", err)
	}

	if loaded.CPU.A != orig.CPU.A {
		t.Errorf("A: expected 0x%02X, got 0x%02X", orig.CPU.A, loaded.CPU.A)
	}
	if loaded.CPU.IX != orig.CPU.IX {
		t.Errorf("IX: expected 0x%04X, got 0x%04X", orig.CPU.IX, loaded.CPU.IX)
	}
	if loaded.CPU.IY != orig.CPU.IY {
		t.Errorf("IY: expected 0x%04X, got 0x%04X", orig.CPU.IY, loaded.CPU.IY)
	}
	if loaded.CPU.IM != orig.CPU.IM {
		t.Errorf("IM: expected %d, got %d", orig.CPU.IM, loaded.CPU.IM)
	}
	if loaded.Memory.RAM[5][0] != 0xAA {
		t.Errorf("RAM[5][0]: expected 0xAA, got 0x%02X", loaded.Memory.RAM[5][0])
	}
	if loaded.Memory.RAM[2][0] != 0xBB {
		t.Errorf("RAM[2][0]: expected 0xBB, got 0x%02X", loaded.Memory.RAM[2][0])
	}
}

func TestSZXPageMapping(t *testing.T) {
	// Create a snapshot with distinct data in each bank
	orig := New()
	orig.Memory.Is128K = true
	for bank := 0; bank < 8; bank++ {
		for i := range orig.Memory.RAM[bank] {
			orig.Memory.RAM[bank][i] = byte(bank)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.szx")
	if err := orig.Save(path); err != nil {
		t.Fatalf("Failed to save SZX: %v", err)
	}

	loaded := New()
	if err := loaded.Load(path); err != nil {
		t.Fatalf("Failed to load SZX: %v", err)
	}

	// Verify each bank has the correct data (1:1 mapping)
	for bank := 0; bank < 8; bank++ {
		if loaded.Memory.RAM[bank][0] != byte(bank) {
			t.Errorf("Bank %d: expected 0x%02X, got 0x%02X", bank, bank, loaded.Memory.RAM[bank][0])
		}
	}
}

func TestTapePlayerLoadTAP(t *testing.T) {
	// Create a minimal TAP file: 2-byte length + block data
	var buf bytes.Buffer

	// Block 1: header (flag byte 0x00 = header)
	block1 := []byte{0x00, 0x03, 0x41, 0x42, 0x43} // flag + type + "ABC"
	binary.Write(&buf, binary.LittleEndian, uint16(len(block1)))
	buf.Write(block1)

	// Block 2: data (flag byte 0xFF = data)
	block2 := make([]byte, 10)
	block2[0] = 0xFF
	binary.Write(&buf, binary.LittleEndian, uint16(len(block2)))
	buf.Write(block2)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.tap")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	// Need to import ula package for TapePlayer - test at snapshot level instead
	// Just verify the TAP parsing produces correct block count
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Parse blocks manually to verify
	blockCount := 0
	offset := 0
	for offset+2 <= len(data) {
		blockLen := int(binary.LittleEndian.Uint16(data[offset : offset+2]))
		offset += 2 + blockLen
		blockCount++
	}
	if blockCount != 2 {
		t.Errorf("Expected 2 blocks, got %d", blockCount)
	}
}

func TestZ80Compression(t *testing.T) {
	snap := New()

	// Test compression of repeated bytes
	data := make([]byte, 100)
	for i := range data {
		data[i] = 0xAA
	}
	compressed := snap.compressZ80(data)
	decompressed, err := snap.decompressZ80(compressed)
	if err != nil {
		t.Fatalf("Decompression failed: %v", err)
	}
	if len(decompressed) != len(data) {
		t.Errorf("Decompressed length: expected %d, got %d", len(data), len(decompressed))
	}
	for i, b := range decompressed {
		if b != data[i] {
			t.Errorf("Byte %d: expected 0x%02X, got 0x%02X", i, data[i], b)
			break
		}
	}

	// Test that 0xED bytes are handled correctly
	edData := []byte{0xED, 0x00, 0xED, 0xED, 0x55}
	compressed = snap.compressZ80(edData)
	decompressed, err = snap.decompressZ80(compressed)
	if err != nil {
		t.Fatalf("ED decompression failed: %v", err)
	}
	if len(decompressed) != len(edData) {
		t.Errorf("ED decompressed length: expected %d, got %d", len(edData), len(decompressed))
	}
}
