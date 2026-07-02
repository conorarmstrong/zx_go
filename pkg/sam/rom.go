// Package sam implements the MGT SAM Coupé (1989): a Z80B at 6 MHz with a
// custom display/IO ASIC (four screen modes, 128-colour palette + 16-entry
// CLUT), 256/512K paged memory, an SAA1099 stereo sound chip and a WD1772
// floppy controller. It is a self-contained machine (like pkg/zx8x), reusing
// the shared Z80 core (pkg/z80) but with its own memory map, IO and video.
package sam

import "fmt"

const (
	// PageSize is the SAM memory page granularity (16 KB).
	PageSize = 0x4000
	// ROMSize is the SAM system ROM: 32 KB = ROM0 (low 16K) + ROM1 (high 16K).
	ROMSize = 2 * PageSize
	// ZX82HeaderLen is the optional "ZX82" wrapper header on some ROM images
	// (Andy Wright dumps); stripped before use.
	ZX82HeaderLen = 140
)

// SplitROM validates a raw SAM Coupé system-ROM image and splits it into ROM0
// (the low 16 KB, paged at $0000 on reset) and ROM1 (the high 16 KB, paged at
// $C000 when LMPR bit 6 is set). An optional 140-byte "ZX82" wrapper header is
// stripped first. The image is accepted when at least 32 KB remain and either
// it is exactly 32 KB or its first byte is DI (0xF3, the genuine reset entry).
func SplitROM(data []byte) (rom0, rom1 []byte, err error) {
	raw := data
	if len(raw) >= 4 && string(raw[:4]) == "ZX82" {
		if len(raw) < ZX82HeaderLen {
			return nil, nil, fmt.Errorf("sam: truncated ZX82 header (%d bytes)", len(raw))
		}
		raw = raw[ZX82HeaderLen:]
	}
	if len(raw) < ROMSize {
		return nil, nil, fmt.Errorf("sam: ROM image too small: %d bytes, need %d", len(raw), ROMSize)
	}
	if len(raw) != ROMSize && raw[0] != 0xF3 {
		return nil, nil, fmt.Errorf("sam: not a SAM ROM (size %d, first byte %#02x is not DI)", len(raw), raw[0])
	}
	rom0 = make([]byte, PageSize)
	rom1 = make([]byte, PageSize)
	copy(rom0, raw[:PageSize])
	copy(rom1, raw[PageSize:ROMSize])
	return rom0, rom1, nil
}
