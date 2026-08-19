package sam

import (
	"bytes"
	"reflect"
	"testing"
)

// State capture for the SAM Coupé.
//
// Until this existed the SAM was the one machine in the line with no capture at
// all: cmd/zx_go's stateRegistry returned an empty registry for it, so rewind
// and time travel silently did nothing there and quick-save refused.
//
// The machine is five devices and a handful of ASIC latches. What makes it more
// than the latches is the same thing it is on every other controller here: the
// state no port reports. The WD1772's physical head position is distinct from
// its Track Register, its transfer buffer holds the sector mid-flight, and the
// SAA1099's six tone phases and two LFSRs decide the next sample. A capture
// that stopped at the readable registers would restore a machine that runs, at
// the right volume, playing a different note.
//
// These are written as replay properties where a replay is possible and named
// directly where it is not, following pkg/disciple.

// capturePoint is a fixture that moves some part of the machine off its
// power-on value, so the round-trip tests have something to observe.
type capturePoint struct {
	name  string
	build func(t *testing.T) *Machine
}

func machineWithASIC(t *testing.T) *Machine {
	t.Helper()
	m := newTestMachine(t)
	m.WritePort(0x00FE, 0x27) // BORDER: colour bits plus MIC
	m.WritePort(0x0AF8, 0x55) // CLUT entry 10
	m.WritePort(0x00F9, 0x40) // LINE interrupt target
	m.WritePort(0x00FA, 0x1F) // LMPR
	m.WritePort(0x00FB, 0x22) // HMPR
	m.WritePort(0x00FC, 0x63) // VMPR
	m.WritePort(0x0080, 0x11) // LEPR
	m.WritePort(0x0081, 0x02) // HEPR
	return m
}

var samCapturePoints = []capturePoint{
	{"asic latches", machineWithASIC},
	{"beeper mid-frame", func(t *testing.T) *Machine {
		m := machineWithASIC(t)
		beepAt(t, m, 1000, true)
		beepAt(t, m, 2000, false)
		beepAt(t, m, 3000, true)
		return m
	}},
	{"a settled dc filter", func(t *testing.T) *Machine {
		m := machineWithASIC(t)
		beepAt(t, m, 0, true)
		buf := make([]int16, SamplesPerFrame*2)
		m.GenerateAudioStereo(buf)
		return m
	}},
	{"frame counters", func(t *testing.T) *Machine {
		m := machineWithASIC(t)
		m.RunFrame()
		m.RunFrame()
		return m
	}},
	{"light pen", func(t *testing.T) *Machine {
		m := machineWithASIC(t)
		// A T-state inside the active display, so both pen registers latch a
		// beam position rather than the off-screen constant. ExecuteFrame
		// rebases the counter each frame, so the clock is moved directly
		// rather than through setFrameRel, whose frameStart = Tstates - offset
		// would underflow from the 0 the last frame left behind.
		m.frameStart = 0
		m.CPU.SetTstates(100*samCyclesPerLine + 200)
		// The register is selected by bit 8 of the port, not by the high byte:
		// penByte masks the address with 0x01FF, so 0x1AF8 reads LPEN, not HPEN.
		_, _ = m.ReadPort(0x00F8) // LPEN
		_, _ = m.ReadPort(0x01F8) // HPEN
		return m
	}},
	{"mid-frame raster", func(t *testing.T) *Machine {
		m := machineWithASIC(t)
		// Stopped half way down the frame, which is where a capture taken by
		// the rewind ring actually lands: the renderer has drawn the lines
		// above the beam and not the ones below it.
		m.frameStart = 0
		m.renderCursor = 0
		m.CPU.SetTstates(CyclesPerFrame / 2)
		m.WritePort(0x00FE, 0x01) // any port write flushes the raster to here
		return m
	}},
	{"a frame boundary with overshoot", func(t *testing.T) *Machine {
		m := machineWithASIC(t)
		// A real frame does not begin at T-state 0. ExecuteFrame overshoots its
		// budget by the length of whichever instruction crossed the boundary,
		// and the next frame starts from there.
		m.frameStart = 23
		m.CPU.SetTstates(23 + 5000)
		return m
	}},
	{"a hand-moved dc clamp", func(t *testing.T) *Machine {
		m := machineWithASIC(t)
		// The clamp has no writer beyond New, which leaves it at the speaker
		// amplitude for the life of the machine. It is still filter state and
		// still restored, so it is moved by hand — the only way it can be made
		// to differ between capture and restore.
		m.beeperDC.SetLimit(int32(beeperAmplitude) / 3)
		return m
	}},
}

// patternDisk builds an MGT image whose every byte is distinct enough that a
// read resuming from the wrong offset is visible in the stream.
func patternDisk(t *testing.T) *Disk {
	t.Helper()
	d := blankMGT()
	for i := range d.data {
		d.data[i] = byte(i*7 + i/512)
	}
	return d
}

func decodeMachineState(t *testing.T, blob []byte) machineState {
	t.Helper()
	var s machineState
	if err := decodeGob(blob, &s); err != nil {
		t.Fatalf("decoding machine state: %v", err)
	}
	return s
}

// The identifier is stored in every blob and must not drift.
func TestSAMStateIDsAreStable(t *testing.T) {
	m := newTestMachine(t)
	for _, tc := range []struct {
		got, want string
	}{
		{m.StateID(), "sam.asic"},
		{m.Mem.StateID(), "sam.memory"},
		{m.Kbd.StateID(), "sam.keyboard"},
		{m.SAA.StateID(), "sam.saa1099"},
		{m.FDC[0].StateID(), "sam.fdc1"},
		{m.FDC[1].StateID(), "sam.fdc2"},
	} {
		if tc.got != tc.want {
			t.Errorf("StateID = %q, want %q", tc.got, tc.want)
		}
	}
}

// The two drives must not share an identifier, or a registry holding both would
// restore whichever was applied last into both.
func TestTheTwoDrivesHaveDistinctIDs(t *testing.T) {
	m := newTestMachine(t)
	if m.FDC[0].StateID() == m.FDC[1].StateID() {
		t.Fatalf("both drives answer to %q", m.FDC[0].StateID())
	}
}

// Every captured field must be moved by at least one fixture, or the round-trip
// tests say nothing about it: a field no fixture moves round-trips whether or
// not it is restored, which is exactly how a dropped restore hides.
//
// This walks the wire struct by reflection rather than by a hand-written list,
// so a field added to the capture and forgotten here shows up as a field
// nothing moves.
func TestEveryCapturedASICFieldIsMovedBySomeFixture(t *testing.T) {
	base := reflect.ValueOf(decodeMachineState(t, newTestMachine(t).SaveState()))

	moved := map[string]string{}
	for _, cp := range samCapturePoints {
		got := reflect.ValueOf(decodeMachineState(t, cp.build(t).SaveState()))
		for i := 0; i < base.NumField(); i++ {
			name := base.Type().Field(i).Name
			if _, done := moved[name]; done {
				continue
			}
			if !reflect.DeepEqual(base.Field(i).Interface(), got.Field(i).Interface()) {
				moved[name] = cp.name
			}
		}
	}
	for i := 0; i < base.NumField(); i++ {
		name := base.Type().Field(i).Name
		if _, ok := moved[name]; !ok {
			t.Errorf("no fixture moves %s off its power-on value, so every round-trip test "+
				"passes for it whether or not it is restored", name)
		}
	}
}

// Every device round-trips: capture, disturb, restore, and the blob matches.
func TestEverySAMDeviceRoundTrips(t *testing.T) {
	for _, cp := range samCapturePoints {
		t.Run(cp.name, func(t *testing.T) {
			before := cp.build(t)
			want := before.SaveState()

			after := newTestMachine(t)
			if err := after.LoadState(want); err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			if got := after.SaveState(); !bytes.Equal(want, got) {
				t.Error("the ASIC state did not survive a round trip")
			}
		})
	}
}

// The audio path is the property that matters for the SAA and the beeper: from
// a captured state the machine must produce the sound it produced the first
// time. Comparing the waveform rather than the fields is what makes this
// independent of which fields anyone remembered to add.
func TestReplayingFromACaptureReproducesTheSameAudio(t *testing.T) {
	prime := func(m *Machine) {
		writeSAA(m, 0x1C, 0x02)
		writeSAA(m, 0x00, 0x9F) // channel 0: both sides, different levels
		writeSAA(m, 0x08, 0x40)
		writeSAA(m, 0x10, 0x35)
		writeSAA(m, 0x14, 0x01)
		writeSAA(m, 0x18, 0x03) // noise generator 0
		writeSAA(m, 0x15, 0x02) // noise on channel 1
		writeSAA(m, 0x01, 0x77)
		writeSAA(m, 0x1C, 0x01)
	}
	drive := func(m *Machine, frames int) []byte {
		var out bytes.Buffer
		for f := 0; f < frames; f++ {
			beepAt(t, m, uint64(f*137%CyclesPerFrame), f%2 == 0)
			buf := make([]int16, SamplesPerFrame*2)
			m.GenerateAudioStereo(buf)
			for _, s := range buf {
				out.WriteByte(byte(s))
				out.WriteByte(byte(s >> 8))
			}
		}
		return out.Bytes()
	}

	live := newTestMachine(t)
	prime(live)
	_ = drive(live, 2) // get the generators off their power-on phase

	saaBlob, asicBlob := live.SAA.SaveState(), live.SaveState()
	first := drive(live, 6)

	restored := newTestMachine(t)
	if err := restored.SAA.LoadState(saaBlob); err != nil {
		t.Fatalf("SAA LoadState: %v", err)
	}
	if err := restored.LoadState(asicBlob); err != nil {
		t.Fatalf("machine LoadState: %v", err)
	}
	second := drive(restored, 6)

	if !bytes.Equal(first, second) {
		t.Error("the audio replayed differently from the same capture: some part of the " +
			"sound path's position is not captured")
	}
}

// The SAA's tone phase is the clearest case of state no port reports. Restoring
// the registers alone puts the chip back at the right pitch and the wrong point
// in its cycle, which is a click on every rewind.
func TestTheSAAToneAndNoisePhasesAreCaptured(t *testing.T) {
	m := newTestMachine(t)
	writeSAA(m, 0x1C, 0x02)
	writeSAA(m, 0x00, 0x0F)
	writeSAA(m, 0x08, 0x40)
	writeSAA(m, 0x10, 0x05)
	writeSAA(m, 0x14, 0x01)
	writeSAA(m, 0x1C, 0x01)
	buf := make([]int16, SamplesPerFrame*2)
	m.GenerateAudioStereo(buf) // advance the phases off zero

	blob := m.SAA.SaveState()
	// A chip with the same registers but a fresh phase.
	fresh := newTestMachine(t)
	writeSAA(fresh, 0x1C, 0x02)
	writeSAA(fresh, 0x00, 0x0F)
	writeSAA(fresh, 0x08, 0x40)
	writeSAA(fresh, 0x10, 0x05)
	writeSAA(fresh, 0x14, 0x01)
	writeSAA(fresh, 0x1C, 0x01)

	freshBuf := make([]int16, 64)
	fresh.SAA.GenerateStereo(freshBuf)

	if err := fresh.SAA.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	restoredBuf := make([]int16, 64)
	fresh.SAA.GenerateStereo(restoredBuf)

	wantBuf := make([]int16, 64)
	m.SAA.GenerateStereo(wantBuf)

	if !bytes.Equal(int16sToBytes(restoredBuf), int16sToBytes(wantBuf)) {
		t.Error("the restored chip produced different samples: the phase was not captured")
	}
}

func int16sToBytes(xs []int16) []byte {
	out := make([]byte, 0, len(xs)*2)
	for _, x := range xs {
		out = append(out, byte(x), byte(x>>8))
	}
	return out
}

// The memory capture has to carry the paging registers AND the RAM, and the
// resolved page pointers have to be rebuilt from them rather than restored —
// they are pointers into this machine's own arrays.
func TestTheMemoryCaptureRebuildsThePageMapping(t *testing.T) {
	m := machineWithASIC(t)
	m.Mem.Write(0x8000, 0xA5)
	blob := m.Mem.SaveState()

	after := newTestMachine(t)
	if err := after.Mem.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := after.Mem.LMPR(); got != m.Mem.LMPR() {
		t.Errorf("LMPR = %#02x, want %#02x", got, m.Mem.LMPR())
	}
	if got := after.Mem.Read(0x8000); got != 0xA5 {
		t.Errorf("restored read at $8000 = %#02x, want 0xA5: either the RAM or the page "+
			"mapping did not come back", got)
	}
	// And the mapping must be live rather than a stale pointer into the old
	// machine: a write through the restored memory has to land in ITS ram.
	after.Mem.Write(0x8000, 0x5A)
	if got := m.Mem.Read(0x8000); got != 0xA5 {
		t.Error("writing to the restored machine changed the original: the page pointers " +
			"were restored rather than rebuilt")
	}
}

// A drive mid-transfer is the case that makes the controller worth capturing.
func TestADriveMidTransferIsCaptured(t *testing.T) {
	m := newTestMachine(t)
	fdc := m.FDC[0]
	fdc.InsertDisk(patternDisk(t))

	fdc.WriteTrack(0)
	fdc.WriteSector(1)
	fdc.WriteCommand(0x80) // read sector
	// Pull a few bytes so the transfer is genuinely part way through, and check
	// they are the start of the sector: a capture taken at position 0 would
	// round-trip whether or not the position was restored.
	for i := 0; i < 5; i++ {
		if got, want := fdc.ReadData(), byte(i*7); got != want {
			t.Fatalf("byte %d of the sector = %#02x, want %#02x", i, got, want)
		}
	}

	blob := fdc.SaveState()

	restored := NewWD1772()
	restored.InsertDisk(patternDisk(t))
	if err := restored.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	var wantRest, gotRest []byte
	for i := 0; i < 16; i++ {
		wantRest = append(wantRest, fdc.ReadData())
		gotRest = append(gotRest, restored.ReadData())
	}
	if !bytes.Equal(wantRest, gotRest) {
		t.Errorf("the rest of the sector differed:\n  live     %x\n  restored %x\n"+
			"(the transfer buffer or its position was not captured)", wantRest, gotRest)
	}
}

// A disk is media, not machine state, exactly as the tape and the +3's floppies
// are. A rewind cannot un-write a floppy and must not un-insert one either.
func TestTheDiskItselfIsNotCaptured(t *testing.T) {
	m := newTestMachine(t)
	m.FDC[0].InsertDisk(patternDisk(t))
	withDisk := m.FDC[0].SaveState()

	bare := NewWD1772()
	if err := bare.LoadState(withDisk); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if bare.disk != nil {
		t.Error("restoring a capture inserted a disk into a drive that had none")
	}
}

// A malformed blob must be rejected whole rather than applied in part, so a bad
// capture cannot leave the machine half rewound.
func TestLoadStateRejectsRubbish(t *testing.T) {
	m := machineWithASIC(t)
	want := m.SaveState()

	for _, blob := range [][]byte{nil, {}, []byte("not gob at all")} {
		if err := m.LoadState(blob); err == nil {
			t.Errorf("LoadState(%q) returned nil, want an error", blob)
		}
	}
	if got := m.SaveState(); !bytes.Equal(want, got) {
		t.Error("a rejected LoadState still changed the machine")
	}
}
