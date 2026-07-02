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

// DetectFormat detects the snapshot format based on file extension and content
func DetectFormat(path string) SnapshotFormat {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".sna":
		return FormatSNA
	case ".z80":
		return FormatZ80
	case ".szx":
		return FormatSZX
	default:
		return FormatUnknown
	}
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
