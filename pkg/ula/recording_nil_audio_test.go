package ula

import "testing"

// ULA recording nil-guard paths: without an audio system attached (the
// typical test fixture), Start/StopRecording and Close must silently no-op.

func TestStartRecording_NilAudioReturnsNil(t *testing.T) {
	u := &ULA{}
	if err := u.StartRecording("/tmp/unused.wav"); err != nil {
		t.Errorf("StartRecording with nil audio = %v, want nil", err)
	}
}

func TestStopRecording_NilAudioReturnsNil(t *testing.T) {
	u := &ULA{}
	if err := u.StopRecording(); err != nil {
		t.Errorf("StopRecording with nil audio = %v, want nil", err)
	}
}

func TestIsRecording_NilAudioReturnsFalse(t *testing.T) {
	u := &ULA{}
	if u.IsRecording() {
		t.Error("IsRecording with nil audio = true, want false")
	}
}

func TestClose_NilAudioIsSafe(t *testing.T) {
	u := &ULA{}
	u.Close() // must not panic on the nil-audio path
}
