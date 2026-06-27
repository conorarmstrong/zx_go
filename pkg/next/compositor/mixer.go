package compositor

// MixerInputs are the per-pixel inputs to the FPGA video mixer — the stage-2
// layer / SLU-priority block of zxnext.vhd (lines 7100-7355). All RGB values
// are native 9-bit RGB333 (the Next palette's hardware width).
type MixerInputs struct {
	// ULA / LoRes layer
	ULARGB     uint16
	ULAEn      bool
	ULAClipped bool
	Border     bool
	// Tilemap layer
	TMRGB      uint16
	TMPixelEn  bool
	TMTextmode bool
	TMEn       bool
	TMBelow    bool
	// Sprite layer
	SpriteRGB uint16
	SpriteEn  bool
	// Layer 2
	Layer2RGB      uint16
	Layer2En       bool
	Layer2Priority bool
	// config
	Transparent byte // NR$14, compared against rgb[8:1]
	Fallback    byte // NR$4A
	StencilMode bool // NR$68 SLU stencil
	BlendMode   byte // NR$68 blend (0..3)
	Priority    byte // NR$15 bits 4:2 (0..7)
}

// Mix reproduces the FPGA video mixer exactly (zxnext.vhd 7100-7355): from the
// per-layer pixels and the NextReg config it returns the final 9-bit RGB333
// output, implementing ALL EIGHT NR$15 layer orderings — SLU/LSU/SUL/LUS/USL/ULS
// plus the two additive blend modes — the per-layer transparency derivations,
// the ULA+tilemap combine, the SLU stencil and the Layer-2 priority promotion.
// Verified against the captured FPGA golden (TestMixerMatchesFPGAGolden); this
// is the hardware-faithful reference for layer compositing.
func Mix(in MixerInputs) uint16 {
	hi8 := func(rgb uint16) byte { return byte(rgb >> 1) } // rgb[8:1]
	tr := in.Transparent

	// 7100-7124: per-layer transparency + gated colours (verbatim).
	ulaMixTransparent := hi8(in.ULARGB) == tr || in.ULAClipped
	ulaMixRGB := uint16(0)
	if !ulaMixTransparent {
		ulaMixRGB = in.ULARGB
	}
	ulaTransparent := ulaMixTransparent || !in.ULAEn
	ulaRGB := uint16(0)
	if !ulaTransparent {
		ulaRGB = in.ULARGB
	}
	tmTransparent := !in.TMPixelEn || (in.TMTextmode && hi8(in.TMRGB) == tr) || !in.TMEn
	tmRGB := uint16(0)
	if !tmTransparent {
		tmRGB = in.TMRGB
	}
	stencilTransparent := ulaTransparent || tmTransparent
	stencilRGB := uint16(0)
	if !stencilTransparent {
		stencilRGB = ulaRGB & tmRGB
	}
	var ulatmRGB uint16
	if !tmTransparent && (!in.TMBelow || ulaTransparent) {
		ulatmRGB = tmRGB
	} else {
		ulatmRGB = ulaRGB
	}
	ulatmTransparent := ulaTransparent && tmTransparent
	spriteTransparent := !in.SpriteEn
	spriteRGB := uint16(0)
	if !spriteTransparent {
		spriteRGB = in.SpriteRGB
	}
	layer2Transparent := hi8(in.Layer2RGB) == tr || !in.Layer2En
	layer2RGB := uint16(0)
	if !layer2Transparent {
		layer2RGB = in.Layer2RGB
	}
	layer2Priority := in.Layer2Priority && !layer2Transparent

	// 7125-7138: ula_final (stencil-mode select).
	var ulaFinalRGB uint16
	var ulaFinalTransparent bool
	if in.StencilMode && in.ULAEn && in.TMEn {
		ulaFinalRGB, ulaFinalTransparent = stencilRGB, stencilTransparent
	} else {
		ulaFinalRGB, ulaFinalTransparent = ulatmRGB, ulatmTransparent
	}

	// 7140-7180: blend-mode mix_rgb / mix_top / mix_bot.
	var mixRGB, mixTopRGB, mixBotRGB uint16
	var mixRGBTransparent, mixTopTransparent, mixBotTransparent bool
	switch in.BlendMode {
	case 0:
		mixRGB, mixRGBTransparent = ulaMixRGB, ulaMixTransparent
		mixTopTransparent, mixTopRGB = tmTransparent || in.TMBelow, tmRGB
		mixBotTransparent, mixBotRGB = tmTransparent || !in.TMBelow, tmRGB
	case 2:
		mixRGB, mixRGBTransparent = ulaFinalRGB, ulaFinalTransparent
		mixTopTransparent, mixTopRGB = true, tmRGB
		mixBotTransparent, mixBotRGB = true, tmRGB
	case 3:
		mixRGB, mixRGBTransparent = tmRGB, tmTransparent
		mixTopTransparent, mixTopRGB = ulaTransparent || !in.TMBelow, ulaRGB
		mixBotTransparent, mixBotRGB = ulaTransparent || in.TMBelow, ulaRGB
	default: // 1
		mixRGB, mixRGBTransparent = 0, true
		if in.TMBelow {
			mixTopTransparent, mixTopRGB = ulaTransparent, ulaRGB
			mixBotTransparent, mixBotRGB = tmTransparent, tmRGB
		} else {
			mixTopTransparent, mixTopRGB = tmTransparent, tmRGB
			mixBotTransparent, mixBotRGB = ulaTransparent, ulaRGB
		}
	}

	// 7196-7355: SLU ordering + additive blend.
	chan3 := func(rgb uint16, sh uint) int { return int((rgb >> sh) & 7) }
	mr := chan3(layer2RGB, 6) + chan3(mixRGB, 6)
	mg := chan3(layer2RGB, 3) + chan3(mixRGB, 3)
	mb := chan3(layer2RGB, 0) + chan3(mixRGB, 0)
	pack := func(r, g, b int) uint16 {
		return uint16(r&7)<<6 | uint16(g&7)<<3 | uint16(b&7)
	}

	// fallback default: fallback_rgb_2 & (fallback(1) or fallback(0))
	fb := uint16(in.Fallback)
	out := fb<<1 | (fb>>1&1 | fb&1)

	// ula_final wins over a sprite only when NOT in the border-with-tilemap-
	// transparent-and-sprite-present case (zxnext.vhd LUS/USL/ULS guard):
	// `not (ula_border and tm_transparent and not sprite_transparent)`.
	ulaBorderSpriteMask := in.Border && tmTransparent && !spriteTransparent

	switch in.Priority {
	case 0: // SLU
		switch {
		case layer2Priority:
			out = layer2RGB
		case !spriteTransparent:
			out = spriteRGB
		case !layer2Transparent:
			out = layer2RGB
		case !ulaFinalTransparent:
			out = ulaFinalRGB
		}
	case 1: // LSU
		switch {
		case !layer2Transparent:
			out = layer2RGB
		case !spriteTransparent:
			out = spriteRGB
		case !ulaFinalTransparent:
			out = ulaFinalRGB
		}
	case 2: // SUL
		switch {
		case layer2Priority:
			out = layer2RGB
		case !spriteTransparent:
			out = spriteRGB
		case !ulaFinalTransparent:
			out = ulaFinalRGB
		case !layer2Transparent:
			out = layer2RGB
		}
	case 3: // LUS
		switch {
		case !layer2Transparent:
			out = layer2RGB
		case !ulaFinalTransparent && !ulaBorderSpriteMask:
			out = ulaFinalRGB
		case !spriteTransparent:
			out = spriteRGB
		}
	case 4: // USL
		switch {
		case layer2Priority:
			out = layer2RGB
		case !ulaFinalTransparent && !ulaBorderSpriteMask:
			out = ulaFinalRGB
		case !spriteTransparent:
			out = spriteRGB
		case !layer2Transparent:
			out = layer2RGB
		}
	case 5: // ULS
		switch {
		case layer2Priority:
			out = layer2RGB
		case !ulaFinalTransparent && !ulaBorderSpriteMask:
			out = ulaFinalRGB
		case !layer2Transparent:
			out = layer2RGB
		case !spriteTransparent:
			out = spriteRGB
		}
	case 6: // blend (U|T)S(T|U)(B+L) — additive, clamp >7 to 7
		if mr > 7 {
			mr = 7
		}
		if mg > 7 {
			mg = 7
		}
		if mb > 7 {
			mb = 7
		}
		switch {
		case layer2Priority:
			out = pack(mr, mg, mb)
		case !mixTopTransparent:
			out = mixTopRGB
		case !spriteTransparent:
			out = spriteRGB
		case !mixBotTransparent:
			out = mixBotRGB
		case !layer2Transparent:
			out = pack(mr, mg, mb)
		}
	default: // 7: blend (U|T)S(T|U)(B+L-5)
		if !mixRGBTransparent {
			sub5 := func(v int) int {
				switch {
				case v <= 4:
					return 0
				case (v>>2)&3 == 3: // >= 12
					return 7
				default:
					return (v + 11) & 0xF // minus 5 (mod 16)
				}
			}
			mr, mg, mb = sub5(mr), sub5(mg), sub5(mb)
		}
		switch {
		case layer2Priority:
			out = pack(mr, mg, mb)
		case !mixTopTransparent:
			out = mixTopRGB
		case !spriteTransparent:
			out = spriteRGB
		case !mixBotTransparent:
			out = mixBotRGB
		case !layer2Transparent:
			out = pack(mr, mg, mb)
		}
	}
	return out
}
