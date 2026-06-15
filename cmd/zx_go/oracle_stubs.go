//go:build !oracle

package main

// The reference-emulator comparison/oracle tooling (compare-foreign, the
// --next-bisect / --next-lockstep / --next-nrdiff / --next-memdiff first-
// divergence drivers, and reference-state capture) is development-only and is
// compiled only under the `oracle` build tag — it is not part of the shipped
// emulator. These stubs keep the default build self-contained; rebuild with
// `go build -tags oracle ./cmd/zx_go` to enable the real implementations.

import "errors"

var errOracleBuild = errors.New("development-only oracle tooling: rebuild with -tags oracle to enable")

func runCaptureNextSnapshot(string) error  { return errOracleBuild }
func runNextBisectFromSpec(string) error   { return errOracleBuild }
func runNextLockstepFromSpec(string) error { return errOracleBuild }
func runNextNRDiffFromSpec(string) error   { return errOracleBuild }
func runNextMemDiffFromSpec(string) error  { return errOracleBuild }

func (d *remoteDebugger) cmdCompareForeign([]string) string {
	return "ERR compare-foreign is a development-only oracle command (rebuild with -tags oracle)"
}
