package rzx

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

// buildSyntheticFile constructs an in-memory RZX with one of every
// block type so the round-trip tests exercise the full parser/writer.
func buildSyntheticFile() *File {
	return &File{
		Blocks: []Block{
			{
				Type: BlockCreator,
				Creator: &CreatorBlock{
					Program: "zx_go test",
					Major:   1,
					Minor:   0,
					Custom:  []byte{0xDE, 0xAD, 0xBE, 0xEF},
				},
			},
			{
				Type: BlockSnapshot,
				Snapshot: &SnapshotBlock{
					Format: SnapshotFormatSZX,
					Data:   bytes.Repeat([]byte{0xA5}, 1024),
				},
			},
			{
				Type: BlockInput,
				Input: &InputBlock{
					Tstates: 12345,
					Frames: []Frame{
						{Instructions: 100, InBytes: []byte{0xFF, 0xBF, 0xFE}},
						{Instructions: 200, InBytes: []byte{0xFF, 0xBF, 0xFE}}, // identical to previous — should round-trip via repeat marker if the recording side wrote it that way
						{Instructions: 50, RepeatLast: true},
						{Instructions: 75, InBytes: []byte{0x7F}},
						{Instructions: 0, InBytes: nil}, // zero-length frame
					},
				},
			},
			{
				Type: BlockSignStart,
				SignStart: &SignStartBlock{
					KeyID:    0xCAFEBABE,
					WeekCode: 0x12345678,
					Extra:    []byte{0x01, 0x02, 0x03},
				},
			},
			{
				Type: BlockSignEnd,
				SignEnd: &SignEndBlock{
					Signature: bytes.Repeat([]byte{0x55}, 64),
				},
			},
		},
	}
}

// File IO wrapper coverage: ReadFile, File.WriteFile, Recording.WriteFile
// and Playback.Stop.

func TestReadFileWriteFile_Roundtrip(t *testing.T) {
	src := buildSyntheticFile()
	path := t.TempDir() + "/test.rzx"
	if err := src.WriteFile(path, WriteOptions{Compress: false}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dst, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	assertFilesEqual(t, src, dst)
}

func TestReadFile_MissingFile(t *testing.T) {
	_, err := ReadFile("/nonexistent/does-not-exist.rzx")
	if err == nil {
		t.Errorf("ReadFile missing file = nil err")
	}
}

func TestWriteFile_ErrorPropagation(t *testing.T) {
	src := buildSyntheticFile()
	// Path inside a non-existent dir → os.WriteFile fails.
	err := src.WriteFile("/nonexistent/dir/x.rzx", WriteOptions{})
	if err == nil {
		t.Errorf("WriteFile to bad dir = nil err")
	}
}

func TestPlaybackStopIdempotent(t *testing.T) {
	src := buildSyntheticFile()
	data, _ := src.Write(WriteOptions{})
	f, _ := Read(data)
	p := NewPlayback(f)
	if p.Finished() {
		t.Errorf("Finished before Stop = true")
	}
	p.Stop()
	if !p.Finished() {
		t.Errorf("after Stop: Finished = false")
	}
	// Double-stop must be a no-op.
	p.Stop()
	if !p.Finished() {
		t.Errorf("after second Stop: Finished = false")
	}
}

func TestRoundTripUncompressed(t *testing.T) {
	src := buildSyntheticFile()
	data, err := src.Write(WriteOptions{Compress: false})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	dst, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertFilesEqual(t, src, dst)
}

func TestRoundTripCompressed(t *testing.T) {
	src := buildSyntheticFile()
	data, err := src.Write(WriteOptions{Compress: true})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	dst, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertFilesEqual(t, src, dst)
}

func TestRoundTripSigned(t *testing.T) {
	// Signed flag flips the header version byte and flag bit. The
	// blocks themselves are unchanged. Verify the header round-trips
	// without losing structure.
	src := buildSyntheticFile()
	data, err := src.Write(WriteOptions{Compress: true, Signed: true})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if data[5] != versionMinorSign {
		t.Errorf("signed file: minor version = %d, want %d", data[5], versionMinorSign)
	}
	if data[6] != 0x01 {
		t.Errorf("signed file: flags low byte = %02X, want 0x01", data[6])
	}
	dst, err := Read(data)
	if err != nil {
		t.Fatalf("Read signed: %v", err)
	}
	assertFilesEqual(t, src, dst)
}

func TestReadRejectsBadMagic(t *testing.T) {
	data := make([]byte, headerSize)
	copy(data, []byte("NOTRZX!!!"))
	_, err := Read(data)
	if err != ErrSignature {
		t.Errorf("Read non-RZX: err = %v, want ErrSignature", err)
	}
}

func TestReadRejectsTruncated(t *testing.T) {
	if _, err := Read([]byte{0, 1, 2}); err == nil {
		t.Error("Read 3-byte slice: want error, got nil")
	}
}

func TestReadRejectsUnknownBlock(t *testing.T) {
	// Build a valid header followed by a block with ID 0xAB (not in
	// the dispatch table) and a 5-byte length.
	var buf bytes.Buffer
	writeHeader(&buf, false)
	buf.WriteByte(0xAB)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(5))
	if _, err := Read(buf.Bytes()); err == nil {
		t.Error("Read unknown block: want error, got nil")
	}
}

func TestRepeatFrameMarker(t *testing.T) {
	// Hand-craft a tiny RZX with one IRB containing a normal frame
	// followed by a repeat-last frame, then verify both decode
	// correctly.
	src := &File{
		Blocks: []Block{
			{
				Type: BlockInput,
				Input: &InputBlock{
					Tstates: 0,
					Frames: []Frame{
						{Instructions: 10, InBytes: []byte{0x01, 0x02}},
						{Instructions: 20, RepeatLast: true},
					},
				},
			},
		},
	}
	data, err := src.Write(WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	dst, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(dst.Blocks) != 1 || dst.Blocks[0].Input == nil {
		t.Fatalf("expected 1 input block, got %#v", dst.Blocks)
	}
	frames := dst.Blocks[0].Input.Frames
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}
	if frames[1].RepeatLast != true {
		t.Errorf("frame 1 RepeatLast = false, want true")
	}
	if frames[1].InBytes != nil {
		t.Errorf("frame 1 InBytes = %v, want nil for repeat-last", frames[1].InBytes)
	}
}

func TestSnapshotCompressionFallback(t *testing.T) {
	// A 4-byte snapshot is too small for zlib to compress smaller —
	// the writer should fall back to uncompressed and the read side
	// should still recover the same bytes.
	src := &File{
		Blocks: []Block{
			{
				Type: BlockSnapshot,
				Snapshot: &SnapshotBlock{
					Format: SnapshotFormatZ80,
					Data:   []byte{0x01, 0x02, 0x03, 0x04},
				},
			},
		},
	}
	data, err := src.Write(WriteOptions{Compress: true})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	dst, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(dst.Blocks[0].Snapshot.Data, src.Blocks[0].Snapshot.Data) {
		t.Errorf("snapshot data mismatch: got %v, want %v",
			dst.Blocks[0].Snapshot.Data, src.Blocks[0].Snapshot.Data)
	}
}

// assertFilesEqual compares two parsed RZX files block-by-block. We
// don't use reflect.DeepEqual on the whole file because internal
// cursor state (currentBlock, dataFrame, ...) is irrelevant to the
// on-disk representation.
func assertFilesEqual(t *testing.T, want, got *File) {
	t.Helper()
	if len(got.Blocks) != len(want.Blocks) {
		t.Fatalf("block count: got %d, want %d", len(got.Blocks), len(want.Blocks))
	}
	for i := range want.Blocks {
		w, g := want.Blocks[i], got.Blocks[i]
		if w.Type != g.Type {
			t.Errorf("block %d: type %02X, want %02X", i, g.Type, w.Type)
			continue
		}
		switch w.Type {
		case BlockCreator:
			if !reflect.DeepEqual(w.Creator, g.Creator) {
				t.Errorf("block %d: creator mismatch:\n  got  %+v\n  want %+v", i, g.Creator, w.Creator)
			}
		case BlockSnapshot:
			if w.Snapshot.Format != g.Snapshot.Format {
				t.Errorf("block %d: snap format %q, want %q", i, g.Snapshot.Format, w.Snapshot.Format)
			}
			if !bytes.Equal(w.Snapshot.Data, g.Snapshot.Data) {
				t.Errorf("block %d: snap data mismatch (lens %d/%d)", i, len(g.Snapshot.Data), len(w.Snapshot.Data))
			}
		case BlockInput:
			if w.Input.Tstates != g.Input.Tstates {
				t.Errorf("block %d: tstates %d, want %d", i, g.Input.Tstates, w.Input.Tstates)
			}
			if len(w.Input.Frames) != len(g.Input.Frames) {
				t.Errorf("block %d: frame count %d, want %d", i, len(g.Input.Frames), len(w.Input.Frames))
				continue
			}
			for j := range w.Input.Frames {
				wf, gf := w.Input.Frames[j], g.Input.Frames[j]
				if wf.Instructions != gf.Instructions {
					t.Errorf("block %d frame %d: instructions %d, want %d", i, j, gf.Instructions, wf.Instructions)
				}
				if wf.RepeatLast != gf.RepeatLast {
					t.Errorf("block %d frame %d: repeatLast %v, want %v", i, j, gf.RepeatLast, wf.RepeatLast)
				}
				if !bytes.Equal(wf.InBytes, gf.InBytes) {
					t.Errorf("block %d frame %d: inBytes %v, want %v", i, j, gf.InBytes, wf.InBytes)
				}
			}
		case BlockSignStart:
			if !reflect.DeepEqual(w.SignStart, g.SignStart) {
				t.Errorf("block %d: sign-start mismatch:\n  got  %+v\n  want %+v", i, g.SignStart, w.SignStart)
			}
		case BlockSignEnd:
			if !bytes.Equal(w.SignEnd.Signature, g.SignEnd.Signature) {
				t.Errorf("block %d: sign-end signature mismatch", i)
			}
		}
	}
}
