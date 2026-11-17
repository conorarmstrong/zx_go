package palette

// LoadFirmwareDefaults fills the palettes with what the Next's boot firmware
// loads. The FPGA's palette RAM has no initial contents
// (ram/init/none.bin.txt, zxnext.vhd:6960,7013): a machine that boots through
// the firmware gets these from it, and a machine built without running the
// firmware (a test harness loading a program straight into memory) needs them
// set here or every layer draws black.
//
// The values are the firmware's, read back from a booted machine. Both ULA
// palettes hold the Spectrum colours, 16 entries repeated through all 256:
// each channel 101 when on and 111 when bright, except bright magenta, which
// is 111 001 111 so that it does not match the default transparency $E3.
// Layer 2 and the sprites get the identity mapping, entry i being RGB332 i
// with its ninth bit the OR of the two blue bits. The firmware leaves the
// tilemap palettes at zero.
func (b *Bank) LoadFirmwareDefaults() {
	ula := [16]uint16{
		0x000, 0x005, 0x140, 0x145, 0x028, 0x02D, 0x168, 0x16D,
		0x000, 0x007, 0x1C0, 0x1CF, 0x038, 0x03F, 0x1F8, 0x1FF,
	}
	for _, sel := range []byte{PaletteULAFirst, PaletteULASecond} {
		p := b.palettes[sel]
		for i := 0; i < 256; i++ {
			p.Set(byte(i), ula[i&15])
		}
	}
	for _, sel := range []byte{PaletteLayer2First, PaletteLayer2Second, PaletteSpritesFirst, PaletteSpritesSecond} {
		p := b.palettes[sel]
		for i := 0; i < 256; i++ {
			v := uint16(i) << 1
			if i&0x03 != 0 {
				v |= 1
			}
			p.Set(byte(i), v)
		}
	}
}
