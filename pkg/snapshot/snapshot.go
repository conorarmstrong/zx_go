package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SnapshotFormat represents different snapshot file formats
type SnapshotFormat int

const (
	FormatSNA SnapshotFormat = iota
	FormatZ80
	FormatSZX
	FormatUnknown
)

// CPUState represents the Z80 CPU state in a snapshot
type CPUState struct {
	// 8-bit registers
	A, F, B, C, D, E, H, L byte
	// Shadow registers
	A_, F_, B_, C_, D_, E_, H_, L_ byte
	// 16-bit registers
	IX, IY, SP, PC uint16
	// Special registers
	I, R byte
	// Interrupt state
	IFF1, IFF2 bool
	IM         byte
	// Halted — true when the CPU is parked in HALT awaiting an
	// interrupt. SNA/SZX/Z80 file formats don't carry this bit
	// (loaders leave it false, matching their semantics: a
	// file-loaded snapshot resumes running); the in-process
	// time-travel ring needs it so a rewind restores the exact
	// execution state (a stale Halted=true with IFF1=false left
	// the rewound CPU unresumable).
	Halted bool
	// Border color
	BorderColor byte
}

// MemoryState represents the memory contents
type MemoryState struct {
	RAM      [8][]byte // 8 banks of 16KB each
	Is128K   bool
	Port7FFD byte // 128K paging register
	// Port1FFD is the +2A/+3 secondary paging register: bit 0 selects
	// special paging, bits 2:1 the special configuration, and bit 2
	// doubles as the high bit of the ROM select. A +3 snapshot without
	// it loses special paging and ROM 2/3. Carried by SZX and by .z80
	// v3 with a 55-byte extended header; the SNA format has no slot
	// for it.
	Port1FFD byte
}

// Snapshot handles loading and saving of emulator snapshots.
type Snapshot struct {
	CPU    CPUState
	Memory MemoryState
	Format SnapshotFormat
}

// New creates a new Snapshot instance.
func New() *Snapshot {
	s := &Snapshot{
		Format: FormatUnknown,
	}
	// Initialize RAM banks
	for i := 0; i < 8; i++ {
		s.Memory.RAM[i] = make([]byte, 16384)
	}
	return s
}

// snaSize128KAlt is the 128K SNA variant carrying one extra bank, for the case
// where the page mapped at $C000 is not already among the five stored banks.
// The other two sizes are SNA48K_SIZE / SNA128K_SIZE in sna.go.
const snaSize128KAlt = SNA128K_SIZE + 16384

// DetectFormat detects the snapshot format from the file extension, falling
// back to the file's size when the extension is not one we know.
//
// The fallback exists because snapshots circulate under other names: NextZXOS
// ships games whose snapshots carry a .snx extension and are byte-for-byte
// 48K SNA. Without it those are rejected as an unsupported format, which is
// what the extension-only version did despite this function's contract having
// always promised to look at content too.
func DetectFormat(path string) SnapshotFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sna":
		return FormatSNA
	case ".z80":
		return FormatZ80
	case ".szx":
		return FormatSZX
	}
	// Unknown extension: an exact SNA size is unambiguous enough to act on.
	// Z80 and SZX are variable-length and carry no size signature, so they
	// are deliberately not guessed at.
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		switch fi.Size() {
		case SNA48K_SIZE, SNA128K_SIZE, snaSize128KAlt:
			return FormatSNA
		}
	}
	return FormatUnknown
}

// Load loads a snapshot from a file.
func (s *Snapshot) Load(path string) error {
	format := DetectFormat(path)
	if format == FormatUnknown {
		return fmt.Errorf("unsupported snapshot format: %s", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open snapshot file: %w", err)
	}
	defer func() { _ = file.Close() }()

	s.Format = format

	switch format {
	case FormatSNA:
		return s.loadSNA(file)
	case FormatZ80:
		return s.loadZ80(file)
	case FormatSZX:
		return s.loadSZX(file)
	default:
		return fmt.Errorf("unsupported format: %d", format)
	}
}

// LoadBytes parses a snapshot from an in-memory byte slice using the
// supplied format. Used by callers that have the snapshot bytes
// available without a backing file — in particular the RZX package,
// which embeds snapshots inside its own container format.
func (s *Snapshot) LoadBytes(data []byte, format SnapshotFormat) error {
	s.Format = format
	r := bytes.NewReader(data)
	switch format {
	case FormatSNA:
		return s.loadSNA(r)
	case FormatZ80:
		return s.loadZ80(r)
	case FormatSZX:
		return s.loadSZX(r)
	default:
		return fmt.Errorf("unsupported format: %d", format)
	}
}

// SaveBytes serialises the snapshot to a byte slice in the requested
// format. Symmetric counterpart to LoadBytes for the RZX embedding path.
func (s *Snapshot) SaveBytes(format SnapshotFormat) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch format {
	case FormatSNA:
		err = s.saveSNA(&buf)
	case FormatZ80:
		err = s.saveZ80(&buf)
	case FormatSZX:
		err = s.saveSZX(&buf)
	default:
		return nil, fmt.Errorf("unsupported format: %d", format)
	}
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Save saves a snapshot to a file.
func (s *Snapshot) Save(path string) error {
	format := DetectFormat(path)
	if format == FormatUnknown {
		return fmt.Errorf("unsupported snapshot format: %s", path)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create snapshot file: %w", err)
	}
	defer func() { _ = file.Close() }()

	switch format {
	case FormatSNA:
		return s.saveSNA(file)
	case FormatZ80:
		return s.saveZ80(file)
	case FormatSZX:
		return s.saveSZX(file)
	default:
		return fmt.Errorf("unsupported format: %d", format)
	}
}
