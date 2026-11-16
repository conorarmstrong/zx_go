package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// dmaDelayGuest runs a program that takes a hardware IM2 frame interrupt and,
// from inside the service routine, starts a continuous zxnDMA copy. ccValue is
// written to NR$CC first. It returns the first destination byte as the
// routine saw it right after starting the copy, and the four bytes at the
// destination once the routine has returned.
//
// With NR$CC bit 0 set the ULA's dma-interrupt enable is on, so the ULA
// device being out of idle (in service) holds dma_delay_i high and the DMA
// must not take the bus until the RETI frees the slot.
func dmaDelayGuest(t *testing.T, ccValue byte) (inISR byte, after [4]byte) {
	t.Helper()
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	main := []byte{
		0xF3,             // di
		0x01, 0x3B, 0x24, // ld bc,$243B
		0x3E, 0xC0, // ld a,$C0
		0xED, 0x79, // out (c),a
		0x01, 0x3B, 0x25, // ld bc,$253B
		0x3E, 0x01, // ld a,1      ; hardware IM2
		0xED, 0x79, // out (c),a
		0x01, 0x3B, 0x24, // ld bc,$243B
		0x3E, 0xCC, // ld a,$CC
		0xED, 0x79, // out (c),a
		0x01, 0x3B, 0x25, // ld bc,$253B
		0x3E, ccValue, // ld a,cc
		0xED, 0x79, // out (c),a
		0x3E, 0x90, // ld a,$90
		0xED, 0x47, // ld i,a
		0xED, 0x5E, // im 2
		0xFB, // ei
		0x76, // halt
		0xF3, // di
		0x76, // halt
	}
	isr := []byte{
		0x21, 0x00, 0x82, // ld hl,prog
		0x06, 0x0D, //       ld b,13
		0x0E, 0x6B, //       ld c,$6B
		0xED, 0xB3, //       otir
		0x3A, 0x00, 0x91, // ld a,(dst)
		0x32, 0x00, 0xA0, // ld (result),a
		0xED, 0x4D, //       reti
	}
	prog := []byte{
		0xC3,                         // reset
		0x7D, 0x00, 0x92, 0x04, 0x00, // WR0 A->B, source $9200, length 4
		0x14,             // WR1 port A memory, increment
		0x10,             // WR2 port B memory, increment
		0xCD, 0x00, 0x91, // WR4 continuous, destination $9100
		0xCF, // WR6 load
		0x87, // WR6 enable
	}
	if len(prog) != 13 {
		t.Fatalf("prog is %d bytes, want 13", len(prog))
	}
	put := func(addr uint16, b []byte) {
		for i, v := range b {
			h.WriteMemory(addr+uint16(i), v)
		}
	}
	put(0x8000, main)
	put(0x8100, isr)
	put(0x8200, prog)
	put(0x9016, []byte{0x00, 0x81}) // vector 11<<1: the ULA frame interrupt
	put(0x9200, []byte{0xAA, 0xBB, 0xCC, 0xDD})
	h.CPU().PC = 0x8000
	h.RunFrames(4)
	for i := range after {
		after[i] = h.Memory(0x9100 + uint16(i))
	}
	return h.Memory(0xA000), after
}

func TestNextDMAStartedInAnISRRunsAtOnceWithoutTheEnable(t *testing.T) {
	inISR, after := dmaDelayGuest(t, 0x00)
	if inISR != 0xAA {
		t.Errorf("with NR$CC=0 the copy should finish inside the routine, saw %02X", inISR)
	}
	if after != [4]byte{0xAA, 0xBB, 0xCC, 0xDD} {
		t.Errorf("destination %X", after)
	}
}

func TestNextDMAStartedInAnISRWaitsForRETIWhenTheULAEnableIsSet(t *testing.T) {
	inISR, after := dmaDelayGuest(t, 0x01)
	if inISR != 0x00 {
		t.Errorf("the DMA ran inside the service routine (saw %02X) despite NR$CC bit 0", inISR)
	}
	if after != [4]byte{0xAA, 0xBB, 0xCC, 0xDD} {
		t.Errorf("after the RETI the block should have completed, destination %X", after)
	}
}

// inBlockGuest starts a continuous 16 KB copy with interrupts disabled, at
// 3.5 MHz: 16384 bytes at 6 T-states each, 98304 T-states with the CPU held
// off the bus. CTC channel 0 is started just before it as a timer with
// prescaler 256 and time constant 256, so its zero count, and with it an IM2
// request, falls about 8192 T-states into the block. The CPU never
// acknowledges it, so with NR$CD bit 0 set the CTC device stays out of idle
// from that byte on and the DMA must park there (dma.vhd:427-428). It returns
// the first and last destination bytes as seen right after the block's OTIR.
func inBlockGuest(t *testing.T, cd byte) (first, last byte) {
	t.Helper()
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	nr := func(reg, val byte) []byte {
		return []byte{0xED, 0x91, reg, val} // nextreg reg,val
	}
	var main []byte
	main = append(main, 0xF3) // di
	main = append(main, nr(0x07, 0x00)...)
	main = append(main, nr(0xC0, 0x01)...) // hardware IM2
	main = append(main, nr(0xC4, 0x00)...) // no ULA interrupts
	main = append(main, nr(0xC5, 0x01)...) // CTC channel 0 interrupt enable
	main = append(main, nr(0xCD, cd)...)
	main = append(main,
		0x01, 0x3B, 0x18, // ld bc,$183B
		0x3E, 0xA5, //       ld a,$A5 ; int, timer, prescaler 256, TC follows
		0xED, 0x79, //       out (c),a
		0x3E, 0x00, //       ld a,0   ; time constant 256
		0xED, 0x79, //       out (c),a
		0x21, 0x00, 0x82, // ld hl,prog
		0x06, 0x0D, //       ld b,13
		0x0E, 0x6B, //       ld c,$6B
		0xED, 0xB3, //       otir
		0x3A, 0x00, 0xA0, // ld a,($A000)
		0x32, 0x00, 0x90, // ld ($9000),a
		0x3A, 0xFF, 0xDF, // ld a,($DFFF)
		0x32, 0x01, 0x90, // ld ($9001),a
		0x76, // halt
	)
	prog := []byte{
		0xC3,                         // reset
		0x7D, 0x00, 0x40, 0x00, 0x40, // WR0 A->B, source $4000, length $4000
		0x14,             // WR1 port A memory, increment
		0x10,             // WR2 port B memory, increment
		0xCD, 0x00, 0xA0, // WR4 continuous, destination $A000
		0xCF, // WR6 load
		0x87, // WR6 enable
	}
	put := func(addr uint16, b []byte) {
		for i, v := range b {
			h.WriteMemory(addr+uint16(i), v)
		}
	}
	for a := 0x4000; a < 0x8000; a++ {
		h.WriteMemory(uint16(a), 0x5A)
	}
	put(0x8000, main)
	put(0x8200, prog)
	h.CPU().PC = 0x8000
	h.RunFrames(4)
	return h.Memory(0x9000), h.Memory(0x9001)
}

func TestNextDMABlockRunsThroughACTCInterruptWithoutTheEnable(t *testing.T) {
	first, last := inBlockGuest(t, 0x00)
	if first != 0x5A || last != 0x5A {
		t.Errorf("with NR$CD=0 the block should complete, first %02X last %02X", first, last)
	}
}

func TestNextDMABlockYieldsToACTCInterruptMidBlock(t *testing.T) {
	first, last := inBlockGuest(t, 0x01)
	if first != 0x5A {
		t.Errorf("the block did not start (first %02X)", first)
	}
	if last != 0x00 {
		t.Errorf("the block ran through the CTC interrupt (last %02X) despite NR$CD bit 0", last)
	}
}
