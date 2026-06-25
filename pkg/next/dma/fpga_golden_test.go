package dma

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// goldenMem is a 64K byte array matching the testbench's behavioral RAM target.
// It persists across the golden's RESET boundaries — exactly like the FPGA bus
// target, which only the DMA chip resets, not the surrounding memory.
type goldenMem [65536]byte

func (m *goldenMem) Read(a uint16) byte     { return m[a] }
func (m *goldenMem) Write(a uint16, v byte) { m[a] = v }

// goldenIO mirrors the testbench IO space: per-port FIFOs of bytes the DMA
// reads (seeded by IOSEED), and an ordered log of the bytes the DMA writes
// (asserted against the golden's IOW lines).
type goldenIO struct {
	reads  map[uint16][]byte
	writes []ioWriteRec
}

type ioWriteRec struct {
	port uint16
	val  byte
}

func newGoldenIO() *goldenIO { return &goldenIO{reads: map[uint16][]byte{}} }

func (g *goldenIO) WritePort(port uint16, val byte) {
	g.writes = append(g.writes, ioWriteRec{port, val})
}

func (g *goldenIO) ReadPort(port uint16) byte {
	q := g.reads[port]
	if len(q) == 0 {
		return 0xFF
	}
	v := q[0]
	g.reads[port] = q[1:]
	return v
}

// TestDMAMatchesFPGAGolden replays the zxnDMA golden — captured from the real
// FPGA device/dma.vhd (entity z80dma) driven through its WR0..WR6 command stream
// with a behavioral 64K-RAM + IO bus target, simulated under GHDL — through our
// public DMA API and asserts the moved bytes, the read-mask read-back sequence,
// the final byte counter / port pointers, and the IO write log all match the
// hardware exactly. Tooling: _tools/dma-vhdl-test/.
//
// Replay contract (one golden line = one op):
//
//	RESET                 re-new the DMA (the RAM/IO target persists, as in HW)
//	SEED  addr val        pre-seed RAM[addr]
//	IOSEED port val       enqueue a byte the DMA will read from that IO port
//	CMD   byte            d.WriteCommand(byte)  — one port-0x6B stream byte
//	RUN                   marker: our model runs the transfer at ENABLE already
//	MEM   addr val        assert RAM[addr] == val after the transfer
//	IOW   n port val      assert the n-th IO write the DMA issued
//	RDBK  byte            assert d.ReadCommand() == byte
func TestDMAMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/dma_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	mem := &goldenMem{}
	io := newGoldenIO()
	var d *DMA

	hx := func(s string) uint64 { n, _ := strconv.ParseUint(s, 16, 32); return n }
	dec := func(s string) int { n, _ := strconv.Atoi(s); return n }

	sc := bufio.NewScanner(f)
	ln, checks := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fld := strings.Fields(line)
		switch fld[0] {
		case "RESET":
			d = New(mem)
			d.SetIOBus(io)
		case "SEED":
			mem[uint16(hx(fld[1]))] = byte(hx(fld[2]))
		case "IOSEED":
			p := uint16(hx(fld[1]))
			io.reads[p] = append(io.reads[p], byte(hx(fld[2])))
		case "CMD":
			d.WriteCommand(byte(hx(fld[1])))
		case "RUN":
			// our model runs the transfer synchronously at ENABLE; nothing to do
		case "MEM":
			addr := uint16(hx(fld[1]))
			want := byte(hx(fld[2]))
			if got := mem[addr]; got != want {
				t.Errorf("line %d: MEM[%04X] = %02X, want %02X", ln, addr, got, want)
			}
			checks++
		case "IOW":
			n := dec(fld[1])
			wantPort := uint16(hx(fld[2]))
			wantVal := byte(hx(fld[3]))
			if n < len(io.writes) {
				w := io.writes[n]
				if w.port != wantPort || w.val != wantVal {
					t.Errorf("line %d: IOW[%d] = port %04X val %02X, want port %04X val %02X",
						ln, n, w.port, w.val, wantPort, wantVal)
				}
			} else if wantPort != 0 || wantVal != 0 {
				// golden empty-slot rows (IOW n 00 00) past the real writes are fine
				t.Errorf("line %d: IOW[%d] missing, want port %04X val %02X",
					ln, n, wantPort, wantVal)
			}
			checks++
		case "RDBK":
			want := byte(hx(fld[1]))
			if got := d.ReadCommand(); got != want {
				t.Errorf("line %d: RDBK = %02X, want %02X", ln, got, want)
			}
			checks++
		default:
			t.Fatalf("line %d: unknown golden op %q", ln, fld[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if checks < 40 {
		t.Fatalf("expected the full golden replay, only saw %d assertions", checks)
	}
}
