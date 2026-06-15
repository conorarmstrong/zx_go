package dma

import "testing"

// ioLog is a fake IO bus capturing OUTs and serving canned INs.
type ioLog struct {
	writes []ioWrite
	reads  map[uint16][]byte // per-port FIFO of bytes to return
}

type ioWrite struct {
	port uint16
	val  byte
}

func newIOLog() *ioLog { return &ioLog{reads: map[uint16][]byte{}} }

func (l *ioLog) WritePort(port uint16, val byte) {
	l.writes = append(l.writes, ioWrite{port, val})
}

func (l *ioLog) ReadPort(port uint16) byte {
	q := l.reads[port]
	if len(q) == 0 {
		return 0xFF
	}
	v := q[0]
	l.reads[port] = q[1:]
	return v
}

// wr1 / wr2 base-byte builders: address mode + memory/IO select, no timing byte.
func wr1Byte(mode byte, isIO bool) byte {
	b := map[byte]byte{addrDecrement: 0x04, addrIncrement: 0x14, addrFixed: 0x24}[mode]
	if isIO {
		b |= 0x08
	}
	return b
}

func wr2Byte(mode byte, isIO bool) byte {
	b := map[byte]byte{addrDecrement: 0x00, addrIncrement: 0x10, addrFixed: 0x20}[mode]
	if isIO {
		b |= 0x08
	}
	return b
}

// A transfer whose destination (port B) is an IO port OUTs each byte to that
// port instead of writing memory — this is how DMA uploads to sprite/Layer2/DAC
// ports work, and the bug the old "everything is memory" model had.
func TestIOEndpointDestination(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 4; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	io := newIOLog()
	d := New(mem)
	d.SetIOBus(io)
	// WR0 A->B, src=$4000 len=4; WR1 port A mem increment; WR2 port B IO fixed
	// at $005B; WR4 port B addr=$005B; LOAD; ENABLE.
	feed(d, []byte{
		0xC3,
		0x7D, 0x00, 0x40, 0x04, 0x00, // WR0 A->B src=$4000 len=4
		wr1Byte(addrIncrement, false), // port A: memory, increment
		wr2Byte(addrFixed, true),      // port B: IO, fixed
		0x8D, 0x5B, 0x00,              // WR4 port B addr=$005B
		0xCF, 0x87,
	})
	if len(io.writes) != 4 {
		t.Fatalf("IO writes = %d, want 4", len(io.writes))
	}
	for i, w := range io.writes {
		if w.port != 0x005B {
			t.Errorf("write %d to port $%04X, want $005B", i, w.port)
		}
		if w.val != byte(0xA0+i) {
			t.Errorf("write %d val $%02X, want $%02X", i, w.val, byte(0xA0+i))
		}
	}
	// Memory destination region must be untouched.
	if mem[0x005B] != 0 {
		t.Errorf("memory $005B = $%02X, want 0 (IO endpoint must not write memory)", mem[0x005B])
	}
}

// A transfer whose source (port A) is an IO port INs each byte from that port.
func TestIOEndpointSource(t *testing.T) {
	mem := memMap{}
	io := newIOLog()
	io.reads[0x0057] = []byte{0x11, 0x22, 0x33}
	d := New(mem)
	d.SetIOBus(io)
	// WR0 A->B src(port A)=$0057 len=3; WR1 port A IO fixed; WR2 port B mem inc;
	// WR4 port B=$6000.
	feed(d, []byte{
		0xC3,
		0x7D, 0x57, 0x00, 0x03, 0x00, // WR0 A->B "src"=$0057 len=3
		wr1Byte(addrFixed, true), // port A: IO, fixed
		wr2Byte(addrIncrement, false),
		0x8D, 0x00, 0x60, // WR4 port B=$6000
		0xCF, 0x87,
	})
	for i, want := range []byte{0x11, 0x22, 0x33} {
		if got := mem[0x6000+uint16(i)]; got != want {
			t.Errorf("mem[$%04X] = $%02X, want $%02X (from IO read)", 0x6000+uint16(i), got, want)
		}
	}
}

// After a transfer the internal byte counter equals the block length and the
// current port pointers have advanced past the block.
func TestCurrentPointersAndCounter(t *testing.T) {
	mem := memMap{}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x6000, 16, addrIncrement, addrIncrement, true))
	if d.ByteCounter() != 16 {
		t.Errorf("ByteCounter = %d, want 16", d.ByteCounter())
	}
	if d.CurrentA() != 0x4010 {
		t.Errorf("CurrentA = $%04X, want $4010", d.CurrentA())
	}
	if d.CurrentB() != 0x6010 {
		t.Errorf("CurrentB = $%04X, want $6010", d.CurrentB())
	}
}

// Continue ($D3) resets the byte counter so a following ENABLE transfers
// another block from the CURRENT pointers (not the original start addresses).
func TestContinueResumesFromCurrent(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 8; i++ {
		mem[0x4000+i] = byte(0x10 + i)
	}
	d := New(mem)
	feed(d, transferCmd(0x4000, 0x6000, 4, addrIncrement, addrIncrement, true))
	// First 4 bytes copied to $6000..$6003. Now Continue + ENABLE: another 4
	// from current pointers ($4004 -> $6004).
	feed(d, []byte{0xD3, 0x87})
	for i := uint16(0); i < 8; i++ {
		if got, want := mem[0x6000+i], byte(0x10+i); got != want {
			t.Errorf("after continue mem[$%04X] = $%02X, want $%02X", 0x6000+i, got, want)
		}
	}
}

// WR5 auto-restart (D5) reloads the start addresses at end of block, so a
// re-ENABLE repeats the transfer from the original start.
func TestAutoRestartReloads(t *testing.T) {
	mem := memMap{0x4000: 0xEE}
	d := New(mem)
	// transferCmd uses WR5=0x82 (stop on end). Build our own with WR5 D5 set
	// (0xA2 = %10100010 → auto-restart).
	feed(d, []byte{
		0xC3,
		0x7D, 0x00, 0x40, 0x01, 0x00, // WR0 A->B src=$4000 len=1
		wr1Byte(addrFixed, false),
		wr2Byte(addrIncrement, false),
		0x8D, 0x00, 0x60, // WR4 port B=$6000
		0xA2,       // WR5 auto-restart
		0xCF, 0x87, // LOAD, ENABLE
	})
	if d.CurrentB() != 0x6000 {
		t.Errorf("auto-restart: CurrentB = $%04X, want reloaded $6000", d.CurrentB())
	}
	// A re-ENABLE (no re-LOAD) repeats from the start.
	mem[0x6000] = 0
	d.WriteCommand(0x87)
	if mem[0x6000] != 0xEE {
		t.Errorf("auto-restart re-ENABLE did not repeat: $6000 = $%02X", mem[0x6000])
	}
}
