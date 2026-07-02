// Package audiodac implements the classic ZX Spectrum 8-bit DAC sound add-ons:
// the Cheetah SpecDrum (OUT to port $DF) and a mono Covox (OUT to port $FB).
// Each OUT latches an unsigned 8-bit PCM sample; the device records every write
// with its T-state offset within the frame so the audio system can reconstruct
// the waveform sample-accurately (a box-filter integration, exactly like the
// beeper) rather than snapshotting one level per buffer. The two devices are
// independent opt-in peripherals — a disabled device claims no port, so Covox
// does not clash with the ZX Printer (also on $FB) unless the user enables it.
package audiodac

// Port low-byte decodes.
const (
	SpecDrumPort = 0xDF // Cheetah SpecDrum
	CovoxPort    = 0xFB // mono Covox (shares $FB with the ZX Printer)
)

// amplitude scales an 8-bit sample (centred on 128) into the mixed audio range.
// Chosen so full scale (~±12k) sits a little below the beeper's ±20000.
const amplitude = 96

type event struct {
	tstateOffset int
	level        byte
}

// DAC is a SpecDrum + Covox 8-bit DAC pair.
type DAC struct {
	specDrum   bool
	covox      bool
	startLevel byte // level at the start of the current frame (carries over)
	events     []event
}

// New returns a DAC with both devices disabled and the output centred (silent).
func New() *DAC { return &DAC{startLevel: 0x80} }

// SetSpecDrum / SetCovox enable or disable each device.
func (d *DAC) SetSpecDrum(on bool) { d.specDrum = on }
func (d *DAC) SetCovox(on bool)    { d.covox = on }

// SpecDrumEnabled / CovoxEnabled report the current state.
func (d *DAC) SpecDrumEnabled() bool { return d.specDrum }
func (d *DAC) CovoxEnabled() bool    { return d.covox }

// Enabled reports whether either device is active.
func (d *DAC) Enabled() bool { return d.specDrum || d.covox }

// Handles reports whether an OUT to the given port low byte is claimed by an
// enabled device.
func (d *DAC) Handles(low byte) bool {
	return (d.specDrum && low == SpecDrumPort) || (d.covox && low == CovoxPort)
}

// Record latches a sample written at tstateOffset within the current frame.
func (d *DAC) Record(tstateOffset int, val byte) {
	d.events = append(d.events, event{tstateOffset: tstateOffset, level: val})
}

// GenerateFrame returns one frame of mixed-range samples reconstructed from the
// recorded writes, then clears the events and carries the final level into the
// next frame. samplesPerFrame is the audio-rate sample count; tstatesPerFrame is
// the CPU-clock length of the frame.
func (d *DAC) GenerateFrame(samplesPerFrame, tstatesPerFrame int) []int16 {
	out := make([]int16, samplesPerFrame)
	level := d.startLevel
	idx := 0
	for i := 0; i < samplesPerFrame; i++ {
		sampleStart := i * tstatesPerFrame / samplesPerFrame
		sampleEnd := (i + 1) * tstatesPerFrame / samplesPerFrame
		sampleLen := sampleEnd - sampleStart

		// Box-filter integrate the held level over [sampleStart, sampleEnd).
		var acc int64
		cur := sampleStart
		for idx < len(d.events) && d.events[idx].tstateOffset < sampleEnd {
			next := d.events[idx].tstateOffset
			if next < cur {
				next = cur
			}
			acc += int64(level) * int64(next-cur)
			level = d.events[idx].level
			cur = next
			idx++
		}
		acc += int64(level) * int64(sampleEnd-cur)

		avg := level
		if sampleLen > 0 {
			avg = byte(acc / int64(sampleLen))
		}
		out[i] = (int16(avg) - 128) * amplitude
	}

	// Any event at or after the frame's final sample boundary didn't land in
	// a [sampleStart, sampleEnd) window above and so never updated level —
	// without this it would be silently discarded below instead of carrying
	// into the next frame.
	if idx < len(d.events) {
		level = d.events[len(d.events)-1].level
	}

	d.startLevel = level
	d.events = d.events[:0]
	return out
}

// Reset clears all state to silence.
func (d *DAC) Reset() {
	d.startLevel = 0x80
	d.events = d.events[:0]
}
