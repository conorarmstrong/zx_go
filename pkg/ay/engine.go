package ay

// Engine is a multi-AY chip bank. The ZX Spectrum Next ships
// three AY-3-8912 instances visible through the classic
// 0xFFFD / 0xBFFD ports — guest code picks which chip the port
// writes hit by writing the chip-select value to NextReg 0x06
// bits 0-1.
//
// Single-AY models (48K does not have one; 128K / +2 / +2A / +3
// have one) continue to use the existing AY type directly.
// Engine is the ModelNext-only wrapper.
type Engine struct {
	chips    [3]*AY
	selected byte // 0..2
	disabled bool // NextReg 0x06 bit 2: AY chip disable
}

// NewEngine returns an Engine with three freshly-reset AY chips.
func NewEngine() *Engine {
	return &Engine{
		chips: [3]*AY{New(), New(), New()},
	}
}

// Select applies a NextReg 0x06 ("Peripheral 2") write. Per the FPGA spec,
// bits 1-0 are the AUDIO CHIP MODE (00 = YM, 01 = AY, 10 = ZXN-8950,
// 11 = hold all AY in reset) and bit 2 is PS/2 mode — NOT AY-related. So only
// "hold all AY in reset" (bits 1-0 == 11) silences the engine; YM/AY/8950 are
// all active (we model AY behaviour for each). NR$06 does NOT select which
// TurboSound chip is active — that is SelectChip, driven by the $FFFD
// chip-select protocol. NextZXOS sets bit 2 for PS/2 during boot, so bit 2
// must be ignored here or that boot-time write would wrongly silence AY
// output.
func (e *Engine) Select(val byte) {
	e.disabled = val&0x03 == 0x03
}

// SelectChip sets the active TurboSound chip (0..2), clamping higher values.
// Driven by the $FFFD chip-select protocol (write 0xFF/0xFE/0xFD to select
// chip 0/1/2), not by NextReg 0x06.
func (e *Engine) SelectChip(idx byte) {
	if idx > 2 {
		idx = 2
	}
	e.selected = idx
}

// Selected returns the active chip index (0..2).
func (e *Engine) Selected() byte { return e.selected }

// Disabled reports whether AY output is suppressed (NextReg 0x06 bits 1-0 == 11,
// "hold all AY in reset").
func (e *Engine) Disabled() bool { return e.disabled }

// Active returns the currently-selected chip. Port writes through
// 0xFFFD / 0xBFFD on ModelNext route through this.
func (e *Engine) Active() *AY { return e.chips[e.selected] }

// Chip returns the chip at index i (0..2), or nil if i is out
// of range.
func (e *Engine) Chip(i int) *AY {
	if i < 0 || i >= 3 {
		return nil
	}
	return e.chips[i]
}

// SetChip installs an existing AY at the given index, replacing
// the freshly-constructed one. Useful when the engine is layered
// on top of a host that already has a single AY (e.g. the ULA's
// singleton u.ay): adopting that chip as the engine's chip 0
// avoids carrying two AY instances when only one is needed.
func (e *Engine) SetChip(i int, c *AY) {
	if i < 0 || i >= 3 || c == nil {
		return
	}
	e.chips[i] = c
}

// MixInto sums all three TurboSound chips into buf, so the Engine satisfies the
// audio system's AY source (audio.AYSource). Each AY.MixInto adds (saturating)
// its own contribution, so silent chips (no registers written) contribute
// nothing — making this correct for a plain 128K (only chip 0 used) as well as
// TurboSound. When AY output is disabled (NextReg 0x06 bits 1-0 == 11) nothing
// is mixed.
//
// Port writes route to engine.Active(), so the audio mixer must pull from the
// Engine itself rather than a chip held elsewhere — otherwise it would mix a
// chip the guest never writes to.
func (e *Engine) MixInto(buf []int16) {
	if e.disabled {
		return
	}
	for _, c := range e.chips {
		if c != nil {
			c.MixInto(buf)
		}
	}
}

// Reset reinitialises all three chips and selects chip 0.
func (e *Engine) Reset() {
	for _, c := range e.chips {
		c.Reset()
	}
	e.selected = 0
	e.disabled = false
}
