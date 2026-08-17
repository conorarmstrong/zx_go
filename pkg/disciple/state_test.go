package disciple

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The DISCiPLE's four addressable WD1772 registers are the small part of it.
// What decides the next byte the guest receives is the sector transfer buffer
// and its position, the physical head position of EACH drive (which the Track
// Register only tracks when a command's update flag is set), the direction the
// last step went (a bare Step repeats it), whether the in-flight command was
// multi-sector (the Sector Register auto-increments when it finishes), and
// which command class last ran (it changes what the status bits mean). None of
// that is readable through the ports, and none of it is in any snapshot format.
//
// These tests are written as a replay property rather than field by field,
// because a field-by-field test only checks the fields someone remembered to
// add. Where a piece of state genuinely cannot be reached through the ports,
// the test says so and names the field directly.

// patternMGT writes an MGT image whose every byte is distinct enough that a
// read starting from the wrong offset, the wrong side or the wrong cylinder is
// visible in the stream.
func patternMGT(t *testing.T) string {
	t.Helper()
	const (
		sides   = 2
		cyls    = 80
		sectors = 10
		secSize = 512
	)
	img := make([]byte, sides*cyls*sectors*secSize)
	for logical := 0; logical < sides*cyls; logical++ {
		for s := 0; s < sectors; s++ {
			base := (logical*sectors + s) * secSize
			for i := 0; i < secSize; i++ {
				img[base+i] = byte(logical*31 + s*17 + i*7 + 1)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "pattern.mgt")
	if err := os.WriteFile(path, img, 0644); err != nil {
		t.Fatalf("write pattern.mgt: %v", err)
	}
	return path
}

// newPatternDisciple builds a DISCiPLE with a distinct disk in both drives, so
// that a capture which loses the selected drive, or one drive's head position,
// shows up as different bytes rather than as an error.
func newPatternDisciple(t *testing.T) *Disciple {
	t.Helper()
	dir := createTestROMDir(t)
	d, err := NewDisciple(dir, newTestMemory(t, dir))
	if err != nil {
		t.Fatalf("NewDisciple: %v", err)
	}
	for drive := 0; drive < 2; drive++ {
		if err := d.LoadDisk(drive, patternMGT(t)); err != nil {
			t.Fatalf("LoadDisk(%d): %v", drive, err)
		}
	}
	return d
}

// primedForReplay returns a DISCiPLE part-way through a multi-sector Read
// Sector: drive 1 selected, side 1, the head parked on track 5 after a step
// that went outwards, and ten bytes of the sector already handed to the guest.
// Drive 0's head is deliberately left somewhere else, so a capture that keeps
// only one head position is caught.
func primedForReplay(t *testing.T) *Disciple {
	t.Helper()
	d := newPatternDisciple(t)

	// Park drive 0's head on track 9, then leave it selected no longer.
	d.HandlePortWrite(portControl, 0x01) // bit 0 set -> drive 0, side 0
	d.HandlePortWrite(portFDCData, 9)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek to 9

	// Drive 1, side 1.
	d.HandlePortWrite(portControl, 0x02) // bit 0 clear -> drive 1, bit 1 -> side 1
	d.HandlePortWrite(portFDCData, 5)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek to 5
	d.HandlePortWrite(portFDCCmdStatus, 0x50) // Step-In  (u set): head 6
	d.HandlePortWrite(portFDCCmdStatus, 0x70) // Step-Out (u set): head 5, direction now out

	d.HandlePortWrite(portFDCSector, 3)
	d.HandlePortWrite(portFDCCmdStatus, 0x90) // Read Sector with m set: multi-sector
	for i := 0; i < 10; i++ {
		d.HandlePortRead(portFDCData) // ten bytes consumed; the transfer is mid-flight
	}
	return d
}

// replayScript drives the interface through a sequence of port accesses and
// returns every byte it gave back. The order is chosen so each piece of hidden
// state is observed before the script itself overwrites it: the in-flight
// transfer is drained first, the multi-sector auto-increment is read straight
// after it completes, the step direction by a bare Step before anything else
// moves the head, and the drive selection last, because changing it ends the
// run on this drive.
func replayScript(d *Disciple) []byte {
	var out []byte
	rd := func(p uint16) {
		v, _ := d.HandlePortRead(p)
		out = append(out, v)
	}

	rd(portFDCCmdStatus) // status: busy, DRQ, motor, and the command class
	rd(portFDCTrack)
	rd(portFDCSector)

	// Drain the rest of the in-flight sector. A capture that lost the buffer,
	// its length or its position diverges here on the very first byte.
	for i := 0; i < 502; i++ {
		rd(portFDCData)
	}
	rd(portFDCCmdStatus) // the transfer has ended: busy and DRQ must have dropped
	rd(portFDCSector)    // multi-sector armed -> the Sector Register advanced

	// A bare Step repeats the last direction, which is the only way to observe
	// it. The head went outwards when the fixture was built, so this must too.
	d.HandlePortWrite(portFDCCmdStatus, 0x30) // Step with u set
	rd(portFDCTrack)
	rd(portFDCCmdStatus)

	// Read Address reports the ID field under the head, so it reveals the
	// physical cylinder and the selected side rather than the Track Register.
	d.HandlePortWrite(portFDCCmdStatus, 0xC0)
	for i := 0; i < 6; i++ {
		rd(portFDCData)
	}
	rd(portFDCSector) // Read Address copies the ID's track byte here

	// Switch to the other drive last: its head is on a different cylinder, so a
	// capture that kept only one entry of headTrack diverges from here on.
	d.HandlePortWrite(portControl, 0x01) // drive 0, side 0
	d.HandlePortWrite(portFDCCmdStatus, 0xC0)
	for i := 0; i < 6; i++ {
		rd(portFDCData)
	}

	// Step In at the very end, so the direction a bare Step repeats is left
	// pointing the OTHER way from the captured one. Without this the script
	// never changes the step direction, the live value still matches the
	// captured value when the state is restored, and a capture that dropped
	// stepDir passes by accident. The same trap applies to every field below
	// that the run happens to leave where it found it.
	d.HandlePortWrite(portFDCCmdStatus, 0x50)
	return out
}

// primedAfterAFailedRead returns a settled controller carrying the state a
// completed command leaves behind: the sticky error bits of a Record Not Found,
// the Type II status interpretation, a raised interrupt line, and a Data
// Register holding a value nothing has overwritten. The in-flight fixture above
// cannot reach any of it, because a failed command leaves no transfer and a
// running transfer overwrites the Data Register on every read.
func primedAfterAFailedRead(t *testing.T) *Disciple {
	t.Helper()
	d := newPatternDisciple(t)
	d.HandlePortWrite(portControl, 0x01) // drive 0, side 0
	d.HandlePortWrite(portFDCData, 0x2A) // the Seek target the script reads back
	d.HandlePortWrite(portFDCSector, 200)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector: no such sector -> RNF
	if d.statusReg == 0 {
		t.Fatal("the failed read set no sticky status bits, so this fixture proves nothing")
	}
	return d
}

// settledScript observes what the in-flight script cannot. Status is read
// first, because issuing any command clears the sticky error bits; the Seek
// then reveals the Data Register, which is the only way to observe it once a
// transfer is not running.
func settledScript(d *Disciple) []byte {
	var out []byte
	rd := func(p uint16) {
		v, _ := d.HandlePortRead(p)
		out = append(out, v)
	}

	rd(portFDCCmdStatus) // sticky error bits, motor, and the command class
	rd(portFDCTrack)

	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek to whatever the Data Register holds
	rd(portFDCTrack)
	rd(portFDCCmdStatus)
	return out
}

// The same replay property for a settled controller. Between capture and
// restore the run issues a command that succeeds, which clears the sticky
// status bits and moves the Data Register, so a capture that dropped either is
// caught rather than passing because the value never changed.
func TestReplayingFromASettledControllerReproducesTheSameStream(t *testing.T) {
	d := primedAfterAFailedRead(t)

	st := d.SaveState()
	first := settledScript(d)

	// Churn: a command that succeeds clears statusReg, and a fresh Data
	// Register write moves the Seek target somewhere else entirely.
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector: succeeds, clears the error bits
	d.HandlePortWrite(portFDCData, 0x71)
	if d.statusReg != 0 {
		t.Fatal("the churn left the sticky bits set, so the restore below proves nothing")
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if second := settledScript(d); !bytes.Equal(first, second) {
		t.Errorf("the settled controller replayed differently; first diverges at %d",
			firstDiff(first, second))
	}
}

// The motor output is guest-visible as status bit 7, but only a restore can
// ever turn it off: every command switches it on and nothing in the I/O-advanced
// model switches it off again. So the capture has to be taken before any
// command has run, which is the one moment it is false.
func TestTheMotorGoesBackOff(t *testing.T) {
	d := newPatternDisciple(t)
	if d.motorOn {
		t.Fatal("the motor is already on before any command, so this test proves nothing")
	}

	st := d.SaveState()
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore: switches the motor on
	if !d.motorOn {
		t.Fatal("a command did not switch the motor on, so the restore below proves nothing")
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st, _ := d.HandlePortRead(portFDCCmdStatus); st&stMotorOn != 0 {
		t.Errorf("status = %#02x after restore: bit 7 says the motor is still running", st)
	}
}

// The interrupt line is real state — the WD1772 asserts INTRQ on the edge
// connector — but the DISCiPLE brings no port that reports it: reading $1F
// returns the joystick/printer status, which is always $FF. So there is no
// port sequence that would notice it missing, and this test names the field.
func TestTheInterruptLineRoundTrips(t *testing.T) {
	d := newPatternDisciple(t)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore raises INTRQ
	if !d.intrq {
		t.Fatal("the command did not raise INTRQ, so this test proves nothing")
	}

	st := d.SaveState()
	d.HandlePortRead(portFDCCmdStatus) // reading the status register clears it
	if d.intrq {
		t.Fatal("reading status did not clear INTRQ, so the restore below proves nothing")
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !d.intrq {
		t.Error("INTRQ was not restored: a guest polling the line would hang waiting for it")
	}
}

// The property that matters: from a captured state, the controller must hand
// the guest the same bytes it handed it the first time.
func TestReplayingFromCapturedStateReproducesTheSamePortStream(t *testing.T) {
	d := primedForReplay(t)

	st := d.SaveState()
	first := replayScript(d)

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := replayScript(d)

	if !bytes.Equal(first, second) {
		t.Errorf("the DISCiPLE returned a different byte stream on replay from the same captured "+
			"state: some part of the controller is not being captured\nfirst diverges at %d",
			firstDiff(first, second))
	}
}

// The negative that gives the test above its teeth. A snapshot-style restore of
// the addressable registers round-trips those registers, so a weak test would
// pass on it; this proves the port stream depends on a great deal more.
func TestTheAddressableRegistersAloneAreNotEnough(t *testing.T) {
	d := primedForReplay(t)
	st := d.SaveState()
	want := replayScript(d)

	// Rebuild from what the guest can read back, the way a snapshot format
	// does: the control register and the three writable registers.
	other := newPatternDisciple(t)
	other.HandlePortWrite(portControl, 0x02)
	other.HandlePortWrite(portFDCTrack, 5)
	other.HandlePortWrite(portFDCSector, 3)
	if bytes.Equal(want, replayScript(other)) {
		t.Fatal("the register-only rebuild reproduced the stream, so this test is measuring nothing")
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := replayScript(d); !bytes.Equal(want, got) {
		t.Errorf("full state capture failed to reproduce the stream it was taken from; "+
			"first diverges at %d", firstDiff(want, got))
	}
}

// A write transfer is the other half: the bytes already fed in, and the sector
// the command was addressed to, decide what lands on the disk when the last
// byte arrives. Neither is readable through the ports.
func TestReplayingFromCapturedStateFinishesTheSameWrite(t *testing.T) {
	d := newPatternDisciple(t)

	d.HandlePortWrite(portControl, 0x02) // drive 1, side 1
	d.HandlePortWrite(portFDCData, 9)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek to track 9
	d.HandlePortWrite(portFDCSector, 7)
	d.HandlePortWrite(portFDCCmdStatus, 0xA0) // Write Sector
	for i := 0; i < 100; i++ {
		d.HandlePortWrite(portFDCData, byte(0xC0+i)) // a hundred bytes in, mid-transfer
	}

	st := d.SaveState()

	readBack := func() []byte {
		tr := d.disks[1].Track(1, 9)
		if tr == nil {
			t.Fatalf("track 9 side 1 vanished")
		}
		sec := tr.FindSector(7)
		if sec == nil {
			t.Fatalf("sector 7 vanished")
		}
		return append([]byte(nil), sec.Data...)
	}
	feedRest := func() []byte {
		var status []byte
		for i := 100; i < 512; i++ {
			d.HandlePortWrite(portFDCData, byte(0xC0+i))
			v, _ := d.HandlePortRead(portFDCCmdStatus)
			status = append(status, v)
		}
		return status
	}

	firstStatus := feedRest()
	firstSector := readBack()

	// Carry on past the capture and write a different sector on the other
	// drive, side and cylinder, which is what a rewind actually rewinds over.
	// Without this the controller would still be holding the first write's
	// target, and a capture that had not recorded it would pass by accident.
	d.HandlePortWrite(portControl, 0x01) // drive 0, side 0
	d.HandlePortWrite(portFDCData, 2)
	d.HandlePortWrite(portFDCCmdStatus, 0x10)
	d.HandlePortWrite(portFDCSector, 4)
	d.HandlePortWrite(portFDCCmdStatus, 0xA0)
	for i := 0; i < 512; i++ {
		d.HandlePortWrite(portFDCData, 0x5A)
	}

	// Leave a READ in flight rather than a write. Without this the controller
	// is still in write mode when the state is restored, so a capture that had
	// dropped the read/write direction would resume writing anyway and pass.
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80)
	if d.xferWrite {
		t.Fatal("the churn left the controller in write mode, so the restore below proves nothing")
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	secondStatus := feedRest()
	secondSector := readBack()

	if !bytes.Equal(firstStatus, secondStatus) {
		t.Errorf("status stream differed while finishing the write; first diverges at %d",
			firstDiff(firstStatus, secondStatus))
	}
	if !bytes.Equal(firstSector, secondSector) {
		t.Errorf("the replayed write committed different data; first diverges at %d",
			firstDiff(firstSector, secondSector))
	}
}

// The disk is the medium, not machine state: restoring must not roll back what
// has been written to it, any more than a rewind can un-write a real floppy.
func TestRestoreDoesNotRollBackTheMedium(t *testing.T) {
	d := newPatternDisciple(t)
	disk := d.disks[0]

	st := d.SaveState()

	d.HandlePortWrite(portControl, 0x01) // drive 0, side 0
	d.HandlePortWrite(portFDCData, 2)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek to track 2
	d.HandlePortWrite(portFDCSector, 4)
	d.HandlePortWrite(portFDCCmdStatus, 0xA0) // Write Sector
	for i := 0; i < 512; i++ {
		d.HandlePortWrite(portFDCData, 0x5A)
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	sec := disk.Track(0, 2).FindSector(4)
	if sec == nil {
		t.Fatal("sector 4 vanished")
	}
	for i, b := range sec.Data {
		if b != 0x5A {
			t.Fatalf("byte %d of the written sector = %#02x after restore, want %#02x: "+
				"the capture rolled back the medium", i, b, 0x5A)
		}
	}
	if d.disks[0] != disk {
		t.Error("restoring detached the mounted disk")
	}
	if d.diskPaths[0] == "" {
		t.Error("restoring forgot the mounted disk's path")
	}
}

// A capture must not alias the live controller. The registry copies the blob
// too, but a device handing back a view of its own transfer buffer is a bug
// worth catching where it would be introduced.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	d := primedForReplay(t)
	st := d.SaveState()
	want := replayScript(d)

	// Churn the live controller hard: drain what remains, move both heads and
	// overwrite every register.
	for i := 0; i < 600; i++ {
		d.HandlePortRead(portFDCData)
	}
	d.HandlePortWrite(portControl, 0x01)
	d.HandlePortWrite(portFDCTrack, 0xEE)
	d.HandlePortWrite(portFDCSector, 0xEE)
	d.HandlePortWrite(portFDCData, 0xEE)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := replayScript(d); !bytes.Equal(want, got) {
		t.Errorf("the capture changed under the running controller; first diverges at %d",
			firstDiff(want, got))
	}
}

// The 8 KB of interface RAM is machine state: GDOS runs from it, and its
// contents are exactly what a rewind has to put back. It is not reachable
// through the I/O ports at all, so this test names it directly.
func TestInterfaceRAMRoundTrips(t *testing.T) {
	d := newPatternDisciple(t)
	ram := d.GetRAM()
	for i := range ram {
		ram[i] = byte(i*3 + 5)
	}
	want := append([]byte(nil), ram...)

	st := d.SaveState()
	for i := range ram {
		ram[i] = 0x00
	}
	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if got := d.GetRAM(); !bytes.Equal(want, got) {
		t.Errorf("interface RAM differs after restore; first diverges at %d", firstDiff(want, got))
	}
}

// The capture must not alias the live RAM either: a restore that handed back
// the same backing array would let later guest writes rewrite history.
func TestCapturedRAMDoesNotAliasTheLiveRAM(t *testing.T) {
	d := newPatternDisciple(t)
	d.GetRAM()[0x100] = 0x11

	st := d.SaveState()
	d.GetRAM()[0x100] = 0x22

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := d.GetRAM()[0x100]; got != 0x11 {
		t.Errorf("RAM byte = %#02x after restore, want %#02x: the capture aliased the live RAM",
			got, 0x11)
	}
}

// The paging latches decide whether the guest is executing GDOS or the
// Spectrum ROM at the moment of the capture, so they are machine state. Only
// two of them can be reached through a port, and reading that port is itself
// what changes them, so this test names all four directly. The control register
// is here for the same reason: reading $1F returns the joystick/printer status
// (always $FF), never the stored byte.
func TestPagingLatchesAndControlRegisterRoundTrip(t *testing.T) {
	d := newPatternDisciple(t)

	d.HandlePortWrite(portControl, 0x02) // control register = 2, drive 1, side 1
	d.PageIn()                           // romPaged = true, memswap = false
	d.HandlePortWrite(portBoot, 0)       // memswap = true
	d.SetInhibit(false)

	st := d.SaveState()

	// Move every one of them somewhere else.
	d.HandlePortWrite(portControl, 0x01)
	d.HandlePortWrite(portPatch, 0) // romPaged = false
	d.HandlePortRead(portBoot)      // memswap = false
	d.SetInhibit(true)              // inhibited, and romPaged forced false
	d.enabled = false

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if !d.IsROMPaged() {
		t.Error("romPaged was not restored: the guest would resume with GDOS paged out")
	}
	if !d.IsMemSwapped() {
		t.Error("memswap was not restored: ROM and RAM would resume in the wrong halves")
	}
	if d.IsInhibited() {
		t.Error("inhibited was not restored")
	}
	if !d.enabled {
		t.Error("enabled was not restored")
	}
	if d.controlReg != 0x02 {
		t.Errorf("control register = %#02x after restore, want %#02x", d.controlReg, 0x02)
	}
}

// A Write Track (format) transfer is collected into the same buffer as a sector
// write but committed differently, so the flag that says which is in flight has
// to be captured. Without it a restored format would be committed as a sector.
func TestAFormatInFlightSurvivesTheRoundTrip(t *testing.T) {
	d := newPatternDisciple(t)
	d.HandlePortWrite(portControl, 0x01) // drive 0, side 0
	d.HandlePortWrite(portFDCCmdStatus, 0xF0)
	if !d.formatting {
		t.Fatal("Write Track did not start a format, so this test proves nothing")
	}
	for i := 0; i < 50; i++ {
		d.HandlePortWrite(portFDCData, 0x4E)
	}

	st := d.SaveState()
	d.HandlePortWrite(portFDCCmdStatus, 0xD0) // Force Interrupt: abandons the format
	if d.formatting && d.busy {
		t.Fatal("Force Interrupt left the format running, so the restore below proves nothing")
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !d.formatting {
		t.Error("the in-flight format was lost: the rest of the stream would be committed as a sector")
	}
	if d.xferPos != 50 {
		t.Errorf("format collected %d bytes after restore, want 50", d.xferPos)
	}
}

func TestStateIDIsStable(t *testing.T) {
	d := newPatternDisciple(t)
	if got := d.StateID(); got != "disciple" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift",
			got, "disciple")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	d := newPatternDisciple(t)
	if err := d.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
}

// A blob that decodes but describes an impossible controller must be refused
// before anything is applied: a drive index outside 0..1 would index the drive
// array out of bounds the next time the guest touched the disk.
func TestLoadStateRejectsOutOfRangeStateWithoutApplyingIt(t *testing.T) {
	d := primedForReplay(t)
	good := d.SaveState()
	want := replayScript(d)
	if err := d.LoadState(good); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	for _, tc := range []struct {
		name    string
		corrupt func(*discipleState)
	}{
		{"drive out of range", func(s *discipleState) { s.Drive = 2 }},
		{"drive negative", func(s *discipleState) { s.Drive = -1 }},
		{"head out of range", func(s *discipleState) { s.Head = 2 }},
		{"transfer position past the buffer", func(s *discipleState) { s.XferPos = len(s.XferBuf) + 1 }},
		{"transfer length past the buffer", func(s *discipleState) { s.XferLen = len(s.XferBuf) + 1 }},
		{"RAM the wrong size", func(s *discipleState) { s.RAM = []byte{1, 2, 3} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s discipleState
			if err := decodeDiscipleState(good, &s); err != nil {
				t.Fatalf("decoding the good blob: %v", err)
			}
			tc.corrupt(&s)
			if err := d.LoadState(encodeDiscipleState(s)); err == nil {
				t.Fatal("an impossible controller state was accepted")
			}
			if got := replayScript(d); !bytes.Equal(want, got) {
				t.Errorf("the rejected state was partly applied; stream diverges at %d",
					firstDiff(want, got))
			}
			if err := d.LoadState(good); err != nil {
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
