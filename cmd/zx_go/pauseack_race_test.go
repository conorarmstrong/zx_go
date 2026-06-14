package main

import (
	"runtime"
	"testing"
	"time"
)

// Regression: a state command that initiates a pause on a RUNNING CPU
// must not lose the ack signal. The old path did paused.Store(true)
// then read pauseAckCh inside waitForPauseAck — so if the headless loop
// acked (signalPauseAck closes the then-current channel and installs a
// fresh one) in that gap, the waiter grabbed the fresh channel and
// blocked until the full timeout. pauseAndAck captures the channel and
// stores paused under the same pauseAckMu critical section
// signalPauseAck takes, so the close cannot be missed.
func TestPauseAndAck_NoLostWakeup(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.pauseAckCh = make(chan struct{}) // the real constructor inits this
	d.pauseAckTimeoutMS.Store(500)     // fail fast if the ack is lost

	// Stand in for the headless loop: once it observes paused, ack via
	// the same primitive WaitIfPaused uses. The sleep biases toward the
	// interleaving the fix must tolerate (ack between store and wait).
	go func() {
		for !d.paused.Load() {
			runtime.Gosched()
		}
		time.Sleep(time.Millisecond)
		d.signalPauseAck()
	}()

	if !d.pauseAndAck() {
		t.Fatal("pauseAndAck timed out — ack signal was lost")
	}
}

// The already-paused fast path returns true immediately without needing
// a fresh ack (a BP hit, for instance, leaves the CPU paused before the
// next state command runs).
func TestPauseAndAck_AlreadyPaused(t *testing.T) {
	d := newRemoteWithCPU(t)
	d.pauseAckCh = make(chan struct{})
	d.pauseAckTimeoutMS.Store(200)
	d.paused.Store(true)
	if !d.pauseAndAck() {
		t.Fatal("pauseAndAck should return true immediately when already paused")
	}
}
