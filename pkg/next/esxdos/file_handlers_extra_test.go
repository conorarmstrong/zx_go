package esxdos

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Covers residual esxDOS file-handler branches: F_READ multi-chunk +
// directory-handle refusal, F_SEEK FROM_CUR / negative offset /
// bad-handle, F_FSTAT on a missing file, and F_READDIR name
// truncation past 12 chars.

// TestFReadMultiChunk reads a file larger than fReadChunkSize (4096)
// so the chunk loop iterates more than once and the final short read
// terminates it. Verifies every byte lands and BC holds the total.
func TestFReadMultiChunk(t *testing.T) {
	const size = fReadChunkSize + 904 // 5000 bytes → two chunks
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i * 7) // deterministic non-trivial pattern
	}
	d, cpu, mem := setupFileAPI(t, map[string][]byte{"big.bin": content})

	writeASCII(mem, 0x4000, "/big.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN: A=%#x", cpu.A)
	}
	handle := cpu.A

	cpu.A = handle
	cpu.SetBC(uint16(size)) // 5000 fits in 16 bits
	cpu.SetHL(0x8000)
	invokeAPI(d, cpu, mem, F_READ, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_READ: A=%#x", cpu.A)
	}
	if cpu.BC() != uint16(size) {
		t.Fatalf("F_READ multi-chunk: BC=%d, want %d", cpu.BC(), size)
	}
	for i := 0; i < size; i++ {
		if got := mem.Read(0x8000 + uint16(i)); got != content[i] {
			t.Fatalf("byte[%d]=%#x, want %#x", i, got, content[i])
		}
	}
}

// TestFReadOnDirectoryHandleEWRTYPE: a directory handle opened via
// F_OPENDIR must be refused by F_READ.
func TestFReadOnDirectoryHandleEWRTYPE(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{"dir/a.txt": []byte("x")})
	writeASCII(mem, 0x4000, "/dir")
	cpu.SetHL(0x4000)
	invokeAPI(d, cpu, mem, F_OPENDIR, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPENDIR: A=%#x", cpu.A)
	}
	dh := cpu.A
	cpu.A = dh
	cpu.SetBC(4)
	cpu.SetHL(0x6000)
	invokeAPI(d, cpu, mem, F_READ, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EWRTYPE {
		t.Errorf("F_READ on dir handle: CF=%v A=%#x, want CF=1 A=EWRTYPE(%#x)",
			cpu.F&z80.FLAG_C != 0, cpu.A, ESX_EWRTYPE)
	}
}

// TestFSeekFromCurrent exercises whence=1 (relative to current).
func TestFSeekFromCurrent(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"d.bin": {0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	})
	writeASCII(mem, 0x4000, "/d.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	handle := cpu.A

	// Seek start → 4.
	cpu.A = handle
	cpu.L = 0
	cpu.SetBC(0)
	cpu.SetDE(4)
	invokeAPI(d, cpu, mem, F_SEEK, 0x5000)

	// Seek current +3 → 7.
	cpu.A = handle
	cpu.L = 1
	cpu.SetBC(0)
	cpu.SetDE(3)
	invokeAPI(d, cpu, mem, F_SEEK, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_SEEK cur: A=%#x", cpu.A)
	}
	if cpu.BC() != 0 || cpu.DE() != 7 {
		t.Errorf("F_SEEK cur: pos BC=%d DE=%d, want 0/7", cpu.BC(), cpu.DE())
	}
}

// TestFSeekNegativeFromEnd uses a negative (sign-extended 32-bit)
// offset with whence=2 (end).
func TestFSeekNegativeFromEnd(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"d.bin": {0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, // len 10
	})
	writeASCII(mem, 0x4000, "/d.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	handle := cpu.A

	// offset = -3 as signed 32-bit = 0xFFFFFFFD → BC=0xFFFF DE=0xFFFD.
	cpu.A = handle
	cpu.L = 2
	cpu.SetBC(0xFFFF)
	cpu.SetDE(0xFFFD)
	invokeAPI(d, cpu, mem, F_SEEK, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_SEEK -3 from end: A=%#x", cpu.A)
	}
	// 10 + (-3) = 7.
	if cpu.BC() != 0 || cpu.DE() != 7 {
		t.Errorf("F_SEEK -3 from end: pos BC=%d DE=%d, want 0/7", cpu.BC(), cpu.DE())
	}
}

// TestFSeekBadHandle: seeking an unregistered handle → EBADF.
func TestFSeekBadHandle(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	cpu.A = 88
	cpu.L = 0
	cpu.SetBC(0)
	cpu.SetDE(0)
	invokeAPI(d, cpu, mem, F_SEEK, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EBADF {
		t.Errorf("F_SEEK bad handle: CF=%v A=%#x, want CF=1 A=EBADF(%#x)",
			cpu.F&z80.FLAG_C != 0, cpu.A, ESX_EBADF)
	}
}

// TestFStatMissingFile: F_FSTAT on a path that doesn't exist → ENOENT.
func TestFStatMissingFile(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	writeASCII(mem, 0x4000, "/nope.bin")
	cpu.SetHL(0x4000)
	cpu.SetDE(0x5100)
	invokeAPI(d, cpu, mem, F_FSTAT, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_ENOENT {
		t.Errorf("F_FSTAT missing: CF=%v A=%#x, want CF=1 A=ENOENT(%#x)",
			cpu.F&z80.FLAG_C != 0, cpu.A, ESX_ENOENT)
	}
}

// TestFReadDirTruncatesLongName: a filename longer than 12 chars is
// truncated to 12 bytes in the entry buffer (NULL-terminated after).
func TestFReadDirTruncatesLongName(t *testing.T) {
	const longName = "abcdefghijklmnop.bin" // 20 chars > 12
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"dir/" + longName: []byte("z"),
	})
	writeASCII(mem, 0x4000, "/dir")
	cpu.SetHL(0x4000)
	invokeAPI(d, cpu, mem, F_OPENDIR, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPENDIR: A=%#x", cpu.A)
	}
	dh := cpu.A

	cpu.A = dh
	cpu.SetHL(0x7000)
	invokeAPI(d, cpu, mem, F_READDIR, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_READDIR: A=%#x", cpu.A)
	}
	// Name must be exactly 12 bytes then NUL.
	var name []byte
	for off := uint16(0); off < 13; off++ {
		b := mem.Read(0x7000 + off)
		if b == 0 {
			break
		}
		name = append(name, b)
	}
	if len(name) != 12 || string(name) != longName[:12] {
		t.Errorf("F_READDIR long name = %q (len %d), want %q (len 12)",
			name, len(name), longName[:12])
	}
}
