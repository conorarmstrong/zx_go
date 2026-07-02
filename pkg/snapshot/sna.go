package snapshot

import (
	"encoding/binary"
	"fmt"
	"io"
)

// SNA file format structures
// 48K SNA format is 49179 bytes total
// 128K SNA format is 131103 bytes total

const (
	SNA48K_SIZE  = 49179
	SNA128K_SIZE = 131103
)

// loadSNA loads a .SNA format snapshot
func (s *Snapshot) loadSNA(file io.Reader) error {
	// Read the fixed header (27 bytes for CPU state)
	header := make([]byte, 27)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("failed to read SNA header: %w", err)
	}

	// Parse CPU state from header per official SNA specification.
	// Register pairs are little-endian (low byte first).
	s.CPU.I = header[0]
	s.CPU.L_ = header[1]
	s.CPU.H_ = header[2]
	s.CPU.E_ = header[3]
	s.CPU.D_ = header[4]
	s.CPU.C_ = header[5]
	s.CPU.B_ = header[6]
	s.CPU.F_ = header[7]
	s.CPU.A_ = header[8]
	s.CPU.L = header[9]
	s.CPU.H = header[10]
	s.CPU.E = header[11]
	s.CPU.D = header[12]
	s.CPU.C = header[13]
	s.CPU.B = header[14]
	s.CPU.IY = binary.LittleEndian.Uint16(header[15:17])
	s.CPU.IX = binary.LittleEndian.Uint16(header[17:19])

	// Byte 19: bit 2 = IFF2, bits 0-1 = part of IM (stored at byte 25)
	s.CPU.IFF2 = (header[19] & 0x04) != 0
	s.CPU.IFF1 = s.CPU.IFF2 // SNA assumes IFF1 = IFF2

	s.CPU.R = header[20]
	s.CPU.F = header[21]
	s.CPU.A = header[22]
	s.CPU.SP = binary.LittleEndian.Uint16(header[23:25])
	s.CPU.IM = header[25] & 0x03
	s.CPU.BorderColor = header[26] & 0x07

	// Read the 48K dump (per the SNA format spec): page 5 at 0x4000,
	// page 2 at 0x8000, and the page currently mapped at 0xC000. On a
	// 48K machine the 0xC000 page is always physical bank 0. On a 128K
	// machine it is whichever bank port 0x7FFD selected — but the 7FFD
	// value is stored in the extra header that follows the 48K dump, so
	// we can't know which bank this third page belongs to until later.
	// Buffer it and place it once the 7FFD byte is read; loading it
	// straight into bank 0 would (a) lose the paged-in bank's data and
	// (b) leave bank 0 to be overwritten by the remaining-pages loop.
	// Bank 5 (0x4000-0x7FFF)
	if _, err := io.ReadFull(file, s.Memory.RAM[5]); err != nil {
		return fmt.Errorf("failed to read RAM bank 5: %w", err)
	}
	// Bank 2 (0x8000-0xBFFF)
	if _, err := io.ReadFull(file, s.Memory.RAM[2]); err != nil {
		return fmt.Errorf("failed to read RAM bank 2: %w", err)
	}
	// The 0xC000 page — buffered until we know which bank it is.
	pagedPage := make([]byte, 16384)
	if _, err := io.ReadFull(file, pagedPage); err != nil {
		return fmt.Errorf("failed to read RAM paged page: %w", err)
	}

	// Check if this is a 128K SNA file by trying to read the 4-byte
	// PC/port7FFD/TR-DOS trailer that follows the 48K dump. io.ReadFull
	// distinguishes a clean 48K file (zero bytes left, io.EOF) from a
	// 128K file cut short mid-trailer (1-3 bytes read,
	// io.ErrUnexpectedEOF) — the latter must be reported as an error
	// rather than silently misread as a 48K snapshot.
	extraHeader := make([]byte, 4)
	_, err := io.ReadFull(file, extraHeader)
	switch err {
	case nil:
		// This is a 128K SNA file
		s.Memory.Is128K = true

		// Parse 128K specific data
		s.CPU.PC = binary.LittleEndian.Uint16(extraHeader[0:2])
		s.Memory.Port7FFD = extraHeader[2]
		// extraHeader[3] contains TR-DOS ROM paged flag (usually 0)

		// The 48K dump's 0xC000 page belongs to the bank port 0x7FFD
		// had selected. Place the buffered page into that bank now.
		currentPage := int(s.Memory.Port7FFD & 0x07)
		copy(s.Memory.RAM[currentPage], pagedPage)

		// Read the remaining RAM banks in ascending order, skipping the
		// pages already present in the 48K dump (5, 2 and the paged-in
		// page). Per the spec, when the paged page is itself 5 or 2 the
		// remaining set is all of 0,1,3,4,6,7 (the paged page is not
		// additionally subtracted), which falls out of this loop because
		// 5/2 are already excluded.
		for bankNum := 0; bankNum < 8; bankNum++ {
			if bankNum == 5 || bankNum == 2 || bankNum == currentPage {
				continue
			}

			if _, err := io.ReadFull(file, s.Memory.RAM[bankNum]); err != nil {
				return fmt.Errorf("failed to read RAM bank %d: %w", bankNum, err)
			}
		}
	case io.EOF:
		// 48K SNA: the 0xC000 page is physical bank 0.
		copy(s.Memory.RAM[0], pagedPage)
		// 48K SNA: PC is stored on the stack — pop it from RAM
		s.Memory.Is128K = false
		sp := s.CPU.SP
		lo := s.readRAM(sp)
		hi := s.readRAM(sp + 1)
		s.CPU.PC = uint16(hi)<<8 | uint16(lo)
		s.CPU.SP += 2
	default:
		return fmt.Errorf("error checking for 128K data: %w", err)
	}

	return nil
}

// readRAM reads a byte from the snapshot's 48K RAM address space (0x4000-0xFFFF)
func (s *Snapshot) readRAM(addr uint16) byte {
	switch {
	case addr >= 0xC000:
		return s.Memory.RAM[0][addr-0xC000]
	case addr >= 0x8000:
		return s.Memory.RAM[2][addr-0x8000]
	case addr >= 0x4000:
		return s.Memory.RAM[5][addr-0x4000]
	default:
		return 0 // ROM area
	}
}

// saveSNA saves a snapshot in .SNA format
func (s *Snapshot) saveSNA(file io.Writer) error {
	// Create header (27 bytes)
	header := make([]byte, 27)

	header[0] = s.CPU.I
	header[1] = s.CPU.L_
	header[2] = s.CPU.H_
	header[3] = s.CPU.E_
	header[4] = s.CPU.D_
	header[5] = s.CPU.C_
	header[6] = s.CPU.B_
	header[7] = s.CPU.F_
	header[8] = s.CPU.A_
	header[9] = s.CPU.L
	header[10] = s.CPU.H
	header[11] = s.CPU.E
	header[12] = s.CPU.D
	header[13] = s.CPU.C
	header[14] = s.CPU.B
	binary.LittleEndian.PutUint16(header[15:17], s.CPU.IY)
	binary.LittleEndian.PutUint16(header[17:19], s.CPU.IX)

	// Byte 19: bit 2 = IFF2
	if s.CPU.IFF2 {
		header[19] |= 0x04
	}

	header[20] = s.CPU.R
	header[21] = s.CPU.F
	header[22] = s.CPU.A
	binary.LittleEndian.PutUint16(header[23:25], s.CPU.SP)
	header[25] = s.CPU.IM & 0x03
	header[26] = s.CPU.BorderColor & 0x07

	// Write header
	if _, err := file.Write(header); err != nil {
		return fmt.Errorf("failed to write SNA header: %w", err)
	}

	// Write the 48K dump (per the SNA format spec): page 5 at 0x4000,
	// page 2 at 0x8000, then the page mapped at 0xC000. On 48K that is
	// physical bank 0; on 128K it is the bank port 0x7FFD selected.
	// Writing a hardcoded bank 0 here for a 128K snapshot would lose the
	// paged-in bank's contents and duplicate bank 0.
	pagedBank := 0
	if s.Memory.Is128K {
		pagedBank = int(s.Memory.Port7FFD & 0x07)
	}
	if _, err := file.Write(s.Memory.RAM[5]); err != nil {
		return fmt.Errorf("failed to write RAM bank 5: %w", err)
	}
	if _, err := file.Write(s.Memory.RAM[2]); err != nil {
		return fmt.Errorf("failed to write RAM bank 2: %w", err)
	}
	if _, err := file.Write(s.Memory.RAM[pagedBank]); err != nil {
		return fmt.Errorf("failed to write RAM paged bank %d: %w", pagedBank, err)
	}

	// If 128K, write additional data
	if s.Memory.Is128K {
		// Additional header for 128K
		extraHeader := make([]byte, 4)
		binary.LittleEndian.PutUint16(extraHeader[0:2], s.CPU.PC)
		extraHeader[2] = s.Memory.Port7FFD
		extraHeader[3] = 0 // TR-DOS ROM paged (usually 0)

		if _, err := file.Write(extraHeader); err != nil {
			return fmt.Errorf("failed to write 128K header: %w", err)
		}

		// Write remaining RAM banks
		currentPage := int(s.Memory.Port7FFD & 0x07)
		for bankNum := 0; bankNum < 8; bankNum++ {
			// Skip banks already written and current page
			if bankNum == 5 || bankNum == 2 || bankNum == currentPage {
				continue
			}

			if _, err := file.Write(s.Memory.RAM[bankNum]); err != nil {
				return fmt.Errorf("failed to write RAM bank %d: %w", bankNum, err)
			}
		}
	}

	return nil
}
