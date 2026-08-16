package screendiff

import "testing"

// The verdict is the whole product of a differential comparison, and most of
// the taxonomy is refusal: a statement that the two machines were not
// comparable and that no pixel count from them should be read as faithfulness.
// These tests exist because refusals are the part that fails silently. A guard
// that can never fire looks exactly like a guard that never needed to fire,
// and the comparison goes on printing IDENTICAL either way.
//
// Judge is pure, so the whole taxonomy is table-testable with no emulator.

// scr builds a display file that carries shape, with its first n bytes
// inverted relative to the base pattern. Diff(scr(0), scr(n)) is therefore
// exactly n bytes, which is what lets the pixel bands be tested at their
// boundaries.
func scr(n int) []byte {
	s := make([]byte, ScreenLen)
	for i := range s {
		s[i] = byte(i * 7)
	}
	for i := 0; i < n && i < ScreenLen; i++ {
		s[i] ^= 0xFF
	}
	return s
}

// solid is a display file with no shape at all: every bitmap byte the same.
func solid(v byte) []byte {
	s := make([]byte, ScreenLen)
	for i := 0; i < bitmapLen; i++ {
		s[i] = v
	}
	return s
}

// loaded is a run that reached a comparable point: it finished loading, it is
// not moving, and the probe key changed its screen. Tests spoil one property
// at a time from here.
func loaded(before, after []byte) Run {
	return Run{Before: before, After: after, Fires: 1, Idle: true}
}

// responded is the pair of runs a legitimate comparison is made from: both
// loaded, both settled, and both answered the key.
func responded(oursAfter, refAfter []byte) (Run, Run) {
	return loaded(scr(64), oursAfter), loaded(scr(64), refAfter)
}

func TestJudgeAgreesOnlyWhenBothSidesActuallyGotSomewhere(t *testing.T) {
	ours, ref := responded(scr(0), scr(0))
	v, pct := Judge(ours, ref, false, true)
	if v != Identical || pct != 0 {
		t.Fatalf("two loaded machines on the same screen: got %s %.2f%%, want %s 0%%", v, pct, Identical)
	}
}

// Requirement: a side that did not load can never agree. On the tape path Idle
// is the loader event stream going quiet; on the disk path it is the screen
// leaving the loader menu. Judge only sees the flag.
func TestJudgeRefusesWhenEitherSideDidNotLoad(t *testing.T) {
	stall := func(r Run) Run { r.Idle = false; return r }
	for _, tc := range []struct {
		name      string
		spoilOurs bool
		spoilRef  bool
	}{
		{"ours still loading", true, false},
		{"reference still loading", false, true},
		{"neither loaded", true, true},
	} {
		ours, ref := responded(scr(0), scr(0))
		if tc.spoilOurs {
			ours = stall(ours)
		}
		if tc.spoilRef {
			ref = stall(ref)
		}
		if v, _ := Judge(ours, ref, false, true); v != Loading {
			t.Errorf("%s: got %s, want %s", tc.name, v, Loading)
		}
	}
}

// Requirement: two machines showing nothing are not agreeing about anything.
// This is the verdict that used to be IDENTICAL for a title that failed on
// both sides and left the screen flat.
func TestJudgeRefusesWhenBothScreensAreBlank(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ours, ref []byte
		want      Verdict
	}{
		{"both solid paper", solid(0x00), solid(0x00), BlankPair},
		{"both solid ink", solid(0xFF), solid(0xFF), BlankPair},
		{"blank in different colours is still blank on both", solid(0x00), solid(0xFF), BlankPair},
		{"only ours blank is a real difference", solid(0x00), scr(0), Diverges},
		{"only the reference blank is a real difference", scr(0), solid(0x00), Diverges},
	} {
		ours, ref := responded(tc.ours, tc.ref)
		if v, _ := Judge(ours, ref, false, true); v != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, v, tc.want)
		}
	}
}

// Requirement: a match must not be allowed to mean "neither machine
// responded". The stimulus check is before-vs-after on each side, which is
// data the comparison was already collecting and never looking at.
func TestJudgeRefusesWhenTheProbeKeyChangedNothingOnBothSides(t *testing.T) {
	same := scr(0)
	dead := loaded(same, same) // Before == After: the key did nothing

	if v, _ := Judge(dead, dead, false, true); v != Inert {
		t.Errorf("the key did nothing on either side: got %s, want %s", v, Inert)
	}

	// One side responding and the other not is a genuine behavioural
	// difference, and the pixel verdict is the honest way to report it.
	live := loaded(same, scr(3000))
	if v, _ := Judge(dead, live, false, true); v == Inert {
		t.Errorf("only the reference responded: got %s, which hides a real difference", v)
	}
	if v, _ := Judge(live, dead, false, true); v == Inert {
		t.Errorf("only our side responded: got %s, which hides a real difference", v)
	}
}

// No key was sent, so Before and After are the same sample by construction and
// there was no stimulus to ignore. Reporting INERT here would be a lie in the
// other direction.
func TestJudgeDoesNotClaimInertWhenNoKeyWasSent(t *testing.T) {
	same := scr(0)
	if v, _ := Judge(loaded(same, same), loaded(same, same), false, false); v != Identical {
		t.Errorf("no probe key sent: got %s, want %s", v, Identical)
	}
}

// The motion guard is the reason a moving screen is not called a divergence,
// and it has to be sampled around the screen that was actually compared.
func TestJudgeRefusesWhenTheComparedScreenIsStillMoving(t *testing.T) {
	for _, tc := range []struct {
		name                string
		oursMoved, refMoved bool
	}{
		{"ours moving", true, false},
		{"reference moving", false, true},
		{"both moving", true, true},
	} {
		ours, ref := responded(scr(0), scr(0))
		ours.Moved, ref.Moved = tc.oursMoved, tc.refMoved
		if v, _ := Judge(ours, ref, false, true); v != Animated {
			t.Errorf("%s: got %s, want %s", tc.name, v, Animated)
		}
	}
}

// A moving screen must still be refused when the pair is far enough apart to
// reach the divergence band, which is a separate path through Judge from the
// one above.
func TestJudgeRefusesAMovingScreenInTheDivergenceBandToo(t *testing.T) {
	ours, ref := responded(scr(0), scr(3000))
	ours.Moved = true
	if v, _ := Judge(ours, ref, false, true); v != Animated {
		t.Errorf("a moving screen 43%% apart: got %s, want %s", v, Animated)
	}
}

// The refusals have to be checked in a fixed order, because each one makes the
// evidence for the next meaningless: a reference that restarted the machine
// has no meaningful block count, so RESET must not be masked by SPLIT.
func TestJudgeChecksTheVoidingGuardsInPriorityOrder(t *testing.T) {
	ours, ref := responded(scr(0), scr(0))
	ours.Idle, ref.Idle, ref.Reset = false, false, true
	if v, _ := Judge(ours, ref, true, true); v != Reset {
		t.Errorf("a reference reset beats every other refusal: got %s, want %s", v, Reset)
	}

	ours, ref = responded(scr(0), scr(0))
	ours.Idle, ref.Idle = false, false
	if v, _ := Judge(ours, ref, true, true); v != Loading {
		t.Errorf("an unfinished load beats a custom loader: got %s, want %s", v, Loading)
	}

	ours, ref = responded(scr(0), scr(0))
	ref.Fires = 9
	if v, _ := Judge(ours, ref, true, true); v != Custom {
		t.Errorf("a custom loader beats a block-count split: got %s, want %s", v, Custom)
	}

	ours, ref = responded(scr(0), scr(0))
	ref.Fires = 9
	if v, _ := Judge(ours, ref, false, true); v != Split {
		t.Errorf("different block counts: got %s, want %s", v, Split)
	}

	// A side that cannot count blocks reports -1, and an unknown count must
	// not be compared against a known one: that would invent a split on every
	// disk title, where neither side counts anything.
	ours, ref = responded(scr(0), scr(0))
	ours.Fires, ref.Fires = -1, 9
	if v, _ := Judge(ours, ref, false, true); v == Split {
		t.Errorf("one side cannot count blocks: got %s, which invents a split", v)
	}

	// A blank screen is not a divergence and it is not a comparison either, so
	// it outranks the pixel verdicts and is outranked by the load guards.
	ours, ref = responded(solid(0), solid(0))
	ours.Idle = false
	if v, _ := Judge(ours, ref, false, true); v != Loading {
		t.Errorf("an unfinished load beats a blank screen: got %s, want %s", v, Loading)
	}
	ours, ref = responded(solid(0), solid(0))
	ours.Moved = true
	if v, _ := Judge(ours, ref, false, true); v != BlankPair {
		t.Errorf("a blank screen beats motion: got %s, want %s", v, BlankPair)
	}
	// ...and a screen that stayed blank through the probe key is refused as
	// blank rather than as inert: there was never anything to change.
	ours, ref = loaded(solid(0), solid(0)), loaded(solid(0), solid(0))
	if v, _ := Judge(ours, ref, false, true); v != BlankPair {
		t.Errorf("a screen that was blank throughout: got %s, want %s", v, BlankPair)
	}
}

// The pixel bands are the only verdicts reachable once every guard has passed,
// and their boundaries are what a compatibility table ends up quoting.
func TestJudgeBandsThePixelDifference(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int
		want  Verdict
	}{
		{"no difference", 0, Identical},
		{"just inside minor", 138, Minor},    // 138/6912 = 1.997%
		{"just outside minor", 139, Partial}, // 139/6912 = 2.011%
		{"just inside partial", 1727, Partial},
		{"just outside partial", 1728, Diverges}, // 1728/6912 = 25.000%
	} {
		ours, ref := responded(scr(0), scr(tc.bytes))
		v, pct := Judge(ours, ref, false, true)
		if v != tc.want {
			t.Errorf("%s (%d bytes, %.3f%%): got %s, want %s", tc.name, tc.bytes, pct, v, tc.want)
		}
	}
}

// Blank is what stands between "both machines showed nothing" and IDENTICAL,
// so it must not accept a screen that has anything on it.
func TestBlankMeansNoShapeAtAll(t *testing.T) {
	if !Blank(solid(0x00)) || !Blank(solid(0xFF)) {
		t.Error("a uniform bitmap carries no shape and must count as blank")
	}
	one := solid(0x00)
	one[3000] = 0x01
	if Blank(one) {
		t.Error("a single set pixel is shape: not blank")
	}
	if Blank(scr(0)) {
		t.Error("a patterned bitmap is not blank")
	}
	// Attributes alone are not shape: a solid-paper screen stays blank however
	// the colours are set, because the viewer still sees one flat rectangle.
	att := solid(0x00)
	for i := bitmapLen; i < ScreenLen; i++ {
		att[i] = 0x47
	}
	if !Blank(att) {
		t.Error("attributes over a uniform bitmap are still a flat screen")
	}
	// A short or missing capture is not evidence of anything either.
	if !Blank(nil) || !Blank(make([]byte, 100)) {
		t.Error("a screen that was never captured cannot count as shape")
	}
}

// The disk path has no loader event stream to go quiet, so "did it load" is
// answered by the screen leaving the loader menu. It is the only guard
// standing between the comparison and two machines that both sat on the ROM
// menu.
func TestDiskLoadedMeansTheScreenLeftTheLoaderMenu(t *testing.T) {
	menu := scr(64)
	if DiskLoaded(menu, menu) {
		t.Error("still showing the menu at the deadline: that is not loaded")
	}
	if !DiskLoaded(menu, scr(0)) {
		t.Error("the screen changed away from the menu: that is loaded")
	}
	// A machine that blanked the screen and did nothing else has left the
	// menu; the blank guard is what catches that, not this one.
	if !DiskLoaded(menu, solid(0)) {
		t.Error("a blanked screen has left the menu")
	}
}

// DiskLoaded is described as the only guard between a comparison and two
// machines that both sat on the ROM menu, but it had none of the
// missing-capture guards the rest of this package enforces. A menu screen that
// was never captured read as "the screen left the menu", which is the opposite
// of what a failed capture means.
func TestDiskLoadedRefusesToDecideOnACaptureThatFailed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		menu, after []byte
	}{
		{"the menu was never captured", nil, scr(0)},
		{"the menu capture was short", make([]byte, 100), scr(0)},
		{"the later screen was never captured", scr(64), nil},
		{"neither was captured", nil, nil},
	} {
		if DiskLoaded(tc.menu, tc.after) {
			t.Errorf("%s: reported loaded, but a missing capture is data we do not have, "+
				"not evidence the screen moved", tc.name)
		}
	}
}
