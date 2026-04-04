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
	SNA48K_SIZE = 49179
	SNA128K_SIZE = 131103
)

// loadSNA loads a .SNA format snapshot
func (s *Snapshot) loadSNA(file io.Reader) error {
	// Read the fixed header (27 bytes for CPU state)
	header := make([]byte, 27)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("failed to read SNA header: %w", err)
	}

	// Parse CPU state from header
	s.CPU.I = header[0]
	s.CPU.H_ = header[1]
	s.CPU.L_ = header[2]
	s.CPU.D_ = header[3]
	s.CPU.E_ = header[4]
	s.CPU.B_ = header[5]
	s.CPU.C_ = header[6]
	s.CPU.F_ = header[7]
	s.CPU.A_ = header[8]
	s.CPU.H = header[9]
	s.CPU.L = header[10]
	s.CPU.D = header[11]
	s.CPU.E = header[12]
	s.CPU.B = header[13]
	s.CPU.C = header[14]
	s.CPU.F = header[15]
	s.CPU.A = header[16]
	s.CPU.IY = binary.LittleEndian.Uint16(header[17:19])
	s.CPU.IX = binary.LittleEndian.Uint16(header[19:21])
	
	// IFF2 is stored, IFF1 is derived
	if header[21]&0x04 != 0 {
		s.CPU.IFF2 = true
		s.CPU.IFF1 = true // SNA assumes IFF1 = IFF2
	}
	
	s.CPU.R = header[22]
	s.CPU.F = header[23] // Flags again?
	s.CPU.A = header[24] // A again?
	s.CPU.SP = binary.LittleEndian.Uint16(header[25:27])
	
	// Interrupt mode (derived from header[21])
	s.CPU.IM = header[21] & 0x03
	
	// Border color (bits 1-3 of header[26])
	s.CPU.BorderColor = (header[26] >> 1) & 0x07

	// Read 48K of RAM data (banks 5, 2, 0 - the standard 48K layout)
	// Bank 5 (0x4000-0x7FFF)
	if _, err := io.ReadFull(file, s.Memory.RAM[5]); err != nil {
		return fmt.Errorf("failed to read RAM bank 5: %w", err)
	}
	// Bank 2 (0x8000-0xBFFF) 
	if _, err := io.ReadFull(file, s.Memory.RAM[2]); err != nil {
		return fmt.Errorf("failed to read RAM bank 2: %w", err)
	}
	// Bank 0 (0xC000-0xFFFF)
	if _, err := io.ReadFull(file, s.Memory.RAM[0]); err != nil {
		return fmt.Errorf("failed to read RAM bank 0: %w", err)
	}

	// Check if this is a 128K SNA file by trying to read more data
	extraHeader := make([]byte, 4)
	n, err := file.Read(extraHeader)
	if err != nil && err != io.EOF {
		return fmt.Errorf("error checking for 128K data: %w", err)
	}

	if n == 4 {
		// This is a 128K SNA file
		s.Memory.Is128K = true
		
		// Parse 128K specific data
		s.CPU.PC = binary.LittleEndian.Uint16(extraHeader[0:2])
		s.Memory.Port7FFD = extraHeader[2]
		// extraHeader[3] contains TR-DOS ROM paged flag (usually 0)

		// Read the remaining RAM banks (1, 3, 4, 6, 7)
		// The order depends on which page is currently mapped at 0xC000
		currentPage := int(s.Memory.Port7FFD & 0x07)
		
		// Read all remaining banks
		for bankNum := 0; bankNum < 8; bankNum++ {
			// Skip banks we already loaded (5, 2, and the current paged bank)
			if bankNum == 5 || bankNum == 2 || bankNum == currentPage {
				continue
			}
			
			if _, err := io.ReadFull(file, s.Memory.RAM[bankNum]); err != nil {
				return fmt.Errorf("failed to read RAM bank %d: %w", bankNum, err)
			}
		}
	} else {
		// This is a 48K SNA file
		s.Memory.Is128K = false
		// PC is stored on the stack, we need to pop it
		// For now, we'll set it to 0 and let the emulator handle it
		s.CPU.PC = 0
	}

	return nil
}

// saveSNA saves a snapshot in .SNA format
func (s *Snapshot) saveSNA(file io.Writer) error {
	// Create header (27 bytes)
	header := make([]byte, 27)
	
	header[0] = s.CPU.I
	header[1] = s.CPU.H_
	header[2] = s.CPU.L_
	header[3] = s.CPU.D_
	header[4] = s.CPU.E_
	header[5] = s.CPU.B_
	header[6] = s.CPU.C_
	header[7] = s.CPU.F_
	header[8] = s.CPU.A_
	header[9] = s.CPU.H
	header[10] = s.CPU.L
	header[11] = s.CPU.D
	header[12] = s.CPU.E
	header[13] = s.CPU.B
	header[14] = s.CPU.C
	header[15] = s.CPU.F
	header[16] = s.CPU.A
	binary.LittleEndian.PutUint16(header[17:19], s.CPU.IY)
	binary.LittleEndian.PutUint16(header[19:21], s.CPU.IX)
	
	// Interrupt state and mode
	if s.CPU.IFF2 {
		header[21] |= 0x04
	}
	header[21] |= s.CPU.IM & 0x03
	
	header[22] = s.CPU.R
	header[23] = s.CPU.F
	header[24] = s.CPU.A
	binary.LittleEndian.PutUint16(header[25:27], s.CPU.SP)

	// Write header
	if _, err := file.Write(header); err != nil {
		return fmt.Errorf("failed to write SNA header: %w", err)
	}

	// Write 48K of RAM (banks 5, 2, 0)
	if _, err := file.Write(s.Memory.RAM[5]); err != nil {
		return fmt.Errorf("failed to write RAM bank 5: %w", err)
	}
	if _, err := file.Write(s.Memory.RAM[2]); err != nil {
		return fmt.Errorf("failed to write RAM bank 2: %w", err)
	}
	if _, err := file.Write(s.Memory.RAM[0]); err != nil {
		return fmt.Errorf("failed to write RAM bank 0: %w", err)
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