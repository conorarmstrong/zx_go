// Package copper implements the Spectrum Next's Copper coprocessor:
// 1024 16-bit instructions (MOVE / WAIT / NOOP / HALT), four start
// modes (stop, start-from-0, continue, auto-restart-on-VBL) and
// raster synchronisation via the ULA scanline counter. Control lives
// in NextRegs 0x60-0x62.
package copper
