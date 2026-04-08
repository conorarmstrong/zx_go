// Package rzx implements the .rzx input recording format used by ZX
// Spectrum emulators (FUSE, Spectaculator, etc.) for reproducible
// session playback. The package mirrors libspectrum's rzx.c API
// (rzx_alloc / rzx_read / rzx_write / rzx_start_input / ...) so the
// emulator-side glue can follow FUSE's rzx.c almost line-for-line.
//
// An RZX file is a chain of typed blocks: a creator block, an embedded
// snapshot, one or more "input recording blocks" (IRBs), optionally
// bracketed by sign-start / sign-end blocks. Each IRB carries a sequence
// of frames; each frame stores how many CPU instructions were executed
// in that frame plus the sequence of bytes the CPU read from IN ports.
// Replay feeds those bytes back at the right moment, which makes the
// session deterministic without needing to model the original
// peripheral hardware.
//
// All multi-byte integers in an RZX file are little-endian.
package rzx

// BlockType is the 1-byte ID that introduces every RZX block. Values
// match libspectrum's libspectrum_rzx_block_id enum (libspectrum.h.in:878).
type BlockType byte

const (
	BlockCreator   BlockType = 0x10 // Tool that produced the file
	BlockSignStart BlockType = 0x20 // Start of signed region (DSA)
	BlockSignEnd   BlockType = 0x21 // End of signed region (DSA signature)
	BlockSnapshot  BlockType = 0x30 // Embedded snapshot (Z80/SZX/SNA)
	BlockInput     BlockType = 0x80 // Input recording block (IRB)
)

// Magic bytes that identify an RZX file.
var magic = []byte("RZX!")

// Header layout — 10 bytes total at the start of every RZX file.
const (
	headerSize       = 10
	versionMajor     = 0
	versionMinorPlay = 12 // Unsigned files
	versionMinorSign = 13 // DSA-signed files
)

// Header flags (4-byte little-endian field at offset 6).
const (
	headerFlagSigned uint32 = 0x01 // DSA signature blocks present
)

// Snapshot block flags (4-byte little-endian field at offset +5 of the
// snapshot block payload).
const (
	snapFlagExternal   uint32 = 0x01 // Snapshot is an external file path, not embedded — libspectrum skips these
	snapFlagCompressed uint32 = 0x02 // Embedded snapshot bytes are zlib-compressed
)

// Input recording block flags (4-byte little-endian field at offset +13
// of the IRB payload). Bit 0 (0x01) is reserved by libspectrum and not
// emitted by anything we know of, so we don't define a constant for it.
const (
	irbFlagCompressed uint32 = 0x02 // Frame stream is zlib-compressed
)

// repeatFrameMarker is the special IN-count value that means "this
// frame's IN reads are identical to the last non-repeated frame's IN
// reads, don't store them again". libspectrum.c:175.
const repeatFrameMarker uint16 = 0xFFFF

// Snapshot format identifiers — written as 4 bytes (3-char name +
// trailing NUL) inside a snapshot block. Matches libspectrum.c:45.
const (
	SnapshotFormatSNA = "SNA"
	SnapshotFormatSZX = "SZX"
	SnapshotFormatZ80 = "Z80"
)

// File is the in-memory representation of an RZX file: a chronological
// list of blocks. Cursor state for playback / recording lives on the
// Playback / Recording driver types, not here.
type File struct {
	Blocks []Block
}

// Block is the discriminated-union representation of any RZX block. The
// Type field selects exactly one of the inner pointers; the others
// remain nil. We use a sum-type-by-tag rather than an interface so the
// playback / recording / iteration paths can stay allocation-free and
// switch on Type without type assertions.
type Block struct {
	Type      BlockType
	Creator   *CreatorBlock
	Snapshot  *SnapshotBlock
	Input     *InputBlock
	SignStart *SignStartBlock
	SignEnd   *SignEndBlock
}

// CreatorBlock identifies the tool that produced the file. Round-tripped
// verbatim — the parser doesn't validate Program / Major / Minor.
type CreatorBlock struct {
	Program string // 20 bytes, NUL-padded on the wire
	Major   uint16
	Minor   uint16
	Custom  []byte // arbitrary creator-specific payload
}

// SnapshotBlock embeds a Spectrum snapshot. Data holds the raw,
// uncompressed snapshot bytes; the parser handles zlib decompression
// transparently. Format is the 3-character snapshot type identifier
// (SNA / SZX / Z80). Automatic distinguishes intermediate (rollback)
// snapshots from the user-driven initial snapshot — set by the
// recording side via AddSnap.
type SnapshotBlock struct {
	Format    string
	Data      []byte
	Automatic bool
}

// InputBlock (IRB — Input Recording Block) is a sequence of frames
// recorded between two snapshots, plus the T-state offset at which the
// block began. Frames is the per-frame instruction-count + IN-byte
// stream that drives playback.
type InputBlock struct {
	Tstates uint32 // CPU T-state counter at the moment recording started
	Frames  []Frame

	// nonRepeat tracks the index of the last non-repeated frame so the
	// recording side can collapse runs of identical frames into
	// repeatFrameMarker. Mirrors input_block_t.non_repeat in libspectrum.
	nonRepeat int
}

// Frame is one emulator frame's worth of input. Instructions is the
// fetch count (R-derived) consumed during the frame; InBytes is the
// sequence of bytes returned to the CPU by IN-port reads in the order
// the CPU asked for them. RepeatLast=true means "use the last
// non-repeated frame's IN bytes" — InBytes is nil in that case.
type Frame struct {
	Instructions uint16
	InBytes      []byte
	RepeatLast   bool
}

// SignStartBlock marks the start of a DSA-signed region. We preserve
// the fields for round-trip but don't verify the signature — matching
// FUSE's behaviour when gcrypt is unavailable.
type SignStartBlock struct {
	KeyID    uint32
	WeekCode uint32
	Extra    []byte // any bytes after the week code, preserved for round-trip
}

// SignEndBlock holds the raw DSA signature payload (the r and s MPIs as
// they appear on disk). We don't decode them; the bytes are preserved
// verbatim so a parsed-then-written file is byte-identical when not
// recompressed.
type SignEndBlock struct {
	Signature []byte
}
