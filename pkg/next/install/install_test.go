package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// redirectConfig is the local in-package helper. Cross-package
// callers should use install/installtest.RedirectConfig. We can't
// just import installtest here because that would be a self-cycle
// (installtest is documented as a child of install).
// redirectConfig points install.Path() at a temp directory. Mirrors
// installtest.RedirectConfig but local to the install package
// (importing installtest here would be a cycle since installtest
// imports install). Must set ZX_GO_NEXT_ROM_DIR — the legacy
// HOME / XDG_CONFIG_HOME / APPDATA envs are no longer consulted
// by Path() (it now uses repo-relative resolution by default).
func redirectConfig(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	nextDir := filepath.Join(tmp, "zx_go", "next")
	t.Setenv("ZX_GO_NEXT_ROM_DIR", nextDir)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("APPDATA", tmp)
	return nextDir
}

func TestInstallROMRoundTrip(t *testing.T) {
	redirectConfig(t)

	src := filepath.Join(t.TempDir(), "fake.rom")
	content := []byte("ZX Next test ROM blob; not a real ROM.")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := InstallROM(src)
	if err != nil {
		t.Fatalf("InstallROM: %v", err)
	}

	data, err := os.ReadFile(got.DestPath)
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("installed bytes differ from source")
	}

	expected := sha256.Sum256(content)
	if got.SHA256 != hex.EncodeToString(expected[:]) {
		t.Errorf("SHA mismatch: got %s, want %s", got.SHA256, hex.EncodeToString(expected[:]))
	}
	if got.Size != int64(len(content)) {
		t.Errorf("Size = %d, want %d", got.Size, len(content))
	}
}

func TestPathCreatesDirectory(t *testing.T) {
	redirectConfig(t)

	dir, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat install dir: %v", err)
	}
	if !st.IsDir() {
		t.Errorf("Path = %q is not a directory", dir)
	}
}

func TestLoadROMReturnsInstalledBytes(t *testing.T) {
	redirectConfig(t)

	// Install a fake ROM first.
	src := filepath.Join(t.TempDir(), "myrom.bin")
	content := []byte{0x01, 0x02, 0x03, 0x04}
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallROM(src); err != nil {
		t.Fatal(err)
	}

	got, err := LoadROM("myrom.bin")
	if err != nil {
		t.Fatalf("LoadROM: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("LoadROM returned %x, want %x", got, content)
	}
}

func TestLoadROMMissingReturnsSentinel(t *testing.T) {
	redirectConfig(t)

	_, err := LoadROM("definitely-not-installed.rom")
	if !errors.Is(err, ErrROMNotInstalled) {
		t.Errorf("expected ErrROMNotInstalled, got %v", err)
	}
}

// TestInstallROMVersionMismatchWarning exercises the cross-check
// against the SD card source: installing an enNxtmmc.rom whose bytes
// differ from the same-named file on the SD card should set
// VersionWarning so the user sees the problem at install time
// instead of post-boot when NextZXOS traps with "Version mismatch:".
// This is the exact regression that ate three sessions of debugging
// before being identified — keep the test as a regression marker.
func TestInstallROMVersionMismatchWarning(t *testing.T) {
	redirectConfig(t)

	// Set up an SD card source directory with a "different" copy.
	sdRoot := filepath.Join(t.TempDir(), "sd")
	sdMachineDir := filepath.Join(sdRoot, "machines", "next")
	if err := os.MkdirAll(sdMachineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdBytes := []byte("NEW divMMC ROM bytes (version 0209)")
	if err := os.WriteFile(filepath.Join(sdMachineDir, DivMMCROM), sdBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZX_GO_NEXT_SD_DIR", sdRoot)

	// Now install a different ROM under the well-known name.
	src := filepath.Join(t.TempDir(), DivMMCROM)
	if err := os.WriteFile(src, []byte("OLD divMMC ROM bytes (version 4206)"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := InstallROM(src)
	if err != nil {
		t.Fatalf("InstallROM: %v", err)
	}
	if got.VersionWarning == "" {
		t.Fatalf("expected VersionWarning to be set; got empty")
	}
	if !contains(got.VersionWarning, "Version mismatch") {
		t.Errorf("VersionWarning should mention \"Version mismatch\"; got %q", got.VersionWarning)
	}
}

// TestInstallROMVersionMatchSilent confirms no warning fires when
// the installed and SD-card copies are byte-identical.
func TestInstallROMVersionMatchSilent(t *testing.T) {
	redirectConfig(t)

	sdRoot := filepath.Join(t.TempDir(), "sd")
	sdMachineDir := filepath.Join(sdRoot, "machines", "next")
	if err := os.MkdirAll(sdMachineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("identical bytes on both sides")
	if err := os.WriteFile(filepath.Join(sdMachineDir, DistroROM), content, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZX_GO_NEXT_SD_DIR", sdRoot)

	src := filepath.Join(t.TempDir(), DistroROM)
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := InstallROM(src)
	if err != nil {
		t.Fatalf("InstallROM: %v", err)
	}
	if got.VersionWarning != "" {
		t.Errorf("expected no VersionWarning for matching content; got %q", got.VersionWarning)
	}
}

// TestInstallROMNonWellKnownNoCheck confirms the cross-check is
// scoped to the two ROMs that participate in the NextZXOS version
// handshake — a custom-named ROM should never trigger the check
// even if the SD card has a same-named different file.
func TestInstallROMNonWellKnownNoCheck(t *testing.T) {
	redirectConfig(t)

	sdRoot := filepath.Join(t.TempDir(), "sd")
	sdMachineDir := filepath.Join(sdRoot, "machines", "next")
	if err := os.MkdirAll(sdMachineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdMachineDir, "custom.rom"), []byte("sd-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZX_GO_NEXT_SD_DIR", sdRoot)

	src := filepath.Join(t.TempDir(), "custom.rom")
	if err := os.WriteFile(src, []byte("install-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := InstallROM(src)
	if err != nil {
		t.Fatalf("InstallROM: %v", err)
	}
	if got.VersionWarning != "" {
		t.Errorf("non-well-known ROM should not trigger version check; got warning %q", got.VersionWarning)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ========================================================================
// SDCardRoot / SDCardImage tests.
// ========================================================================

// TestSDCardRoot_EnvOverridesAll — ZX_GO_NEXT_SD_DIR has highest priority.
func TestSDCardRoot_EnvOverridesAll(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ZX_GO_NEXT_SD_DIR", tmp)
	if got := SDCardRoot(); got != tmp {
		t.Errorf("SDCardRoot = %q, want %q (env override)", got, tmp)
	}
}

// TestSDCardRoot_EnvMissingDirReturnsEmpty — if the env var points
// at a non-existent path, SDCardRoot returns empty (vs falling
// through to a default).
func TestSDCardRoot_EnvMissingDirReturnsEmpty(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_SD_DIR", "/nonexistent/path/that/should/not/exist")
	if got := SDCardRoot(); got != "" {
		t.Errorf("SDCardRoot with missing env path = %q, want empty", got)
	}
}

// TestSDCardRoot_EnvEmptyFallsThrough — when the env var is unset
// and ConfiguredSDDir is empty, SDCardRoot tries <Path()>/sd.
// Redirecting Path() via env to a temp without that subdir gives
// empty.
func TestSDCardRoot_EnvEmptyFallsThrough(t *testing.T) {
	redirectConfig(t) // points Path() at a temp; no `sd` subdir
	t.Setenv("ZX_GO_NEXT_SD_DIR", "")
	saved := ConfiguredSDDir
	defer func() { ConfiguredSDDir = saved }()
	ConfiguredSDDir = ""
	if got := SDCardRoot(); got != "" {
		t.Errorf("SDCardRoot with no config = %q, want empty", got)
	}
}

// TestSDCardImage_EnvOverrides — ZX_GO_NEXT_SD_IMG env var wins.
func TestSDCardImage_EnvOverrides(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_SD_IMG", "/tmp/myimage.img")
	if got := SDCardImage(); got != "/tmp/myimage.img" {
		t.Errorf("SDCardImage = %q, want /tmp/myimage.img", got)
	}
}

// TestPath_NoEnvUsesRepoRoot — when ZX_GO_NEXT_ROM_DIR is unset,
// Path() walks up to find go.mod and uses <repo>/roms/next.
func TestPath_NoEnvUsesRepoRoot(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_ROM_DIR", "")
	dir, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	// findRepoRoot should land us on this repo; the suffix is fixed.
	if !filepath.IsAbs(dir) {
		t.Errorf("Path = %q, want absolute", dir)
	}
	if filepath.Base(dir) != "next" || filepath.Base(filepath.Dir(dir)) != "roms" {
		t.Errorf("Path = %q, want absolute ending with .../roms/next", dir)
	}
}

// TestFindRepoRoot_FallsThroughInAlienCwd — chdir into /tmp (no
// go.mod up the chain) and confirm findRepoRoot reports false.
func TestFindRepoRoot_FallsThroughInAlienCwd(t *testing.T) {
	// Chdir to a tempdir; t.TempDir guarantees no go.mod in any
	// parent (parent walk hits / which has no go.mod on most CI).
	tmp := t.TempDir()
	saved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(saved) }()
	if _, ok := findRepoRoot(); ok {
		// On some hosts /tmp might be under a go module — only fail
		// when we're sure no go.mod sits above us.
		// Walk up to confirm there genuinely isn't one.
		dir := tmp
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				t.Skipf("ambient go.mod at %s; can't reliably test fallthrough", dir)
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		t.Error("findRepoRoot returned ok=true under tempdir with no go.mod")
	}
}

// TestSDCardImage_EnvEmptyFallsToConfigured.
func TestSDCardImage_EnvEmptyFallsToConfigured(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_SD_IMG", "")
	saved := ConfiguredSDImage
	defer func() { ConfiguredSDImage = saved }()
	ConfiguredSDImage = "/configured/path.img"
	if got := SDCardImage(); got != "/configured/path.img" {
		t.Errorf("SDCardImage fall-through = %q, want /configured/path.img", got)
	}
}

// Covers the repo-relative fallback of Path(): when
// ZX_GO_NEXT_ROM_DIR is unset, Path() resolves via findRepoRoot and
// lands at <repo>/roms/next (the .gitignore'd safe default).
func TestPathRepoRelativeFallback(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_ROM_DIR", "") // empty = unset → repo-relative
	dir, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	// Must end in roms/next and the directory must now exist.
	if filepath.Base(dir) != "next" || filepath.Base(filepath.Dir(dir)) != "roms" {
		t.Errorf("Path() = %q, want a path ending in roms/next", dir)
	}
	if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
		t.Errorf("Path() did not create directory %q: %v", dir, statErr)
	}
}

// TestFindRepoRootLocatesGoMod confirms findRepoRoot walks up to the
// module root (this test file lives inside the module).
func TestFindRepoRootLocatesGoMod(t *testing.T) {
	root, ok := findRepoRoot()
	if !ok {
		t.Fatal("findRepoRoot: ok=false inside module")
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Errorf("findRepoRoot returned %q with no go.mod: %v", root, err)
	}
}
