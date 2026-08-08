package main

import (
	"sync"
	"testing"
	"time"

	"github.com/conorarmstrong/zx_go/pkg/snapshot"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The emulation goroutine reads the machine core every frame while the
// machine-switch menu, on the UI goroutine, swaps it wholesale — cpu, mem,
// ula, kbd, peripherals, model and the Next device set. Pausing first is not
// enough on its own: `paused` is only tested at the top of a loop iteration,
// so setting it does not wait for an in-flight frame to finish, and the swap
// can still land mid-frame.
//
// These tests are only meaningful under -race.

// TestModelReadIsSynchronisedWithACoreSwap drives the two goroutines that
// actually collide, doing what each really does: the emulation loop reads the
// model to pick its frame period and T-state budget, and the switch path
// replaces the core. Fails if currentModel loses its lock or a switch path
// stops taking coreMu.
func TestModelReadIsSynchronisedWithACoreSwap(t *testing.T) {
	e, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	// Stands in for the emulation goroutine.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Read outside the frame, as the pacer does.
			_ = frameDurationForModel(e.currentModel())
			// Read inside the frame, as ExecuteFrame does.
			e.coreMu.Lock()
			_ = frameTStatesForModel(e.model)
			e.coreMu.Unlock()
		}
	}()

	// Stands in for the machine-switch menu.
	for i := 0; i < 500; i++ {
		m := roms.Model48K
		if i%2 == 1 {
			m = roms.Model128K
		}
		e.coreMu.Lock()
		e.model = m
		e.coreMu.Unlock()
	}

	close(stop)
	wg.Wait()
}

// TestCoreSwapIsAtomicAgainstAFrame pins that a swap is not observable
// half-applied. The emulation goroutine must never see the new model
// alongside the old memory: it reads both under coreMu, and the switch
// replaces both under the same lock.
func TestCoreSwapIsAtomicAgainstAFrame(t *testing.T) {
	e48, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator(48K): %v", err)
	}
	e128, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatalf("newEmulator(128K): %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e48.coreMu.Lock()
			model, mem := e48.model, e48.mem
			e48.coreMu.Unlock()
			if mem == nil {
				t.Error("observed a nil memory core mid-swap")
				return
			}
			if got := mem.GetCurrentModel(); got != model {
				t.Errorf("observed model %v with memory for %v — swap was not atomic",
					roms.GetModelName(model), roms.GetModelName(got))
				return
			}
		}
	}()

	for i := 0; i < 500; i++ {
		src := e128
		if i%2 == 1 {
			src = e48
		}
		e48.coreMu.Lock()
		e48.model, e48.mem = src.model, src.mem
		e48.coreMu.Unlock()
	}

	close(stop)
	wg.Wait()
}

// The machine switch was not the only UI action that mutates live emulator
// state while the emulation goroutine reads it. Reboot and snapshot restore
// tear through the CPU registers and the whole of RAM, and both are reachable
// from the UI goroutine (menus, drag-and-drop) and the telnet debugger. They
// relied on the same advisory pause, which never waited for an in-flight
// frame.
//
// Both now take coreMu, which means each also needs a Locked variant for the
// callers that already hold it: the machine switch reboots, and RZX playback
// restores intermediate snapshots from inside the frame loop. Calling the
// wrong one there would deadlock, so these run under a timeout.

// TestRebootIsSynchronisedWithAFrame drives reboot against a frame-like
// reader. Under -race this fails if reboot stops taking the lock.
func TestRebootIsSynchronisedWithAFrame(t *testing.T) {
	e, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.coreMu.Lock()
			_ = e.cpu.PC
			_ = e.ula.BorderColour
			e.coreMu.Unlock()
		}
	}()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			e.reboot()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("reboot deadlocked against the frame reader")
	}
	close(stop)
	wg.Wait()
}

// TestRebootLockedDoesNotTakeTheLock pins the contract the switch path
// depends on: rebootLocked must be callable with coreMu already held.
// Without the split this deadlocks rather than failing.
func TestRebootLockedDoesNotTakeTheLock(t *testing.T) {
	e, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}

	done := make(chan struct{})
	go func() {
		e.coreMu.Lock()
		e.rebootLocked()
		e.coreMu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("rebootLocked took coreMu while it was already held")
	}
}

// TestApplySnapshotLockedDoesNotTakeTheLock pins the same contract for the
// RZX playback path, which restores intermediate snapshots from inside the
// frame loop.
func TestApplySnapshotLockedDoesNotTakeTheLock(t *testing.T) {
	e, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	snap := snapshot.New()
	snap.CPU.PC = 0x8000
	snap.Memory.Is128K = false

	done := make(chan struct{})
	go func() {
		e.coreMu.Lock()
		_ = applySnapshotToEmulatorLocked(e, snap)
		e.coreMu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("applySnapshotToEmulatorLocked took coreMu while it was already held")
	}
}
