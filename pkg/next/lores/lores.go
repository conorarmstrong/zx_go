// Package lores implements the ZX Spectrum Next LoRes / Radastan video
// layer, a hardware-faithful port of the FPGA module
// cores/zxnext/src/video/lores.vhd.
//
// LoRes is a low-resolution variant of the ULA layer:
//
//   - LoRes (mode 0): 128x96, 8-bit colour. Each byte in bank 5 is one
//     palette index; the picture is pixel-doubled to 256x192. Top half lives
//     at bank-5 offset $0000-$17FF, bottom half at $2000-$37FF.
//   - Radastan (mode 1): 128x96, 4-bit colour (the original zxuno mode). Two
//     pixels per byte (hi nibble = even x, lo nibble = odd x). The high nibble
//     of the resulting index comes from the palette offset (or, with ULA+
//     enabled, "11" concatenated with the low two bits of the offset). The
//     display file ($4000 / $6000 region) is selected by the Timex dfile bit.
//
// The module is purely combinational on the FPGA: given a raster coordinate
// (hc, vc), the scroll offsets and the byte read from bank 5 at the address it
// computes, it produces a bank-5 address, an 8-bit palette index and a clip
// enable. This package mirrors that exactly (Pixel), then adds a scanline
// renderer (RenderScanline) that performs the bank-5 fetch the FPGA's VRAM
// arbiter does. Verified against the FPGA golden in fpga_golden_test.go.
package lores

// BankReader is the minimum the LoRes layer needs from the memory bus: fetch
// one 16 KB RAM bank by index. The FPGA reads the LoRes display from bank 5
// (the same RAM that backs the classic ULA screen). pkg/memory.Memory's
// GetPage satisfies this — the same contract as the tilemap / layer2 layers.
type BankReader interface {
	GetPage(bank int) []byte
}

// Config holds the per-frame LoRes register state, mirroring the lores.vhd
// input ports that come from NextRegs (lores.vhd:37-54):
//
//	Radastan      = NR$6A bit 5            -> mode_i  (false=lores, true=radastan)
//	Dfile         = Timex display file     -> dfile_i (radastan display-file select)
//	ULAPlus       = ULA+ enabled & !ULAnext-> ulap_en_i
//	PaletteOffset = NR$6A bits 3:0         -> lores_palette_offset_i
//	ScrollX       = NR$32                  -> scroll_x_i
//	ScrollY       = NR$33                  -> scroll_y_i
//	ClipX1/2/Y1/2 = NR$1A ULA clip window  -> clip_x{1,2}_i / clip_y{1,2}_i
type Config struct {
	Radastan      bool
	Dfile         bool
	ULAPlus       bool
	PaletteOffset uint8 // 4 bits
	ScrollX       uint8
	ScrollY       uint8
	ClipX1        uint8
	ClipX2        uint8
	ClipY1        uint8
	ClipY2        uint8
}

// Pixel reproduces lores.vhd verbatim: from the raster coordinate (hc, vc), the
// config and the byte read from bank 5 at the returned addr, it computes the
// bank-5 address to fetch (addr), the 8-bit palette index (pixel) and the clip
// enable (enabled). hc/vc are 9-bit (0..511). data is the byte at bank-5 offset
// addr — note addr depends only on (hc, vc, config), NOT on data, so callers
// resolve addr first, fetch, then call again for the pixel (or use
// RenderScanline, which does both). addr is a 14-bit bank-5 offset (0..16383).
//
// VHDL reference: cores/zxnext/src/video/lores.vhd:82-115.
func (c Config) Pixel(hc, vc uint16, data uint8) (addr uint16, pixel uint8, enabled bool) {
	addr = c.address(hc, vc)
	pixel = c.pixelValue(hc, vc, data)
	enabled = c.clip(hc, vc)
	return
}

// Address computes only the bank-5 offset to fetch for raster (hc, vc) — the
// first half of Pixel. Used by RenderScanline to resolve the byte to read.
//
// VHDL reference: lores.vhd:82-98.
func (c Config) Address(hc, vc uint16) uint16 { return c.address(hc, vc) }

// address: lores.vhd:82-98.
func (c Config) address(hc, vc uint16) uint16 {
	// x <= hc_i(7 downto 0) + scroll_x_i;            (lores.vhd:82)
	x := uint8(hc) + c.ScrollX
	// y_pre <= vc_i + ('0' & scroll_y_i);            (lores.vhd:84) — 9-bit add
	yPre := (vc + uint16(c.ScrollY)) & 0x1FF
	// y(7:6) <= (y_pre(7:6)+1) when y_pre>=192 else y_pre(7:6); y(5:0)=y_pre(5:0)
	// y is 8 bits, so the +1 on bits 7:6 wraps mod 4.                (lores.vhd:86-87)
	var yHi uint8
	if yPre >= 192 {
		yHi = (uint8(yPre>>6) + 1) & 0x03
	} else {
		yHi = uint8(yPre>>6) & 0x03
	}
	y := (yHi << 6) | uint8(yPre&0x3F)

	if c.Radastan {
		// lores_addr_rad <= dfile_i & y(7:1) & x(7:2);          (lores.vhd:96)
		// = dfile<<13 | (y>>1)<<6 | (x>>2)
		addr := (uint16(y>>1) << 6) | uint16(x>>2)
		if c.Dfile {
			addr |= 1 << 13
		}
		return addr & 0x3FFF
	}

	// lores_addr_pre <= y(7:1) & x(7:1);                       (lores.vhd:91)
	pre := (uint16(y>>1) << 7) | uint16(x>>1)
	// lores_addr(13:11) <= (pre(13:11)+1) when y>=96 else pre(13:11);
	// lores_addr(10:0)  <= pre(10:0);                          (lores.vhd:93-94)
	if y >= 96 {
		hi3 := (uint16(pre>>11) + 1) & 0x07
		pre = (hi3 << 11) | (pre & 0x7FF)
	}
	return pre & 0x3FFF
}

// pixelValue: lores.vhd:102-111.
func (c Config) pixelValue(hc, vc uint16, data uint8) uint8 {
	x := uint8(hc) + c.ScrollX
	if c.Radastan {
		// pixel_rad_nib_L <= data(7:4) when x(1)='0' else data(3:0); (lores.vhd:106)
		var nibL uint8
		if x&0x02 == 0 {
			nibL = data >> 4
		} else {
			nibL = data & 0x0F
		}
		// pixel_rad_nib_H <= palette_offset when ulap='0'
		//                    else ("11" & palette_offset(1:0));       (lores.vhd:107)
		var nibH uint8
		if c.ULAPlus {
			nibH = 0x0C | (c.PaletteOffset & 0x03)
		} else {
			nibH = c.PaletteOffset & 0x0F
		}
		// lores_pixel_o <= pixel_rad_nib_H & pixel_rad_nib_L;         (lores.vhd:111)
		return (nibH << 4) | (nibL & 0x0F)
	}
	// pixel_lores_nib_H <= data(7:4) + palette_offset;               (lores.vhd:102)
	// lores_pixel_o <= pixel_lores_nib_H & data(3:0);                (lores.vhd:111)
	nibH := (data>>4 + c.PaletteOffset) & 0x0F
	return (nibH << 4) | (data & 0x0F)
}

// clip: lores.vhd:115.
// lores_pixel_en_o <= '1' when hc in [clip_x1,clip_x2] and vc in [clip_y1,clip_y2].
func (c Config) clip(hc, vc uint16) bool {
	return hc >= uint16(c.ClipX1) && hc <= uint16(c.ClipX2) &&
		vc >= uint16(c.ClipY1) && vc <= uint16(c.ClipY2)
}

// RenderScanline renders one raster line of the LoRes/Radastan layer into dst
// (one byte = one 8-bit palette index per output pixel). vc is the raster Y
// (0..511); width is the number of horizontal raster positions to emit
// (typically the active 256, starting at hc=0). For each x position it
// resolves the bank-5 address, reads the byte from bank 5 via mem, and writes
// the palette index. Positions the clip window rejects are left untouched
// (the caller pre-fills transparent / lower-layer content), matching how the
// FPGA gates the layer off (lores_pixel_en_o = '0').
//
// This performs the bank-5 fetch the FPGA VRAM arbiter does
// (zxnext.vhd:4266-4267: lores_addr -> bank 5 -> lores_data_i).
func (c Config) RenderScanline(mem BankReader, vc int, dst []byte, width int) {
	bank5 := mem.GetPage(5)
	for hx := 0; hx < width; hx++ {
		hc := uint16(hx)
		vcu := uint16(vc)
		if !c.clip(hc, vcu) {
			continue
		}
		addr := c.address(hc, vcu)
		var data uint8
		if int(addr) < len(bank5) {
			data = bank5[addr]
		}
		dst[hx] = c.pixelValue(hc, vcu, data)
	}
}
