package snapshot

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Z80 format constants
const (
	Z80_V1_HEADER_SIZE = 30
	Z80_V2_HEADER_SIZE = 55
	Z80_V3_HEADER_SIZE = 86
)

// Z80 hardware modes
const (
	Z80_HW_48K     = 0
	Z80_HW_48K_IF1 = 1
	Z80_HW_SAMRAM  = 2
	Z80_HW_128K    = 4
	Z80_HW_128K_IF1 = 5
	Z80_HW_PLUS2   = 12
	Z80_HW_PLUS2A  = 13
	Z80_HW_PLUS3   = 14
)

// loadZ80 loads a .Z80 format snapshot
func (s *Snapshot) loadZ80(file io.Reader) error {
	// Read the first 30 bytes (version 1 header)
	header := make([]byte, Z80_V1_HEADER_SIZE)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("failed to read Z80 header: %w", err)
	}

	// Parse basic CPU state
	s.CPU.A = header[0]
	s.CPU.F = header[1]
	s.CPU.C = header[2]
	s.CPU.B = header[3]
	s.CPU.L = header[4]
	s.CPU.H = header[5]
	
	// PC is at bytes 6-7 (little endian)
	pc := binary.LittleEndian.Uint16(header[6:8])
	
	s.CPU.SP = binary.LittleEndian.Uint16(header[8:10])
	s.CPU.I = header[10]
	s.CPU.R = header[11]
	
	// Flags byte at position 12
	flagsByte := header[12]
	if flagsByte == 255 {
		flagsByte = 1 // Default if not set
	}
	s.CPU.R = (s.CPU.R & 0x7F) | ((flagsByte & 0x01) << 7)
	s.CPU.BorderColor = (flagsByte >> 1) & 0x07
	
	// More CPU state
	s.CPU.E = header[13]
	s.CPU.D = header[14]
	s.CPU.C_ = header[15]
	s.CPU.B_ = header[16]
	s.CPU.E_ = header[17]
	s.CPU.D_ = header[18]
	s.CPU.L_ = header[19]
	s.CPU.H_ = header[20]
	s.CPU.A_ = header[21]
	s.CPU.F_ = header[22]
	
	s.CPU.IY = binary.LittleEndian.Uint16(header[23:25])
	s.CPU.IX = binary.LittleEndian.Uint16(header[25:27])
	
	// Interrupt state
	s.CPU.IFF1 = (header[27] != 0)
	s.CPU.IFF2 = (header[28] != 0)
	
	// Interrupt mode
	s.CPU.IM = header[29] & 0x03

	// Check if this is version 1 (PC != 0) or later version (PC == 0)
	if pc != 0 {
		// Version 1 format
		s.CPU.PC = pc
		s.Memory.Is128K = false
		
		// Read compressed memory data
		return s.readZ80V1Memory(file)
	} else {
		// Version 2 or 3 format - read additional header
		additionalHeaderSize := make([]byte, 2)
		if _, err := io.ReadFull(file, additionalHeaderSize); err != nil {
			return fmt.Errorf("failed to read additional header size: %w", err)
		}
		
		headerLen := binary.LittleEndian.Uint16(additionalHeaderSize)
		if headerLen < 23 {
			return fmt.Errorf("invalid additional header length: %d", headerLen)
		}
		
		// Read the rest of the header
		extHeader := make([]byte, headerLen)
		if _, err := io.ReadFull(file, extHeader); err != nil {
			return fmt.Errorf("failed to read extended header: %w", err)
		}
		
		// PC is in extended header
		s.CPU.PC = binary.LittleEndian.Uint16(extHeader[0:2])
		
		// Hardware mode
		hwMode := extHeader[2]
		
		// Set memory model based on hardware mode
		switch hwMode {
		case Z80_HW_48K, Z80_HW_48K_IF1, Z80_HW_SAMRAM:
			s.Memory.Is128K = false
		case Z80_HW_128K, Z80_HW_128K_IF1, Z80_HW_PLUS2, Z80_HW_PLUS2A, Z80_HW_PLUS3:
			s.Memory.Is128K = true
			// Port 0x7FFD value for 128K machines
			if len(extHeader) > 3 {
				s.Memory.Port7FFD = extHeader[3]
			}
		default:
			s.Memory.Is128K = false
		}
		
		// Read memory blocks
		return s.readZ80V2V3Memory(file)
	}
}

// readZ80V1Memory reads compressed memory data from Z80 version 1 format
func (s *Snapshot) readZ80V1Memory(file io.Reader) error {
	// Read all remaining data
	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read memory data: %w", err)
	}
	
	var memData []byte
	
	// Check if data is compressed (last 4 bytes should be 0x00, 0xED, 0xED, 0x00)
	if len(data) >= 4 {
		lastFour := data[len(data)-4:]
		if lastFour[0] == 0x00 && lastFour[1] == 0xED && lastFour[2] == 0xED && lastFour[3] == 0x00 {
			// Data is compressed - decompress it
			decompressed, err := s.decompressZ80(data[:len(data)-4])
			if err != nil {
				return fmt.Errorf("failed to decompress memory: %w", err)
			}
			memData = decompressed
		} else {
			// Data is not compressed
			memData = data
		}
	} else {
		memData = data
	}
	
	// For 48K, we expect exactly 49152 bytes (0x4000 - 0xFFFF)
	if len(memData) < 49152 {
		return fmt.Errorf("insufficient memory data: got %d bytes, expected 49152", len(memData))
	}
	
	// Load into RAM banks 5, 2, 0 (the 48K memory layout)
	copy(s.Memory.RAM[5], memData[0:16384])    // 0x4000-0x7FFF
	copy(s.Memory.RAM[2], memData[16384:32768]) // 0x8000-0xBFFF  
	copy(s.Memory.RAM[0], memData[32768:49152]) // 0xC000-0xFFFF
	
	return nil
}

// readZ80V2V3Memory reads memory blocks from Z80 version 2/3 format
func (s *Snapshot) readZ80V2V3Memory(file io.Reader) error {
	for {
		// Read block header (3 bytes)
		blockHeader := make([]byte, 3)
		n, err := file.Read(blockHeader)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read block header: %w", err)
		}
		if n != 3 {
			break // End of file
		}
		
		blockLen := binary.LittleEndian.Uint16(blockHeader[0:2])
		pageNum := blockHeader[2]
		
		// Read block data
		blockData := make([]byte, blockLen)
		if _, err := io.ReadFull(file, blockData); err != nil {
			return fmt.Errorf("failed to read block data: %w", err)
		}
		
		// Decompress if needed
		var memData []byte
		if blockLen == 0xFFFF {
			// Uncompressed 16KB block
			memData = blockData
		} else {
			// Compressed block
			decompressed, err := s.decompressZ80(blockData)
			if err != nil {
				return fmt.Errorf("failed to decompress block %d: %w", pageNum, err)
			}
			memData = decompressed
		}
		
		// Map page number to RAM bank
		var bankNum int
		switch pageNum {
		case 4: // 0x8000-0xBFFF (48K page 2)
			bankNum = 2
		case 5: // 0xC000-0xFFFF (48K page 0) 
			bankNum = 0
		case 8: // 0x4000-0x7FFF (48K page 5)
			bankNum = 5
		case 3: // 128K RAM bank 0
			bankNum = 0
		case 6: // 128K RAM bank 3
			bankNum = 3
		case 7: // 128K RAM bank 4
			bankNum = 4
		case 9: // 128K RAM bank 6
			bankNum = 6
		case 10: // 128K RAM bank 7
			bankNum = 7
		case 11: // 128K RAM bank 1
			bankNum = 1
		default:
			// Unknown page number, skip
			continue
		}
		
		// Copy data to appropriate RAM bank
		if len(memData) >= 16384 {
			copy(s.Memory.RAM[bankNum], memData[:16384])
		} else {
			copy(s.Memory.RAM[bankNum][:len(memData)], memData)
		}
	}
	
	return nil
}

// decompressZ80 decompresses Z80 RLE compressed data
func (s *Snapshot) decompressZ80(data []byte) ([]byte, error) {
	var result []byte
	i := 0
	
	for i < len(data) {
		if i+3 < len(data) && data[i] == 0xED && data[i+1] == 0xED {
			// RLE sequence: 0xED 0xED count value
			count := data[i+2]
			value := data[i+3]
			
			if count == 0 {
				// Special case: count of 0 means literal 0xED 0xED
				result = append(result, 0xED, 0xED)
				i += 4
			} else {
				// Repeat 'value' 'count' times
				for j := 0; j < int(count); j++ {
					result = append(result, value)
				}
				i += 4
			}
		} else {
			// Literal byte
			result = append(result, data[i])
			i++
		}
	}
	
	return result, nil
}

// compressZ80 compresses data using Z80 RLE compression
func (s *Snapshot) compressZ80(data []byte) []byte {
	var result []byte
	i := 0
	
	for i < len(data) {
		current := data[i]
		count := 1
		
		// Count consecutive identical bytes
		for i+count < len(data) && data[i+count] == current && count < 255 {
			count++
		}
		
		if count >= 5 || current == 0xED {
			// Use RLE for runs of 5+ bytes or any ED bytes
			result = append(result, 0xED, 0xED, byte(count), current)
		} else {
			// Output literally
			for j := 0; j < count; j++ {
				result = append(result, current)
			}
		}
		
		i += count
	}
	
	return result
}

// saveZ80 saves a snapshot in .Z80 format (version 3)
func (s *Snapshot) saveZ80(file io.Writer) error {
	// Create version 1 header (30 bytes)
	header := make([]byte, Z80_V1_HEADER_SIZE)
	
	header[0] = s.CPU.A
	header[1] = s.CPU.F
	header[2] = s.CPU.C
	header[3] = s.CPU.B
	header[4] = s.CPU.L
	header[5] = s.CPU.H
	
	// PC = 0 indicates version 2/3 format
	binary.LittleEndian.PutUint16(header[6:8], 0)
	
	binary.LittleEndian.PutUint16(header[8:10], s.CPU.SP)
	header[10] = s.CPU.I
	header[11] = s.CPU.R & 0x7F
	
	// Flags byte
	flagsByte := byte(0x01) // Bit 0 = bit 7 of R register
	if s.CPU.R&0x80 != 0 {
		flagsByte |= 0x01
	}
	flagsByte |= (s.CPU.BorderColor & 0x07) << 1
	header[12] = flagsByte
	
	header[13] = s.CPU.E
	header[14] = s.CPU.D
	header[15] = s.CPU.C_
	header[16] = s.CPU.B_
	header[17] = s.CPU.E_
	header[18] = s.CPU.D_
	header[19] = s.CPU.L_
	header[20] = s.CPU.H_
	header[21] = s.CPU.A_
	header[22] = s.CPU.F_
	
	binary.LittleEndian.PutUint16(header[23:25], s.CPU.IY)
	binary.LittleEndian.PutUint16(header[25:27], s.CPU.IX)
	
	if s.CPU.IFF1 {
		header[27] = 0xFF
	}
	if s.CPU.IFF2 {
		header[28] = 0xFF
	}
	
	header[29] = s.CPU.IM & 0x03
	
	// Write version 1 header
	if _, err := file.Write(header); err != nil {
		return fmt.Errorf("failed to write Z80 header: %w", err)
	}
	
	// Create extended header for version 3
	extHeaderLen := uint16(54) // Version 3 additional header length
	if err := binary.Write(file, binary.LittleEndian, extHeaderLen); err != nil {
		return fmt.Errorf("failed to write extended header length: %w", err)
	}
	
	extHeader := make([]byte, extHeaderLen)
	binary.LittleEndian.PutUint16(extHeader[0:2], s.CPU.PC)
	
	// Hardware mode
	if s.Memory.Is128K {
		extHeader[2] = Z80_HW_128K
		extHeader[3] = s.Memory.Port7FFD
	} else {
		extHeader[2] = Z80_HW_48K
	}
	
	// Write extended header
	if _, err := file.Write(extHeader); err != nil {
		return fmt.Errorf("failed to write extended header: %w", err)
	}
	
	// Write memory blocks
	if s.Memory.Is128K {
		// Write all 128K RAM banks
		banks := []int{0, 1, 2, 3, 4, 5, 6, 7}
		pageNums := []byte{3, 11, 4, 6, 7, 8, 9, 10}
		
		for i, bankNum := range banks {
			if err := s.writeZ80MemoryBlock(file, s.Memory.RAM[bankNum], pageNums[i]); err != nil {
				return fmt.Errorf("failed to write RAM bank %d: %w", bankNum, err)
			}
		}
	} else {
		// Write 48K memory blocks
		if err := s.writeZ80MemoryBlock(file, s.Memory.RAM[5], 8); err != nil { // 0x4000-0x7FFF
			return err
		}
		if err := s.writeZ80MemoryBlock(file, s.Memory.RAM[2], 4); err != nil { // 0x8000-0xBFFF
			return err
		}
		if err := s.writeZ80MemoryBlock(file, s.Memory.RAM[0], 5); err != nil { // 0xC000-0xFFFF
			return err
		}
	}
	
	return nil
}

// writeZ80MemoryBlock writes a compressed memory block in Z80 format
func (s *Snapshot) writeZ80MemoryBlock(file io.Writer, data []byte, pageNum byte) error {
	// Compress the data
	compressed := s.compressZ80(data)
	
	// Write block header
	blockLen := uint16(len(compressed))
	if err := binary.Write(file, binary.LittleEndian, blockLen); err != nil {
		return fmt.Errorf("failed to write block length: %w", err)
	}
	
	if err := binary.Write(file, binary.LittleEndian, pageNum); err != nil {
		return fmt.Errorf("failed to write page number: %w", err)
	}
	
	// Write compressed data
	if _, err := file.Write(compressed); err != nil {
		return fmt.Errorf("failed to write block data: %w", err)
	}
	
	return nil
}