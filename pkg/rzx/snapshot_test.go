package rzx

import (
	"bytes"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// makeTestSnapshot builds a populated snapshot.Snapshot for the bridge
// round-trip tests. Every CPU register and memory bank carries a
// distinguishable pattern so a missed field shows up immediately.
func makeTestSnapshot() *snapshot.Snapshot {
	s := snapshot.New()
	s.CPU.A = 0x12
	s.CPU.F = 0x34
	s.CPU.B = 0x56
	s.CPU.C = 0x78
	s.CPU.D = 0x9A
	s.CPU.E = 0xBC
	s.CPU.H = 0xDE
	s.CPU.L = 0xF0
	s.CPU.A_ = 0x11
	s.CPU.F_ = 0x22
	s.CPU.B_ = 0x33
	s.CPU.C_ = 0x44
	s.CPU.D_ = 0x55
	s.CPU.E_ = 0x66
	s.CPU.H_ = 0x77
	s.CPU.L_ = 0x88
	s.CPU.IX = 0xAABB
	s.CPU.IY = 0xCCDD
	s.CPU.SP = 0xEEFF
	s.CPU.PC = 0x1234
	s.CPU.I = 0x5A
	s.CPU.R = 0xA5
	s.CPU.IFF1 = true
	s.CPU.IFF2 = true
	s.CPU.IM = 1
	s.CPU.BorderColor = 3
	s.Memory.Is128K = true
	s.Memory.Port7FFD = 0x10
	for bank := 0; bank < 8; bank++ {
		for i := 0; i < 16384; i++ {
			s.Memory.RAM[bank][i] = byte((bank*16384 + i) & 0xFF)
		}
	}
	return s
}

func TestSnapshotBridgeSZXRoundTrip(t *testing.T) {
	src := makeTestSnapshot()
	block, err := EncodeSnapshot(src, SnapshotFormatSZX, false)
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	if block.Format != SnapshotFormatSZX {
		t.Errorf("Format = %q, want %q", block.Format, SnapshotFormatSZX)
	}
	if len(block.Data) == 0 {
		t.Fatal("encoded block has empty Data")
	}
	dst, err := DecodeSnapshot(block)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	assertSnapshotsEqual(t, src, dst)
}

func TestSnapshotBridgeZ80RoundTrip(t *testing.T) {
	src := makeTestSnapshot()
	block, err := EncodeSnapshot(src, SnapshotFormatZ80, false)
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	dst, err := DecodeSnapshot(block)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	assertSnapshotsEqual(t, src, dst)
}

func TestSnapshotBridgeInsideRZX(t *testing.T) {
	// End-to-end: encode a snapshot, wrap it in an RZX file, write,
	// read back, and decode the snapshot. This is the path the
	// playback driver will take when starting playback.
	src := makeTestSnapshot()
	block, err := EncodeSnapshot(src, SnapshotFormatSZX, false)
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	file := &File{Blocks: []Block{{Type: BlockSnapshot, Snapshot: block}}}
	data, err := file.Write(WriteOptions{Compress: true})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	parsed, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(parsed.Blocks) != 1 || parsed.Blocks[0].Snapshot == nil {
		t.Fatalf("expected 1 snapshot block, got %#v", parsed.Blocks)
	}
	dst, err := DecodeSnapshot(parsed.Blocks[0].Snapshot)
	if err != nil {
		t.Fatalf("DecodeSnapshot after RZX round-trip: %v", err)
	}
	assertSnapshotsEqual(t, src, dst)
}

func TestDecodeRejectsExternalLink(t *testing.T) {
	// External-link snapshot blocks have empty Data after parsing
	// (see readSnapshot). DecodeSnapshot must reject them rather
	// than silently returning an empty Snapshot.
	block := &SnapshotBlock{Format: SnapshotFormatSZX, Data: nil}
	if _, err := DecodeSnapshot(block); err == nil {
		t.Error("DecodeSnapshot of external-link block: want error, got nil")
	}
}

func TestDecodeRejectsUnknownFormat(t *testing.T) {
	block := &SnapshotBlock{Format: "ZIP", Data: []byte{1, 2, 3}}
	if _, err := DecodeSnapshot(block); err == nil {
		t.Error("DecodeSnapshot of unknown format: want error, got nil")
	}
}

func assertSnapshotsEqual(t *testing.T, want, got *snapshot.Snapshot) {
	t.Helper()
	if want.CPU != got.CPU {
		t.Errorf("CPU state mismatch:\n  want %+v\n  got  %+v", want.CPU, got.CPU)
	}
	for bank := 0; bank < 8; bank++ {
		if !bytes.Equal(want.Memory.RAM[bank], got.Memory.RAM[bank]) {
			t.Errorf("RAM bank %d: data mismatch", bank)
		}
	}
}
