package main

import "fmt"

// bisectFirstDivergence binary-searches for the first checkpoint at
// which our state stops matching a reference oracle. It is the
// automated form of the "run the oracle to checkpoint k, diff, repeat"
// procedure: instead of probing every checkpoint linearly, it bisects.
//
// `matches(k)` must report whether our state equals the oracle's after
// both have executed k instructions from a cold reset. It is assumed
// MONOTONE: once the two diverge they stay diverged, so matches is true
// for every k < D and false for every k >= D, where D is the first
// divergence. (Monotonicity holds because a single differing byte at
// step D generally perturbs all later state.)
//
// Returns the smallest k in [lo, hi] with matches(k) == false. If the
// two still agree at hi, returns hi+1 (sentinel: "no divergence within
// the probed range — widen hi"). A probe error aborts the search.
func bisectFirstDivergence(lo, hi int, matches func(int) (bool, error)) (int, error) {
	if lo > hi {
		return 0, fmt.Errorf("bisect: empty range [%d,%d]", lo, hi)
	}
	result := hi + 1
	l, h := lo, hi
	for l <= h {
		mid := int(uint(l+h) >> 1)
		ok, err := matches(mid)
		if err != nil {
			return 0, fmt.Errorf("bisect: probe at %d: %w", mid, err)
		}
		if ok {
			l = mid + 1
		} else {
			result = mid
			h = mid - 1
		}
	}
	return result, nil
}
