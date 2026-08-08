package main

import (
	"sync"
	"testing"

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
