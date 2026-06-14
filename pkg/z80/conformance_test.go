package z80

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flatMem is a 64K flat RAM used by the Cringle conformance harness.
// No paging, no ROM, no contention — the test binaries don't touch
// I/O ports or care about timing, only that loads / stores commit to
// a uniform address space.
type flatMem [0x10000]byte

func (m *flatMem) Read(addr uint16) byte       { return m[addr] }
func (m *flatMem) Write(addr uint16, val byte) { m[addr] = val }
func (m *flatMem) ContendPort(_ uint16)        {}

// nullULA is an inert port-I/O sink: reads return 0xFF, writes are
// discarded. zexdoc / zexall never use ports but the CPU interface
// requires a ULA so we provide one.
type nullULA struct{}

func (nullULA) ReadPort(_ uint16) (byte, bool) { return 0xFF, false }
func (nullULA) WritePort(_ uint16, _ byte)     {}

// runCringleConformance loads a CP/M COM file at 0x0100 and runs it
// under a minimal harness:
//
//   - 64K flat RAM
//   - BDOS trap at 0x0005: function 2 prints E as a char, function 9
//     prints a $-terminated string at DE. A RET opcode is planted at
//     0x0005 so a CALL into it returns naturally after the trap fires.
//   - Termination: WBOOT at 0x0000. CP/M COM convention has the BIOS
//     push 0x0000 on entry so a final RET pops PC=0x0000. We also
//     accept JP 0x0000 — both routes land at PC=0.
//
// Returns the captured output for assertion. Streams progress to
// stdout when -v is set so long runs are observable.
func runCringleConformance(t *testing.T, comPath string) string {
	t.Helper()
	data, err := os.ReadFile(comPath)
	if err != nil {
		t.Fatalf("read %s: %v", comPath, err)
	}
	mem := new(flatMem)
	copy(mem[0x0100:], data)
	mem[0x0005] = 0xC9 // RET — the post-BDOS-trap continuation
	mem[0x0000] = 0x76 // HALT — we'll detect PC==0 before this fires anyway

	cpu := New(mem, nullULA{})
	cpu.PC = 0x0100
	cpu.SP = 0xFFFE
	// Push 0x0000 as the "return from main" target.
	mem[0xFFFE] = 0x00
	mem[0xFFFF] = 0x00

	var out bytes.Buffer
	streamProgress := testing.Verbose()

	emit := func(b byte) {
		out.WriteByte(b)
		if streamProgress {
			fmt.Print(string(b))
		}
	}

	// Each Cringle test prints "OK" or "ERROR" at the end of its
	// line. Fail fast on the first ERROR rather than running the
	// remaining tests to completion (which would take many minutes).
	checkLastLineForError := func() bool {
		s := out.Bytes()
		nl := bytes.LastIndexByte(s, '\n')
		line := s[nl+1:]
		return bytes.Contains(line, []byte("ERROR"))
	}

	// 60 billion is enough for full zexall on a slow host: the
	// suite totals ~50 billion instructions at typical emulator
	// throughput. Without the cap a real infinite loop would hang
	// CI for hours.
	const maxInstr = 60_000_000_000
	for i := uint64(0); i < maxInstr; i++ {
		if cpu.PC == 0x0000 && i > 0 {
			break
		}
		if cpu.PC == 0x0005 {
			switch cpu.C {
			case 0x02:
				emit(cpu.E)
				if cpu.E == '\n' && checkLastLineForError() {
					return out.String()
				}
			case 0x09:
				addr := cpu.DE()
				start := addr
				for {
					b := mem[addr]
					if b == '$' {
						break
					}
					emit(b)
					addr++
					if addr == start { // wrapped — sentinel never found
						t.Fatalf("BDOS 9: '$' not found within 64K from DE=%#x", cpu.DE())
					}
				}
			}
			// Fall through — RET at 0x0005 executes normally.
		}
		cpu.StepInstruction()
	}
	if cpu.PC != 0x0000 {
		t.Fatalf("conformance run did not terminate within %d instructions (PC=%#x)",
			maxInstr, cpu.PC)
	}
	return out.String()
}

// TestZexdoc runs Frank Cringle's zexdoc.com — the documented-only
// instruction exerciser. ~1-3 minutes on a modern host.
// Skipped under -short.
func TestZexdoc(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in -short mode (run without -short to enable)")
	}
	out := runCringleConformance(t, filepath.Join("testdata", "conformance", "zexdoc.com"))
	if strings.Contains(out, "ERROR") {
		t.Fatalf("zexdoc reported errors:\n%s", out)
	}
	t.Logf("zexdoc output:\n%s", out)
}

// TestZexall runs Frank Cringle's zexall.com — documented AND
// undocumented behaviour (F3/F5 flag bits, MEMPTR, undocumented BIT
// n,(HL) flags). ~5-15 minutes on a modern host. Skipped under
// -short.
func TestZexall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping conformance suite in -short mode (run without -short to enable)")
	}
	out := runCringleConformance(t, filepath.Join("testdata", "conformance", "zexall.com"))
	if strings.Contains(out, "ERROR") {
		t.Fatalf("zexall reported errors:\n%s", out)
	}
	t.Logf("zexall output:\n%s", out)
}
