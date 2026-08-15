package screendiff

import (
	"bytes"
	"testing"
)

// PHASE is the one verdict that rescues a pair the pointwise comparison
// already rejected, which makes it the one most able to do damage: every other
// verdict can only refuse a comparison or report a number, while this one
// turns DIVERGES into agreement. If it fires too readily it does not produce a
// wrong number, it produces a wrong conclusion.
//
// So the tests that matter here are the negative ones. The search is 13 of our
// screens against 13 of the reference's, which is 169 chances to find a
// coincidence, and the only things standing between that and a false rescue
// are how near-exact PhaseMatchPct is and which screens are allowed into the
// search at all.
//
// It is tested here rather than against a real title because driving one is
// not a reliable way to reach the case: sending probe keys tends to pull both
// machines out of their attract loop and into a menu, which synchronises them
// and makes the frames match directly. That is a good outcome and a poor test,
// because the branch would go unexercised while looking like it worked.

// cycling is a run that reached a comparable point and then went on showing
// things, which is what an attract cycle does. The cycle screens are the
// samples taken after the compared one, CycleStepT apart.
func cycling(after []byte, cycle ...[]byte) Run {
	r := loaded(scr(64), after)
	r.Cycle = cycle
	return r
}

// BestCrossMatch has to consider the compared screens as well as the sampled
// ones, or a pair that already matches directly would score worse than a pair
// rescued from the cycle.
func TestBestCrossMatchSearchesBothComparedScreensAndBothCycles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ours, ref Run
		want      float64
	}{
		{
			"a direct match scores zero even with no cycle sampled",
			cycling(scr(0)), cycling(scr(0)), 0,
		},
		{
			"the reference's screen turns up later in ours",
			cycling(scr(0), scr(1000), scr(3000)), cycling(scr(3000)), 0,
		},
		{
			"our screen turns up later in the reference's",
			cycling(scr(3000)), cycling(scr(0), scr(1000), scr(3000)), 0,
		},
		{
			"both sides moved on, and match somewhere in the middle",
			cycling(scr(0), scr(2000)), cycling(scr(1000), scr(2000)), 0,
		},
	} {
		m := BestCrossMatch(tc.ours, tc.ref)
		if !m.OK || m.Pct != tc.want {
			t.Errorf("%s: got %.4f%% (ok=%v), want %.4f%%", tc.name, m.Pct, m.OK, tc.want)
		}
	}

	// With nothing in common it must report the closest pair it actually
	// found, not a default. scr(2000) against scr(3000) is 1000 bytes.
	ours := cycling(scr(0), scr(2000))
	ref := cycling(scr(5000), scr(3000))
	want := 100 * 1000.0 / ScreenLen
	if m := BestCrossMatch(ours, ref); !m.OK || m.Pct != want {
		t.Errorf("unrelated sequences: got %.4f%%, want the closest pair %.4f%%", m.Pct, want)
	}
}

// The match has to carry the screens it was decided on, not only the score.
// PHASE is reached on a pairing that is neither side's compared sample, so
// without them the caller has no picture to show for its verdict.
func TestBestCrossMatchCarriesTheScreensItMatched(t *testing.T) {
	ours := cycling(scr(0), scr(3000))
	ref := cycling(scr(6000), scr(3000))

	m := BestCrossMatch(ours, ref)
	if !m.OK || m.Pct != 0 {
		t.Fatalf("expected a perfect match, got %.4f%% (ok=%v)", m.Pct, m.OK)
	}
	if !bytes.Equal(m.Ours, scr(3000)) {
		t.Error("the match must carry our matched screen, not the compared one")
	}
	if !bytes.Equal(m.Ref, scr(3000)) {
		t.Error("the match must carry the reference's matched screen")
	}
}

// The requirement PHASE exists for: two machines on different pages of the
// same correct rotation. Before this verdict the comparison called that a
// divergence, and it was wrong every time.
func TestJudgeRescuesAnAttractCycleSampledAtDifferentPoints(t *testing.T) {
	ours := cycling(scr(0), scr(1000), scr(3000))
	ref := cycling(scr(3000), scr(4000), scr(0))

	v, pct := Judge(ours, ref, false, true)
	if v != Phase {
		t.Fatalf("the same screen appears in both sequences: got %s, want %s", v, Phase)
	}
	// The number printed beside the verdict has to be the one the verdict was
	// reached on. Reporting the direct 43% next to PHASE would read as a
	// contradiction and invite someone to fix the wrong end of it.
	if pct != 0 {
		t.Errorf("PHASE must report the cross-match it was decided on, got %.4f%%", pct)
	}
}

// The negative that carries the weight. Two genuinely different sequences give
// the search 169 pairings to find a coincidence in, and it must find none.
func TestJudgeDoesNotInventPhaseFromUnrelatedScreens(t *testing.T) {
	ours := cycling(scr(0), scr(1000), scr(2000))
	ref := cycling(scr(3000), scr(4000), scr(5000))

	if v, _ := Judge(ours, ref, false, true); v != Diverges {
		t.Errorf("two unrelated sequences: got %s, want %s: a rescue here hides a real divergence", v, Diverges)
	}
}

// A flat screen anywhere in either sequence used to force a rescue: two of
// them match perfectly and mean nothing, exactly as they do in the pointwise
// comparison, which has guarded against it since the BLANK verdict existed.
//
// This is the failure mode with the widest reach, because a title that clears
// the display between attract pages leaves one in every cycle it samples.
func TestJudgeDoesNotRescueFromBlankScreensInTheCycle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		oursFlat []byte
		refFlat  []byte
	}{
		{"solid paper on both sides", solid(0x00), solid(0x00)},
		{"solid ink on both sides", solid(0xFF), solid(0xFF)},
		{"different flat colours still match byte-for-byte in the bitmap", solid(0x00), solid(0x00)},
	} {
		ours := cycling(scr(0), tc.oursFlat)
		ref := cycling(scr(3000), tc.refFlat)
		if v, _ := Judge(ours, ref, false, true); v != Diverges {
			t.Errorf("%s: got %s, want %s", tc.name, v, Diverges)
		}
	}
}

// A capture that failed is data we do not have, and Diff scores a zero-length
// overlap as 0%, which reads as a perfect match. Twelve unguarded captures per
// side made that a likely event rather than a theoretical one.
func TestJudgeDoesNotRescueFromScreensThatWereNeverCaptured(t *testing.T) {
	for _, tc := range []struct {
		name   string
		broken []byte
	}{
		{"a capture that returned nothing", nil},
		{"a capture that returned a short buffer", make([]byte, 100)},
		{"a capture one byte short of a display file", make([]byte, ScreenLen-1)},
	} {
		ours := cycling(scr(0), tc.broken)
		ref := cycling(scr(3000), tc.broken)
		if v, _ := Judge(ours, ref, false, true); v != Diverges {
			t.Errorf("%s: got %s, want %s", tc.name, v, Diverges)
		}
	}

	// The case the flat-screen guard cannot catch on its own: a capture that
	// is truncated but still carries shape. Diff compares only the overlap, so
	// a short copy of the reference's screen scores 0% against the whole of
	// it, and the rescue fires on a screen we never finished reading. Whether
	// the missing bytes would have matched is precisely what is unknown.
	truncated := scr(3000)[:ScreenLen-1]
	ours := cycling(scr(0), truncated)
	ref := cycling(scr(6000), scr(3000))
	if v, _ := Judge(ours, ref, false, true); v != Diverges {
		t.Errorf("a truncated capture that carries shape: got %s, want %s", v, Diverges)
	}

	// And when neither side offers a single comparable screen, the match must
	// say so rather than report a confident zero.
	if m := BestCrossMatch(cycling(solid(0)), cycling(solid(0))); m.OK {
		t.Errorf("two sequences of nothing but blank screens: got a match at %.4f%%, want none", m.Pct)
	}
}

// PhaseMatchPct is deliberately near-exact, and near-exact is a claim about
// where the boundary sits rather than a vague preference. 0.5% of a 6912-byte
// display file is 34.56 bytes.
func TestPhaseMatchNeedsANearExactPairing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int
		want  Verdict
	}{
		{"34 bytes apart is 0.492%: inside the threshold", 34, Phase},
		{"35 bytes apart is 0.506%: outside it", 35, Diverges},
	} {
		// The compared screens are far apart, so the direct verdict is
		// DIVERGES and only the cycle can rescue the pair.
		ours := cycling(scr(0), scr(3000))
		ref := cycling(scr(6000), scr(3000+tc.bytes))
		if v, _ := Judge(ours, ref, false, true); v != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, v, tc.want)
		}
	}
}

// The rescue must not reach up into the bands that already report agreement.
// It fires only where the pointwise comparison has failed as far as it can,
// which is what makes "the direct comparison is tried first" a true statement
// rather than an aspiration.
//
// Before this, a pair 0.6% apart with any cycle match was reported as PHASE at
// 0.0%: it overstated the agreement and threw away the real number, and a
// manifest row recorded at 0.6% was eligible to be relabelled.
func TestJudgeDoesNotLetPhaseReplaceAnAgreementVerdict(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int
		want  Verdict
	}{
		{"an identical pair stays identical", 0, Identical},
		{"a pair inside the minor band stays minor", 100, Minor},
		{"a pair inside the partial band stays partial", 1000, Partial},
		{"a pair in the divergence band is the one PHASE may rescue", 3000, Phase},
	} {
		// Both sides show the same later screen, so a perfect cross-match is
		// always available; only the direct distance changes.
		ours := cycling(scr(0), scr(5000))
		ref := cycling(scr(tc.bytes), scr(5000))
		v, pct := Judge(ours, ref, false, true)
		if v != tc.want {
			t.Errorf("%s (%d bytes): got %s at %.4f%%, want %s", tc.name, tc.bytes, v, pct, tc.want)
		}
	}
}

// An attract cycle is moving by definition, so requiring stillness before a
// phase rescue would void every case the verdict was written for. This
// ordering is intentional and worth pinning: ANIMATED is a refusal to compare,
// and a cross-match is positive evidence that outranks it.
func TestJudgePrefersPhaseOverAnimated(t *testing.T) {
	ours := cycling(scr(0), scr(3000))
	ref := cycling(scr(3000), scr(0))
	ours.Moved, ref.Moved = true, true

	if v, _ := Judge(ours, ref, false, true); v != Phase {
		t.Errorf("a cycling title is moving: got %s, want %s", v, Phase)
	}
}

// INERT is the opposite case, and it outranks the rescue. A cross-match says
// the two machines show the same sequence; it says nothing about input, so
// reporting agreement would hide that the probe key went unanswered on both
// sides. That is the one thing the probe exists to measure.
func TestJudgePrefersInertOverPhase(t *testing.T) {
	deadOurs := scr(0)
	deadRef := scr(3000)

	ours := cycling(deadOurs, scr(5000))
	ours.Before = deadOurs // the key changed nothing
	ref := cycling(deadRef, scr(5000))
	ref.Before = deadRef // nor here

	if v, _ := Judge(ours, ref, false, true); v != Inert {
		t.Errorf("neither machine answered the key: got %s, want %s", v, Inert)
	}
}

// A rescue must never reach past a guard that says the two machines were not
// comparable in the first place. Each case below has a perfect cross-match
// available, and in each the guard must still win.
func TestJudgeDoesNotLetPhaseOverrideTheLoadGuards(t *testing.T) {
	pair := func() (Run, Run) {
		return cycling(scr(0), scr(3000)), cycling(scr(3000), scr(0))
	}

	ours, ref := pair()
	ref.Reset = true
	if v, _ := Judge(ours, ref, false, true); v != Reset {
		t.Errorf("a reference that restarted the machine: got %s, want %s", v, Reset)
	}

	ours, ref = pair()
	ours.Idle = false
	if v, _ := Judge(ours, ref, false, true); v != Loading {
		t.Errorf("a side that never finished loading: got %s, want %s", v, Loading)
	}

	ours, ref = pair()
	if v, _ := Judge(ours, ref, true, true); v != Custom {
		t.Errorf("a custom loader: got %s, want %s", v, Custom)
	}

	ours, ref = pair()
	ref.Fires = 9
	if v, _ := Judge(ours, ref, false, true); v != Split {
		t.Errorf("different block counts: got %s, want %s", v, Split)
	}

	// Two blank screens cross-match perfectly with each other, which is the
	// one case where the search is guaranteed a hit and guaranteed to mean
	// nothing. BLANK must come first.
	ours, ref = cycling(solid(0x00), solid(0xFF)), cycling(solid(0xFF), solid(0x00))
	if v, _ := Judge(ours, ref, false, true); v != BlankPair {
		t.Errorf("two screens with nothing on them: got %s, want %s", v, BlankPair)
	}
}
