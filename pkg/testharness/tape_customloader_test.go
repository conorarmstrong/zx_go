package testharness

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// A title's own loader does not call LD-BYTES at $0556. Ocean's, on the
// RoboCop and Target Renegade tapes, sets AF' itself and jumps to $0562 —
// four instructions into the very same ROM routine, past the point the
// fast-load trap watches. Every block after the BASIC loader arrives that way:
// decoded edge by edge off the EAR bit, in real time, over minutes of guest
// time.
//
// The harness could not see any of it. Both the trap counter and the idle wait
// are driven purely by trap fires, so a load that never re-enters $0556 looks
// finished the moment the last trapped block lands — and a screening taken
// then is a picture of a loading screen, not of the title.
//
// These tests use a tape and a loader the test itself builds, so they pin the
// mechanism rather than a particular commercial title.

// customLoaderPayload is the data length of each block on the synthetic tape.
// Small: every real-time block costs a 3223-pulse pilot (~100 frames) whatever
// its length, so the pilots dominate and a bigger payload only slows the test.
const customLoaderPayload = 16

// customLoaderBlocks is how many blocks the synthetic tape carries. The first
// is loaded through $0556 (the trap's entry point), the rest through $0562, so
// the tape exercises the handover from the trap to the guest's own loader as
// well as the loader itself.
const customLoaderBlocks = 4

// writeCustomLoaderTAP builds a tape of standard-speed data blocks, each
// [flag $FF, payload…, parity]. Real blocks, real parity: the guest's own
// decode has to be byte-correct or the ROM returns with carry clear.
func writeCustomLoaderTAP(t *testing.T) string {
	t.Helper()
	var tape []byte
	for b := 0; b < customLoaderBlocks; b++ {
		blk := []byte{0xFF}
		for i := 0; i < customLoaderPayload; i++ {
			blk = append(blk, byte(b*customLoaderPayload+i))
		}
		var parity byte
		for _, c := range blk {
			parity ^= c
		}
		blk = append(blk, parity)
		var length [2]byte
		binary.LittleEndian.PutUint16(length[:], uint16(len(blk)))
		tape = append(tape, length[:]...)
		tape = append(tape, blk...)
	}
	path := filepath.Join(t.TempDir(), "customloader.tap")
	if err := os.WriteFile(path, tape, 0o600); err != nil {
		t.Fatalf("write tape: %v", err)
	}
	return path
}

// Addresses the synthetic loader uses.
const (
	customLoaderOrg  = 0x8000 // the loader itself
	customLoaderDone = 0x803D // reached when every block loaded
	customLoaderFail = 0x803F // reached on the first bad block
	customLoaderTick = 0x9000 // count of blocks loaded, maintained by the guest
	customLoaderDest = 0xC000 // where the blocks land
)

// customLoaderCode is the guest-side loader, assembled by hand.
//
//	8000  DI                     ; the ROM loader runs with interrupts off
//	8001  LD SP,$BFF0
//	8004  XOR A
//	8005  LD ($9000),A           ; blocks-loaded counter
//	8008  LD IX,$C000            ; LD-BYTES leaves IX past the bytes it read,
//	                             ; so the next block follows on
//	; block 0 the documented way: through LD-BYTES' entry point
//	800C  LD A,$FF               ; expected flag
//	800E  SCF                    ; carry = LOAD, not VERIFY
//	800F  LD DE,$0010
//	8012  CALL $0556
//	8015  JR NC,fail
//	8017  LD HL,$9000
//	801A  INC (HL)
//	801B  LD B,customLoaderBlocks-1
//	; the rest the way a publisher's loader does it: set AF' up by hand and
//	; jump in below $0556, so the fast-load trap never sees the call
//	801D  loop: PUSH BC
//	801E  CALL delay             ; the work a loader does between blocks
//	8021  LD A,$FF
//	8023  OR A                   ; Z must be CLEAR — $05A9 branches on it to
//	                             ; decide the first byte is the flag, and
//	                             ; $0556's INC D is what normally clears it.
//	                             ; Ocean's loader has this same OR A.
//	8024  SCF
//	8025  EX AF,AF'
//	8026  LD DE,$0010
//	8029  LD A,$0F               ; the border LD-BYTES would have set
//	802B  OUT ($FE),A
//	802D  LD HL,ret
//	8030  PUSH HL                ; LD-BYTES RETs to us; $0556-$0561 (which
//	8031  JP $0562               ; pushes $053F) is exactly what we skip
//	8034  ret: POP BC
//	8035  JR NC,fail
//	8037  LD HL,$9000
//	803A  INC (HL)
//	803B  DJNZ loop
//	803D  done: JR done
//	803F  fail: JR fail
//	; ~1.2 s of guest time touching no port at all — see customLoaderDelayFrames
//	8041  delay: LD HL,1249
//	8044  d1: LD B,0
//	8046  d2: DJNZ d2
//	8048  DEC HL
//	8049  LD A,H
//	804A  OR L
//	804B  JR NZ,d1
//	804D  RET
var customLoaderCode = []byte{
	0xF3, 0x31, 0xF0, 0xBF, 0xAF, 0x32, 0x00, 0x90,
	0xDD, 0x21, 0x00, 0xC0,
	0x3E, 0xFF, 0x37, 0x11, 0x10, 0x00, 0xCD, 0x56, 0x05, 0x30, 0x28,
	0x21, 0x00, 0x90, 0x34, 0x06, customLoaderBlocks - 1,
	0xC5, 0xCD, 0x41, 0x80, 0x3E, 0xFF, 0xB7, 0x37, 0x08, 0x11, 0x10, 0x00,
	0x3E, 0x0F, 0xD3, 0xFE,
	0x21, 0x34, 0x80, 0xE5, 0xC3, 0x62, 0x05,
	0xC1, 0x30, 0x08, 0x21, 0x00, 0x90, 0x34, 0x10, 0xE0,
	0x18, 0xFE,
	0x18, 0xFE,
	0x21, 0xE1, 0x04, 0x06, 0x00, 0x10, 0xFE, 0x2B, 0x7C, 0xB5, 0x20, 0xF7, 0xC9,
}

// customLoaderDelayFrames is how long the loader's inter-block pause lasts:
// 1249 iterations of 3356 T-states is 4 191 644 T, ~60 frames.
//
// It is there because a real loader has one, and the figure is taken from the
// corpus rather than invented: the longest gap measured between a block
// landing and the loader starting on the next is 51 frames — RoboCop and
// Target Renegade both, The Way of the Tiger at 50. Sixty is just past that,
// and past the one second the idle wait used to allow.
//
// It cannot be pushed much further and stay a real loader: a standard-speed
// block offers only its 1 s pause plus its 2 s pilot of slack, and the ROM
// spends 1 s of that on its own settling delay before it even looks for the
// leader. A guest that thinks for two seconds between blocks would lose the
// pilot on real hardware too.
const customLoaderDelayFrames = 60

// startCustomLoader mounts the synthetic tape and drops the machine straight
// into the loader.
func startCustomLoader(t *testing.T) *Harness {
	t.Helper()
	return startLoader(t, customLoaderCode)
}

// checkCustomLoaderLoaded asserts the guest got every block, byte for byte.
func checkCustomLoaderLoaded(t *testing.T, h *Harness) {
	t.Helper()
	if pc := h.CPU().PC; pc == customLoaderFail {
		t.Fatalf("guest loader reported a bad block after %d of %d",
			h.Memory(customLoaderTick), customLoaderBlocks)
	} else if pc != customLoaderDone {
		t.Fatalf("guest loader still running at $%04X after %d of %d blocks",
			pc, h.Memory(customLoaderTick), customLoaderBlocks)
	}
	for b := 0; b < customLoaderBlocks; b++ {
		for i := 0; i < customLoaderPayload; i++ {
			addr := uint16(customLoaderDest + b*customLoaderPayload + i)
			if got, want := h.Memory(addr), byte(b*customLoaderPayload+i); got != want {
				t.Fatalf("block %d byte %d at $%04X = %#02x, want %#02x",
					b, i, addr, got, want)
			}
		}
	}
}

// TestRunUntilTapeIdleWaitsForACustomLoader is the fault RoboCop and Target
// Renegade hit: the guest's own loader is decoding edges for minutes of guest
// time, and the harness calls the load finished as soon as the trap stops
// firing. Anything sampling at that point measures a loading screen.
//
// It pins both halves of that. The trap fires once here and never again, so a
// wait driven by trap fires alone cannot see the load at all; and the loader
// pauses ~100 frames between blocks, so a wait whose quiet window is shorter
// than a real inter-block gap ends inside one.
//
// The assertion is on load PROGRESS, not on pixels: when the wait says the
// tape is idle, every block on it must already be in the guest's memory.
func TestRunUntilTapeIdleWaitsForACustomLoader(t *testing.T) {
	// The screening's own window, so the test pins the figure that ships.
	const deadlineT = 1 << 31 // ~10 min of guest time; the load needs ~20 s
	h := startCustomLoader(t)

	if !h.RunUntilTapeIdle(TapeIdleQuietT, deadlineT) {
		t.Fatalf("RunUntilTapeIdle hit the deadline; guest reached $%04X with %d of %d blocks",
			h.CPU().PC, h.Memory(customLoaderTick), customLoaderBlocks)
	}
	if got := h.Memory(customLoaderTick); got != customLoaderBlocks {
		t.Fatalf("tape reported idle after %d of %d blocks — the wait cannot see a load that bypasses $0556",
			got, customLoaderBlocks)
	}
	checkCustomLoaderLoaded(t, h)
}

// TestTapeIdleQuietWindowOutlastsAnInterBlockGap pins the window itself
// against the gap it exists to bridge. A loader that pauses to unpack a block
// has not finished loading, and the two are told apart by nothing except how
// long the harness is prepared to wait.
func TestTapeIdleQuietWindowOutlastsAnInterBlockGap(t *testing.T) {
	gapT := uint64(customLoaderDelayFrames) * TstatesPerFrame
	if TapeIdleQuietT <= gapT {
		t.Fatalf("TapeIdleQuietT = %d T does not outlast a %d-frame inter-block gap (%d T)",
			TapeIdleQuietT, customLoaderDelayFrames, gapT)
	}
}

// TestCustomLoaderReadsEveryBlockInRealTime is the control: given the frames,
// the emulation itself carries the whole load. It separates "the machine
// cannot do this" from "the harness cannot see it" — only the second is true,
// and a fix to the second must not be credited with the first.
func TestCustomLoaderReadsEveryBlockInRealTime(t *testing.T) {
	h := startCustomLoader(t)
	if err := h.RunUntil(func(h *Harness) bool {
		pc := h.CPU().PC
		return pc == customLoaderDone || pc == customLoaderFail
	}, 3000); err != nil {
		t.Fatalf("%v (guest at $%04X after %d of %d blocks)",
			err, h.CPU().PC, h.Memory(customLoaderTick), customLoaderBlocks)
	}
	checkCustomLoaderLoaded(t, h)
}
