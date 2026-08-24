package ay

import (
	"sync"
	"testing"
)

// The Engine's own fields are reached from two goroutines in the running
// emulator, and were not synchronised while the chips behind them were.
//
// MixIntoStereo runs on the audio callback goroutine (pkg/audio's reader pulls
// it every buffer). Select and Reset run on the emulator goroutine: NextReg $06
// writes come from guest code, and a machine reboot drives the whole NextReg
// file through Reset, which calls Select. Machine -> Reboot on a Next with
// sound playing is therefore a write racing a read, which the race detector
// found through TestRebootRecoversAWedgedDMA.
//
// This is the same hazard in the smallest form that reproduces it. Without the
// synchronisation it fails under -race; with it, it passes.
func TestEngineFieldsAreSafeAcrossGoroutines(t *testing.T) {
	e := NewEngine()
	buf := make([]int16, 128)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() { // the audio callback
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				e.MixIntoStereo(buf)
				_ = e.Disabled()
				_ = e.Selected()
			}
		}
	}()

	wg.Add(1)
	go func() { // the emulator: guest writes and a reboot
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			e.Select(byte(i))
			e.SelectChip(byte(i % 3))
			e.SetStereoMode(i%2 == 0)
			e.SetMonoMask(byte(i))
			if i%128 == 0 {
				e.Reset()
			}
			// The snapshot path reaches exactly the same fields, and a rewind
			// or a savestate load happens on this goroutine while audio plays.
			// Leaving it out of this test is how the first attempt at the fix
			// locked engine.go and left enginestate.go racing.
			if i%64 == 0 {
				blob := e.SaveState()
				if err := e.LoadState(blob); err != nil {
					t.Errorf("LoadState: %v", err)
					return
				}
			}
		}
		close(stop)
	}()

	wg.Wait()
}
