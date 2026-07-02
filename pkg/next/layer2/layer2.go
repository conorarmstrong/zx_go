// Package layer2 implements the Spectrum Next's Layer 2 framebuffer.
//
// Layer 2 is a linear, byte-per-pixel framebuffer that lives in
// three consecutive 16 KB RAM banks. In the 256x192 mode, each row
// is 256 bytes and the buffer is 49152 bytes total — exactly three
// banks.
//
// NextReg 0x12 selects the bank that holds the FIRST 16K of the
// active framebuffer; the two banks above it complete the image.
// NextReg 0x13 does the same for the "shadow" framebuffer used by
// dual-buffer software; both registers are wired via
// pkg/next.WireLayer2 but the renderer only consults the active
// frame.
//
// The 320x256 and 640x256 modes use column-major memory layouts and
// 4bpp packing respectively.
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
	// scrollX is the 9-bit Layer 2 X scroll: NR$71 bit0 (MSB) || NR$16.
	// scrollY is the 8-bit Layer 2 Y scroll (NR$17). Both feed the FPGA
	// address generator (layer2.vhd:152, :156).
	scrollX uint16
	scrollY byte
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

// SetEnabled toggles Layer 2 rendering. When disabled, RenderScanline
// fills dst with index 0.
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

// SetScrollX installs the 9-bit Layer 2 X scroll (NR$71 bit0 || NR$16). Only
// the low 9 bits are kept. Feeds the FPGA address generator (layer2.vhd:152).
func (l *Layer2) SetScrollX(v uint16) { l.scrollX = v & 0x1FF }

// ScrollX returns the current 9-bit X scroll.
func (l *Layer2) ScrollX() uint16 { return l.scrollX }

// SetScrollY installs the 8-bit Layer 2 Y scroll (NR$17, layer2.vhd:156).
func (l *Layer2) SetScrollY(v byte) { l.scrollY = v }

// ScrollY returns the current 8-bit Y scroll.
func (l *Layer2) ScrollY() byte { return l.scrollY }

// fpgaFrameAddr is the faithful port of the Layer 2 framebuffer-address
// generator (video/layer2.vhd:145-167). Given the effective raster coordinate
// (hcEff = displayed screen column, vcEff = displayed row — the FPGA generates
// the address one pixel ahead but the pipeline delay realigns it to the
// displayed pixel, so the displayed column maps to hc_eff directly), it returns
// the 17-bit framebuffer byte offset (layer2_addr) and the in-window/on-screen
// validity (hc_valid AND vc_valid, layer2.vhd:164-165). Scroll is applied per
// :152/:156 with the 320-column / 192-row high-bit wraps (:153/:157).
//
// hcEff/vcEff are the i_phc/i_pvc (res 00) or i_whc/i_wvc (wide) values + 1
// already folded in by the caller; here they ARE hc_eff/vc_eff.
func (l *Layer2) fpgaFrameAddr(hcEff, vcEff int) (addr int, valid bool) {
	wide := l.resolution != 0

	// x_pre = hc_eff + scroll_x (10-bit); layer2.vhd:152.
	xPre := (hcEff + int(l.scrollX)) & 0x3FF
	// x(8:6); :153. Keep x_pre(8:6) when wide_res=0 OR (the 320-column
	// in-range condition); else x_pre(8:6)+"011" (3-bit wrap).
	xHi := (xPre >> 6) & 0x7
	bit9 := (xPre >> 9) & 1
	xBit8 := (xPre >> 8) & 1
	xB76 := (xPre >> 6) & 3
	keepXHi := !wide || (bit9 == 0 && (xBit8 == 0 || xB76 == 0))
	if !keepXHi {
		xHi = (xHi + 3) & 0x7 // +"011", 3-bit truncation
	}
	x := (xHi << 6) | (xPre & 0x3F) // 9-bit

	// y_pre = vc_eff + scroll_y (9-bit); layer2.vhd:156.
	yPre := (vcEff + int(l.scrollY)) & 0x1FF
	// y(7:6); :157. Keep y_pre(7:6) when wide_res=1 OR (the 192-row in-range
	// condition); else y_pre(7:6)+1 (2-bit wrap).
	yHi := (yPre >> 6) & 0x3
	yBit8 := (yPre >> 8) & 1
	yB76 := (yPre >> 6) & 3
	keepYHi := wide || (yBit8 == 0 && yB76 != 3)
	if !keepYHi {
		yHi = (yHi + 1) & 0x3 // +1, 2-bit truncation
	}
	y := (yHi << 6) | (yPre & 0x3F) // 8-bit

	// layer2_addr; :160.
	if !wide {
		addr = ((y & 0xFF) << 8) | (x & 0xFF) // '0' & y & x(7:0)
	} else {
		addr = ((x & 0x1FF) << 8) | (y & 0xFF) // x & y
	}

	// hc_valid / vc_valid; :164-165.
	hcEff9 := hcEff & 0x1FF
	hcBit8 := (hcEff9 >> 8) & 1
	hcB76 := (hcEff9 >> 6) & 3
	var hcValid bool
	if wide {
		hcValid = hcBit8 == 0 || hcB76 == 0
	} else {
		hcValid = hcBit8 == 0
	}
	vcEff9 := vcEff & 0x1FF
	vcBit8 := (vcEff9 >> 8) & 1
	vcB76 := (vcEff9 >> 6) & 3
	var vcValid bool
	if wide {
		vcValid = vcBit8 == 0
	} else {
		vcValid = vcBit8 == 0 && vcB76 != 3
	}
	return addr, hcValid && vcValid
}

// fpgaSramAddr ports the full SRAM-effective address path
// (video/layer2.vhd:172-175): the bank-effective mapping that places the
// framebuffer in the 128K ZX-RAM window and the A21 on-screen guard. Given the
// EFFECTIVE coordinate (hcEff = displayed screen column, vcEff = displayed row)
// it returns the 21-bit SRAM byte address and the final per-pixel enable (clip
// defaults to the full screen, so enable = on-screen validity AND A21=0).
func (l *Layer2) fpgaSramAddr(hcEff, vcEff int) (addr int, enabled bool) {
	addr16, valid := l.fpgaFrameAddr(hcEff, vcEff)

	// layer2_bank_eff = (('0' & bank(6:4)) + 1) & bank(3:0); :172.
	bank := int(l.activeBank) & 0x7F
	bankEff := (((bank >> 4) + 1) << 4) | (bank & 0x0F) // 8-bit
	// layer2_addr_eff = (bank_eff + addr(16:14)) & addr(13:0); :173. 22-bit.
	addrEff := (((bankEff + ((addr16 >> 14) & 0x7)) & 0xFF) << 14) | (addr16 & 0x3FFF)
	a21 := (addrEff >> 21) & 1
	return addrEff & 0x1FFFFF, valid && a21 == 0
}

// fpgaEffectiveAddr is fpgaSramAddr driven by the RAW raster counter, forming
// hc_eff = hc+1 internally (the FPGA generates the address one pixel ahead;
// layer2.vhd:148). The golden vectors are captured against the raw counters, so
// the replay uses this; RenderScanline uses fpgaSramAddr directly with the
// displayed column (the +1 and the 1-pixel pipeline delay cancel so a displayed
// column maps to its own framebuffer coordinate).
func (l *Layer2) fpgaEffectiveAddr(hc, vc int) (addr int, enabled bool) {
	return l.fpgaSramAddr(hc+1, vc)
}

// fpgaPixel ports the Layer 2 pixel path (video/layer2.vhd:202-203): selects the
// 4bpp nibble in hi-res (res 10) — high nibble when sc1=false, low when true —
// then adds the palette offset to the high nibble of the resulting index. In
// 8bpp (res 00/01) the byte passes through unchanged before the offset add and
// sc1 is ignored (layer2_hires_qq tracks resolution bit 1).
func (l *Layer2) fpgaPixel(data byte, sc1 bool) byte {
	pre := data
	if l.resolution&0x02 != 0 { // hi-res 4bpp (res 10)
		if sc1 {
			pre = data & 0x0F
		} else {
			pre = data >> 4
		}
	}
	return (((pre>>4)+l.paletteOffset)&0x0F)<<4 | (pre & 0x0F)
}

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

// RenderScanline writes one row (LineWidth bytes) of palette-indexed pixels
// from the active framebuffer to dst, applying the FPGA's scroll + wrap +
// per-pixel palette offset (video/layer2.vhd). dst must have at least LineWidth
// bytes; extra bytes are left untouched. Off-screen pixels (the FPGA's
// hc_valid/vc_valid wrap regions) and out-of-range banks render as transparent
// (index 0).
//
// Memory mapping: the FPGA's framebuffer offset addr16 = y*256 + x (256 mode)
// or x*256 + y (wide) folds onto our paged RAM as bank = activeBank +
// addr16/16K, off = addr16 % 16K — banks N, N+1, N+2 hold rows 0..63, 64..127,
// 128..191 in 256 mode. fpgaFrameAddr/fpgaSramAddr supply the exact coordinate
// math including scroll (NR$16/$17/$71) and the 192-row / 320-column wraps.
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

	// Fast path: 256 mode, no scroll, identity offset — the original
	// row-major bank copy. Behaviourally identical to the faithful path
	// below for this common case, but avoids the per-pixel address math.
	if l.resolution == 0 && l.scrollX == 0 && l.scrollY == 0 && y < Height {
		bankNum := int(l.activeBank) + y/64
		bankOff := (y % 64) * Width
		page := l.mem.GetPage(bankNum)
		if page == nil || len(page) < bankOff+Width {
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
		return
	}

	// Faithful path: per displayed column, compute the FPGA framebuffer byte
	// and pixel. In 640 mode two pixels share one byte (high/low nibble).
	hires4 := l.resolution&0x02 != 0
	for x := 0; x < w; x++ {
		hc := x
		sc1 := false
		if hires4 {
			hc = x >> 1
			sc1 = x&1 == 1
		}
		// displayed column hc -> hc_eff = hc (the +1 generate-ahead and the
		// 1-pixel pipeline delay cancel for the displayed pixel).
		data, ok := l.fetchByte(hc, y)
		if !ok {
			dst[x] = 0
			continue
		}
		dst[x] = l.fpgaPixel(data, sc1)
	}
}

// fetchByte returns the framebuffer byte the FPGA fetches for displayed column
// hcEff, row vcEff (via fpgaSramAddr, then folded onto our paged RAM), and
// whether the pixel is on-screen/in-window (the FPGA enable). A nil or short
// bank reads as not-ok (transparent).
func (l *Layer2) fetchByte(hcEff, vcEff int) (byte, bool) {
	addr16, en := l.fpgaFrameAddr(hcEff, vcEff)
	if !en {
		return 0, false
	}
	bank := int(l.activeBank) + addr16/0x4000
	off := addr16 % 0x4000
	page := l.mem.GetPage(bank)
	if page == nil || off >= len(page) {
		return 0, false
	}
	return page[off], true
}
