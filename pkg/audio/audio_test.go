package audio

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// fakeSystem produces an AudioSystem instance that's safe to use
// without an oto context — we exercise the queue logic only, not the
// playback path.
func fakeSystem() *AudioSystem {
	return &AudioSystem{}
}

// fakeReader builds an audioReader wired to as with buffers sized
// for `samples` mono samples — enough to drive Read() once without
// touching oto. Lets us test the mixing stages in Read directly.
func fakeReader(as *AudioSystem, samples int) *audioReader {
	return &audioReader{
		audioSys:  as,
		buffer:    make([]byte, samples*ChannelCount*2),
		mixBuffer: make([]int16, samples),
	}
}

// fakeDACSource records that MixInto was called and applies a
// constant contribution. Used to prove SetDAC + Read wiring runs
// MixInto exactly once per Read call with the buffer the audio
// system intends to expose.
type fakeDACSource struct {
	contrib int16
	calls   int
}

func (f *fakeDACSource) MixInto(buf []int16) {
	f.calls++
	for i := range buf {
		buf[i] += f.contrib
	}
}

// TestSetDACSourceMixedInRead verifies the production playback
// path actually pulls from a DACSource: SetDAC attaches it, Read
// pulls beeper samples (silence here), then calls dac.MixInto on
// the same mixBuf before stereo expansion. A regression in the
// ULA.EnableAudio wiring or in audioReader.Read would fail this.
func TestSetDACSourceMixedInRead(t *testing.T) {
	as := fakeSystem()
	r := fakeReader(as, 4)

	src := &fakeDACSource{contrib: 1234}
	as.SetDAC(src)

	out := make([]byte, 4*ChannelCount*2)
	n, err := r.Read(out)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != len(out) {
		t.Fatalf("Read returned %d bytes, want %d", n, len(out))
	}
	if src.calls != 1 {
		t.Errorf("DAC MixInto calls = %d, want 1", src.calls)
	}

	// Decode the stereo output. Beeper queue is empty so popBeeper
	// returns zeros; no AY; only DAC contributes — each mono sample
	// should equal contrib (1234), expanded to both stereo channels.
	for i := 0; i < 4; i++ {
		base := i * ChannelCount * 2
		gotLE := int16(uint16(out[base]) | uint16(out[base+1])<<8)
		if gotLE != 1234 {
			t.Errorf("sample %d L = %d, want 1234", i, gotLE)
		}
		gotRE := int16(uint16(out[base+2]) | uint16(out[base+3])<<8)
		if gotRE != 1234 {
			t.Errorf("sample %d R = %d, want 1234", i, gotRE)
		}
	}
}

// TestSetDACNilSkipsMix verifies the nil guard: detaching DAC means
// MixInto isn't called. Important because Read runs on the playback
// goroutine and a nil-deref there would crash the program with no
// useful stack.
func TestSetDACNilSkipsMix(t *testing.T) {
	as := fakeSystem()
	r := fakeReader(as, 2)

	src := &fakeDACSource{contrib: 9999}
	as.SetDAC(src)
	as.SetDAC(nil) // detach

	out := make([]byte, 2*ChannelCount*2)
	if _, err := r.Read(out); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if src.calls != 0 {
		t.Errorf("DAC MixInto calls after detach = %d, want 0", src.calls)
	}
}

// TestPushAndPopRoundTrip verifies that samples pushed by the
// emulation goroutine come back out in order from the playback path.
func TestPushAndPopRoundTrip(t *testing.T) {
	as := fakeSystem()

	in := []int16{1, 2, 3, 4, 5}
	as.PushBeeperSamples(in)

	out := make([]int16, len(in))
	as.popBeeperSamples(out)

	for i := range in {
		if out[i] != in[i] {
			t.Errorf("sample %d: got %d, want %d", i, out[i], in[i])
		}
	}
}

// TestPopUnderflowDecaysToSilence verifies that when the queue is drained
// and more samples are requested than available, the unfilled slots continue
// from the last delivered value (so there's no jump into the underrun) and
// then decay toward 0. A sustained underrun must reach ~silence rather than
// hold a flat DC plateau — holding flat DC is what clicked when the signal
// stepped back on resume.
func TestPopUnderflowDecaysToSilence(t *testing.T) {
	as := fakeSystem()
	as.PushBeeperSamples([]int16{100, 200, 300})

	out := make([]int16, 6)
	as.popBeeperSamples(out)

	// Real samples drain first; the first underrun slot continues from the
	// last real sample (continuous — no step into the gap).
	if out[0] != 100 || out[1] != 200 || out[2] != 300 {
		t.Fatalf("drained samples = %v, want [100 200 300 ...]", out[:3])
	}
	if out[3] != 300 {
		t.Errorf("first underrun slot = %d, want 300 (continuous with last real sample)", out[3])
	}
	// Then it decays toward 0 (strictly shrinking, no flat plateau).
	if out[4] >= out[3] || out[5] >= out[4] {
		t.Errorf("underrun not decaying toward silence: %v", out[3:])
	}

	// A long underrun must fade essentially to silence.
	long := make([]int16, 4000)
	as.popBeeperSamples(long)
	if v := long[len(long)-1]; v < -2 || v > 2 {
		t.Errorf("after a long underrun, tail = %d, want ~0 (faded to silence)", v)
	}
}

// TestPopFromEmptyHoldsZero verifies that the very first underrun
// (before any samples have been pushed) outputs zeros rather than
// garbage from uninitialised memory.
func TestPopFromEmptyHoldsZero(t *testing.T) {
	as := fakeSystem()
	out := make([]int16, 4)
	for i := range out {
		out[i] = 0x7FFF // poison
	}
	as.popBeeperSamples(out)
	for i, v := range out {
		if v != 0 {
			t.Errorf("sample %d: got %d, want 0", i, v)
		}
	}
}

// TestQueueOverflowDropsOldest verifies that pushing more samples than
// the ring buffer can hold drops the oldest entries and keeps the
// newest, since the playback goroutine has fallen behind and we'd
// rather glitch than grow latency.
func TestQueueOverflowDropsOldest(t *testing.T) {
	as := fakeSystem()

	// Push two ring-buffers worth of distinguishable samples in
	// one go. The first half should be overwritten by the second.
	const burst = queueCapacity * 2
	in := make([]int16, burst)
	for i := range in {
		in[i] = int16(i % 32767)
	}
	as.PushBeeperSamples(in)

	// Drain everything and verify the first sample is from the
	// SECOND half of the input (the oldest got dropped).
	out := make([]int16, queueCapacity)
	as.popBeeperSamples(out)

	expectedFirst := int16((burst - queueCapacity) % 32767)
	if out[0] != expectedFirst {
		t.Errorf("after overflow: out[0] = %d, want %d (oldest should be dropped)", out[0], expectedFirst)
	}
}

// TestResetClearsQueue verifies that Reset drops any buffered samples
// and replaces them with the silence pre-fill cushion. The first
// queuePrefill samples drained after Reset must all be zero.
func TestResetClearsQueue(t *testing.T) {
	as := fakeSystem()
	as.PushBeeperSamples([]int16{1, 2, 3})
	as.Reset()

	out := make([]int16, 3)
	for i := range out {
		out[i] = 0x7FFF // poison
	}
	as.popBeeperSamples(out)
	for i, v := range out {
		if v != 0 {
			t.Errorf("after Reset: sample %d = %d, want 0", i, v)
		}
	}
}

// ========================================================================
// WAV recording tests: header shape, lifecycle state, and the finalisation
// step that writes the sample count back to the header.
// ========================================================================

// decodeWavHeader pulls the fields we care about out of the 44-byte
// canonical PCM WAV header writeWavHeader emits.
type wavHeader struct {
	riff          string
	riffSize      uint32
	wave          string
	fmtChunkID    string
	fmtChunkSize  uint32
	audioFormat   uint16
	channels      uint16
	sampleRate    uint32
	byteRate      uint32
	blockAlign    uint16
	bitsPerSample uint16
	dataChunkID   string
	dataSize      uint32
}

func decodeWavHeader(t *testing.T, b []byte) wavHeader {
	t.Helper()
	if len(b) < 44 {
		t.Fatalf("header too short: %d bytes", len(b))
	}
	return wavHeader{
		riff:          string(b[0:4]),
		riffSize:      binary.LittleEndian.Uint32(b[4:8]),
		wave:          string(b[8:12]),
		fmtChunkID:    string(b[12:16]),
		fmtChunkSize:  binary.LittleEndian.Uint32(b[16:20]),
		audioFormat:   binary.LittleEndian.Uint16(b[20:22]),
		channels:      binary.LittleEndian.Uint16(b[22:24]),
		sampleRate:    binary.LittleEndian.Uint32(b[24:28]),
		byteRate:      binary.LittleEndian.Uint32(b[28:32]),
		blockAlign:    binary.LittleEndian.Uint16(b[32:34]),
		bitsPerSample: binary.LittleEndian.Uint16(b[34:36]),
		dataChunkID:   string(b[36:40]),
		dataSize:      binary.LittleEndian.Uint32(b[40:44]),
	}
}

// TestWriteWavHeaderShape pins the canonical PCM-WAV header layout.
// Any media player that opens the file must see exactly this byte
// arrangement — RIFF/WAVE/fmt /data chunk IDs, audioFormat=1 (PCM),
// derived byteRate + blockAlign + riffSize.
func TestWriteWavHeaderShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	const sampleCount uint32 = 100
	const channels uint16 = 1
	const rate uint32 = 44100
	const bps uint16 = 16

	if err := writeWavHeader(f, sampleCount, channels, rate, bps); err != nil {
		t.Fatalf("writeWavHeader: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 44 {
		t.Fatalf("header file = %d bytes, want 44", len(b))
	}

	h := decodeWavHeader(t, b)
	dataSize := sampleCount * uint32(channels) * uint32(bps) / 8
	wantBlock := channels * bps / 8
	wantByteRate := rate * uint32(channels) * uint32(bps) / 8

	if h.riff != "RIFF" || h.wave != "WAVE" || h.fmtChunkID != "fmt " || h.dataChunkID != "data" {
		t.Errorf("chunk IDs wrong: %+v", h)
	}
	if h.audioFormat != 1 {
		t.Errorf("audioFormat = %d, want 1 (PCM)", h.audioFormat)
	}
	if h.fmtChunkSize != 16 {
		t.Errorf("fmtChunkSize = %d, want 16", h.fmtChunkSize)
	}
	if h.channels != channels {
		t.Errorf("channels = %d, want %d", h.channels, channels)
	}
	if h.sampleRate != rate {
		t.Errorf("sampleRate = %d, want %d", h.sampleRate, rate)
	}
	if h.byteRate != wantByteRate {
		t.Errorf("byteRate = %d, want %d", h.byteRate, wantByteRate)
	}
	if h.blockAlign != wantBlock {
		t.Errorf("blockAlign = %d, want %d", h.blockAlign, wantBlock)
	}
	if h.bitsPerSample != bps {
		t.Errorf("bitsPerSample = %d, want %d", h.bitsPerSample, bps)
	}
	if h.dataSize != dataSize {
		t.Errorf("dataSize = %d, want %d", h.dataSize, dataSize)
	}
	if h.riffSize != 36+dataSize {
		t.Errorf("riffSize = %d, want %d", h.riffSize, 36+dataSize)
	}
}

// TestWriteWavHeaderStereo16 covers the same header on a different
// shape — confirms blockAlign / byteRate scale with channel count.
func TestWriteWavHeaderStereo16(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if err := writeWavHeader(f, 50, 2, 48000, 16); err != nil {
		t.Fatalf("writeWavHeader: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := decodeWavHeader(t, b)

	if h.channels != 2 {
		t.Errorf("channels = %d, want 2", h.channels)
	}
	if h.blockAlign != 4 {
		t.Errorf("blockAlign = %d, want 4 (2ch * 16bit/8)", h.blockAlign)
	}
	if h.byteRate != 48000*4 {
		t.Errorf("byteRate = %d, want %d", h.byteRate, 48000*4)
	}
	// 50 stereo 16-bit samples = 50 * 4 = 200 bytes data.
	if h.dataSize != 200 {
		t.Errorf("dataSize = %d, want 200", h.dataSize)
	}
}

// TestIsRecordingInitiallyFalse confirms IsRecording on a fresh
// AudioSystem returns false, before any Start.
func TestIsRecordingInitiallyFalse(t *testing.T) {
	as := fakeSystem()
	if as.IsRecording() {
		t.Errorf("IsRecording on fresh system = true, want false")
	}
}

// TestStartRecordingLifecycle exercises the full Start→IsRecording→
// Stop→IsRecording cycle and verifies StopRecording finalises the
// file with a valid header.
func TestStartRecordingLifecycle(t *testing.T) {
	as := fakeSystem()
	path := filepath.Join(t.TempDir(), "rec.wav")

	if err := as.StartRecording(path); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	if !as.IsRecording() {
		t.Errorf("IsRecording after Start = false, want true")
	}
	// File should exist with a 44-byte header (zero samples so far).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after Start: %v", err)
	}
	if info.Size() < 44 {
		t.Errorf("file size after Start = %d, want >= 44", info.Size())
	}

	if err := as.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	if as.IsRecording() {
		t.Errorf("IsRecording after Stop = true, want false")
	}

	// Header should still be present and parseable after stop.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 44 {
		t.Fatalf("post-stop file = %d bytes, want >= 44", len(b))
	}
	h := decodeWavHeader(t, b)
	if h.riff != "RIFF" || h.wave != "WAVE" {
		t.Errorf("post-stop header IDs wrong: %+v", h)
	}
}

// TestStopRecordingNoOp confirms StopRecording with no active
// recording is a clean no-op (returns nil, doesn't panic). Start
// itself calls Stop first, so this path runs every time.
func TestStopRecordingNoOp(t *testing.T) {
	as := fakeSystem()
	if err := as.StopRecording(); err != nil {
		t.Errorf("StopRecording with no active rec: err = %v, want nil", err)
	}
	if as.IsRecording() {
		t.Errorf("IsRecording after no-op Stop = true")
	}
}

// TestStartRecordingFinalisesPriorRecording — calling StartRecording
// twice on different paths must finalise the first file (close it
// with a proper header) before opening the second.
func TestStartRecordingFinalisesPriorRecording(t *testing.T) {
	as := fakeSystem()
	dir := t.TempDir()
	p1 := filepath.Join(dir, "first.wav")
	p2 := filepath.Join(dir, "second.wav")

	if err := as.StartRecording(p1); err != nil {
		t.Fatalf("StartRecording p1: %v", err)
	}
	if err := as.StartRecording(p2); err != nil {
		t.Fatalf("StartRecording p2: %v", err)
	}

	// First file must still be readable, with a valid header.
	b, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("read p1 after re-Start: %v", err)
	}
	if len(b) < 44 {
		t.Errorf("p1 = %d bytes, want >= 44", len(b))
	}
	h := decodeWavHeader(t, b)
	if h.riff != "RIFF" {
		t.Errorf("p1 lost RIFF marker after re-Start: %q", h.riff)
	}

	// Active recording is now p2.
	if !as.IsRecording() {
		t.Errorf("IsRecording after second Start = false, want true")
	}
	_ = as.StopRecording()
}

// TestWriteRecordingAppendsSamples verifies writeRecording appends
// samples and StopRecording back-patches the sample count into the
// header (bytes 4 and 40).
func TestWriteRecordingAppendsSamples(t *testing.T) {
	as := fakeSystem()
	path := filepath.Join(t.TempDir(), "rec.wav")
	if err := as.StartRecording(path); err != nil {
		t.Fatal(err)
	}

	// Push 8 samples through the private write path. Each sample is
	// 2 bytes (16-bit mono) so we expect dataSize == 16 in the
	// finalised header.
	in := []int16{1, -1, 2, -2, 3, -3, 4, -4}
	as.writeRecording(in)

	if err := as.StopRecording(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := decodeWavHeader(t, b)
	if h.dataSize != 16 {
		t.Errorf("dataSize after 8 samples = %d, want 16", h.dataSize)
	}
	if h.riffSize != 36+16 {
		t.Errorf("riffSize after Stop = %d, want %d", h.riffSize, 36+16)
	}

	// Sample bytes must be little-endian.
	want := make([]byte, len(in)*2)
	for i, s := range in {
		binary.LittleEndian.PutUint16(want[i*2:], uint16(s))
	}
	if !bytes.Equal(b[44:44+len(want)], want) {
		t.Errorf("sample bytes mismatch:\n  got  %x\n  want %x", b[44:44+len(want)], want)
	}
}

// TestWriteRecordingNoOpWhenStopped — writeRecording with no active
// recording must not panic or open a file.
func TestWriteRecordingNoOpWhenStopped(t *testing.T) {
	as := fakeSystem()
	// No StartRecording call.
	as.writeRecording([]int16{1, 2, 3})
	if as.IsRecording() {
		t.Errorf("writeRecording shouldn't activate recording")
	}
}

// TestPrefillSilenceProvidesCushion verifies that a fresh AudioSystem
// already has queuePrefill silent samples queued up, so the first few
// consumer pulls don't drain the queue and trigger DC underflow (without the
// cushion, the producer's 50Hz/882-sample pushes lag the consumer's ~43Hz
// pulls, so every pull is a partial buffer padded with underrun silence).
func TestPrefillSilenceProvidesCushion(t *testing.T) {
	as := fakeSystem()
	as.prefillSilence()

	if as.queueSize != queuePrefill {
		t.Fatalf("after prefillSilence: queueSize = %d, want %d", as.queueSize, queuePrefill)
	}

	out := make([]int16, queuePrefill)
	as.popBeeperSamples(out)
	for i, v := range out {
		if v != 0 {
			t.Errorf("prefill sample %d = %d, want 0 (silence)", i, v)
			break
		}
	}
}

// TestStartRecording_OpenError verifies StartRecording propagates an
// os.Create failure as a wrapped "create wav file" error.
func TestStartRecording_OpenError(t *testing.T) {
	as := fakeSystem()
	// Path points at a directory that doesn't exist → os.Create
	// fails with a path-error.
	err := as.StartRecording("/no/such/directory/that/should/not/exist/out.wav")
	if err == nil {
		t.Fatal("StartRecording to unwritable path should return error")
	}
}

// TestWriteRecording_MaxSamplesCap covers writeRecording's maxWavSamples
// truncation, the remaining==0 early-return, and the recScratch grow path.
func TestWriteRecording_MaxSamplesCap(t *testing.T) {
	as := fakeSystem()
	dir := t.TempDir()
	path := dir + "/cap.wav"
	if err := as.StartRecording(path); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}
	// One sample slot left before the WAV size fields would overflow.
	as.recMu.Lock()
	as.recSamples = maxWavSamples - 1
	as.recMu.Unlock()

	// Three samples offered → only one is written (truncation path).
	as.writeRecording([]int16{0x1111, 0x2222, 0x3333})
	as.recMu.Lock()
	got := as.recSamples
	as.recMu.Unlock()
	if got != maxWavSamples {
		t.Errorf("recSamples = %d, want %d (capped)", got, maxWavSamples)
	}

	// Now full: remaining==0 → early return, no change.
	as.writeRecording([]int16{0x4444})
	as.recMu.Lock()
	got = as.recSamples
	as.recMu.Unlock()
	if got != maxWavSamples {
		t.Errorf("after full, recSamples = %d, want %d", got, maxWavSamples)
	}
	if err := as.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
}
