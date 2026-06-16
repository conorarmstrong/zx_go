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

// Select sets the active chip from a NextReg 0x06 byte. The
// low 2 bits give the chip index (0..2); a value of 3 is clamped
// to chip 2. Bit 2 disables AY entirely (the engine still routes
// reads / writes but mixing-side code can use Disabled() to skip
// sample generation).
func (e *Engine) Select(val byte) {
	idx := val & 0x03
	if idx > 2 {
		idx = 2
	}
	e.selected = idx
	e.disabled = val&0x04 != 0
}

// Selected returns the active chip index (0..2).
func (e *Engine) Selected() byte { return e.selected }

// Disabled reports whether NextReg 0x06 bit 2 is set, suppressing
// AY output system-wide.
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
// TurboSound. When AY output is disabled (NextReg 0x06 bit 2) nothing is mixed.
//
// This is what makes 128K/AY music audible on the Next: port writes route to
// engine.Active(), but the audio mixer must pull from the engine — feeding it
// the ULA's single u.ay (as the classic path did) mixes a chip the Next never
// writes to, so the music was silent.
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
