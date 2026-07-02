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

func TestDiscipleInhibitGetterAndPageOut(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatal(err)
	}
	if d.IsInhibited() {
		t.Errorf("default IsInhibited = true")
	}
	// PageIn first to confirm pageOut on SetInhibit(true).
	d.PageIn()
	if !d.IsROMPaged() {
		t.Errorf("after PageIn: IsROMPaged = false")
	}
	d.SetInhibit(true)
	if !d.IsInhibited() {
		t.Errorf("after SetInhibit(true): IsInhibited = false")
	}
	if d.IsROMPaged() {
		t.Errorf("SetInhibit(true) didn't clear romPaged")
	}
}

func TestDiscipleGetRAM(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	if got := len(d.GetRAM()); got != 0x2000 {
		t.Errorf("RAM length = %d, want 8192", got)
	}
}

func TestDiscipleDiskPathOOB(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	if got := d.DiskPath(0); got != "" {
		t.Errorf("DiskPath(0) initial = %q, want empty", got)
	}
	// Out-of-range — guard returns empty string.
	if got := d.DiskPath(-1); got != "" {
		t.Errorf("DiskPath(-1) = %q, want empty (OOB guard)", got)
	}
	if got := d.DiskPath(99); got != "" {
		t.Errorf("DiskPath(99) = %q, want empty (OOB guard)", got)
	}
}

func TestDisciplePageInPopulatesRAM(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)

	d.PageIn()
	if !d.IsROMPaged() {
		t.Errorf("PageIn didn't set romPaged")
	}
	// PageIn sets RAM[0x1DE5] to 0x47 ('G') as the GDOS-initialised
	// marker.
	if got := d.GetRAM()[0x1DE5]; got != 0x47 {
		t.Errorf("GDOS init flag at RAM[0x1DE5] = $%02X, want 0x47", got)
	}
}

func TestDisciplePostFetchHookNoOp(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	// PostFetchHook is documented as a no-op; just confirm it doesn't
	// panic and doesn't change state.
	d.PageIn()
	prev := d.IsROMPaged()
	d.PostFetchHook(0x0000)
	d.PostFetchHook(0xFFFF)
	if d.IsROMPaged() != prev {
		t.Errorf("PostFetchHook changed romPaged state")
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
	// Step-In with the update flag (u=1, bit 4) so the Track Register tracks the
	// head — the WD1772 leaves the register unchanged when u=0.
	d.HandlePortWrite(portFDCCmdStatus, 0x50)
	track, _ := d.HandlePortRead(portFDCTrack)
	if track != 1 {
		t.Errorf("track = %d", track)
	}
}

func TestStepOut(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)
	d, _ := NewDisciple(dir, mem)
	// Seek to track 5 so head and Track Register agree, then Step-Out with the
	// update flag (u=1) so the Track Register follows the head down to 4.
	d.HandlePortWrite(portFDCData, 5)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek track 5
	d.HandlePortWrite(portFDCCmdStatus, 0x70) // Step-Out, u=1
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
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // Seek track 5
	d.HandlePortWrite(portControl, 0x01)      // drive 0, side 0
	d.HandlePortWrite(portFDCSector, 3)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // Read Sector

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

func TestPreFetchHook_Triggers(t *testing.T) {
	dir := createTestROMDir(t)
	mem := newTestMemory(t, dir)

	for _, pc := range []uint16{0x0001, 0x0008, 0x0066, 0x028E} {
		d, _ := NewDisciple(dir, mem)
		d.PreFetchHook(pc)
		if !d.IsROMPaged() {
			t.Errorf("PC=0x%04X should page in", pc)
		}
	}
	// Non-trigger address should NOT page in
	d, _ := NewDisciple(dir, mem)
	d.PreFetchHook(0x1234)
	if d.IsROMPaged() {
		t.Error("PC=0x1234 should not page in")
	}
}

// TestLoadROM_WrongSizeFile — a gdos.rom with the wrong size should be
// skipped by loadROM's size check and the embedded ROM used instead.
func TestLoadROM_WrongSizeFile(t *testing.T) {
	dir := t.TempDir()
	// 48.rom is required by memory.New.
	sysROM := make([]byte, 16384)
	if err := os.WriteFile(filepath.Join(dir, "48.rom"), sysROM, 0644); err != nil {
		t.Fatal(err)
	}
	// gdos.rom present but wrong size → loadROM should fall through.
	if err := os.WriteFile(filepath.Join(dir, "gdos.rom"), make([]byte, 1024), 0644); err != nil {
		t.Fatal(err)
	}
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple: %v", err)
	}
	if len(d.GetROM()) != 0x2000 {
		t.Errorf("ROM size after wrong-size fallback = %d, want 0x2000", len(d.GetROM()))
	}
}

// TestLoadROM_EmbeddedFallback — no filesystem disciple ROM →
// embedded gdos.rom asset is used (size guaranteed 0x2000, so the
// loop accepts it on the first iteration).
func TestLoadROM_EmbeddedFallback(t *testing.T) {
	dir := t.TempDir()
	sysROM := make([]byte, 16384)
	if err := os.WriteFile(filepath.Join(dir, "48.rom"), sysROM, 0644); err != nil {
		t.Fatal(err)
	}
	// Intentionally NO disciple ROM file → falls through to embedded.
	mem := newTestMemory(t, dir)
	d, err := NewDisciple(dir, mem)
	if err != nil {
		t.Fatalf("NewDisciple: %v", err)
	}
	if len(d.GetROM()) != 0x2000 {
		t.Errorf("embedded fallback ROM size = %d, want 0x2000", len(d.GetROM()))
	}
}

// --- Remaining WD1770 command coverage: Read-Address ($C0),
// Force-Interrupt ($D0), Read-Track ($E0) / Write-Track ($F0) RNF stubs,
// Step-In ($40) / Step-Out ($60), plus PostFetchHook (a documented no-op).

func TestDisciple_CmdReadAddress(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x01) // drive 0, side 0
	d.HandlePortWrite(portFDCData, 0)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // seek track 0
	d.HandlePortWrite(portFDCCmdStatus, 0xC0) // Read Address
	// Six ID bytes should be DRQ-readable: C,H,R,N,CRC1,CRC2.
	got := make([]byte, 6)
	for i := range got {
		got[i], _ = d.HandlePortRead(portFDCData)
	}
	// First byte (cylinder) is 0 for track 0; trackReg should be set
	// to the cylinder read off the disk.
	if d.trackReg != got[0] {
		t.Errorf("trackReg=%d, want cylinder byte %d", d.trackReg, got[0])
	}
}

func TestDisciple_ForceInterrupt(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x01)
	d.HandlePortWrite(portFDCSector, 1)
	d.HandlePortWrite(portFDCCmdStatus, 0x80) // start a read → busy
	d.HandlePortWrite(portFDCCmdStatus, 0xD0) // Force Interrupt
	if d.busy {
		t.Error("Force Interrupt should clear busy")
	}
	if d.xferBuf != nil {
		t.Error("Force Interrupt should drop the transfer buffer")
	}
}

func TestDisciple_ReadWriteTrack(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portControl, 0x01) // select drive 0

	// Read Track ($E0) with a disk present streams the raw track bytes.
	d.HandlePortWrite(portFDCCmdStatus, 0xE0)
	if d.statusReg&stRNF != 0 {
		t.Error("Read Track with a disk should not set RNF")
	}
	if !d.drq || len(d.xferBuf) == 0 {
		t.Error("Read Track should set up a transfer of the raw track bytes")
	}

	// Write Track ($F0) begins a format transfer (write + formatting).
	d.HandlePortWrite(portFDCCmdStatus, 0xF0)
	if d.statusReg&stRNF != 0 {
		t.Error("Write Track with a disk should not set RNF")
	}
	if !d.drq || !d.xferWrite || !d.formatting {
		t.Error("Write Track should begin a format write transfer")
	}
}

func TestDisciple_StepInOut(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	d.HandlePortWrite(portFDCData, 0)
	d.HandlePortWrite(portFDCCmdStatus, 0x10) // seek track 0
	// Use the update flag (u=1, bit 4) so the Track Register follows the head;
	// the WD1772 only updates the register when u=1.
	d.HandlePortWrite(portFDCCmdStatus, 0x50) // Step-In u=1 → track 1
	if d.trackReg != 1 {
		t.Errorf("after Step-In trackReg=%d, want 1", d.trackReg)
	}
	d.HandlePortWrite(portFDCCmdStatus, 0x70) // Step-Out u=1 → track 0
	if d.trackReg != 0 {
		t.Errorf("after Step-Out trackReg=%d, want 0", d.trackReg)
	}
	// Step-Out at track 0 clamps (no underflow); head and register stay at 0.
	d.HandlePortWrite(portFDCCmdStatus, 0x70)
	if d.trackReg != 0 {
		t.Errorf("Step-Out at track0 should clamp, got %d", d.trackReg)
	}
}

func TestDisciple_PostFetchHook_NoOp(t *testing.T) {
	d := newTestDiscipleWithDisk(t)
	// Documented no-op; just must not panic or mutate paging.
	before := d.IsROMPaged()
	d.PostFetchHook(0x0008)
	if d.IsROMPaged() != before {
		t.Error("PostFetchHook should not change paging")
	}
}
