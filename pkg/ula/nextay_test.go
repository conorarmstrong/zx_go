package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/ay"
)

// TestPort0xFFFDRoutesToEngineActiveChip pins that when an Engine is wired
// via SetNextAY, port 0xFFFD register selects + reads go to
// engine.Active(), not the singleton u.ay. Selecting a different chip
// changes which AY services the next port write.
func TestPort0xFFFDRoutesToEngineActiveChip(t *testing.T) {
	u := newTestULA(t)
	engine := ay.NewEngine()
	u.SetNextAY(engine)

	// Select AY register 14 on chip 0 (mixer enable register;
	// reachable via port 0xFFFD).
	u.WritePort(0xFFFD, 14)
	if engine.Chip(0).Selected() != 14 {
		t.Errorf("chip 0 register after FFFD write = %d, want 14", engine.Chip(0).Selected())
	}
	// Chips 1 / 2 untouched.
	if engine.Chip(1).Selected() != 0 || engine.Chip(2).Selected() != 0 {
		t.Errorf("chips 1/2 disturbed: %d / %d, want 0/0",
			engine.Chip(1).Selected(), engine.Chip(2).Selected())
	}

	// Switch to chip 2 and write again — only chip 2's register
	// should now move. (Chip select is the TurboSound mechanism, not NR$06.)
	engine.SelectChip(2)
	u.WritePort(0xFFFD, 7) // mixer register
	if engine.Chip(2).Selected() != 7 {
		t.Errorf("chip 2 register after switch + FFFD write = %d, want 7", engine.Chip(2).Selected())
	}
	if engine.Chip(0).Selected() != 14 {
		t.Errorf("chip 0 register clobbered: %d, want 14 (sticky from earlier write)", engine.Chip(0).Selected())
	}
}

func TestEngineDisabledDropsAYWrites(t *testing.T) {
	u := newTestULA(t)
	engine := ay.NewEngine()
	u.SetNextAY(engine)

	engine.Select(0x03) // NR$06 bits 1-0 = 11: hold all AY in reset (disabled)
	u.WritePort(0xFFFD, 14)
	// All three chips should be untouched.
	for i := 0; i < 3; i++ {
		if engine.Chip(i).Selected() != 0 {
			t.Errorf("chip %d disturbed while engine disabled: %d", i, engine.Chip(i).Selected())
		}
	}
}

func TestSetNextAYNilUnhooks(t *testing.T) {
	// newTestULA is built on a 48K model which has no AY at all
	// (u.ay is nil). We can't directly observe "routes to u.ay"
	// here, but we CAN verify that unhooking the engine
	// successfully (i.e. doesn't panic on subsequent writes
	// because activeAY now returns nil and the chip!=nil guard
	// fires) is safe.
	u := newTestULA(t)
	engine := ay.NewEngine()
	u.SetNextAY(engine)
	u.SetNextAY(nil)
	u.WritePort(0xFFFD, 6) // must not panic
}
