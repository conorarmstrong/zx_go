package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/ay"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// NR$08 bit 5 and NR$09 bits 7:5 are the AY panning controls
// (zxnext.vhd:5177 and :5186). Both registers were decoded and stored and
// neither reached the sound chips, so a guest could select ACB stereo and hear
// no difference.

func newAYStereoTest(t *testing.T) (*nextregs.Dispatcher, *ay.Engine) {
	t.Helper()
	d := nextregs.New()
	e := ay.NewEngine()
	mem, err := memory.New("", roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	// The owners of both registers go in first, exactly as next.Wire orders
	// them: WireAYStereo chains onto them and would be the handler silently
	// replaced if it ran first.
	WireContentionDisable(d, mem)
	WirePeripheral3(d, nil)
	WireAYStereo(d, e)
	return d, e
}

// NR$08 bit 5 selects between the two stereo laws.
func TestNR08Bit5SelectsTheStereoLaw(t *testing.T) {
	d, e := newAYStereoTest(t)

	d.WriteReg(0x08, 0x00)
	if got := e.Chip(0).StereoModeSetting(); got != ay.StereoABC {
		t.Errorf("NR$08 bit 5 clear gave %v, want ABC", got)
	}

	d.WriteReg(0x08, 0x20)
	if got := e.Chip(0).StereoModeSetting(); got != ay.StereoACB {
		t.Errorf("NR$08 bit 5 set gave %v, want ACB", got)
	}
}

// NR$09 bits 7:5 hold individual chips mono.
func TestNR09HoldsIndividualChipsMono(t *testing.T) {
	d, e := newAYStereoTest(t)
	d.WriteReg(0x08, 0x20) // ACB everywhere

	d.WriteReg(0x09, 1<<6) // bit 6 → PSG 1
	if got := e.Chip(1).StereoModeSetting(); got != ay.StereoMono {
		t.Errorf("chip 1 = %v, want mono after NR$09 bit 6", got)
	}
	if got := e.Chip(0).StereoModeSetting(); got != ay.StereoACB {
		t.Errorf("chip 0 = %v, want ACB: only the masked chip is held mono", got)
	}
}

// Chaining, not replacing. NR$08's owner clears the RAM contention flag and
// NR$09's masks off its one-shot bit; a bare SetOnWrite here would delete
// either side effect while the register still read back correctly.
func TestTheStereoWiringDoesNotDisplaceTheRegisterOwners(t *testing.T) {
	d := nextregs.New()
	e := ay.NewEngine()
	mem, err := memory.New("", roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	WireContentionDisable(d, mem)
	WirePeripheral3(d, nil)
	WireAYStereo(d, e)

	// NR$08 bit 6 is the contention disable, owned by WireContentionDisable.
	d.WriteReg(0x08, 0x40)
	if !mem.RAMContentionDisabled {
		t.Error("NR$08 bit 6 no longer disables RAM contention: the owner's handler was replaced")
	}
	// NR$09 bit 3 is a one-shot and must not be stored, which is
	// WirePeripheral3's doing.
	d.WriteReg(0x09, 0x08)
	if d.Raw(0x09)&0x08 != 0 {
		t.Error("NR$09 bit 3 was stored: the owner's handler was replaced")
	}
}

// A reset returns both registers to their power-on values, so the panning has
// to follow rather than staying where the last guest left it.
func TestAResetReturnsThePanningToTheRegisterDefaults(t *testing.T) {
	d, e := newAYStereoTest(t)
	d.WriteReg(0x08, 0x20)
	d.WriteReg(0x09, 0xE0) // all three chips mono

	d.Reset()

	for i := 0; i < 3; i++ {
		if got := e.Chip(i).StereoModeSetting(); got != ay.StereoABC {
			t.Errorf("chip %d after reset = %v, want ABC (both registers back to 0)", i, got)
		}
	}
}
