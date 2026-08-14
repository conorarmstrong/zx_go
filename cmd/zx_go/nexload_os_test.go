package main

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// This file proves the faithful path for loading .nex games that depend on
// the NextZXOS runtime: instead of bank-injecting (which overwrites the OS's
// own workspace banks and breaks the game's RST 8 file I/O), it drives the
// genuine NextZXOS ".nexload" dot command from the Command Line — exactly how
// a real Spectrum Next loads these games. The OS CDs into the game's folder
// and provides the environment, so the game's runtime file opens succeed.

// nexRunFrames runs the machine GUI-style for n frames.
func nexRunFrames(emu *emulator, n int) {
	for i := 0; i < n; i++ {
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
	}
}

// nexPressCombo presses a set of matrix keys together, holds, releases, then
// pauses — long enough for the NextZXOS 50 Hz key scan to register a distinct
// keystroke between presses.
func nexPressCombo(emu *emulator, keys [][2]int, hold, gap int) {
	for _, k := range keys {
		emu.kbd.PressMatrixKey(k[0], byte(k[1]), true)
	}
	nexRunFrames(emu, hold)
	for _, k := range keys {
		emu.kbd.PressMatrixKey(k[0], byte(k[1]), false)
	}
	nexRunFrames(emu, gap)
}

// The typist below drives the command line the way a person does: it presses a
// key, looks at what the machine put on the line, and presses again if the
// machine did not take it. Nothing here waits a fixed number of frames for the
// OS, because a fixed wait is what made this harness order dependent — see
// TestNexCommandLineSurvivesBusyPrompt.
const (
	// nexHoldScans is how many of the guest's own keyboard scans a press is
	// held for. Short enough that the ROM's auto-repeat (tens of frames)
	// can never deliver the same key twice.
	nexHoldScans = 4
	// nexIdleScans is how many scans the matrix is left idle after a
	// release, so the ROM sees the key genuinely lift before the next one
	// arrives. Without it a SYMBOL SHIFT combo typed straight after its own
	// base key ("m" then ".", which is SYMBOL SHIFT + M) doubles the letter.
	nexIdleScans = 6
	// nexKeyPresses is how many times one character is offered to a busy OS
	// before the typist gives up and fails.
	nexKeyPresses = 16
	// nexEchoSettleFrames is how long a press is given to show up on the OS's
	// copy of the line before it counts as dropped. It has to exceed the
	// longest the editor can be busy between accepting a key and echoing it,
	// or the typist duplicates characters; see nexTypeChar.
	nexEchoSettleFrames = 40
)

// nexSettle runs frames until the guest has completed scans keyboard scans,
// counted as frames in which the ULA's port-$FE read count advanced.
//
// Be clear about what this does and does not buy. In every state this harness
// actually drives, the ROM's 50 Hz interrupt handler scans the keyboard every
// frame whatever the foreground is doing — TestTypedCharactersSurviveABusyOS
// measures exactly that, eight port-$FE reads per frame throughout — so here
// nexSettle(n) is in practice a pause of n frames. It is NOT what makes the
// typing deterministic; the per-character confirmation against the OS's own
// copy of the line is. What this adds is a bound tied to a guest-observable
// event rather than a host frame count, so if the guest ever does stop
// scanning (interrupts off during SD work) the pause stretches to match
// instead of running ahead of the machine.
//
// maxFrames bounds it so a machine that has stopped scanning entirely cannot
// wedge the harness.
func nexSettle(emu *emulator, scans, maxFrames int) {
	for i, done := 0, 0; i < maxFrames && done < scans; i++ {
		before := emu.ula.FEReadCount()
		nexRunFrames(emu, 1)
		if emu.ula.FEReadCount() != before {
			done++
		}
	}
}

// nexTapKey presses a set of matrix keys together and releases them, clocked by
// the guest's key scan rather than by a host frame count.
func nexTapKey(emu *emulator, keys [][2]int) {
	for _, k := range keys {
		emu.kbd.PressMatrixKey(k[0], byte(k[1]), true)
	}
	nexSettle(emu, nexHoldScans, 3*nexHoldScans)
	for _, k := range keys {
		emu.kbd.PressMatrixKey(k[0], byte(k[1]), false)
	}
	nexSettle(emu, nexIdleScans, 3*nexIdleScans)
}

// nexWaitEcho runs frames until the command line reads want, reporting whether
// it got there within maxFrames.
func nexWaitEcho(emu *emulator, want string, maxFrames int) bool {
	for i := 0; i < maxFrames; i++ {
		if nexCmdEcho(emu) == want {
			return true
		}
		nexRunFrames(emu, 1)
	}
	return nexCmdEcho(emu) == want
}

// nexTypeChar presses one character and confirms NextZXOS took it, by reading
// the OS's own copy of the command line rather than inferring anything from the
// screen. A press the OS was too busy to consume is simply repeated, which is
// what a person does when the machine is thinking; anything else appearing on
// the line is a hard error, because it means the line is no longer what the
// caller asked for.
func nexTypeChar(emu *emulator, prefix string, c rune) error {
	keys, ok := nexKeyMatrix[c]
	if !ok {
		return fmt.Errorf("no key mapping for %q", c)
	}
	want := prefix + string(c)
	// The OS finishes the previous command and only then clears the line, so
	// a character typed into that window can echo and still be wiped. Wait
	// for the line to be exactly what has been typed so far.
	if !nexWaitEcho(emu, prefix, 600) {
		return fmt.Errorf("command line never settled to %q before typing %q: it reads %q",
			prefix, c, nexCmdEcho(emu))
	}
	for i := 0; i < nexKeyPresses; i++ {
		nexTapKey(emu, keys)
		// Give a keystroke that is still in flight time to be consumed
		// before deciding it was dropped. Sampling the echo immediately
		// after the release is what made this race: if the OS was mid-SD
		// work and picked the key up a few frames later, the line still
		// read prefix here, the loop pressed again, and the OS then took
		// both — leaving prefix+c+c, which is the unrecoverable branch
		// below. Waiting for the echo means a late keystroke lands as a
		// success rather than provoking a duplicate.
		nexWaitEcho(emu, want, nexEchoSettleFrames)
		switch got := nexCmdEcho(emu); got {
		case want:
			return nil
		case prefix:
			// Genuinely dropped: the OS had the whole settle window and
			// the line never changed. Offer it again.
		default:
			return fmt.Errorf("command line corrupted typing %q: it reads %q, want %q", c, got, want)
		}
	}
	return fmt.Errorf("NextZXOS never accepted %q after %d presses: the line reads %q, want %q",
		c, nexKeyPresses, nexCmdEcho(emu), want)
}

// nexTypeLine types an ASCII string onto the NextZXOS command line, confirming
// every character against the OS's own copy of the line.
func nexTypeLine(emu *emulator, s string) error {
	prefix := ""
	for _, c := range s {
		if err := nexTypeChar(emu, prefix, c); err != nil {
			return err
		}
		prefix += string(c)
	}
	return nil
}

// nexSubmitLine presses ENTER and waits for NextZXOS to take the line. The OS
// signals that in one of two ways: it redraws or clears the command line, or it
// stops scanning the keyboard while it works. Both are needed — a launching
// game does not always disturb bank 7.
func nexSubmitLine(emu *emulator) error {
	line := nexCmdEcho(emu)
	for attempt := 0; attempt < 2; attempt++ {
		nexTapKey(emu, [][2]int{{6, 0x01}}) // ENTER
		quiet, running := 0, 0
		for i := 0; i < 300; i++ {
			if nexCmdEcho(emu) != line {
				return nil
			}
			// A launched game is the third signal, and the one that makes
			// a second ENTER safe to withhold. Some titles leave bank 7
			// undisturbed AND poll the keyboard every frame, so neither
			// of the other two ever fires; without this the retry pressed
			// ENTER straight into the running game, which can start, skip
			// or exit it, corrupting the very verdict being measured.
			if emu.cpu.PC != nextMenuLoopPC {
				if running++; running >= nexLaunchFrames {
					return nil
				}
			} else {
				running = 0
			}
			before := emu.ula.FEReadCount()
			nexRunFrames(emu, 1)
			if emu.ula.FEReadCount() == before {
				if quiet++; quiet >= 3 {
					return nil
				}
			} else {
				quiet = 0
			}
		}
		// Only offer ENTER again if the machine is demonstrably still
		// sitting at the prompt. Retrying blind is how a keystroke ends
		// up inside a game.
		if emu.cpu.PC != nextMenuLoopPC {
			return fmt.Errorf("NextZXOS did not take the command line %q, and the machine has left "+
				"the prompt (PC=%#04x) — not pressing ENTER again into whatever is running", line, emu.cpu.PC)
		}
	}
	return fmt.Errorf("NextZXOS did not take the command line %q on ENTER", line)
}

// nexLaunchFrames is how many consecutive frames the CPU must spend out of the
// OS key-wait loop before the machine counts as having been handed over to the
// game. Long enough that the OS's own "no such file" path — which is a few
// frames of work and then straight back to the prompt — cannot reach it.
const nexLaunchFrames = 20

// nexAtOSKeyWait reports whether the machine is sitting in the NextZXOS key
// wait loop. A single PC sample cannot tell an idle OS apart from a running
// game that happens to be inside a ROM call at that instant, and the loop is
// where the command prompt idles as well as the menu, so this samples across
// frames and only says yes if the machine never leaves it.
func nexAtOSKeyWait(emu *emulator) bool {
	for i := 0; i < nexLaunchFrames; i++ {
		if emu.cpu.PC != nextMenuLoopPC {
			return false
		}
		nexRunFrames(emu, 1)
	}
	return true
}

// nexWatchLaunch runs the machine for frames and reports whether NEXLOAD handed
// it over to the game, watching the whole window instead of sampling the state
// at the end of it.
//
// Sampling at the end conflates two different things, and that is the other
// half of what the 3x retry was hiding. TX-1696 is the case in point: it loads,
// plays its intro, and sits on its own title menu — and because nothing is
// pressing a key, it gives up and returns to NextZXOS on its own, somewhere
// between 17 and 30 seconds in depending only on the host clock the guest RTC
// reads. A sample at 28 seconds therefore called the same successful launch
// "launched" or "did not launch" at random, which no amount of retrying fixes.
func nexWatchLaunch(emu *emulator, frames int) bool {
	launched, run := false, 0
	for i := 0; i < frames; i++ {
		if emu.cpu.PC == nextMenuLoopPC {
			run = 0
		} else if run++; run >= nexLaunchFrames {
			launched = true
		}
		nexRunFrames(emu, 1)
	}
	return launched
}

// nexOpenCommandLine steps from the main menu (cursor on "Browser", as left by
// bootNextToMenu) to the Command Line, and waits for the prompt to exist. The
// menu leaves its own data in the bank-7 area, so an empty command line is the
// prompt's own signal that it has started and is the thing to wait for.
func nexOpenCommandLine(emu *emulator) error {
	nexTapKey(emu, [][2]int{{0, 0x01}, {4, 0x10}}) // cursor DOWN -> Command Line
	nexTapKey(emu, [][2]int{{6, 0x01}})            // ENTER -> the command prompt

	// "bank 7 reads empty" is a weak signal for "the prompt is open": it can
	// already be true the instant ENTER is pressed, and nexWaitEcho checks
	// before running a frame, so the wait could be a no-op and the first
	// characters would be typed at the MENU — surfacing much later as a
	// corrupted command line blamed on the title.
	//
	// Give the OS unambiguous time to act on the selection first. This is
	// weaker than proving the transition and is deliberately so; both
	// stronger options were tried and are worse:
	//   - typing a probe character and deleting it proves the line echoes,
	//     but shifts every later keystroke by two taps, and TX-1696 times
	//     out back to the OS on its own schedule, so the probe changed that
	//     title's verdict. A test may not perturb its subject.
	//   - watching for the CPU to leave and re-enter the key-wait loop does
	//     not work, because the command prompt idles in that same loop and
	//     the work is already done by the time the watcher starts.
	// If this ever does fire wrongly, nexTypeChar catches it immediately:
	// typing at the menu produces no echo, and it fails naming the prompt.
	nexRunFrames(emu, 60)
	if !nexWaitEcho(emu, "", 600) {
		return fmt.Errorf("the NextZXOS command prompt did not open: bank 7 reads %q", nexCmdEcho(emu))
	}
	return nil
}

// nexloadFromMenu drives the real NextZXOS NEXLOAD on sdPath. It expects the
// machine at the main menu with the cursor on "Browser" (as left by
// bootNextToMenu), steps down to "Command Line", changes to the game's own
// directory, types `.nexload <file>`, presses ENTER, then runs loadFrames
// frames to let the OS load and start the game. The path is typed lowercase;
// spaces are typed literally (NEXLOAD takes the rest of the line as the
// filename, so no quoting is needed).
//
// The CD matters, exactly as it does for the NextBASIC harness. A game that
// loads assets at runtime opens them by a path relative to the current
// directory, and a user reaches a game through the Browser, which changes
// into its folder first. Launching by absolute path from the root left the
// current directory at "/", so every relative open failed: Eternal Battle
// built its IM 2 table, got nothing back, and ran off into its own data until
// it hit an FF byte and RST 38'd into a filler ramp. That looked like a
// hardware bug and is not one.
//
// Every wait in here is on something the guest did, never on a frame count:
// the OS's own copy of the command line for each typed character, and its key
// scan for the pauses between them. The version that guessed frame counts (and
// then guessed at screen stability, which ULA FLASH makes ambiguous) dropped
// characters whenever anything shifted the timing, so the suite passed or
// failed on test ordering.
func nexloadFromMenu(emu *emulator, sdPath string, loadFrames int) (launched bool, err error) {
	if err := nexOpenCommandLine(emu); err != nil {
		return false, err
	}

	dir, file := path.Split(sdPath)
	dir = strings.TrimSuffix(dir, "/")
	if dir != "" {
		// Quoted because .cd splits its argument on spaces.
		if err := nexTypeLine(emu, `.cd "`+strings.ToLower(dir)+`"`); err != nil {
			return false, err
		}
		if err := nexSubmitLine(emu); err != nil {
			return false, err
		}
	}

	if err := nexTypeLine(emu, ".nexload "+strings.ToLower(file)); err != nil {
		return false, err
	}
	if err := nexSubmitLine(emu); err != nil { // ENTER -> run NEXLOAD
		return false, err
	}
	return nexWatchLaunch(emu, loadFrames), nil
}

// TestNexloadOSGamesIfPresent verifies that games which depend on the NextZXOS
// runtime (they open data files via RST 8 at startup) load and render when
// driven through the real `.nexload` loader — the bank-injection path can't
// host them because it clobbers the OS's workspace banks. Skipped when the
// Next ROMs / SD games (gitignored) are absent, so CI stays green.
func TestNexloadOSGamesIfPresent(t *testing.T) {
	cases := []struct {
		hostPath, sdPath, name string
		loadFrames             int
	}{
		{
			"../../roms/next/sd/games/Next/Warhawk/Warhawk.nex",
			"/games/Next/Warhawk/Warhawk.nex", "warhawk", 1400,
		},
		{
			"../../roms/next/sd/games/Next/Revival Survival/RevivalSurvival.nex",
			"/games/Next/Revival Survival/RevivalSurvival.nex", "revival", 1400,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(c.hostPath); err != nil {
				t.Skipf("%s not present (gitignored SD content)", c.hostPath)
			}
			emu := bootNextToMenu(t)
			launched, err := nexloadFromMenu(emu, c.sdPath, c.loadFrames)
			if err != nil {
				t.Fatalf("%s: driving the NextZXOS command line: %v", c.name, err)
			}

			img := emu.renderFrame()
			nonBlank := !uniformImage(img)
			verdictPC := emu.cpu.PC
			// launched only says the CPU was busy for a while, which
			// NEXLOAD's own work satisfies on its way to failing. Ask the
			// harder question too: is the machine still running the game at
			// the end of the window, or did it hand back to NextZXOS? The SD
			// suite asserts both, and without the second one a game that
			// loads and immediately dies passes here.
			backAtOS := nexAtOSKeyWait(emu)

			if dir := os.Getenv("NEX_RENDER_OUT_DIR"); dir != "" {
				var buf bytes.Buffer
				if writeScreenshotPNG(emu, &buf) == nil {
					_ = os.WriteFile(dir+"/"+c.name+"-nexload.png", buf.Bytes(), 0o644)
				}
			}
			if !launched {
				t.Errorf("%s: NEXLOAD never handed the machine over (PC=%#04x) — game did not launch", c.name, verdictPC)
			}
			if backAtOS {
				t.Errorf("%s: back at the NextZXOS key-wait at the end of the window (PC=%#04x) — "+
					"the game launched and then gave the machine back", c.name, verdictPC)
			}
			if !nonBlank {
				t.Errorf("%s: screen blank after NEXLOAD — game did not render", c.name)
			}
			t.Logf("%s: launched via NEXLOAD, PC=%#04x nonblank=%v", c.name, verdictPC, nonBlank)
		})
	}
}
