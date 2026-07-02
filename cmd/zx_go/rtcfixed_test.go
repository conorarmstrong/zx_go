package main

import (
	"testing"
	"time"
)

// ZX_GO_RTC_FIXED freezes the guest RTC at a fixed RFC3339 instant.
// Deterministic menu clock phase: with a frozen clock the NextZXOS
// menu never enters its per-second redraw task window, so key
// events always land in the deep-idle context and the launch gate
// ($5B8E SP-page check) passes reproducibly.
func TestParseRTCFixed(t *testing.T) {
	tm, ok := parseRTCFixed("2026-01-02T03:04:05Z")
	if !ok || tm.UTC() != time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) {
		t.Errorf("parseRTCFixed valid = %v %v", tm, ok)
	}
	if _, ok := parseRTCFixed("garbage"); ok {
		t.Error("garbage accepted")
	}
	if _, ok := parseRTCFixed(""); ok {
		t.Error("empty accepted")
	}
}
