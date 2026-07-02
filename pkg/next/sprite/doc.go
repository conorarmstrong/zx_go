// Package sprite implements the Spectrum Next's hardware-sprite engine:
// 128 sprite slots, 16x16 patterns in 4bpp or 8bpp, mirror/rotate/scale
// 1-8x, anchor groups (composite + unified) and collision detection
// (status in port 0x303B). The per-line sprite-count / bandwidth limit
// is not yet modelled.
package sprite
