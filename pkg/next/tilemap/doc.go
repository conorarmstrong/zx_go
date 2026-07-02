// Package tilemap implements the Spectrum Next's Layer 3 tilemap:
// 40x32 or 80x32 grids of 8x8 tiles, 4bpp/8bpp, per-tile attribute
// (palette offset, mirror, rotate, priority), scroll and clipping.
// Tile-map and tile-definition base addresses come from NextRegs 0x6E/0x6F.
package tilemap
