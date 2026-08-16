package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// quietEmulator builds a headless emulator for the model with sound off, and
// restores the CLI flag global afterwards.
func quietEmulator(t *testing.T, model roms.SpectrumModel) *emulator {
	t.Helper()
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	t.Cleanup(func() { cliFlagsActive = prev })

	emu, err := newEmulator(model)
	if err != nil {
		t.Fatalf("newEmulator(%s): %v", roms.GetModelName(model), err)
	}
	return emu
}

// registeredDevices is the registry's device set, in the canonical order a
// capture emits.
func registeredDevices(e *emulator) []string {
	return e.stateRegistry().Capture().Devices()
}

func hasDevice(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// The registry must describe the machine in front of it, not the union of every
// machine the emulator can build. Restore refuses a state whose device set
// differs in either direction, so a 48K registry carrying an AY makes every
// restore an error, and a +3 registry missing the FDC restores a machine whose
// disk controller is still in the present.
//
// Asserting the exact set (not "contains") is the point: it fails both when a
// device this model does not have creeps in and when one it does have is
// dropped.
func TestStateRegistryDeviceSetPerModel(t *testing.T) {
	for _, tc := range []struct {
		model roms.SpectrumModel
		want  []string
	}{
		// Every model carries the CPU. It was missing from the registry
		// entirely until a replay oracle found it: a restore returned the RAM,
		// the sound chip and the disk controller while leaving PC and the
		// register file in the present.
		//
		// 48K: no AY fitted, no disk controller.
		{roms.Model48K, []string{"audiodac", "cpu", "keyboard", "memory", "ula"}},
		// 128K: the AY-3-8912 arrives, still no FDC.
		{roms.Model128K, []string{"audiodac", "ay", "cpu", "keyboard", "memory", "ula"}},
		// +3: AY plus the uPD765 floppy controller.
		{roms.ModelPlus3, []string{"audiodac", "ay", "cpu", "keyboard", "memory", "plus3fdc", "ula"}},
	} {
		t.Run(roms.GetModelName(tc.model), func(t *testing.T) {
			emu := quietEmulator(t, tc.model)
			got := registeredDevices(emu)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("device set:\n got %v\nwant %v", got, tc.want)
			}
		})
	}
}

// The Next's set is the interesting one: the whole Next block, the TurboSound
// engine INSTEAD of the classic single AY (the engine owns that very chip), and
// none of the classic-bus devices the Next does not carry.
func TestStateRegistryDeviceSetNext(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	emu := quietEmulator(t, roms.ModelNext)
	want := []string{
		"ay.turbosound", "cpu", "keyboard", "memory",
		"next.copper", "next.dac", "next.divmmc", "next.dma", "next.layer2",
		"next.nextregs", "next.palette", "next.sprite", "next.tilemap",
		"ula",
	}
	got := registeredDevices(emu)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("device set:\n got %v\nwant %v", got, want)
	}
	// The classic single-AY identifier must NOT appear alongside the engine:
	// the ULA's chip is the engine's chip 0, so registering both would capture
	// one chip twice and restore it from two different blobs.
	if hasDevice(got, "ay") {
		t.Error("both the TurboSound engine and its own chip 0 are registered")
	}
}

// Leaving the Next takes its whole bus with it. The registry is built from the
// emulator's Next refs, so any one of them left behind after the teardown would
// describe a Layer 2 / Copper / TurboSound block on a machine that no longer
// has a Next bus — and would then refuse every classic capture as "carries a
// device this machine does not have".
func TestStateRegistryDropsNextBusOnTeardown(t *testing.T) {
	if !nextROMsInstalled() {
		t.Skip("Next ROMs not installed")
	}
	emu := quietEmulator(t, roms.ModelNext)
	unwireNextSubsystems(emu)

	for _, name := range registeredDevices(emu) {
		if strings.HasPrefix(name, "next.") || name == "ay.turbosound" {
			t.Errorf("%q still registered after the Next bus was unwired", name)
		}
	}
}

// A capture must restore the machine it was taken from. Capture, run on,
// restore, capture again: the two captures are the same bytes exactly when
// every registered device gave back what it was holding.
//
// The "the machine actually moved" guard is not ceremony. Without it a machine
// whose registered devices never change while running would pass this test no
// matter what Restore did.
func TestStateRegistryCaptureRestoreRoundTrips(t *testing.T) {
	models := []roms.SpectrumModel{roms.Model48K, roms.Model128K, roms.ModelPlus3}
	if nextROMsInstalled() {
		models = append(models, roms.ModelNext)
	}
	for _, model := range models {
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			emu := quietEmulator(t, model)
			emu.paused.Store(false)
			for i := 0; i < 60; i++ {
				runOneFrameHeadless(emu, model)
			}

			reg := emu.stateRegistry()
			before := reg.Capture()

			for i := 0; i < 20; i++ {
				runOneFrameHeadless(emu, model)
			}
			moved := reg.Capture()
			if bytes.Equal(before.Bytes(), moved.Bytes()) {
				t.Fatalf("no registered device changed over 20 frames — the fixture is inert, not the restore correct")
			}

			if err := reg.Restore(before); err != nil {
				t.Fatalf("restore: %v", err)
			}
			if after := reg.Capture(); !bytes.Equal(before.Bytes(), after.Bytes()) {
				t.Errorf("restored machine does not re-capture as the machine that was captured")
			}
		})
	}
}

// The Beta Disk interface does not exist until a TR-DOS disk is mounted. Until
// then it must not be in the registry — and a capture taken while it was absent
// must be REFUSED once it exists, not quietly applied to everything else. The
// pre-Beta capture has nothing to say about a controller that was not there;
// restoring it would rewind the machine around a controller left mid-command.
func TestStateRegistryLazyBetaDisk(t *testing.T) {
	emu := quietEmulator(t, roms.Model128K)

	beforeReg := emu.stateRegistry()
	if hasDevice(beforeReg.Capture().Devices(), "betadisk") {
		t.Fatal("betadisk registered before one was ever created")
	}
	preBeta := beforeReg.Capture()

	if _, err := emu.ensureBeta(); err != nil {
		t.Fatalf("ensureBeta: %v", err)
	}

	afterReg := emu.stateRegistry()
	if !hasDevice(afterReg.Capture().Devices(), "betadisk") {
		t.Error("beta interface is mounted but not registered")
	}
	err := afterReg.Restore(preBeta)
	if err == nil {
		t.Fatal("a capture taken before the Beta existed was accepted after it did")
	}
	if !strings.Contains(err.Error(), "betadisk") {
		t.Errorf("error should name the device that has no state: %v", err)
	}
}

// Register panics on a duplicate identifier, by design — two devices sharing a
// name would overwrite each other's state. That makes the wiring itself the
// thing under test: building the registry for every machine, with every
// optional device attached, is the proof that no real configuration trips it.
func TestStateRegistryNoDuplicateIdentifiers(t *testing.T) {
	models := []roms.SpectrumModel{
		roms.Model48K, roms.Model128K, roms.ModelPlus2, roms.ModelPlus2A,
		roms.ModelPlus3, roms.ModelPentagon, roms.ModelZX81, roms.ModelSAM,
	}
	if nextROMsInstalled() {
		models = append(models, roms.ModelNext)
	}
	for _, model := range models {
		t.Run(roms.GetModelName(model), func(t *testing.T) {
			emu := quietEmulator(t, model)
			attachEveryOptionalDevice(t, emu)

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("building the registry panicked: %v", r)
				}
			}()
			names := emu.stateRegistry().Capture().Devices()
			seen := map[string]bool{}
			for _, n := range names {
				if seen[n] {
					t.Errorf("duplicate device identifier %q in %v", n, names)
				}
				seen[n] = true
			}
		})
	}
}

// attachEveryOptionalDevice turns on the lazily-created peripherals this model
// supports, so the duplicate-identifier proof covers the widest wiring each
// machine can reach rather than only its cold-boot set.
func attachEveryOptionalDevice(t *testing.T, e *emulator) {
	t.Helper()
	if e.betaSupported() {
		if _, err := e.ensureBeta(); err != nil {
			t.Fatalf("ensureBeta: %v", err)
		}
	}
	if e.peripherals == nil {
		return
	}
	variant := e.peripherals.GetCompatibleMultifaceVariant(e.model)
	if err := e.peripherals.EnableMultiface(variant, "roms"); err != nil {
		t.Logf("multiface not attached (%v) — the duplicate proof is narrower here", err)
	}
	if e.peripherals.CanEnableInterface1() {
		if rom, ok := e.mem.GetROMManager().GetROM(roms.ROMINTERFACE1); ok {
			if err := e.peripherals.EnableInterface1(rom); err != nil {
				t.Logf("interface 1 not attached (%v)", err)
			}
		}
	}
}

// Attaching a Multiface or an Interface 1 adds it to the machine, so it must
// appear in the registry — the same rule as the Beta, on the devices the
// peripheral manager owns rather than the emulator.
func TestStateRegistryOptionalPeripherals(t *testing.T) {
	emu := quietEmulator(t, roms.Model48K)
	if hasDevice(registeredDevices(emu), "multiface") {
		t.Fatal("multiface registered before one was attached")
	}
	if hasDevice(registeredDevices(emu), "if1") {
		t.Fatal("if1 registered before one was attached")
	}

	if err := emu.peripherals.EnableMultiface(emu.peripherals.GetCompatibleMultifaceVariant(roms.Model48K), "roms"); err != nil {
		t.Skipf("multiface ROM unavailable: %v", err)
	}
	rom, ok := emu.mem.GetROMManager().GetROM(roms.ROMINTERFACE1)
	if !ok {
		t.Skip("interface 1 ROM unavailable")
	}
	if err := emu.peripherals.EnableInterface1(rom); err != nil {
		t.Fatalf("EnableInterface1: %v", err)
	}

	got := registeredDevices(emu)
	if !hasDevice(got, "multiface") {
		t.Errorf("multiface attached but not registered: %v", got)
	}
	if !hasDevice(got, "if1") {
		t.Errorf("interface 1 attached but not registered: %v", got)
	}
}

// The processor was absent from the registry entirely: every peripheral had a
// Device and the CPU did not, so a restore returned the RAM, the sound chip
// and the disk controller while leaving PC, the register file, the interrupt
// flip-flops and the T-state counter in the present. The machine would resume
// from the wrong instruction with the right memory.
//
// Found by the replay oracle, which would have diverged at instruction 0 on
// every model.
func TestTheRegistryIncludesTheCPUOnEveryModel(t *testing.T) {
	for _, m := range []roms.SpectrumModel{roms.Model48K, roms.Model128K, roms.ModelPlus3, roms.ModelNext} {
		emu := quietEmulator(t, m)
		if !hasDevice(registeredDevices(emu), "cpu") {
			t.Errorf("%v: registry has no \"cpu\" device, so a restore leaves PC and the "+
				"register file untouched", roms.GetModelName(m))
		}
	}
}

// pkg/next/lores has a machinestate.Device and is NOT in the registry, which
// looks like an omission and is not one: nothing in the emulator imports that
// package, so there is no instance to register. Its state.go was written for a
// device the machine does not currently hold.
//
// This pins the situation rather than the wish. If LoRes is later wired into
// the machine, this test fails and points at the registry, which is the moment
// the registration becomes both possible and necessary.
func TestLoResIsAnOrphanPackageAndThereforeUnregistered(t *testing.T) {
	emu := quietEmulator(t, roms.ModelNext)
	if hasDevice(registeredDevices(emu), "next.lores") {
		t.Error("next.lores is now registered, so the emulator holds a LoRes instance: " +
			"delete this test, it has served its purpose")
	}
}
