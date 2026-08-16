// Package screendiff compares the screens two Spectrum machines ended up
// showing and issues a verdict about whether they agree.
//
// It exists as its own package for one reason: the verdict is the whole
// product of a differential comparison, and most of the taxonomy is refusal.
// A guard that can never fire looks exactly like a guard that never needed to
// fire, and the comparison goes on printing IDENTICAL either way. That failure
// mode is invisible without tests, so the logic lives somewhere the test suite
// can reach it rather than inside a developer tool.
//
// Everything here is pure: no emulator, no processes, no files. The driver
// that produces the screens is a separate concern.
package screendiff

import (
	"bytes"

	"github.com/conorarmstrong/zx_go/pkg/testharness"
)

// ScreenLen is the size of a Spectrum display file: 6144 bytes of bitmap
// followed by 768 attribute bytes.
const ScreenLen = 6912

// bitmapLen is the bitmap half, which is the part that carries shape.
const bitmapLen = 6144

// Verdict is the outcome of one title's comparison.
type Verdict string

const (
	Identical Verdict = "IDENTICAL"
	Minor     Verdict = "match (minor)"
	Partial   Verdict = "PARTIAL"
	Diverges  Verdict = "DIVERGES"
	Loading   Verdict = "LOADING"
	Split     Verdict = "SPLIT"
	Custom    Verdict = "CUSTOM"
	Reset     Verdict = "RESET"
	Animated  Verdict = "ANIMATED"
	BlankPair Verdict = "BLANK"
	Inert     Verdict = "INERT"
	Phase     Verdict = "PHASE"
)

// CycleSamples and CycleStepT define the run of screens each side is sampled
// over after the compared one, and they exist because a single paired sample
// cannot survive an attract cycle.
//
// A title that rotates title screen, credits, high scores puts the two
// machines on different pages of the same correct sequence, and comparing one
// frame against one frame reports that as a divergence. It is not one: the
// same runs came back byte-identical and 14% apart on consecutive runs with
// nothing changed but a timing window, which is the proof that the verdict was
// measuring phase.
//
// 12 samples 3 s apart span 36 s, which covers the rotations measured in the
// corpus (a page holds 3-5 s, a full rotation 20-40 s).
//
// Two limits follow from a fixed grid, and both are real:
//
//   - A rotation longer than the 36 s span can still be missed entirely.
//   - A page held for less than CycleStepT can fall between consecutive
//     samples on our grid and the reference's alike, so the pairing that
//     would have rescued the title never enters the search. The corpus puts
//     the shortest pages at about 3 s, which is the step itself, so this is
//     a live risk rather than a theoretical one.
//
// Both show up as a divergence that is really a sampling miss. Neither is
// fixable by tuning the step alone: a finer grid costs proportionally more
// wall-clock on every title, including the great majority that do not cycle.
const (
	CycleSamples = 12
	CycleStepT   = 10_500_000 // 3 s
)

// PhaseMatchPct is how close a cross-match has to be before two screens count
// as the same one seen at different points in a cycle.
//
// It is deliberately near-exact. The search is (1+CycleSamples) squared, which
// is 169 pairings, and a loose threshold would let two genuinely different
// screens pair off across all of them. That is a lot of chances to find a
// coincidence, and the whole value of the verdict is that "the reference's
// screen appears somewhere in ours" is a strong claim.
const PhaseMatchPct = 0.5

// Run is what one machine produced.
//
// Before and After bracket the probe key, and the pair is the evidence that
// the key did anything at all: comparing only After against the other
// machine's After cannot tell "both responded the same way" from "neither
// responded".
type Run struct {
	Menu   []byte // the screen the loader was started from; disk path only
	Before []byte // display file at the settle point, before the probe key
	After  []byte // display file after the probe key: the screen compared
	Fires  int    // ROM loader entries; -1 when the side cannot report them
	Moved  bool   // the compared screen was still changing when sampled
	Motion int    // display-file bytes that changed over the motion window
	Idle   bool   // the load finished before the deadline
	Reset  bool   // the reference restarted the machine mid-run
	AtT    uint64 // guest T-states from this side's anchor to the sample
	Keys   int    // probe keys actually sent, so the other side can replay

	// Cycle is a run of screens sampled after the compared one, CycleStepT
	// apart. It is what makes an attract cycle comparable: the question is
	// not "do these two frames match" but "does the other machine's screen
	// appear anywhere in this machine's sequence".
	Cycle [][]byte
}

// Match is the closest pairing found between two sides' sequences, and it
// carries the screens themselves rather than only the score.
//
// The screens matter: PHASE is the one verdict decided on a screen that is not
// either side's compared sample, so without them the caller cannot show what
// it matched. A percentage says how far apart two screens are and never what
// either one shows, which is how a wrong row once entered a manifest for a
// screen that actually read ERROR IN LOADING.
type Match struct {
	Pct  float64 // differing bytes as a percentage
	Ours []byte  // the screen from our sequence
	Ref  []byte  // the screen from the reference's
	OK   bool    // false when neither side offered a comparable screen
}

// comparable reports whether a captured screen can carry a meaningful
// comparison at all.
//
// Two screens are excluded, and for the same underlying reason: matching them
// proves nothing, while the byte comparison between them reads as a perfect
// match. A short or missing capture is data we do not have, and Diff scores a
// zero-length overlap as 0% by construction. A flat screen is the shape a
// failure takes, and any two flat screens agree completely without either
// machine having drawn anything.
//
// The pointwise verdict has guards against both. The cross-match search had
// none, which let a single failed capture or one cleared page anywhere in
// either 13-screen sequence force a PHASE rescue over an unrelated title.
func comparable(scr []byte) bool {
	return len(scr) >= ScreenLen && !Blank(scr)
}

// BestCrossMatch is the closest any screen in one side's sequence comes to any
// screen in the other's.
//
// Both sides' compared screens are included, so a pair that already matches
// directly scores 0 here too.
func BestCrossMatch(ours, ref Run) Match {
	oursAll := sequence(ours)
	refAll := sequence(ref)

	best := Match{Pct: 100}
	for _, o := range oursAll {
		if !comparable(o) {
			continue
		}
		for _, r := range refAll {
			if !comparable(r) {
				continue
			}
			if _, pct := Diff(o, r); pct < best.Pct || !best.OK {
				best = Match{Pct: pct, Ours: o, Ref: r, OK: true}
			}
		}
	}
	return best
}

// sequence is the compared screen followed by the sampled ones, built once so
// the search does not rebuild it inside its own loop.
func sequence(r Run) [][]byte {
	all := make([][]byte, 0, 1+len(r.Cycle))
	all = append(all, r.After)
	return append(all, r.Cycle...)
}

// Judge decides what, if anything, the two runs say about each other.
//
// custom reports that the title carries its own loader, and probed reports
// that a key was actually sent. The order of the cases is the substance of
// this function: each refusal makes the evidence for the next meaningless, so
// they are checked strongest-claim-last.
func Judge(ours, ref Run, custom, probed bool) (Verdict, float64) {
	_, pct := Diff(ours.After, ref.After)
	switch {
	case ref.Reset:
		return Reset, pct
	case !ours.Idle || !ref.Idle:
		return Loading, pct
	case custom:
		return Custom, pct
	case ours.Fires >= 0 && ref.Fires >= 0 && ours.Fires != ref.Fires:
		return Split, pct
	case Blank(ours.After) && Blank(ref.After):
		return BlankPair, pct
	case probed && bytes.Equal(ours.Before, ours.After) && bytes.Equal(ref.Before, ref.After):
		// The key was sent and neither machine's screen moved for it. Whatever
		// the two screens then agree about, it is not how they answer a key.
		//
		// This sits above the phase rescue deliberately. A cross-match found
		// in the sampled screens says the two machines show the same sequence;
		// it says nothing about input, and reporting agreement here would hide
		// that the probe went unanswered on both sides.
		return Inert, pct
	case pct >= divergeBand:
		// The pointwise comparison has already failed as far as it can fail,
		// so a rescue can only improve the verdict and cannot mask agreement.
		//
		// Restricting the rescue to this band is what makes "the direct
		// comparison is tried first" true. Allowing it at any pct above the
		// threshold let a pair 0.6% apart be reported as PHASE at 0.0%, which
		// both overstated the agreement and discarded the real number.
		if m := BestCrossMatch(ours, ref); m.OK && m.Pct <= PhaseMatchPct {
			return Phase, m.Pct
		}
		if ours.Moved || ref.Moved {
			return Animated, pct
		}
		return Diverges, pct
	case ours.Moved || ref.Moved:
		// Duplicated deliberately rather than hoisted: inside the divergence
		// band the rescue must be tried BEFORE motion voids the comparison,
		// because an attract cycle is moving by definition. Outside it, motion
		// voids first. Hoisting one check above the band would change which of
		// those two wins.
		return Animated, pct
	case pct == 0:
		return Identical, pct
	case pct < minorBand:
		return Minor, pct
	}
	return Partial, pct
}

// The pixel bands are the only verdicts reachable once every guard has passed,
// and their boundaries are what a compatibility table ends up quoting.
const (
	minorBand   = 2
	divergeBand = 25
)

// Diff counts the differing bytes between two screens, and reports them as a
// percentage of the overlap.
//
// A zero-length overlap reports 0%, which reads as a perfect match and is why
// callers must not hand it screens they failed to capture. comparable is the
// guard for that.
func Diff(a, b []byte) (int, float64) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0, 0
	}
	d := 0
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			d++
		}
	}
	return d, 100 * float64(d) / float64(n)
}

// Lit counts set pixels in the bitmap half, which is the cheapest single
// number that says whether a screen has anything on it.
func Lit(scr []byte) int {
	n := 0
	for i := 0; i < bitmapLen && i < len(scr); i++ {
		b := scr[i]
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) != 0 {
				n++
			}
		}
	}
	return n
}

// Blank reports whether a display file carries no shape at all: every one of
// the 6144 bitmap bytes the same value.
//
// It matters because a flat screen is the shape a failure takes. Two machines
// that both showed solid paper are not agreeing about the title, they are both
// showing nothing, and the byte comparison between them is 0% by construction.
// Attributes are deliberately not consulted: a solid bitmap is one flat
// rectangle whatever colour it is painted.
func Blank(scr []byte) bool {
	if len(scr) < bitmapLen {
		// Deliberately the opposite answer to the harness's, and the reason
		// the shared predicate takes the length check out of the equation:
		// here a screen we could not capture must VOID the comparison, while
		// in the harness "we could not look" must not read as "nothing was
		// drawn". Same question, different default.
		return true
	}
	return testharness.FlatBitmapScreen(scr)
}

// DiskLoaded reports whether the screen left the loader menu.
//
// The disk path has no loader event stream to go quiet, so this is how "did it
// load" is answered there. It is the only guard standing between a comparison
// and two machines that both sat on the ROM menu.
//
// A capture that failed answers nothing. Comparing a missing menu against a
// real screen finds them unequal and would report a load, which is the
// opposite of what missing data means: the same rule comparable() enforces for
// the cross-match search, applied to the one guard that decides whether a disk
// side got anywhere at all.
func DiskLoaded(menu, before []byte) bool {
	if len(menu) < ScreenLen || len(before) < ScreenLen {
		return false
	}
	return !bytes.Equal(menu, before)
}
