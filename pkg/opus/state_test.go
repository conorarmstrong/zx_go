package opus

import (
	"bytes"
	"reflect"
	"testing"
)

// The Opus Discovery is memory-mapped, so the guest reaches every part of it
// through ordinary loads and stores in the window above the ROM. What the four
// WD1770 registers show is the small part. What decides the next byte the guest
// gets is the transfer buffer and its position, the sector a write was
// addressed to when it started, the 6821's two-registers-behind-one-address
// state, the 2 KB SRAM the ROM builds its tables in, the pager's two flip-flops
// mid-flight, and the DRQ byte-clock — the Opus wires DRQ to NMI and the ROM
// moves one byte per interrupt, so a capture that loses the pending byte-period
// stalls the transfer with BUSY still asserted.
//
// These tests are written as a replay property rather than field by field,
// because a field-by-field test only checks the fields someone remembered to
// add. Where a piece of state cannot be reached that way, the test names it.

// patternImage is a disk whose every byte is distinct enough that a read
// starting from the wrong track, sector or offset is visible in the stream.
func patternImage(seed int) *Image {
	img := NewImage()
	for i := range img.Data {
		img.Data[i] = byte(i*7 + seed*29 + 1)
	}
	return img
}

// testClock is the CPU T-state counter the interface is paced by. The script
// advances it explicitly, because the byte-period is what the capture has to
// preserve and a wall-clock would make the result nondeterministic.
type testClock struct{ t uint64 }

// newPatternOpus builds an interface with a distinct disk in both drives.
func newPatternOpus(t *testing.T) (*Interface, *testClock) {
	t.Helper()
	rom := make([]byte, ROMSize)
	for i := range rom {
		rom[i] = byte(i*3 + 11)
	}
	i, err := NewInterface(rom)
	if err != nil {
		t.Fatalf("NewInterface: %v", err)
	}
	i.Dev.Mount(0, patternImage(1))
	i.Dev.Mount(1, patternImage(2))
	clk := &testClock{}
	i.SetClock(func() uint64 { return clk.t })
	return i, clk
}

// selectThrough drives the PIA the way the ROM does, but through the overlay
// rather than straight at the Device, so the pager has to be paged in for it to
// land. fdc_test.go's selectDrive talks to the Device directly.
func selectThrough(i *Interface, drive int) {
	selectDrive(i.Dev, drive)
}

// primedForReplay returns an interface part-way through a sector read: drive 1
// selected, the head on track 7, sector 5 half consumed, a byte-period part
// way through, the SRAM carrying a pattern, and the pager armed but not yet
// flipped. Drive 0 is left on a different track so a capture that keeps only
// one drive's position is caught.
func primedForReplay(t *testing.T, i *Interface, clk *testClock) {
	t.Helper()

	for addr := uint16(RAMBase); addr <= RAMTop; addr++ {
		i.Write(addr, byte(addr*5+3))
	}

	selectThrough(i, 0)
	i.Write(FDCBase+1, 3)    // track register
	i.Write(FDCBase+0, 0x10) // SEEK: drive 0's head to track 3

	selectThrough(i, 1)
	i.Write(FDCBase+1, 7)    // track 7
	i.Write(FDCBase+2, 5)    // sector 5
	i.Write(FDCBase+0, 0x80) // READ SECTOR

	// Consume a few bytes, each one arriving a byte-period after the last.
	for n := 0; n < 6; n++ {
		clk.t += BytePeriodTStates
		i.Dev.TickTo(clk.t)
		i.Read(FDCBase + 3)
	}
	// Stop PART WAY through the next byte-period, so the capture has to carry
	// the remainder. Landing exactly on a boundary would hide it.
	clk.t += BytePeriodTStates / 3
	i.Dev.TickTo(clk.t)

	// Arm the pager without letting the change land: the flip is delayed one
	// M1 cycle, so a capture taken here holds an armed but unlanded change.
	i.PreFetch(RST8)
}

// replayScript drives the interface the way the machine does — opcode fetches
// pacing the byte-clock, loads and stores through the window — and returns
// everything it gave back, including the NMI pulses that actually move a
// transfer forward.
func replayScript(i *Interface, clk *testClock) []byte {
	var out []byte
	nmiCount := 0
	i.Dev.SetNMI(func() { nmiCount++ })

	rd := func(addr uint16) {
		v, _ := i.Dev.TryRead(addr)
		out = append(out, v)
	}
	note := func() {
		out = append(out, byte(nmiCount))
		if i.Active() {
			out = append(out, 1)
		} else {
			out = append(out, 0)
		}
	}

	// Let the armed pager change land, then walk the rest of the transfer.
	note()
	i.PreFetch(0x1234) // an ordinary fetch: the armed change lands here
	note()

	for step := 0; step < 300; step++ {
		clk.t += BytePeriodTStates / 2
		i.PreFetch(0x1234)
		rd(FDCBase + 0) // status: BUSY and DRQ
		note()
		if v, _ := i.Dev.TryRead(FDCBase + 0); v&StatusDRQ != 0 {
			rd(FDCBase + 3) // the byte DRQ was raised for
		}
	}

	rd(FDCBase + 1) // track register
	rd(FDCBase + 2) // sector register

	// The 6821 keeps two registers behind each address, so read both halves.
	rd(PIABase + 1) // CRA
	rd(PIABase + 0) // PRA or DDRA, whichever CRA bit 2 selects
	rd(PIABase + 3) // CRB
	rd(PIABase + 2)

	// A sample of the SRAM, which nothing else in the script touches.
	for addr := uint16(RAMBase); addr <= RAMTop; addr += 149 {
		rd(addr)
	}

	// Start a fresh read from whichever drive the PIA currently selects. The
	// two disks carry different patterns, so these bytes reveal the drive
	// select, the data direction and the control registers behind it — none of
	// which the reads above can distinguish, because they report the latched
	// value rather than what it does.
	i.Dev.TryWrite(FDCBase+1, 9)
	i.Dev.TryWrite(FDCBase+2, 2)
	i.Dev.TryWrite(FDCBase+0, 0x80)
	for n := 0; n < 8; n++ {
		clk.t += BytePeriodTStates
		i.PreFetch(0x1234)
		rd(FDCBase + 3)
	}

	// Page out and back in, which only behaves correctly if both pager
	// flip-flops were restored.
	i.PreFetch(PageOut)
	note()
	i.PreFetch(0x1234)
	note()
	i.PreFetch(PageIn)
	note()
	i.PreFetch(0x1234)
	note()

	return out
}

// The property that matters: from a captured state, the interface must behave
// exactly as it did the first time.
func TestReplayingFromCapturedStateReproducesTheSameStream(t *testing.T) {
	i, clk := newPatternOpus(t)
	primedForReplay(t, i, clk)

	st := i.SaveState()
	mark := clk.t
	first := replayScript(i, clk)

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	clk.t = mark
	second := replayScript(i, clk)

	if !bytes.Equal(first, second) {
		t.Errorf("the Opus returned a different stream on replay from the same captured state: "+
			"some part of the interface is not being captured\nfirst diverges at %d",
			firstDiff(first, second))
	}
}

// The negative that gives the test above its teeth. Rebuilding from the four
// register values a snapshot would store reproduces those registers and
// nothing else.
func TestTheRegisterFileAloneIsNotEnough(t *testing.T) {
	i, clk := newPatternOpus(t)
	primedForReplay(t, i, clk)

	st := i.SaveState()
	mark := clk.t
	want := replayScript(i, clk)

	other, otherClk := newPatternOpus(t)
	selectThrough(other, 1)
	other.Write(FDCBase+1, 7)
	other.Write(FDCBase+2, 5)
	otherClk.t = mark
	if bytes.Equal(want, replayScript(other, otherClk)) {
		t.Fatal("the register-only rebuild reproduced the stream, so this test is measuring nothing")
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	clk.t = mark
	if got := replayScript(i, clk); !bytes.Equal(want, got) {
		t.Errorf("full state capture failed to reproduce the stream it was taken from; "+
			"first diverges at %d", firstDiff(want, got))
	}
}

// A write transfer is the other half: the bytes already fed in, and the track
// and sector the command was addressed to, decide what lands on the disk when
// the last byte arrives. The target is latched when the command starts, so
// nothing the guest can read reports it.
func TestReplayingFromCapturedStateFinishesTheSameWrite(t *testing.T) {
	i, clk := newPatternOpus(t)
	selectThrough(i, 0)
	i.Write(FDCBase+1, 11) // track 11
	i.Write(FDCBase+2, 4)  // sector 4
	i.Write(FDCBase+0, 0xA0)

	feed := func(from, to int) {
		for n := from; n < to; n++ {
			clk.t += BytePeriodTStates
			i.Dev.TickTo(clk.t)
			i.Write(FDCBase+3, byte(0xB0+n))
		}
	}
	feed(0, 100) // a hundred bytes in, mid-transfer

	st := i.SaveState()
	mark := clk.t

	readBack := func() []byte {
		b, err := i.Dev.Disk(0).ReadSector(11, 4)
		if err != nil {
			t.Fatalf("ReadSector: %v", err)
		}
		return append([]byte(nil), b...)
	}
	// The medium is deliberately not captured, so the second run has to put the
	// same bytes back by itself. Without clearing the sector first, the first
	// run's committed data is still sitting there and the comparison below
	// passes whether or not the replayed write did anything at all.
	clearSector := func() {
		if err := i.Dev.Disk(0).WriteSector(11, 4, make([]byte, SectorSize)); err != nil {
			t.Fatalf("clearing the target sector: %v", err)
		}
	}

	feed(100, SectorSize)
	firstSector := readBack()

	// Carry on past the capture: a different drive, track and sector, which is
	// what a rewind actually rewinds over. Without this the controller would
	// still hold the first write's target, and a capture that had not recorded
	// it would pass by accident.
	selectThrough(i, 1)
	i.Write(FDCBase+1, 2)
	i.Write(FDCBase+2, 9)
	i.Write(FDCBase+0, 0xA0)
	feed(0, SectorSize)

	// Leave a READ in flight rather than a write, so a capture that dropped the
	// transfer direction cannot resume writing by accident.
	selectThrough(i, 0)
	i.Write(FDCBase+1, 3)
	i.Write(FDCBase+2, 1)
	i.Write(FDCBase+0, 0x80)

	clearSector()
	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	clk.t = mark
	feed(100, SectorSize)
	secondSector := readBack()

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
	i, clk := newPatternOpus(t)
	disk := i.Dev.Disk(0)

	st := i.SaveState()

	selectThrough(i, 0)
	i.Write(FDCBase+1, 6)
	i.Write(FDCBase+2, 2)
	i.Write(FDCBase+0, 0xA0)
	for n := 0; n < SectorSize; n++ {
		clk.t += BytePeriodTStates
		i.Dev.TickTo(clk.t)
		i.Write(FDCBase+3, 0x5A)
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	got, err := disk.ReadSector(6, 2)
	if err != nil {
		t.Fatalf("ReadSector: %v", err)
	}
	for n, b := range got {
		if b != 0x5A {
			t.Fatalf("byte %d of the written sector = %#02x after restore, want %#02x: "+
				"the capture rolled back the medium", n, b, 0x5A)
		}
	}
	if i.Dev.Disk(0) != disk {
		t.Error("restoring detached the mounted disk")
	}
}

// A capture must not alias the live interface. A device handing back a view of
// its own transfer buffer is a bug worth catching where it would be introduced.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	i, clk := newPatternOpus(t)
	primedForReplay(t, i, clk)

	st := i.SaveState()
	mark := clk.t
	want := replayScript(i, clk)

	// Churn hard: finish the transfer, move the head, rewrite the SRAM and the
	// PIA, and leave the pager somewhere else.
	for n := 0; n < 400; n++ {
		clk.t += BytePeriodTStates
		i.Dev.TickTo(clk.t)
		i.Read(FDCBase + 3)
	}
	selectThrough(i, 0)
	i.Write(FDCBase+0, 0x00) // RESTORE
	i.Write(FDCBase+2, 17)   // a different sector register
	for addr := uint16(RAMBase); addr <= RAMTop; addr++ {
		i.Write(addr, 0xEE)
	}
	// Reconfigure the PIA to something the fixture never used. selectThrough
	// above leaves it exactly as the fixture did, so on its own it would move
	// neither the data direction nor the control registers, and a capture that
	// dropped them would pass because they never changed.
	i.Write(PIABase+1, 0x00) // CRA bit 2 clear: the next PRA access is DDRA
	// 0xF0 rather than any mask that keeps the low bits: port A reads back as
	// pr&ddr, so a direction change that still passes the drive-select bits
	// through leaves the readback identical and proves nothing.
	i.Write(PIABase+0, 0xF0)
	i.Write(PIABase+3, 0x24) // CRB somewhere else entirely
	i.Write(PIABase+2, 0x55)
	i.PreFetch(PageOut)
	i.PreFetch(0x1234)

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	clk.t = mark
	if got := replayScript(i, clk); !bytes.Equal(want, got) {
		t.Errorf("the capture changed under the running interface; first diverges at %d",
			firstDiff(want, got))
	}
}

// feedFmt writes one byte of a WRITE TRACK stream, paced like the real one.
func feedFmt(i *Interface, clk *testClock, v byte) {
	clk.t += BytePeriodTStates
	i.Dev.TickTo(clk.t)
	i.Write(FDCBase+3, v)
}

// feedFormatUpToData feeds sync, an ID address mark, the four ID bytes, sync
// again and the data address mark, leaving the parser inside the data field
// with a whole sector still to come. Reaching that point is what moves the
// parser's own fields off their defaults: a run of filler bytes leaves every
// one of them at zero, so a capture taken there proves nothing about them.
func feedFormatUpToData(i *Interface, clk *testClock, track, sector byte) {
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync)
	}
	feedFmt(i, clk, markIDAddress)
	feedFmt(i, clk, track)
	feedFmt(i, clk, 0)      // side
	feedFmt(i, clk, sector) // sector
	feedFmt(i, clk, 1)      // size code 1 -> 256 bytes, which is the Opus sector
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync)
	}
	feedFmt(i, clk, markData)
}

// A format in flight collects a whole raw track through the same data register
// a sector write uses, and commits it completely differently. The flag, the
// field the parser is inside, how many bytes that field still needs and the
// bytes already collected all have to survive, or the rest of the guest's
// stream is parsed as something else and the sector never lands.
func TestAFormatInFlightSurvivesTheRoundTrip(t *testing.T) {
	i, clk := newPatternOpus(t)
	selectThrough(i, 0)
	i.Write(FDCBase+1, 5)    // track 5
	i.Write(FDCBase+0, 0xF0) // WRITE TRACK
	if !i.Dev.fdc.formatting {
		t.Fatal("WRITE TRACK did not start a format, so this test proves nothing")
	}
	feedFormatUpToData(i, clk, 5, 6)
	for n := 0; n < 100; n++ {
		feedFmt(i, clk, byte(0x30+n)) // 100 of the sector's 256 bytes
	}
	f := &i.Dev.fdc
	if f.fmtField != fieldData || f.fmtNeed != SectorSize || len(f.fmtData) != 100 {
		t.Fatalf("the fixture is not mid-data-field: field=%d need=%d have=%d",
			f.fmtField, f.fmtNeed, len(f.fmtData))
	}

	st := i.SaveState()

	// Leave the parser somewhere that differs from the capture in EVERY field,
	// which takes three steps rather than one. A second format that reaches the
	// same point leaves formatting, the current field, the size and the have-ID
	// flag exactly as they were, and a capture that dropped any of them would
	// pass because the value never moved.
	//
	// First a format whose ID declares size code 0, so the byte count the data
	// field is waiting for becomes 128 instead of 256.
	i.Write(FDCBase+0, 0xD0) // FORCE INTERRUPT
	i.Write(FDCBase+1, 8)
	i.Write(FDCBase+0, 0xF0)
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync)
	}
	feedFmt(i, clk, markIDAddress)
	feedFmt(i, clk, 8)
	feedFmt(i, clk, 0)
	feedFmt(i, clk, 2)
	feedFmt(i, clk, 0) // size code 0 -> 128 bytes
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync)
	}
	feedFmt(i, clk, markData)

	// Then stop part way through an ID field, which moves the current field and
	// clears the have-ID flag.
	i.Write(FDCBase+0, 0xD0)
	i.Write(FDCBase+0, 0xF0)
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync)
	}
	feedFmt(i, clk, markIDAddress)
	feedFmt(i, clk, 8)
	feedFmt(i, clk, 0)

	// Finally abandon it, so the format flag itself differs too.
	i.Write(FDCBase+0, 0xD0)

	if f.formatting || f.fmtField != fieldID || f.fmtNeed != 128 || f.fmtHaveID {
		t.Fatalf("the churn did not move every parser field: formatting=%v field=%d need=%d haveID=%v",
			f.formatting, f.fmtField, f.fmtNeed, f.fmtHaveID)
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	// Finish the sector the FIRST format was collecting. It can only land in
	// the right place, with the right contents, if every parser field came back.
	for n := 100; n < SectorSize; n++ {
		feedFmt(i, clk, byte(0x30+n))
	}
	got, err := i.Dev.Disk(0).ReadSector(5, 6)
	if err != nil {
		t.Fatalf("the formatted sector is not readable: %v", err)
	}
	for n := range got {
		if want := byte(0x30 + n); got[n] != want {
			t.Fatalf("formatted sector byte %d = %#02x, want %#02x", n, got[n], want)
		}
	}
}

// The sync-run counter is the one parser field the test above cannot reach: a
// mark only counts as a mark when it follows three sync bytes, and once the
// parser is inside a field the counter is not consulted. So this captures
// part-way through the sync run itself.
func TestAPartialSyncRunSurvivesTheRoundTrip(t *testing.T) {
	i, clk := newPatternOpus(t)
	selectThrough(i, 0)
	i.Write(FDCBase+1, 12)
	i.Write(FDCBase+0, 0xF0) // WRITE TRACK
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync) // the run is complete but no mark has arrived
	}
	if i.Dev.fdc.fmtSync != 3 {
		t.Fatalf("sync counter = %d, want 3", i.Dev.fdc.fmtSync)
	}

	st := i.SaveState()
	feedFmt(i, clk, 0x4E) // any non-sync byte resets the run
	if i.Dev.fdc.fmtSync != 0 {
		t.Fatal("the filler byte did not reset the sync run, so the restore proves nothing")
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	// With the run restored, the next ID address mark is recognised. Without
	// it, the mark is read as ordinary track data and the sector is lost.
	feedFmt(i, clk, markIDAddress)
	if i.Dev.fdc.fmtField != fieldID {
		t.Error("the ID address mark was not recognised: the restored sync run was lost")
	}
}

// A data address mark only opens a data field when an ID field has already
// been seen, so the parser carries that fact between the two. It is consulted
// at exactly one moment — the mark arriving — which is why the mid-data-field
// fixture above cannot reach it: by then the decision has been made.
func TestTheHaveIDFlagSurvivesTheRoundTrip(t *testing.T) {
	i, clk := newPatternOpus(t)
	selectThrough(i, 0)
	i.Write(FDCBase+1, 4)
	i.Write(FDCBase+0, 0xF0) // WRITE TRACK

	// Sync, an ID address mark and the four ID bytes. The ID is complete, so
	// the parser is back to scanning while holding the fact that it has one.
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync)
	}
	feedFmt(i, clk, markIDAddress)
	feedFmt(i, clk, 4) // track
	feedFmt(i, clk, 0) // side
	feedFmt(i, clk, 7) // sector
	feedFmt(i, clk, 1) // size code 1 -> 256 bytes
	f := &i.Dev.fdc
	if !f.fmtHaveID || f.fmtField != fieldNone {
		t.Fatalf("the fixture is not between the ID and its data: haveID=%v field=%d",
			f.fmtHaveID, f.fmtField)
	}

	st := i.SaveState()

	// Restart the format, which clears the flag while leaving the parser
	// scanning exactly as it was.
	i.Write(FDCBase+0, 0xD0)
	i.Write(FDCBase+0, 0xF0)
	if f.fmtHaveID {
		t.Fatal("the restart did not clear the flag, so the restore below proves nothing")
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for n := 0; n < 3; n++ {
		feedFmt(i, clk, markSync)
	}
	feedFmt(i, clk, markData)
	if f.fmtField != fieldData {
		t.Error("the data address mark was ignored: without the have-ID flag the sector's own " +
			"bytes are parsed as gap and the sector is never written")
	}
}

// WRITE TRACK has no length in the command: it runs from one index pulse to
// the next, so the count of bytes taken so far is the only thing that ends it.
// A capture that lost the count restores a format that runs a whole extra
// revolution, with BUSY asserted the entire time.
func TestTheFormatByteCountSurvivesTheRoundTrip(t *testing.T) {
	i, clk := newPatternOpus(t)
	selectThrough(i, 0)
	i.Write(FDCBase+1, 3)
	i.Write(FDCBase+0, 0xF0) // WRITE TRACK

	const short = 10
	for n := 0; n < TrackRawBytes-short; n++ {
		feedFmt(i, clk, 0x4E) // gap filler: no marks, so no sectors are parsed
	}
	if !i.Dev.fdc.formatting {
		t.Fatal("the format ended early, so this test proves nothing")
	}

	st := i.SaveState()

	// Restart the format, which puts the count back to zero.
	i.Write(FDCBase+0, 0xD0)
	i.Write(FDCBase+0, 0xF0)
	if i.Dev.fdc.fmtPos != 0 {
		t.Fatal("the restart did not reset the count, so the restore below proves nothing")
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for n := 0; n < short; n++ {
		feedFmt(i, clk, 0x4E)
	}
	if i.Dev.fdc.formatting {
		t.Error("the format did not reach the index pulse: the byte count was lost, so the " +
			"controller stays BUSY for another whole revolution")
	}
}

// The data register is only observable while no transfer is running: during one
// every read returns the buffer instead, and the register is overwritten by the
// transfer as it goes. A SEEK is what reveals it, because SEEK takes its target
// from the data register.
func TestTheDataRegisterRoundTrips(t *testing.T) {
	i, _ := newPatternOpus(t)
	selectThrough(i, 0)
	i.Write(FDCBase+3, 0x2A)

	st := i.SaveState()
	i.Write(FDCBase+3, 0x11)

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	i.Write(FDCBase+0, 0x10) // SEEK: the head goes to whatever the register holds
	if got, _ := i.Dev.TryRead(FDCBase + 1); got != 0x2A {
		t.Errorf("SEEK went to track %d after restore, want %d: the data register was lost",
			got, 0x2A)
	}
}

// The byte-clock has a "has it ever been read" flag, because the first tick
// establishes a baseline rather than measuring an interval. It is false only
// before the first tick, so a capture taken then is the only one that can prove
// it round-trips — and getting it wrong swallows the first byte-period of
// whatever transfer follows the restore.
func TestTheByteClockRemembersItHasNeverBeenRead(t *testing.T) {
	i, clk := newPatternOpus(t)
	if i.Dev.fdc.started {
		t.Fatal("the clock has already been read, so this test proves nothing")
	}

	st := i.SaveState()
	clk.t = 500_000
	i.Dev.TickTo(clk.t)
	if !i.Dev.fdc.started {
		t.Fatal("a tick did not mark the clock as read, so the restore below proves nothing")
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if i.Dev.fdc.started {
		t.Error("the clock came back marked as already read: the next tick would be measured " +
			"as a huge elapsed interval rather than establishing a baseline")
	}
}

// The pager's flip is delayed one M1 cycle, so between arming and landing there
// is a state no single boolean describes. A capture taken in that gap has to
// hold both flip-flops, or the overlay lands a fetch early or not at all.
func TestAnArmedButUnlandedPageChangeSurvives(t *testing.T) {
	i, _ := newPatternOpus(t)
	i.PreFetch(PageOut) // arms a page-out; the overlay is still in
	if !i.Active() {
		t.Fatal("the page-out landed immediately, so this test proves nothing")
	}

	st := i.SaveState()
	i.PreFetch(0x1234) // the change lands: the overlay goes out
	if i.Active() {
		t.Fatal("the armed change never landed, so the restore below proves nothing")
	}

	if err := i.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !i.Active() {
		t.Fatal("the overlay was not restored")
	}
	i.PreFetch(0x1234)
	if i.Active() {
		t.Error("the armed page-out was lost: the overlay stayed in for a fetch it should have left")
	}
}

func TestStateIDIsStable(t *testing.T) {
	i, _ := newPatternOpus(t)
	if got := i.StateID(); got != "opus" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "opus")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	i, _ := newPatternOpus(t)
	if err := i.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
}

// A blob that decodes but describes an impossible interface must be refused
// before anything is applied.
func TestLoadStateRejectsOutOfRangeStateWithoutApplyingIt(t *testing.T) {
	i, clk := newPatternOpus(t)
	primedForReplay(t, i, clk)
	good := i.SaveState()
	mark := clk.t
	want := replayScript(i, clk)
	if err := i.LoadState(good); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	for _, tc := range []struct {
		name    string
		corrupt func(*opusState)
	}{
		{"drive out of range", func(s *opusState) { s.FDC.Drive = 2 }},
		{"drive negative", func(s *opusState) { s.FDC.Drive = -1 }},
		{"buffer position past the buffer", func(s *opusState) { s.FDC.Pos = len(s.FDC.Buf) + 1 }},
		{"SRAM the wrong size", func(s *opusState) { s.RAM = []byte{1, 2, 3} }},
		{"negative byte-period remainder", func(s *opusState) { s.FDC.Remaining = -1 }},
		// Pos == len(Buf) is not a position the live controller can hold: as
		// soon as a transfer reaches its end readData drops the buffer. Accept
		// it from a blob and the guest's next access indexes one past the end.
		{"position exactly at the end of the buffer", func(s *opusState) { s.FDC.Pos = len(s.FDC.Buf) }},
		// The format parser indexes fmtID[3] the moment a data address mark
		// arrives, and fmtID[2] when the sector commits, so a have-ID flag that
		// disagrees with the ID actually held is a panic waiting for the next
		// byte the guest writes.
		{"have-ID set with no ID collected", func(s *opusState) {
			s.FDC.Formatting, s.FDC.FmtHaveID, s.FDC.FmtID = true, true, nil
		}},
		{"data field open with no ID collected", func(s *opusState) {
			s.FDC.Formatting, s.FDC.FmtField, s.FDC.FmtID = true, 2, nil
			s.FDC.FmtNeed = SectorSize
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s opusState
			if err := decodeOpusState(good, &s); err != nil {
				t.Fatalf("decoding the good blob: %v", err)
			}
			tc.corrupt(&s)
			if err := i.LoadState(encodeOpusState(s)); err == nil {
				t.Fatal("an impossible interface state was accepted")
			}
			clk.t = mark
			if got := replayScript(i, clk); !bytes.Equal(want, got) {
				t.Errorf("the rejected state was partly applied; stream diverges at %d",
					firstDiff(want, got))
			}
			if err := i.LoadState(good); err != nil {
				t.Fatalf("restoring for the next case: %v", err)
			}
		})
	}
}

// firstDiff reports the index of the first differing byte, or -1.
func firstDiff(a, b []byte) int {
	for n := 0; n < len(a) && n < len(b); n++ {
		if a[n] != b[n] {
			return n
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

// decodeForTest exposes the wire struct so the guard below can walk it.
func decodeForTest(t *testing.T, blob []byte) opusState {
	t.Helper()
	var s opusState
	if err := decodeOpusState(blob, &s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}

// walkFields flattens the nested wire struct to dotted paths, so a field added
// inside fdcState or piaState is walked as well as one added at the top.
func walkFields(prefix string, v reflect.Value, out map[string]any) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		name := prefix + f.Name
		if f.Type.Kind() == reflect.Struct {
			walkFields(name+".", v.Field(i), out)
			continue
		}
		out[name] = v.Field(i).Interface()
	}
}

// capturePoints are the fixtures the guard walks. Several are needed because
// some fields are mutually exclusive by construction: a controller mid-read is
// not mid-format, and the format parser cannot be inside a data field and
// part-way through a sync run at the same time.
var capturePoints = []struct {
	name  string
	build func(*testing.T) *Interface
}{
	{"mid sector read, pager armed", func(t *testing.T) *Interface {
		i, clk := newPatternOpus(t)
		primedForReplay(t, i, clk)
		return i
	}},
	{"mid sector write", func(t *testing.T) *Interface {
		i, clk := newPatternOpus(t)
		selectThrough(i, 0)
		i.Write(FDCBase+1, 11)
		i.Write(FDCBase+2, 4)
		i.Write(FDCBase+0, 0xA0)
		for n := 0; n < 20; n++ {
			clk.t += BytePeriodTStates
			i.Dev.TickTo(clk.t)
			i.Write(FDCBase+3, byte(0xB0+n))
		}
		return i
	}},
	{"mid format data field", func(t *testing.T) *Interface {
		i, clk := newPatternOpus(t)
		selectThrough(i, 0)
		i.Write(FDCBase+1, 5)
		i.Write(FDCBase+0, 0xF0)
		feedFormatUpToData(i, clk, 5, 6)
		for n := 0; n < 30; n++ {
			feedFmt(i, clk, byte(0x30+n))
		}
		return i
	}},
	{"part way through a format sync run", func(t *testing.T) *Interface {
		i, clk := newPatternOpus(t)
		selectThrough(i, 0)
		i.Write(FDCBase+0, 0xF0)
		for n := 0; n < 3; n++ {
			feedFmt(i, clk, markSync)
		}
		return i
	}},
	{"paged out", func(t *testing.T) *Interface {
		i, _ := newPatternOpus(t)
		i.PreFetch(PageOut)
		i.PreFetch(0x1234)
		return i
	}},
	{"SRAM written", func(t *testing.T) *Interface {
		i, _ := newPatternOpus(t)
		for addr := uint16(RAMBase); addr <= RAMTop; addr++ {
			i.Write(addr, byte(addr*5+3))
		}
		return i
	}},
}

// Every captured field must be moved by at least one fixture, or the round-trip
// tests say nothing about it: a field no fixture moves round-trips whether or
// not it is restored, which is exactly how a dropped restore hides.
//
// Walked by reflection rather than by a hand-written list, so a field added to
// the capture and forgotten here shows up as a field nothing moves.
func TestEveryCapturedFieldIsMovedBySomeFixture(t *testing.T) {
	fresh, _ := newPatternOpus(t)
	base := map[string]any{}
	walkFields("", reflect.ValueOf(decodeForTest(t, fresh.SaveState())), base)

	moved := map[string]string{}
	for _, cp := range capturePoints {
		got := map[string]any{}
		walkFields("", reflect.ValueOf(decodeForTest(t, cp.build(t).SaveState())), got)
		for name, want := range base {
			if _, done := moved[name]; done {
				continue
			}
			if !reflect.DeepEqual(want, got[name]) {
				moved[name] = cp.name
			}
		}
	}

	for name := range base {
		if _, ok := moved[name]; !ok {
			t.Errorf("no fixture moves %s off its power-on value, so every round-trip test "+
				"passes for it whether or not it is restored", name)
		}
	}
}
