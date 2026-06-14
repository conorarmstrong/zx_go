// Package compositor sums the Spectrum Next's display layers per
// scanline: ULA (or LoRes / Timex / ULAnext), Layer 2, Tilemap (Layer
// 3) and the 128 hardware sprites, with priority configured by NextReg
// 0x15. Sprint 6 lands v1 (ULA + Layer 2 + Tilemap); Sprint 7 grows it
// to the full 5-source pipeline with sprite bandwidth and collision
// accounting.
package compositor
