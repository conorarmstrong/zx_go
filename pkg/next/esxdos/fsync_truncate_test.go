package esxdos

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// F_FGETPOS returns the current cursor in BCDE after a seek.
func TestFGetPos(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"d.bin": {0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	})
	writeASCII(mem, 0x4000, "/d.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x01
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	handle := cpu.A

	// Seek to 7.
	cpu.A = handle
	cpu.L = 0
	cpu.SetBC(0)
	cpu.SetDE(7)
	invokeAPI(d, cpu, mem, F_SEEK, 0x5000)

	// F_FGETPOS should report 7.
	cpu.A = handle
	cpu.SetBC(0xDEAD)
	cpu.SetDE(0xBEEF)
	invokeAPI(d, cpu, mem, F_FGETPOS, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_FGETPOS: CF set, A=%#x", cpu.A)
	}
	if cpu.BC() != 0 || cpu.DE() != 7 {
		t.Errorf("F_FGETPOS: BC=%d DE=%d, want 0/7", cpu.BC(), cpu.DE())
	}
}

func TestFGetPosBadHandle(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	cpu.A = 77
	invokeAPI(d, cpu, mem, F_FGETPOS, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EBADF {
		t.Errorf("F_FGETPOS bad handle: CF=%v A=%#x, want CF=1 A=EBADF",
			cpu.F&z80.FLAG_C != 0, cpu.A)
	}
}

// F_FTRUNCATE shortens a file to the BCDE size; host file reflects it.
func TestFTruncateShortens(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{
		"big.bin": {0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, // 10 bytes
	})
	writeASCII(mem, 0x4000, "/big.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x02 // write mode (truncate needs writable handle)
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_OPEN: A=%#x", cpu.A)
	}
	handle := cpu.A

	// Truncate to 4 bytes.
	cpu.A = handle
	cpu.SetBC(0)
	cpu.SetDE(4)
	invokeAPI(d, cpu, mem, F_FTRUNCATE, 0x5000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_FTRUNCATE: CF set, A=%#x", cpu.A)
	}
	cpu.A = handle
	invokeAPI(d, cpu, mem, F_CLOSE, 0x5000)

	got, err := os.ReadFile(filepath.Join(mountRootFor(d), "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("after F_FTRUNCATE(4): host file size = %d, want 4", len(got))
	}
}

func TestFTruncateBadHandle(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	cpu.A = 66
	cpu.SetBC(0)
	cpu.SetDE(0)
	invokeAPI(d, cpu, mem, F_FTRUNCATE, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EBADF {
		t.Errorf("F_FTRUNCATE bad handle: CF=%v A=%#x, want CF=1 A=EBADF",
			cpu.F&z80.FLAG_C != 0, cpu.A)
	}
}

// F_FTRUNCATE on a read-only handle fails (Truncate returns error).
func TestFTruncateReadOnlyFails(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, map[string][]byte{"ro.bin": {1, 2, 3}})
	writeASCII(mem, 0x4000, "/ro.bin")
	cpu.SetHL(0x4000)
	cpu.B = 0x01 // read-only
	invokeAPI(d, cpu, mem, F_OPEN, 0x5000)
	handle := cpu.A
	cpu.A = handle
	cpu.SetBC(0)
	cpu.SetDE(1)
	invokeAPI(d, cpu, mem, F_FTRUNCATE, 0x5000)
	if cpu.F&z80.FLAG_C == 0 {
		t.Error("F_FTRUNCATE on read-only handle should fail (CF=1)")
	}
}

// F_SYNC on a writable handle succeeds and the written bytes are on disk.
func TestFSync(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	writeASCII(mem, 0x5000, "/synced.txt")
	cpu.SetHL(0x5000)
	cpu.B = 0x16 // write+create+truncate
	invokeAPI(d, cpu, mem, F_OPEN, 0x6000)
	handle := cpu.A

	payload := []byte("DURABLE")
	for i, b := range payload {
		mem[0x4000+uint16(i)] = b
	}
	cpu.A = handle
	cpu.SetHL(0x4000)
	cpu.SetBC(uint16(len(payload)))
	invokeAPI(d, cpu, mem, F_WRITE, 0x6000)

	cpu.A = handle
	invokeAPI(d, cpu, mem, F_SYNC, 0x6000)
	if cpu.F&z80.FLAG_C != 0 {
		t.Fatalf("F_SYNC: CF set, A=%#x", cpu.A)
	}
	// Bytes must be readable from the host filesystem now.
	got, err := os.ReadFile(filepath.Join(mountRootFor(d), "synced.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "DURABLE" {
		t.Errorf("after F_SYNC: host file = %q, want %q", got, "DURABLE")
	}
}

func TestFSyncBadHandle(t *testing.T) {
	d, cpu, mem := setupFileAPI(t, nil)
	cpu.A = 55
	invokeAPI(d, cpu, mem, F_SYNC, 0x5000)
	if cpu.F&z80.FLAG_C == 0 || cpu.A != ESX_EBADF {
		t.Errorf("F_SYNC bad handle: CF=%v A=%#x, want CF=1 A=EBADF",
			cpu.F&z80.FLAG_C != 0, cpu.A)
	}
}
