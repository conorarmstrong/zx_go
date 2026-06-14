// Package sprite implements the Spectrum Next's hardware-sprite engine:
// 128 sprite slots, 16x16 patterns in 4bpp or 8bpp, mirror/rotate/scale
// 1-8x, anchor groups (composite + unified), per-line bandwidth limit
// and collision/over-limit detection (status in port 0x303B). Sprint 7
// lands the engine and integrates it with the compositor.
package sprite
