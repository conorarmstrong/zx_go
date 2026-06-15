package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/dma"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Port 0x6B command writes and register read-backs both route through the ULA
// to the zxnDMA: a mem->mem transfer programmed via WritePort(0x6B,...) runs,
// and the byte counter is read back via ReadPort(0x6B).
func TestULARoutesDMACommandAndReadback(t *testing.T) {
	dir := "test_roms_dma"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	for i := uint16(0); i < 8; i++ {
		mem.Write(0x6000+i, byte(0x10+i))
	}
	u := New(mem, keyboard.New())
	d := dma.New(mem)
	u.SetNextDMA(d)

	// Program an A->B mem copy $6000 -> $7000, length 8, both incrementing,
	// continuous, via the port (matches the WR-stream the ROM would write).
	stream := []byte{
		0xC3,
		0x7D, 0x00, 0x60, 0x08, 0x00, // WR0 A->B src=$6000 len=8
		0x14,             // WR1 port A increment, mem
		0x10,             // WR2 port B increment, mem
		0xAD, 0x00, 0x70, // WR4 continuous, port B=$7000
		0xCF, 0x87, // LOAD, ENABLE
	}
	for _, b := range stream {
		u.WritePort(0x006B, b)
	}
	for i := uint16(0); i < 8; i++ {
		if got, want := mem.Read(0x7000+i), byte(0x10+i); got != want {
			t.Fatalf("mem[$%04X] = $%02X, want $%02X (DMA via ULA port)", 0x7000+i, got, want)
		}
	}

	// Read back the byte counter (mask bits 1,2) through ReadPort(0x6B).
	u.WritePort(0x006B, 0xBB)
	u.WritePort(0x006B, 0x06)
	lo, ok := u.ReadPort(0x006B)
	if !ok {
		t.Fatal("ReadPort(0x6B) not claimed by the DMA")
	}
	hi, _ := u.ReadPort(0x006B)
	if lo != 0x08 || hi != 0x00 {
		t.Errorf("byte counter read-back = $%02X%02X, want $0008", hi, lo)
	}
}
