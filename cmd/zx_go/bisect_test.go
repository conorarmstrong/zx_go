package main

import (
	"errors"
	"testing"
)

// The bisection engine finds the first checkpoint at which our state
// diverges from a reference, given a monotone "matches at checkpoint k"
// predicate (true for every k < D, false for every k >= D). This models
// the kill-and-restart binary search: each probe re-runs the foreign
// oracle from cold to checkpoint k and asks "do we still agree?".

func TestBisectFirstDivergenceMidpoint(t *testing.T) {
	const d = 137
	matches := func(k int) (bool, error) { return k < d, nil }
	got, err := bisectFirstDivergence(0, 1000, matches)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != d {
		t.Fatalf("first divergence = %d, want %d", got, d)
	}
}

func TestBisectNoDivergenceReturnsHiPlusOne(t *testing.T) {
	got, err := bisectFirstDivergence(0, 500, func(int) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 501 {
		t.Fatalf("no-divergence sentinel = %d, want 501 (hi+1)", got)
	}
}

func TestBisectDivergesAtRangeStart(t *testing.T) {
	got, err := bisectFirstDivergence(10, 99, func(int) (bool, error) { return false, nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 10 {
		t.Fatalf("first divergence = %d, want 10 (lo)", got)
	}
}

func TestBisectEmptyRangeErrors(t *testing.T) {
	if _, err := bisectFirstDivergence(50, 49, func(int) (bool, error) { return true, nil }); err == nil {
		t.Fatal("expected error for empty range lo>hi")
	}
}

func TestBisectProbeErrorPropagates(t *testing.T) {
	sentinel := errors.New("socket closed")
	_, err := bisectFirstDivergence(0, 1000, func(int) (bool, error) { return false, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped %v", err, sentinel)
	}
}

func TestBisectIsLogarithmic(t *testing.T) {
	const d = 654321
	probes := 0
	got, err := bisectFirstDivergence(0, 1<<20, func(k int) (bool, error) {
		probes++
		return k < d, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != d {
		t.Fatalf("first divergence = %d, want %d", got, d)
	}
	// log2(2^20) = 20; allow a couple extra for boundary probes. A
	// linear scan would be ~654k probes — this guards the bisection.
	if probes > 25 {
		t.Fatalf("probes = %d, expected ~log2(range) (<=25)", probes)
	}
}
