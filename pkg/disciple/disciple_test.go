package disciple

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

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
		t.Fatalf("memory.New: %v", err)
	}
	return mem
}

func buildTestMGT(t *testing.T) (string, []byte) {
	t.Helper()
	const (
		sides   = 2
		cyls    = 80
		sectors = 10
		secSize = 512
	)
	img := make([]byte, sides*cyls*sectors*secSize)
	for logical := 0; logical < sides*cyls; logical++ {
		cyl := logical / sides
		head := logical % sides
		for s := 0; s < sectors; s++ {
			off := (logical*sectors + s) * secSize
			img[off] = byte(cyl)
			img[off+1] = byte(head)
			img[off+2] = byte(s + 1)
			for i := 3; i < secSize; i++ {
				img[i+off] = 0xCC
			}
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mgt")
	if err := os.WriteFile(path, img, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path, img
}

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
		t.Error("should be enabled")
	}
	if d.IsROMPaged() {
		t.Error("should start paged out (page in on reboot)")
	}
	if len(d.GetROM()) != 0x2000 {
		t.Errorf("ROM size = %d", len(d.GetROM()))
	}
}

// --- Port decode ---

func TestPortDecode_AllPorts(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	for _, port := range []uint16{
		portFDCCmdStatus, portFDCTrack, portFDCSector, portFDCData,
		portControl, portBoot, portPatch,
	} {
		if _, handled := d.HandlePortRead(port); !handled {
			t.Errorf("port 0x%02X should be handled", port)
		}
	}
}

func TestPortDecode_Inhibited(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.SetInhibit(true)
	if _, handled := d.HandlePortRead(portFDCCmdStatus); handled {
		t.Error("inhibited: should not handle")
	}
}

// --- Registers ---

func TestTrackRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.HandlePortWrite(portFDCTrack, 0x2A)
	val, _ := d.HandlePortRead(portFDCTrack)
	if val != 0x2A {
		t.Errorf("got 0x%02X", val)
	}
}

func TestSectorRegister(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.HandlePortWrite(portFDCSector, 0x05)
	val, _ := d.HandlePortRead(portFDCSector)
	if val != 0x05 {
		t.Errorf("got 0x%02X", val)
	}
}

// --- Type I ---

func TestRestore(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.HandlePortWrite(portFDCTrack, 0x20)
	d.HandlePortWrite(portFDCCmdStatus, 0x00)
	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 0 {
		t.Errorf("track = %d", track)
	}
}

func TestSeek(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.HandlePortWrite(portFDCData, 0x15)
	d.HandlePortWrite(portFDCCmdStatus, 0x10)
	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 0x15 {
		t.Errorf("track = 0x%02X", track)
	}
}

func TestStepIn(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore
	d.HandlePortWrite(portFDCCmdStatus, 0x40)
	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 1 {
		t.Errorf("track = %d", track)
	}
}

func TestStepOut(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.HandlePortWrite(portFDCTrack, 5)
	d.HandlePortWrite(portFDCCmdStatus, 0x60)
	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 4 {
		t.Errorf("track = %d", track)
	}
}

// --- Paging via port 0xBB ---

func TestPatchPage(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if d.IsROMPaged() {
		t.Error("should start unpaged")
	}
	// Read port 0xBB → page in
	d.HandlePortRead(portPatch)
	if !d.IsROMPaged() {
		t.Error("should be paged in after port 0xBB read")
	}
	// Write port 0xBB → page out
	d.HandlePortWrite(portPatch, 0)
	if d.IsROMPaged() {
		t.Error("should be paged out after port 0xBB write")
	}
}

// --- Boot memswap via port 0x7B ---

func TestBootMemswap(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	if d.IsMemSwapped() {
		t.Error("should start normal")
	}
	d.HandlePortWrite(portBoot, 0) // write → swapped
	if !d.IsMemSwapped() {
		t.Error("should be swapped after write")
	}
	d.HandlePortRead(portBoot) // read → normal
	if d.IsMemSwapped() {
		t.Error("should be normal after read")
	}
}

// --- Control register ---

func TestControlRegister_DriveSelect(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portControl, 0x01) // bit 0=1 → drive 0
	if d.drive != 0 {
		t.Errorf("drive = %d, want 0", d.drive)
	}
	d.HandlePortWrite(portControl, 0x00) // bit 0=0 → drive 1
	if d.drive != 1 {
		t.Errorf("drive = %d, want 1", d.drive)
	}
}

func TestControlRegister_SideSelect(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.HandlePortWrite(portControl, 0x02) // bit 1=1 → side 1
	if d.head != 1 {
		t.Errorf("head = %d, want 1", d.head)
	}
	d.HandlePortWrite(portControl, 0x00) // bit 1=0 → side 0
	if d.head != 0 {
		t.Errorf("head = %d, want 0", d.head)
	}
}

// --- Read Sector ---

func TestReadSector(t *testing.T) {
	d := newTestDiscipleWithDisk(t)

	d.HandlePortWrite(portFDCData, 5)
	d.HandlePortWrite(portFDCCmdStatus, 0x10)     // Seek track 5
	d.HandlePortWrite(portControl, 0x01)           // drive 0, side 0
	d.HandlePortWrite(portFDCSector, 3)
	d.HandlePortWrite(portFDCCmdStatus, 0x80)      // Read Sector

	buf := make([]byte, 512)
	for i := range buf {
		buf[i], _ = d.HandlePortRead(portFDCData)
	}

	if buf[0] != 5 {
		t.Errorf("[0] = %d, want 5", buf[0])
	}
	if buf[1] != 0 {
		t.Errorf("[1] = %d, want 0", buf[1])
	}
	if buf[2] != 3 {
		t.Errorf("[2] = %d, want 3", buf[2])
	}
	if buf[3] != 0xCC {
		t.Errorf("[3] = 0x%02X, want 0xCC", buf[3])
	}
}

func TestReadSector_NoDisk(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80)
	st, _ := d.HandlePortRead(portFDCCmdStatus)
	if st&stRNF == 0 {
		t.Error("RNF should be set")
	}
}

// --- Write Sector ---

func TestWriteSector(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x01)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0xA0) // Write Sector

	for i := 0; i < 512; i++ {
		d.HandlePortWrite(portFDCData, 0xAA)
	}

	// Read back
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80)
	buf := make([]byte, 512)
	for i := range buf {
		buf[i], _ = d.HandlePortRead(portFDCData)
	}
	for i, b := range buf {
		if b != 0xAA {
			t.Errorf("[%d] = 0x%02X, want 0xAA", i, b)
			break
		}
	}
}

// --- Read Address ---

func TestReadAddress(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x01)
	d.HandlePortWrite(portFDCCmdStatus, 0x00) // Restore
	d.HandlePortWrite(portFDCCmdStatus, 0xC0) // Read Address

	id := make([]byte, 6)
	for i := range id {
		id[i], _ = d.HandlePortRead(portFDCData)
	}
	if id[0] != 0 {
		t.Errorf("C = %d", id[0])
	}
	if id[2] != 1 {
		t.Errorf("R = %d, want 1", id[2])
	}
}

// --- Side 1 ---

func TestReadSector_Side1(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x03) // drive 0 + side 1
	d.HandlePortWrite(portFDCData, 10)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek 10
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80)

	buf := make([]byte, 512)
	for i := range buf {
		buf[i], _ = d.HandlePortRead(portFDCData)
	}
	if buf[0] != 10 {
		t.Errorf("[0] = %d, want 10", buf[0])
	}
	if buf[1] != 1 {
		t.Errorf("[1] = %d, want 1 (side 1)", buf[1])
	}
}

// --- LoadDisk ---

func TestLoadDisk_InvalidDrive(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	if err := d.LoadDisk(-1, "x"); err == nil {
		t.Error("drive -1 should fail")
	}
	if err := d.LoadDisk(2, "x"); err == nil {
		t.Error("drive 2 should fail")
	}
}

func TestLoadDisk_FileNotFound(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	if err := d.LoadDisk(0, "/nonexistent"); err == nil {
		t.Error("should fail")
	}
}

func TestEjectDisk(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.EjectDisk(0)
	if d.HasDisk(0) {
		t.Error("should be empty")
	}
}

// --- NMI auto-page ---

func TestPreFetchHook_NMI(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.PreFetchHook(0x0066)
	if !d.IsROMPaged() {
		t.Error("should page in on NMI vector")
	}
}

func TestPreFetchHook_NoPageOnRST8(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.PreFetchHook(0x0008)
	if d.IsROMPaged() {
		t.Error("RST 8 should NOT auto-page (GDOS uses RAM hooks instead)")
	}
}
