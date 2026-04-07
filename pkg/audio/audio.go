package audio

import (
	"encoding/binary"
	"fmt"
	_ "io" // Used implicitly by io.Reader interface
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/oto/v2"
)

// AYSource is the minimal interface that the audio system needs from an AY
// sound chip implementation. It is satisfied by *ay.AY but kept as an
// interface here so the audio package does not need to import the ay package
// (which would create a dependency cycle through the ULA).
type AYSource interface {
	MixInto(buf []int16)
}

const (
	// Audio parameters
	SampleRate   = 44100
	ChannelCount = 2     // Stereo
	BitDepth     = 16    // 16-bit
	BufferSize   = 4096  // Buffer size in samples
)

// AudioSystem handles audio output for the ZX Spectrum beeper and (on 128K+
// machines) the AY-3-8912 sound chip.
type AudioSystem struct {
	context    *oto.Context
	player     oto.Player
	reader     *audioReader
	mutex      sync.Mutex

	// Beeper state
	speakerState atomic.Bool
	lastToggle   time.Time

	// Optional AY-3-8912 source. When non-nil, AY samples are mixed into the
	// beeper output stream.
	ay   AYSource
	ayMu sync.RWMutex

	// WAV recording state. When recFile is non-nil the audio reader appends
	// each generated sample to the file as 16-bit mono PCM. recSamples
	// counts the total samples written so we can finalise the WAV header on
	// stop. We use uint64 because uint32 overflows the WAV data-size field
	// (and corrupts the file) after about 27 hours at 44.1kHz; the writer
	// stops appending once we hit the header's uint32 limit.
	recMu      sync.Mutex
	recFile    *os.File
	recSamples uint64
	recScratch []byte // reused per writeRecording call to avoid allocs

	// Audio generation
	running     bool
	sampleTime  float64
	clockSpeed  float64 // Z80 clock speed in Hz
}

// audioReader implements io.Reader to generate audio samples
type audioReader struct {
	audioSys   *AudioSystem
	buffer     []byte
	mixBuffer  []int16 // scratch space for AY mixing
	lastSample int16   // For low-pass filtering
}

// New creates a new AudioSystem instance
func New() (*AudioSystem, error) {
	// Initialize audio context
	ctx, ready, err := oto.NewContext(SampleRate, ChannelCount, oto.FormatSignedInt16LE)
	if err != nil {
		return nil, fmt.Errorf("failed to create audio context: %w", err)
	}
	
	// Wait for audio context to be ready
	<-ready
	
	as := &AudioSystem{
		context:    ctx,
		clockSpeed: 3500000.0, // 3.5MHz for ZX Spectrum
		lastToggle: time.Now(),
	}
	
	// Create audio reader
	as.reader = &audioReader{
		audioSys:  as,
		buffer:    make([]byte, BufferSize*ChannelCount*2), // 2 bytes per sample
		mixBuffer: make([]int16, BufferSize),
	}
	
	// Create audio player
	as.player = ctx.NewPlayer(as.reader)
	
	return as, nil
}

// Read implements io.Reader for the audioReader
func (ar *audioReader) Read(p []byte) (n int, err error) {
	bytesToRead := len(p)
	if bytesToRead > len(ar.buffer) {
		bytesToRead = len(ar.buffer)
	}

	// Generate audio samples
	samples := bytesToRead / (ChannelCount * 2) // 2 bytes per sample

	// Resize the mono mix scratch buffer if needed.
	if cap(ar.mixBuffer) < samples {
		ar.mixBuffer = make([]int16, samples)
	}
	mixBuf := ar.mixBuffer[:samples]

	// Step 1: build the beeper baseline into the mix buffer.
	for i := 0; i < samples; i++ {
		var sample int16
		if ar.audioSys.speakerState.Load() {
			sample = 3000 // Reduced amplitude to avoid harsh high-pitched sounds
		} else {
			sample = -3000 // Reduced negative amplitude
		}

		// Apply low-pass filter to remove high-frequency components.
		// This creates a smooth transition and reduces harsh high-pitched
		// sounds.
		alpha := float64(0.1)
		filteredSample := int16(float64(ar.lastSample)*(1.0-alpha) + float64(sample)*alpha)
		ar.lastSample = filteredSample
		mixBuf[i] = filteredSample
	}

	// Step 2: if an AY chip is attached, mix its output on top of the beeper
	// stream.
	ar.audioSys.ayMu.RLock()
	ay := ar.audioSys.ay
	ar.audioSys.ayMu.RUnlock()
	if ay != nil {
		ay.MixInto(mixBuf)
	}

	// Step 2b: WAV recording — append the mono mix to the recording file
	// before stereo expansion.
	ar.audioSys.writeRecording(mixBuf)

	// Step 3: emit the mono mix as stereo bytes.
	for i := 0; i < samples; i++ {
		sample := mixBuf[i]
		baseIdx := i * ChannelCount * 2
		// Left channel
		ar.buffer[baseIdx] = byte(sample & 0xFF)
		ar.buffer[baseIdx+1] = byte((sample >> 8) & 0xFF)
		// Right channel
		ar.buffer[baseIdx+2] = byte(sample & 0xFF)
		ar.buffer[baseIdx+3] = byte((sample >> 8) & 0xFF)
	}

	copy(p, ar.buffer[:bytesToRead])
	return bytesToRead, nil
}

// Start begins audio output
func (as *AudioSystem) Start() error {
	as.mutex.Lock()
	defer as.mutex.Unlock()
	
	if as.running {
		return nil
	}
	
	as.running = true
	as.sampleTime = 0
	
	// Start the player
	as.player.Play()
	
	return nil
}

// Stop stops audio output
func (as *AudioSystem) Stop() {
	as.mutex.Lock()
	defer as.mutex.Unlock()
	
	as.running = false
	if as.player != nil {
		as.player.Pause()
	}
}

// SetSpeakerState updates the speaker state (called when port 0xFE bit 4 changes)
func (as *AudioSystem) SetSpeakerState(state bool) {
	as.speakerState.Store(state)
}

// SetAY attaches an AY-3-8912 source whose output will be mixed into the
// beeper stream. Pass nil to detach.
func (as *AudioSystem) SetAY(ay AYSource) {
	as.ayMu.Lock()
	defer as.ayMu.Unlock()
	as.ay = ay
}

// GetSpeakerState returns the current speaker state
func (as *AudioSystem) GetSpeakerState() bool {
	return as.speakerState.Load()
}


// Close closes the audio system and releases resources
func (as *AudioSystem) Close() error {
	as.Stop()
	_ = as.StopRecording()

	// Note: oto v2 context doesn't have a Close method
	// The context will be cleaned up when the program exits
	return nil
}

// SetVolume adjusts the audio volume (0.0 to 1.0)
func (as *AudioSystem) SetVolume(volume float64) {
	// Volume control would be implemented here
	// For now, we'll keep it simple and not implement volume control
}

// Reset resets the audio system to initial state
func (as *AudioSystem) Reset() {
	as.speakerState.Store(false)
	as.mutex.Lock()
	defer as.mutex.Unlock()

	as.lastToggle = time.Now()
	as.sampleTime = 0
}

// StartRecording opens path for writing and begins capturing the mixed mono
// audio output as a 16-bit PCM WAV file at SampleRate. If a recording is
// already in progress it is finalised first.
func (as *AudioSystem) StartRecording(path string) error {
	if err := as.StopRecording(); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create wav file: %w", err)
	}
	// Write a placeholder WAV header. We'll seek back and patch the size
	// fields when the recording stops.
	if err := writeWavHeader(f, 0, 1, SampleRate, 16); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write wav header: %w", err)
	}
	as.recMu.Lock()
	as.recFile = f
	as.recSamples = 0
	as.recMu.Unlock()
	return nil
}

// maxWavSamples is the largest number of mono 16-bit samples we can capture
// before either of the WAV header's uint32 size fields would overflow.
// dataSize = samples*2 must fit in uint32, AND riffSize = 36 + dataSize
// must also fit — so the binding limit is (maxUint32 - 36) / 2. About
// 13.6 hours at 44.1kHz; writeRecording stops appending past this.
const maxWavSamples uint64 = (0xFFFFFFFF - 36) / 2

// StopRecording finalises any in-progress recording. It is a no-op if there
// is no active recording.
func (as *AudioSystem) StopRecording() error {
	as.recMu.Lock()
	f := as.recFile
	samples := as.recSamples
	as.recFile = nil
	as.recSamples = 0
	as.recMu.Unlock()
	if f == nil {
		return nil
	}
	// Patch the WAV header with the actual sample count, then close.
	// recSamples is capped during recording so this conversion is safe.
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return err
	}
	if err := writeWavHeader(f, uint32(samples), 1, SampleRate, 16); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// IsRecording reports whether a WAV recording is currently in progress.
func (as *AudioSystem) IsRecording() bool {
	as.recMu.Lock()
	defer as.recMu.Unlock()
	return as.recFile != nil
}

// writeRecording appends a mono sample buffer to the active WAV file. Called
// from the audio reader on the audio playback goroutine.
func (as *AudioSystem) writeRecording(samples []int16) {
	as.recMu.Lock()
	defer as.recMu.Unlock()
	if as.recFile == nil {
		return
	}
	// Truncate the buffer at the WAV size limit so recSamples never crosses
	// the uint32 data-size boundary. Once full we silently drop further
	// samples; StopRecording will write a valid (capped) header.
	remaining := maxWavSamples - as.recSamples
	if remaining == 0 {
		return
	}
	n := uint64(len(samples))
	if n > remaining {
		n = remaining
		samples = samples[:n]
	}

	need := int(n) * 2
	if cap(as.recScratch) < need {
		as.recScratch = make([]byte, need)
	}
	buf := as.recScratch[:need]
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(s))
	}
	if _, err := as.recFile.Write(buf); err != nil {
		// Drop the recording silently on write failure — the playback path
		// must not block on disk I/O retries.
		_ = as.recFile.Close()
		as.recFile = nil
		return
	}
	as.recSamples += n
}

// writeWavHeader writes a 44-byte canonical PCM WAV header. sampleCount is
// the number of mono frames in the file.
func writeWavHeader(f *os.File, sampleCount uint32, channels uint16, sampleRate uint32, bitsPerSample uint16) error {
	byteRate := sampleRate * uint32(channels) * uint32(bitsPerSample) / 8
	blockAlign := channels * bitsPerSample / 8
	dataSize := sampleCount * uint32(blockAlign)
	riffSize := 36 + dataSize

	hdr := make([]byte, 44)
	copy(hdr[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(hdr[4:8], riffSize)
	copy(hdr[8:12], []byte("WAVE"))
	copy(hdr[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(hdr[16:20], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(hdr[20:22], 1)  // PCM format
	binary.LittleEndian.PutUint16(hdr[22:24], channels)
	binary.LittleEndian.PutUint32(hdr[24:28], sampleRate)
	binary.LittleEndian.PutUint32(hdr[28:32], byteRate)
	binary.LittleEndian.PutUint16(hdr[32:34], blockAlign)
	binary.LittleEndian.PutUint16(hdr[34:36], bitsPerSample)
	copy(hdr[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(hdr[40:44], dataSize)

	_, err := f.Write(hdr)
	return err
}
