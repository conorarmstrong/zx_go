package testharness

import (
	"fyne.io/fyne/v2"
	"github.com/conorarmstrong/zx_go/pkg/roms"
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
	return h.attachTape(tp)
}

// LoadTZX is LoadTAP for the TZX container. Most of the canonical Spectrum
// catalogue ships as TZX rather than TAP, because TZX is the format that can
// describe the custom loaders publishers used.
//
// The same fast-load trap applies, and the same caveat with it: a title whose
// loader bypasses the ROM's LD-BYTES never trips the trap and loads from the
// signal in real time instead, so it needs proportionally more frames.
func (h *Harness) LoadTZX(path string) error {
	tp := ula.NewTapePlayer()
	if err := tp.LoadTZX(path); err != nil {
		return err
	}
	return h.attachTape(tp)
}

// attachTape wires a loaded player to the ULA and installs the LD-BYTES trap.
func (h *Harness) attachTape(tp *ula.TapePlayer) error {
	h.ula.SetTapePlayer(tp)
	tp.Play()
	h.resetTapeLoadCounters()

	h.cpu.TrapCheck = func(pc uint16) bool {
		if pc != 0x0556 || !h.tapeTrapROMActive() || !tp.HasMoreBlocks() {
			return false
		}
		block := tp.NextBlock()
		if block == nil {
			return false
		}
		h.tapeBlocks++
		h.tapeLastLoad = h.elapsedT
		// NextBlock advances the cursor past the block it returned, so the
		// block the guest was offered is the one before it. Whether it counts
		// as READ is decided below, once the flag and length have been
		// checked — a block handed over and rejected is a load error, not a
		// block the title loaded.
		offered := tp.CurrentBlock() - 1
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
			// NextBlock only ever returns a data block, so this is really an
			// assertion that the two agree about the population being counted
			// — not a bounds check, which cannot fail here because NextBlock
			// increments the cursor before returning.
			if tp.BlockIsData(offered) {
				h.tapeDecoded[offered] = true
			}
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

// resetTapeLoadCounters puts every signal describing a tape load back to its
// start-of-load value.
//
// It is called when a tape is attached, and again from Reboot. Reboot
// deliberately leaves inserted media mounted, so without this a machine
// rebooted mid-tape carries the previous run's blocks into the new one and
// TapeBlocksDecoded reports the union of the two — a figure the compatibility
// manifest quotes.
func (h *Harness) resetTapeLoadCounters() {
	h.tapeBlocks, h.tapeLastLoad = 0, 0
	h.tapeFEReads, h.tapeLastEdge, h.tapeEdgesSeen = h.ula.FEReadCount(), 0, false
	h.tapeDecoded = map[int]bool{}
}

// tapeTrapROMActive reports whether the 48 BASIC ROM — the one that holds
// LD-BYTES at $0556 — is the ROM currently paged at $0000. The fast-load trap
// may only fire then: on any other ROM, $0556 is unrelated code, and trapping
// it hands a tape block to whatever IX/DE happened to hold.
//
// This mirrors cmd/zx_go's tapeTrapROMActive, so a headless 128K tape run
// behaves the same way as the session a user gets. The Next is the one
// deliberate difference: the GUI's Next runs NextZXOS, where this loader is
// not the tape path at all, whereas the harness boots the Next to 48K BASIC on
// the embedded 48K ROM precisely so tape-loaded conformance programs can run
// without the proprietary OS (see LoadTAP).
func (h *Harness) tapeTrapROMActive() bool {
	switch h.mem.GetCurrentModel() {
	case roms.Model48K:
		return true
	case roms.ModelNext:
		// The Next screens tapes on the embedded 48K ROM, so the trap is
		// wanted — but only while that ROM is actually the one at $0000.
		// Trusting $0556 unconditionally here reintroduced exactly the
		// hazard this function exists to prevent: through the FPGA bootrom
		// chain, NextZXOS, or divMMC RAM paged low, $0556 holds unrelated
		// code, and firing there consumes a tape block, writes it wherever
		// IX/DE happen to point, and returns to a bogus PC popped off the
		// stack. The GUI has always returned false for the Next for this
		// reason; a harness that screens a Next tape needs the narrower
		// test, not the looser one.
		// ROM bank 0 is the embedded 48 BASIC the Next tape screening runs
		// on. Anything else paged low is not a machine with LD-BYTES there.
		return h.mem.GetROMBank() == 0
	case roms.Model128K, roms.ModelPlus2, roms.ModelPentagon:
		return h.mem.GetROMBank() == 1
	case roms.ModelPlus2A, roms.ModelPlus3:
		return h.mem.GetROMBank() == 3
	default:
		return false
	}
}

// TapeBlocksConsumed is how many tape blocks the LD-BYTES trap has injected
// since the tape was attached or the machine was last rebooted — i.e. how many
// times the guest entered the ROM loader and got a block back.
//
// Reboot leaves the tape mounted and its cursor where it was, but resets this
// and every other load counter, because they describe a load the rebooted
// machine did not perform. The consequence to know: after a reboot mid-tape
// the counters cover only the remainder while TapeBlocksTotal still spans the
// whole tape, so the ratio understates. Rewind the player if you need the two
// to describe the same span.
//
// It exists so a differential run can prove both machines consumed the same
// tape before any screen is compared. Two machines that loaded different
// amounts of a tape will differ on screen for reasons that have nothing to do
// with emulation faithfulness, so an unequal count has to void the comparison
// rather than be reported as a divergence.
func (h *Harness) TapeBlocksConsumed() int { return h.tapeBlocks }

// TapeLastLoadTstate is the absolute CPU T-state at which the last block was
// injected, or 0 if none has been. Loading is "finished" when this stops
// advancing — see RunUntilTapeIdle.
func (h *Harness) TapeLastLoadTstate() uint64 { return h.tapeLastLoad }

// tapeEdgeReadsPerFrame is the port-$FE read count in a single frame above
// which the guest is taken to be inside a tape loader's edge-timing loop
// rather than merely scanning the keyboard.
//
// A loader polls the EAR bit hundreds to thousands of times a frame to time
// one edge; the ROM's keyboard scan reads the port eight times a frame. The
// two populations are three orders of magnitude apart, so the exact figure
// matters little — it is the GUI's tapeLoadReadThreshold, kept identical so a
// headless run and the session a user gets agree on when a load is running.
const tapeEdgeReadsPerFrame = ula.LoadReadThreshold

// tapeFrameTick samples the loader-activity signal, once per frame, from the
// ULA's monotonic port-$FE read counter. Called from RunFrames.
//
// This is the only signal that sees a title loading itself. The fast-load trap
// keys on PC == $0556, and a publisher's loader does not go there: Ocean's, on
// the RoboCop and Target Renegade tapes, sets AF' by hand and jumps to $0562 —
// four instructions into the same ROM routine, below the trap point. Every
// block after the BASIC loader then arrives edge by edge in real time, over
// minutes of guest time, without the trap counter moving once.
func (h *Harness) tapeFrameTick() {
	reads := h.ula.FEReadCount()
	rate := reads - h.tapeFEReads
	h.tapeFEReads = reads
	tp := h.ula.GetTapePlayer()
	if tp == nil || rate <= tapeEdgeReadsPerFrame {
		return
	}
	h.tapeEdgesSeen = true
	h.tapeLastEdge = h.elapsedT
	// The guest spent this frame decoding edges, and the block the tape is on
	// is the one it was decoding.
	//
	// The one way this credits a block the guest did not get is a loader that
	// is still in its edge loop as the cursor rolls into the next block and
	// then gives up on it. That guest was trying to read the block, which is
	// the distinction being drawn here — the counter separates "the guest was
	// reading the tape" from "the tape rolled past an idle guest", and a
	// failed read belongs on the first side of that line, not the second.
	//
	// Only blocks that carry bytes, because that is the population the total
	// counts. A turbo TZX interleaves bare signal blocks with its payloads,
	// and crediting those against a data-block total let the published figure
	// exceed 100%.
	//
	// The cursor also sits one past the last block once the tape has run out,
	// and that index is not a block: crediting it reported 19 blocks decoded
	// off an 18-block tape. A guest still hunting for a pilot that will never
	// arrive has not read anything. BlockIsData rejects both.
	if h.tapeDecoded == nil {
		// A tape mounted straight onto the ULA — Harness.ULA() is exported and
		// SetTapePlayer is how the GUI and pkg/ula do it — never went through
		// attachTape, so nothing allocated the set. Writing to a nil map
		// panics inside RunFrames, which is a poor way to learn that.
		h.tapeDecoded = map[int]bool{}
	}
	if blk := tp.CurrentBlock(); tp.BlockIsData(blk) {
		h.tapeDecoded[blk] = true
	}
}

// TapeBlocksDecoded is how many distinct blocks of the tape the guest itself
// read — through the fast-load trap, or by decoding the block's pulses off the
// EAR bit.
//
// It exists because neither of the other two counts answers that question, and
// the compatibility manifest was once written from one of them.
//
// TapeBlocksConsumed counts trap fires, so it cannot see a title's own loader:
// RoboCop traps 6 blocks of its 16 and reads the other ten itself.
//
// TapePlayer.CurrentBlock has the opposite fault. The player is stepped from
// every port-$FE read, so the tape keeps rolling under a guest that is merely
// scanning its keyboard at a menu, and the cursor runs on through blocks
// nothing is reading. That is how R-Type came to be recorded as having loaded
// all 24 of its blocks when it had read 10 and was sitting at its side-change
// prompt.
//
// # This is an upper bound, and there is one way it can be wrong
//
// A block read through the trap is certain: the guest asked, the bytes were
// handed over and the flag matched. A block credited from the read rate is an
// inference — the guest was hammering port $FE while the tape sat on that
// block — and a keyboard poll tight enough to clear the threshold looks
// exactly like a loader's edge loop. Such a guest is credited with every block
// that rolls past it.
//
// What contains that is the load having to fall silent: a guest polling above
// the threshold never lets RunUntilTapeIdle report the load finished, so the
// screening marks it TapeIncomplete and the figure is not one to quote. See
// Screening.TapeBlocksDecoded, which samples at the idle point for exactly
// this reason. The Way of the Tiger is the live example — its menu polls hard
// enough to be indistinguishable from a loader.
//
// Frame granularity is the other limit. The credit goes to whichever block the
// cursor sits on at the end of the frame, so a turbo loader that retires
// several blocks inside one frame is credited with one of them, and a decode
// straddling a frame boundary can be credited to the block after it.
func (h *Harness) TapeBlocksDecoded() int { return len(h.tapeDecoded) }

// tapeLoadStarted reports whether anything has yet read the tape: a block
// through the trap, or the guest decoding edges itself.
func (h *Harness) tapeLoadStarted() bool { return h.tapeBlocks > 0 || h.tapeEdgesSeen }

// tapeLastActivity is the guest T-state of the most recent sign of a load,
// from either path.
func (h *Harness) tapeLastActivity() uint64 {
	if h.tapeLastEdge > h.tapeLastLoad {
		return h.tapeLastEdge
	}
	return h.tapeLastLoad
}

// RunUntilTapeIdle runs the machine until the tape stops being read, and
// reports whether that actually happened.
//
// Idle means a load has started — a block through the trap, or the guest
// reading tape edges for itself — and quietT T-states of guest time have
// passed with no sign of either. That is an event in the guest, not a duration
// guessed in advance, which is what lets two different machines be compared at
// equivalent points: both wait for their own loader to stop.
//
// Both signals are needed. Waiting only on trap fires reports idle the instant
// the last trapped block lands, which on a title that then loads itself is the
// start of the load rather than the end of it: RoboCop trapped 6 blocks of 16
// and its own loader took another ~24000 frames over the remaining ten, all
// of it invisible to the trap counter.
//
// It returns false if deadlineT T-states elapse first — the tape is still
// loading. A false here must void a comparison rather than produce a verdict:
// two machines caught at different points in a load differ on screen for
// reasons that say nothing about faithfulness.
func (h *Harness) RunUntilTapeIdle(quietT, deadlineT uint64) bool {
	deadline := h.elapsedT + deadlineT
	for {
		if h.tapeLoadStarted() && h.elapsedT-h.tapeLastActivity() >= quietT {
			return true
		}
		if h.elapsedT >= deadline {
			return false
		}
		h.RunFrames(1)
	}
}

// StartTapeLoad starts the machine's own tape loader, whichever way this model
// starts one: a 48K needs LOAD"" typed at the K prompt, while the 128/+2 power
// on into a ROM menu whose pre-highlighted first entry is Tape Loader, so
// ENTER alone is the whole sequence. The caller must already have run the
// machine to that prompt or menu.
//
// The 48K sequence appears to work on a 128K, but only by accident: J and
// SYMBOL SHIFT+P do nothing at the menu and the trailing ENTER selects Tape
// Loader. Anything relying on that coincidence breaks the moment the menu
// gains a keystroke.
//
// The +2A/+3 are deliberately not special-cased: their menu's first entry is
// the disk Loader, not a tape loader, so there is no single keystroke that
// starts a tape and pretending otherwise would start something else.
func (h *Harness) StartTapeLoad() bool {
	switch h.mem.GetCurrentModel() {
	case roms.Model128K, roms.ModelPlus2, roms.ModelPentagon:
		h.TapKey(fyne.KeyReturn)
		return true
	case roms.ModelPlus2A, roms.ModelPlus3:
		// Refuse rather than do something else. The doc above says these are
		// deliberately not special-cased because their menu's first entry is
		// the disk Loader, but falling through to TypeLoadCommand did not
		// leave them alone: its trailing ENTER lands on that pre-highlighted
		// Loader and starts a DISK load. The caller then cannot tell "the
		// tape never started" from "the tape loader is broken", which is the
		// worst of both. Say so instead.
		return false
	case roms.Model48K:
		h.TypeLoadCommand()
		return true
	case roms.ModelNext:
		// The harness boots the Next to 48 BASIC on the embedded ROM precisely
		// so tape-loaded conformance programs can run without the proprietary
		// OS (see LoadTAP), and the corpus ships zilogdma.tap as a Next entry.
		// So the 48K sequence is right here, but only while that ROM is the
		// one paged low: through the bootrom chain or NextZXOS there is no
		// 48-BASIC prompt to type at. Same test the fast-load trap gates on.
		if h.mem.GetROMBank() != 0 {
			return false
		}
		h.TypeLoadCommand()
		return true
	default:
		// ZX80/ZX81, SAM: no classic 48-BASIC tape prompt to type at.
		return false
	}
}

// TypeLoadCommand types `LOAD""` + ENTER at the 48K BASIC prompt (K cursor):
// J = the LOAD keyword, SymbolShift+P = ", twice, then ENTER. Each key is held
// a few frames and released with a gap so the ROM's ~50 Hz keyboard scan
// registers distinct presses (the two quotes especially need the gap).
func (h *Harness) TypeLoadCommand() {
	tapKey := func(k fyne.KeyName) {
		h.PressKey(k)
		h.RunFrames(4)
		h.ReleaseKey(k)
		h.RunFrames(8)
	}
	tapKey(fyne.KeyJ) // LOAD
	h.PressSymbolShift(true)
	h.RunFrames(2)
	tapKey(fyne.KeyP) // "
	tapKey(fyne.KeyP) // "
	h.PressSymbolShift(false)
	h.RunFrames(4)
	tapKey(fyne.KeyReturn)
}
