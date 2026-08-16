package tilemap

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"hash/fnv"
	"reflect"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

// The tilemap is a register mirror in front of a renderer: nothing in it
// counts, latches or auto-increments, so its whole state is the eleven values
// the NextReg dispatcher last wrote into it. That makes an omission cheap to
// make and invisible to spot — leave the scroll or one clip edge out of the
// capture and the layer still draws a plausible picture, just not the one the
// machine was rewound to.
//
// So these tests are first a replay property — capture, render frames, restore,
// render the same frames, compare what the compositor saw — and then the same
// drive asserted against the capture itself, because a replay can only observe
// the fields it happens to make change the picture. Neither is a hand-written
// field-by-field comparison, which would only check the fields someone
// remembered to add. The drive below deliberately leaves the layer somewhere
// else entirely (see poisonTilemap) before the replay starts, so a field whose
// restore is missing resumes from the wrong value instead of from a value that
// happened to still be correct.

var _ machinestate.Device = (*Tilemap)(nil)

// stateBanks fills both selectable banks with dense, distinct content.
//
// Density is what gives the replay its teeth. Against the sparse fixtures the
// rest of this package uses — a handful of tiles, the rest zero — a wrong map
// base, a wrong scroll or a wrong tile-defs offset all land on zeroes and
// render the same transparent picture, and a test that cannot see a field
// cannot fail when that field's restore is deleted.
func stateBanks() *twoBank {
	b := &twoBank{bank5: make([]byte, 0x4000), bank7: make([]byte, 0x4000)}
	for i := range b.bank5 {
		b.bank5[i] = byte(i*7) ^ byte(i>>3) ^ 0x11
		b.bank7[i] = byte(i*13) ^ byte(i>>5) ^ 0x8C
	}
	return b
}

// primedTilemap brings the layer up the way NextZXOS does — $6B, $6C, $6E,
// $6F, $2F:$30, $31, $1B — and stops PART WAY THROUGH that program.
//
// Mid-sequence is the point. The layer has no transfer cursor of its own to be
// caught halfway through, but the register program that configures it is
// several writes long and a rewind can land between any two of them: here the
// map base has already moved to bank 7 while the tile definitions still point
// at the old bank-5 setup, which is a state that renders (recognisably wrong)
// and that no capture taken at a quiet boundary would prove is restored.
func primedTilemap() *Tilemap {
	tm := New(stateBanks())
	tm.SetEnabled(true)     // $6B bit 7
	tm.SetControl(0x23)     // strip_flags + mode_512 + on_top, 40-col, 4bpp
	tm.SetDefaultAttr(0xB7) // palette offset $B, mirrored, rotated, tile bit 8 set
	tm.SetTileMapBase(0x8D) // $6E: bank 7, offset $0D00 — the NEW map
	tm.SetTilesBase(0x1C)   // $6F: bank 5, offset $1C00 — still the OLD defs
	tm.SetScrollX(0x123)
	tm.SetScrollY(0x5B)
	tm.SetClip(0x08, 0x8A, 0x11, 0xC7)
	return tm
}

// poisonTilemap leaves the layer at values that differ from primedTilemap's in
// every single field, and is what every drive ends on.
//
// Without it the replay would be free to pass on fields the drive happened to
// leave where the capture found them: restoring nothing at all would look
// identical to restoring everything. Ending somewhere else forces the restore
// to do the work.
func poisonTilemap(tm *Tilemap) {
	tm.SetEnabled(false)
	tm.SetControl(0x00)
	tm.SetDefaultAttr(0x00)
	tm.SetTileMapBase(0x00)
	tm.SetTilesBase(0x00)
	tm.SetScrollX(0)
	tm.SetScrollY(0)
	tm.SetClip(0x00, 0x9F, 0x00, 0xFF) // the FPGA reset window: wide open
}

// driveTilemapFrames renders whole frames the way the compositor does and
// returns everything it could have seen: the three flags it asks the layer
// before compositing, and a digest of every scanline of every frame.
//
// The per-frame register program comes LAST, after the frame it does not
// govern has been rendered. That ordering is what gives the test its teeth: the
// values a capture is holding are the ones that paint the first replayed frame,
// before the program overwrites them. Reconfigure first and every restored
// register would be discarded before it could ever be seen.
func driveTilemapFrames(tm *Tilemap, frames int) []byte {
	log := new(bytes.Buffer)
	// 640 pixels: the width the compositor hands over in 80-column mode, and
	// harmless in 40-column mode (the layer wraps as a torus), so one buffer
	// covers both and a wrongly restored mode bit stays visible.
	dst := make([]byte, 2*Width40)
	for f := 0; f < frames; f++ {
		log.WriteByte(tmBoolByte(tm.Enabled()))
		log.WriteByte(tmBoolByte(tm.OnTop()))
		log.WriteByte(tmBoolByte(tm.Is80Col()))
		h := fnv.New64a()
		for y := 0; y < Height; y++ {
			tm.RenderScanline(y, dst)
			_, _ = h.Write(dst)
		}
		_ = binary.Write(log, binary.BigEndian, h.Sum64())

		// The next frame's registers, as a program writing NR$6B/$6C/$6E/$6F/
		// $2F:$30/$31/$1B every frame would leave them. The scrolls accumulate
		// rather than being assigned, so a wrong starting value stays wrong for
		// the whole run instead of being corrected by the next write.
		tm.SetEnabled(f%5 != 3)
		tm.SetControl(byte(0x21 + f*0x0A)) // walks strip_flags, textmode, 80-col
		tm.SetDefaultAttr(byte(f*29 + 3))
		tm.SetTileMapBase(byte(f * 37))
		tm.SetTilesBase(byte(f*53 + 0x80))
		tm.SetScrollX((tm.scrollX + 13*(f+1)) & 0x3FF)
		tm.SetScrollY((tm.scrollY + 7*f + 1) & 0xFF)
		tm.SetClip(byte(f), byte(0x9F-f), byte(f*3), byte(0xFF-f*2))
	}
	poisonTilemap(tm)
	return log.Bytes()
}

func tmBoolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// The property that matters: from a captured state, the layer must draw the
// frames it drew the first time.
func TestReplayingFromCapturedStateReproducesTheSameFrames(t *testing.T) {
	tm := primedTilemap()

	st := tm.SaveState()
	first := driveTilemapFrames(tm, 5)

	if err := tm.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveTilemapFrames(tm, 5)

	if !bytes.Equal(first, second) {
		t.Error("the tilemap drew a different picture on replay from the same captured " +
			"state: some part of its configuration is not being captured")
	}
}

// A rewind can land in the middle of a frame, between two scanlines the
// compositor has already taken and the ones it has not. Resuming has to finish
// that frame the same way.
func TestReplayingFromMidFrameFinishesTheFrameTheSameWay(t *testing.T) {
	tm := primedTilemap()
	dst := make([]byte, 2*Width40)

	// The top half of the frame, already composited and gone.
	for y := 0; y < 128; y++ {
		tm.RenderScanline(y, dst)
	}

	st := tm.SaveState()
	first := finishFrameAndRunOn(tm, 3)

	if err := tm.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := finishFrameAndRunOn(tm, 3)

	if !bytes.Equal(first, second) {
		t.Error("the bottom half of the interrupted frame came out differently on replay")
	}
}

// finishFrameAndRunOn renders the rest of an interrupted frame and then whole
// frames after it, so the replay covers both the resumed frame and the ones the
// register program reconfigures.
func finishFrameAndRunOn(tm *Tilemap, frames int) []byte {
	log := new(bytes.Buffer)
	dst := make([]byte, 2*Width40)
	h := fnv.New64a()
	for y := 128; y < Height; y++ {
		tm.RenderScanline(y, dst)
		_, _ = h.Write(dst)
	}
	_ = binary.Write(log, binary.BigEndian, h.Sum64())
	log.Write(driveTilemapFrames(tm, frames))
	return log.Bytes()
}

// The replay tests above assert on what the layer drew, which is the property a
// rewind has to have. It is not on its own a complete audit: a field is only
// observable there if the fixture happens to make it change the picture, and a
// field that reaches no render — NR$6B bits 4 and 2 — cannot be observed there
// at all. The AY's capture scored 8 of 19 on exactly that blind spot.
//
// So the capture is also asserted on directly: drive every field somewhere
// else, restore, re-capture, and compare the two blobs. That observes all
// eleven fields whether or not a render could.
func TestEveryCapturedFieldSurvivesARoundTrip(t *testing.T) {
	tm := primedTilemap()
	want := tm.SaveState()

	driveTilemapFrames(tm, 5)
	if bytes.Equal(tm.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := tm.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := tm.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard that makes the score above mean something. A mutation score is
// bounded above by what the fixture moves: deleting a field's restore proves
// nothing if the fixture left that field the same either side of the restore,
// because the deleted line had nothing to do.
//
// Every drive here ends in poisonTilemap, so this is really the assertion that
// primedTilemap and poisonTilemap disagree about every single field. Retune
// either of them onto a shared value — the clip edges are the easy mistake,
// since poison uses the FPGA reset window and a primed edge could drift onto
// $9F or $FF — and the tests above silently stop covering that field.
//
// The comparison is by reflection rather than a hand-written list so that a
// field added to the capture is covered here the moment it is added, instead of
// waiting for someone to remember to extend the list.
func TestTheDriveSequenceChangesEveryCapturedField(t *testing.T) {
	tm := primedTilemap()
	before := decodeTilemapStateForTest(t, tm.SaveState())
	driveTilemapFrames(tm, 5)
	after := decodeTilemapStateForTest(t, tm.SaveState())

	bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
	typ := bv.Type()
	if typ.NumField() == 0 {
		t.Fatal("the captured state has no fields, so this guard checks nothing")
	}
	for i := 0; i < typ.NumField(); i++ {
		if reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			t.Errorf("%s is unchanged by the drive sequence (%v either side), so a lost "+
				"restore of it would go undetected", typ.Field(i).Name, bv.Field(i).Interface())
		}
	}
}

// Both guards above are bounded by the fields the capture already has: eleven
// restores and eleven kills say nothing about a twelfth field added to Tilemap
// and never captured at all, which is the omission state.go's comment claims
// these tests catch. This is what makes that claim true — the layer's own
// fields, less the memory bus it does not own, must be exactly the captured set.
func TestEveryFieldOfTheLayerIsCaptured(t *testing.T) {
	captured := map[string]bool{
		// mem is the RAM bus (pkg/memory); it captures itself under its own name
		// in the machinestate registry, and holds no tilemap state of its own.
		"mem": true,
	}
	st := reflect.TypeOf(tilemapState{})
	for i := 0; i < st.NumField(); i++ {
		captured[strings.ToLower(st.Field(i).Name)] = true
	}

	tt := reflect.TypeOf(Tilemap{})
	for i := 0; i < tt.NumField(); i++ {
		if name := tt.Field(i).Name; !captured[strings.ToLower(name)] {
			t.Errorf("Tilemap.%s has no field of that name in tilemapState: a rewind would "+
				"leave it holding present-day values", name)
		}
	}
}

func decodeTilemapStateForTest(t *testing.T, b []byte) tilemapState {
	t.Helper()
	var s tilemapState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}

// The fixture guard. Every test above compares two renders, and two renders of
// a blank layer are equal whatever the state — so if the fixture ever stopped
// producing a picture, the whole file would pass while proving nothing.
//
// This is not hypothetical: index 0 is the transparent slot, so a tile
// definition full of zero nibbles renders as an empty scanline no matter which
// bank, base, scroll or palette offset selected it.
func TestStateFixtureActuallyRendersSomethingToObserve(t *testing.T) {
	tm := primedTilemap()
	dst := make([]byte, 2*Width40)
	seen := map[byte]bool{}
	for y := 0; y < Height; y++ {
		tm.RenderScanline(y, dst)
		for _, v := range dst {
			seen[v] = true
		}
	}
	if len(seen) < 16 {
		t.Fatalf("the primed fixture renders only %d distinct pixel values: the replay "+
			"tests cannot see a register they cannot make change the picture", len(seen))
	}
	// And the clip window has to be doing something, or all four clip fields
	// are invisible to every test here.
	tm2 := primedTilemap()
	tm2.RenderScanline(0, dst) // above clipY1 = $11
	for _, v := range dst {
		if v != 0 {
			t.Fatal("scanline 0 is not clipped away: the primed clip window is not biting")
		}
	}
}

// A capture must not alias the live device. The registry copies the blob too,
// but a device handing back a view of its own state is a bug worth catching
// where it would be made.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	tm := primedTilemap()

	st := tm.SaveState()
	before := append([]byte(nil), st...)

	driveTilemapFrames(tm, 3)

	if !bytes.Equal(st, before) {
		t.Error("the captured state changed while the layer kept running: " +
			"SaveState handed back a view of live device state")
	}
}

func TestStateIDIsStable(t *testing.T) {
	tm := New(stateBanks())
	if got := tm.StateID(); got != "next.tilemap" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift",
			got, "next.tilemap")
	}
}

// A malformed blob must be reported and must leave the layer exactly as it was.
// A half-applied state is the worst outcome available: the machine runs, and
// the caller believes the rewind worked.
func TestLoadStateRejectsRubbishWithoutHalfApplying(t *testing.T) {
	tm := primedTilemap()

	st := tm.SaveState()
	want := driveTilemapFrames(tm, 3)

	if err := tm.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, bad := range [][]byte{
		{0xDE, 0xAD, 0xBE, 0xEF},
		st[:len(st)/2], // a truncated capture: a real failure mode
		{},
		nil,
	} {
		if err := tm.LoadState(bad); err == nil {
			t.Errorf("a malformed state blob (%d bytes) must be reported, not applied", len(bad))
		}
	}

	if got := driveTilemapFrames(tm, 3); !bytes.Equal(want, got) {
		t.Error("a rejected LoadState changed the layer: the state was applied in part")
	}
}

// NR$6B bits 4 and 2 are stored by the layer but never read by it — bit 4
// selects which tilemap palette the palette bank serves (pkg/next/wire.go pushes
// it there) and bit 2 has no meaning in this core. No render can observe either,
// so this is the only place their capture can be checked at all.
func TestControlBitsNoRenderCanSeeStillRoundTrip(t *testing.T) {
	tm := primedTilemap()
	tm.SetControl(0x14) // bits 4 and 2 set, everything else clear
	st := tm.SaveState()
	tm.SetControl(0x00)
	if err := tm.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := tm.control; got != 0x14 {
		t.Errorf("control = %#02x after round trip, want 0x14: the bits no render "+
			"can see are being dropped", got)
	}
}

// The bank selector survives a restore with the FPGA's own bit assignment: CPU
// bit 7 picks bank 7, CPU bit 6 is inert (zxnext.vhd:5468-5473 stores bit 7 and
// bits 5:0, discarding bit 6).
//
// A capture that normalised the base bytes — masking bit 6 off, say, the way
// the NextReg read-back does — would round-trip to the same picture and no
// replay could tell. This pins the raw byte, and then proves the restored value
// still decodes to the bank it decoded to before.
func TestBaseRegistersRestoreWithBit7AsTheBankSelector(t *testing.T) {
	banks := stateBanks()
	tm := New(banks)
	tm.SetEnabled(true)
	tm.SetStripFlags(true)
	tm.SetDefaultAttr(0x10) // palette offset 1, no mirror/rotate, tile bit 8 clear
	tm.SetTileMapBase(0x4D) // bit 6 set, bit 7 clear: bank 5, offset $0D00
	tm.SetTilesBase(0xCD)   // bits 7 and 6 set: bank 7, offset $0D00

	dst := make([]byte, Width40)
	want := make([]byte, Width40)
	tm.RenderScanline(64, want)

	st := tm.SaveState()
	poisonTilemap(tm)
	if err := tm.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if tm.mapBase != 0x4D || tm.tilesBase != 0xCD {
		t.Errorf("base registers = $%02X/$%02X after round trip, want $4D/$CD: the "+
			"capture is rewriting the bytes the guest wrote", tm.mapBase, tm.tilesBase)
	}
	tm.RenderScanline(64, dst)
	if !bytes.Equal(want, dst) {
		t.Error("the restored bases render a different scanline: the map is being read " +
			"from the wrong bank or the wrong offset")
	}
	// And the picture has to depend on the bank bit at all, or the assertion
	// above would hold for any base whose low bits matched.
	tm.SetTilesBase(0x4D) // same offset, bank 5 instead of bank 7
	other := make([]byte, Width40)
	tm.RenderScanline(64, other)
	if bytes.Equal(want, other) {
		t.Fatal("bank 5 and bank 7 render identically: the fixture cannot see the " +
			"bank selector, so this test proves nothing about it")
	}
}
