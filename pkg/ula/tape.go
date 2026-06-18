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
	// dataPulses is the number of pulses in `pulses` that make up the block's
	// pilot/sync/data (i.e. everything before the trailing inter-block pause).
	// dataConsumed becomes true once the real-time player has played past them
	// — the block's bytes are "off the tape" and only the silent gap remains.
	// The fast-load trap uses this: if the current block's data is already
	// consumed (we're mid-pause), NextBlock skips it and returns the next
	// block, so a trap firing during a real-time-loaded block's trailing pause
	// doesn't hand back the just-finished block (the cause of a spurious
	// "R Tape loading error" when the header loads real-time and the program
	// then traps).
	dataPulses   int
	dataConsumed bool
	// pulseBlock is the block index the current `pulses` were generated from,
	// or -1 if none. The fast-load trap (NextBlock) advances blockIdx without
	// touching the pulse state, so Update must detect blockIdx != pulseBlock
	// and regenerate — otherwise it replays the previous block's pulses (or
	// skips a block), which desyncs any real-time / custom (turbo) loader that
	// takes over after a trapped block and causes "R Tape loading error".
	pulseBlock int
}

type tapeBlock struct {
	data []byte

	// Optional pause (ms) appended after this block. Used by TZX loader.
	pause uint16

	// Turbo / pure-data parameters (TZX block IDs 0x11/0x14). When turbo is
	// false the block is treated as a standard-speed (TAP-style) block.
	turbo      bool
	pilotPulse uint16
	syncFirst  uint16
	syncSecond uint16
	zeroPulse  uint16
	onePulse   uint16
	pilotLen   uint16
	usedBits   byte
}

// NewTapePlayer creates an empty tape player.
func NewTapePlayer() *TapePlayer {
	return &TapePlayer{pulseBlock: -1}
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
	tp.blockIdx = 0
	tp.pulseBlock = -1
	tp.dataConsumed = false
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

// SaveTAP writes the currently loaded blocks back to a TAP file. Each block
// is emitted as a 16-bit little-endian length followed by the raw block
// bytes — the same wire format LoadTAP consumes. TZX-only metadata
// (per-block timing parameters, pause lengths) is dropped, since the TAP
// format has no place for it.
func (tp *TapePlayer) SaveTAP(path string) error {
	tp.mu.Lock()
	blocks := make([][]byte, len(tp.blocks))
	for i, b := range tp.blocks {
		// Copy so a concurrent mutator can't tear the bytes mid-write.
		cp := make([]byte, len(b.data))
		copy(cp, b.data)
		blocks[i] = cp
	}
	tp.mu.Unlock()

	if len(blocks) == 0 {
		return fmt.Errorf("no blocks to save")
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create TAP: %w", err)
	}
	defer func() { _ = f.Close() }()

	for _, data := range blocks {
		if len(data) > 0xFFFF {
			return fmt.Errorf("block too large for TAP format: %d bytes", len(data))
		}
		var lenBuf [2]byte
		binary.LittleEndian.PutUint16(lenBuf[:], uint16(len(data)))
		if _, err := f.Write(lenBuf[:]); err != nil {
			return fmt.Errorf("write TAP length: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("write TAP block: %w", err)
		}
	}
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
	tp.pulses, tp.dataPulses = tp.generatePulsesData(tp.blocks[tp.blockIdx])
	tp.dataConsumed = false
	tp.pulseBlock = tp.blockIdx
	tp.pulseIdx = 0
	tp.earBit = false
}

// Stop stops tape playback.
func (tp *TapePlayer) Stop() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.playing = false
}

// Resume re-enables playback WITHOUT restarting the current block (unlike
// Play, which regenerates the block's pulses from the start). Used by the
// loader-activity auto-pause: the tape is paused while the running program is
// not reading edges (e.g. a multi-load game's menu, or inter-block
// processing) so it doesn't advance past the next part, then resumed exactly
// where it left off when the loader starts reading again. Update() continues
// from the preserved pulse position (and re-generates only if a block boundary
// was crossed).
func (tp *TapePlayer) Resume() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if len(tp.blocks) == 0 || tp.blockIdx >= len(tp.blocks) {
		return
	}
	tp.playing = true
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
	tp.pulseBlock = -1
	tp.dataConsumed = false
}

// NextBlock returns the bytes of the next tape block (without the leading
// length word) and advances the block pointer. Used by the fast-load ROM trap.
// Returns nil if no further blocks are available.
func (tp *TapePlayer) NextBlock() []byte {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	// If the real-time player already played this block's data (we're in its
	// trailing pause), the block is spent — skip it so the trap returns the
	// next, unread block rather than the one just loaded in real time.
	if tp.dataConsumed && tp.blockIdx < len(tp.blocks) {
		tp.blockIdx++
		tp.dataConsumed = false
		tp.pulseBlock = -1 // force the real-time player to resync
	}
	if tp.blockIdx >= len(tp.blocks) {
		return nil
	}
	block := tp.blocks[tp.blockIdx].data
	tp.blockIdx++
	// Return a copy so callers can't mutate our internal state.
	out := make([]byte, len(block))
	copy(out, block)
	return out
}

// HasMoreBlocks returns true if at least one more block is available.
func (tp *TapePlayer) HasMoreBlocks() bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.blockIdx < len(tp.blocks)
}

// BlockSummary describes a single tape block, suitable for display in a
// browser dialog.
type BlockSummary struct {
	Index    int
	Length   int
	Type     string // "Header", "Data", "Turbo", "Pure data"
	FlagByte byte   // 0x00 for headers, 0xFF for data, etc.
	Title    string // Decoded BASIC/CODE/screen header name where available
}

// Blocks returns a snapshot of the tape's block list. The slice is freshly
// allocated and safe for the caller to keep.
func (tp *TapePlayer) Blocks() []BlockSummary {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	out := make([]BlockSummary, len(tp.blocks))
	for i, b := range tp.blocks {
		s := BlockSummary{Index: i, Length: len(b.data)}
		switch {
		case b.turbo && b.pilotLen == 0:
			s.Type = "Pure data"
		case b.turbo:
			s.Type = "Turbo"
		default:
			s.Type = "Data"
		}
		if len(b.data) > 0 {
			s.FlagByte = b.data[0]
			if b.data[0] == 0x00 && !b.turbo {
				s.Type = "Header"
				// 17-byte standard header: flag, type, name (10 chars), len, p1, p2, checksum
				if len(b.data) >= 13 {
					name := make([]byte, 0, 10)
					for _, c := range b.data[2:12] {
						if c < 32 || c > 126 {
							c = ' '
						}
						name = append(name, c)
					}
					s.Title = string(name)
				}
			}
		}
		out[i] = s
	}
	return out
}

// SeekToBlock positions the tape at the given block index. Playback is
// stopped (the caller can call Play afterwards). Out-of-range indices are
// clamped to the last playable block — clamping past the end would leave
// the tape in a non-playable state.
func (tp *TapePlayer) SeekToBlock(idx int) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if idx < 0 {
		idx = 0
	}
	if max := len(tp.blocks) - 1; idx > max {
		idx = max
	}
	if idx < 0 {
		idx = 0 // Empty tape: leave blockIdx at 0; Play will then no-op.
	}
	tp.blockIdx = idx
	tp.playing = false
	tp.pulses = nil
	tp.pulseBlock = -1
	tp.dataConsumed = false
	tp.pulseIdx = 0
}

// Update advances the tape state by the given number of T-states.
// Returns the current EAR bit value.
func (tp *TapePlayer) Update(tstates uint64) bool {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if !tp.playing {
		return false
	}

	// Resync if blockIdx was moved out from under us (the fast-load trap's
	// NextBlock advances it without touching the pulse state) or pulses were
	// never generated. Generate this block's pulses from its pilot so a
	// real-time/custom loader taking over after a trapped block reads the
	// correct block rather than replaying the previous one or skipping ahead.
	if tp.pulseBlock != tp.blockIdx {
		if tp.blockIdx >= len(tp.blocks) {
			tp.playing = false
			return false
		}
		tp.pulses, tp.dataPulses = tp.generatePulsesData(tp.blocks[tp.blockIdx])
		tp.dataConsumed = false
		tp.pulseBlock = tp.blockIdx
		tp.pulseIdx = 0
		tp.lastToggle = tp.tstate
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

	// Once the pilot/sync/data pulses are played, the block's bytes are off the
	// tape — only the trailing pause remains. Mark it so the trap skips this
	// block if it fires during the pause (see NextBlock).
	if tp.pulseIdx >= tp.dataPulses {
		tp.dataConsumed = true
	}

	// If we've exhausted all pulses, move to the next block.
	if tp.pulseIdx >= len(tp.pulses) {
		tp.blockIdx++
		if tp.blockIdx >= len(tp.blocks) {
			tp.playing = false
			return false
		}
		tp.pulses, tp.dataPulses = tp.generatePulsesData(tp.blocks[tp.blockIdx])
		tp.dataConsumed = false
		tp.pulseBlock = tp.blockIdx
		tp.pulseIdx = 0
		tp.lastToggle = tp.tstate
	}

	return tp.earBit
}

// generatePulses converts a tape block into a sequence of pulse durations (in T-states).
//
// Standard Spectrum (TAP / TZX 0x10) encoding:
//   - Pilot tone: 2168 T-states per pulse, 8063 pulses for header, 3223 for data
//   - Sync pulses: 667 then 735 T-states
//   - Data bits: 0 = 2x 855 T-states, 1 = 2x 1710 T-states
//
// TZX 0x11 (turbo) and 0x14 (pure data) blocks override these timings via the
// fields on tapeBlock; pure data blocks have pilotLen == 0 and so emit no
// pilot or sync.
func (tp *TapePlayer) generatePulses(blk tapeBlock) []uint16 {
	pulses, _ := tp.generatePulsesData(blk)
	return pulses
}

// generatePulsesData is generatePulses plus the count of pulses that precede the
// trailing inter-block pause (the pilot/sync/data pulses).
func (tp *TapePlayer) generatePulsesData(blk tapeBlock) (pulses []uint16, dataPulses int) {
	data := blk.data
	if len(data) == 0 {
		return nil, 0
	}

	// Resolve timings (turbo blocks override the defaults).
	pilotPulse := uint16(2168)
	syncFirst := uint16(667)
	syncSecond := uint16(735)
	zeroPulse := uint16(855)
	onePulse := uint16(1710)
	usedBits := byte(8)

	var pilotPulses int
	if blk.turbo {
		pilotPulse = blk.pilotPulse
		syncFirst = blk.syncFirst
		syncSecond = blk.syncSecond
		zeroPulse = blk.zeroPulse
		onePulse = blk.onePulse
		pilotPulses = int(blk.pilotLen)
		if blk.usedBits >= 1 && blk.usedBits <= 8 {
			usedBits = blk.usedBits
		}
	} else {
		pilotPulses = 3223 // Data block default
		if data[0] < 128 {
			pilotPulses = 8063 // Header block
		}
	}

	// Pre-allocate: pilot + 2 sync + 16 pulses per byte + pause padding.
	pulses = make([]uint16, 0, pilotPulses+2+len(data)*16+50)

	// Pilot tone
	for i := 0; i < pilotPulses; i++ {
		pulses = append(pulses, pilotPulse)
	}

	// Sync pulses (only if we emitted a pilot — pure data blocks have neither).
	if pilotPulses > 0 {
		pulses = append(pulses, syncFirst, syncSecond)
	}

	// Data bits. The last byte may have fewer than 8 valid bits (TZX usedBits).
	for i, b := range data {
		bits := 8
		if i == len(data)-1 {
			bits = int(usedBits)
		}
		for bit := 7; bit >= 8-bits; bit-- {
			if b&(1<<bit) != 0 {
				pulses = append(pulses, onePulse, onePulse)
			} else {
				pulses = append(pulses, zeroPulse, zeroPulse)
			}
		}
	}

	// Everything up to here is pilot/sync/data; the pause follows.
	dataPulses = len(pulses)

	// Trailing pause. TZX specifies a value in milliseconds; if zero we fall
	// back to the legacy ~1 second silence used for TAP playback. We split
	// the silence into uint16-sized chunks because pulse durations are
	// stored as uint16 T-states.
	pauseTStates := uint64(3500000) // ~1s default
	if blk.pause > 0 {
		// 3500 T-states per millisecond on a 3.5MHz Z80.
		pauseTStates = uint64(blk.pause) * 3500
	}
	for pauseTStates > 0 {
		chunk := uint16(65535)
		if pauseTStates < uint64(chunk) {
			chunk = uint16(pauseTStates)
		}
		pulses = append(pulses, chunk)
		pauseTStates -= uint64(chunk)
	}

	return pulses, dataPulses
}
