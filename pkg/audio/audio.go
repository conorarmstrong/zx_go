package audio

import (
	"fmt"
	_ "io" // Used implicitly by io.Reader interface
	"sync"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/oto/v2"
)

const (
	// Audio parameters
	SampleRate   = 44100
	ChannelCount = 2     // Stereo
	BitDepth     = 16    // 16-bit
	BufferSize   = 4096  // Buffer size in samples
)

// AudioSystem handles audio output for the ZX Spectrum beeper
type AudioSystem struct {
	context    *oto.Context
	player     oto.Player
	reader     *audioReader
	mutex      sync.Mutex
	
	// Beeper state
	speakerState atomic.Bool
	lastToggle   time.Time
	
	// Audio generation
	running     bool
	sampleTime  float64
	clockSpeed  float64 // Z80 clock speed in Hz
}

// audioReader implements io.Reader to generate audio samples
type audioReader struct {
	audioSys   *AudioSystem
	buffer     []byte
	lastSample int16 // For low-pass filtering
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
		audioSys: as,
		buffer:   make([]byte, BufferSize*ChannelCount*2), // 2 bytes per sample
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
	for i := 0; i < samples; i++ {
		// Simple beeper simulation: much lower amplitude to reduce high-pitched harshness
		var sample int16
		if ar.audioSys.speakerState.Load() {
			sample = 3000 // Reduced amplitude to avoid harsh high-pitched sounds
		} else {
			sample = -3000 // Reduced negative amplitude
		}
		
		// Apply low-pass filter to remove high-frequency components
		// This creates a smooth transition and reduces harsh high-pitched sounds
		alpha := float64(0.1) // Low-pass filter coefficient (0.1 = strong filtering)
		filteredSample := int16(float64(ar.lastSample)*(1.0-alpha) + float64(sample)*alpha)
		ar.lastSample = filteredSample
		sample = filteredSample
		
		// Stereo output (same signal on both channels)
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

// GetSpeakerState returns the current speaker state
func (as *AudioSystem) GetSpeakerState() bool {
	return as.speakerState.Load()
}


// Close closes the audio system and releases resources
func (as *AudioSystem) Close() error {
	as.Stop()
	
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