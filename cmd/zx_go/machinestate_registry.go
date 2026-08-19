package main

import (
	"reflect"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

// stateRegistry collects the devices of the machine as it is wired right now.
//
// The set is per machine and not a superset, because Registry.Restore refuses a
// state whose device set differs in either direction. Listing a device this
// machine does not have turns every restore into an error; omitting one it does
// have restores a machine that is quietly part present-day — an AY still
// playing the note it reached while the rest of the machine was rewound. So
// each device is included exactly when the machine really carries it: no AY on
// a 48K, no floppy controller anywhere but the +3/+2A, no Next blocks off the
// Next.
//
// Devices created lazily — the Beta Disk interface on the first TR-DOS mount,
// a Multiface or Interface 1 when the user attaches one — appear only once they
// exist. A capture taken beforehand is then refused, which is the design
// working rather than a gap: that capture has nothing to say about a controller
// which was not there, and applying it to everything else would rewind the
// machine around a controller left mid-command.
func (e *emulator) stateRegistry() *machinestate.Registry {
	r := machinestate.New()

	// The SAM Coupé runs on pkg/sam's own memory, keyboard, sound chip and disk
	// controllers, none of which the Spectrum path knows about. e.mem and e.kbd
	// hold inert stand-ins that exist so the GUI's menu code has something to
	// read, so the SAM registers its own devices and returns before reaching
	// them — registering the stand-ins would produce a capture that restores
	// nothing the machine actually runs on.
	//
	// The CPU is shared: newSamEmulator points e.cpu at the SAM's own z80, so
	// it is the same device the Spectrum path registers, and it goes first here
	// for the same reason it does there.
	if e.sam != nil {
		registerDevices(r, e.cpu, e.sam, e.sam.Mem, e.sam.Kbd, e.sam.SAA,
			e.sam.FDC[0], e.sam.FDC[1])
		return r
	}

	// Core. The CPU comes first because without it a restore returns the RAM,
	// the sound chip and the disk controller and leaves PC, the register file,
	// the interrupt flip-flops and the T-state counter in the present — a
	// machine resuming from the wrong instruction with the right memory, which
	// is stranger than restoring nothing. e.ula is nil on the ZX80/ZX81, which
	// generate video from the CPU.
	registerDevices(r, e.cpu, e.mem, e.kbd, e.ula, e.speccyDAC)

	// AY. On the Next the TurboSound engine is the device: it owns all three
	// generators plus the $FFFD chip selection and the NextReg $06 reset, and
	// its chip 0 IS the ULA's classic AY (wireNextSubsystems hands it over).
	// Registering both would capture that one chip twice and restore it from
	// two different blobs.
	if e.nextAY != nil {
		registerDevices(r, e.nextAY)
	} else if e.ula != nil {
		registerDevices(r, e.ula.AY()) // nil on the 48K, which has no AY fitted
	}

	// Edge-connector and internal peripherals. Each accessor returns nil when
	// the machine does not carry that device.
	if e.peripherals != nil {
		registerDevices(r, e.peripherals.Plus3FDC(), e.peripherals.GetMultiface(), e.peripherals.IF1(),
			e.peripherals.GetDisciple())
	}
	registerDevices(r, e.betaDisk, e.opus)

	// The tape player exists only once a tape is mounted, and GetTapePlayer
	// returns nil until then — the same lazy shape as the Beta above, and for
	// the same reason: a capture taken with no tape in the deck must not claim
	// to carry a tape position, or restoring it would rewind the machine around
	// a load it knows nothing about.
	if e.ula != nil {
		registerDevices(r, e.ula.GetTapePlayer())
	}

	// Spectrum Next bus. All nil off the Next, and cleared together by
	// unwireNextSubsystems when the machine leaves it.
	registerDevices(r, e.nextRegs, e.nextPalette, e.nextTilemap, e.nextCopper,
		e.nextSprites, e.nextLayer2, e.nextDAC, e.nextDMA, e.nextDivMMC,
		e.nextClipWindows, e.nextReset, e.nextLoRes, e.nextULAPlus)

	// The LoRes layer carries the resolved ULA clip window as well as its own
	// registers, which is why registering it matters beyond the layer itself.
	// ClipWindows.LoadState deliberately does not push coordinates back down
	// on restore, because every consumer captures the coordinates it was given
	// under its own name from the same instant. That reasoning only holds while
	// every consumer is a registered Device — and this is one.

	return r
}

// registerDevices adds the devices that are actually present, skipping the
// absent ones.
//
// The nil check is the whole point, and a plain d == nil does not do it. Every
// optional device above is a typed pointer, and a nil one placed in a Device
// interface makes an interface value that is NOT nil, so it reaches Register —
// which asks it its name, and *ay.AY reads a field to answer. The 48K's absent
// AY would take the emulator down at startup.
func registerDevices(r *machinestate.Registry, ds ...machinestate.Device) {
	for _, d := range ds {
		if d == nil {
			continue
		}
		if v := reflect.ValueOf(d); v.Kind() == reflect.Pointer && v.IsNil() {
			continue
		}
		r.Register(d)
	}
}
