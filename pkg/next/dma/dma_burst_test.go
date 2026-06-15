package dma

import "testing"

// burstStream builds an A->B mem transfer in BURST mode with a fixed-time
// prescaler, so the transfer interleaves with the CPU (one byte per prescaler
// cycles) instead of completing instantly.
func burstStream(src, dst, length uint16, presc byte) []byte {
	return []byte{
		0xC3,
		0x7D, byte(src), byte(src >> 8), byte(length), byte(length >> 8), // WR0 A->B
		0x14,                            // WR1 port A increment, mem (no timing byte)
		0x50,                            // WR2 port B increment, mem, timing-byte-follows
		0x22,                            // WR2 timing byte: prescaler-follows (D5) + cycle code 2
		presc,                           // ZXN prescaler
		0xCD, byte(dst), byte(dst >> 8), // WR4 burst + port B addr
		0xCF, 0x87, // LOAD, ENABLE
	}
}

// A burst+prescaler transfer does NOT complete at ENABLE; it pumps one byte at
// a time as the clock advances, letting the CPU run in between.
func TestBurstInterleavesWithClock(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 4; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 4, 50)) // prescaler 50 → byte every 50 T

	// Deferred: nothing transferred at ENABLE.
	if d.ByteCounter() != 0 {
		t.Fatalf("burst transferred %d bytes at ENABLE, want 0 (deferred)", d.ByteCounter())
	}

	// Byte i becomes due at now = i*50. Step in 10-T increments.
	transferredBy := map[uint16]uint64{}
	for now = 0; now <= 4*50; now += 10 {
		before := d.ByteCounter()
		d.Step(now)
		for b := before; b < d.ByteCounter(); b++ {
			transferredBy[b] = now
		}
	}
	for i := uint16(0); i < 4; i++ {
		if got, want := mem[0x6000+i], byte(0xA0+i); got != want {
			t.Errorf("mem[$%04X] = $%02X, want $%02X", 0x6000+i, got, want)
		}
	}
	if d.ByteCounter() != 4 {
		t.Fatalf("after stepping, counter = %d, want 4", d.ByteCounter())
	}
	// Byte i should not have transferred before its due time i*50.
	for i := uint16(0); i < 4; i++ {
		if transferredBy[i] < uint64(i)*50 {
			t.Errorf("byte %d transferred at T=%d, before its due time %d", i, transferredBy[i], uint64(i)*50)
		}
	}
}

// Burst WITHOUT a prescaler runs to completion at ENABLE (per the spec: burst
// only yields to the CPU in fixed-time mode).
func TestBurstNoPrescalerRunsToCompletion(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 4; i++ {
		mem[0x4000+i] = byte(i)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	// Burst mode, no prescaler byte.
	feed(d, []byte{
		0xC3, 0x7D, 0x00, 0x40, 0x04, 0x00, 0x14, 0x10,
		0xCD, 0x00, 0x60, 0xCF, 0x87,
	})
	if d.ByteCounter() != 4 {
		t.Errorf("burst-no-prescaler counter = %d at ENABLE, want 4 (runs to completion)", d.ByteCounter())
	}
}

// A burst transfer with no clock wired falls back to running to completion, so
// behaviour is safe even without the emulator clock hook.
func TestBurstWithoutClockCompletes(t *testing.T) {
	mem := memMap{}
	mem[0x4000] = 0x99
	d := New(mem) // no SetClock
	feed(d, burstStream(0x4000, 0x6000, 1, 50))
	if mem[0x6000] != 0x99 {
		t.Errorf("burst with no clock did not complete: $6000 = $%02X", mem[0x6000])
	}
}
