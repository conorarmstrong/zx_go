package testharness

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
)

// Screening is what one headless run of a title produced, reduced to the few
// signals that decide whether it got anywhere.
//
// All of it comes from the display the guest itself drew, so it works the same
// for a tape, a snapshot, a disk or a .nex, and on any model.
type Screening struct {
	// Pixels is the count of pixels differing from the frame's dominant
	// colour, i.e. how much was drawn over the background.
	Pixels int
	// Colours is the number of distinct colours in the frame.
	Colours int
	// Moved reports whether the frame changed between two samples taken far
	// enough apart to clear a slow animation cycle.
	Moved bool
	// Error is a BASIC error report read off the bottom line, empty if none.
	Error string
	// Responded reports that the screen changed materially after keys were
	// sent to a screen that was otherwise still. It is only meaningful for a
	// still screen: a title already animating changes on its own, so a change
	// after a keypress would prove nothing.
	Responded bool
	// CanvasPixels and CanvasColours are Pixels and Colours measured over the
	// display window with the ROM's editor area excluded as well as the
	// border. They exist to tell a game's own sparse screen apart from an
	// idle BASIC prompt, which the raw counts cannot: both light a few
	// hundred pixels. See MinCanvasPixels.
	CanvasPixels  int
	CanvasColours int
	// TapeIncomplete records that a tape load hit its deadline while the
	// loader was still working. The screen at that moment is a LOADING
	// screen, and reporting it as the title is precisely the error that put
	// nine wrong verdicts in the manifest. RunUntilTapeIdle's own doc says a
	// false here must void a comparison rather than produce a verdict, so it
	// is carried here rather than discarded at the call site.
	TapeIncomplete bool
	// TapeBlocksDecoded and TapeBlocksTotal are how many blocks of the tape
	// the guest itself read, and how many byte-carrying blocks the tape holds.
	// Both are zero unless this screening loaded a tape.
	//
	// They are recorded because the manifest note for a tape title has to
	// quote a load figure, and quoting the wrong one is what put seven wrong
	// rows in docs/compatibility.md — see Harness.TapeBlocksDecoded for which
	// counter says what. Carrying the right number here means the note is
	// written from it rather than from whichever count came to hand.
	//
	// Sampled at the moment the load fell silent, not at the end of the run.
	// After that point the title is running its own code, and a menu that
	// polls the keyboard hard enough to look like a loader keeps earning
	// credit for blocks the tape merely rolls past.
	//
	// When TapeIncomplete is set the load never fell silent, so there is no
	// such moment and the figure is an upper bound rather than a count. Do not
	// quote it.
	TapeBlocksDecoded int
	TapeBlocksTotal   int
	// FlatBitmap records that every byte of the guest's display file bitmap
	// holds the same value, so whatever colour the attributes paint it, no
	// shape was drawn.
	//
	// It exists because the pixel and colour floors cannot see this. A screen
	// of uniform $55 with two attribute values — uninitialised video memory,
	// which renders as even vertical stripes — measures 18432 px in 2 colours
	// and clears both floors easily. Eight +3 disk rows were recorded as
	// *Boots* on exactly that measurement, five of them identical to the
	// pixel, which is the tell: unrelated games do not draw the same screen.
	//
	// The differential tool has always applied this test and reported all
	// eight as blank. Asking whether the bitmap has any structure is a better
	// question than asking how much of it is lit.
	FlatBitmap bool
	// BankInjected records that the program was loaded by copying banks and
	// jumping, rather than through the machine's own loader. That path cannot
	// host a .nex which calls NextZXOS at runtime — the game's banks overwrite
	// the ones the OS keeps its screen and workspace in — so a blank frame
	// says nothing about the title.
	BankInjected bool
}

// Verdict is the classification of a screening.
type Verdict int

const (
	// VerdictBlank means nothing meaningful was drawn.
	VerdictBlank Verdict = iota
	// VerdictStatic means a screen was drawn but is not changing: a title
	// screen or a menu waiting for input. A real result, not a failure.
	VerdictStatic
	// VerdictResponds means a still screen changed materially after keys were
	// sent — the strongest evidence automated screening can produce, because
	// it separates a title waiting at a menu from one hung with a menu on it.
	VerdictResponds
	// VerdictLive means a screen was drawn and is still changing.
	VerdictLive
	// VerdictError means the guest reported a BASIC error.
	VerdictError
	// VerdictInconclusive means the load path cannot host this program, so
	// nothing can be concluded from what it drew. Never a fault.
	VerdictInconclusive
)

func (v Verdict) String() string {
	switch v {
	case VerdictBlank:
		return "Blank"
	case VerdictStatic:
		return "Static"
	case VerdictResponds:
		return "Responds"
	case VerdictLive:
		return "Live"
	case VerdictError:
		return "Error"
	case VerdictInconclusive:
		return "Inconclusive"
	}
	return "Unknown"
}

// Thresholds separating "a screen" from "a few stray characters".
//
// A tape loader is the case these are set against: it moves vividly for
// minutes while drawing almost nothing, so movement alone is never evidence a
// title ran. Content has to be on screen before movement counts for anything.
const (
	// MinContentPixels is the drawn-pixel floor, calibrated against real
	// titles rather than guessed. The sparsest genuine screen measured is
	// R-Type's side-change prompt at 3724 — a logo and one line of text on
	// black, unmistakably content. Busier screens run from 5k to 49k. A title
	// that renders nothing scores 0. The floor sits well under the sparsest
	// real screen because a false "Blank" records a working title as broken,
	// which is the worse error; Colours does the discriminating work.
	MinContentPixels = 1000
	// MinContentColours is the matching floor on distinct colours. With the
	// border cropped, a loader leaves an empty display area, so this only has
	// to clear "one flat colour". It is deliberately low: Elite's title screen
	// is three colours and is unambiguously content.
	MinContentColours = 2
	// MinCanvasPixels is the floor for the second, narrower measurement that
	// only applies when MinContentPixels has already rejected the frame.
	//
	// The pixel count alone cannot separate a game's own one-line prompt from
	// the ROM's idle BASIC screen: Alien Syndrome's "PRESS ENTER" is 498
	// pixels and the game plays, while Adidas Championship Tie-Break's bare
	// "K" cursor is 181 and nothing is running. Input response does not
	// separate them either, because BASIC echoes what it is sent.
	//
	// What separates them is *where* the pixels are. The bottom two character
	// rows are the editor and report area the ROM owns; everything above is
	// the program's canvas. Measuring the canvas alone puts the idle prompt at
	// zero and leaves the game's prompt untouched. This is the same reasoning
	// that crops the border: do not count chrome the ROM drew as evidence the
	// guest drew something.
	//
	// The floor sits between the two observed populations — nothing spurious
	// has ever scored above zero on the canvas, and the sparsest real screen
	// measured is 498.
	MinCanvasPixels = 200
	// flatBitmapMaxColours bounds the flat-bitmap veto. The eight frames it
	// was added for measured exactly 2 colours; a screen painted in the
	// attribute bytes over a solid bitmap is real artwork and uses many more.
	flatBitmapMaxColours = 2
)

// editorLines is the height in scanlines of the ROM's editor and report area
// at the foot of the display: two character rows.
const editorLines = 2 * 8

// TapeIdleQuietT is how long a tape must show no loader activity before a load
// counts as finished: five seconds of guest time.
//
// It is exported for the same reason ula.LoadReadThreshold is: more than one
// place decides when a load has stopped, and they must not drift. _tools/refdiff
// had its own one-second window, calibrated for trapped blocks — which land
// microseconds apart — and it declared RoboCop finished after 8 of 16 blocks
// while the reference read all 16, then reported the two machines as having
// loaded different amounts of tape.
//
// It has to outlast the gap between one block and the next. A loader is not
// polling continuously — between blocks it pages, decompresses and sets a
// screen up, touching no port at all, and only then starts hunting the next
// pilot. Measured across the whole tape corpus, the longest such gap that a
// load then carried on through is 51 frames, a shade over one second: RoboCop
// and Target Renegade both, with The Way of the Tiger at 50. One second of
// quiet therefore lands squarely inside a real gap — it is what declared
// RoboCop finished after 6 of its 16 blocks. Five seconds clears the measured
// worst case by five times.
//
// Erring long is cheap and erring short is not: a window that is too long only
// runs the machine on for a few hundred more frames after the load really has
// finished, while one that is too short screens a loading screen and records
// it as the title.
const TapeIdleQuietT = 5 * 3_500_000

// tapeLoadBudget scales the tape's nominal play time into a guest-time budget
// for a real-time load. A load costs strictly more than the medium: the quiet
// window is charged to the same budget, and the tape only advances inside
// ULA.tapeLevel, whose frame-relative guard drops the interval across every
// frame boundary, so the medium runs slower than guest time. 1.61x was
// measured on the synthetic custom-loader tape; 3x is deliberately well clear.
// Overshooting costs wall-clock on a title that has already finished;
// undershooting screens a partial load and calls it a title screen, which is
// the error this exists to stop.
const tapeLoadBudgetMul = 3

// Classify reduces a screening to a verdict.
func Classify(s Screening) Verdict {
	if s.Error != "" {
		return VerdictError
	}
	// A load that was still running when the budget expired says nothing
	// about the title: what is on screen is its loading screen. Nine manifest
	// rows recorded exactly that as a working title screen, so this is not a
	// hypothetical.
	if s.TapeIncomplete {
		return VerdictInconclusive
	}
	// A bitmap with one repeated value has no shape on it, and no pixel count
	// can tell you that: uninitialised video memory measures 18432 px in 2
	// colours and clears both floors.
	//
	// Bounded by the colour count so it cannot condemn attribute-only
	// artwork. A picture painted in the 768 attribute bytes over a solid
	// bitmap is a real screen and uses many colours; the frames this exists
	// to reject used exactly two. Without the bound this veto sits in front of
	// a strictly better measurement — the composited frame — and overrules it.
	if s.FlatBitmap && s.Colours <= flatBitmapMaxColours {
		if s.BankInjected {
			return VerdictInconclusive
		}
		return VerdictBlank
	}
	blank := s.Pixels < MinContentPixels || s.Colours < MinContentColours
	// The canvas measurement can only ever rescue a frame the raw counts
	// rejected, never condemn one they accepted. That keeps every verdict
	// already recorded intact.
	if blank && s.CanvasPixels >= MinCanvasPixels && s.CanvasColours >= MinContentColours {
		blank = false
	}
	if blank {
		// Bank injection cannot host an OS-dependent .nex, so a blank frame
		// from it is not evidence the title is broken. Warhawk was recorded as
		// a Known issue on exactly that mistake while working perfectly
		// through the real NEXLOAD path.
		if s.BankInjected {
			return VerdictInconclusive
		}
		return VerdictBlank
	}
	if s.Moved {
		return VerdictLive
	}
	if s.Responded {
		return VerdictResponds
	}
	return VerdictStatic
}

// basicError matches the Spectrum's bottom-line report, e.g.
// "Integer out of range, 0:1" or "9 STOP statement, 12:1". The Opus and other
// interfaces use the same shape for their own messages.
var basicError = regexp.MustCompile(`(?i)\b(error|out of range|not found|invalid|no room|write protected|nonsense|out of memory|variable not found|subscript wrong)\b`)

// ScreenError returns the guest's error report if the screen shows one.
func ScreenError(screenText string) string {
	lines := strings.Split(strings.TrimRight(screenText, "\n"), "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-3; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" && basicError.MatchString(l) {
			return l
		}
	}
	return ""
}

// ScreenTitle measures the running machine, then runs settle more frames and
// measures again, so the two samples are far enough apart to tell a still
// screen from a slow animation.
//
// It measures the COMPOSITED frame rather than the ULA display file at $4000.
// That is not a detail: on the Next a title can draw entirely into Layer 2 or
// the tilemap and leave the ULA bitmap empty, and measuring $4000 reported two
// of the three vendored Next demos as blank — a working title recorded as
// broken, the worst error this harness can make.
//
// settle is rounded up to a multiple of the 32-frame FLASH period so the two
// samples catch FLASH in the same phase. Otherwise every classic screen with a
// flashing cursor would register as motion.
func (h *Harness) ScreenTitle(settle int) Screening {
	if r := settle % flashPeriod; r != 0 {
		settle += flashPeriod - r
	}
	before := h.frameBytes()
	flatBefore := h.flatBitmap()
	s := Screening{Error: ScreenError(h.ScreenText())}
	s.Pixels, s.Colours = measureFrame(before)
	s.CanvasPixels, s.CanvasColours = measureFrame(dropBottomLines(before, editorLines))

	h.RunFrames(settle)
	after := h.frameBytes()

	// Keep the busier sample: a title still painting when first measured would
	// otherwise be judged on a half-drawn screen.
	if p, c := measureFrame(after); p > s.Pixels {
		s.Pixels, s.Colours = p, c
	}
	if p, c := measureFrame(dropBottomLines(after, editorLines)); p > s.CanvasPixels {
		s.CanvasPixels, s.CanvasColours = p, c
	}
	if s.Error == "" {
		s.Error = ScreenError(h.ScreenText())
	}
	s.Moved = !bytes.Equal(before, after)
	// Both samples, not just the last one. Pixels and Colours keep whichever
	// sample was busier, so judging flatness on the other one lets a title
	// that wipes its bitmap during the settle be condemned on a frame whose
	// own recorded pixel count proves it drew something.
	s.FlatBitmap = flatBefore && h.flatBitmap()
	return s
}

// flatBitmap reports whether the guest's display file bitmap holds one
// repeated value, i.e. carries no shape at all.
//
// Attributes are deliberately not consulted: a flat bitmap is one plain
// rectangle however many colours it is painted in, and it was the colour
// count that let these frames past.
//
// It answers false for any machine whose picture is not that display file.
// The Next draws Layer 2, tilemap and sprite modes over an ULA bitmap it may
// never touch, and DisplayFile still hands one back for it — so asking this
// question there condemned working Layer 2 titles, which is the worst error
// this harness can make.
func (h *Harness) flatBitmap() bool {
	if h.mem.GetCurrentModel() == roms.ModelNext {
		return false
	}
	scr, ok := h.DisplayFile()
	if !ok || len(scr) < 6144 {
		return false
	}
	return FlatBitmapScreen(scr)
}

// FlatBitmapScreen reports whether a 6912-byte display file carries no shape:
// every byte of its 6144-byte bitmap holds the same value.
//
// Exported because _tools/refdiff asks the same question and the two must not
// drift — the same reasoning that exported TapeIdleQuietT. They had already
// drifted on the short-input case before this was shared.
//
// A display file too short to hold a bitmap is not evidence either way, so it
// answers false: "we could not look" must not read as "nothing was drawn".
func FlatBitmapScreen(scr []byte) bool {
	if len(scr) < 6144 {
		return false
	}
	for i := 1; i < 6144; i++ {
		if scr[i] != scr[0] {
			return false
		}
	}
	return true
}

// flashPeriod is the ULA FLASH cycle in frames (16 on, 16 off).
const flashPeriod = 32

// frameBytes renders the current frame and returns the pixels of the DISPLAY
// WINDOW only, with the border cropped off.
//
// Cropping is what makes the measurement mean anything. A tape loader fills
// the border with vivid moving stripes while the display area stays empty, so
// measuring the whole frame counts a loader as content and forces the colour
// floor up to compensate — which then rejects genuinely monochrome titles.
// Elite draws 35 000 pixels in three colours and was being called blank for
// exactly that reason. Crop the border and the loader scores nothing, so the
// floors can sit where real content actually is.
func (h *Harness) frameBytes() []byte {
	img := h.ScreenImage()
	out := make([]byte, 0, ula.ScreenWidth*ula.ScreenHeight*4)
	for y := ula.BorderTop; y < ula.BorderTop+ula.ScreenHeight; y++ {
		row := y*img.Stride + ula.BorderLeft*4
		out = append(out, img.Pix[row:row+ula.ScreenWidth*4]...)
	}
	return out
}

// dropBottomLines returns the display window with the last n scanlines
// removed, i.e. the program's canvas without the ROM's editor area.
func dropBottomLines(pix []byte, n int) []byte {
	keep := len(pix) - n*ula.ScreenWidth*4
	if keep < 0 {
		return nil
	}
	return pix[:keep]
}

// measureFrame counts distinct colours, and pixels differing from the most
// common one — the background, whatever colour the program chose for it.
func measureFrame(pix []byte) (pixels, colours int) {
	hist := map[uint32]int{}
	for i := 0; i+3 < len(pix); i += 4 {
		hist[uint32(pix[i])<<24|uint32(pix[i+1])<<16|uint32(pix[i+2])<<8|uint32(pix[i+3])]++
	}
	best := 0
	for _, n := range hist {
		if n > best {
			best = n
		}
	}
	total := len(pix) / 4
	return total - best, len(hist)
}

// ScreenFile loads a title, runs it, and measures the result.
//
// Dispatch is by extension, and an unhandled one is an error rather than a
// blank screening: "we never loaded it" and "it loaded and drew nothing" are
// different answers, and conflating them is how an untested title gets
// recorded as broken.
func (h *Harness) ScreenFile(path string, frames int) (Screening, error) {
	var bankInjected bool
	// Tape loads are the only path that can time out mid-load; everything
	// else is complete by construction.
	tapeIdle := true
	// Sampled inside the tape branch, at the moment the load falls silent, so
	// they describe the load rather than whatever the title did afterwards.
	var tapeDecoded, tapeTotal int
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	// .snx is a NextZXOS-flavoured snapshot; the ones on the SD card are
	// byte-for-byte 48K .sna (27-byte header + 49152), so the same loader
	// handles them.
	case ".sna", ".z80", ".szx", ".snx":
		if err := h.LoadSnapshot(path); err != nil {
			return Screening{}, err
		}
	case ".nex":
		if err := h.LoadNEX(path); err != nil {
			return Screening{}, err
		}
		bankInjected = true
	case ".tap", ".tzx":
		h.RunFrames(200) // reach the BASIC prompt
		load := h.LoadTAP
		if ext == ".tzx" {
			load = h.LoadTZX
		}
		if err := load(path); err != nil {
			return Screening{}, err
		}
		// Start the machine's own loader, whichever way this model starts one.
		// Typing LOAD"" unconditionally worked on the 128K only by accident —
		// J and SYMBOL SHIFT+P do nothing at its menu and the trailing ENTER
		// happens to select Tape Loader — and on a +2A/+3 that same ENTER lands
		// on the pre-highlighted DISK Loader and starts a disk load. See
		// StartTapeLoad, which was written for this dispatch and refuses the
		// models it cannot start rather than doing something else.
		if !h.StartTapeLoad() {
			return Screening{}, fmt.Errorf(
				"testharness: %s has no tape loader to start on this model", filepath.Base(path))
		}
		// Then wait for the loader to fall silent rather than for a frame
		// count, because a frame count cannot know how long a load takes. A
		// title that loads itself decodes tape edges in real time — RoboCop
		// spends ~24000 frames on the ten blocks its BASIC loader hands OVER TO the
		// game's own loader,
		// and the manifest's 3000 caught it mid-load and recorded its LOADING
		// screen as the title. The deadline comes from the tape: a load cannot
		// outlast the medium it reads.
		if tp := h.ULA().GetTapePlayer(); tp != nil {
			// The deadline must exceed the load, not merely the tape. A
			// real-time load costs strictly MORE guest time than the tape's
			// nominal play time: the quiet window is charged to the same
			// budget, and the tape only advances inside ULA.tapeLevel, whose
			// frame-relative guard drops the interval across every frame
			// boundary — so the medium runs slower than guest time. Measured
			// on the synthetic custom-loader tape, a 620-frame nominal tape
			// needed 998 frames, 1.61x. Passing PlayTstates() straight in
			// made the wait terminate on the deadline mid-load, every time,
			// for exactly the titles it was written for.
			budget := tp.PlayTstates()*tapeLoadBudgetMul + TapeIdleQuietT
			tapeIdle = h.RunUntilTapeIdle(TapeIdleQuietT, budget)
			// Sample here, not after the settle below. Past this point the
			// title is running its own code, and a menu polling port $FE hard
			// enough to look like a loader goes on earning credit for blocks
			// the tape merely rolls past — so a figure taken later depends on
			// the manifest's frame column rather than on the load.
			tapeDecoded, tapeTotal = h.TapeBlocksDecoded(), tp.DataBlockCount()
		}
	case ".dsk", ".edsk":
		if err := h.InsertPlus3Disk(0, path); err != nil {
			return Screening{}, err
		}
		h.Reboot()
		// A +3 powers on into its ROM menu — Loader / +3 BASIC / Calculator /
		// 48 BASIC — and stays there until ENTER selects the highlighted
		// "Loader". Without this the screening measures the ROM's own menu,
		// which is a drawn screen whose highlight moves under a probe
		// keypress, so every disk scored as booting and responding whether or
		// not it was readable at all.
		h.RunFrames(200) // reach the menu
		h.TapKey("Return")
		h.RunFrames(100)
	default:
		return Screening{}, fmt.Errorf("testharness: no screening loader for %q", ext)
	}
	h.RunFrames(frames)
	s := h.ScreenTitle(96)
	s.TapeIncomplete = !tapeIdle
	s.TapeBlocksDecoded, s.TapeBlocksTotal = tapeDecoded, tapeTotal
	s.BankInjected = bankInjected
	// Probing is only informative on a still screen; see ProbeInput.
	if !s.Moved && s.Error == "" {
		s.Responded = h.ProbeInput()
	}
	return s, nil
}

// probeKeys are the keys a title is most likely to act on at a title screen or
// menu: fire, start, and the usual menu selections. They are sent in turn
// because there is no way to know which one a given title wants.
var probeKeys = []fyne.KeyName{
	fyne.KeySpace, fyne.KeyReturn, fyne.Key1, fyne.Key0, fyne.Key5,
}

// respondThreshold is the fraction of the display window that must change for
// a frame to count as a response. A cursor blink or a one-character echo moves
// a handful of pixels; a menu selection, a screen wipe or a level starting
// moves a great many. The floor sits between them.
const respondThreshold = 0.002 // 0.2% of the display window

// respondCeiling is the drift above which a screen is too busy to probe: its
// own motion would swamp any response. Such titles are already classified Live
// on the strength of that motion.
const respondCeiling = 0.02 // 2% of the display window

// ProbeInput sends keys to a running machine and reports whether the display
// changed materially *because of them*.
//
// This is what separates a title waiting at a menu from one that has hung with
// a menu on screen — a distinction the still-frame measurements cannot make,
// since both are simply still.
//
// It runs a control first: the same elapsed time with no key pressed. A screen
// that animates on its own changes between any two samples, and without the
// control that change is credited to the keys. That is not hypothetical — it
// is what the first version did, reporting 85 of 87 titles as responding
// because things like Cybernoid's animated menu border moved on their own.
// A response only counts when it clearly exceeds what the screen does
// unprompted.
func (h *Harness) ProbeInput() bool {
	const holdFrames = 4

	// Control: one probe interval, no key. Whatever changes here is the
	// screen's own doing.
	base := h.frameBytes()
	h.RunFrames(flashPeriod)
	drift := changedFraction(base, h.frameBytes())

	// A screen already churning cannot be probed this way: any keyed change is
	// indistinguishable from its own motion. Say "no response" rather than
	// guess — such a title is reported as Live on its own evidence anyway.
	if drift > respondCeiling {
		return false
	}

	before := h.frameBytes()
	for _, k := range probeKeys {
		h.PressKey(k)
		h.RunFrames(holdFrames)
		h.ReleaseKey(k)
		h.RunFrames(flashPeriod - holdFrames)
		// Each key costs exactly one FLASH period, so every comparison lands
		// in the same FLASH phase and a flashing attribute is not motion.
		if changedFraction(before, h.frameBytes()) > drift+respondThreshold {
			return true
		}
	}
	return false
}

// changedFraction is the proportion of pixels that differ between two frames.
func changedFraction(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	diff := 0
	for i := 0; i+3 < len(a); i += 4 {
		if a[i] != b[i] || a[i+1] != b[i+1] || a[i+2] != b[i+2] {
			diff++
		}
	}
	return float64(diff) / float64(len(a)/4)
}
