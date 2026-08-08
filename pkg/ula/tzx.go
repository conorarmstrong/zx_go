package ula

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
)

// TZX version emitted by SaveTZX. 1.20 is the version most modern
// TZX readers (including this package's loader and FUSE) support
// without quirks. Bump only when adopting a block type that requires
// a newer minor.
const (
	tzxVersionMajor = 1
	tzxVersionMinor = 20
)

// SaveTZX writes the currently loaded blocks back to a TZX file.
// Each block is emitted using the same TZX block-ID it parsed as on
// load: standard-speed (0x10), turbo (0x11), or pure-data (0x14).
// Only data blocks are written: metadata LoadTZX skipped (group /
// archive info, text) and the signal-only and flow-control blocks
// (pure tone, pulse sequence, direct recording, pause, jump, loops)
// are dropped, so a save-after-load roundtrip keeps only the bytes.
func (tp *TapePlayer) SaveTZX(path string) error {
	tp.mu.Lock()
	blocks := make([]tapeBlock, 0, len(tp.blocks))
	for _, b := range tp.blocks {
		if b.kind != kindData {
			continue
		}
		cp := make([]byte, len(b.data))
		copy(cp, b.data)
		b.data = cp
		blocks = append(blocks, b)
	}
	tp.mu.Unlock()

	if len(blocks) == 0 {
		return fmt.Errorf("no blocks to save")
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create TZX: %w", err)
	}
	defer func() { _ = f.Close() }()

	// TZX header: "ZXTape!" + 0x1A + major + minor.
	header := []byte{'Z', 'X', 'T', 'a', 'p', 'e', '!', 0x1A, tzxVersionMajor, tzxVersionMinor}
	if _, err := f.Write(header); err != nil {
		return fmt.Errorf("write TZX header: %w", err)
	}

	for i, b := range blocks {
		if err := writeTZXBlock(f, b); err != nil {
			return fmt.Errorf("write block %d: %w", i, err)
		}
	}
	return nil
}

// writeTZXBlock emits a single tapeBlock as the appropriate TZX
// block type. The heuristic picks 0x10 (standard speed) for any
// block parsed as non-turbo, 0x14 (pure data) for turbo blocks
// with no pilot, and 0x11 (turbo) otherwise.
func writeTZXBlock(f *os.File, b tapeBlock) error {
	if !b.turbo {
		// 0x10 standard-speed data block: pause(2) + length(2) + data.
		if len(b.data) > 0xFFFF {
			return fmt.Errorf("block too large for TZX 0x10: %d bytes", len(b.data))
		}
		var hdr [5]byte
		hdr[0] = 0x10
		binary.LittleEndian.PutUint16(hdr[1:3], b.pause)
		binary.LittleEndian.PutUint16(hdr[3:5], uint16(len(b.data)))
		if _, err := f.Write(hdr[:]); err != nil {
			return err
		}
		_, err := f.Write(b.data)
		return err
	}

	usedBits := b.usedBits
	if usedBits == 0 {
		usedBits = 8
	}

	if b.pilotLen == 0 {
		// 0x14 pure-data block (no pilot): zeroPulse(2) + onePulse(2)
		// + usedBits(1) + pause(2) + length(3) + data.
		if len(b.data) >= 1<<24 {
			return fmt.Errorf("block too large for TZX 0x14: %d bytes", len(b.data))
		}
		var hdr [11]byte
		hdr[0] = 0x14
		binary.LittleEndian.PutUint16(hdr[1:3], b.zeroPulse)
		binary.LittleEndian.PutUint16(hdr[3:5], b.onePulse)
		hdr[5] = usedBits
		binary.LittleEndian.PutUint16(hdr[6:8], b.pause)
		l := uint32(len(b.data))
		hdr[8] = byte(l)
		hdr[9] = byte(l >> 8)
		hdr[10] = byte(l >> 16)
		if _, err := f.Write(hdr[:]); err != nil {
			return err
		}
		_, err := f.Write(b.data)
		return err
	}

	// 0x11 turbo-speed data block: pilotPulse(2) + syncFirst(2) +
	// syncSecond(2) + zeroPulse(2) + onePulse(2) + pilotLen(2) +
	// usedBits(1) + pause(2) + length(3) + data.
	if len(b.data) >= 1<<24 {
		return fmt.Errorf("block too large for TZX 0x11: %d bytes", len(b.data))
	}
	var hdr [19]byte
	hdr[0] = 0x11
	binary.LittleEndian.PutUint16(hdr[1:3], b.pilotPulse)
	binary.LittleEndian.PutUint16(hdr[3:5], b.syncFirst)
	binary.LittleEndian.PutUint16(hdr[5:7], b.syncSecond)
	binary.LittleEndian.PutUint16(hdr[7:9], b.zeroPulse)
	binary.LittleEndian.PutUint16(hdr[9:11], b.onePulse)
	binary.LittleEndian.PutUint16(hdr[11:13], b.pilotLen)
	hdr[13] = usedBits
	binary.LittleEndian.PutUint16(hdr[14:16], b.pause)
	l := uint32(len(b.data))
	hdr[16] = byte(l)
	hdr[17] = byte(l >> 8)
	hdr[18] = byte(l >> 16)
	if _, err := f.Write(hdr[:]); err != nil {
		return err
	}
	_, err := f.Write(b.data)
	return err
}

// LoadTZX loads a TZX file into the tape player.
// TZX is a more comprehensive tape format than TAP, supporting:
// - Standard speed data blocks (ID 0x10)
// - Turbo speed data blocks (ID 0x11)
// - Pure tone (ID 0x12)
// - Pulse sequence (ID 0x13)
// - Pure data block (ID 0x14)
// - Pause/stop (ID 0x20)
// - Group start/end (ID 0x21/0x22)
// - Archive info (ID 0x32)
// - Text description (ID 0x30)
// - And more...
//
// This implementation handles the most common block types that are
// needed to load the vast majority of TZX files.
func (tp *TapePlayer) LoadTZX(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read TZX file: %w", err)
	}

	// Verify TZX header: "ZXTape!" + 0x1A
	if len(data) < 10 {
		return fmt.Errorf("TZX file too short")
	}
	if string(data[0:7]) != "ZXTape!" || data[7] != 0x1A {
		return fmt.Errorf("invalid TZX signature")
	}

	// Skip header (10 bytes: 7 signature + 1 EOF + 1 major + 1 minor)
	offset := 10

	tp.mu.Lock()
	defer tp.mu.Unlock()

	tp.blocks = nil

	// Label the outer loop so truncation `break`s actually abort parsing
	// rather than fall through to the next iteration with a stale offset
	// (which used to spin forever on malformed files).
parseLoop:
	for offset < len(data) {
		blockID := data[offset]
		offset++

		switch blockID {
		case 0x10: // Standard speed data block
			if offset+4 > len(data) {
				break parseLoop
			}
			pause := binary.LittleEndian.Uint16(data[offset : offset+2])
			length := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
			offset += 4
			if offset+int(length) > len(data) {
				break parseLoop
			}
			blockData := data[offset : offset+int(length)]
			offset += int(length)
			tp.blocks = append(tp.blocks, tapeBlock{data: blockData, pause: pause})

		case 0x11: // Turbo speed data block
			if offset+18 > len(data) {
				break parseLoop
			}
			// Read turbo parameters
			pilotPulse := binary.LittleEndian.Uint16(data[offset : offset+2])
			syncFirst := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
			syncSecond := binary.LittleEndian.Uint16(data[offset+4 : offset+6])
			zeroPulse := binary.LittleEndian.Uint16(data[offset+6 : offset+8])
			onePulse := binary.LittleEndian.Uint16(data[offset+8 : offset+10])
			pilotLen := binary.LittleEndian.Uint16(data[offset+10 : offset+12])
			usedBits := data[offset+12]
			pause := binary.LittleEndian.Uint16(data[offset+13 : offset+15])
			length := uint32(data[offset+15]) | uint32(data[offset+16])<<8 | uint32(data[offset+17])<<16
			offset += 18
			if offset+int(length) > len(data) {
				break parseLoop
			}
			blockData := data[offset : offset+int(length)]
			offset += int(length)
			tp.blocks = append(tp.blocks, tapeBlock{
				data:       blockData,
				pause:      pause,
				pilotPulse: pilotPulse,
				syncFirst:  syncFirst,
				syncSecond: syncSecond,
				zeroPulse:  zeroPulse,
				onePulse:   onePulse,
				pilotLen:   pilotLen,
				usedBits:   usedBits,
				turbo:      true,
			})

		case 0x12: // Pure tone
			if offset+4 > len(data) {
				break parseLoop
			}
			tp.blocks = append(tp.blocks, tapeBlock{
				kind:      kindPureTone,
				toneLen:   binary.LittleEndian.Uint16(data[offset : offset+2]),
				toneCount: binary.LittleEndian.Uint16(data[offset+2 : offset+4]),
			})
			offset += 4

		case 0x13: // Pulse sequence
			if offset+1 > len(data) {
				break parseLoop
			}
			count := int(data[offset])
			offset++
			if offset+count*2 > len(data) {
				break parseLoop
			}
			seq := make([]uint32, count)
			for i := 0; i < count; i++ {
				seq[i] = uint32(binary.LittleEndian.Uint16(data[offset+i*2 : offset+i*2+2]))
			}
			tp.blocks = append(tp.blocks, tapeBlock{kind: kindPulseSeq, seq: seq})
			offset += count * 2

		case 0x14: // Pure data block
			if offset+10 > len(data) {
				break parseLoop
			}
			zeroPulse := binary.LittleEndian.Uint16(data[offset : offset+2])
			onePulse := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
			usedBits := data[offset+4]
			pause := binary.LittleEndian.Uint16(data[offset+5 : offset+7])
			length := uint32(data[offset+7]) | uint32(data[offset+8])<<8 | uint32(data[offset+9])<<16
			offset += 10
			if offset+int(length) > len(data) {
				break parseLoop
			}
			blockData := data[offset : offset+int(length)]
			offset += int(length)
			tp.blocks = append(tp.blocks, tapeBlock{
				data:       blockData,
				pause:      pause,
				zeroPulse:  zeroPulse,
				onePulse:   onePulse,
				usedBits:   usedBits,
				turbo:      true,
				pilotLen:   0, // No pilot
				pilotPulse: 0,
				syncFirst:  0,
				syncSecond: 0,
			})

		case 0x15: // Direct recording
			if offset+8 > len(data) {
				break parseLoop
			}
			sampleT := binary.LittleEndian.Uint16(data[offset : offset+2])
			pause := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
			usedBits := data[offset+4]
			length := uint32(data[offset+5]) | uint32(data[offset+6])<<8 | uint32(data[offset+7])<<16
			offset += 8
			if offset+int(length) > len(data) {
				break parseLoop
			}
			tp.blocks = append(tp.blocks, tapeBlock{
				kind:          kindDirect,
				data:          data[offset : offset+int(length)],
				sampleTStates: sampleT,
				pause:         pause,
				usedBits:      usedBits,
			})
			offset += int(length)

		case 0x18, 0x19: // CSW recording / generalised data block
			// Both carry a 4-byte payload length, then a stream that decodes
			// to explicit pulse durations. A malformed one is skipped rather
			// than aborting the load — the length field still tells us how
			// far to advance.
			if offset+4 > len(data) {
				break parseLoop
			}
			length := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			if length < 0 || offset+length > len(data) {
				break parseLoop
			}
			payload := data[offset : offset+length]
			offset += length

			var seq []uint32
			var pause uint16
			var derr error
			if blockID == 0x18 {
				seq, pause, derr = decodeCSW(payload)
			} else {
				seq, pause, derr = decodeGeneralisedData(payload)
			}
			if derr != nil {
				slog.Warn("TZX: skipping block", "id", fmt.Sprintf("%#02x", blockID), "err", derr)
				break
			}
			if len(seq) > 0 {
				tp.blocks = append(tp.blocks, tapeBlock{
					kind: kindPulseSeq, seq: seq, pause: pause,
				})
			}

		case 0x20: // Pause (or, when zero, stop the tape)
			if offset+2 > len(data) {
				break parseLoop
			}
			tp.blocks = append(tp.blocks, tapeBlock{
				kind:  kindPause,
				pause: binary.LittleEndian.Uint16(data[offset : offset+2]),
			})
			offset += 2

		case 0x21: // Group start
			if offset+1 > len(data) {
				break parseLoop
			}
			nameLen := int(data[offset])
			offset += 1 + nameLen

		case 0x22: // Group end
			// No data

		case 0x23: // Jump to block (signed, relative to this block)
			if offset+2 > len(data) {
				break parseLoop
			}
			tp.blocks = append(tp.blocks, tapeBlock{
				kind:       kindJump,
				jumpOffset: int16(binary.LittleEndian.Uint16(data[offset : offset+2])),
			})
			offset += 2

		case 0x24: // Loop start
			if offset+2 > len(data) {
				break parseLoop
			}
			tp.blocks = append(tp.blocks, tapeBlock{
				kind:      kindLoopStart,
				loopCount: binary.LittleEndian.Uint16(data[offset : offset+2]),
			})
			offset += 2

		case 0x25: // Loop end
			tp.blocks = append(tp.blocks, tapeBlock{kind: kindLoopEnd})

		case 0x2A: // Stop the tape if in 48K mode
			tp.blocks = append(tp.blocks, tapeBlock{kind: kindStop48K})
			offset += 4

		case 0x2B: // Set signal level
			if offset+5 > len(data) {
				break parseLoop
			}
			tp.blocks = append(tp.blocks, tapeBlock{
				kind:  kindSetLevel,
				level: data[offset+4]&1 != 0,
			})
			offset += 5

		case 0x30: // Text description
			if offset+1 > len(data) {
				break parseLoop
			}
			textLen := int(data[offset])
			offset += 1 + textLen

		case 0x31: // Message block
			if offset+2 > len(data) {
				break parseLoop
			}
			// time := data[offset]
			textLen := int(data[offset+1])
			offset += 2 + textLen

		case 0x32: // Archive info
			if offset+2 > len(data) {
				break parseLoop
			}
			blockLen := binary.LittleEndian.Uint16(data[offset : offset+2])
			offset += 2 + int(blockLen)

		case 0x33: // Hardware type
			if offset+1 > len(data) {
				break parseLoop
			}
			count := int(data[offset])
			offset += 1 + count*3

		case 0x35: // Custom info block
			if offset+20 > len(data) {
				break parseLoop
			}
			length := binary.LittleEndian.Uint32(data[offset+16 : offset+20])
			offset += 20 + int(length)

		case 0x5A: // Glue block
			offset += 9

		default:
			// Unknown block — try to skip using length field
			if offset+4 <= len(data) {
				length := binary.LittleEndian.Uint32(data[offset : offset+4])
				offset += 4 + int(length)
			} else {
				offset = len(data) // Can't parse further
			}
		}
	}

	if len(tp.blocks) == 0 {
		return fmt.Errorf("no loadable blocks found in TZX file")
	}

	tp.blockIdx = 0
	tp.playing = false
	return nil
}
