// Package layer2 implements the Spectrum Next's Layer 2 framebuffer.
//
// Layer 2 is a linear, byte-per-pixel framebuffer that lives in
// three consecutive 16 KB RAM banks. In the 256x192 mode (the only
// mode Sprint 6 ships), each row is 256 bytes and the buffer is
// 49152 bytes total — exactly three banks.
//
// NextReg 0x12 selects the bank that holds the FIRST 16K of the
// active framebuffer; the two banks above it complete the image.
// NextReg 0x13 does the same for the "shadow" framebuffer used by
// dual-buffer software. Sprint 6 wires both registers via
// pkg/next.WireLayer2 but the renderer only consults the active
// frame.
//
// The 320x256 and 640x256 modes — which use column-major memory
// layouts and 4bpp packing respectively — are deferred to a later
// pass; the underlying machinery (palette, compositor) is the same
// so they're additive, not a rewrite.
package layer2

// Width and Height fix the dimensions of the only mode supported
// here.
const (
	Width  = 256
	Height = 192
)

// BankReader is the minimal interface Layer 2 needs from the
// memory bus: read a 16 KB RAM bank by index. pkg/memory.Memory
// satisfies this through its existing GetPage method.
type BankReader interface {
	GetPage(bank int) []byte
}

// Layer2 holds the per-layer state: which RAM bank starts the
// active framebuffer, the shadow bank, and a memory reference for
// fetching pixel data.
type Layer2 struct {
	mem        BankReader
	activeBank byte
	shadowBank byte
	enabled    bool
	// resolution mirrors NR$70 bits 5:4: 0 = 256×192 (row-major 8bpp),
	// 1 = 320×256 (column-major 8bpp), 2 = 640×256 (column-major 4bpp).
	resolution byte
	// paletteOffset mirrors NR$70 bits 3:0 — added (mod 16) to the high
	// nibble of every Layer 2 pixel index (layer2.vhd:203). 0 = identity.
	paletteOffset byte
}

// New constructs a Layer 2 reader backed by the given memory bus.
// Disabled by default — guest code (or test code) flips it on
// when ready.
func New(mem BankReader) *Layer2 {
	return &Layer2{mem: mem}
}

// SetActiveBank installs the RAM bank that holds the first 16 KB
// of the active framebuffer. Only bits 6-0 of v are kept — bit 7
// is reserved on real hardware.
func (l *Layer2) SetActiveBank(v byte) { l.activeBank = v & 0x7F }

// ActiveBank returns the currently-installed active bank index.
func (l *Layer2) ActiveBank() byte { return l.activeBank }

// SetShadowBank installs the shadow framebuffer's starting bank.
func (l *Layer2) SetShadowBank(v byte) { l.shadowBank = v & 0x7F }

// ShadowBank returns the shadow bank index.
func (l *Layer2) ShadowBank() byte { return l.shadowBank }

// SetEnabled toggles Layer 2 rendering. When disabled,
// RenderScanline fills dst with the configured transparency
// colour (Sprint 6 uses 0; transparency-colour plumbing is a
// later sprint).
func (l *Layer2) SetEnabled(on bool) { l.enabled = on }

// Enabled reports whether Layer 2 is rendering.
func (l *Layer2) Enabled() bool { return l.enabled }

// SetResolution installs the NR$70 resolution (0 = 256×192, 1 = 320×256,
// 2 = 640×256). Higher bits are ignored.
func (l *Layer2) SetResolution(v byte) { l.resolution = v & 0x03 }

// Resolution returns the current resolution selector (0/1/2).
func (l *Layer2) Resolution() byte { return l.resolution }

// SetPaletteOffset installs the NR$70 palette offset (bits 3:0).
func (l *Layer2) SetPaletteOffset(v byte) { l.paletteOffset = v & 0x0F }

// PaletteOffset returns the current NR$70 palette offset.
func (l *Layer2) PaletteOffset() byte { return l.paletteOffset }

// applyOffset adds the palette offset (mod 16) to the high nibble of an
// 8-bit pixel index, leaving the low nibble unchanged — the FPGA's
// layer2.vhd:203 `(pixel(7:4)+offset) & pixel(3:0)`. offset 0 is identity.
func (l *Layer2) applyOffset(b byte) byte {
	if l.paletteOffset == 0 {
		return b
	}
	return (((b>>4)+l.paletteOffset)&0x0F)<<4 | (b & 0x0F)
}

// LineWidth returns the active framebuffer width in pixels for the
// current resolution (256, 320 or 640).
func (l *Layer2) LineWidth() int {
	switch l.resolution {
	case 1:
		return 320
	case 2:
		return 640
	default:
		return Width
	}
}

// LineHeight returns the active framebuffer height (192 for 256×192,
// else 256).
func (l *Layer2) LineHeight() int {
	if l.resolution == 0 {
		return Height
	}
	return 256
}

// RenderScanline writes one row (Width bytes) of palette-indexed
// pixels from the active framebuffer to dst. dst must have at
// least Width bytes; extra bytes are left untouched.
//
// Layout: y rows of 256 bytes each. Since 16384 / 256 = 64, a
// 256-byte row is always entirely within one bank — banks N,
// N+1, N+2 contain rows 0..63, 64..127, 128..191 respectively.
func (l *Layer2) RenderScanline(y int, dst []byte) {
	w := l.LineWidth()
	if y < 0 || y >= l.LineHeight() || len(dst) < w {
		return
	}
	if !l.enabled {
		// Production callers (the compositor) short-circuit BEFORE
		// reading our scanline when we're disabled, so this fill
		// is normally unreachable. We zero dst anyway for the
		// occasional direct caller (tests, debug overlays) so it
		// doesn't see stale bytes.
		for i := 0; i < w; i++ {
			dst[i] = 0
		}
		return
	}
	if l.resolution == 1 {
		l.renderColumnMajor(y, dst, 320)
		return
	}
	if l.resolution == 2 {
		l.renderColumnMajor4bpp(y, dst)
		return
	}
	bankNum := int(l.activeBank) + y/64
	bankOff := (y % 64) * Width
	page := l.mem.GetPage(bankNum)
	if page == nil || len(page) < bankOff+Width {
		// Out-of-range or short bank — treat as transparent.
		for i := 0; i < Width; i++ {
			dst[i] = 0
		}
		return
	}
	if l.paletteOffset == 0 {
		copy(dst[:Width], page[bankOff:bankOff+Width])
		return
	}
	for i := 0; i < Width; i++ {
		dst[i] = l.applyOffset(page[bankOff+i])
	}
}

// renderColumnMajor renders one row of a column-major 8bpp framebuffer
// (320×256 / 640×256). Per the Next layout, the framebuffer is stored
// column-by-column: byte(x,y) = activeBank*16K + x*256 + y. A row reads
// bytes at stride 256, crossing a bank boundary every 64 columns.
func (l *Layer2) renderColumnMajor(y int, dst []byte, w int) {
	curBank := -1
	var page []byte
	base := int(l.activeBank) * 0x4000
	for x := 0; x < w; x++ {
		globalOff := base + x*256 + y
		bank := globalOff / 0x4000
		if bank != curBank {
			curBank = bank
			page = l.mem.GetPage(bank)
		}
		off := globalOff % 0x4000
		if page == nil || off >= len(page) {
			dst[x] = 0
			continue
		}
		dst[x] = l.applyOffset(page[off])
	}
}

// renderColumnMajor4bpp renders one row of the 640×256 4bpp column-major
// framebuffer. It is stored as 320 byte-columns (640 pixels / 2 per byte);
// byte(c,y) = activeBank*16K + c*256 + y, and each byte packs two 4-bit
// pixels: the high nibble is the left pixel, the low nibble the right.
// dst receives 640 palette indices (0..15); the L2 palette applies any
// sub-palette offset downstream.
func (l *Layer2) renderColumnMajor4bpp(y int, dst []byte) {
	curBank := -1
	var page []byte
	base := int(l.activeBank) * 0x4000
	for c := 0; c < 320; c++ {
		globalOff := base + c*256 + y
		bank := globalOff / 0x4000
		if bank != curBank {
			curBank = bank
			page = l.mem.GetPage(bank)
		}
		off := globalOff % 0x4000
		var b byte
		if page != nil && off < len(page) {
			b = page[off]
		}
		dst[2*c] = l.applyOffset(b >> 4)
		dst[2*c+1] = l.applyOffset(b & 0x0F)
	}
}
