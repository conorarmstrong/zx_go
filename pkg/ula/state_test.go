package ula

import (
	"bytes"
	"encoding/binary"
	"hash/fnv"
	"image"
	"image/color"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/audio"
	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/machinestate"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The ULA is the device where a partial capture is least visible and most
// damaging. Its guest-writable registers — the border colour, the speaker bit
// — are a handful of bytes; what actually decides the next frame is where the
// ULA is INSIDE the frame: which border changes have been recorded but not yet
// painted, which speaker toggles are queued for a waveform not yet
// synthesised, how far through the 16-frame flash cycle it is, what the frame
// origin (frameStartTstate) is that every T-state offset is measured from.
// None of that is readable by the guest and none of it appears in a .SNA or
// .Z80. Restore the registers alone and the machine runs, one frame off, with
// the border stripes on the wrong scanlines and the beeper phase shifted.
//
// These tests are therefore written as a replay property — capture, run,
// restore, run again, compare what an outside observer saw — and not as a
// field-by-field comparison, which only checks the fields someone remembered
// to add.

var _ machinestate.Device = (*ULA)(nil)

// newStateTestULA builds a 48K ULA with screen RAM full of real content and
// its T-state counter wired.
//
// The screen content matters: against a blank screen the scroll, flash and
// attribute paths all render identically whatever their state, and a test that
// cannot see a field cannot fail when that field's restore is deleted.
func newStateTestULA(t *testing.T) (*ULA, *uint64) {
	t.Helper()
	dir := "test_roms_ula_state"
	createTestROMs(t, dir)
	t.Cleanup(func() { cleanupTestROMs(dir) })

	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	page := mem.GetPage(5)
	for i := 0; i < 0x1800; i++ {
		page[i] = byte(i*7) ^ byte(i>>5)
	}
	for i := 0x1800; i < 0x1B00; i++ {
		// Every cell a different ink/paper/bright, and FLASH set on all of
		// them so the flash phase is visible in the picture.
		page[i] = byte(i*11) | 0x80
	}

	// The CPU's T-state counter. The emulator's frame loop runs it from ~0 to
	// the frame length and then wraps it (z80.go ExecuteFrame's trailing
	// `c.tstates -= tstatesEnd`), so the tests drive it the same way.
	var ts uint64
	mem.TStates = &ts
	return New(mem, keyboard.New()), &ts
}

// primeULA drives a freshly built ULA into a non-trivial mid-frame state: some
// way through the flash cycle, with border changes and port traffic already
// recorded for a frame that has NOT been composed yet.
//
// Stopping mid-frame is the point. A capture taken at a frame boundary would
// not prove the ULA's position within the frame is captured, which is the
// whole reason a rewind can land anywhere.
func primeULA(u *ULA, ts *uint64) {
	u.KempstonEnabled = true
	u.SetKempstonButton(KempstonRight|KempstonUp, true)
	u.SetULAScrollX(0x2B)
	u.SetULAScrollY(0x51)
	u.SetULAFineScrollX(true)
	u.TapeIn = true
	u.Mic = true
	u.port123BVal = 0x5A
	// The palette has no public setter today — it is loaded once by
	// initPalette — but it is still device state, so a restore has to bring it
	// back. Primed directly for want of a writer.
	u.palette[2] = color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xFF}
	u.palette[9] = color.RGBA{R: 0x44, G: 0x55, B: 0x66, A: 0xFF}

	// Twelve whole frames: enough that the flash counter is part-way through
	// its 16-frame cycle, so the flash toggle falls INSIDE the replayed window
	// below and a wrong counter shows up as a whole frame of swapped
	// ink/paper.
	driveFrames(u, ts, 12)

	// ...and now a partial frame. These writes sit in borderChanges and
	// audioEvents unpainted and unsynthesised, and the read moves the port-$FE
	// counter, all with the frame origin still at the previous frame's wrap.
	*ts = 1000
	u.WritePort(0x00FE, 0x13)
	*ts = 3000
	u.WritePort(0x00FE, 0x06)
	*ts = 4200
	_, _ = u.ReadPort(0x00FE)
}

func primedULA(t *testing.T) (*ULA, *uint64) {
	t.Helper()
	u, ts := newStateTestULA(t)
	primeULA(u, ts)
	return u, ts
}

// driveFrames runs the machine the way the emulator's frame loop does — port
// traffic at rising T-states through the frame, then the CPU counter wraps and
// the ULA composes — and returns everything an outside observer could see:
// every IN result, a digest of every composed frame, and the port-$FE read
// counter.
//
// The per-frame configuration changes come LAST, after the frame has been
// composed. That ordering is what gives the test its teeth: the values a
// capture is holding are the ones that paint the first replayed frame, before
// the schedule overwrites them. Reconfigure first and every restored
// configuration byte would be discarded before it could ever be seen.
func driveFrames(u *ULA, ts *uint64, frames int) []byte {
	log := new(bytes.Buffer)
	for f := 0; f < frames; f++ {
		// Border + speaker writes at four points down the frame, so the frame
		// carries a list of mid-frame changes rather than one final colour.
		for i, at := range []uint64{6000, 24000, 45000, 61000} {
			*ts = at
			u.WritePort(0x00FE, byte((f*3+i*5)&0x07)|byte((i&1)<<4))
		}
		// Reads: keyboard/EAR ($FE), Kempston ($1F), and an unattached port
		// whose value is the floating bus — which is computed from the frame
		// origin, so it reports where the ULA thinks it is in the frame.
		for _, at := range []uint64{14400, 30000, 52000} {
			*ts = at
			for _, p := range []uint16{0x00FE, 0x001F, 0x00FF} {
				v, ok := u.ReadPort(p)
				log.WriteByte(v)
				log.WriteByte(boolByte(ok))
			}
		}
		// End of frame: ExecuteFrame leaves the counter just past the frame
		// boundary, and only then does the machine compose.
		*ts = uint64(f % 5)
		writeFrameDigest(log, u.Render())

		// Next frame's configuration, as a NextReg-writing program and a user
		// at the menu would leave it.
		u.SetULAScrollX(byte(f*13 + 5))
		u.SetULAScrollY(byte(f*7 + 3))
		u.SetULAFineScrollX(f%2 == 0)
		u.SetULAOutputDisabled(f == 4)
		u.WritePort(0x00FF, byte(f%8)) // Timex SCLD video mode
		u.SetKempstonButton(KempstonFire, f%3 == 0)
		u.KempstonEnabled = f < 2
	}
	_ = binary.Write(log, binary.BigEndian, u.FEReadCount())
	return log.Bytes()
}

// writeFrameDigest folds a composed frame into the log. The size is part of the
// digest because the wide paths return a different image entirely.
func writeFrameDigest(w *bytes.Buffer, img *image.RGBA) {
	h := fnv.New64a()
	var dims [8]byte
	binary.BigEndian.PutUint32(dims[0:], uint32(img.Bounds().Dx()))
	binary.BigEndian.PutUint32(dims[4:], uint32(img.Bounds().Dy()))
	_, _ = h.Write(dims[:])
	_, _ = h.Write(img.Pix)
	_ = binary.Write(w, binary.BigEndian, h.Sum64())
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// The property that matters: from a captured state, the ULA must paint the
// frames it painted the first time and return the bytes it returned.
func TestReplayingFromCapturedStateReproducesTheSameFrames(t *testing.T) {
	u, ts := primedULA(t)

	st := u.SaveState()
	first := driveFrames(u, ts, 8)

	if err := u.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveFrames(u, ts, 8)

	if !bytes.Equal(first, second) {
		t.Error("the ULA produced a different picture / different port reads on replay " +
			"from the same captured state: some part of the frame position is not being captured")
	}
}

// The 640-pixel path. NextReg $68 bit 2 (the half-pixel horizontal scroll) is
// stored on every model but can only be SEEN here — at 320 pixels half a pixel
// is not representable and the render rounds it away — so without this test
// that bit's restore could be deleted and nothing would notice.
func TestReplayingFromCapturedStateReproducesTheSameWideFrames(t *testing.T) {
	u, ts := primedULA(t)
	u.SetNextRegs(&fakeNextRegs{})
	u.SetNextCompositor(wideCompositor{})

	st := u.SaveState()
	first := driveWideFrames(u, ts, 6)

	if err := u.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveWideFrames(u, ts, 6)

	if !bytes.Equal(first, second) {
		t.Error("the 640-pixel path replayed differently from the same captured state")
	}
}

// driveWideFrames is driveFrames plus the two things only the Next-wired paths
// expose: the 640-pixel frame (where the half-pixel scroll lands) and the
// port-$123B read-back latch.
func driveWideFrames(u *ULA, ts *uint64, frames int) []byte {
	log := new(bytes.Buffer)
	for f := 0; f < frames; f++ {
		*ts = 12000
		v, ok := u.ReadPort(0x123B) // the Layer 2 latch, read back
		log.WriteByte(v)
		log.WriteByte(boolByte(ok))

		log.Write(driveFrames(u, ts, 1))

		*ts = 64000
		u.WritePort(0x123B, byte(f*37))
	}
	return log.Bytes()
}

// The audio path.
//
// It is dead when no audio system is attached — flushAudioFrame returns at
// once — so none of the speaker/tape/DC state would move at all and a fixture
// without a sink would let every audio field's restore be deleted silently.
// A zero-value AudioSystem is a working sink (PushBeeperSamples only touches
// its own ring), but it hands nothing back, so what this test observes is the
// DC-blocking filter the frame's samples ran through on the way out: one pole
// of memory whose position encodes the waveform that just passed through it.
func TestReplayingFromCapturedStateReproducesTheSameAudio(t *testing.T) {
	u, ts := newStateTestULA(t)
	// Attached BEFORE priming: speaker toggles are only recorded as audio
	// events while a sink exists.
	u.audio = &audio.AudioSystem{}
	primeULA(u, ts)

	st := u.SaveState()
	first := driveAudioFrames(u, ts, 6)

	if err := u.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveAudioFrames(u, ts, 6)

	if !bytes.Equal(first, second) {
		t.Error("the beeper waveform replayed differently from the same captured state: " +
			"some part of the audio path's position is not being captured")
	}
}

func driveAudioFrames(u *ULA, ts *uint64, frames int) []byte {
	log := new(bytes.Buffer)
	for f := 0; f < frames; f++ {
		// A square wave whose edges move from frame to frame, so the frame the
		// capture was taken in is not interchangeable with any other.
		for i, at := range []uint64{2000, 17000, 33333, 48000, 60000} {
			*ts = at + uint64(f*37)
			val := byte(0x00)
			if (f+i)%2 == 0 {
				val = 0x10 // speaker bit
			}
			u.WritePort(0x00FE, val|byte((f+i)&0x07))
		}
		*ts = uint64(f % 5)
		u.Render() // synthesises the frame and pushes it through the DC blocker
		for _, s := range dcProbe(u) {
			_ = binary.Write(log, binary.BigEndian, s)
		}
		// The turbo-load mute and the A/B filter switch are both live controls;
		// exercise them after the frame they governed, as with the video
		// configuration in driveFrames.
		u.SetFastLoad(f == 2)
		u.SetDCBlockEnabled(f != 3)
	}
	return log.Bytes()
}

// dcProbe reads the audio path's position by pushing a fixed step through the
// DC blocker and returning what comes out. The filter is one pole of memory
// over everything the ULA has already emitted, so this observes the waveform
// without needing the audio system to hand the samples back.
//
// It advances the filter, which is a real mutation — but an identical one in
// both runs, which is all a replay comparison requires.
func dcProbe(u *ULA) []int16 {
	probe := make([]int16, 8)
	for i := range probe {
		probe[i] = int16(4000 * (i%2*2 - 1))
	}
	u.dc.process(probe)
	return probe
}

// The tape path: the EAR line, and the T-state the tape was last advanced to.
//
// The tape player is its own device with its own position, and this test only
// captures the ULA — so the replay mounts a second, identical player at the
// same point and restores the ULA on top of it. The order matters:
// SetTapePlayer re-syncs lastTapeTstate to "now", so it has to happen before
// LoadState, not after.
func TestReplayingFromCapturedStateReproducesTheSameTapeEAR(t *testing.T) {
	u, ts := newStateTestULA(t)
	u.audio = &audio.AudioSystem{} // so the EAR transitions are recorded too
	tapePath := createTestTAP(t, t.TempDir())

	mount := func() {
		// Late in the frame, so the first read of the replay window (just
		// after the counter wraps) does NOT advance the tape and therefore
		// reports the restored EAR level rather than a freshly computed one.
		*ts = 60000
		tp := NewTapePlayer()
		if err := tp.LoadTAP(tapePath); err != nil {
			t.Fatalf("LoadTAP: %v", err)
		}
		tp.Play()
		u.SetTapePlayer(tp)
	}

	mount()
	u.TapeIn = true // mid-pulse: the EAR line is high

	st := u.SaveState()
	first := driveTapeFrames(u, ts, 4)

	mount()
	if err := u.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveTapeFrames(u, ts, 4)

	if !bytes.Equal(first, second) {
		t.Error("the tape EAR stream replayed differently from the same captured state")
	}
}

// driveTapeFrames samples port $FE the way an edge-timed loader does: often,
// at rising T-states, starting immediately after the frame wrap.
func driveTapeFrames(u *ULA, ts *uint64, frames int) []byte {
	log := new(bytes.Buffer)
	for f := 0; f < frames; f++ {
		for at := uint64(3); at < 69000; at += 499 {
			*ts = at
			v, _ := u.ReadPort(0x00FE)
			log.WriteByte(v & 0x40) // the EAR bit
		}
		*ts = uint64(f % 5)
		u.Render()
		for _, s := range dcProbe(u) {
			_ = binary.Write(log, binary.BigEndian, s)
		}
	}
	return log.Bytes()
}

// LastFrame composes lazily when nothing has been rendered yet, and returns
// the stored frame when something has. Whether a machine has rendered is
// therefore observable — and getting it wrong on restore does not just show a
// stale picture, it skips a Render, and on the Next a Render is where the
// Copper runs.
func TestReplayingFromCapturedStateReproducesTheLazyFirstFrame(t *testing.T) {
	u, ts := newStateTestULA(t)
	// Deliberately NOT rendered: this is a machine that has been configured
	// and stepped but never composed, which is exactly the state a rewind to
	// the first instants of a run lands in.
	u.SetULAScrollY(0x20)
	*ts = 8000
	u.WritePort(0x00FE, 0x05)

	st := u.SaveState()
	first := driveWithLastFrame(u, ts, 4)

	if err := u.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveWithLastFrame(u, ts, 4)

	if !bytes.Equal(first, second) {
		t.Error("the lazy first frame replayed differently: whether the machine had " +
			"already composed a frame is not being captured")
	}
}

func driveWithLastFrame(u *ULA, ts *uint64, frames int) []byte {
	log := new(bytes.Buffer)
	writeFrameDigest(log, u.LastFrame())
	log.Write(driveFrames(u, ts, frames))
	return log.Bytes()
}

// A capture must not alias the live device. The registry copies the blob too,
// but a device handing back a view of its own slices is a bug worth catching
// where it would be made.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	u, ts := primedULA(t)

	st := u.SaveState()
	before := append([]byte(nil), st...)

	// Everything the ULA appends to or clears in a frame — the border change
	// list, the audio event list — is rewritten several times over by this.
	driveFrames(u, ts, 5)

	if !bytes.Equal(st, before) {
		t.Error("the captured state changed while the machine kept running: " +
			"SaveState handed back a view of live device state")
	}
}

func TestStateIDIsStable(t *testing.T) {
	u, _ := newStateTestULA(t)
	if got := u.StateID(); got != "ula" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "ula")
	}
}

// A malformed blob must be reported and must leave the machine exactly as it
// was. A half-applied state is the worst outcome available: the machine runs,
// and the caller believes the rewind worked.
func TestLoadStateRejectsRubbishWithoutHalfApplying(t *testing.T) {
	u, ts := primedULA(t)

	st := u.SaveState()
	want := driveFrames(u, ts, 3)

	if err := u.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, bad := range [][]byte{
		{0xDE, 0xAD, 0xBE, 0xEF},
		st[:len(st)/2], // a truncated capture: a real failure mode
		{},
	} {
		if err := u.LoadState(bad); err == nil {
			t.Errorf("a malformed state blob (%d bytes) must be reported, not applied", len(bad))
		}
	}

	if got := driveFrames(u, ts, 3); !bytes.Equal(want, got) {
		t.Error("a rejected LoadState changed the machine: the state was applied in part")
	}
}

// Mic (port $FE bit 3, the tape-save output) has no read-back path anywhere in
// this emulator, so no replay can observe it. It is captured all the same —
// it is guest-written state, and the rewind of a machine mid-SAVE has to bring
// it back — and this is the one place it can be checked at all.
func TestMicRoundTrips(t *testing.T) {
	u, _ := newStateTestULA(t)
	u.Mic = true
	st := u.SaveState()
	u.Mic = false
	if err := u.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !u.Mic {
		t.Error("Mic did not survive the round trip")
	}
}

// wideCompositor is a transparent Next render stack: it passes the ULA's own
// scanline through untouched (the shared stubCompositor draws nothing, which
// would black out the inner screen) and reports an 80-column tilemap, so the
// ULA takes its 640-pixel path — the only one where the half-pixel scroll
// exists.
type wideCompositor struct{ stubCompositor }

func (wideCompositor) ComposeScanline(y int, ulaRGBA, dst []byte) { copy(dst, ulaRGBA) }
func (wideCompositor) ComposeScanlineRange(y int, ulaRGBA, dst []byte, x0, x1 int) {
	copy(dst[x0*4:x1*4], ulaRGBA[x0*4:x1*4])
}
func (wideCompositor) TilemapIs80Col() bool { return true }
