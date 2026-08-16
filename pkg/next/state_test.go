package next

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// The clip windows are the state a NextReg dump cannot describe. NR$18-$1B
// each hold four coordinates behind a 2-bit write index, and the dispatcher
// stores nothing for them at all — WireClipWindows owns both the coordinates
// and the index. The tilemap and the sprite engine capture the coordinates
// they are handed, so Layer 2's window, the ULA's window and ALL FOUR indices
// were reachable from nowhere until this device existed.
//
// These tests assert on the CAPTURE rather than on behaviour, because the
// index is only observable through NR$1C and a coordinate only through the
// index that happens to be selected: a behavioural test sees one of the four
// coordinates per window and calls the other three covered.

func clipFixture(t *testing.T) (*nextregs.Dispatcher, *ClipWindows) {
	t.Helper()
	d := nextregs.New()
	return d, WireClipWindows(d, nil, nil)
}

// driveClipWindowsA leaves each of the four windows with a different
// coordinate set AND a different write index, none of them the power-on
// value. The write counts are 1/2/3/5, so no window's index returns to where
// it started: four writes would wrap the 2-bit index back to zero and hide a
// lost restore completely.
func driveClipWindowsA(d *nextregs.Dispatcher) {
	d.WriteReg(0x18, 0x11) // Layer 2: idx 0 -> 1

	d.WriteReg(0x19, 0x21) // sprite: idx 0 -> 2
	d.WriteReg(0x19, 0x22)

	d.WriteReg(0x1A, 0x31) // ULA: idx 0 -> 3
	d.WriteReg(0x1A, 0x32)
	d.WriteReg(0x1A, 0x33)

	d.WriteReg(0x1B, 0x41) // tilemap: idx 0 -> 1, wrapping once
	d.WriteReg(0x1B, 0x42)
	d.WriteReg(0x1B, 0x43)
	d.WriteReg(0x1B, 0x44)
	d.WriteReg(0x1B, 0x45)
}

// driveClipWindowsB runs on from wherever A left off, and every window ends
// with both a different coordinate set and a different index than A left it.
// That is what gives the round-trip test its teeth: a field the fixture never
// makes differ cannot fail when its restore is deleted.
func driveClipWindowsB(d *nextregs.Dispatcher) {
	d.WriteReg(0x18, 0x91) // idx 1 -> 3
	d.WriteReg(0x18, 0x92)

	d.WriteReg(0x19, 0x93) // idx 2 -> 1
	d.WriteReg(0x19, 0x94)
	d.WriteReg(0x19, 0x95)

	d.WriteReg(0x1A, 0x96) // idx 3 -> 1
	d.WriteReg(0x1A, 0x97)

	d.WriteReg(0x1B, 0x98) // idx 1 -> 3
	d.WriteReg(0x1B, 0x99)
}

func decodeClipState(t *testing.T, b []byte) clipWindowsState {
	t.Helper()
	var s clipWindowsState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding clip-window state: %v", err)
	}
	return s
}

// The guard that makes the mutation score mean something: every captured
// field must actually differ between the two drive sequences. A score of 8/8
// over a fixture that moves six fields is a weaker claim than 6/6 over one
// that moves all eight, and the bare number cannot tell them apart.
func TestClipWindowsFixtureMovesEveryCapturedField(t *testing.T) {
	d, c := clipFixture(t)
	driveClipWindowsA(d)
	a := decodeClipState(t, c.SaveState())
	driveClipWindowsB(d)
	b := decodeClipState(t, c.SaveState())

	for _, f := range []struct {
		name string
		a, b any
	}{
		{"Layer2Coord", a.Layer2Coord, b.Layer2Coord},
		{"Layer2Idx", a.Layer2Idx, b.Layer2Idx},
		{"SpriteCoord", a.SpriteCoord, b.SpriteCoord},
		{"SpriteIdx", a.SpriteIdx, b.SpriteIdx},
		{"ULACoord", a.ULACoord, b.ULACoord},
		{"ULAIdx", a.ULAIdx, b.ULAIdx},
		{"TilemapCoord", a.TilemapCoord, b.TilemapCoord},
		{"TilemapIdx", a.TilemapIdx, b.TilemapIdx},
	} {
		if f.a == f.b {
			t.Errorf("%s is %v in both drive sequences: deleting its restore could never fail a test", f.name, f.a)
		}
	}
}

func TestClipWindowsCaptureRoundTrips(t *testing.T) {
	d, c := clipFixture(t)
	driveClipWindowsA(d)
	before := c.SaveState()

	driveClipWindowsB(d)
	if bytes.Equal(before, c.SaveState()) {
		t.Fatal("the drive sequence changed nothing — the fixture is inert, not the restore correct")
	}

	if err := c.LoadState(before); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if after := c.SaveState(); !bytes.Equal(before, after) {
		t.Error("the restored clip windows do not re-capture as the windows that were captured")
	}
}

// The failure that motivated the device: a rewind taken part-way through a
// four-write coordinate sequence must resume with the index it was captured
// with, or the guest's next write lands in the wrong coordinate and the
// window ends up clipping something it never asked to clip.
func TestClipWindowsRestoreResumesMidWriteSequence(t *testing.T) {
	d, c := clipFixture(t)
	d.WriteReg(0x18, 0x11) // x1 written; the next write belongs in x2
	st := c.SaveState()

	d.WriteReg(0x18, 0x22) // x2
	d.WriteReg(0x18, 0x33) // y1 — the sequence has run on

	if err := c.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	d.WriteReg(0x18, 0x99) // must land in x2, as it would have done

	// NR$1C read-back: the Layer 2 index is bits 1:0. One write past the
	// captured index is 2; an unrestored index would have wrapped to 0.
	if got := d.ReadReg(0x1C) & 0x03; got != 2 {
		t.Errorf("Layer 2 write index after the restored write = %d, want 2", got)
	}
	// The read mux returns coord[idx] — y1, still at its power-on $00. An
	// unrestored index would have put $99 in y2 and left this reading the
	// x1 the capture was taken at ($11).
	if got := d.ReadReg(0x18); got != 0x00 {
		t.Errorf("NR$18 read after the restored write = $%02X, want $00 (y1 untouched)", got)
	}
}

func TestClipWindowsStateIDIsStable(t *testing.T) {
	_, c := clipFixture(t)
	if got := c.StateID(); got != "next.clipwindows" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.clipwindows")
	}
}

func TestClipWindowsLoadStateRejectsRubbish(t *testing.T) {
	_, c := clipFixture(t)
	if err := c.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
	if err := c.LoadState(nil); err == nil {
		t.Error("an empty blob means the capture failed and must be reported")
	}
}

// NR$02's reset-type history is the piece of machine state software can see
// only two thirds of. The FPGA keeps three bits (zxnext.vhd:1306 init "100",
// :1736 shifting on every soft reset) and the read mux exposes bits 1:0
// (vhd:5891), so bit 2 cannot be reconstructed from the stored register byte
// that pkg/next/nextregs captures — and it is the bit that decides what the
// NEXT soft reset shifts into view.

func resetFixture(t *testing.T) (*nextregs.Dispatcher, *ResetControl) {
	t.Helper()
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	d := nextregs.New()
	return d, WireReset(d, mem, z80.New(mem, wireTestULA{}), nil)
}

// driveResetA / driveResetB each move BOTH captured fields, and neither
// returns one to where the other left it: the histories are "001" and "100"
// and the latched NMI sources $04 and $08. A sequence that toggled a source
// on and off again, or shifted the history an even number of times, would
// re-converge and hide a lost restore.
func driveResetA(d *nextregs.Dispatcher) {
	d.WriteReg(0x02, 0x05) // soft reset + divMMC NMI: "010" -> "001", source $04
}

func driveResetB(d *nextregs.Dispatcher) {
	d.WriteReg(0x02, 0x0A) // hard reset + multiface NMI: history "100", source $08
}

func decodeResetState(t *testing.T, b []byte) resetState {
	t.Helper()
	var s resetState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding reset state: %v", err)
	}
	return s
}

func TestResetControlFixtureMovesEveryCapturedField(t *testing.T) {
	d, rc := resetFixture(t)
	driveResetA(d)
	a := decodeResetState(t, rc.SaveState())
	driveResetB(d)
	b := decodeResetState(t, rc.SaveState())

	for _, f := range []struct {
		name string
		a, b any
	}{
		{"ResetType", a.ResetType, b.ResetType},
		{"NMISource", a.NMISource, b.NMISource},
	} {
		if f.a == f.b {
			t.Errorf("%s is %v in both drive sequences: deleting its restore could never fail a test", f.name, f.a)
		}
	}
}

func TestResetControlCaptureRoundTrips(t *testing.T) {
	d, rc := resetFixture(t)
	driveResetA(d)
	before := rc.SaveState()

	driveResetB(d)
	if bytes.Equal(before, rc.SaveState()) {
		t.Fatal("the drive sequence changed nothing — the fixture is inert, not the restore correct")
	}

	if err := rc.LoadState(before); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if after := rc.SaveState(); !bytes.Equal(before, after) {
		t.Error("the restored reset control does not re-capture as the control that was captured")
	}
}

// The failure that motivated the device. Capture with the history at "100",
// run on until it has shifted down to "001", then rewind: the next soft reset
// must shift from "100" again and show the guest $02.
//
// A capture holding only what software can read would have stored the low two
// bits of "100", which are "00", and the same soft reset would then have
// shifted "000" -> "000" and shown $00. So this fails for a device that
// captures the visible register instead of the hidden shift register.
func TestResetControlRestoreShiftsFromTheHistoryItWasCapturedWith(t *testing.T) {
	d, rc := resetFixture(t)
	d.WriteReg(0x02, 0x02) // hard reset: history back to "100"
	st := rc.SaveState()

	d.WriteReg(0x02, 0x01) // soft: "010"
	d.WriteReg(0x02, 0x01) // soft: "001"
	if got := d.ReadReg(0x02) & 0x03; got != 0x01 {
		t.Fatalf("NR$02 low bits after two soft resets = %#02x, want 01", got)
	}

	if err := rc.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	d.WriteReg(0x02, 0x01) // the first soft reset after the rewind
	if got := d.ReadReg(0x02) & 0x03; got != 0x02 {
		t.Errorf("NR$02 low bits = %#02x after the rewound soft reset, want 02: "+
			"the history shifted from the wrong value", got)
	}
}

func TestResetControlStateIDIsStable(t *testing.T) {
	_, rc := resetFixture(t)
	if got := rc.StateID(); got != "next.reset" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.reset")
	}
}

func TestResetControlLoadStateRejectsRubbish(t *testing.T) {
	_, rc := resetFixture(t)
	if err := rc.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
	if err := rc.LoadState(nil); err == nil {
		t.Error("an empty blob means the capture failed and must be reported")
	}
}
