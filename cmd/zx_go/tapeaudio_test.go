package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
)

// writeTwoBlockTAP writes a minimal 2-block TAP (a header + a data block) so a
// loaded TapePlayer reports HasMoreBlocks()==true at block 0.
func writeTwoBlockTAP(t *testing.T) string {
	t.Helper()
	var data []byte
	put := func(block []byte) {
		var l [2]byte
		binary.LittleEndian.PutUint16(l[:], uint16(len(block)))
		data = append(data, l[:]...)
		data = append(data, block...)
	}
	put([]byte{0x00, 0x03, 0x41, 0x42, 0x43, 0x44, 0x45}) // header
	put(append([]byte{0xFF}, make([]byte, 19)...))        // data
	path := filepath.Join(t.TempDir(), "two.tap")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTapeAudioMuted pins the audio-muting policy during a fast-tape load:
// audio is muted for the WHOLE load (so it does not blip on/off at every
// inter-block gap — the "stutter" during a multi-block load), but only while
// fast-tape is enabled. Once the loader is idle (auto-paused) or fast-tape is
// off, audio is live so the game's music plays.
func TestTapeAudioMuted(t *testing.T) {
	emu, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	tp := ula.NewTapePlayer()
	if err := tp.LoadTAP(writeTwoBlockTAP(t)); err != nil {
		t.Fatal(err)
	}
	emu.ula.SetTapePlayer(tp)

	// No tape playing yet (just loaded) -> not muted.
	emu.fastTape.Store(true)
	if emu.tapeAudioMuted() {
		t.Error("idle (not playing) tape should not mute audio")
	}

	// Actively loading (playing, blocks remaining) with fast-tape on -> muted
	// for the whole load, regardless of the per-tick read activity (so it
	// does not blip on/off at each inter-block gap).
	tp.Play()
	if !emu.tapeAudioMuted() {
		t.Error("fast-tape load in progress should mute audio across the whole load")
	}

	// Fast-tape disabled -> real-time load with authentic loading sound, no mute.
	emu.fastTape.Store(false)
	if emu.tapeAudioMuted() {
		t.Error("with fast-tape off the loading sound must be audible (no mute)")
	}

	// Loader idle / auto-paused (tape stopped) -> not muted, so music plays.
	emu.fastTape.Store(true)
	tp.Stop()
	if emu.tapeAudioMuted() {
		t.Error("a paused tape (gameplay) must not mute audio — music needs to play")
	}
}
