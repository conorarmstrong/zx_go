package if1

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

// The Interface 1 is the device that proves why a rewind needs more than the
// bytes on the cartridge.
//
// A microdrive is a tape loop with one head. What the next MDR read returns is
// decided by where that head sits, how far through the current transfer window
// the controller is, which byte was last latched, and how far down the GAP and
// SYNC countdowns the status port has walked — none of which the guest can
// read back, and none of which any snapshot format stores. Restore the
// cartridge alone and the drive runs, but it runs from a different point on the
// tape: the block the IF1 ROM was half-way through never arrives, and the load
// that was in progress does not finish.
//
// These tests are written as a replay property rather than a field-by-field
// comparison, because a field-by-field test only checks the fields someone
// remembered to add.

// patternedCartridge builds a cartridge whose every byte differs from its
// neighbours. A blank cartridge is 0xFF everywhere, and against 0xFF everywhere
// a lost head position reads exactly the same as a kept one — the test would
// pass while measuring nothing.
func patternedCartridge(blocks int, seed byte) *microdrive.Cartridge {
	c := microdrive.New(blocks)
	for off := 0; off < blocks*microdrive.BlockLen; off++ {
		c.SetDataAt(off, byte(off*7)+seed)
	}
	return c
}

// primedIF1 returns an interface part-way through a microdrive operation: the
// shadow ROM paged in, two bays spinning, the heads at different points on
// their tapes, one drive past the end of its transfer window with a latched
// byte and the other still streaming, the GAP countdown part-way down, and a
// half-written preamble in the block under the head.
func primedIF1(t *testing.T) *IF1 {
	t.Helper()

	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}
	i.PageIn() // the shadow ROM is live for the whole of a microdrive command

	i.ULA.Bus.Insert(0, patternedCartridge(12, 0x11))
	wp := patternedCartridge(12, 0x80)
	wp.SetWriteProtect(true)
	i.ULA.Bus.Insert(2, wp)

	// Select drive 0 only: falling COMMS CLK edge with DATA low.
	i.HandlePortWrite(PortCTR, 0x03)
	i.HandlePortWrite(PortCTR, 0x00)

	// Poll the status port so the GAP countdown sits part-way down rather than
	// at the value a reset would give it.
	for k := 0; k < 7; k++ {
		i.HandlePortRead(PortCTR)
	}

	// Move drive 0's head off the block boundary, then re-select so that
	// drive 0 lands in a record window (528 bytes) while drive 2 is still at a
	// header window (15). The two bays then disagree about where their
	// transfer window ends, which is what makes both the window size and the
	// latched byte observable in the same read.
	for k := 0; k < 10; k++ {
		i.HandlePortRead(PortMDR)
	}
	i.HandlePortWrite(PortCTR, 0x03) // CLK high, DATA high
	i.HandlePortWrite(PortCTR, 0x01) // falling edge, DATA high: motor bit off
	i.HandlePortWrite(PortCTR, 0x02) // CLK high, DATA low
	i.HandlePortWrite(PortCTR, 0x00) // falling edge, DATA low: drives 0 and 2 spin

	// Start a preamble and stop half-way through it, so the block under each
	// head is mid-format rather than either blank or finished.
	for k := 0; k < 5; k++ {
		i.HandlePortWrite(PortMDR, 0x00)
	}

	// Stream 515 bytes. Drive 2's 15-byte window ends almost at once, so it
	// spends the rest of the run handing back its latched byte. Drive 0 is in
	// a 528-byte record window, so this leaves it at 520 transferred: eight
	// bytes short of the end, which is what makes the transfer counter itself
	// observable. A count that merely sat somewhere in the middle of the
	// window would read the same as any other middle value, and the counter
	// would round-trip untested.
	for k := 0; k < 515; k++ {
		i.HandlePortRead(PortMDR)
	}
	return i
}

// stateFlag renders a bool into the recorded output.
func stateFlag(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// ctrData picks the COMMS DATA level for one pass of the drive-select walk.
// The line is active low: a low bit shifts a motor-on into drive 0. Mostly low
// so the tape keeps moving, high every fifth pass so motor-off bits walk down
// the daisy chain too.
func ctrData(cycle int) byte {
	if cycle%5 == 4 {
		return 0x01
	}
	return 0x00
}

// bays records the state an observer can see without driving the interface at
// all: which bays are loaded, which tapes have been written to, which are
// spinning, and what is on them. That is the interface's exported surface, and
// Drive.Modified is the flag any write-the-cartridge-back path has to act on,
// so a rewind that gets these wrong discards a user's writes without a single
// port access going astray.
func bays(i *IF1) []byte {
	out := make([]byte, 0, 4096)
	for n := 0; n < NumDrives; n++ {
		d := i.ULA.Bus.Drive(n)
		out = append(out, stateFlag(d.Inserted), stateFlag(d.Modified), stateFlag(d.MotorOn))
		if d.Cartridge != nil {
			out = append(out, stateFlag(d.Cartridge.WriteProtect()))
			out = append(out, d.Cartridge.RawBytes()...)
		}
	}
	return out
}

// driveForward runs a fixed sequence of the operations the IF1 ROM performs
// during a microdrive load, and records everything an observer could see: the
// bays it started from, every byte the interface handed back, and the bays it
// was left holding.
//
// The order within a pass matters. The run of MDR reads comes first because
// that is what the ROM does after a rewind lands mid-block — it finishes the
// transfer that was in flight. Any CTR access calls microdrives_restart, which
// resets the transfer counter and re-aligns the head, so a sequence that
// polled the status port first would wipe the window state before it could be
// observed and the replay would stop measuring it.
func driveForward(i *IF1) []byte {
	out := bays(i)
	for cycle := 0; cycle < 60; cycle++ {
		// Stream a run of bytes off the tape head.
		for k := 0; k < 20; k++ {
			v, _ := i.HandlePortRead(PortMDR)
			out = append(out, v)
		}
		// Then poll the status port waiting for the GAP/SYNC pattern.
		for k := 0; k < 3; k++ {
			v, _ := i.HandlePortRead(PortCTR)
			out = append(out, v)
		}
		// Walk the drive-select shift register. The CLK-low write comes first
		// on purpose: whether it is a falling edge at all depends on the
		// latched clock level, which is part of the captured state.
		i.HandlePortWrite(PortCTR, ctrData(cycle))
		i.HandlePortWrite(PortCTR, ctrData(cycle)|0x02)
		// Every fourth pass writes a block: the 12-byte preamble the ROM lays
		// down (10 x 0x00 then 2 x 0xFF), then data.
		if cycle%4 == 3 {
			for k := 0; k < 10; k++ {
				i.HandlePortWrite(PortMDR, 0x00)
			}
			for k := 0; k < 2; k++ {
				i.HandlePortWrite(PortMDR, 0xFF)
			}
			for k := 0; k < 25; k++ {
				i.HandlePortWrite(PortMDR, byte(k*13+cycle))
			}
		}
		// The shadow ROM leaves the address space part-way through, as it does
		// when an IF1 ROM routine returns to BASIC.
		if cycle == 20 {
			i.PostFetchHook(PageOutAddr)
		}
		if b, ok := i.MemoryRead(uint16(cycle) * 37); ok {
			out = append(out, b)
		} else {
			out = append(out, 0xAA)
		}
	}
	return append(out, bays(i)...)
}

// The property that matters: from a captured state, the interface must hand
// back the same bytes it handed back the first time.
func TestReplayingFromCapturedStateReproducesTheSameStream(t *testing.T) {
	i := primedIF1(t)

	st := i.SaveState()
	first := driveForward(i)

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveForward(i)

	if !bytes.Equal(first, second) {
		t.Error("the Interface 1 produced a different stream on replay from the same captured state: " +
			"some part of the microdrive controller is not being captured")
	}
}

// The rewind that actually happens: the machine ran on, and the bays were
// meddled with, before the state was put back. A restore has to return the
// whole interface to the captured configuration, not just the parts that
// happen to have moved on their own.
func TestRestoringOverADisturbedInterfaceReproducesTheSameStream(t *testing.T) {
	i := primedIF1(t)

	st := i.SaveState()
	want := driveForward(i)

	// Everything that could have happened since the capture.
	i.ULA.Bus.Eject(0)
	i.ULA.Bus.Insert(5, patternedCartridge(10, 0x40))
	i.ULA.Bus.Drive(2).Cartridge.SetWriteProtect(false)
	i.ULA.Bus.Drive(2).Cartridge.SetDataAt(100, 0x5A)
	i.PageOut()

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	got := driveForward(i)

	if !bytes.Equal(want, got) {
		t.Error("replay after a disturbed restore diverged: the restore did not put the bays " +
			"back the way the capture found them")
	}
}

// The negative that gives the test above its teeth. If the head position and
// the window counters were not captured, a cartridge-only restore would still
// pass a weak test, because the cartridge bytes do round-trip. This proves the
// stream depends on more than the media, so the test is measuring something.
func TestCartridgeContentsAloneAreNotEnough(t *testing.T) {
	i := primedIF1(t)
	st := i.SaveState()
	want := driveForward(i)

	// What a snapshot format carries: the ROM and the cartridges, and nothing
	// about where on the tape either head had got to.
	b := New()
	if err := b.LoadROMBytes(fakeROM()); err != nil {
		t.Fatalf("LoadROMBytes: %v", err)
	}
	b.PageIn()
	b.ULA.Bus.Insert(0, patternedCartridge(12, 0x11))
	wp := patternedCartridge(12, 0x80)
	wp.SetWriteProtect(true)
	b.ULA.Bus.Insert(2, wp)

	if bytes.Equal(want, driveForward(b)) {
		t.Fatal("the media alone reproduced the stream, so this fixture is not exercising " +
			"the controller state the replay test exists to protect")
	}

	// ...and the full capture does what the media alone could not.
	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !bytes.Equal(want, driveForward(i)) {
		t.Error("full state capture failed to reproduce the stream it was taken from")
	}
}

// A capture must not alias the live machine. The registry copies the blob too,
// but a device handing back a view of a tape it is still writing to is the bug
// worth catching here.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	i := primedIF1(t)
	st := i.SaveState()

	c := i.Cartridge(0)
	before := c.DataAt(500)
	c.SetDataAt(500, before^0xFF)
	for k := 0; k < 200; k++ {
		i.HandlePortRead(PortMDR)
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := i.Cartridge(0).DataAt(500); got != before {
		t.Errorf("cartridge byte 500 = %#02x after restore, want %#02x: the capture aliased the live tape",
			got, before)
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New().StateID(); got != "if1" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "if1")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	i := New()
	if err := i.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
}

// A blob that decodes but describes an impossible cartridge must be refused
// before anything is applied. A bay left holding half a restored cartridge
// would keep running and read the wrong tape, which is the failure that is
// hardest to notice.
func TestLoadStateRejectsInconsistentCartridgeWithoutApplyingAnything(t *testing.T) {
	i := primedIF1(t)
	before := i.SaveState()

	var bad if1State
	bad.Drives[0].CartPresent = true
	bad.Drives[0].CartBlocks = 12
	bad.Drives[0].CartData = []byte{1, 2, 3} // nowhere near 12 blocks
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(bad); err != nil {
		t.Fatalf("encoding the bad state: %v", err)
	}

	if err := i.LoadState(buf.Bytes()); err == nil {
		t.Error("a cartridge whose length does not match its data must be refused")
	}
	if after := i.SaveState(); !bytes.Equal(before, after) {
		t.Error("a refused state was partly applied: the interface changed underneath the caller")
	}
}
