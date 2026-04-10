package disciple

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// createTestROMDir builds a temporary directory with the minimal ROM files
// required by memory.New and the Disciple interface.
func createTestROMDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sysROM := make([]byte, 16384)
	for i := range sysROM {
		sysROM[i] = byte(i & 0xFF)
	}
	if err := os.WriteFile(filepath.Join(dir, "48.rom"), sysROM, 0644); err != nil {
		t.Fatalf("write 48.rom: %v", err)
	}
	gdosROM := make([]byte, 8192)
	for i := range gdosROM {
		gdosROM[i] = byte((i + 0x42) & 0xFF)
	}
	if err := os.WriteFile(filepath.Join(dir, "gdos.rom"), gdosROM, 0644); err != nil {
		t.Fatalf("write gdos.rom: %v", err)
	}
	return dir
}

func newTestMemory(t *testing.T, dir string) *memory.Memory {
	t.Helper()
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New failed: %v", err)
	}
	return mem
}

// buildTestMGT creates a standard 2-side, 80-cylinder, 10-sector, 512-byte
// MGT disk image where each sector's first three bytes are (cylinder, head,
// sector number) followed by 0xCC fill.
func buildTestMGT(t *testing.T) (string, []byte) {
	t.Helper()
	const (
		sides   = 2
		cyls    = 80
		sectors = 10
		secSize = 512
	)
	img := make([]byte, sides*cyls*sectors*secSize)
	// MGT is side-alternating: cyl0/head0, cyl0/head1, cyl1/head0, ...
	for logical := 0; logical < sides*cyls; logical++ {
		cyl := logical / sides
		head := logical % sides
		for s := 0; s < sectors; s++ {
			off := (logical*sectors + s) * secSize
			img[off] = byte(cyl)
			img[off+1] = byte(head)
			img[off+2] = byte(s + 1) // sector IDs are 1-based
			for i := 3; i < secSize; i++ {
				img[i+off] = 0xCC
			}
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mgt")
	if err := os.WriteFile(path, img, 0644); err != nil {
		t.Fatalf("write test.mgt: %v", err)
	}
	return path, img
}

// newTestDiscipleWithDisk creates a DISCiPLE with a test MGT disk in drive 0.
func newTestDiscipleWithDisk(t *testing.T) *Disciple {
	t.Helper()
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple: %v", err)
	}
	path, _ := buildTestMGT(t)
	if err := d.LoadDisk(0, path); err != nil {
		t.Fatalf("LoadDisk: %v", err)
	}
	return d
}

// --- Creation ---

func TestNewDisciple_WithRealROM(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple: %v", err)
	}
	if !d.IsEnabled() {
		t.Error("newly created Disciple should be enabled")
	}
	if d.IsInhibited() {
		t.Error("should not be inhibited")
	}
	if d.IsROMPaged() {
		t.Error("should not have ROM paged in")
	}
	if len(d.GetROM()) != 0x2000 {
		t.Errorf("ROM size = %d, want 8192", len(d.GetROM()))
	}
	if len(d.GetRAM()) != 0x2000 {
		t.Errorf("RAM size = %d, want 8192", len(d.GetRAM()))
	}
	if d.GetROM()[0] != byte(0x42&0xFF) {
		t.Errorf("ROM[0] = 0x%02X, want 0x42", d.GetROM()[0])
	}
}

func TestNewDisciple_FallbackROM(t *testing.T) {
	dir := t.TempDir()
	sysROM := make([]byte, 16384)
	if err := os.WriteFile(filepath.Join(dir, "48.rom"), sysROM, 0644); err != nil {
		t.Fatal(err)
	}
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple: %v", err)
	}
	if len(d.GetROM()) != 0x2000 {
		t.Errorf("ROM size = %d, want 8192", len(d.GetROM()))
	}
	if d.GetROM()[0] != 0xF3 {
		t.Errorf("ROM[0] = 0x%02X, want 0xF3 (DI)", d.GetROM()[0])
	}
}

// --- Port decode ---

func TestPortDecode_FDCPorts(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	for _, port := range []uint16{portFDCCmdStatus, portFDCTrack, portFDCSector, portFDCData, portControl} {
		if _, handled := d.HandlePortRead(port); !handled {
			t.Errorf("port 0x%02X should be handled", port)
		}
	}
}

func TestPortDecode_UnknownPort(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if _, handled := d.HandlePortRead(0xFF); handled {
		t.Error("port 0xFF should not be handled")
	}
}

func TestPortDecode_Inhibited(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.SetInhibit(true)

	if _, handled := d.HandlePortRead(portFDCCmdStatus); handled {
		t.Error("should not handle ports when inhibited")
	}
	if handled := d.HandlePortWrite(portFDCTrack, 0x10); handled {
		t.Error("should not handle writes when inhibited")
	}
}

// --- Register read/write ---

func TestTrackRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCTrack, 0x2A)
	val, _ := d.HandlePortRead(portFDCTrack)
	if val != 0x2A {
		t.Errorf("track = 0x%02X, want 0x2A", val)
	}
}

func TestSectorRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCSector, 0x05)
	val, _ := d.HandlePortRead(portFDCSector)
	if val != 0x05 {
		t.Errorf("sector = 0x%02X, want 0x05", val)
	}
}

// --- Type I commands ---

func TestRestore(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCTrack, 0x20)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore

	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 0 {
		t.Errorf("track after Restore = %d, want 0", track)
	}
}

func TestSeek(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCData, 0x15)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek

	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 0x15 {
		t.Errorf("track after Seek = 0x%02X, want 0x15", track)
	}
}

func TestStepIn(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore (track=0)
	d.HandlePortWrite(portFDCCmdStatus, 0x40) // Step-In

	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 1 {
		t.Errorf("track after StepIn from 0 = %d, want 1", track)
	}
}

func TestStepIn_MaxTrack(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCTrack, 79)
	d.HandlePortWrite(portFDCCmdStatus, 0x40) // Step-In

	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 79 {
		t.Errorf("track = %d, want 79 (clamped)", track)
	}
}

func TestStepOut(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCTrack, 5)
	d.HandlePortWrite(portFDCCmdStatus, 0x60) // Step-Out

	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 4 {
		t.Errorf("track = %d, want 4", track)
	}
}

func TestStepOut_MinTrack(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore
	d.HandlePortWrite(portFDCCmdStatus, 0x60) // Step-Out

	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 0 {
		t.Errorf("track = %d, want 0 (clamped)", track)
	}
}

func TestForceInterrupt(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCCmdStatus, 0xD0)
	// After ForceInt, INTRQ should be set
	ctrl, _ := d.HandlePortRead(portControl)
	if ctrl&0x40 == 0 {
		t.Error("INTRQ (bit 6 of control read) should be set after Force Interrupt")
	}
}

// --- Type I status bits ---

func TestStatus_NotReady_NoDisk(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stNotReady == 0 {
		t.Error("Not Ready (bit 7) should be set without a disk")
	}
}

func TestStatus_Track0(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore

	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stTrack0 == 0 {
		t.Error("Track 0 (bit 2) should be set when at track 0")
	}
}

func TestStatus_HeadLoaded(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Type I command

	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stHeadLoaded == 0 {
		t.Error("Head Loaded (bit 5) should be set with disk present")
	}
}

// --- Read Sector ---

func TestReadSector(t *testing.T) {
	d := newTestDiscipleWithDisk(t)

	// Seek to track 5
	d.HandlePortWrite(portFDCData, 5)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek

	// Select side 0 via control register
	d.HandlePortWrite(portControl, 0x01) // drive 1, side 0

	// Read sector 3
	d.HandlePortWrite(portFDCSector, 3)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector

	// Status should show DRQ
	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stDRQ == 0 {
		t.Fatal("DRQ should be set during read")
	}
	if st&stBusy == 0 {
		t.Fatal("Busy should be set during read")
	}

	// Read the full sector (512 bytes)
	buf := make([]byte, 512)
	for i := range buf {
		val, _ := d.HandlePortRead(portFDCData)
		buf[i] = val
	}

	// The test MGT has: sector[0]=cylinder, [1]=head, [2]=sectorID
	if buf[0] != 5 {
		t.Errorf("sector data[0] = %d, want 5 (cylinder)", buf[0])
	}
	if buf[1] != 0 {
		t.Errorf("sector data[1] = %d, want 0 (head)", buf[1])
	}
	if buf[2] != 3 {
		t.Errorf("sector data[2] = %d, want 3 (sector R)", buf[2])
	}
	if buf[3] != 0xCC {
		t.Errorf("sector data[3] = 0x%02X, want 0xCC (fill)", buf[3])
	}

	// After full sector, DRQ and Busy should be clear
	st, _ = d.HandlePortRead(portFDCCmdStatus)
	if st&stDRQ != 0 {
		t.Error("DRQ should be clear after full sector read")
	}
	if st&stBusy != 0 {
		t.Error("Busy should be clear after full sector read")
	}
}

func TestReadSector_NoDisk(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector

	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stRNF == 0 {
		t.Error("RNF should be set when no disk loaded")
	}
}

func TestReadSector_BadSector(t *testing.T) {
	d := newTestDiscipleWithDisk(t)

	d.HandlePortWrite(portFDCSector, 99) // non-existent
	d.HandlePortWrite(portFDCCmdStatus, 0x80)

	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stRNF == 0 {
		t.Error("RNF should be set for non-existent sector")
	}
}

// --- Write Sector ---

func TestWriteSector(t *testing.T) {
	d := newTestDiscipleWithDisk(t)

	// Select side 0, drive 1
	d.HandlePortWrite(portControl, 0x01)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore (track 0)
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0xA0) // Write Sector

	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stDRQ == 0 {
		t.Fatal("DRQ should be set for write")
	}

	// Write 512 bytes of 0xAA
	for i := 0; i < 512; i++ {
		d.HandlePortWrite(portFDCData, 0xAA)
	}

	// Verify transfer completed
	st, _ = d.HandlePortRead(portFDCCmdStatus)
	if st&stBusy != 0 {
		t.Error("Busy should be clear after write completes")
	}

	// Read back the same sector to verify the write
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector

	buf := make([]byte, 512)
	for i := range buf {
		b, _ := d.HandlePortRead(portFDCData)
		buf[i] = b
	}
	for i, b := range buf {
		if b != 0xAA {
			t.Errorf("readback[%d] = 0x%02X, want 0xAA", i, b)
			break
		}
	}
}

// --- Read Address ---

func TestReadAddress(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x01) // drive 1, side 0
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore

	d.HandlePortWrite(portFDCCmdStatus, 0xC0) // Read Address

	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stDRQ == 0 {
		t.Fatal("DRQ should be set for Read Address")
	}

	// Read 6 bytes: C, H, R, N, CRC1, CRC2
	id := make([]byte, 6)
	for i := range id {
		b, _ := d.HandlePortRead(portFDCData)
		id[i] = b
	}
	if id[0] != 0 {
		t.Errorf("C = %d, want 0 (track 0)", id[0])
	}
	if id[1] != 0 {
		t.Errorf("H = %d, want 0 (head 0)", id[1])
	}
	// R should be the first sector on the track (1)
	if id[2] != 1 {
		t.Errorf("R = %d, want 1 (first sector)", id[2])
	}
}

// --- Control register ---

func TestControlRegister_DriveSelect(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portControl, 0x02) // drive 2
	if d.drive != 1 {
		t.Errorf("drive = %d, want 1", d.drive)
	}
	d.HandlePortWrite(portControl, 0x01) // drive 1
	if d.drive != 0 {
		t.Errorf("drive = %d, want 0", d.drive)
	}
}

func TestControlRegister_SideSelect(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portControl, 0x04) // side 1
	if d.head != 1 {
		t.Errorf("head = %d, want 1", d.head)
	}
	d.HandlePortWrite(portControl, 0x00) // side 0
	if d.head != 0 {
		t.Errorf("head = %d, want 0", d.head)
	}
}

func TestControlRegister_PageIn(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if d.IsROMPaged() {
		t.Error("ROM should not be paged initially")
	}
	d.HandlePortWrite(portControl, 0x10) // page in
	if !d.IsROMPaged() {
		t.Error("ROM should be paged in")
	}
	d.HandlePortWrite(portControl, 0x00) // page out
	if d.IsROMPaged() {
		t.Error("ROM should be paged out")
	}
}

func TestControlRead_DRQ(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x01) // drive 1, side 0

	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector

	ctrl, _ := d.HandlePortRead(portControl)
	if ctrl&0x80 == 0 {
		t.Error("DRQ (bit 7) should be set in control read during transfer")
	}
}

// --- LoadDisk ---

func TestLoadDisk_InvalidDrive(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if err := d.LoadDisk(-1, "test.mgt"); err == nil {
		t.Error("drive -1 should fail")
	}
	if err := d.LoadDisk(2, "test.mgt"); err == nil {
		t.Error("drive 2 should fail")
	}
}

func TestLoadDisk_FileNotFound(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if err := d.LoadDisk(0, "/nonexistent/disk.mgt"); err == nil {
		t.Error("missing file should fail")
	}
}

func TestLoadDisk_ValidMGT(t *testing.T) {
	d := newTestDiscipleWithDisk(t)

	if !d.HasDisk(0) {
		t.Error("drive 0 should have a disk")
	}
	if d.HasDisk(1) {
		t.Error("drive 1 should not have a disk")
	}
}

func TestEjectDisk(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.EjectDisk(0)
	if d.HasDisk(0) {
		t.Error("drive 0 should be empty after eject")
	}
}

// --- SetInhibit ---

func TestSetInhibit(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portControl, 0x10) // page in
	if !d.IsROMPaged() {
		t.Fatal("ROM should be paged in")
	}

	d.SetInhibit(true)
	if !d.IsInhibited() {
		t.Error("should be inhibited")
	}
	if d.IsROMPaged() {
		t.Error("ROM should be paged out on inhibit")
	}
	if d.IsEnabled() {
		t.Error("should not be enabled when inhibited")
	}

	d.SetInhibit(false)
	if d.IsInhibited() {
		t.Error("should not be inhibited")
	}
	if !d.IsEnabled() {
		t.Error("should be enabled after un-inhibit")
	}
}

// --- GetROM / GetRAM ---

func TestGetRAM_IsWritable(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	ram := d.GetRAM()
	ram[0] = 0xAB
	if d.GetRAM()[0] != 0xAB {
		t.Error("GetRAM should return the same backing slice")
	}
}

// --- Side 1 read ---

func TestReadSector_Side1(t *testing.T) {
	d := newTestDiscipleWithDisk(t)

	// Select side 1
	d.HandlePortWrite(portControl, 0x05) // drive 1 + side 1
	d.HandlePortWrite(portFDCData, 10)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek to track 10

	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector

	buf := make([]byte, 512)
	for i := range buf {
		b, _ := d.HandlePortRead(portFDCData)
		buf[i] = b
	}

	if buf[0] != 10 {
		t.Errorf("data[0] = %d, want 10 (cylinder)", buf[0])
	}
	if buf[1] != 1 {
		t.Errorf("data[1] = %d, want 1 (head/side 1)", buf[1])
	}
	if buf[2] != 1 {
		t.Errorf("data[2] = %d, want 1 (sector R)", buf[2])
	}
}
