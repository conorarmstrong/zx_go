package testharness

import (
	"strings"
)

// Spectrum screen-memory layout. The bitmap area is 6 KB at 0x4000
// arranged in three 2 KB thirds (top, middle, bottom screen). Each
// third is 8 character rows × 32 characters × 8 bitmap bytes, but
// laid out so that scanlines are interleaved across rows for the
// CRT scanning hardware. To read a character cell at (col, row) we
// need to compute the address of each of its 8 scanline bytes
// individually using the ZX Spectrum address scheme.
//
// charScreenAddr returns the screen-memory address of the byte for
// scanline `line` (0..7) of the character cell at (col, row), where
// col is 0..31 and row is 0..23. The formula:
//
//	addr = 0x4000
//	     | ((row & 0x18) << 8)   // bits 5..6 of row → addr bits 11..12
//	     | ((line & 0x07) << 8)  // line within cell → addr bits 8..10
//	     | ((row & 0x07) << 5)   // bits 0..2 of row → addr bits 5..7
//	     | (col & 0x1F)          // col → addr bits 0..4
//
// See "ZX Spectrum screen layout" — Sinclair ULA addressing.
func charScreenAddr(col, row, line int) uint16 {
	return 0x4000 |
		uint16((row&0x18)<<8) |
		uint16((line&0x07)<<8) |
		uint16((row&0x07)<<5) |
		uint16(col&0x1F)
}

// readCharCell returns the 8 bitmap bytes for the character at
// (col, row) in screen memory. Each byte is one scanline; bit 7 is
// the leftmost pixel.
func (h *Harness) readCharCell(col, row int) [8]byte {
	var cell [8]byte
	for line := 0; line < 8; line++ {
		cell[line] = h.Memory(charScreenAddr(col, row, line))
	}
	return cell
}

// charSetAddr is the start of the Spectrum character set in the
// 48K ROM. The character set covers ASCII codes 32 (space) through
// 127, with each character represented as 8 bitmap bytes — 96
// characters × 8 bytes = 768 bytes total.
const charSetAddr = 0x3D00

// readROMChar returns the 8 bitmap bytes for ASCII code `c` in the
// 48K ROM character set. Codes outside 32..127 return an empty cell.
func (h *Harness) readROMChar(c byte) [8]byte {
	if c < 32 || c > 127 {
		return [8]byte{}
	}
	var cell [8]byte
	base := uint16(charSetAddr) + uint16(c-32)*8
	for i := 0; i < 8; i++ {
		cell[i] = h.Memory(base + uint16(i))
	}
	return cell
}

// matchChar compares an 8-byte character cell against the 96
// printable characters in the Spectrum ROM and returns the matching
// ASCII code. Returns ' ' (space) if nothing matches — the caller
// can decide whether unrecognised cells matter.
//
// The match is exact bitwise. The Spectrum supports inverse video
// (FLASH attribute), but the screen *bitmap* is not inverted by
// FLASH — only the colour attributes are. So a normal character and
// a flashing character look identical in the bitmap. Cells with
// graphics characters (codes 128..143) or user-defined graphics
// (UDGs at 144..164) won't match the printable ROM font and fall
// through to space.
func (h *Harness) matchChar(cell [8]byte) byte {
	// Empty cell → space, fast path.
	allZero := true
	for _, b := range cell {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return ' '
	}
	for c := byte(32); c <= 127; c++ {
		romCell := h.readROMChar(c)
		if romCell == cell {
			return c
		}
	}
	return ' '
}

// ScreenText returns the contents of the Spectrum text-mode screen
// as a 24-line string. Each line is 32 characters wide, padded
// with spaces. Lines are separated by newlines and trailing
// whitespace on each line is preserved (so the indices line up
// with character coordinates).
//
// The mapping uses the host ROM's character set at 0x3D00 — for a
// 48K Spectrum that's the standard Sinclair font. Cells that don't
// match a printable ROM character (graphics, UDGs, partial cell
// updates mid-frame) render as spaces.
//
// Use this for "verify the BASIC banner appeared" or "the CAT
// command produced this file list" assertions. For binary screen
// content (e.g. game graphics) use ScreenImage instead.
func (h *Harness) ScreenText() string {
	var b strings.Builder
	b.Grow(24*33 + 1)
	for row := 0; row < 24; row++ {
		for col := 0; col < 32; col++ {
			cell := h.readCharCell(col, row)
			b.WriteByte(h.matchChar(cell))
		}
		if row < 23 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// RunUntilText runs frames until ScreenText contains the given
// substring or maxFrames have elapsed. Returns the frame count at
// which the text appeared, or an error on timeout. Used by tests
// that need to wait for a specific BASIC output (the boot banner,
// a CAT result, etc.).
func (h *Harness) RunUntilText(want string, maxFrames int) (int, error) {
	for i := 0; i < maxFrames; i++ {
		if strings.Contains(h.ScreenText(), want) {
			return i, nil
		}
		h.RunFrames(1)
	}
	if strings.Contains(h.ScreenText(), want) {
		return maxFrames, nil
	}
	return maxFrames, &textTimeoutError{want: want, frames: maxFrames}
}

// textTimeoutError is returned by RunUntilText when the wanted
// substring doesn't appear in time. Includes the want string in
// the message so test failures are self-describing.
type textTimeoutError struct {
	want   string
	frames int
}

func (e *textTimeoutError) Error() string {
	return "testharness: text " + quote(e.want) + " not seen within " + itoa(e.frames) + " frames"
}

// quote and itoa are tiny helpers to avoid pulling fmt into a
// hot path that runs on every RunUntilText timeout. Worth
// nothing efficiency-wise, but consistent with the test-only
// scope of the package.
func quote(s string) string {
	return "\"" + s + "\""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
