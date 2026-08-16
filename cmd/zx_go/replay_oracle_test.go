package main

// The replay-equivalence oracle.
//
// Every other test in the capture effort is a claim about one device made by
// the person who wrote that device. This is the measurement: it boots a real
// machine, captures it through the real registry, runs it forward, rewinds it,
// runs it forward again, and asks whether the machine produced the same thing
// twice.
//
//	run to instruction A
//	S  := registry.Capture()
//	F1 := fingerprint(run K instructions)
//	registry.Restore(S)
//	F2 := fingerprint(run K instructions)
//	assert F1 == F2
//
// The design constraint that makes it worth running is that the FINGERPRINT IS
// BROADER THAN THE CAPTURE. If the fingerprint were built from the device state
// structs the capture already covers, the test would be circular: it would
// compare a capture against itself and pass no matter how much of the machine
// was missing. So the fingerprint is taken from the machine's OUTPUTS and from
// memory in bulk, none of it routed through a SaveState:
//
//   - the whole Z80 register file including the shadow set, sampled after EVERY
//     instruction, read off the live CPU rather than out of its capture;
//   - the entire RAM pool, all banks, not just the eight mapped into the
//     current window, plus the four ROM pages (a Next in config mode writes to
//     them);
//   - the paging window itself: both 16K page maps, the eight MMU slots and the
//     paging ports, so "right bytes behind the wrong window" is caught;
//   - the composed display, pixel for pixel, once per emulated frame across the
//     whole window — a stream rather than a single final picture, because a
//     lost ULA heals within a frame or two and a single end-of-run frame does
//     not see it;
//   - a stream of audio samples pulled from the AY (or the TurboSound engine)
//     and of the beeper level, sampled repeatedly DURING the K instructions, so
//     a chip whose hidden tone/noise/envelope counters were not restored shows
//     up as a different waveform rather than as a matching register file;
//   - the border colour sampled on the same schedule, because border writes are
//     an output the final rendered frame alone would only partly show.
//
// A device missing from the capture cannot hide from that. The machine diverges
// during replay and one of those observables differs.
//
// Reporting is per observable and, for the CPU, per instruction, because "the
// oracle failed" is worth very little and "the register file differs at
// instruction 41,207, PC $0C0A vs $0C0D" names the bug.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// oracleBootFrames is how far the machine runs before the capture is taken.
// Far enough that the ROM is past its RAM test and into its interrupt-driven
// keyboard-scan loop, so the captured machine is a running one — mid-frame,
// mid-flash-cycle, with a live interrupt pattern — rather than a power-on
// state where almost nothing has moved yet.
const oracleBootFrames = 120

// oracleInstructions is K: how far the machine runs after the capture, on each
// of the two passes. Ten frames' worth on a 48K, so several ULA interrupts and
// several flash-counter steps fall inside the replay window.
const oracleInstructions = 60000

// oracleWindow scales K by the CPU clock so the replay window is the same
// number of ULA FRAMES on every machine.
//
// It is not a refinement. The Next boots at 28 MHz and so retires eight times
// as many instructions per frame; at a flat 60,000 the window covered a single
// composed frame there against nine on a 48K, which left the display
// fingerprint with almost nothing to compare and made the Next's result far
// weaker than it looked.
func oracleWindow(e *emulator) int {
	return oracleInstructions * e.cpu.SpeedMultiplier()
}

// oracleAudioEvery is how often, in instructions, the run pulls audio samples
// and samples the beeper and border. Pulling samples advances the AY's hidden
// counters, which is the point: it makes the sample stream depend on state no
// register file holds. Both passes pull on exactly the same schedule, so the
// mutation is part of the experiment rather than noise in it.
const oracleAudioEvery = 512

// oracleAudioSamples is how many AY samples each pull takes.
const oracleAudioSamples = 4

// namedDigest is one observable and its hash. The name is what a failure
// reports, so it has to say which output diverged, not just that one did.
type namedDigest struct {
	name string
	sum  [sha256.Size]byte
	size int
}

// replayTrace is everything one pass of K instructions produced.
type replayTrace struct {
	// cpuTrace is the register file after every instruction, packed
	// traceEntryBytes to an entry. It is what turns "they diverged" into
	// "they diverged at instruction N".
	cpuTrace []byte
	// audio is the interleaved beeper level and AY samples, sampled every
	// oracleAudioEvery instructions.
	audio []byte
	// border is the border colour on the same schedule.
	border []byte
	// video is one digest per composed frame, in order.
	video []byte
}

// traceEntryBytes is the packed size of one instruction's register file:
// twelve 16-bit registers, I, R, the interrupt/halt flags, IM, and the low 32
// bits of the T-state counter.
const traceEntryBytes = 32

// appendCPUTrace packs the register file into dst.
//
// The shadow set is in here deliberately. It is the part of the CPU a
// register-only capture is most likely to forget, and on the Spectrum the ROM's
// interrupt handler uses it on every frame, so a machine that lost it diverges
// within one interrupt rather than eventually.
func appendCPUTrace(dst []byte, c *z80.CPU) []byte {
	be := binary.BigEndian
	dst = be.AppendUint16(dst, c.PC)
	dst = be.AppendUint16(dst, c.SP)
	dst = be.AppendUint16(dst, uint16(c.A)<<8|uint16(c.F))
	dst = be.AppendUint16(dst, uint16(c.B)<<8|uint16(c.C))
	dst = be.AppendUint16(dst, uint16(c.D)<<8|uint16(c.E))
	dst = be.AppendUint16(dst, uint16(c.H)<<8|uint16(c.L))
	dst = be.AppendUint16(dst, uint16(c.A_)<<8|uint16(c.F_))
	dst = be.AppendUint16(dst, uint16(c.B_)<<8|uint16(c.C_))
	dst = be.AppendUint16(dst, uint16(c.D_)<<8|uint16(c.E_))
	dst = be.AppendUint16(dst, uint16(c.H_)<<8|uint16(c.L_))
	dst = be.AppendUint16(dst, c.IX)
	dst = be.AppendUint16(dst, c.IY)
	var flags byte
	if c.IFF1 {
		flags |= 1
	}
	if c.IFF2 {
		flags |= 2
	}
	if c.Halted {
		flags |= 4
	}
	dst = append(dst, c.I, c.R, flags, c.IM)
	return be.AppendUint32(dst, uint32(c.Tstates()))
}

// decodeTraceEntry renders one packed entry for a failure message.
func decodeTraceEntry(e []byte) string {
	be := binary.BigEndian
	return fmt.Sprintf("PC=$%04X SP=$%04X AF=$%04X BC=$%04X DE=$%04X HL=$%04X "+
		"AF'=$%04X BC'=$%04X DE'=$%04X HL'=$%04X IX=$%04X IY=$%04X "+
		"I=$%02X R=$%02X IFF1=%v IFF2=%v HALT=%v IM=%d T=%d",
		be.Uint16(e[0:]), be.Uint16(e[2:]), be.Uint16(e[4:]), be.Uint16(e[6:]),
		be.Uint16(e[8:]), be.Uint16(e[10:]), be.Uint16(e[12:]), be.Uint16(e[14:]),
		be.Uint16(e[16:]), be.Uint16(e[18:]), be.Uint16(e[20:]), be.Uint16(e[22:]),
		e[24], e[25], e[26]&1 != 0, e[26]&2 != 0, e[26]&4 != 0, e[27],
		be.Uint32(e[28:]))
}

// oracleBoot runs the machine to the point the capture is taken from.
//
// It renders every frame, as the GUI does. That is not cosmetic: the ULA's
// border-change and speaker-event lists are per-frame and are only drained by
// Render, so a boot that never rendered would leave the capture holding a
// hundred frames of accumulated events and would not resemble any machine a
// user ever rewinds.
func oracleBoot(e *emulator, model roms.SpectrumModel, frames int) {
	e.paused.Store(false)
	for i := 0; i < frames; i++ {
		runOneFrameHeadless(e, model)
		if e.ula != nil {
			e.ula.Render()
		}
	}
	oracleExercise(e, model)
}

// oracleExerciserOrigin is where the exerciser is assembled. $8000 is RAM on
// every model the oracle runs, and no paging port moves it.
const oracleExerciserOrigin = 0x8000

// oracleExerciser is a guest program that makes the machine USE the devices the
// oracle is trying to measure.
//
// Without it the property is nearly vacuous. A Spectrum left alone after boot
// sits in its ROM's keyboard-scan loop: the display file is never written, the
// speaker never toggles, the border never changes, and on a 128K nothing ever
// addresses the AY at all. The oracle would then compare a static screen with a
// static screen and silence with silence — and would still pass with the sound
// chip's entire capture deleted, which is precisely the class of hole it exists
// to find. So the machine is given something to do first.
//
// Every pass round the loop, driven by a counter in RAM: a write to port $FE
// (border colour, MIC, and the speaker bit, so the ULA records a border change
// AND a speaker toggle each time round), an AY register select through $FFFD
// and a data write through $BFFD (so the tone, noise and envelope generators
// are reprogrammed while running, leaving their hidden counters somewhere no
// fresh chip would be), a store into the display file that walks the whole
// bitmap and attribute area, and a read back of port $FE. Interrupts stay
// enabled, so the ROM's own frame handler keeps running underneath.
//
//	LD A,(counter)      3A 00 81
//	INC A               3C
//	LD (counter),A      32 00 81
//	OUT ($FE),A         D3 FE
//	AND $0F             E6 0F
//	LD BC,$FFFD         01 FD FF
//	OUT (C),A           ED 79      ; AY register select
//	LD BC,$BFFD         01 FD BF
//	LD A,(counter)      3A 00 81
//	OUT (C),A           ED 79      ; AY register data
//	LD HL,(cursor)      2A 01 81
//	LD A,(counter)      3A 00 81
//	LD (HL),A           77
//	INC HL              23
//	LD A,H              7C
//	CP $5B              FE 5B      ; past the attributes?
//	JR NZ,+3            20 03
//	LD HL,$5800         21 00 58
//	LD (cursor),HL      22 01 81
//	IN A,($FE)          DB FE      ; keyboard rows / EAR
//	JP $8000            C3 00 80
//
// The cursor sweeps the ATTRIBUTE area, $5800-$5AFF, rather than the bitmap.
// That is not arbitrary. Attribute bytes carry the FLASH bit, and the ULA's
// position in the 16-frame flash cycle is state a capture can lose; with the
// counter written into attributes, roughly half the screen flashes, so a
// restored ULA that resumed at the wrong point in that cycle paints a
// different picture. Sweeping the 6 KB bitmap instead took more instructions
// than the whole replay window, so the cursor never reached an attribute at
// all and the composed frames came out identical no matter what the ULA had
// been restored to — a display fingerprint that could not see the display
// device.
var oracleExerciser = []byte{
	0x3A, 0x00, 0x81,
	0x3C,
	0x32, 0x00, 0x81,
	0xD3, 0xFE,
	0xE6, 0x0F,
	0x01, 0xFD, 0xFF,
	0xED, 0x79,
	0x01, 0xFD, 0xBF,
	0x3A, 0x00, 0x81,
	0xED, 0x79,
	0x2A, 0x01, 0x81,
	0x3A, 0x00, 0x81,
	0x77,
	0x23,
	0x7C,
	0xFE, 0x5B,
	0x20, 0x03,
	0x21, 0x00, 0x58,
	0x22, 0x01, 0x81,
	0xDB, 0xFE,
	0xC3, 0x00, 0x80,
}

// oracleNextExerciser is the same loop with the Next bus added.
//
// The classic loop leaves every Next block untouched, so running it on a Next
// would have the oracle report a green machine while saying nothing whatever
// about the nine devices that make it a Next.
//
// The two Next writes it adds are chosen for what they do NOT re-state. Both
// are streams into a CURSOR, and the cursor is the hidden state:
//
//   - port $253B writes the NextReg the machine already has selected. The
//     selection is made once, before the run, and never repeated — so the
//     register file's "which register is selected" is live state that decides
//     where every write in the replay window lands. The selected register is
//     NR$41, the palette value, which itself auto-increments the palette write
//     index, so the palette bank's cursor decides which entry each write
//     recolours.
//   - ports $57 and $5B stream sprite attribute and PATTERN bytes. The sprite
//     engine advances its own cursor per byte and rolls into the next slot, so
//     where a byte lands is decided by state the guest never restates either.
//     The pattern stream is what makes the sprites visible at all: pattern
//     memory powers on uniform, so sprites drawn from it are a flat block whose
//     appearance does not depend on which slot got which attribute, and the
//     composed frame came out identical however the engine had been restored.
//
// An earlier version re-selected the register and re-set both indices every
// time round the loop. It exercised the same devices and proved nothing: a
// stale cursor was overwritten on the first iteration and the two passes
// converged before the first frame was composed. Streaming into the cursor is
// what makes the difference survive.
//
// Sprites are switched on before the run (NR$15) so the sprite stream reaches
// the composed picture.
//
//	LD A,(counter)      3A 00 81
//	INC A               3C
//	LD (counter),A      32 00 81
//	OUT ($FE),A         D3 FE      ; border + MIC + speaker
//	AND $0F             E6 0F
//	LD BC,$FFFD         01 FD FF
//	OUT (C),A           ED 79      ; AY register select
//	LD BC,$BFFD         01 FD BF
//	LD A,(counter)      3A 00 81
//	OUT (C),A           ED 79      ; AY register data
//	LD BC,$253B         01 3B 25
//	LD A,(counter)      3A 00 81
//	OUT (C),A           ED 79      ; NextReg data -> the selected register
//	LD A,(counter)      3A 00 81
//	OUT ($57),A         D3 57      ; sprite attribute stream
//	OUT ($57),A         D3 57
//	OUT ($57),A         D3 57
//	OUT ($57),A         D3 57
//	OUT ($5B),A         D3 5B      ; sprite pattern stream
//	OUT ($5B),A         D3 5B
//	OUT ($5B),A         D3 5B
//	OUT ($5B),A         D3 5B
//	LD HL,(cursor)      2A 01 81
//	LD A,(counter)      3A 00 81
//	LD (HL),A           77
//	INC HL              23
//	LD A,H              7C
//	CP $5B              FE 5B
//	JR NZ,+3            20 03
//	LD HL,$5800         21 00 58
//	LD (cursor),HL      22 01 81
//	IN A,($FE)          DB FE
//	JP $8000            C3 00 80
var oracleNextExerciser = []byte{
	0x3A, 0x00, 0x81,
	0x3C,
	0x32, 0x00, 0x81,
	0xD3, 0xFE,
	0xE6, 0x0F,
	0x01, 0xFD, 0xFF,
	0xED, 0x79,
	0x01, 0xFD, 0xBF,
	0x3A, 0x00, 0x81,
	0xED, 0x79,
	0x01, 0x3B, 0x25,
	0x3A, 0x00, 0x81,
	0xED, 0x79,
	0x3A, 0x00, 0x81,
	0xD3, 0x57,
	0xD3, 0x57,
	0xD3, 0x57,
	0xD3, 0x57,
	0xD3, 0x5B,
	0xD3, 0x5B,
	0xD3, 0x5B,
	0xD3, 0x5B,
	0x2A, 0x01, 0x81,
	0x3A, 0x00, 0x81,
	0x77,
	0x23,
	0x7C,
	0xFE, 0x5B,
	0x20, 0x03,
	0x21, 0x00, 0x58,
	0x22, 0x01, 0x81,
	0xDB, 0xFE,
	0xC3, 0x00, 0x80,
}

// oracleNextSetup arms the two cursors the Next exerciser streams into, and
// makes the sprite engine visible so the stream reaches the picture. Done once,
// before the run, precisely so the loop never restates it.
func oracleNextSetup(e *emulator) {
	if e.nextRegs != nil {
		e.nextRegs.WriteReg(0x15, 0x03) // sprites visible, and over the border
		e.nextRegs.WriteReg(0x40, 0x00) // palette write index to the start
	}
	if e.ula != nil {
		e.ula.WritePort(0x243B, 0x41) // select NR$41: palette value, auto-incrementing
		e.ula.WritePort(0x303B, 0x00) // sprite slot 0, attribute cursor at its first byte
	}
}

// oracleExerciserVars is where the exerciser keeps its counter and its display
// cursor. It is clear of the longest program — the Next one is 99 bytes — so
// the loop cannot overwrite its own code.
const oracleExerciserVars = 0x8100

// oracleExercise assembles the exerciser into RAM, points the CPU at it, and
// lets it run for a few frames before the capture is taken — so the capture is
// of a machine whose sound chip is mid-envelope and whose ULA is mid-frame with
// events already queued, not of one that has merely been configured.
func oracleExercise(e *emulator, model roms.SpectrumModel) {
	prog := oracleExerciser
	if model == roms.ModelNext {
		prog = oracleNextExerciser
		oracleNextSetup(e)
	}
	for i, b := range prog {
		e.mem.Write(uint16(oracleExerciserOrigin+i), b)
	}
	e.mem.Write(oracleExerciserVars+0, 0x00) // counter
	e.mem.Write(oracleExerciserVars+1, 0x00) // display cursor, low
	e.mem.Write(oracleExerciserVars+2, 0x58) // cursor high — the attribute area
	e.cpu.PC = oracleExerciserOrigin
	for i := 0; i < 8; i++ {
		runOneFrameHeadless(e, model)
		if e.ula != nil {
			e.ula.Render()
		}
	}
}

// oracleAudioProbe pulls count audio samples from whichever AY the machine
// carries, advancing the chip exactly as the audio mixer would.
//
// The Next's TurboSound engine is asked rather than its chip 0, because the
// engine is what the mixer pulls from and two of its three chips are invisible
// from the ULA. A 48K has no AY at all and contributes no samples; its beeper
// is the whole of its audio and is sampled separately.
func oracleAudioProbe(e *emulator, count int) []int16 {
	if e.nextAY != nil {
		buf := make([]int16, count)
		e.nextAY.MixInto(buf)
		return buf
	}
	if e.ula != nil {
		if a := e.ula.AY(); a != nil {
			return a.GenerateSamples(count)
		}
	}
	return nil
}

// replayRun drives the machine forward k instructions, recording the trace.
//
// StepInstructionWithIRQ is the driver rather than ExecuteFrame because the
// property is stated in instructions and because it is the path that mirrors
// the frame loop's body one instruction at a time — including the frame
// interrupt, so the replay window contains real interrupt acceptance rather
// than an artificially quiet machine.
// The display is composed once per emulated frame, exactly as the GUI does it,
// and every composed frame goes into the fingerprint.
//
// Composing only once, at the end of the run, was not enough. The ULA's border
// and speaker event lists are per-frame and Render is what drains them, so a
// run that never rendered accumulated tens of thousands of events into a single
// final frame — and a machine whose ULA was left un-rewound produced the SAME
// final picture, because the handful of stale events at the head were buried
// under the ones both passes went on to record. A stream of frames catches it
// on the first one.
func replayRun(e *emulator, model roms.SpectrumModel, k int) replayTrace {
	tr := replayTrace{
		cpuTrace: make([]byte, 0, k*traceEntryBytes),
		audio:    make([]byte, 0, (k/oracleAudioEvery+1)*(1+2*oracleAudioSamples)),
		border:   make([]byte, 0, k/oracleAudioEvery+1),
	}
	frameLen := uint64(frameTStatesForModel(model)) * uint64(e.cpu.SpeedMultiplier())
	frame := e.cpu.Tstates() / frameLen
	for i := 0; i < k; i++ {
		e.cpu.StepInstructionWithIRQ()
		tr.cpuTrace = appendCPUTrace(tr.cpuTrace, e.cpu)
		if now := e.cpu.Tstates() / frameLen; now != frame {
			frame = now
			if e.ula != nil {
				sum := sha256.Sum256(e.ula.Render().Pix)
				tr.video = append(tr.video, sum[:]...)
			}
		}
		if i%oracleAudioEvery != 0 {
			continue
		}
		if e.ula != nil {
			var speaker byte
			if e.ula.Speaker {
				speaker = 1
			}
			tr.audio = append(tr.audio, speaker)
			tr.border = append(tr.border, e.ula.BorderColour)
		}
		for _, s := range oracleAudioProbe(e, oracleAudioSamples) {
			tr.audio = binary.BigEndian.AppendUint16(tr.audio, uint16(s))
		}
	}
	return tr
}

// observeMachine fingerprints everything the machine can be seen to hold or
// produce, without asking any device for its captured state.
func observeMachine(e *emulator, tr replayTrace) []namedDigest {
	var out []namedDigest
	add := func(name string, b []byte) {
		out = append(out, namedDigest{name: name, sum: sha256.Sum256(b), size: len(b)})
	}

	add("cpu.trace", tr.cpuTrace)
	add("audio.stream", tr.audio)
	add("video.border", tr.border)
	add("video.frames", tr.video)

	// RAM, every bank in the pool — not the visible window. A device that
	// forgot which bank was paged in would still show the same window; a
	// machine that ran differently writes different bytes into banks the
	// window never showed.
	h := sha256.New()
	ramBytes := 0
	for _, bank := range e.mem.SnapshotPool() {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(bank)))
		_, _ = h.Write(hdr[:])
		_, _ = h.Write(bank)
		ramBytes += len(bank)
	}
	var ramSum [sha256.Size]byte
	copy(ramSum[:], h.Sum(nil))
	out = append(out, namedDigest{name: "ram.pool", sum: ramSum, size: ramBytes})

	// The ROM pages are state, not a fixed image: on the Next a guest write in
	// config mode lands in one of them.
	var romBytes []byte
	for p := 0; p < 4; p++ {
		romBytes = append(romBytes, e.mem.GetROMPage(p)...)
	}
	add("rom.pages", romBytes)

	// The window laid over the pool. Restoring the bytes behind the wrong
	// window is the failure this catches.
	var win []byte
	rd, wr := e.mem.GetPageMap()
	for i := 0; i < 4; i++ {
		win = binary.BigEndian.AppendUint32(win, uint32(rd[i]))
		win = binary.BigEndian.AppendUint32(win, uint32(wr[i]))
	}
	mmu := e.mem.SnapshotMMU()
	win = append(win, mmu[:]...)
	p7ffd, p1ffd, special := e.mem.GetPortState()
	var sp byte
	if special {
		sp = 1
	}
	win = append(win, p7ffd, p1ffd, sp, e.mem.AltROMReg(), e.mem.DFFDValue())
	add("memory.window", win)

	// The picture. Rendered once per pass, at the same point in each, so the
	// comparison is between two frames the machine composed from equivalent
	// state — not between a frame and a stale buffer.
	if e.ula != nil {
		add("video.frame", e.ula.Render().Pix)
	}
	return out
}

// diffFingerprints returns the observables that differ between two passes, in
// fingerprint order. It reports rather than asserts, so the same comparison
// serves the oracle (where a difference is a failure) and the mutation test
// (where a difference is the expected result).
func diffFingerprints(first, second []namedDigest) []namedDigest {
	var out []namedDigest
	for i := range first {
		if i >= len(second) || first[i].name != second[i].name {
			panic("replay oracle: fingerprint shape changed between passes")
		}
		if first[i].sum != second[i].sum {
			out = append(out, first[i])
		}
	}
	return out
}

// compareFingerprints reports every observable that differs, and returns
// whether they matched.
func compareFingerprints(t *testing.T, first, second []namedDigest) bool {
	t.Helper()
	if len(first) != len(second) {
		t.Fatalf("fingerprint shape changed between passes: %d observables then %d",
			len(first), len(second))
	}
	diff := diffFingerprints(first, second)
	for _, d := range diff {
		t.Errorf("DIVERGED: %s (%d bytes) — replaying from the captured state "+
			"produced a different %s", d.name, d.size, d.name)
	}
	return len(diff) == 0
}

// reportCPUDivergence names the exact instruction the two passes parted at.
func reportCPUDivergence(t *testing.T, a, b []byte) {
	t.Helper()
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i+traceEntryBytes <= n; i += traceEntryBytes {
		ea, eb := a[i:i+traceEntryBytes], b[i:i+traceEntryBytes]
		if bytes.Equal(ea, eb) {
			continue
		}
		t.Errorf("CPU state first differs after instruction %d of %d:\n  first pass:  %s\n  replay:      %s",
			i/traceEntryBytes, len(a)/traceEntryBytes, decodeTraceEntry(ea), decodeTraceEntry(eb))
		return
	}
	if len(a) != len(b) {
		t.Errorf("CPU traces differ in length: %d vs %d instructions",
			len(a)/traceEntryBytes, len(b)/traceEntryBytes)
	}
}

// runOracle performs the whole property on one machine and reports what
// differed. Returns true when the replay was equivalent.
func runOracle(t *testing.T, model roms.SpectrumModel) bool {
	t.Helper()
	emu := quietEmulator(t, model)
	oracleBoot(emu, model, oracleBootFrames)

	reg := emu.stateRegistry()
	captured := reg.Capture()
	t.Logf("captured %d devices, %d bytes: %v",
		reg.Len(), captured.Size(), captured.Devices())

	first := replayRun(emu, model, oracleWindow(emu))
	f1 := observeMachine(emu, first)

	if err := reg.Restore(captured); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	second := replayRun(emu, model, oracleWindow(emu))
	f2 := observeMachine(emu, second)

	ok := compareFingerprints(t, f1, f2)
	if !bytes.Equal(first.cpuTrace, second.cpuTrace) {
		reportCPUDivergence(t, first.cpuTrace, second.cpuTrace)
	}
	return ok
}

var oracleModels = []roms.SpectrumModel{
	roms.Model48K, roms.Model128K, roms.ModelPlus2, roms.ModelPlus2A, roms.ModelPlus3,
}

// The oracle proper: the machine's own capture, nothing added.
//
// This is the test that decides whether the capture effort worked. A failure
// names the observable that could not be reproduced and, for the CPU, the exact
// instruction the two passes parted at — which is the pointer to the device
// that was not captured, because whatever the guest touched just before that
// instruction is what came back holding the wrong value.
func TestReplayEquivalence(t *testing.T) {
	for _, model := range oracleModels {
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			runOracle(t, model)
		})
	}
}

func TestReplayEquivalenceNext(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	runOracle(t, roms.ModelNext)
}

// --- does the oracle bite? -----------------------------------------------
//
// A green oracle means one of two things: the capture is complete, or the
// measurement is blunt. There is no way to tell them apart from the green.
// These tests tell them apart, by taking a machine whose capture IS complete
// and deliberately leaving one device behind — the exact failure the oracle
// exists to catch — and requiring the oracle to catch it.
//
// The mutation is applied at the Device level rather than by rebuilding the
// registry, because "this device was not rewound" and "this device was never in
// the capture" are the same machine: everything else goes back to the captured
// point, and one device carries on from the future. The stale blob is taken at
// the END of the first pass, so it is exactly the present-day state a forgotten
// device would still be holding.

// staleAfterRestore runs the property with one device deliberately left in the
// future, and returns the observables that diverged.
func staleAfterRestore(t *testing.T, model roms.SpectrumModel, pick func(*emulator) machinestate.Device) []string {
	t.Helper()
	emu := quietEmulator(t, model)
	oracleBoot(emu, model, oracleBootFrames)

	reg := emu.stateRegistry()
	captured := reg.Capture()

	first := replayRun(emu, model, oracleWindow(emu))
	f1 := observeMachine(emu, first)

	victim := pick(emu)
	stale := victim.SaveState()

	if err := reg.Restore(captured); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := victim.LoadState(stale); err != nil {
		t.Fatalf("re-applying %s's present-day state: %v", victim.StateID(), err)
	}

	second := replayRun(emu, model, oracleWindow(emu))
	f2 := observeMachine(emu, second)

	var diverged []string
	for i := range f1 {
		if f1[i].sum != f2[i].sum {
			diverged = append(diverged, f1[i].name)
		}
	}
	return diverged
}

// Every device the exerciser actually drives must be detectable by its absence.
//
// A device that does NOT show up here is not proof of a complete capture — it
// is proof that the fixture never made the machine use it, and therefore that
// the oracle says nothing about that device. That distinction is the whole
// reason this test lists the observable it expects as well as the device.
//
// The three cases below are the ones the fixture drives. Measured across the
// whole registry, the rest come out invisible and the oracle is silent about
// them: `keyboard` (no key is ever pressed, so its captured state and its
// present-day state are the same bytes), `audiodac` (SpecDrum and Covox are
// both off, so nothing writes it) and `plus3fdc` (no disk is inserted and no
// command is issued). Each would need the fixture to drive it before the
// oracle could say anything either way.
//
// It also matters WHICH observable catches each device, not just that one does.
// The ULA case was silent on video.frames until two fixture bugs were fixed —
// the guest never reached the attribute area, so no attribute carried a FLASH
// bit and the flash phase could not reach a pixel; and only the final frame was
// composed, by which point both passes had painted the same picture. Both were
// found by this test failing, which is the only reason they are not still there
// making the oracle look stronger than it was.
func TestReplayOracleDetectsAnUncapturedDevice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model roms.SpectrumModel
		pick  func(*emulator) machinestate.Device
		want  string
	}{
		{"cpu", roms.Model128K, func(e *emulator) machinestate.Device { return e.cpu }, "cpu.trace"},
		{"memory", roms.Model128K, func(e *emulator) machinestate.Device { return e.mem }, "ram.pool"},
		{"ula", roms.Model128K, func(e *emulator) machinestate.Device { return e.ula }, "video.frames"},
		{"ay", roms.Model128K, func(e *emulator) machinestate.Device { return e.ula.AY() }, "audio.stream"},
		{"ay.turbosound", roms.ModelNext, func(e *emulator) machinestate.Device { return e.nextAY }, "audio.stream"},
		{"next.sprite", roms.ModelNext, func(e *emulator) machinestate.Device { return e.nextSprites }, "video.frames"},
		{"next.palette", roms.ModelNext, func(e *emulator) machinestate.Device { return e.nextPalette }, "video.frames"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.model == roms.ModelNext && !nextROMsInstalled() {
				t.Skip("Next ROMs not installed")
			}
			diverged := staleAfterRestore(t, tc.model, tc.pick)
			if len(diverged) == 0 {
				t.Fatalf("leaving %s un-rewound changed nothing the oracle can see — "+
					"the fixture never makes the machine use it, so the oracle proves "+
					"nothing about %s's capture", tc.name, tc.name)
			}
			found := false
			for _, d := range diverged {
				if d == tc.want {
					found = true
				}
			}
			if !found {
				t.Errorf("leaving %s un-rewound diverged %v, but not %q — "+
					"the observable meant to cover this device does not",
					tc.name, diverged, tc.want)
			}
		})
	}
}
