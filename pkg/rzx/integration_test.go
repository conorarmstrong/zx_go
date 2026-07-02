package rzx

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// rzxTestRig wires up enough of the emulator to run tiny CPU programs
// against the RZX recording/playback hooks. The keyboard isn't reachable
// during these tests — they only exercise the IN-port intercept path,
// which uses an injectable peripheral byte source.
type rzxTestRig struct {
	cpu *z80.CPU
	mem *memory.Memory
	ula *ula.ULA
	dir string
}

// newRZXTestRig builds a fresh emulator instance backed by stub ROMs.
// Loads a tiny program at 0x8000 that loops forever:
//
//	IN A,(0xFE)   ; reads from port 0xFE — RZX intercepts
//	JP 0x8000     ; jump back, repeat
//
// The loop is two instructions per iteration, so a frame budget of N
// instructions yields N/2 IN reads. Perfect for round-trip testing.
func newRZXTestRig(t *testing.T) *rzxTestRig {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"48.rom", "128-0.rom", "128-1.rom"} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, memory.PageSize), 0644); err != nil {
			t.Fatalf("write stub ROM: %v", err)
		}
	}
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	kbd := keyboard.New()
	u := ula.New(mem, kbd)
	cpu := z80.New(mem, u)

	// Tiny IN-then-JP loop at 0x8000.
	mem.Write(0x8000, 0xDB) // IN A,(n)
	mem.Write(0x8001, 0xFE) // n = 0xFE
	mem.Write(0x8002, 0xC3) // JP nn
	mem.Write(0x8003, 0x00) // nn lo
	mem.Write(0x8004, 0x80) // nn hi
	cpu.PC = 0x8000
	// Disable interrupts so the end-of-frame IRQ doesn't push the
	// CPU into ROM (which would do additional IN reads we'd have
	// to model).
	cpu.IFF1 = false

	return &rzxTestRig{cpu: cpu, mem: mem, ula: u, dir: dir}
}

// snapshot extracts the rig's CPU + memory state into a snapshot.Snapshot
// the same way cmd/zx_go/main.go's createSnapshotFromEmulator does it.
// Inlined here so the test doesn't depend on package main.
func (r *rzxTestRig) snapshot() *snapshot.Snapshot {
	s := snapshot.New()
	s.CPU.A = r.cpu.A
	s.CPU.F = r.cpu.F
	s.CPU.B = r.cpu.B
	s.CPU.C = r.cpu.C
	s.CPU.D = r.cpu.D
	s.CPU.E = r.cpu.E
	s.CPU.H = r.cpu.H
	s.CPU.L = r.cpu.L
	s.CPU.A_ = r.cpu.A_
	s.CPU.F_ = r.cpu.F_
	s.CPU.B_ = r.cpu.B_
	s.CPU.C_ = r.cpu.C_
	s.CPU.D_ = r.cpu.D_
	s.CPU.E_ = r.cpu.E_
	s.CPU.H_ = r.cpu.H_
	s.CPU.L_ = r.cpu.L_
	s.CPU.IX = r.cpu.IX
	s.CPU.IY = r.cpu.IY
	s.CPU.SP = r.cpu.SP
	s.CPU.PC = r.cpu.PC
	s.CPU.I = r.cpu.I
	s.CPU.R = r.cpu.R
	s.CPU.IFF1 = r.cpu.IFF1
	s.CPU.IFF2 = r.cpu.IFF2
	s.CPU.IM = r.cpu.IM
	for i := 0; i < 8; i++ {
		copy(s.Memory.RAM[i], r.mem.GetPage(i))
	}
	return s
}

// applySnapshot is the inverse — restore the rig from a snapshot.
func (r *rzxTestRig) applySnapshot(s *snapshot.Snapshot) {
	r.cpu.A = s.CPU.A
	r.cpu.F = s.CPU.F
	r.cpu.B = s.CPU.B
	r.cpu.C = s.CPU.C
	r.cpu.D = s.CPU.D
	r.cpu.E = s.CPU.E
	r.cpu.H = s.CPU.H
	r.cpu.L = s.CPU.L
	r.cpu.A_ = s.CPU.A_
	r.cpu.F_ = s.CPU.F_
	r.cpu.B_ = s.CPU.B_
	r.cpu.C_ = s.CPU.C_
	r.cpu.D_ = s.CPU.D_
	r.cpu.E_ = s.CPU.E_
	r.cpu.H_ = s.CPU.H_
	r.cpu.L_ = s.CPU.L_
	r.cpu.IX = s.CPU.IX
	r.cpu.IY = s.CPU.IY
	r.cpu.SP = s.CPU.SP
	r.cpu.PC = s.CPU.PC
	r.cpu.I = s.CPU.I
	r.cpu.R = s.CPU.R
	r.cpu.IFF1 = s.CPU.IFF1
	r.cpu.IFF2 = s.CPU.IFF2
	r.cpu.IM = s.CPU.IM
	for i := 0; i < 8; i++ {
		copy(r.mem.GetPage(i), s.Memory.RAM[i])
	}
}

// TestRecordPlaybackEndToEnd records 5 frames against the test rig
// (each frame: one IN read + a few HALT-spin instructions),
// serialises the recording, parses it back, plays it against a fresh
// rig, and verifies the playback feeds the same IN bytes the recording
// captured. This is the canonical "round-trip determinism" test.
func TestRecordPlaybackEndToEnd(t *testing.T) {
	rig := newRZXTestRig(t)

	// Pre-program the keyboard port (0xFE) to return distinct bytes
	// per frame so we can verify playback delivers them in order.
	// Since the rig's keyboard isn't directly addressable, we install
	// our own ULA record hook later that captures the value.
	const framesToRecord = 5
	const instructionsPerFrame = 50

	rec := NewRecording()
	snap := rig.snapshot()
	block, err := EncodeSnapshot(snap, SnapshotFormatSZX, false)
	if err != nil {
		t.Fatalf("encode snap: %v", err)
	}
	rec.AddSnap(block, false)
	rec.StartInput(uint32(rig.cpu.Tstates()))

	// Hook the ULA so every IN read gets recorded.
	rig.ula.SetRZXRecordHook(rec.RecordIN)

	// Run framesToRecord frames. Each frame executes
	// instructionsPerFrame instructions; the test program does one
	// IN read at the start, then spins HALT, so the recorder
	// captures exactly one IN byte per frame.
	for f := 0; f < framesToRecord; f++ {
		before := rig.cpu.InstructionCount()
		rig.cpu.ExecuteRZXFrame(instructionsPerFrame)
		delta := rig.cpu.InstructionCount() - before
		if err := rec.StoreFrame(uint16(delta)); err != nil {
			t.Fatalf("frame %d StoreFrame: %v", f, err)
		}
	}
	rig.ula.SetRZXRecordHook(nil)

	// Flatten the recording's frames into a single per-frame IN
	// stream, expanding RepeatLast frames into the previous frame's
	// bytes (so the comparison against playback is on the actual
	// byte sequence the CPU should see, not the on-wire encoding).
	type frameBytes []byte
	wantFrames := make([]frameBytes, 0, framesToRecord)
	var lastBytes frameBytes
	for _, frame := range rec.file.Blocks[len(rec.file.Blocks)-1].Input.Frames {
		if frame.RepeatLast {
			// Copy the last non-repeated frame's bytes.
			fb := make(frameBytes, len(lastBytes))
			copy(fb, lastBytes)
			wantFrames = append(wantFrames, fb)
		} else {
			fb := make(frameBytes, len(frame.InBytes))
			copy(fb, frame.InBytes)
			wantFrames = append(wantFrames, fb)
			lastBytes = fb
		}
	}
	if len(wantFrames) != framesToRecord {
		t.Fatalf("recorded frame count = %d, want %d", len(wantFrames), framesToRecord)
	}
	// Sanity check: every frame should have at least one IN byte
	// (the IN-JP loop fits ~7 IN reads in 50 instructions).
	for i, fb := range wantFrames {
		if len(fb) == 0 {
			t.Fatalf("frame %d recorded zero IN bytes — IN-JP loop produced no reads?", i)
		}
	}

	// Serialise → reload → play back against a fresh rig.
	data, err := rec.Bytes(WriteOptions{Compress: true})
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	parsed, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	rig2 := newRZXTestRig(t)
	pb := NewPlayback(parsed)
	startSnap, err := pb.Start(0)
	if err != nil {
		t.Fatalf("Start playback: %v", err)
	}
	if startSnap == nil {
		t.Fatal("Start returned nil snapshot")
	}
	decoded, err := DecodeSnapshot(startSnap)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	rig2.applySnapshot(decoded)
	rig2.cpu.SetTstates(uint64(pb.Tstates()))

	// Per-frame capture of what the playback hook delivered.
	var perFrameGot [][]byte
	var currentFrameGot []byte
	rig2.ula.SetRZXPlaybackHook(func() (byte, bool) {
		b, err := pb.NextByte()
		if err != nil {
			return 0, false
		}
		currentFrameGot = append(currentFrameGot, b)
		return b, true
	})

	for f := 0; f < framesToRecord; f++ {
		currentFrameGot = nil
		instructions := uint64(pb.Instructions())
		rig2.cpu.ExecuteRZXFrame(instructions)
		fb := make([]byte, len(currentFrameGot))
		copy(fb, currentFrameGot)
		perFrameGot = append(perFrameGot, fb)
		if _, err := pb.Frame(); err != nil && !errors.Is(err, ErrPlaybackFinished) {
			t.Fatalf("frame %d Frame: %v", f, err)
		}
	}

	if len(perFrameGot) != len(wantFrames) {
		t.Fatalf("got %d playback frames, want %d", len(perFrameGot), len(wantFrames))
	}
	for f := range wantFrames {
		if !bytes.Equal(perFrameGot[f], wantFrames[f]) {
			t.Errorf("frame %d:\n  got  %v\n  want %v", f, perFrameGot[f], wantFrames[f])
		}
	}
}

// TestPlaybackDesyncTriggersError verifies that if the CPU during
// playback consumes a different number of IN reads than the recording
// captured for that frame, the playback layer reports a desync error.
// This simulates the "emulator divergence" failure mode.
func TestPlaybackDesyncTriggersError(t *testing.T) {
	// Build a recording with 3 IN bytes in a frame.
	file := &File{
		Blocks: []Block{
			{Type: BlockInput, Input: &InputBlock{
				Frames: []Frame{
					{Instructions: 100, InBytes: []byte{0x01, 0x02, 0x03}},
				},
			}},
		},
	}
	pb := NewPlayback(file)
	if _, err := pb.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Consume only 2 of 3 bytes — simulating a CPU that did one
	// fewer IN read than the recording.
	_, _ = pb.NextByte()
	_, _ = pb.NextByte()

	// Closing the frame should report the desync.
	if _, err := pb.Frame(); err == nil {
		t.Error("Frame after partial IN consumption: want desync error, got nil")
	}
}
