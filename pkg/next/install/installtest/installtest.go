// Package installtest provides test helpers for callers that need
// to exercise ModelNext construction paths without touching the
// developer's real install directory.
//
// This package exists so the same "redirect to a temp dir" boilerplate
// doesn't get copy-pasted across every test file that constructs a
// Next memory bus or harness. Importing "testing" from a non-_test.go
// file is permitted by convention for testutil packages.
package installtest

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
)

// RedirectConfig points install.Path() at a fresh temp directory
// for the duration of the test by setting ZX_GO_NEXT_ROM_DIR.
// Returns the resolved path so callers can drop fixture files into
// it. Also sets HOME / XDG_CONFIG_HOME / APPDATA for any code that
// still calls os.UserConfigDir() directly (belt-and-suspenders;
// the install package itself no longer uses UserConfigDir).
//
// Use this any time a test would otherwise call install.Path() or
// install.LoadROM(): without the redirect those reach
// repo-relative ./roms/next/, which is fine for prod but should
// never be written to by tests.
func RedirectConfig(t testing.TB) string {
	t.Helper()
	tmp := t.TempDir()
	nextDir := filepath.Join(tmp, "zx_go", "next")
	t.Setenv("ZX_GO_NEXT_ROM_DIR", nextDir)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("APPDATA", tmp)
	return nextDir
}

// AssertSandboxed fails the test if install.Path() resolves to
// somewhere outside a temp directory. Call this from test fixtures
// that are about to write to install.Path() so a missing
// RedirectConfig call surfaces immediately instead of silently
// truncating the developer's real NextZXOS ROM.
//
// Heuristic: the path must be under one of the common temp-dir
// prefixes (/tmp, /var/folders on macOS, Windows TEMP via the
// env var). Anything else — most notably anything under
// $HOME/Library/Application Support — fails the test.
func AssertSandboxed(t testing.TB) {
	t.Helper()
	dir, err := install.Path()
	if err != nil {
		t.Fatalf("install.Path: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if !isUnderTempDir(abs) {
		t.Fatalf("install.Path() resolves to %q — this is NOT a temp directory; the test is about to write to the developer's real install dir. Add installtest.RedirectConfig(t) before any fixture write.", abs)
	}
}

// isUnderTempDir returns true if abs is plausibly inside a system
// temp directory. macOS uses /var/folders/... for t.TempDir;
// Linux uses /tmp; Windows uses %TEMP% which is usually
// C:\Users\<u>\AppData\Local\Temp.
func isUnderTempDir(abs string) bool {
	for _, prefix := range []string{"/tmp/", "/var/folders/", "/private/tmp/", "/private/var/folders/"} {
		if strings.HasPrefix(abs, prefix) {
			return true
		}
	}
	// Windows temp paths — check for "\\Temp\\" or "\\AppData\\Local\\Temp\\"
	if strings.Contains(abs, `\Temp\`) || strings.Contains(abs, `\AppData\Local\Temp\`) {
		return true
	}
	return false
}
