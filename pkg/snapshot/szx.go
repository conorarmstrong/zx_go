package snapshot

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

// SZX format constants
const (
	SZX_SIGNATURE     = "ZXST"
	SZX_VERSION_MAJOR = 1
	SZX_VERSION_MINOR = 4
	// szxMaxBlockSize bounds the block-length field read from an SZX
	// block header before it is used as an allocation size. Real SZX
	// blocks (register sets, a compressed 16K RAM page, small metadata)
	// are at most tens of kilobytes; this cap is far above that, so it
	// only rejects a corrupt or hostile length field, not real content.
	szxMaxBlockSize = 64 * 1024 * 1024
)

// SZX block types
const (
	ZXSTBID_CREATOR   = "CRTR"
	ZXSTBID_Z80REGS   = "Z80R"
	ZXSTBID_SPECREGS  = "SPCR"
	ZXSTBID_RAMPAGE   = "RAMP"
	ZXSTBID_AY        = "AY\x00\x00"
	ZXSTBID_TIMINGS   = "TMNG"
	ZXSTBID_KEYBOARD  = "KEYB"
	ZXSTBID_MOUSE     = "AMXM"
	ZXSTBID_JOYSTICK  = "JOY\x00"
	ZXSTBID_COVOX     = "COVX"
	ZXSTBID_SOUNDDEV  = "SDEV"
	ZXSTBID_OPUS      = "OPUS"
	ZXSTBID_PLUSD     = "PLSD"
	ZXSTBID_DISCIPLE  = "DSC\x00"
	ZXSTBID_MULTIFACE = "MFCE"
	ZXSTBID_ROM       = "ROM\x00"
)

// SZX machine IDs
const (
	ZXSTMID_16K          = 0
	ZXSTMID_48K          = 1
	ZXSTMID_128K         = 2
	ZXSTMID_PLUS2        = 3
	ZXSTMID_PLUS2A       = 4
	ZXSTMID_PLUS3        = 5
	ZXSTMID_PLUS3E       = 6
	ZXSTMID_PENTAGON128  = 7
	ZXSTMID_TC2048       = 8
	ZXSTMID_TC2068       = 9
	ZXSTMID_SCORPION     = 10
	ZXSTMID_SE           = 11
	ZXSTMID_TS2068       = 12
	ZXSTMID_PENTAGON512  = 13
	ZXSTMID_PENTAGON1024 = 14
	ZXSTMID_NTSC48K      = 15
	ZXSTMID_128KE        = 16
)

// SZX header structure
type szxHeader struct {
	Signature    [4]byte
	MajorVersion byte
	MinorVersion byte
	MachineID    byte
	Flags        byte
}

// SZX block header structure
type szxBlockHeader struct {
	ID   [4]byte
	Size uint32
}

// Z80 registers block structure
type szxZ80Regs struct {
	AF, BC, DE, HL       uint16
	AF_, BC_, DE_, HL_   uint16
	IX, IY, SP, PC       uint16
	I, R, IFF1, IFF2, IM byte
	CyclesStart          uint32
	HoldIntReqCycles     byte
	Flags                byte
	MemPtr               uint16
}

// Spectrum registers block structure
type szxSpecRegs struct {
	Border   byte
	Port7FFD byte
	Port1FFD byte
	PortFE   byte
	Reserved [4]byte
}

// RAM page block structure
type szxRamPage struct {
	Flags      uint16
	PageNumber byte
	// Data follows (16384 bytes, possibly compressed)
}

// loadSZX loads a .SZX format snapshot
func (s *Snapshot) loadSZX(file io.Reader) error {
	// Read SZX header
	var header szxHeader
	if err := binary.Read(file, binary.LittleEndian, &header); err != nil {
		return fmt.Errorf("failed to read SZX header: %w", err)
	}

	// Verify signature
	if string(header.Signature[:]) != SZX_SIGNATURE {
		return fmt.Errorf("invalid SZX signature: %s", string(header.Signature[:]))
	}

	// Set machine type based on machine ID
	switch header.MachineID {
	case ZXSTMID_16K, ZXSTMID_48K, ZXSTMID_NTSC48K:
		s.Memory.Is128K = false
	case ZXSTMID_128K, ZXSTMID_PLUS2, ZXSTMID_PLUS2A, ZXSTMID_PLUS3, ZXSTMID_128KE:
		s.Memory.Is128K = true
	default:
		s.Memory.Is128K = false // Default to 48K for unknown machines
	}

	// Process blocks
	for {
		var blockHeader szxBlockHeader
		err := binary.Read(file, binary.LittleEndian, &blockHeader)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read block header: %w", err)
		}

		blockID := string(blockHeader.ID[:])
		blockSize := blockHeader.Size
		if blockSize > szxMaxBlockSize {
			return fmt.Errorf("SZX block %q declares implausible size %d bytes (max %d)", blockID, blockSize, szxMaxBlockSize)
		}

		// Read block data
		blockData := make([]byte, blockSize)
		if _, err := io.ReadFull(file, blockData); err != nil {
			return fmt.Errorf("failed to read block data for %s: %w", blockID, err)
		}

		// Process block based on ID
		switch blockID {
		case ZXSTBID_Z80REGS:
			if err := s.loadSZXZ80Regs(blockData); err != nil {
				return fmt.Errorf("failed to load Z80 registers: %w", err)
			}

		case ZXSTBID_SPECREGS:
			if err := s.loadSZXSpecRegs(blockData); err != nil {
				return fmt.Errorf("failed to load Spectrum registers: %w", err)
			}

		case ZXSTBID_RAMPAGE:
			if err := s.loadSZXRamPage(blockData); err != nil {
				return fmt.Errorf("failed to load RAM page: %w", err)
			}

		default:
			// Skip unknown blocks
			continue
		}
	}

	return nil
}

// loadSZXZ80Regs loads Z80 CPU registers from SZX block
func (s *Snapshot) loadSZXZ80Regs(data []byte) error {
	if len(data) < 37 {
		return fmt.Errorf("z80 registers block too small: %d bytes", len(data))
	}

	var regs szxZ80Regs
	if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &regs); err != nil {
		return fmt.Errorf("failed to parse Z80 registers: %w", err)
	}

	// Convert from SZX format to our CPU state
	s.CPU.A = byte(regs.AF >> 8)
	s.CPU.F = byte(regs.AF & 0xFF)
	s.CPU.B = byte(regs.BC >> 8)
	s.CPU.C = byte(regs.BC & 0xFF)
	s.CPU.D = byte(regs.DE >> 8)
	s.CPU.E = byte(regs.DE & 0xFF)
	s.CPU.H = byte(regs.HL >> 8)
	s.CPU.L = byte(regs.HL & 0xFF)

	s.CPU.A_ = byte(regs.AF_ >> 8)
	s.CPU.F_ = byte(regs.AF_ & 0xFF)
	s.CPU.B_ = byte(regs.BC_ >> 8)
	s.CPU.C_ = byte(regs.BC_ & 0xFF)
	s.CPU.D_ = byte(regs.DE_ >> 8)
	s.CPU.E_ = byte(regs.DE_ & 0xFF)
	s.CPU.H_ = byte(regs.HL_ >> 8)
	s.CPU.L_ = byte(regs.HL_ & 0xFF)

	s.CPU.IX = regs.IX
	s.CPU.IY = regs.IY
	s.CPU.SP = regs.SP
	s.CPU.PC = regs.PC
	s.CPU.I = regs.I
	s.CPU.R = regs.R
	s.CPU.IFF1 = (regs.IFF1 != 0)
	s.CPU.IFF2 = (regs.IFF2 != 0)
	s.CPU.IM = regs.IM

	return nil
}

// loadSZXSpecRegs loads Spectrum-specific registers from SZX block
func (s *Snapshot) loadSZXSpecRegs(data []byte) error {
	if len(data) < 8 {
		return fmt.Errorf("spectrum registers block too small: %d bytes", len(data))
	}

	var regs szxSpecRegs
	if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &regs); err != nil {
		return fmt.Errorf("failed to parse Spectrum registers: %w", err)
	}

	s.CPU.BorderColor = regs.Border & 0x07
	s.Memory.Port7FFD = regs.Port7FFD

	return nil
}

// loadSZXRamPage loads a RAM page from SZX block
func (s *Snapshot) loadSZXRamPage(data []byte) error {
	if len(data) < 3 {
		return fmt.Errorf("ram page block too small: %d bytes", len(data))
	}

	var ramPage szxRamPage
	if err := binary.Read(bytes.NewReader(data[:3]), binary.LittleEndian, &ramPage); err != nil {
		return fmt.Errorf("failed to parse RAM page header: %w", err)
	}

	pageData := data[3:]
	bankNum := int(ramPage.PageNumber)

	// Check if data is compressed
	isCompressed := (ramPage.Flags & 0x01) != 0

	var memData []byte
	if isCompressed {
		// Decompress using zlib
		reader, err := zlib.NewReader(bytes.NewReader(pageData))
		if err != nil {
			return fmt.Errorf("failed to create zlib reader: %w", err)
		}
		defer func() { _ = reader.Close() }()

		// A RAM page is always exactly 16384 bytes uncompressed, so cap
		// the read just above that. Without this, a small hostile zlib
		// stream could inflate to an arbitrarily large buffer (a
		// "decompression bomb") before the excess is discarded below.
		decompressed, err := io.ReadAll(io.LimitReader(reader, 16384+1))
		if err != nil {
			return fmt.Errorf("failed to decompress RAM page %d: %w", bankNum, err)
		}
		memData = decompressed
	} else {
		memData = pageData
	}

	// SZX page numbers map directly to RAM bank numbers (0-7)
	if bankNum < 0 || bankNum > 7 {
		return fmt.Errorf("unknown RAM page number: %d", bankNum)
	}
	targetBank := bankNum

	// Copy data to RAM bank
	if len(memData) >= 16384 {
		copy(s.Memory.RAM[targetBank], memData[:16384])
	} else {
		// Zero-fill if data is shorter
		copy(s.Memory.RAM[targetBank][:len(memData)], memData)
		for i := len(memData); i < 16384; i++ {
			s.Memory.RAM[targetBank][i] = 0
		}
	}

	return nil
}

// saveSZX saves a snapshot in .SZX format
func (s *Snapshot) saveSZX(file io.Writer) error {
	// Write SZX header
	header := szxHeader{
		Signature:    [4]byte{'Z', 'X', 'S', 'T'},
		MajorVersion: SZX_VERSION_MAJOR,
		MinorVersion: SZX_VERSION_MINOR,
		Flags:        0,
	}

	// Set machine ID based on our memory configuration
	if s.Memory.Is128K {
		header.MachineID = ZXSTMID_128K
	} else {
		header.MachineID = ZXSTMID_48K
	}

	if err := binary.Write(file, binary.LittleEndian, header); err != nil {
		return fmt.Errorf("failed to write SZX header: %w", err)
	}

	// Write Z80 registers block
	if err := s.saveSZXZ80Regs(file); err != nil {
		return fmt.Errorf("failed to write Z80 registers: %w", err)
	}

	// Write Spectrum registers block
	if err := s.saveSZXSpecRegs(file); err != nil {
		return fmt.Errorf("failed to write Spectrum registers: %w", err)
	}

	// Write RAM pages
	if s.Memory.Is128K {
		// Write all 8 banks for 128K
		for i := 0; i < 8; i++ {
			if err := s.saveSZXRamPage(file, i); err != nil {
				return fmt.Errorf("failed to write RAM page %d: %w", i, err)
			}
		}
	} else {
		// Write only banks 0, 2, 5 for 48K
		banks := []int{5, 2, 0}
		for _, bank := range banks {
			if err := s.saveSZXRamPage(file, bank); err != nil {
				return fmt.Errorf("failed to write RAM page %d: %w", bank, err)
			}
		}
	}

	return nil
}

// saveSZXZ80Regs saves Z80 CPU registers as SZX block
func (s *Snapshot) saveSZXZ80Regs(file io.Writer) error {
	// Write block header
	blockHeader := szxBlockHeader{
		ID:   [4]byte{'Z', '8', '0', 'R'},
		Size: 37, // Size of Z80 registers structure
	}
	if err := binary.Write(file, binary.LittleEndian, blockHeader); err != nil {
		return err
	}

	// Create Z80 registers structure
	regs := szxZ80Regs{
		AF:               uint16(s.CPU.A)<<8 | uint16(s.CPU.F),
		BC:               uint16(s.CPU.B)<<8 | uint16(s.CPU.C),
		DE:               uint16(s.CPU.D)<<8 | uint16(s.CPU.E),
		HL:               uint16(s.CPU.H)<<8 | uint16(s.CPU.L),
		AF_:              uint16(s.CPU.A_)<<8 | uint16(s.CPU.F_),
		BC_:              uint16(s.CPU.B_)<<8 | uint16(s.CPU.C_),
		DE_:              uint16(s.CPU.D_)<<8 | uint16(s.CPU.E_),
		HL_:              uint16(s.CPU.H_)<<8 | uint16(s.CPU.L_),
		IX:               s.CPU.IX,
		IY:               s.CPU.IY,
		SP:               s.CPU.SP,
		PC:               s.CPU.PC,
		I:                s.CPU.I,
		R:                s.CPU.R,
		IM:               s.CPU.IM,
		CyclesStart:      0,
		HoldIntReqCycles: 0,
		Flags:            0,
		MemPtr:           0,
	}

	if s.CPU.IFF1 {
		regs.IFF1 = 1
	}
	if s.CPU.IFF2 {
		regs.IFF2 = 1
	}

	// Write registers data
	if err := binary.Write(file, binary.LittleEndian, regs); err != nil {
		return err
	}

	return nil
}

// saveSZXSpecRegs saves Spectrum-specific registers as SZX block
func (s *Snapshot) saveSZXSpecRegs(file io.Writer) error {
	// Write block header
	blockHeader := szxBlockHeader{
		ID:   [4]byte{'S', 'P', 'C', 'R'},
		Size: 8, // Size of Spectrum registers structure
	}
	if err := binary.Write(file, binary.LittleEndian, blockHeader); err != nil {
		return err
	}

	// Create Spectrum registers structure
	regs := szxSpecRegs{
		Border:   s.CPU.BorderColor,
		Port7FFD: s.Memory.Port7FFD,
		Port1FFD: 0,                 // Not used in our emulator
		PortFE:   s.CPU.BorderColor, // Same as border
	}

	// Write registers data
	if err := binary.Write(file, binary.LittleEndian, regs); err != nil {
		return err
	}

	return nil
}

// saveSZXRamPage saves a RAM page as SZX block
func (s *Snapshot) saveSZXRamPage(file io.Writer, bankNum int) error {
	// Compress the RAM data
	var compressedData bytes.Buffer
	writer := zlib.NewWriter(&compressedData)
	if _, err := writer.Write(s.Memory.RAM[bankNum]); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	compressed := compressedData.Bytes()
	blockSize := uint32(3 + len(compressed)) // 3 bytes header + compressed data

	// Write block header
	blockHeader := szxBlockHeader{
		ID:   [4]byte{'R', 'A', 'M', 'P'},
		Size: blockSize,
	}
	if err := binary.Write(file, binary.LittleEndian, blockHeader); err != nil {
		return err
	}

	// SZX page numbers map directly to internal bank numbers
	pageNum := byte(bankNum)

	// Create RAM page header
	ramPage := szxRamPage{
		Flags:      0x01, // Compressed
		PageNumber: pageNum,
	}

	// Write RAM page header
	if err := binary.Write(file, binary.LittleEndian, ramPage); err != nil {
		return err
	}

	// Write compressed data
	if _, err := file.Write(compressed); err != nil {
		return err
	}

	return nil
}
