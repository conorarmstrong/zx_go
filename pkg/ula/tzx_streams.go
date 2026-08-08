package ula

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

// Decoders for the two TZX block types that carry a pulse stream rather than
// bytes: 0x18 (CSW recording) and 0x19 (generalised data block). Both resolve
// to an explicit list of pulse durations, which the player replays verbatim.
//
// Both were previously skipped via their length field, so their content was
// dropped without a word.

// cpuHzTape is the 3.5 MHz reference the TZX timings are quoted against.
const cpuHzTape = 3500000

// decodeCSW turns a TZX 0x18 payload (everything after the 4-byte block
// length) into pulse durations in T-states.
//
// Layout: pause(2) + samplingRate(3) + compression(1) + storedPulses(4) +
// the CSW stream. The stream is CSW's RLE encoding: each byte is a pulse
// length in SAMPLES, and a zero byte escapes to a following 32-bit length.
// Compression 0x01 is that stream raw; 0x02 is the same stream deflated.
func decodeCSW(payload []byte) (pulses []uint32, pause uint16, err error) {
	// pause(2) + samplingRate(3) + compression(1) + storedPulses(4).
	const cswHeader = 10
	if len(payload) < cswHeader {
		return nil, 0, fmt.Errorf("CSW block truncated: %d bytes", len(payload))
	}
	pause = binary.LittleEndian.Uint16(payload[0:2])
	rate := uint32(payload[2]) | uint32(payload[3])<<8 | uint32(payload[4])<<16
	compression := payload[5]
	stream := payload[cswHeader:]

	if rate == 0 {
		return nil, 0, fmt.Errorf("CSW block has a zero sampling rate")
	}

	switch compression {
	case 0x01: // RLE
	case 0x02: // Z-RLE
		zr, zerr := zlib.NewReader(bytes.NewReader(stream))
		if zerr != nil {
			return nil, 0, fmt.Errorf("CSW Z-RLE: %w", zerr)
		}
		defer func() { _ = zr.Close() }()
		inflated, rerr := io.ReadAll(zr)
		if rerr != nil {
			return nil, 0, fmt.Errorf("CSW Z-RLE: %w", rerr)
		}
		stream = inflated
	default:
		return nil, 0, fmt.Errorf("CSW compression type %#02x not supported", compression)
	}

	// One sample is cpuHzTape/rate T-states. Truncated, matching how the
	// rest of the tape pipeline quantises to whole T-states.
	perSample := uint64(cpuHzTape) / uint64(rate)
	if perSample == 0 {
		perSample = 1
	}

	for i := 0; i < len(stream); {
		n := uint64(stream[i])
		i++
		if n == 0 {
			if i+4 > len(stream) {
				break // truncated escape; keep what we decoded
			}
			n = uint64(binary.LittleEndian.Uint32(stream[i : i+4]))
			i += 4
		}
		d := n * perSample
		if d > 0 {
			pulses = append(pulses, clampPulse(d))
		}
	}
	return pulses, pause, nil
}

// clampPulse narrows a T-state duration to the pulse width the player uses.
func clampPulse(d uint64) uint32 {
	const max = uint64(^uint32(0))
	if d > max {
		return uint32(max)
	}
	return uint32(d)
}

// decodeGeneralisedData turns a TZX 0x19 payload (everything after the
// 4-byte block length) into pulse durations in T-states.
//
// The block describes pilot/sync and data as streams of SYMBOLS drawn from an
// alphabet. Layout:
//
//	pause(2) TOTP(4) NPP(1) ASP(1) TOTD(4) NPD(1) ASD(1)
//	[pilot SYMDEF: ASP * (flag + NPP words)] [pilot PRLE: TOTP * (sym + rep word)]
//	[data  SYMDEF: ASD * (flag + NPD words)] [data stream: TOTD symbols, packed]
//
// A symbol is a run of pulses; a zero pulse length ends it early, which is how
// symbols of different lengths share a fixed-width table. Each data symbol
// occupies ceil(log2(ASD)) bits, most significant first.
//
// The per-symbol flag selects the starting level (continue / invert / force
// low / force high). The player toggles on every pulse, which already matches
// the alternating case; the flag is decoded and applied by merging runs that
// should hold their level.
func decodeGeneralisedData(payload []byte) (pulses []uint32, pause uint16, err error) {
	if len(payload) < 14 {
		return nil, 0, fmt.Errorf("generalised block truncated: %d bytes", len(payload))
	}
	pause = binary.LittleEndian.Uint16(payload[0:2])
	totp := binary.LittleEndian.Uint32(payload[2:6])
	npp := int(payload[6])
	asp := int(payload[7])
	totd := binary.LittleEndian.Uint32(payload[8:12])
	npd := int(payload[12])
	asd := int(payload[13])
	if asp == 0 && totp > 0 {
		asp = 256
	}
	if asd == 0 && totd > 0 {
		asd = 256
	}
	off := 14

	// readAlphabet consumes count symbol definitions of maxPulses each.
	readAlphabet := func(count, maxPulses int) ([]gdSymbol, error) {
		out := make([]gdSymbol, 0, count)
		for i := 0; i < count; i++ {
			need := 1 + 2*maxPulses
			if off+need > len(payload) {
				return nil, fmt.Errorf("generalised block: symbol table truncated")
			}
			s := gdSymbol{flag: payload[off]}
			off++
			for p := 0; p < maxPulses; p++ {
				v := binary.LittleEndian.Uint16(payload[off : off+2])
				off += 2
				if v == 0 {
					// A zero length ends the symbol; the rest of the row is
					// padding.
					off += 2 * (maxPulses - p - 1)
					break
				}
				s.pulses = append(s.pulses, v)
			}
			out = append(out, s)
		}
		return out, nil
	}

	var emit levelPulses

	if totp > 0 {
		alpha, aerr := readAlphabet(asp, npp)
		if aerr != nil {
			return nil, 0, aerr
		}
		for i := uint32(0); i < totp; i++ {
			if off+3 > len(payload) {
				return nil, 0, fmt.Errorf("generalised block: pilot stream truncated")
			}
			sym := int(payload[off])
			rep := int(binary.LittleEndian.Uint16(payload[off+1 : off+3]))
			off += 3
			if sym >= len(alpha) {
				return nil, 0, fmt.Errorf("generalised block: pilot symbol %d out of range", sym)
			}
			for r := 0; r < rep; r++ {
				emit.symbol(alpha[sym])
			}
		}
	}

	if totd > 0 {
		alpha, aerr := readAlphabet(asd, npd)
		if aerr != nil {
			return nil, 0, aerr
		}
		bits := 0
		for (1 << bits) < asd {
			bits++
		}
		if bits == 0 {
			bits = 1
		}
		br := bitReader{data: payload[off:]}
		for i := uint32(0); i < totd; i++ {
			sym, ok := br.read(bits)
			if !ok {
				return nil, 0, fmt.Errorf("generalised block: data stream truncated")
			}
			if sym >= len(alpha) {
				return nil, 0, fmt.Errorf("generalised block: data symbol %d out of range", sym)
			}
			emit.symbol(alpha[sym])
		}
	}

	return emit.merged(), pause, nil
}

// gdSymbol is one entry of a generalised block's alphabet.
type gdSymbol struct {
	flag   byte
	pulses []uint16
}

// levelPulses accumulates (duration, level) pairs so symbols whose flag says
// "hold the level" can be merged into single pulses — the player toggles at
// every pulse boundary, so a held level has to be one pulse.
type levelPulses struct {
	durations []uint64
	levels    []bool
	level     bool
}

// symbol appends one symbol's pulses, applying its starting-level flag.
func (l *levelPulses) symbol(s gdSymbol) {
	switch s.flag & 0x03 {
	case 0x01: // opposite to the previous symbol
		l.level = !l.level
	case 0x02: // force low
		l.level = false
	case 0x03: // force high
		l.level = true
	default: // 0x00: continue at the current level
	}
	for _, p := range s.pulses {
		l.durations = append(l.durations, uint64(p))
		l.levels = append(l.levels, l.level)
		l.level = !l.level
	}
}

// merged collapses adjacent pulses at the same level into one.
func (l *levelPulses) merged() []uint32 {
	if len(l.durations) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(l.durations))
	run := l.durations[0]
	cur := l.levels[0]
	for i := 1; i < len(l.durations); i++ {
		if l.levels[i] == cur {
			run += l.durations[i]
			continue
		}
		out = append(out, clampPulse(run))
		run, cur = l.durations[i], l.levels[i]
	}
	return append(out, clampPulse(run))
}

// bitReader pulls fixed-width big-endian bit fields out of a byte slice.
type bitReader struct {
	data []byte
	pos  int // bit position
}

func (b *bitReader) read(n int) (int, bool) {
	v := 0
	for i := 0; i < n; i++ {
		byteIdx := b.pos >> 3
		if byteIdx >= len(b.data) {
			return 0, false
		}
		bit := (b.data[byteIdx] >> (7 - uint(b.pos&7))) & 1
		v = v<<1 | int(bit)
		b.pos++
	}
	return v, true
}
