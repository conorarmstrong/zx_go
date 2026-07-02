package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
)

// TestLoadInstalledDivMMCROM_MissingIsSilent verifies the common case
// (ROM simply not installed) returns nil without logging anything —
// xref tolerates "no divMMC ROM available".
func TestLoadInstalledDivMMCROM_MissingIsSilent(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_ROM_DIR", t.TempDir())

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	if rom := loadInstalledDivMMCROM(); rom != nil {
		t.Errorf("expected nil for a missing ROM, got %d bytes", len(rom))
	}
	if buf.Len() != 0 {
		t.Errorf("missing-ROM case should not log anything, got: %q", buf.String())
	}
}

// TestLoadInstalledDivMMCROM_ReadErrorIsLogged verifies a genuine read
// failure (as opposed to "not installed") is surfaced via slog rather
// than swallowed silently — matching the errors.Is(ErrROMNotInstalled)
// handling used elsewhere for the same install.LoadROM call (see
// next.go's divMMC ROM load and main.go's warm-boot reapply).
func TestLoadInstalledDivMMCROM_ReadErrorIsLogged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZX_GO_NEXT_ROM_DIR", dir)
	// A directory in place of the ROM file makes os.ReadFile fail with
	// something other than "not exist" (permission/is-a-directory),
	// i.e. a genuine read error rather than "not installed".
	if err := os.Mkdir(filepath.Join(dir, install.DivMMCROM), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	if rom := loadInstalledDivMMCROM(); rom != nil {
		t.Errorf("expected nil on read error, got %d bytes", len(rom))
	}
	if !strings.Contains(buf.String(), install.DivMMCROM) {
		t.Errorf("read error should be logged, got: %q", buf.String())
	}
}
