package main

import (
	"os"
	"testing"
)

// The NEXLOAD suite launches games by typing into the genuine NextZXOS command
// line one synthetic keystroke at a time. These two tests pin down the two ways
// that typing used to go wrong, both of which turned into "the game did not
// launch" verdicts that flipped with test ordering.

// nexTypingFixture boots NextZXOS, opens the Command Line, and returns the
// machine sitting at an empty prompt. Skips when the Next ROMs / SD image
// (gitignored) are absent.
func nexTypingFixture(t *testing.T) *emulator {
	t.Helper()
	if _, err := os.Stat("../../roms/next/sd/games/Next/Warhawk/Warhawk.nex"); err != nil {
		t.Skipf("Next SD games not present (gitignored)")
	}
	emu := bootNextToMenu(t)
	if err := nexOpenCommandLine(emu); err != nil {
		t.Fatalf("open command line: %v", err)
	}
	return emu
}

// TestNexCommandLineSurvivesBusyPrompt is the regression test for the race that
// made the whole NEXLOAD suite order dependent.
//
// After ENTER, NextZXOS spends about twenty frames finishing the previous
// command, and it clears the command line at the end of that — so a character
// typed into that window is either dropped or wiped after it has echoed, even
// though the ROM keeps scanning the keyboard at its usual eight port-$FE reads
// per frame throughout. Typing two frames after ENTER used to lose exactly the
// leading "." and leave "nexload warhawk.nex" on the line: NEXLOAD then never
// ran and the OS dropped back to a prompt, which the suite reported as the game
// failing to launch.
//
// Two frames is the sharp case, but the same thing happened for any fixed wait
// that turned out to be a frame too short, and how long was long enough
// depended on what else had run in the same process.
func TestNexCommandLineSurvivesBusyPrompt(t *testing.T) {
	emu := nexTypingFixture(t)

	if err := nexTypeLine(emu, `.cd "/games/next/warhawk"`); err != nil {
		t.Fatalf("type .cd line: %v", err)
	}
	// ENTER is pressed directly, not through nexSubmitLine, so that typing
	// starts at a fixed two frames afterwards — deep inside the window where
	// the OS is still finishing the .cd and has not yet cleared the line.
	nexTapKey(emu, [][2]int{{6, 0x01}})
	nexRunFrames(emu, 2)

	const want = ".nexload warhawk.nex"
	if err := nexTypeLine(emu, want); err != nil {
		t.Fatalf("type NEXLOAD line: %v", err)
	}
	if got := nexCmdEcho(emu); got != want {
		t.Errorf("command line = %q, want %q", got, want)
	}
}

// TestNexloadMissingFileIsNotLaunched is the negative control for the launch
// verdict. The verdict has to be able to say no: NEXLOAD on a file that is not
// there does a little work, prints its complaint and drops back to the prompt,
// and that must never read as a launch — otherwise the whole corpus would pass
// whatever it typed.
func TestNexloadMissingFileIsNotLaunched(t *testing.T) {
	if _, err := os.Stat("../../roms/next/sd/games/Next/Warhawk/Warhawk.nex"); err != nil {
		t.Skipf("Next SD games not present (gitignored)")
	}
	emu := bootNextToMenu(t)
	launched, err := nexloadFromMenu(emu, "/games/Next/Warhawk/nosuchfile.nex", 1400)
	if err != nil {
		t.Fatalf("driving the NextZXOS command line: %v", err)
	}
	if launched {
		t.Errorf("NEXLOAD on a missing file reported a launch (PC=%#04x)", emu.cpu.PC)
	}
}

// TestNexTypingDoesNotDoubleKeys guards the other half of the typist: every
// press must be released and left released for long enough that the ROM's key
// re-arm cannot deliver the same key twice. Doubling shows up on repeated
// letters, and on a SYMBOL SHIFT combo typed straight after its own base key —
// ".nexload lom." used to arrive as ".nexload lomm" ("." is SYMBOL SHIFT + M)
// and '.cd "/demos/tilemap"' as "...tilemapp" ('"' is SYMBOL SHIFT + P).
func TestNexTypingDoesNotDoubleKeys(t *testing.T) {
	emu := nexTypingFixture(t)

	const want = `aabbll  zzm.p"`
	if err := nexTypeLine(emu, want); err != nil {
		t.Fatalf("type: %v", err)
	}
	if got := nexCmdEcho(emu); got != want {
		t.Errorf("command line = %q, want %q", got, want)
	}
}
