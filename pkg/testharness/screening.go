package testharness

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

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
}

// Verdict is the classification of a screening.
type Verdict int

const (
	// VerdictBlank means nothing meaningful was drawn.
	VerdictBlank Verdict = iota
	// VerdictStatic means a screen was drawn but is not changing: a title
	// screen or a menu waiting for input. A real result, not a failure.
	VerdictStatic
	// VerdictLive means a screen was drawn and is still changing.
	VerdictLive
	// VerdictError means the guest reported a BASIC error.
	VerdictError
)

func (v Verdict) String() string {
	switch v {
	case VerdictBlank:
		return "Blank"
	case VerdictStatic:
		return "Static"
	case VerdictLive:
		return "Live"
	case VerdictError:
		return "Error"
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
)

// Classify reduces a screening to a verdict.
func Classify(s Screening) Verdict {
	if s.Error != "" {
		return VerdictError
	}
	if s.Pixels < MinContentPixels || s.Colours < MinContentColours {
		return VerdictBlank
	}
	if s.Moved {
		return VerdictLive
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
	s := Screening{Error: ScreenError(h.ScreenText())}
	s.Pixels, s.Colours = measureFrame(before)

	h.RunFrames(settle)
	after := h.frameBytes()

	// Keep the busier sample: a title still painting when first measured would
	// otherwise be judged on a half-drawn screen.
	if p, c := measureFrame(after); p > s.Pixels {
		s.Pixels, s.Colours = p, c
	}
	if s.Error == "" {
		s.Error = ScreenError(h.ScreenText())
	}
	s.Moved = !bytes.Equal(before, after)
	return s
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
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".sna", ".z80", ".szx":
		if err := h.LoadSnapshot(path); err != nil {
			return Screening{}, err
		}
	case ".nex":
		if err := h.LoadNEX(path); err != nil {
			return Screening{}, err
		}
	case ".tap", ".tzx":
		h.RunFrames(200) // reach the BASIC prompt
		load := h.LoadTAP
		if ext == ".tzx" {
			load = h.LoadTZX
		}
		if err := load(path); err != nil {
			return Screening{}, err
		}
		h.TypeLoadCommand()
	default:
		return Screening{}, fmt.Errorf("testharness: no screening loader for %q", ext)
	}
	h.RunFrames(frames)
	return h.ScreenTitle(96), nil
}
