package ula

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TZX 0x19, the generalised data block, describes pilot/sync and data as
// streams of SYMBOLS drawn from an alphabet, each symbol being a short run of
// pulses. It had no case in the parser at all.

// gdbSymbol is one alphabet entry: a level-flag plus its pulse lengths.
type gdbSymbol struct {
	flag   byte
	pulses []uint16
}

// gdbBlock assembles a 0x19 block. npp/npd are the maximum pulses per symbol
// in each alphabet; shorter symbols are zero-padded, which the decoder must
// treat as an early end.
func gdbBlock(pauseMS uint16, pilotAlpha []gdbSymbol, pilotRLE [][2]int,
	dataAlpha []gdbSymbol, dataSymbols []int, npp, npd byte) []byte {

	writeAlpha := func(w *bytes.Buffer, alpha []gdbSymbol, maxPulses byte) {
		for _, s := range alpha {
			w.WriteByte(s.flag)
			for i := 0; i < int(maxPulses); i++ {
				var v uint16
				if i < len(s.pulses) {
					v = s.pulses[i]
				}
				_ = binary.Write(w, binary.LittleEndian, v)
			}
		}
	}

	body := new(bytes.Buffer)
	_ = binary.Write(body, binary.LittleEndian, pauseMS)
	_ = binary.Write(body, binary.LittleEndian, uint32(len(pilotRLE))) // TOTP
	body.WriteByte(npp)                                                // NPP
	body.WriteByte(byte(len(pilotAlpha)))                              // ASP
	_ = binary.Write(body, binary.LittleEndian, uint32(len(dataSymbols)))
	body.WriteByte(npd)                  // NPD
	body.WriteByte(byte(len(dataAlpha))) // ASD

	if len(pilotRLE) > 0 {
		writeAlpha(body, pilotAlpha, npp)
		for _, e := range pilotRLE {
			body.WriteByte(byte(e[0]))
			_ = binary.Write(body, binary.LittleEndian, uint16(e[1]))
		}
	}
	if len(dataSymbols) > 0 {
		writeAlpha(body, dataAlpha, npd)
		// Pack the data stream: each symbol takes ceil(log2(ASD)) bits, MSB
		// first, padded to a byte boundary.
		bits := 0
		for (1 << bits) < len(dataAlpha) {
			bits++
		}
		if bits == 0 {
			bits = 1
		}
		var acc, n uint32
		for _, s := range dataSymbols {
			acc = acc<<uint(bits) | uint32(s)
			n += uint32(bits)
			for n >= 8 {
				body.WriteByte(byte(acc >> (n - 8)))
				n -= 8
			}
		}
		if n > 0 {
			body.WriteByte(byte(acc << (8 - n)))
		}
	}

	out := new(bytes.Buffer)
	out.WriteByte(0x19)
	_ = binary.Write(out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// TestTZXGeneralisedPilotRepeats pins the pilot/sync stream: a run-length
// list of (symbol, repeat) pairs expanded into that symbol's pulses.
func TestTZXGeneralisedPilotRepeats(t *testing.T) {
	// One pilot symbol: two pulses of 100 and 200. Repeated three times.
	pilot := []gdbSymbol{{flag: 0, pulses: []uint16{100, 200}}}
	b := newTZXBuilder()
	b.raw(gdbBlock(0, pilot, [][2]int{{0, 3}}, nil, nil, 2, 0)...)
	tp := loadTZXBytes(t, b)

	if tp.BlockCount() != 1 {
		t.Fatalf("block count = %d, want 1", tp.BlockCount())
	}
	pulses := tp.generatePulses(tp.blocks[0])
	want := []uint32{100, 200, 100, 200, 100, 200}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Errorf("pulse %d = %d, want %d", i, pulses[i], want[i])
		}
	}
}

// TestTZXGeneralisedShortSymbolStopsAtZero pins that a zero pulse length ends
// a symbol early, which is how the format packs symbols of differing length
// into a fixed-width table.
func TestTZXGeneralisedShortSymbolStopsAtZero(t *testing.T) {
	// NPP is 3, but the symbol only uses one pulse.
	pilot := []gdbSymbol{{flag: 0, pulses: []uint16{500}}}
	b := newTZXBuilder()
	b.raw(gdbBlock(0, pilot, [][2]int{{0, 2}}, nil, nil, 3, 0)...)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	want := []uint32{500, 500}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v (a zero length must end the symbol)", pulses, want)
	}
}

// TestTZXGeneralisedDataStreamUnpacksSymbols pins the packed data stream:
// with a two-symbol alphabet each symbol is one bit, most significant first.
func TestTZXGeneralisedDataStreamUnpacksSymbols(t *testing.T) {
	data := []gdbSymbol{
		{flag: 0, pulses: []uint16{10}}, // symbol 0
		{flag: 0, pulses: []uint16{20}}, // symbol 1
	}
	b := newTZXBuilder()
	b.raw(gdbBlock(0, nil, nil, data, []int{1, 0, 1, 1}, 0, 1)...)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	want := []uint32{20, 10, 20, 20}
	if len(pulses) != len(want) {
		t.Fatalf("pulses = %v, want %v", pulses, want)
	}
	for i := range want {
		if pulses[i] != want[i] {
			t.Errorf("pulse %d = %d, want %d", i, pulses[i], want[i])
		}
	}
}

// TestTZXGeneralisedHonoursPause pins the trailing pause.
func TestTZXGeneralisedHonoursPause(t *testing.T) {
	pilot := []gdbSymbol{{flag: 0, pulses: []uint16{100}}}
	b := newTZXBuilder()
	b.raw(gdbBlock(50, pilot, [][2]int{{0, 1}}, nil, nil, 1, 0)...)
	tp := loadTZXBytes(t, b)

	pulses := tp.generatePulses(tp.blocks[0])
	if len(pulses) != 2 {
		t.Fatalf("pulses = %v, want one pulse plus a pause", pulses)
	}
	if want := uint32(50 * 3500); pulses[1] != want {
		t.Errorf("pause = %d, want %d", pulses[1], want)
	}
}

// TestTZXGeneralisedTruncatedIsSkipped pins that a block whose declared
// tables run past the end of the file does not abort the whole load.
func TestTZXGeneralisedTruncatedIsSkipped(t *testing.T) {
	pilot := []gdbSymbol{{flag: 0, pulses: []uint16{100}}}
	blk := gdbBlock(0, pilot, [][2]int{{0, 1}}, nil, nil, 1, 0)
	// Claim far more payload than is present.
	binary.LittleEndian.PutUint32(blk[1:5], 0xFFFF)

	b := newTZXBuilder()
	b.standardBlock([]byte{0xAA})
	b.raw(blk...)
	tp := loadTZXBytes(t, b)

	if got := tp.BlockCount(); got != 1 {
		t.Errorf("block count = %d, want 1 (only the standard block)", got)
	}
}
