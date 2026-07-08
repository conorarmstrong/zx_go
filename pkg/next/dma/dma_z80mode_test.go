package dma

import "testing"

// The access port selects the DMA mode (zxnext.vhd:1817 dma_mode <=
// port_0b_lsb; ports.txt 0x0b/0x6b "accessing port selects DMA mode"):
// $6B = zxn dma, $0B = Z80-DMA-compatible. In z80 mode LOAD seeds the byte
// counter with -1 (dma.vhd:664 "z80 dma loads -1"), so the transfer loop
// (repeat while counter < block length) moves length+1 bytes — the classic
// Z80 DMA convention.
func TestZ80ModeTransfersLengthPlusOne(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 8; i++ {
		mem[0x4000+i] = byte(0x10 + i)
	}
	d := New(mem)
	d.SetZ80Mode(true)
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	// length 4 in z80 mode => 5 bytes move.
	for i := uint16(0); i < 5; i++ {
		if got, want := mem[0x6000+i], byte(0x10+i); got != want {
			t.Errorf("dst[$%04X] = $%02X, want $%02X (z80 mode moves len+1)", 0x6000+i, got, want)
		}
	}
	if _, moved := mem[0x6005]; moved {
		t.Error("dst[$6005] written — z80 mode must move exactly len+1 bytes")
	}
	// Pointers advance once per byte: start + 5.
	if d.CurrentA() != 0x4005 || d.CurrentB() != 0x6005 {
		t.Errorf("pointers = A $%04X / B $%04X, want $4005 / $6005", d.CurrentA(), d.CurrentB())
	}
	// The raw counter started at -1, so after 5 increments it reads 4 — the
	// programmed length, exactly what the FPGA read-back returns.
	if d.ByteCounter() != 4 {
		t.Errorf("byte counter = %d, want 4 (z80 mode counts from -1)", d.ByteCounter())
	}
}

// zxn mode (the default) is unchanged by the z80-mode support: length bytes
// move and the counter counts from 0.
func TestZxnModeStillTransfersLength(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 8; i++ {
		mem[0x4000+i] = byte(0x10 + i)
	}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	if _, moved := mem[0x6004]; moved {
		t.Error("dst[$6004] written — zxn mode must move exactly len bytes")
	}
	if d.ByteCounter() != 4 || d.CurrentA() != 0x4004 {
		t.Errorf("counter/A = %d / $%04X, want 4 / $4004", d.ByteCounter(), d.CurrentA())
	}
}

// Auto-restart reloads the counter with the mode's initial value (dma.vhd:482:
// z80 mode reloads -1), so every repeated block also moves length+1 bytes.
func TestZ80ModeAutoRestartReloadsMinusOne(t *testing.T) {
	count := 0
	mem := &countingMem{count: &count}
	d := New(mem)
	d.SetZ80Mode(true)
	// WR5 with D5 (auto-restart) between WR2 and WR4, then LOAD+ENABLE. Build
	// the stream by hand: fixed ports so the counting sink sees every byte.
	feed(d, []byte{
		0xC3,
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0 A->B src=$4000 len=3
		0x24,             // WR1 port A fixed, mem
		0x20,             // WR2 port B fixed, mem
		0xAA,             // WR5 auto-restart
		0xAD, 0x00, 0x60, // WR4 continuous, port B=$6000
		0xCF, 0x87, // LOAD, ENABLE — block 1: 4 bytes (len 3 + 1)
	})
	first := count
	if first != 4 {
		t.Fatalf("first block wrote %d bytes, want 4 (len 3 + 1 in z80 mode)", first)
	}
	feed(d, []byte{0x87}) // ENABLE again — auto-restart left it armed
	if count-first != 4 {
		t.Errorf("restarted block wrote %d bytes, want 4 (counter reloads -1)", count-first)
	}
}

// $BF (Read Status Byte) points the read sequence at the status register
// (dma.vhd:687 reg_rd_seq_s := RD_STATUS): the next port read returns the
// status byte regardless of where the cycling sequence stood, and the
// sequence then continues to the next masked register after status.
func TestReadStatusByteCommand(t *testing.T) {
	d := New(memMap{})
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	// Mask: counter lo + port A lo (bits 1,3). Consume one read so the cursor
	// sits mid-sequence, then issue $BF.
	feed(d, []byte{0xBB, 0x0A})
	_ = d.ReadCommand()  // counter lo — cursor now at port A lo
	d.WriteCommand(0xBF) // Read Status Byte
	// End of block reached => status = $1A (endofblock_n=0, fixed 1101, idle).
	if got := d.ReadCommand(); got != 0x1A {
		t.Errorf("read after $BF = $%02X, want status $1A", got)
	}
	// After status the sequence resumes at the first masked register: counter lo.
	if got := d.ReadCommand(); got != 0x04 {
		t.Errorf("read after status = $%02X, want counter lo $04", got)
	}
}

// An empty read mask still returns the status byte — the FPGA's read FSM
// falls back to RD_STATUS whenever no mask bit selects a next register
// (dma.vhd R6_BYTE_0 / every RD_* else branch), never floating.
func TestEmptyReadMaskReturnsStatus(t *testing.T) {
	d := New(memMap{})
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	feed(d, []byte{0xBB, 0x00})
	if got := d.ReadCommand(); got != 0x1A {
		t.Errorf("empty-mask read = $%02X, want status $1A", got)
	}
	if got := d.ReadCommand(); got != 0x1A {
		t.Errorf("second empty-mask read = $%02X, want status $1A (stays on status)", got)
	}
}
