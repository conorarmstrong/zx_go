// Package dac implements the Spectrum Next's four 8-bit DACs at the
// documented ports (channel A 0x0F/0xF1, B 0x1F/0xF3, C 0xF9/0xDF
// Specdrum-compatible, D 0xFB). Sprint 5 brings them up and wires
// them into the ULA mixer alongside the multi-AY engine and the
// beeper.
package dac
