package testharness

import (
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// LoadTAP mounts a .tap image and installs a fast-load trap on the 48K ROM's
// LD-BYTES routine ($0556): each ROM tape-load call injects the next tape
// block straight into memory instead of being decoded edge-by-edge in real
// time. This turns a multi-thousand-frame real-time load into a handful of
// frames while still driving the genuine ROM loader — the caller boots the
// machine and types LOAD"" (or the 128 Tape Loader) as normal; only the
// per-block byte transfer is accelerated.
//
// The machine must be running the 48K BASIC ROM at $0000 (a 48K model, or the
// Next booted to 48K BASIC with the embedded 48K ROM, which is where LD-BYTES
// lives at $0556). The trap goes inert once every block has been consumed, so
// it never interferes with the loaded program.
//
// This mirrors cmd/zx_go's installTapeTrap but is scoped to the Harness so
// tape-loaded software (e.g. the DMA conformance corpus, which needs the Next's
// zxnDMA) can run headless without the proprietary NextZXOS boot.
func (h *Harness) LoadTAP(path string) error {
	tp := ula.NewTapePlayer()
	if err := tp.LoadTAP(path); err != nil {
		return err
	}
	h.ula.SetTapePlayer(tp)
	tp.Play()

	h.cpu.TrapCheck = func(pc uint16) bool {
		if pc != 0x0556 || !tp.HasMoreBlocks() {
			return false
		}
		block := tp.NextBlock()
		if block == nil {
			return false
		}
		// LD-BYTES contract: A = expected flag byte, carry = LOAD (vs VERIFY),
		// IX = destination, DE = byte count. A tape block is [flag, data…,
		// checksum].
		expectedFlag := h.cpu.A
		isLoad := (h.cpu.F & z80.FLAG_C) != 0
		dst := h.cpu.IX
		count := uint16(h.cpu.D)<<8 | uint16(h.cpu.E)

		success := true
		if len(block) < 1 || block[0] != expectedFlag {
			success = false // flag mismatch → R Tape loading error
		} else {
			data := block[1:]
			if len(data) > 0 {
				data = data[:len(data)-1] // drop the checksum byte
			}
			n := int(count)
			if n > len(data) {
				n = len(data)
				success = false
			}
			if isLoad {
				for i := 0; i < n; i++ {
					h.mem.Write(dst+uint16(i), data[i])
				}
			}
			h.cpu.IX = dst + uint16(n)
			h.cpu.D, h.cpu.E = 0, 0
		}
		if success {
			h.cpu.F |= z80.FLAG_C
		} else {
			h.cpu.F &^= z80.FLAG_C
		}

		// Return from LD-BYTES: it is entered via CALL, so RET by popping the
		// caller's return address off the stack into PC.
		low := h.mem.Read(h.cpu.SP)
		high := h.mem.Read(h.cpu.SP + 1)
		h.cpu.SP += 2
		h.cpu.PC = uint16(high)<<8 | uint16(low)
		return true
	}
	return nil
}
