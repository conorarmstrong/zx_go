package lores

import (
	"bytes"
	"testing"
)

// The LoRes layer has no counters of its own: everything it holds is a
// register the guest wrote. That does not make its capture trivial, because
// what those registers mean depends entirely on WHEN they are read. A
// raster-split program rewrites the scroll offsets, the mode bit and the clip
// window while the frame is being drawn, so the layer is almost never at the
// values anyone would write down as "the configuration". Capture it between
// two Copper writes and the state that must come back is a scroll offset that
// has wrapped, an address fold that is engaged, and a clip window part way
// round its four coordinates.
//
// These tests are written as a replay property — draw, capture, draw on,
// restore, draw on again, compare the pixels — rather than a field-by-field
// comparison, because a field-by-field test only checks the fields someone
// remembered to add.

// clipped is what a rejected pixel is left as. RenderScanline does not write
// the positions the clip window rejects (the caller pre-fills them with the
// layer below), so the sentinel makes the clip registers as visible in the
// output as the scroll registers are.
const clipped = 0xAA

// pattern is a bank 5 filled with a deterministic pseudorandom byte stream, so
// that neighbouring addresses differ in both nibbles. A repeating pattern
// would hide exactly the failures worth catching: a scroll offset restored one
// line out, or the radastan display-file bit restored the wrong way, both land
// on a different address and must land on a different byte.
func pattern() fakeBank5 {
	b5 := make([]byte, 16384)
	x := uint32(0x12345678)
	for i := range b5 {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b5[i] = byte(x)
	}
	return fakeBank5{b5}
}

// tick is one scanline's worth of the register churn a Copper raster-split
// program performs, and every step is a function of the register's CURRENT
// value. That is what gives the replay test its teeth: each line the layer
// draws after a restore depends on what the register held at the capture, so a
// register left behind does not corrupt one line, it corrupts every line after
// it.
//
// The periods are odd primes against the 137-step replay window rather than
// round numbers, and that is load-bearing. A boolean toggled an even number of
// times across the window arrives back at the value it was captured with, and
// a capture that dropped it would then replay identically: the mutation would
// not bite and the test would be measuring nothing. Over i in [0,137) the
// three periods below fire 5, 3 and 5 times.
// TestTheReplayWindowMovesEveryCapturedRegister is the guard on that
// arithmetic — if a period is ever changed, it says so.
func (c *Config) tick(i int) {
	c.ScrollX += 3 // wraps at 256, and flips the radastan nibble phase
	c.ScrollY += 5 // walks through the y>=192 and y>=96 address folds
	c.PaletteOffset = (c.PaletteOffset + 1) & 0x0F
	if i%31 == 0 {
		c.Radastan = !c.Radastan
	}
	if i%47 == 0 {
		c.Dfile = !c.Dfile
	}
	if i%29 == 0 {
		c.ULAPlus = !c.ULAPlus
	}
	// The clip window breathes inside fixed bounds instead of shrinking. A
	// window that closed would blank the layer part way through the replay
	// window and hide every later divergence behind an all-sentinel frame.
	// The bounds are chosen to sit inside the lines the window actually draws
	// (0..44 and 100..191): a clip coordinate the raster never reaches cannot
	// clip anything, and a test built on one proves nothing about it.
	c.ClipX1 = (c.ClipX1 + 1) % 5     // 0..4
	c.ClipX2 = 0xF0 + (c.ClipX2+1)%13 // 240..252
	c.ClipY1 = (c.ClipY1 + 1) % 7     // 0..6
	c.ClipY2 = 0xA0 + (c.ClipY2+1)%31 // 160..190
}

// run drives the layer forward the way the raster does: draw a scanline, apply
// one line of raster-split churn, move to the next line, wrapping into the
// following frame at the bottom of this one. It returns every pixel emitted,
// which is the observable this file compares.
//
// The raster position is a parameter rather than a field, because it belongs
// to the video timing, not to this layer — see state.go.
func run(c *Config, mem BankReader, firstLine, steps int) []byte {
	out := make([]byte, 0, steps*256)
	line := make([]byte, 256)
	for i := 0; i < steps; i++ {
		for j := range line {
			line[j] = clipped
		}
		c.RenderScanline(mem, (firstLine+i)%192, line, 256)
		out = append(out, line...)
		c.tick(i)
	}
	return out
}

// The replay window: 137 lines starting at line 100, so it runs to the bottom
// of the frame and on into the top of the next one. Resuming across the frame
// boundary is the case worth testing, because that is where a layer that only
// looks restored stops looking restored.
const (
	captureLine = 100
	replaySteps = 137
)

// primed returns the layer part way down a frame, after 100 lines of
// raster-split churn: the scroll registers have wrapped, the address folds are
// engaged, the mode bit has flipped several times and the clip window is mid
// cycle. It is deliberately not a state anyone would write by hand — a device
// captured at rest proves nothing about resuming.
func primed() *Config {
	c := &Config{
		ScrollX:       0x07,
		ScrollY:       0xC1, // already past the y>=192 fold
		PaletteOffset: 0x05,
		Radastan:      true,
		ClipX1:        0x00,
		ClipX2:        0xFF,
		ClipY1:        0x00,
		ClipY2:        0xBF,
	}
	run(c, pattern(), 0, 100)
	return c
}

// The property that matters: from a captured state, the layer must draw the
// pixels it drew the first time.
func TestReplayingFromCapturedStateRedrawsTheSamePixels(t *testing.T) {
	mem := pattern()
	c := primed()

	st := c.SaveState()
	first := run(c, mem, captureLine, replaySteps)

	if err := c.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := run(c, mem, captureLine, replaySteps)

	if !bytes.Equal(first, second) {
		t.Error("the LoRes layer drew different pixels on replay from the same captured state: " +
			"some part of the layer is not being captured")
	}
}

// The guard on the fixture, and the reason the test above means anything.
//
// A register that holds the same value at the end of the replay window as it
// held at the capture would survive not being restored at all: the restore
// would be a no-op and the replay would match. Every captured register must
// therefore have MOVED across the window, or the mutation that deletes it
// silently passes.
func TestTheReplayWindowMovesEveryCapturedRegister(t *testing.T) {
	c := primed()
	at := *c
	run(c, pattern(), captureLine, replaySteps)
	after := *c

	for _, f := range []struct {
		name string
		same bool
	}{
		{"Radastan", at.Radastan == after.Radastan},
		{"Dfile", at.Dfile == after.Dfile},
		{"ULAPlus", at.ULAPlus == after.ULAPlus},
		{"PaletteOffset", at.PaletteOffset == after.PaletteOffset},
		{"ScrollX", at.ScrollX == after.ScrollX},
		{"ScrollY", at.ScrollY == after.ScrollY},
		{"ClipX1", at.ClipX1 == after.ClipX1},
		{"ClipX2", at.ClipX2 == after.ClipX2},
		{"ClipY1", at.ClipY1 == after.ClipY1},
		{"ClipY2", at.ClipY2 == after.ClipY2},
	} {
		if f.same {
			t.Errorf("%s holds the same value at the end of the replay window as at the capture, "+
				"so a capture that dropped it would replay identically: the fixture is not "+
				"testing that register", f.name)
		}
	}
}

// The other half of the fixture guard: a register that has moved still proves
// nothing unless moving it changes the pixels. This is the trap that let an AY
// mutation pass while the noise LFSR was not being restored — the fixture had
// noise disabled, so the register never reached the output.
func TestEveryCapturedRegisterChangesWhatTheLayerDraws(t *testing.T) {
	mem := pattern()
	base := *primed()

	ref := base
	want := run(&ref, mem, captureLine, replaySteps)

	for _, f := range []struct {
		name  string
		tweak func(*Config)
	}{
		{"Radastan", func(c *Config) { c.Radastan = !c.Radastan }},
		{"Dfile", func(c *Config) { c.Dfile = !c.Dfile }},
		{"ULAPlus", func(c *Config) { c.ULAPlus = !c.ULAPlus }},
		{"PaletteOffset", func(c *Config) { c.PaletteOffset = (c.PaletteOffset + 1) & 0x0F }},
		{"ScrollX", func(c *Config) { c.ScrollX++ }},
		{"ScrollY", func(c *Config) { c.ScrollY++ }},
		{"ClipX1", func(c *Config) { c.ClipX1 = (c.ClipX1 + 1) % 5 }},
		{"ClipX2", func(c *Config) { c.ClipX2 = 0xF0 + (c.ClipX2+1)%13 }},
		{"ClipY1", func(c *Config) { c.ClipY1 = (c.ClipY1 + 1) % 7 }},
		{"ClipY2", func(c *Config) { c.ClipY2 = 0xA0 + (c.ClipY2+1)%31 }},
	} {
		alt := base
		f.tweak(&alt)
		if got := run(&alt, mem, captureLine, replaySteps); bytes.Equal(got, want) {
			t.Errorf("changing %s did not change a single pixel the layer draws, so the replay "+
				"test cannot tell whether it is restored: the fixture is wrong, not the device",
				f.name)
		}
	}
}

// A capture must not alias the live layer. The registry copies the blob too,
// but a device handing back a view of its own state is a bug worth catching
// where the device is.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	mem := pattern()
	c := primed()

	st := c.SaveState()
	blob := append([]byte(nil), st...)
	at := *c

	run(c, mem, captureLine, replaySteps)

	if !bytes.Equal(st, blob) {
		t.Error("the captured blob changed while the layer carried on drawing: the capture " +
			"aliases live state, so a rewind would arrive at the present")
	}
	if err := c.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if *c != at {
		t.Errorf("restored layer is %+v, want the captured %+v", *c, at)
	}
}

func TestStateIDIsStable(t *testing.T) {
	c := &Config{}
	if got := c.StateID(); got != "next.lores" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift",
			got, "next.lores")
	}
}

// A state that cannot be decoded must leave the layer exactly as it was. Half
// applying one is worse than refusing it: the machine carries on drawing, with
// a scroll offset from the capture and a clip window from the present.
func TestLoadStateRejectsMalformedInputWithoutHalfApplying(t *testing.T) {
	c := primed()
	before := *c
	good := c.SaveState()

	for _, bad := range []struct {
		name string
		blob []byte
	}{
		{"empty", nil},
		{"rubbish", []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"truncated capture", good[:len(good)/2]},
		{"capture with its tail lopped off", good[:len(good)-1]},
	} {
		if err := c.LoadState(bad.blob); err == nil {
			t.Errorf("%s: a malformed state blob must be reported, not accepted", bad.name)
		}
		if *c != before {
			t.Errorf("%s: the layer changed while rejecting a malformed state (%+v, want %+v)",
				bad.name, *c, before)
			*c = before
		}
	}
}
