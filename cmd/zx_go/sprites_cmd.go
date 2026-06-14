package main

import (
	"fmt"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
)

// formatSpriteList renders the sprite bank: a header (enable +
// selected slot) then one line per VISIBLE sprite. get(i) returns the
// attribute for slot i; max is the slot count to scan. Pure function
// (getter injected) for unit testing without an emulator.
func formatSpriteList(get func(int) *sprite.Attr, enabled bool, sel byte, max int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "OK Sprites: %s  sel=%d\r\n", enDis(enabled), sel)

	shown := 0
	for i := 0; i < max; i++ {
		a := get(i)
		if a == nil || !a.Visible {
			continue
		}
		fmt.Fprintf(&b, "  %03d: X=%d Y=%d pat=%d pal=%d\r\n", i, a.X, a.Y, a.Pattern, a.Palette)
		shown++
	}
	if shown == 0 {
		b.WriteString("  (no visible sprites)\r\n")
	}
	return b.String()
}

// cmdSpriteList is the `sprite-list` debugger command — a decoded
// snapshot of the visible sprites in the live sprite bank.
func (d *remoteDebugger) cmdSpriteList() string {
	if d.emu == nil || d.emu.nextSprites == nil {
		return "ERR sprite-list: no sprite engine (not a Next machine?)"
	}
	s := d.emu.nextSprites
	return formatSpriteList(s.Sprite, s.Enabled(), s.SelectedSprite(), sprite.MaxSprites)
}
