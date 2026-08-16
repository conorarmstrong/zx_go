package betadisk

import (
	"bytes"
	"testing"
)

// The Beta interface is a device whose guest-visible registers are the small
// part of it. The status/track/sector/data registers are what TR-DOS polls and
// what a .Z80 snapshot would store; what actually decides the next byte the
// guest receives is the sector buffer and its position, the physical head
// position of each drive, the direction the last Step went (a bare Step
// command repeats it), the selected drive and side, and — mid-write — the
// sector the transfer was addressed to when the command was issued. None of
// that is readable through the ports.
//
// These tests are written as a replay property rather than a field-by-field
// comparison, because a field-by-field test only checks the fields someone
// remembered to add.

// primedForReplay returns an interface part-way through a Read Sector: drive 1
// selected, side 1, the head parked on track 5 after a step that went outwards,
// and ten bytes of the sector already handed to the guest.
func primedForReplay(t *testing.T) *Interface {
	t.Helper()
	it := NewInterface()
	it.FDC.Mount(1, patternImage())

	it.WritePort(0x00FF, 0x05) // drive 1, no reset, bit 4 clear -> side 1
	it.WritePort(0x007F, 5)    // data register = seek target
	it.WritePort(0x001F, 0x10) // Seek: head and track register to 5
	it.WritePort(0x001F, 0x50) // Step In  (update track): head 6
	it.WritePort(0x001F, 0x70) // Step Out (update track): head 5, direction now out
	it.WritePort(0x005F, 3)    // sector register
	it.WritePort(0x001F, 0x80) // Read Sector
	for i := 0; i < 10; i++ {
		it.ReadPort(0x007F) // ten bytes consumed; the transfer is mid-flight
	}
	return it
}

// patternImage is an 80-track double-sided disk whose every byte is distinct
// enough that a read stream starting from the wrong offset is visible.
func patternImage() *Image {
	img := NewImage(80, 2)
	for i := range img.Data {
		img.Data[i] = byte(i*7 + 1)
	}
	return img
}

// replayScript drives the interface through a sequence of Beta port accesses
// and returns every byte it gave back. The order is chosen so each piece of
// hidden state is observed before the script itself overwrites it: the data
// register is read out by a Seek before any data transfer reloads it, the head
// position and step direction by a bare Step before the Seek moves the head,
// and the drive selection last, because changing it ends the run.
func replayScript(it *Interface) []byte {
	var out []byte
	rd := func(port uint16) { out = append(out, it.ReadPort(port)) }

	rd(0x00FF) // INTRQ / DRQ lines
	rd(0x001F) // status
	rd(0x003F) // track register
	rd(0x005F) // sector register

	it.WritePort(0x001F, 0x30) // bare Step (update track): repeats the last direction
	rd(0x003F)                 // where the head went
	rd(0x001F)

	it.WritePort(0x001F, 0x10) // Seek: the target is whatever the data register holds
	rd(0x003F)

	for i := 0; i < 246; i++ {
		rd(0x007F) // drain the rest of the sector transfer
	}
	rd(0x001F)
	rd(0x00FF)

	it.WritePort(0x001F, 0xC0) // Read Address
	for i := 0; i < 6; i++ {
		rd(0x007F) // track, side, sector, length, CRC
	}
	rd(0x005F) // Read Address copies the track byte here

	// Step In late in the run, so the direction a bare Step repeats is left
	// pointing the other way: without this the script never changes the step
	// direction and a capture that dropped it would pass by accident.
	it.WritePort(0x001F, 0x50)
	rd(0x003F)

	it.WritePort(0x00FF, 0x16) // drive 2 (empty), side 0
	it.WritePort(0x001F, 0x00) // Restore
	rd(0x003F)

	return out
}

// The property that matters: from a captured state, the controller must hand
// the guest the same bytes it handed it the first time.
func TestReplayingFromCapturedStateReproducesTheSamePortStream(t *testing.T) {
	it := primedForReplay(t)

	st := it.SaveState()
	first := replayScript(it)

	if err := it.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := replayScript(it)

	if !bytes.Equal(first, second) {
		t.Errorf("the Beta interface returned a different byte stream on replay from the same "+
			"captured state: some part of the controller is not being captured\nfirst diverges at %d",
			firstDiff(first, second))
	}
}

// The negative that gives the test above its teeth. A snapshot-style restore of
// the four addressable registers round-trips those registers, so a weak test
// would pass on it; this proves the port stream depends on more than them.
func TestTheAddressableRegistersAloneAreNotEnough(t *testing.T) {
	it := primedForReplay(t)
	st := it.SaveState()
	want := replayScript(it)

	// Rebuild an interface from what the guest can read back, the way a
	// snapshot format does: the four registers and the same disk.
	other := NewInterface()
	other.FDC.Mount(1, patternImage())
	other.WritePort(0x00FF, 0x05)
	other.WritePort(0x003F, 5)
	other.WritePort(0x005F, 3)
	if bytes.Equal(want, replayScript(other)) {
		t.Fatal("the register-only rebuild reproduced the stream, so this test is measuring nothing")
	}

	if err := it.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := replayScript(it); !bytes.Equal(want, got) {
		t.Errorf("full state capture failed to reproduce the stream it was taken from; "+
			"first diverges at %d", firstDiff(want, got))
	}
}

// A write transfer is the other half: the bytes already fed in, and the sector
// the command was addressed to, decide what lands on the disk when the last
// byte arrives. Neither is readable through the ports.
func TestReplayingFromCapturedStateFinishesTheSameWrite(t *testing.T) {
	img := patternImage()
	it := NewInterface()
	it.FDC.Mount(1, img)
	it.FDC.Mount(0, patternImage()) // somewhere else for the run to go afterwards

	it.WritePort(0x00FF, 0x05) // drive 1, side 1
	it.WritePort(0x007F, 9)
	it.WritePort(0x001F, 0x10) // Seek to track 9
	it.WritePort(0x005F, 7)    // sector 7
	it.WritePort(0x001F, 0xA0) // Write Sector
	for i := 0; i < 100; i++ {
		it.WritePort(0x007F, byte(0xC0+i)) // a hundred bytes in, mid-transfer
	}

	st := it.SaveState()

	feedRest := func() []byte {
		var status []byte
		for i := 100; i < SectorSize; i++ {
			it.WritePort(0x007F, byte(0xC0+i))
			status = append(status, it.ReadPort(0x001F), it.ReadPort(0x00FF))
		}
		return status
	}

	firstStatus := feedRest()
	firstSector, err := img.ReadSector(9, 1, 7)
	if err != nil {
		t.Fatalf("reading back the written sector: %v", err)
	}

	// Carry on past the capture and write a sector that differs from the first
	// in every coordinate — drive, side, track and sector — which is what a
	// rewind actually rewinds over. Without this the controller would still be
	// holding the first write's target when the state is restored, and a
	// capture that had not recorded that target would pass by accident.
	it.WritePort(0x00FF, 0x14) // drive 0, side 0
	it.WritePort(0x007F, 2)
	it.WritePort(0x001F, 0x10) // Seek to track 2
	it.WritePort(0x005F, 4)    // sector 4
	it.WritePort(0x001F, 0xA0) // Write Sector
	for i := 0; i < SectorSize; i++ {
		it.WritePort(0x007F, 0x5A)
	}

	// The medium is deliberately not part of the captured state (see state.go),
	// so the test resets it: a rewind does not un-write a floppy, and this
	// second run must put the same bytes back by itself.
	if err := img.WriteSector(9, 1, 7, make([]byte, SectorSize)); err != nil {
		t.Fatalf("clearing the target sector: %v", err)
	}
	if err := it.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	secondStatus := feedRest()
	secondSector, err := img.ReadSector(9, 1, 7)
	if err != nil {
		t.Fatalf("reading back the written sector: %v", err)
	}

	if !bytes.Equal(firstStatus, secondStatus) {
		t.Errorf("status stream differed while finishing the write; first diverges at %d",
			firstDiff(firstStatus, secondStatus))
	}
	if !bytes.Equal(firstSector, secondSector) {
		t.Errorf("the replayed write committed different data; first diverges at %d",
			firstDiff(firstSector, secondSector))
	}
	if bytes.Equal(secondSector, make([]byte, SectorSize)) {
		t.Error("the replayed write committed nothing at all")
	}
}

// The disk is the medium, not machine state: restoring must not roll back what
// has been written to it, any more than a rewind can un-write a real floppy.
func TestRestoreDoesNotRollBackTheMedium(t *testing.T) {
	img := patternImage()
	it := NewInterface()
	it.FDC.Mount(1, img)

	st := it.SaveState()

	it.WritePort(0x00FF, 0x05)
	it.WritePort(0x007F, 2)
	it.WritePort(0x001F, 0x10) // Seek to track 2
	it.WritePort(0x005F, 4)
	it.WritePort(0x001F, 0xA0) // Write Sector
	for i := 0; i < SectorSize; i++ {
		it.WritePort(0x007F, 0x5A)
	}

	if err := it.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	got, err := img.ReadSector(2, 1, 4)
	if err != nil {
		t.Fatalf("ReadSector: %v", err)
	}
	for i, b := range got {
		if b != 0x5A {
			t.Fatalf("byte %d of the written sector = %#02x after restore, want %#02x: "+
				"the capture rolled back the medium", i, b, 0x5A)
		}
	}
	if it.FDC.disks[1] != img {
		t.Error("restoring detached the mounted disk")
	}
}

// A capture must not alias the live controller. The registry copies the blob
// too, but a device handing back a view of its own transfer buffer is a bug
// worth catching where it would be introduced.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	it := primedForReplay(t)
	st := it.SaveState()
	want := replayScript(it)

	// Churn the live controller hard: drain the buffer it was captured
	// mid-transfer on, move the heads, and overwrite every register.
	for i := 0; i < 300; i++ {
		it.ReadPort(0x007F)
	}
	it.WritePort(0x00FF, 0x07) // drive 3, side 1
	it.WritePort(0x003F, 0xEE)
	it.WritePort(0x005F, 0xEE)
	it.WritePort(0x007F, 0xEE)
	it.WritePort(0x001F, 0x00) // Restore

	if err := it.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := replayScript(it); !bytes.Equal(want, got) {
		t.Errorf("the capture changed under the running controller; first diverges at %d",
			firstDiff(want, got))
	}
}

// The $FF system latch is the one piece of captured state the replay property
// cannot reach: writing it selects the drive and the side, and reading $FF
// returns the INTRQ/DRQ lines rather than the latch, so nothing the guest can
// observe depends on the stored byte. It is still real hardware state — the
// register holds its value on the board — so it is captured, and this is the
// only assertion in the file that has to name a field, because there is no
// port sequence that would notice it missing.
func TestSystemLatchRoundTrips(t *testing.T) {
	it := NewInterface()
	it.WritePort(0x00FF, 0x2D)
	st := it.SaveState()

	it.WritePort(0x00FF, 0x16)
	if err := it.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if it.sysReg != 0x2D {
		t.Errorf("system register = %#02x after restore, want %#02x", it.sysReg, 0x2D)
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := NewInterface().StateID(); got != "betadisk" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "betadisk")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	it := NewInterface()
	if err := it.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
}

// A blob that decodes but describes an impossible controller must be refused
// before anything is applied: a drive index outside 0..3 would index the drive
// array out of bounds the next time the guest touched the disk.
func TestLoadStateRejectsOutOfRangeStateWithoutApplyingIt(t *testing.T) {
	it := primedForReplay(t)
	good := it.SaveState()
	want := replayScript(it)
	if err := it.LoadState(good); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	for _, tc := range []struct {
		name    string
		corrupt func(*betaState)
	}{
		{"drive out of range", func(s *betaState) { s.FDC.Drive = 4 }},
		{"write drive out of range", func(s *betaState) { s.FDC.WriteDrive = -1 }},
		{"side out of range", func(s *betaState) { s.FDC.Side = 2 }},
		{"buffer position past the buffer", func(s *betaState) { s.FDC.BufPos = len(s.FDC.Buf) + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s betaState
			if err := decodeBetaState(good, &s); err != nil {
				t.Fatalf("decoding the good blob: %v", err)
			}
			tc.corrupt(&s)
			if err := it.LoadState(encodeBetaState(s)); err == nil {
				t.Fatal("an impossible controller state was accepted")
			}
			if got := replayScript(it); !bytes.Equal(want, got) {
				t.Errorf("the rejected state was partly applied; stream diverges at %d",
					firstDiff(want, got))
			}
			if err := it.LoadState(good); err != nil {
				t.Fatalf("restoring for the next case: %v", err)
			}
		})
	}
}

// firstDiff reports the index of the first differing byte, or -1.
func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}
