package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The Spectrum Next "machine type" personality (NextReg $03 bits
// 2:0) selects which classic machine the Next emulates: 1=48K,
// 2=128K/+2, 3=+2A/+2B/+3, 4=Pentagon. The firmware config menu
// writes this in config mode; the value must read back through the
// reconstructed NR$03 byte (zxnext.vhd:5894) and drive the maskable
// frame-INT timing (FrameIntTiming). This pins every personality.
func TestNR03AllPersonalities(t *testing.T) {
	cases := []struct {
		name       string
		write      byte // NR$03 value (bit7 set = update timing, bits 2:0 = type)
		wantType   byte // expected machine_type read-back (bits 2:0)
		wantTiming byte // expected machine_timing (bits 6:4) after the write
	}{
		{"48K", 0x81, 0x01, 0x01},      // type 1, timing 000→001 (48K)
		{"128K/+2", 0xA2, 0x02, 0x02},  // type 2, timing 010 (128K)
		{"+2A/+3", 0xB3, 0x03, 0x03},   // type 3, timing 011 (+3)
		{"Pentagon", 0xC4, 0x04, 0x04}, // type 4, timing 100 (Pentagon)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
			if err != nil {
				t.Fatal(err)
			}
			disp := nextregs.New()
			WireMachineType(disp, mem)
			// machine_type is only writable in config mode
			// (zxnext.vhd:5137). On real hardware the FPGA bootrom is
			// active at power-on so config mode is on; the bare test
			// memory has no bootrom, so enter it explicitly the way
			// the firmware config editor is in when it writes the
			// chosen personality.
			mem.EnterConfigMode()
			disp.WriteReg(0x03, c.write)

			got := disp.ReadReg(0x03)
			if got&0x07 != c.wantType {
				t.Errorf("%s: NR$03 machine_type read-back = %d, want %d (full byte $%02X)",
					c.name, got&0x07, c.wantType, got)
			}
			if (got>>4)&0x07 != c.wantTiming {
				t.Errorf("%s: NR$03 machine_timing = %d, want %d (full byte $%02X)",
					c.name, (got>>4)&0x07, c.wantTiming, got)
			}

			// The same byte must drive a sensible frame-INT timing
			// (assert tstate > 0, pulse width > 0).
			assert, pulse := FrameIntTiming(c.write, false)
			if assert <= 0 || pulse <= 0 {
				t.Errorf("%s: FrameIntTiming($%02X) = assert %d pulse %d, want both > 0",
					c.name, c.write, assert, pulse)
			}
		})
	}
}

// A personality write only takes effect in config mode; once a real
// machine is installed and config mode has exited, a later NR$03
// write with a different type must NOT silently change the
// personality (zxnext.vhd:5137 gates machine_type on config_mode).
func TestNR03PersonalityLockedOutsideConfigMode(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireMachineType(disp, mem)

	disp.WriteReg(0x03, 0xB3) // +3 personality, exits config mode
	if disp.ReadReg(0x03)&0x07 != 0x03 {
		t.Fatalf("precondition: +3 personality not set")
	}
	// Now out of config mode — try to switch to 48K.
	disp.WriteReg(0x03, 0x81)
	if got := disp.ReadReg(0x03) & 0x07; got != 0x03 {
		t.Errorf("machine_type changed to %d outside config mode; must stay 3 (+3)", got)
	}
}
