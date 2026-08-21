package sam

import (
	"bytes"
	"reflect"
	"testing"
)

// Per-device coverage for the SAM's capture.
//
// state_test.go covers the ASIC latches. This covers the other four devices,
// and it exists because a mutation audit said it had to: of the 35 field
// restores in state.go, 20 survived having their line deleted. Every one of
// them was a restore no fixture moved, so the round-trip tests passed for it
// whether or not it was there.
//
// The shape below is the one that closes that for good rather than field by
// field. Each device gets a set of fixtures, a reflection guard proving some
// fixture moves every captured field, and a capture-perturb-restore-recapture
// test. Together those mean a deleted restore always changes a blob that is
// compared, so it always fails.

// crossRoundTrip is the property every device has to satisfy, checked across
// every PAIR of fixtures rather than against one perturbation.
//
// That pairing is the point. A single capture-perturb-restore test only covers
// the fields the perturbation happens to move, so a restore of a field the
// perturbation leaves alone passes whether or not it is there. Running every
// fixture against every other means each field is covered by whichever pair
// disagrees about it — which is exactly the set the coverage guard below
// proves is non-empty.
func crossRoundTrip[T any](t *testing.T, fixtures []namedFixture[T],
	capture func(T) []byte, load func(T, []byte) error) {
	t.Helper()
	for _, from := range fixtures {
		for _, to := range fixtures {
			if from.name == to.name {
				continue
			}
			t.Run(from.name+" restored to "+to.name, func(t *testing.T) {
				live := from.build(t)
				want := capture(to.build(t))
				if bytes.Equal(capture(live), want) {
					t.Skip("these two fixtures capture identically, so the pair proves nothing")
				}
				if err := load(live, want); err != nil {
					t.Fatalf("LoadState: %v", err)
				}
				if got := capture(live); !bytes.Equal(want, got) {
					t.Error("the restored device does not re-capture as the device that was captured")
				}
			})
		}
	}
}

// namedFixture is one device state, built fresh each time it is used so the
// pairs cannot contaminate each other.
type namedFixture[T any] struct {
	name  string
	build func(t *testing.T) T
}

// fieldsMovedBy checks that some fixture moves every field of the wire struct
// off the value the first fixture produces.
func fieldsMovedBy[W any, T any](t *testing.T, fixtures []namedFixture[T], capture func(T) []byte) {
	t.Helper()
	base := reflect.ValueOf(decodeInto[W](t, capture(fixtures[0].build(t))))
	moved := map[string]string{}
	for _, f := range fixtures[1:] {
		got := reflect.ValueOf(decodeInto[W](t, capture(f.build(t))))
		for i := 0; i < base.NumField(); i++ {
			name := base.Type().Field(i).Name
			if _, done := moved[name]; done {
				continue
			}
			if !reflect.DeepEqual(base.Field(i).Interface(), got.Field(i).Interface()) {
				moved[name] = f.name
			}
		}
	}
	for i := 0; i < base.NumField(); i++ {
		name := base.Type().Field(i).Name
		if _, ok := moved[name]; !ok {
			t.Errorf("no fixture moves %s off the value %q produces, so every round-trip "+
				"test passes for it whether or not it is restored", name, fixtures[0].name)
		}
	}
}

func decodeInto[T any](t *testing.T, blob []byte) T {
	t.Helper()
	var s T
	if err := decodeGob(blob, &s); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// Keyboard.
// ---------------------------------------------------------------------------

func keyboardFixtures() []namedFixture[*Keyboard] {
	return []namedFixture[*Keyboard]{
		{"idle", func(t *testing.T) *Keyboard { return NewKeyboard() }},
		{"held keys", func(t *testing.T) *Keyboard {
			k := NewKeyboard()
			k.SetKey(3, 2, true)
			k.SetKey(7, 0, true)
			return k
		}},
		{"a typed symbol", func(t *testing.T) *Keyboard {
			k := NewKeyboard()
			k.TypeRune('"') // starts the overlay and its countdown
			return k
		}},
		{"a symbol half spent", func(t *testing.T) *Keyboard {
			k := NewKeyboard()
			k.TypeRune('(')
			k.Tick()
			k.Tick()
			return k
		}},
	}
}

func TestTheKeyboardCaptureCoversEveryField(t *testing.T) {
	fieldsMovedBy[keyboardState](t, keyboardFixtures(), (*Keyboard).SaveState)
}

func TestTheKeyboardRoundTrips(t *testing.T) {
	crossRoundTrip(t, keyboardFixtures(), (*Keyboard).SaveState, (*Keyboard).LoadState)
}

// A restored typed-symbol pulse must still be visible to the guest's next scan.
// The pulse is a countdown rather than a level, so restoring the matrix without
// it either drops the keypress or repeats it.
func TestARestoredSymbolPulseIsStillDelivered(t *testing.T) {
	k := NewKeyboard()
	k.TypeRune('"')
	blob := k.SaveState()

	// Run the pulse out, so the overlay is gone.
	for i := 0; i < samRunePulseFrames+2; i++ {
		k.Tick()
	}
	spent := scanAllRows(k)

	if err := k.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if restored := scanAllRows(k); bytes.Equal(restored, spent) {
		t.Error("the restored keyboard scans as though the symbol pulse had already expired")
	}
}

func scanAllRows(k *Keyboard) []byte {
	out := make([]byte, 9)
	for row := 0; row < 9; row++ {
		out[row] = k.Scan(^byte(1 << uint(row)))
	}
	return out
}

// ---------------------------------------------------------------------------
// Memory.
// ---------------------------------------------------------------------------

// memWithExternal builds a memory with a megabyte of external RAM fitted, so
// the external pages are something a fixture can actually move. The default
// machine has none (NewMemory asks for 0 MB), which is why ExtRAM survived the
// first version of this guard: there was nowhere for a write to land.
func memWithExternal(t *testing.T) *Memory {
	t.Helper()
	rom0, rom1, err := SplitROM(synthROM())
	if err != nil {
		t.Fatalf("SplitROM: %v", err)
	}
	return NewMemoryWithRAM(rom0, rom1, 512, 1)
}

func memoryFixtures() []namedFixture[*Memory] {
	return []namedFixture[*Memory]{
		{"power on", memWithExternal},
		{"paged", func(t *testing.T) *Memory {
			m := memWithExternal(t)
			m.SetLMPR(0x1F)
			m.SetHMPR(0x22)
			m.SetVMPR(0x63)
			m.SetLEPR(0x11)
			m.SetHEPR(0x02)
			return m
		}},
		{"written RAM", func(t *testing.T) *Memory {
			m := memWithExternal(t)
			m.Write(0x8000, 0xA5)
			m.Write(0x4000, 0x11)
			return m
		}},
		{"written external RAM", func(t *testing.T) *Memory {
			m := memWithExternal(t)
			// HMPR bit 7 maps section C from external RAM, selected by LEPR.
			m.SetLEPR(0x03)
			m.SetHMPR(0x80)
			m.Write(0x8000, 0x5A)
			return m
		}},
		{"screen off, no contention", func(t *testing.T) *Memory {
			m := memWithExternal(t)
			m.SetScreenOff(true)
			m.SetContentionEnabled(false)
			return m
		}},
	}
}

func TestTheMemoryCaptureCoversEveryField(t *testing.T) {
	fieldsMovedBy[memoryState](t, memoryFixtures(), (*Memory).SaveState)
}

func TestTheMemoryRoundTrips(t *testing.T) {
	crossRoundTrip(t, memoryFixtures(), (*Memory).SaveState, (*Memory).LoadState)
}

// ---------------------------------------------------------------------------
// WD1772.
// ---------------------------------------------------------------------------

func driveFixtures() []namedFixture[*WD1772] {
	withDisk := func(t *testing.T) *WD1772 {
		t.Helper()
		w := NewWD1772()
		w.InsertDisk(patternDisk(t))
		return w
	}
	return []namedFixture[*WD1772]{
		{"idle", withDisk},
		{"a completed seek", func(t *testing.T) *WD1772 {
			w := withDisk(t)
			w.WriteData(9)
			w.WriteCommand(0x18) // seek to the track in the data register
			w.WriteCommand(0x58) // step in, updating the track register
			w.ReadStatus()       // advances the Type I index-pulse counter
			return w
		}},
		{"a step outward", func(t *testing.T) *WD1772 {
			// The controller powers up remembering an inward step
			// (NewWD1772 sets lastDir = +1), so only an outward one moves the
			// direction memory a bare Step command later repeats.
			w := withDisk(t)
			w.WriteData(9)
			w.WriteCommand(0x18)
			w.WriteCommand(0x68) // step out
			return w
		}},
		{"mid read, multi-sector", func(t *testing.T) *WD1772 {
			w := withDisk(t)
			w.SetSide(1)
			w.WriteTrack(0)
			w.WriteSector(3)
			w.WriteCommand(0x90) // read multiple
			for i := 0; i < 6; i++ {
				w.ReadData()
			}
			return w
		}},
		{"mid write", func(t *testing.T) *WD1772 {
			w := withDisk(t)
			w.WriteTrack(0)
			w.WriteSector(2)
			w.WriteCommand(0xA0) // write sector
			w.WriteData(0x77)
			return w
		}},
		{"mid format", func(t *testing.T) *WD1772 {
			// WRITE TRACK collects a whole track image, so a capture
			// taken here has to say it is formatting: restored as an
			// ordinary sector write it would commit the track image
			// into one sector.
			w := withDisk(t)
			w.WriteCommand(0xF0)
			for _, b := range []byte{0xFE, 0x00, 0x00, 0x01, 0x02, 0xF7, 0xFB} {
				w.WriteData(b)
			}
			return w
		}},
		{"a starved DRQ", func(t *testing.T) *WD1772 {
			w := withDisk(t)
			w.WriteTrack(0)
			w.WriteSector(1)
			w.WriteCommand(0x80)
			for i := 0; i < wdLostDataReads+2; i++ {
				w.ReadStatus() // DRQ left unserviced long enough to time out
			}
			return w
		}},
	}
}

func TestTheDriveCaptureCoversEveryField(t *testing.T) {
	fieldsMovedBy[fdcState](t, driveFixtures(), (*WD1772).SaveState)
}

func TestTheDriveRoundTrips(t *testing.T) {
	crossRoundTrip(t, driveFixtures(), (*WD1772).SaveState, (*WD1772).LoadState)
}
