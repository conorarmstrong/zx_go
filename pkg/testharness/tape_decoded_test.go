package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The keyboard-poll loop in the key-wait loader below: the guest is idle at
// the tape exactly while its PC is inside this range.
const (
	keyWaitLoop    = 0x801B // first instruction of the loop
	keyWaitLoopEnd = 0x8025 // last instruction of it
)

// keyWaitCode loads a single block through LD-BYTES and then waits for SPACE,
// polling the keyboard at the rate a menu does rather than spinning on the
// port. The rate matters: the harness tells a loader from a keyboard scan by
// how hard the guest hits port $FE, and a title that spun on it would be
// indistinguishable from one decoding tape edges. This one polls ~80 times a
// frame, well under ula.LoadReadThreshold, exactly as a real menu does.
//
//	8000  DI
//	8001  LD SP,$BFF0
//	8004  XOR A
//	8005  LD ($9000),A        ; blocks-loaded counter
//	8008  LD IX,$C000
//	800C  LD A,$FF
//	800E  SCF                 ; carry = LOAD
//	800F  LD DE,$0010
//	8012  CALL $0556
//	8015  JR NC,fail
//	8017  LD HL,$9000
//	801A  INC (HL)
//	801B  wait: LD B,$40      ; ~830 T of delay, so the poll rate is a menu's
//	801D  w1:   DJNZ w1
//	801F        LD A,$7F      ; keyboard row B/N/M/SymShift/SPACE
//	8021        IN A,($FE)
//	8023        AND $01       ; SPACE, active low
//	8025        JR NZ,wait
//	8027  done: JR done
//	8029  fail: JR fail
var keyWaitCode = []byte{
	0xF3, 0x31, 0xF0, 0xBF, 0xAF, 0x32, 0x00, 0x90,
	0xDD, 0x21, 0x00, 0xC0,
	0x3E, 0xFF, 0x37, 0x11, 0x10, 0x00, 0xCD, 0x56, 0x05, 0x30, 0x12,
	0x21, 0x00, 0x90, 0x34,
	0x06, 0x40, 0x10, 0xFE, 0x3E, 0x7F, 0xDB, 0xFE, 0xE6, 0x01, 0x20, 0xF4,
	0x18, 0xFE,
	0x18, 0xFE,
}

// startKeyWaitLoader mounts the same synthetic tape the custom-loader tests
// use and drops the machine into the key-wait loader above.
func startKeyWaitLoader(t *testing.T) *Harness {
	return startLoader(t, keyWaitCode)
}

// blockCountByteIndex is where customLoaderCode holds its DJNZ count — the
// number of blocks it reads after the first. Patching it is how the overrun
// fixture below asks for more blocks than the tape carries.
const blockCountByteIndex = 28

// hardPollDelayIndex is the LD B operand in keyWaitCode's poll delay. The
// fixture below shortens it so the loop clears ula.LoadReadThreshold, which is
// the whole point: at that rate a keyboard scan is indistinguishable from a
// tape loader's edge loop.
const hardPollDelayIndex = 28

// startHardPollLoader is the key-wait loader with its delay cut to nothing, so
// it hammers port $FE about 1300 times a frame instead of 80.
func startHardPollLoader(t *testing.T) *Harness {
	t.Helper()
	code := append([]byte(nil), keyWaitCode...)
	if code[hardPollDelayIndex-1] != 0x06 {
		t.Fatalf("keyWaitCode[%d] is not the LD B — the fixture is patching the wrong byte",
			hardPollDelayIndex-1)
	}
	code[hardPollDelayIndex] = 0x01
	return startLoader(t, code)
}

// TestAHardKeyboardPollKeepsTheLoadFromGoingIdle pins the property that keeps
// every published load figure honest.
//
// TapeBlocksDecoded infers a block from the guest's port-$FE read rate, and a
// keyboard poll tight enough to clear the threshold is indistinguishable from
// a loader decoding edges — such a guest is credited with every block that
// rolls past it. Nothing in the counter can tell the two apart.
//
// What contains it is this: a guest polling that hard never lets the load fall
// silent, so RunUntilTapeIdle reports the load unfinished, the screening marks
// it TapeIncomplete, and Screening.TapeBlocksDecoded is documented as an upper
// bound not to be quoted. The Way of the Tiger is the live case — its menu
// polls hard enough that the screening calls it inconclusive.
//
// If this ever stops holding, an inflated figure reaches the manifest with
// nothing flagging it.
func TestAHardKeyboardPollKeepsTheLoadFromGoingIdle(t *testing.T) {
	h := startHardPollLoader(t)

	idle := h.RunUntilTapeIdle(TapeIdleQuietT, 1500*TstatesPerFrame)

	if pc := h.CPU().PC; pc < keyWaitLoop || pc > keyWaitLoopEnd {
		t.Fatalf("guest is at $%04X, not in its keyboard-wait loop ($%04X-$%04X) — the "+
			"fixture has to be polling for this test to mean anything",
			pc, keyWaitLoop, keyWaitLoopEnd)
	}
	if idle {
		t.Errorf("RunUntilTapeIdle reported the load finished while the guest was polling "+
			"port $FE at loader rate. It read 1 block and is waiting for a key, but the "+
			"counter credits it with %d — and nothing now marks that figure unsafe to quote",
			h.TapeBlocksDecoded())
	}
}

// startOverrunLoader runs the custom loader against a tape two blocks shorter
// than it asks for, so the guest spends its last seconds in the ROM loader's
// edge loop with the tape already run out.
func startOverrunLoader(t *testing.T) *Harness {
	t.Helper()
	code := append([]byte(nil), customLoaderCode...)
	if code[blockCountByteIndex-1] != 0x06 {
		t.Fatalf("customLoaderCode[%d] is not the LD B — the fixture is patching the wrong byte",
			blockCountByteIndex-1)
	}
	code[blockCountByteIndex] = customLoaderBlocks + 1
	return startLoader(t, code)
}

// startLoader boots a 48K, mounts the synthetic tape and drops the machine
// into the given hand-assembled loader.
func startLoader(t *testing.T, code []byte) *Harness {
	t.Helper()
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	t.Cleanup(h.CloseFiles)
	if err := h.LoadTAP(writeCustomLoaderTAP(t)); err != nil {
		t.Fatalf("LoadTAP: %v", err)
	}
	for i, b := range code {
		h.WriteMemory(uint16(customLoaderOrg+i), b)
	}
	h.CPU().PC = customLoaderOrg
	return h
}

// TestScreenFileRecordsTheTapeLoadFigures pins the wiring, not a title. A
// manifest note for a tape has to quote how much of it loaded, and the whole
// reason seven rows were wrong is that the figure came from whichever counter
// was to hand rather than from the screening itself. If ScreenFile does not
// carry the numbers, the next note is written from a guess again.
func TestScreenFileRecordsTheTapeLoadFigures(t *testing.T) {
	h, err := New(roms.Model48K)
	if err != nil {
		t.Fatalf("New(48K): %v", err)
	}
	t.Cleanup(h.CloseFiles)

	s, err := h.ScreenFile(writeCustomLoaderTAP(t), 50)
	if err != nil {
		t.Fatalf("ScreenFile: %v", err)
	}
	if s.TapeBlocksTotal != customLoaderBlocks {
		t.Errorf("TapeBlocksTotal = %d, want %d", s.TapeBlocksTotal, customLoaderBlocks)
	}
	// The synthetic tape carries no BASIC header, so LOAD"" is handed a block
	// whose flag it does not want and reports a loading error. Offering a
	// block to a guest that refuses it is not the guest loading it, and an
	// earlier version of this test asserted the opposite — which is how a
	// multiload calling LOAD "name" would have had every header it skipped
	// added to the figure the manifest quotes.
	if s.TapeBlocksDecoded != 0 {
		t.Errorf("TapeBlocksDecoded = %d after LOAD\"\" rejected every block, want 0",
			s.TapeBlocksDecoded)
	}
}

// TestTapeBlocksDecodedNeverExceedsTheTape pins the arithmetic. A guest still
// in its edge loop when the tape runs out leaves the player's cursor one past
// the last block, and crediting that index reports more blocks decoded than
// the tape holds.
//
// It is not a rounding nicety. The Way of the Tiger measured 19 blocks decoded
// off an 18-block tape, and a count that can exceed its own total cannot be
// used to say how much of a tape a title loaded — which is the only thing this
// counter is for.
func TestTapeBlocksDecodedNeverExceedsTheTape(t *testing.T) {
	h := startOverrunLoader(t)
	h.RunFrames(2500)

	tp := h.ULA().GetTapePlayer()
	total := tp.BlockCount()
	// The over-credit needs the cursor to have run one PAST the last block.
	// total-1 is the cursor still sitting ON it, where the bug cannot happen —
	// admitting that would leave a test that passes while asserting nothing.
	if tp.CurrentBlock() < total {
		t.Fatalf("the cursor is at block %d of %d — it never ran past the end of the "+
			"tape, so this test is not exercising the case it exists for", tp.CurrentBlock(), total)
	}
	if got := h.TapeBlocksDecoded(); got > total {
		t.Errorf("TapeBlocksDecoded = %d on a %d-block tape", got, total)
	}
}

// How much of a tape did a title actually load?
//
// Neither counter the harness had could answer that, and the compatibility
// manifest was written from one of them.
//
// TapeBlocksConsumed counts fast-load trap fires, so it misses every block a
// title's own loader decodes off the EAR bit — RoboCop trapped 6 of 16 and
// read the other nine itself.
//
// The player's own cursor, TapePlayer.CurrentBlock, has the opposite fault: it
// advances whenever the tape rolls. The player is stepped from every port-$FE
// read (ula.tapeLevel), and a title sitting at a menu still scans the keyboard,
// so the tape keeps moving under a guest that is loading nothing at all. A
// title paused at a prompt therefore *appears* to have read the rest of its
// tape. That is not a hypothetical: it is how R-Type came to be recorded as
// loading all 24 of its blocks when it had decoded 9 and was waiting at its
// side-change prompt.
//
// These tests pin a counter that answers the question asked: blocks the guest
// itself read.

// TestTapeBlocksDecodedCountsACustomLoadersBlocks is the case the trap counter
// gets wrong. One block arrives through $0556 and the rest through the guest's
// own edge decoding, and all four were read by the guest.
func TestTapeBlocksDecodedCountsACustomLoadersBlocks(t *testing.T) {
	const deadlineT = 1 << 31
	h := startCustomLoader(t)

	if !h.RunUntilTapeIdle(TapeIdleQuietT, deadlineT) {
		t.Fatalf("RunUntilTapeIdle hit the deadline at $%04X", h.CPU().PC)
	}
	checkCustomLoaderLoaded(t, h)

	if got := h.TapeBlocksDecoded(); got != customLoaderBlocks {
		t.Errorf("TapeBlocksDecoded = %d, want %d — the guest verifiably read every block, "+
			"so a counter of what it read must say so (trap counter said %d)",
			got, customLoaderBlocks, h.TapeBlocksConsumed())
	}
}

// TestTapeBlocksDecodedIgnoresTapeRollingPastAnIdleGuest is the case the
// player's cursor gets wrong, and the one that put wrong rows in the manifest.
//
// The guest reads one block and then waits for a key, polling the keyboard the
// way a title's menu does. The tape keeps rolling — it is stepped by those very
// polls — so the cursor runs on through blocks nobody is reading. A count of
// what the guest loaded must not move.
func TestTapeBlocksDecodedIgnoresTapeRollingPastAnIdleGuest(t *testing.T) {
	h := startKeyWaitLoader(t)

	// Long enough for the remaining blocks to roll past: each is a ~2 s pilot
	// plus its 1 s pause, and there are three of them after the first.
	h.RunFrames(1200)

	if pc := h.CPU().PC; pc < keyWaitLoop || pc > keyWaitLoopEnd {
		t.Fatalf("guest is at $%04X, not in its keyboard-wait loop ($%04X-$%04X) — "+
			"the fixture has to be idle at the tape for this test to mean anything",
			pc, keyWaitLoop, keyWaitLoopEnd)
	}
	if got := h.Memory(customLoaderTick); got != 1 {
		t.Fatalf("guest loaded %d blocks, want exactly 1 before the key wait", got)
	}

	// The premise: the tape really did roll on past the guest.
	rolled := h.ULA().GetTapePlayer().CurrentBlock()
	if rolled < 2 {
		t.Fatalf("the player's cursor is at block %d — the tape did not roll past the "+
			"waiting guest, so this test is not exercising the case it exists for", rolled)
	}

	if got := h.TapeBlocksDecoded(); got != 1 {
		t.Errorf("TapeBlocksDecoded = %d after the guest read 1 block and waited for a key "+
			"while the tape rolled to block %d — a count of what the guest loaded must not "+
			"count tape it never read", got, rolled)
	}
}
