package ay

import "sync"

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
	// mu guards the scalar fields below. The chips behind them carry their own
	// mutex (see AY), and the wrapper's own state did not, which left a real
	// race: MixIntoStereo runs on the audio callback goroutine while Select,
	// SelectChip, the panning setters and Reset run on the emulator goroutine.
	// A NextReg $06 write, or a machine reboot driving the whole register file
	// through Reset, therefore raced every audio buffer. The critical sections
	// are per-buffer rather than per-sample, so the cost is not on the hot path.
	mu sync.Mutex

	chips    [3]*AY
	selected byte // 0..2
	disabled bool // NextReg 0x06 bit 2: AY chip disable

	// Panning. Both registers behind these are engine-wide, so the Engine is
	// where they live and the chips are simply told the result — see
	// applyPanning. acb is NR$08 bit 5 (shared by all three PSGs); monoMask is
	// NR$09 bits 7:5, one bit per PSG.
	acb      bool
	monoMask byte
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
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disabled = val&0x03 == 0x03
}

// SelectChip sets the active TurboSound chip (0..2), clamping higher values.
// Driven by the $FFFD chip-select protocol (write 0xFF/0xFE/0xFD to select
// chip 0/1/2), not by NextReg 0x06.
func (e *Engine) SelectChip(idx byte) {
	if idx > 2 {
		idx = 2
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.selected = idx
}

// Selected returns the active chip index (0..2).
func (e *Engine) Selected() byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.selected
}

// Disabled reports whether AY output is suppressed (NextReg 0x06 bits 1-0 == 11,
// "hold all AY in reset").
func (e *Engine) Disabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.disabled
}

// Active returns the currently-selected chip. Port writes through
// 0xFFFD / 0xBFFD on ModelNext route through this.
func (e *Engine) Active() *AY { return e.chips[e.Selected()] }

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
	// The adopted chip has to inherit the panning the engine is already
	// carrying. The Next hands over the ULA's AY during wiring, which happens
	// after the boot defaults have been applied to NR$08, so without this the
	// machine's own chip would be the one left unpanned.
	e.applyPanning()
}

// SetStereoMode applies NR$08 bit 5: clear selects ABC, set selects ACB
// (zxnext.vhd:5177). It is one bit for all three PSGs.
func (e *Engine) SetStereoMode(acb bool) {
	e.mu.Lock()
	e.acb = acb
	e.mu.Unlock()
	e.applyPanning()
}

// SetMonoMask applies NR$09 bits 7:5, which force individual PSGs back to mono
// (zxnext.vhd:5186). Bit 5 is PSG 0, bit 6 is PSG 1, bit 7 is PSG 2: the VHDL
// assigns nr_wr_dat(7 downto 5) to a (2 downto 0) vector, so the high bit of
// the field lands on the high index. The mask is taken whole, so passing the
// NR$09 byte is safe — the other bits are not ours.
func (e *Engine) SetMonoMask(nr09 byte) {
	e.mu.Lock()
	e.monoMask = (nr09 >> 5) & 0x07
	e.mu.Unlock()
	e.applyPanning()
}

// applyPanning pushes the resolved mode down to each chip.
//
// The mono mask wins outright, matching the VHDL: mono_mode_i(n) appears in
// all three of PSG n's mux conditions, so a chip held mono is unreachable by
// stereo_mode_i.
func (e *Engine) applyPanning() {
	e.mu.Lock()
	acb, mask := e.acb, e.monoMask
	e.mu.Unlock()
	for i, c := range e.chips {
		if c == nil {
			continue
		}
		switch {
		case mask&(1<<uint(i)) != 0:
			c.SetStereoMode(StereoMono)
		case acb:
			c.SetStereoMode(StereoACB)
		default:
			c.SetStereoMode(StereoABC)
		}
	}
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
	if e.Disabled() {
		return
	}
	for _, c := range e.chips {
		if c != nil {
			c.MixInto(buf)
		}
	}
}

// MixIntoStereo is the same sum into an interleaved stereo buffer, each chip
// contributing through its own panning. This is what the audio system pulls
// (audio.AYSource); it mirrors pcm_ay_L_o / pcm_ay_R_o, which turbosound.vhd
// forms by adding all three PSGs' panned pairs (turbosound.vhd:334-335).
func (e *Engine) MixIntoStereo(buf []int16) {
	if e.Disabled() {
		return
	}
	for _, c := range e.chips {
		if c != nil {
			c.MixIntoStereo(buf)
		}
	}
}

// Reset reinitialises all three chips and selects chip 0.
func (e *Engine) Reset() {
	for _, c := range e.chips {
		c.Reset()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.selected = 0
	e.disabled = false
}
