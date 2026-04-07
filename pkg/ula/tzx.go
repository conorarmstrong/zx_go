package ula

import (
	"encoding/binary"
	"fmt"
	"os"
)

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
			// pulseLen := binary.LittleEndian.Uint16(data[offset : offset+2])
			// pulseCount := binary.LittleEndian.Uint16(data[offset+2 : offset+4])
			offset += 4
			// Skipped — pure tone blocks are rare in practice

		case 0x13: // Pulse sequence
			if offset+1 > len(data) {
				break parseLoop
			}
			count := int(data[offset])
			offset++
			offset += count * 2 // Skip pulse lengths

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
			length := uint32(data[offset+5]) | uint32(data[offset+6])<<8 | uint32(data[offset+7])<<16
			offset += 8
			offset += int(length)

		case 0x20: // Pause/stop the tape
			if offset+2 > len(data) {
				break parseLoop
			}
			// pause := binary.LittleEndian.Uint16(data[offset : offset+2])
			offset += 2

		case 0x21: // Group start
			if offset+1 > len(data) {
				break parseLoop
			}
			nameLen := int(data[offset])
			offset += 1 + nameLen

		case 0x22: // Group end
			// No data

		case 0x23: // Jump to block
			offset += 2

		case 0x24: // Loop start
			offset += 2

		case 0x25: // Loop end
			// No data

		case 0x2A: // Stop the tape if in 48K mode
			offset += 4

		case 0x2B: // Set signal level
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
