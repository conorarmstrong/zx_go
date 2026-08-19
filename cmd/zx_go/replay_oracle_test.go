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
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
	"github.com/conorarmstrong/zx_go/pkg/next/nex"
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
	// dac is the Next DAC bank's mixed level on the same schedule. It is a
	// separate stream from audio.stream because it is a separate device and a
	// failure has to name the one that broke; it is the mean of the four
	// channels, which is the only thing the audio mixer ever reads from the
	// bank, so a bank restored with one channel missing shows up here.
	dac []byte
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
// The tail — everything from the DAC block down — is what makes the oracle able
// to say anything at all about the copper, the zxnDMA, the DAC bank and Layer 2.
// Before it, those four devices never moved while the machine ran, so leaving
// any of them un-rewound produced a machine byte-identical to the rewound one
// and the oracle's green was silence rather than evidence. Each block is built
// around what that device's capture would LOSE, and around the two ways a
// difference disappears before it can be seen (see machinestate's "Writing a
// capture test"): a value the guest restates every iteration has converged long
// before the next frame is composed, and an even number of writes puts a phase
// back where it started.
//
//   - The four DACs are written to four DIFFERENT levels, none of them the
//     mixer's centre, so a bank restored with three channels of four shows up in
//     the mixed level — the only thing the audio path ever reads from it.
//
//   - The copper block streams instruction bytes into the copper's WRITE
//     CURSOR, the same trick the palette and sprite streams use above: the
//     cursor is set once, before the run, so a stale one lands every subsequent
//     byte at a different program address and the two passes run permanently
//     different copper lists. Every byte is either >= $80 or $4B, which is not
//     cosmetic: if the two-byte pairing phase is off by one (exactly what a lost
//     hiSet does), the bytes re-pair as (value, register) and a byte < $80 would
//     become a MOVE to whatever NextReg its value names — $02 is the reset
//     register and $50-$57 are the MMU. Bytes >= $80 re-pair as WAITs, which are
//     inert, and $4B is the sprite transparency index, which is not.
//
//   - The DMA block re-issues ENABLE every iteration, and then re-writes the
//     block length as a TWO-BYTE command: a WR0 base byte announcing a follow
//     byte, and the follow byte itself. The transfer is programmed as burst mode
//     with a prescaler (oracleDMAProgram), so it moves one byte every 64
//     T-states from the CPU's per-instruction Step hook and is therefore
//     MID-BLOCK at almost every instruction boundary a capture can be taken at.
//     ENABLE while a burst is still draining is ignored by the chip, so
//     re-arming every time round the loop simply keeps the block running. The
//     two-byte command is there so that a capture can land BETWEEN the base byte
//     and the byte it announced, which is the one moment the chip's follow-byte
//     queue is non-empty — the state its own doc comment says a capture lands in
//     "as often as not", and which nothing else in the fixture would ever
//     produce.
//
//   - The copper's MOVE carries counter PLUS the slow counter, and both halves
//     are there for a measured reason. With the value driven by the fast counter
//     alone the streamed list came out exactly periodic against the write
//     cursor's own wrap — 1024 words is 512 iterations is two whole counter
//     cycles — so the instruction memory converged to the same 1024 words on
//     every wrap and leaving the whole 2 KB program un-restored changed nothing
//     at all. With it driven by the slow counter alone the list stopped being
//     periodic but the value stopped moving within a frame, and one value that
//     happens not to matter makes the whole channel silent. The sum moves every
//     iteration AND drifts, so neither failure is available.
//
//   - The MOVE writes NR$41, a palette entry, and the two registers it is not
//     writing are both worth recording. NR$4B, the sprite transparency index,
//     was the first choice and was nearly silent: the copper demonstrably wrote
//     two different values in the two passes ($80 against $C3, seen at every
//     composed frame) and the frames came out identical, because whether a
//     sprite transparency index matters depends on whether any drawn sprite
//     pixel happens to carry it. NR$14, the global transparency index, was the
//     second, and it worked — it broke TestReplayEquivalenceNext, because
//     pkg/next/compositor keeps its OWN copy of NR$14 (SetTransparency, called
//     from the register's write handler) and nothing captures it. Restoring the
//     NextReg file puts the byte back without re-running the handler, so the
//     compositor composes the frames after a rewind from the value it held
//     before it. That is a real hole in the capture and not this fixture's to
//     fix; it is reported rather than papered over, and re-applying NR$14's
//     handler after the restore makes the property hold, which is the one-line
//     confirmation. The same argument covers NR$15/$4A/$4B/$4C/$68, which are
//     mirrored the same way. NR$41 was chosen instead because a palette write
//     lands in pkg/next/palette, which IS captured, so the copper's channel to
//     the picture runs entirely through state the registry knows about.
//
//   - The Layer 2 and tilemap scrolls are written once every 4096 iterations,
//     not every one. Both layers are register mirrors with no cursor and no
//     phase — every field is the value the guest last wrote — so the ONLY thing
//     that can catch a stale one is a frame composed before the guest writes the
//     register again. At one write per iteration that window is a few hundred
//     T-states and no frame falls inside it; at one per sixteen slow ticks it
//     spans several frames and one always does. The rate is a real constraint
//     rather than a preference: raising it to once per 1024 iterations put the
//     next write inside the same frame as the capture and both layers went
//     undetectable again.
//
//     LD A,(counter)      3A 00 81
//     INC A               3C
//     LD (counter),A      32 00 81
//     OUT ($FE),A         D3 FE      ; border + MIC + speaker
//     AND $0F             E6 0F
//     LD BC,$FFFD         01 FD FF
//     OUT (C),A           ED 79      ; AY register select
//     LD BC,$BFFD         01 FD BF
//     LD A,(counter)      3A 00 81
//     OUT (C),A           ED 79      ; AY register data
//     LD BC,$253B         01 3B 25
//     LD A,(counter)      3A 00 81
//     OUT (C),A           ED 79      ; NextReg data -> the selected register
//     LD A,(counter)      3A 00 81
//     OUT ($57),A         D3 57      ; sprite attribute stream
//     OUT ($57),A         D3 57
//     OUT ($57),A         D3 57
//     OUT ($57),A         D3 57
//     OUT ($5B),A         D3 5B      ; sprite pattern stream
//     OUT ($5B),A         D3 5B
//     OUT ($5B),A         D3 5B
//     OUT ($5B),A         D3 5B
//     LD HL,(cursor)      2A 01 81
//     LD A,(counter)      3A 00 81
//     LD (HL),A           77
//     INC HL              23
//     LD A,H              7C
//     CP $5B              FE 5B
//     JR NZ,+3            20 03
//     LD HL,$5800         21 00 58
//     LD (cursor),HL      22 01 81
//     LD A,(counter)      3A 00 81
//     OUT ($1F),A         D3 1F      ; DAC channel A = c
//     ADD A,$33           C6 33
//     OUT ($0F),A         D3 0F      ; DAC channel B = c+$33
//     ADD A,$33           C6 33
//     OUT ($4F),A         D3 4F      ; DAC channel C = c+$66
//     ADD A,$33           C6 33
//     OUT ($FB),A         D3 FB      ; DAC channel D = c+$99
//     LD A,$80            3E 80
//     NEXTREG $60,A       ED 92 60   ; copper: WAIT, high byte
//     LD A,(counter)      3A 00 81
//     AND $3F             E6 3F
//     OR $80              F6 80
//     NEXTREG $60,A       ED 92 60   ; ... WAIT for line 128..191
//     LD A,$41            3E 41
//     NEXTREG $60,A       ED 92 60   ; copper: MOVE NR$41, high byte
//     LD HL,slow          21 03 81
//     LD A,(counter)      3A 00 81
//     ADD A,(HL)          86
//     OR $80              F6 80
//     NEXTREG $60,A       ED 92 60   ; ... palette colour 128..255
//     LD A,$87            3E 87
//     OUT ($6B),A         D3 6B      ; zxnDMA ENABLE (re-arms a finished block)
//     LD A,$25            3E 25
//     OUT ($6B),A         D3 6B      ; WR0: A->B, block-length low byte follows
//     LD A,(counter)      3A 00 81
//     OUT ($6B),A         D3 6B      ; ... the announced follow byte
//     LD A,(counter)      3A 00 81
//     OR A                B7
//     JR NZ,+28           20 1C      ; every 256th pass round the loop...
//     LD A,(slow)         3A 03 81
//     INC A               3C
//     LD (slow),A         32 03 81
//     AND $0F             E6 0F
//     JR NZ,+17           20 11      ; ... and every sixteenth of those:
//     LD A,(scroll)       3A 04 81
//     ADD A,$0B           C6 0B
//     LD (scroll),A       32 04 81
//     NEXTREG $17,A       ED 92 17   ; Layer 2 Y scroll
//     NEXTREG $31,A       ED 92 31   ; tilemap Y scroll
//     NEXTREG $6C,A       ED 92 6C   ; tilemap default attribute
//     IN A,($FE)          DB FE
//     JP $8000            C3 00 80
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
	0x3A, 0x00, 0x81,
	0xD3, 0x1F,
	0xC6, 0x33,
	0xD3, 0x0F,
	0xC6, 0x33,
	0xD3, 0x4F,
	0xC6, 0x33,
	0xD3, 0xFB,
	0x3E, 0x80,
	0xED, 0x92, 0x60,
	0x3A, 0x00, 0x81,
	0xE6, 0x3F,
	0xF6, 0x80,
	0xED, 0x92, 0x60,
	0x3E, 0x41,
	0xED, 0x92, 0x60,
	0x21, 0x03, 0x81,
	0x3A, 0x00, 0x81,
	0x86,
	0xF6, 0x80,
	0xED, 0x92, 0x60,
	0x3E, 0x87,
	0xD3, 0x6B,
	0x3E, 0x25,
	0xD3, 0x6B,
	0x3A, 0x00, 0x81,
	0xD3, 0x6B,
	0x3A, 0x00, 0x81,
	0xB7,
	0x20, 0x1C,
	0x3A, 0x03, 0x81,
	0x3C,
	0x32, 0x03, 0x81,
	0xE6, 0x0F,
	0x20, 0x11,
	0x3A, 0x04, 0x81,
	0xC6, 0x0B,
	0x32, 0x04, 0x81,
	0xED, 0x92, 0x17,
	0xED, 0x92, 0x31,
	0xED, 0x92, 0x6C,
	0xDB, 0xFE,
	0xC3, 0x00, 0x80,
}

// oracleDMAProgram is the zxnDMA command stream the setup writes to port $6B.
//
// It is a BURST transfer with a prescaler, and that is the whole point: burst +
// prescaler is the one configuration the chip does not finish inside the ENABLE
// (dma.vhd's only case where burst yields the bus). It moves one byte every 64
// T-states from the CPU's per-instruction Step hook, so a 256-byte block takes
// about twenty times round the exerciser's loop and a capture taken between two
// instructions almost always lands MID-BLOCK — with activeBurst set, bytes
// outstanding and an absolute due-time. Those three are what a capture of a
// finished transfer could never contain.
//
// Port A is FIXED at the exerciser's counter and port B walks 256 bytes of
// scratch, so the block does not copy a region: it writes a LOG of what the
// counter held at each of the 256 moments the chip moved a byte. That is
// deliberate. A block that copies one region to another converges — both passes
// end with the same bytes in the same places however the transfer was paced —
// and the first version of this fixture, copying the attribute area, diverged
// nothing for exactly that reason. Logging a moving value against the
// transfer's own clock puts the PACING into RAM, which is the thing a lost
// capture changes.
//
// Auto-restart (WR5 D5) reloads the addresses at end of block so the loop's
// ENABLE starts the next one; the destination is scratch RAM above the
// exerciser that nothing else on the machine writes.
var oracleDMAProgram = []byte{
	0xC3,       // RESET: known controller state
	0x7D,       // WR0: A->B, port A address and block length follow
	0x00, 0x81, // port A start $8100 — the exerciser's counter
	0x00, 0x01, // block length $0100
	0x24,       // WR1: port A is memory at a FIXED address
	0x50,       // WR2: port B is memory, incrementing, timing byte follows
	0x22,       // ... 2-cycle port, prescaler byte follows
	0x40,       // ... prescaler: one byte every 64 T-states
	0xCD,       // WR4: burst mode, port B address follows
	0x00, 0x82, // port B start $8200 — scratch above the exerciser
	0xA2, // WR5: auto-restart at end of block
	0xCF, // LOAD
	0x87, // ENABLE
}

// oracleNextSetup arms the cursors the Next exerciser streams into, makes the
// sprite engine visible so the sprite stream reaches the picture, programs the
// zxnDMA block the loop keeps re-arming, and switches on the two layers whose
// scroll the loop moves. Done once, before the run, precisely so the loop never
// restates any of it.
//
// Three of these were found by the fixture failing to prove anything without
// them:
//
//   - NR$62 STARTS the copper. Its reset start mode is "stop", so without this
//     the loop's stream filled the instruction memory — the capture moved, the
//     write cursor advanced — and not one MOVE ever executed. A device whose
//     state moves is not the same thing as a device the machine is using.
//   - NR$12 points Layer 2 at bank 5, the ULA's own screen, rather than at its
//     default bank 8. Banks 8 and up are still zero on a machine that has only
//     booted, and a uniform layer draws the same picture at every scroll offset,
//     so a Layer 2 restored to the wrong scroll would compose an identical frame.
//     Pointed at the screen it reads a picture the guest is actively changing.
//   - NR$6B enables the tilemap, which NextZXOS leaves off ($6B = $00 after the
//     boot). Its map and tile bases are left at what the boot left them, which
//     is real content.
func oracleNextSetup(e *emulator) {
	if e.nextRegs != nil {
		e.nextRegs.WriteReg(0x15, 0x03) // sprites visible, and over the border
		e.nextRegs.WriteReg(0x40, 0x00) // palette write index to the start
		e.nextRegs.WriteReg(0x12, 0x05) // Layer 2 reads the ULA screen bank
		e.nextRegs.WriteReg(0x69, 0x80) // Layer 2 on
		e.nextRegs.WriteReg(0x6B, 0xA1) // tilemap on, 40 columns, over the ULA
		e.nextRegs.WriteReg(0x61, 0x00) // copper write cursor to instruction 0
		e.nextRegs.WriteReg(0x62, 0xC0) // ... and START it, restarting every VBL
	}
	if e.ula != nil {
		e.ula.WritePort(0x243B, 0x41) // select NR$41: palette value, auto-incrementing
		e.ula.WritePort(0x303B, 0x00) // sprite slot 0, attribute cursor at its first byte
		for _, b := range oracleDMAProgram {
			e.ula.WritePort(0x006B, b)
		}
	}
}

// oracleExerciserVars is where the exerciser keeps its counter, its display
// cursor and (on the Next) the slow counter and scroll position that pace the
// Layer 2 and tilemap writes.
// It is clear of the longest program — the Next one is 171 bytes — so the loop
// cannot overwrite its own code.
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
	e.mem.Write(oracleExerciserVars+3, 0x00) // slow counter (Next: paces the scrolls)
	e.mem.Write(oracleExerciserVars+4, 0x00) // Layer 2 / tilemap scroll position
	e.cpu.PC = oracleExerciserOrigin
	for i := 0; i < 8; i++ {
		runOneFrameHeadless(e, model)
		if e.ula != nil {
			e.ula.Render()
		}
	}
}

// --- the vendored-software fixture ---------------------------------------
//
// The exerciser above is ours. This fixture is somebody else's program: the
// same property, measured on a machine being driven by third-party software
// that knows nothing about this test. zxnext_tilemap is Ben Baker's
// MIT-licensed Next demo, already vendored in pkg/testharness/testdata/corpus
// with its licence text and hash (see that directory's CORPUS.md). It puts up a
// tilemap and scrolls it while W, S or D is held; with D held it moves the
// tilemap's scroll registers and composes a different frame every frame.
//
// What this fixture is NOT is the tilemap's coverage, and the reason is worth
// recording because it looked like it would be. A demo scrolls its layer EVERY
// frame, and the tilemap is a register mirror with no cursor and no hidden
// phase — so a tilemap left un-rewound is overwritten by the demo's own next
// scroll write before the next frame is composed, and every composed frame
// comes out identical. Measured: leaving next.tilemap stale under this fixture
// diverges NOTHING. It is the exerciser, writing its scroll once every sixteen
// slow ticks instead of once a frame, that leaves a stale value standing long
// enough for a frame to be composed from it. That is the general shape of it: a
// device the guest restates faster than the display is sampled cannot be caught
// by watching the display, however busy the guest looks.
//
// Two details of the fixture were found by measuring rather than by reasoning,
// and both are load-bearing:
//
//   - The demo is loaded ON TOP of a booted machine, not into a cold one.
//     Loaded cold — banks, PC, SP, entry bank, exactly as pkg/testharness's
//     LoadNEX does it — it runs (its own code is at the PC) but composes a
//     single flat colour here, so there is nothing for a stale device to change.
//     Loaded after the OS has configured the machine it renders.
//   - The key is pressed AFTER the demo has initialised. Held from the start it
//     scrolls nothing at all: the demo samples the keyboard as it comes up.
//
// The corpus's other two Next demos are not used here and the measurements say
// why. zxnext_layer2_tilemap does move Layer 2 (its scroll advances four pixels
// a frame with D held) but composes a single flat colour in this emulator, both
// cold and after an OS boot, while the same file renders a full scene under
// pkg/testharness — worth a look on its own account, but it leaves nothing for a
// stale Layer 2 to change. SpecBong moves only its sprite engine. Across 1200
// frames and all forty keys, none of the three ever moves the tilemap's or
// Layer 2's registers except as described here.
const oracleDemoNEX = "zxnext_tilemap.nex"

// oracleDemoKeyRow / oracleDemoKeyMask are the 'D' key: matrix row 1, bit 2.
const (
	oracleDemoKeyRow  = 1
	oracleDemoKeyMask = 0x04
)

// oracleDemoFrames is how long the demo is given to come up before the key goes
// down. The corpus golden uses 250 for the same program.
const oracleDemoFrames = 250

// oracleLoadNEX pages a .nex into the machine in front of it: every bank it
// carries, then SP, PC and the entry bank — the same ROM-independent load
// pkg/testharness performs, rather than NextZXOS's NEXLOAD.
func oracleLoadNEX(t *testing.T, e *emulator, path string) {
	t.Helper()
	n, err := nex.ParseFile(path)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for bank, data := range n.Banks {
		page := e.mem.GetPage(bank)
		if page == nil {
			t.Fatalf("%s: bank %d is not allocated on this machine", path, bank)
		}
		copy(page, data)
	}
	e.cpu.SP = n.Header.SP
	e.cpu.PC = n.Header.PC
	e.mem.PageMemory(n.Header.EntryBank & 0x07)
}

// oracleTilemapDemoMachine boots the machine, loads the demo over it, lets it
// come up, and then holds the scroll key down for the rest of the run.
func oracleTilemapDemoMachine(t *testing.T, model roms.SpectrumModel) *emulator {
	t.Helper()
	if model != roms.ModelNext {
		t.Fatalf("the demo fixture is a Next program, not a %s one", roms.GetModelName(model))
	}
	emu := quietEmulator(t, model)
	emu.paused.Store(false)
	run := func(frames int) {
		for i := 0; i < frames; i++ {
			runOneFrameHeadless(emu, model)
			emu.ula.Render()
		}
	}
	run(oracleBootFrames)
	oracleLoadNEX(t, emu, filepath.Join("..", "..", "pkg", "testharness",
		"testdata", "corpus", "bin", oracleDemoNEX))
	run(oracleDemoFrames)
	emu.kbd.PressMatrixKey(oracleDemoKeyRow, oracleDemoKeyMask, true)
	run(8)
	return emu
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
		// Stereo, so a replay that lost the engine's NR$08/NR$09 panning is
		// visible. The mono MixInto ignores the mode by design (see
		// pkg/ay's TestTheMonoMixIsUnaffectedByThePanningMode), so
		// fingerprinting through it could never catch that divergence.
		e.nextAY.MixIntoStereo(buf)
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
		if e.nextDAC != nil {
			// Both sides: the bank is two stereo pairs, so a fingerprint of
			// one of them would not notice a replay that lost the other.
			tr.dac = append(tr.dac, e.nextDAC.LevelL(), e.nextDAC.LevelR())
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
	add("audio.dac", tr.dac)
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

// oracleMachine is the default fixture: a freshly built machine booted to its
// keyboard-scan loop and then given the exerciser to run.
func oracleMachine(t *testing.T, model roms.SpectrumModel) *emulator {
	t.Helper()
	emu := quietEmulator(t, model)
	oracleBoot(emu, model, oracleBootFrames)
	return emu
}

// runOracle performs the whole property on one machine and reports what
// differed. Returns true when the replay was equivalent.
func runOracle(t *testing.T, model roms.SpectrumModel, build func(*testing.T, roms.SpectrumModel) *emulator) bool {
	t.Helper()
	emu := build(t, model)

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
			runOracle(t, model, oracleMachine)
		})
	}
}

func TestReplayEquivalenceNext(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	runOracle(t, roms.ModelNext, oracleMachine)
}

// The same property with third-party software driving the machine instead of
// our own exerciser: a vendored MIT Next demo, running and being scrolled.
func TestReplayEquivalenceNextDemo(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	runOracle(t, roms.ModelNext, oracleTilemapDemoMachine)
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
func staleAfterRestore(t *testing.T, model roms.SpectrumModel, build func(*testing.T, roms.SpectrumModel) *emulator, pick func(*emulator) machinestate.Device) []string {
	t.Helper()
	emu := build(t, model)

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
// The cases below are the ones the fixtures drive. Measured across the whole
// registry, the rest come out invisible and the oracle is silent about them:
// `keyboard` (the only key ever pressed is held down for the whole run, so its
// captured state and its present-day state are the same bytes), `audiodac`
// (SpecDrum and Covox are both off, so nothing writes it) and `plus3fdc` (no
// disk is inserted and no command is issued). Each would need the fixture to
// drive it before the oracle could say anything either way.
//
// It also matters WHICH observable catches each device, not just that one does.
// The ULA case was silent on video.frames until two fixture bugs were fixed —
// the guest never reached the attribute area, so no attribute carried a FLASH
// bit and the flash phase could not reach a pixel; and only the final frame was
// composed, by which point both passes had painted the same picture. Both were
// found by this test failing, which is the only reason they are not still there
// making the oracle look stronger than it was.
//
// The five Next blocks below the sprite engine are the ones the exerciser's
// tail and the demo fixture were built for. Each one names why its observable is
// the one that catches it:
//
//   - next.copper streams a permanently different instruction list, whose MOVEs
//     set the sprite transparency index at scattered raster lines, so the
//     composed frames differ.
//   - next.dma is caught in ram.pool, and the mechanism is worth knowing: the
//     burst engine's due-time is an ABSOLUTE reading of the CPU clock, so a
//     controller left in the future is not merely a few bytes out of step — its
//     next byte is due after a time the rewound CPU has not reached, and the
//     transfer stops dead while the block it was part way through stays armed.
//     The destination region is then never written again.
//   - next.dac is the mixed level of its four channels, the only thing the audio
//     path reads from the bank.
//   - next.layer2 and next.tilemap are register mirrors with no cursor and no
//     hidden phase, so the only thing that can catch them is a frame composed
//     from a scroll value the guest has not yet overwritten. The exerciser
//     writes both scrolls once every sixteen slow ticks — about five frames —
//     for that reason alone; at one write per frame (which is what a real demo
//     does, and what the first version of this fixture did) the stale value is
//     gone before the next frame is composed and the oracle sees nothing. If a
//     later retune of the window or the loop closes that gap, these two cases go
//     red rather than quietly stopping proving anything.
//
// Passing here means the device is covered. It does not mean every FIELD of its
// capture is, and the difference is worth writing down because the next person
// to score this will otherwise measure it again. Deleting one field's restore in
// each device's LoadState and re-running this test and TestReplayEquivalenceNext
// kills: the copper's write cursor; the DMA's port-B pointer, outstanding count
// and next-due time; the DAC's four channel levels; Layer 2's Y scroll; and the
// tilemap's Y scroll and default attribute. Eight of fifty-four. Every survivor
// is a field this fixture does not MOVE — the DMA's latched configuration and
// the two layers' bases and clip windows are written once in setup and never
// again, and a field that does not differ between the capture and the end of the
// window has nothing for a deleted restore to get wrong. Two survivors are worth
// naming because they are not merely unmoved:
//
//   - the copper's instruction memory. The stream rewrites all 1024 words in
//     about six tenths of a frame, so a program left in the future has healed
//     before the next frame is composed. Slowing the stream to make it survive a
//     composition was tried and cost the Layer 2 and tilemap cases, whose
//     detection depends on the loop's length through the write phase.
//   - the DAC's event list and carried level. pkg/ula only records a DAC event
//     when an audio system is attached (ULA.WritePort), and these tests run with
//     --no-sound, so the event-timed half of that capture cannot be reached from
//     here at all. pkg/next/dac's own state test is what covers it.
func TestReplayOracleDetectsAnUncapturedDevice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model roms.SpectrumModel
		build func(*testing.T, roms.SpectrumModel) *emulator
		pick  func(*emulator) machinestate.Device
		want  string
	}{
		{"cpu", roms.Model128K, oracleMachine, func(e *emulator) machinestate.Device { return e.cpu }, "cpu.trace"},
		{"memory", roms.Model128K, oracleMachine, func(e *emulator) machinestate.Device { return e.mem }, "ram.pool"},
		{"ula", roms.Model128K, oracleMachine, func(e *emulator) machinestate.Device { return e.ula }, "video.frames"},
		{"ay", roms.Model128K, oracleMachine, func(e *emulator) machinestate.Device { return e.ula.AY() }, "audio.stream"},
		{"ay.turbosound", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextAY }, "audio.stream"},
		{"next.sprite", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextSprites }, "video.frames"},
		{"next.palette", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextPalette }, "video.frames"},
		{"next.copper", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextCopper }, "video.frames"},
		{"next.dma", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextDMA }, "ram.pool"},
		{"next.dac", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextDAC }, "audio.dac"},
		{"next.layer2", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextLayer2 }, "video.frames"},
		{"next.tilemap", roms.ModelNext, oracleMachine, func(e *emulator) machinestate.Device { return e.nextTilemap }, "video.frames"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.model == roms.ModelNext && !nextROMsInstalled() {
				t.Skip("Next ROMs not installed")
			}
			diverged := staleAfterRestore(t, tc.model, tc.build, tc.pick)
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
