package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// A Z80 program probes the ESP the way NextZXOS does: it writes "AT\r" to the
// UART Tx port, polls the status port until a byte arrives and reads the reply
// from the Rx port. Everything between the guest and the module is the real
// register block and the real serial PHY at 115200 baud, clocked from the
// CPU's instruction stream, so this is the reachability test for the UART as
// the CTC's is for the CTC.
func TestNextGuestProbesTheESPOverThePorts(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	code := []byte{
		0x01, 0x3B, 0x13, //       ld bc,$133B
		0x21, 0x30, 0x80, //       ld hl,msg
		0x1E, 0x03, //             ld e,3
		0x7E,       // send:      ld a,(hl)
		0xED, 0x79, //             out (c),a
		0x23,       //             inc hl
		0x1D,       //             dec e
		0x20, 0xF9, //             jr nz,send
		0x21, 0x00, 0x90, //       ld hl,$9000
		0x16, 0x04, //             ld d,4
		0xED, 0x78, // wait:      in a,(c)        ; status
		0xE6, 0x01, //             and 1           ; rx available
		0x28, 0xFA, //             jr z,wait
		0x06, 0x14, //             ld b,$14        ; $143B Rx data
		0xED, 0x78, //             in a,(c)
		0x06, 0x13, //             ld b,$13
		0x77,       //             ld (hl),a
		0x23,       //             inc hl
		0x15,       //             dec d
		0x20, 0xEF, //             jr nz,wait
		0xF3, //                   di
		0x76, //                   halt
	}
	for i, b := range code {
		h.WriteMemory(uint16(0x8000+i), b)
	}
	for i, b := range []byte("AT\r") {
		h.WriteMemory(uint16(0x8030+i), b)
	}
	h.CPU().PC = 0x8000
	h.CPU().IFF1 = false
	h.CPU().IFF2 = false
	// 3 bytes out and 4 back at 243 clocks a bit, ten bits a byte: about
	// 17,000 CPU T-states each way at 3.5 MHz.
	h.RunFrames(40)

	var got [4]byte
	for i := range got {
		got[i] = h.Memory(uint16(0x9000 + i))
	}
	if string(got[:]) != "OK\r\n" {
		t.Errorf("guest read %q from the ESP, want %q (PC=%04X)", got[:], "OK\r\n", h.CPU().PC)
	}
}

// The ports are claimed on the Next and only there.
func TestNextClaimsTheUARTPorts(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	for _, p := range []uint16{0x133B, 0x143B, 0x153B, 0x163B} {
		if _, ok := h.ULA().ReadPort(p); !ok {
			t.Errorf("port %04X is not claimed on the Next", p)
		}
	}
	if _, ok := h.ULA().ReadPort(0x173B); ok {
		t.Error("port $173B is claimed")
	}
}
