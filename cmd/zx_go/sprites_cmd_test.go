package main

import (
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
)

// TestFormatSpriteList verifies the sprite inspector: a header with
// enable + selected-slot, then one line per VISIBLE sprite (X/Y/
// pattern/palette). Invisible slots are skipped so the 128-slot bank
// doesn't drown the useful rows. jnext's GUI has a sprite viewer; this
// is the scriptable, decoded equivalent. get() injected for unit test.
func TestFormatSpriteList(t *testing.T) {
	attrs := map[int]*sprite.Attr{
		3: {X: 100, Y: 50, Pattern: 7, Palette: 2, Visible: true},
		9: {X: -5, Y: 0, Pattern: 1, Palette: 0, Visible: false}, // hidden
	}
	get := func(i int) *sprite.Attr {
		if a, ok := attrs[i]; ok {
			return a
		}
		return &sprite.Attr{}
	}

	out := formatSpriteList(get, true, 3, 128)

	for _, want := range []string{
		"Sprites", "enabled", "sel=3", // header
		"03", "X=100", "Y=50", "pat=7", "pal=2", // visible slot 3
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sprite list missing %q\n---\n%s", want, out)
		}
	}
	// Hidden slot 9 (pattern 1) must not appear.
	if strings.Contains(out, "pat=1\b") || strings.Contains(out, "09:") {
		t.Errorf("hidden sprite 9 leaked into list:\n%s", out)
	}
}

// TestFormatSpriteList_NoneVisible reports an empty bank clearly
// rather than printing nothing.
func TestFormatSpriteList_NoneVisible(t *testing.T) {
	get := func(int) *sprite.Attr { return &sprite.Attr{} }
	out := formatSpriteList(get, false, 0, 128)
	if !strings.Contains(out, "disabled") {
		t.Errorf("expected disabled in header:\n%s", out)
	}
	if !strings.Contains(out, "no visible sprites") {
		t.Errorf("expected empty-bank note:\n%s", out)
	}
}
