package divmmc

import "testing"

// The page logger must observe CONMEM (port $E3 bit 7) transitions —
// the overlay's force-in override — alongside the automap latch
// events, to give a full overlay-visibility timeline: NextZXOS's IM1
// wrapper re-enters the conmem window each tick via `out ($E3),A`,
// and a dropped/missing write would otherwise be invisible.
func TestPageLoggerFiresOnConmemTransitions(t *testing.T) {
	p := New(makeROM())
	var events []string
	var pcs []uint16
	p.SetPageLogger(func(event string, pc uint16) {
		events = append(events, event)
		pcs = append(pcs, pc)
	})
	p.Step(0x1234)          // record an M1 pc for attribution
	p.WritePort(0xE3, 0x81) // conmem on, bank 1
	p.WritePort(0xE3, 0x83) // conmem stays on (bank change) — no event
	p.WritePort(0xE3, 0x03) // conmem off
	p.WritePort(0xE3, 0x00) // still off — no event

	if len(events) != 2 {
		t.Fatalf("got events %v, want [conmem-on conmem-off]", events)
	}
	if events[0] != "conmem-on" || events[1] != "conmem-off" {
		t.Errorf("events = %v, want [conmem-on conmem-off]", events)
	}
	for i, pc := range pcs {
		if pc != 0x1234 {
			t.Errorf("event %d pc = $%04X, want $1234 (last M1 pc)", i, pc)
		}
	}
}
