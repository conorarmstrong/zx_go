package esxdos

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// setupFileAPI builds a dispatcher wired to a host-directory SD
// card mount populated with the given files, plus a fresh CPU and
// memMap. Returns those plus a helper to invoke an API call as if
// RST 8 had just fired: places the API number at retAddr, the
// return address on the stack, sets PC=0x0008, and dispatches.
//
// The mount root is recorded in mountRoots keyed by *Dispatcher
// so write tests can verify the host filesystem afterwards via
// mountRootFor().
func setupFileAPI(t *testing.T, files map[string][]byte) (*Dispatcher, *z80.CPU, memMap) {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	mount, err := sdcard.NewHostDir(root)
	if err != nil {
		t.Fatal(err)
	}
	d := New()
	d.SetMount(mount)
	mountRoots[d] = root
	// Close any handles the test leaves open BEFORE t.TempDir's RemoveAll
	// runs (LIFO cleanup order) — Windows can't delete a file with an open
	// handle, so a leaked F_OPEN handle would fail the temp-dir cleanup.
	t.Cleanup(func() { d.CloseAll(); delete(mountRoots, d) })

	mem := memMap{}
	cpu := z80.New(memBus{mem}, stubULA{})
	cpu.SP = 0xFF00
	return d, cpu, mem
}

// mountRoots tracks the host root path of the mount each dispatcher
// is wired to. Write-side tests use mountRootFor(d) to peek at the
// host filesystem and verify writes landed correctly. Cleared at
// each test's end via t.Cleanup.
var mountRoots = map[*Dispatcher]string{}

func mountRootFor(d *Dispatcher) string { return mountRoots[d] }

// invokeAPI simulates the post-RST-8 state and dispatches.
func invokeAPI(d *Dispatcher, cpu *z80.CPU, mem memMap, api byte, retAddr uint16) {
	mem[retAddr] = api
	mem[0xFF00] = byte(retAddr & 0xFF)
	mem[0xFF01] = byte(retAddr >> 8)
	cpu.PC = 0x0008
	cpu.SP = 0xFF00
	d.Dispatch(cpu, memBus{mem})
}

// writeASCII stores an ASCII null-terminated string in memMap.
func writeASCII(mem memMap, addr uint16, s string) {
	for i, b := range []byte(s) {
		mem[addr+uint16(i)] = b
	}
	mem[addr+uint16(len(s))] = 0
}

func TestFOpenReadCloseRoundTrip(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"hello.txt": []byte("Hello, Next!"),
	})

	// F_OPEN /hello.txt read-only
	writeASCII(mem, 0x4000, "/hello.txt")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN: CF set, error in A=%#x", cpu.A)
	}
	handle := cpu.A
	if handle == 0 {
		t.Fatalf("F_OPEN: returned handle 0")
	}

	// F_READ 12 bytes into 0x6000
	cpu.A = handle
	cpu.SetBC(12)
	cpu.SetHL(0x6000)
	invokeAPI(d, cpu, mem, F_READ, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_READ: CF set, error in A=%#x", cpu.A)
	}
	if cpu.BC() != 12 {
		t.Errorf("F_READ: bytes-read = %d, want 12", cpu.BC())
	}
	got := make([]byte, 12)
	for i := range got {
		got[i] = mem.Read(0x6000 + uint16(i))
	}
	if string(got) != "Hello, Next!" {
		t.Errorf("F_READ: data = %q, want %q", got, "Hello, Next!")
	}

	// F_CLOSE
	cpu.A = handle
	invokeAPI(d, cpu, mem, F_CLOSE, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Errorf("F_CLOSE: CF set, error in A=%#x", cpu.A)
	}
}

func TestFOpenNotFound(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	writeASCII(mem, 0x4000, "/missing.txt")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	if cpu.F&z80.FLAG_C == 0 {
		t.Errorf("F_OPEN of missing file: expected CF=1")
	}
	if cpu.A != ESX_ENOENT {
		t.Errorf("F_OPEN of missing file: A = %#x, want ESX_ENOENT", cpu.A)
	}
}

func TestFReadOnBadHandle(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	cpu.A = 99 // never registered
	cpu.SetBC(10)
	cpu.SetHL(0x6000)
	invokeAPI(d, cpu, mem, F_READ, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EBADF {
		t.Errorf("F_READ bad handle: CF=%v A=%#x, want CF=1 A=ESX_EBADF (%#x)",
			cpu.F&z80.FLAG_C != 0, cpu.A, ESX_EBADF)
	}
}

func TestFSeekAndReshortRead(t *testing.T) {
	// File with 10 bytes; seek to 4, read 6.
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"data.bin": {0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	})

	writeASCII(mem, 0x4000, "/data.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN failed: A=%#x", cpu.A)
	}
	handle := cpu.A

	// F_SEEK: handle in A, whence=0 (start), offset BCDE=4.
	cpu.A = handle
	cpu.L = 0
	cpu.SetBC(0)
	cpu.SetDE(4)
	invokeAPI(d, cpu, mem, F_SEEK, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_SEEK failed: A=%#x", cpu.A)
	}
	// New position 4: BC=0, DE=4
	if cpu.BC() != 0 || cpu.DE() != 4 {
		t.Errorf("F_SEEK: pos = BC=%d DE=%d, want 0/4", cpu.BC(), cpu.DE())
	}

	// F_READ 6 bytes
	cpu.A = handle
	cpu.SetBC(6)
	cpu.SetHL(0x6000)
	invokeAPI(d, cpu, mem, F_READ, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_READ failed: A=%#x", cpu.A)
	}
	if cpu.BC() != 6 {
		t.Errorf("F_READ: returned %d, want 6", cpu.BC())
	}
	for i := byte(0); i < 6; i++ {
		got := mem.Read(0x6000 + uint16(i))
		want := i + 4
		if got != want {
			t.Errorf("byte[%d] = %d, want %d", i, got, want)
		}
	}
}

func TestFStatBasic(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"info.bin": {0xAA, 0xBB, 0xCC, 0xDD, 0xEE},
	})
	writeASCII(mem, 0x4000, "/info.bin")
	cpu.SetHL(0x4000)
	cpu.SetDE(0x5100) // output buffer
	invokeAPI(d, cpu, mem, F_FSTAT, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_FSTAT: CF=1 A=%#x", cpu.A)
	}
	// Size word at offsets +6/+7 should be 5 (low byte).
	if mem.Read(0x5106) != 5 || mem.Read(0x5107) != 0 {
		t.Errorf("F_FSTAT size word = %d/%d, want 5/0", mem.Read(0x5106), mem.Read(0x5107))
	}
	// Attribute byte at +5 should be 0 (not a dir).
	if mem.Read(0x5105) != 0 {
		t.Errorf("F_FSTAT attr byte = %#x, want 0", mem.Read(0x5105))
	}
}

func TestNoMountReturnsUnknownAPI(t *testing.T) {
	d := New() // no SetMount
	cpu := z80.New(memBus{memMap{}}, stubULA{})
	cpu.SP = 0xFF00
	mem := memMap{}
	mem[0x5000] = F_OPEN
	mem[0xFF00] = 0x00
	mem[0xFF01] = 0x50
	cpu.PC = 0x0008
	d.Dispatch(cpu, memBus{mem})
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EINVAL {
		t.Errorf("no-mount F_OPEN: CF=%v A=%#x, want CF=1 A=ESX_EINVAL (%#x)",
			cpu.F&z80.FLAG_C != 0, cpu.A, ESX_EINVAL)
	}
}

// ---- F_WRITE ----

// TestFWriteCreatesFileWithExactContent verifies F_WRITE end-to-end:
// open with create+write flags, write a payload, close, and check
// the host filesystem has the file with exactly those bytes.
func TestFWriteCreatesFileWithExactContent(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	// Pre-populate the buffer at 0x4000 with the payload.
	payload := []byte("New file content!")
	for i, b := range payload {
		mem[0x4000+uint16(i)] = b
	}
	// Path at 0x5000.
	writeASCII(mem, 0x5000, "/created.txt")

	// F_OPEN flags: write(0x02) + create(0x04) + truncate(0x10) = 0x16.
	cpu.SetHL(0x5000)
	cpu.B = 0x16
	invokeAPI(d, cpu, mem, F_OPEN, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN: CF set, error in A=%#x", cpu.A)
	}
	handle := cpu.A
	if handle == 0 {
		t.Fatalf("F_OPEN: returned handle 0")
	}

	// F_WRITE the payload.
	cpu.A = handle
	cpu.SetHL(0x4000)
	cpu.SetBC(uint16(len(payload)))
	invokeAPI(d, cpu, mem, F_WRITE, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_WRITE: CF set, error in A=%#x", cpu.A)
	}
	if got := cpu.BC(); got != uint16(len(payload)) {
		t.Errorf("F_WRITE: bytes-written = %d, want %d", got, len(payload))
	}

	// F_CLOSE.
	cpu.A = handle
	invokeAPI(d, cpu, mem, F_CLOSE, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_CLOSE: CF set, error in A=%#x", cpu.A)
	}

	// Verify host file exists with exactly the payload bytes.
	root := mountRootFor(d)
	got, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil {
		t.Fatalf("host file read: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("host file content = %q, want %q", got, payload)
	}
}

// TestFWriteOnBadHandleReturnsEBADF verifies the bad-handle error
// path on the write side mirrors the read side.
func TestFWriteOnBadHandleReturnsEBADF(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	cpu.A = 99 // never registered
	cpu.SetHL(0x4000)
	cpu.SetBC(5)
	invokeAPI(d, cpu, mem, F_WRITE, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EBADF {
		t.Errorf("F_WRITE bad handle: CF=%v A=%#x, want CF=1 A=ESX_EBADF (%#x)",
			cpu.F&z80.FLAG_C != 0, cpu.A, ESX_EBADF)
	}
}

// TestFWriteAppendsToExistingFile verifies write-without-truncate
// appends correctly when O_WRONLY is set without O_TRUNC.
func TestFWriteAppendsToExistingFile(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"existing.txt": []byte("HEAD-"),
	})

	// Open in write mode WITHOUT truncate.
	writeASCII(mem, 0x5000, "/existing.txt")
	cpu.SetHL(0x5000)
	cpu.B = 0x02 // write only, no truncate, no create
	invokeAPI(d, cpu, mem, F_OPEN, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN: CF set, A=%#x", cpu.A)
	}
	handle := cpu.A

	// Seek to end (whence=2, offset=0).
	cpu.A = handle
	cpu.L = 2
	cpu.SetBC(0)
	cpu.SetDE(0)
	invokeAPI(d, cpu, mem, F_SEEK, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_SEEK end: CF set, A=%#x", cpu.A)
	}

	// Append "TAIL".
	tail := []byte("TAIL")
	for i, b := range tail {
		mem[0x4000+uint16(i)] = b
	}
	cpu.A = handle
	cpu.SetHL(0x4000)
	cpu.SetBC(uint16(len(tail)))
	invokeAPI(d, cpu, mem, F_WRITE, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_WRITE: CF set, A=%#x", cpu.A)
	}

	cpu.A = handle
	invokeAPI(d, cpu, mem, F_CLOSE, 0x6000)

	root := mountRootFor(d)
	got, err := os.ReadFile(filepath.Join(root, "existing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "HEAD-TAIL" {
		t.Errorf("after append: %q, want %q", got, "HEAD-TAIL")
	}
}

// ---- F_OPENDIR / F_READDIR ----

// TestFOpenDirReadDirEnumeratesEntries opens a directory containing
// known files + a subdirectory, iterates F_READDIR until EOD, and
// asserts every expected name shows up with the right is-dir bit
// and size. Order is filesystem-dependent so we collect into a map.
func TestFOpenDirReadDirEnumeratesEntries(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"games/manic.tap": []byte("manic miner content"),    // 19 bytes
		"games/jsw.tap":   []byte("jet set willy stuff"),    // 19 bytes
		"games/sub/x.bin": []byte("placeholder for subdir"), // creates games/sub/
	})

	writeASCII(mem, 0x5000, "/games")
	cpu.SetHL(0x5000)
	invokeAPI(d, cpu, mem, F_OPENDIR, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPENDIR: CF set, A=%#x", cpu.A)
	}
	dirHandle := cpu.A
	if dirHandle == 0 {
		t.Fatalf("F_OPENDIR: returned handle 0")
	}

	type entry struct {
		isDir bool
		size  uint32
	}
	found := map[string]entry{}

	for i := 0; i < 10; i++ {
		cpu.A = dirHandle
		cpu.SetHL(0x7000)
		invokeAPI(d, cpu, mem, F_READDIR, 0x6000)
		if cpu.F&z80.FLAG_C != 0 {
			if cpu.A != ESX_EWRTYPE {
				t.Fatalf("F_READDIR: unexpected error A=%#x (want EWRTYPE=%#x)", cpu.A, ESX_EWRTYPE)
			}
			break
		}
		// Read null-terminated name then 11-byte stat.
		var name []byte
		off := uint16(0)
		for ; off < 13; off++ {
			b := mem.Read(0x7000 + off)
			if b == 0 {
				break
			}
			name = append(name, b)
		}
		stat := make([]byte, 11)
		for j := uint16(0); j < 11; j++ {
			stat[j] = mem.Read(0x7000 + off + 1 + j)
		}
		isDir := stat[5]&0x10 != 0
		size := uint32(stat[6]) | uint32(stat[7])<<8 | uint32(stat[8])<<16 | uint32(stat[9])<<24
		found[string(name)] = entry{isDir, size}
	}

	if e, ok := found["manic.tap"]; !ok || e.isDir || e.size != 19 {
		t.Errorf("missing or wrong manic.tap entry: %+v (found map: %+v)", e, found)
	}
	if e, ok := found["jsw.tap"]; !ok || e.isDir || e.size != 19 {
		t.Errorf("missing or wrong jsw.tap entry: %+v", e)
	}
	if e, ok := found["sub"]; !ok || !e.isDir {
		t.Errorf("missing or wrong sub entry: %+v (want isDir=true)", e)
	}

	// Cleanup: close the directory handle.
	cpu.A = dirHandle
	invokeAPI(d, cpu, mem, F_CLOSE, 0x6000)
}

// TestFOpenDirMissingReturnsENOENT verifies the negative path.
func TestFOpenDirMissingReturnsENOENT(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	writeASCII(mem, 0x5000, "/nonexistent")
	cpu.SetHL(0x5000)
	invokeAPI(d, cpu, mem, F_OPENDIR, 0x6000)
	if cpu.F&z80.FLAG_C == 0 {
		t.Errorf("F_OPENDIR missing: expected CF=1, got CF=0 A=%#x", cpu.A)
	}
	if cpu.A != ESX_ENOENT {
		t.Errorf("F_OPENDIR missing: A=%#x, want ESX_ENOENT (%#x)", cpu.A, ESX_ENOENT)
	}
}

// TestFReadDirOnFileHandleReturnsEWRTYPE verifies F_READDIR refuses
// a file handle (it must be a directory handle).
func TestFReadDirOnFileHandleReturnsEWRTYPE(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"a.txt": []byte("hi"),
	})

	// Open as a file, not a directory.
	writeASCII(mem, 0x5000, "/a.txt")
	cpu.SetHL(0x5000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN: A=%#x", cpu.A)
	}
	handle := cpu.A

	cpu.A = handle
	cpu.SetHL(0x7000)
	invokeAPI(d, cpu, mem, F_READDIR, 0x6000)
	if cpu.F&z80.FLAG_C == 0 {
		t.Errorf("F_READDIR on file handle: expected CF=1")
	}
	if cpu.A != ESX_EWRTYPE {
		t.Errorf("F_READDIR on file handle: A=%#x, want ESX_EWRTYPE (%#x)", cpu.A, ESX_EWRTYPE)
	}
}

// mapMountError coverage. Three distinct branches:
// ErrNotFound → ENOENT, ErrInvalidPath → EINVAL, default → EIO.
func TestMapMountError_BranchCoverage(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want byte
	}{
		{"ErrNotFound → ENOENT", sdcard.ErrNotFound, ESX_ENOENT},
		{"ErrInvalidPath → EINVAL", sdcard.ErrInvalidPath, ESX_EINVAL},
		{"random error → EIO", errors.New("disk full"), ESX_EIO},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := mapMountError(tc.in)
			var ee *Error
			if !errors.As(err, &ee) {
				t.Fatalf("mapMountError returned non-*Error: %v", err)
			}
			if ee.Code != tc.want {
				t.Errorf("Code = $%02X, want $%02X", ee.Code, tc.want)
			}
		})
	}
}

func TestSetMountNilUnregisters(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{"f.bin": {1}})

	// First confirm F_OPEN works.
	writeASCII(mem, 0x4000, "/f.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN should succeed before unmount")
	}
	openHandle := cpu.A

	// Unmount: that handle should now be closed (we can't read).
	d.SetMount(nil)
	cpu.A = openHandle
	cpu.SetBC(1)
	cpu.SetHL(0x6000)
	invokeAPI(d, cpu, mem, F_READ, 0x5000)
	if cpu.F&z80.FLAG_C == 0 {
		t.Errorf("F_READ after unmount should fail (handle invalidated)")
	}
	// After unmount, F_READ has been deregistered, so the dispatch
	// hits the unknown-API path and returns ESX_EINVAL.
	if cpu.A != ESX_EINVAL {
		t.Errorf("F_READ after unmount: A = %#x, want ESX_EINVAL (%#x)", cpu.A, ESX_EINVAL)
	}
}
