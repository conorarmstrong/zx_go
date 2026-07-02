// Package compositor sums the Spectrum Next's display layers per scanline:
// ULA (or LoRes / Timex / ULAnext), Layer 2, Tilemap (Layer 3) and the 128
// hardware sprites, with priority configured by NextReg 0x15.
//
// The compositor operates per-scanline at 256 pixels wide (plus wide paths
// for the 320/640-px border and hi-res cases). Each layer is asked for its
// row, then the rows are composited per pixel according to the active
// NextReg 0x15 priority mode. The transparency index is the live NextReg
// $14 (global transparency colour, FPGA nr_14, default 0xE3), wired into
// SetTransparency by cmd/zx_go/next.go.
package compositor
