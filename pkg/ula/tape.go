package ula

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
)

// TapePlayer handles TAP file playback by toggling the EAR bit.
type TapePlayer struct {
	mu       sync.Mutex
	blocks   []tapeBlock
	blockIdx int
	playing  bool

	// Timing state (in T-states)
	tstate     uint64
	lastToggle uint64
	earBit     bool

	// Current pulse sequence being played
	pulses   []uint16
	pulseIdx int
}

type tapeBlock struct {
	data []byte
}

// NewTapePlayer creates an empty tape player.
func NewTapePlayer() *TapePlayer {
	return &TapePlayer{}
}

// LoadTAP loads a TAP file into the tape player.
func (tp *TapePlayer) LoadTAP(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read TAP file: %w", err)
	}

	tp.mu.Lock()
	defer tp.mu.Unlock()

	tp.blocks = nil
	offset := 0
	for offset+2 <= len(data) {
		blockLen := int(binary.LittleEndian.Uint16(data[offset : offset+2]))
		offset += 2
		if offset+blockLen > len(data) {
			break
		}
		tp.blocks = append(tp.blocks, tapeBlock{data: data[offset : offset+blockLen]})
		offset += blockLen
	}

	if len(tp.blocks) == 0 {
		return fmt.Errorf("no valid blocks found in TAP file")
	}

	tp.blockIdx = 0
	tp.playing = false
	return nil
}

// Play starts tape playback from the current block.
func (tp *TapePlayer) Play() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if len(tp.blocks) == 0 || tp.blockIdx >= len(tp.blocks) {
		return
	}
	tp.playing = true
	tp.pulses = tp.generatePulses(tp.blocks[tp.blockIdx].data)
	tp.pulseIdx = 0
	tp.earBit = false
}

// Stop stops tape playback.
func (tp *TapePlayer) Stop() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.playing = false
}

// IsPlaying returns whether the tape is playing.
func (tp *TapePlayer) IsPlaying() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.playing
}

// BlockCount returns the number of blocks in the tape.
func (tp *TapePlayer) BlockCount() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return len(tp.blocks)
}

// CurrentBlock returns the current block index.
func (tp *TapePlayer) CurrentBlock() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.blockIdx
}

// Rewind resets the tape to the beginning.
func (tp *TapePlayer) Rewind() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.blockIdx = 0
	tp.playing = false
}

// Update advances the tape state by the given number of T-states.
// Returns the current EAR bit value.
func (tp *TapePlayer) Update(tstates uint64) bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if !tp.playing {
		return false
	}

	tp.tstate += tstates

	// Process pulses
	for tp.pulseIdx < len(tp.pulses) {
		pulseDuration := uint64(tp.pulses[tp.pulseIdx])
		if tp.tstate-tp.lastToggle >= pulseDuration {
			tp.earBit = !tp.earBit
			tp.lastToggle += pulseDuration
			tp.pulseIdx++
		} else {
			break
		}
	}

	// If we've exhausted all pulses, move to the next block
	if tp.pulseIdx >= len(tp.pulses) {
		tp.blockIdx++
		if tp.blockIdx >= len(tp.blocks) {
			tp.playing = false
			return false
		}
		tp.pulses = tp.generatePulses(tp.blocks[tp.blockIdx].data)
		tp.pulseIdx = 0
		tp.lastToggle = tp.tstate
	}

	return tp.earBit
}

// generatePulses converts a tape block into a sequence of pulse durations (in T-states).
// Standard Spectrum tape encoding:
//   - Pilot tone: 2168 T-states per pulse, 8063 pulses for header, 3223 for data
//   - Sync pulses: 667 then 735 T-states
//   - Data bits: 0 = 2x 855 T-states, 1 = 2x 1710 T-states
func (tp *TapePlayer) generatePulses(data []byte) []uint16 {
	if len(data) == 0 {
		return nil
	}

	var pulses []uint16

	// Determine pilot length from flag byte
	pilotPulses := 3223 // Data block default
	if data[0] < 128 {
		pilotPulses = 8063 // Header block
	}

	// Pilot tone
	for i := 0; i < pilotPulses; i++ {
		pulses = append(pulses, 2168)
	}

	// Sync pulses
	pulses = append(pulses, 667, 735)

	// Data bits
	for _, b := range data {
		for bit := 7; bit >= 0; bit-- {
			if b&(1<<bit) != 0 {
				pulses = append(pulses, 1710, 1710) // 1 bit
			} else {
				pulses = append(pulses, 855, 855) // 0 bit
			}
		}
	}

	// Pause between blocks (~1 second = ~3,500,000 T-states, capped to uint16 chunks)
	for i := 0; i < 50; i++ {
		pulses = append(pulses, 65535)
	}

	return pulses
}
