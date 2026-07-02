// Package nextregs implements the Spectrum Next's NextReg register
// file accessed via ports 0x243B (select) and 0x253B (data) and via
// the Z80N NEXTREG opcodes. The dispatcher provides per-register
// storage and a callback table; subsystems attach side-effect
// handlers for CPU speed (0x07), MMU slots (0x50–0x57), palette
// (0x40–0x44), sprite attribute writes (0x35–0x39, 0x75–0x79) and
// Copper instruction load (0x60–0x62).
package nextregs
