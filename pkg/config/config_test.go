package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func redirectConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// macOS uses $HOME/Library/Application Support; redirect HOME so
	// os.UserConfigDir resolves under the temp dir on darwin too.
	t.Setenv("HOME", dir)
	// Windows uses %AppData%; ditto.
	t.Setenv("AppData", dir)
	return dir
}

func TestLoadMissingFileReturnsZeroConfig(t *testing.T) {
	redirectConfig(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if c == nil {
		t.Fatal("Load returned nil config")
	}
	if c.Model != "" || c.Scale != 0 || c.Joystick != "" || c.CRTFilter {
		t.Errorf("Load missing should return zero Config, got %+v", c)
	}
	if len(c.RecentFiles) != 0 {
		t.Errorf("RecentFiles non-empty: %v", c.RecentFiles)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	redirectConfig(t)
	c := &Config{
		Model:       "+3",
		Scale:       200,
		Joystick:    "Kempston",
		CRTFilter:   true,
		RecentFiles: []string{"/tmp/game1.tap", "/tmp/game2.z80"},
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Model != "+3" || got.Scale != 200 || got.Joystick != "Kempston" || !got.CRTFilter {
		t.Errorf("Load returned %+v, want roundtrip of saved values", got)
	}
	if len(got.RecentFiles) != 2 || got.RecentFiles[0] != "/tmp/game1.tap" {
		t.Errorf("RecentFiles = %v, want [game1, game2]", got.RecentFiles)
	}
}

func TestAddRecentDedupesAndCaps(t *testing.T) {
	c := &Config{}
	c.AddRecent("a")
	c.AddRecent("b")
	c.AddRecent("a") // should move 'a' to front, not duplicate
	if got, want := c.RecentFiles, []string{"a", "b"}; !equalStrings(got, want) {
		t.Errorf("dedup: got %v, want %v", got, want)
	}

	// Fill past the cap.
	c.RecentFiles = nil
	for i := 0; i < MaxRecentFiles+5; i++ {
		c.AddRecent(string(rune('A' + i)))
	}
	if len(c.RecentFiles) != MaxRecentFiles {
		t.Errorf("cap: got %d entries, want %d", len(c.RecentFiles), MaxRecentFiles)
	}
	// Most-recent first: the last AddRecent should be at index 0.
	wantFirst := string(rune('A' + MaxRecentFiles + 4))
	if c.RecentFiles[0] != wantFirst {
		t.Errorf("MRU order: first = %q, want %q", c.RecentFiles[0], wantFirst)
	}
}

func TestLoadIgnoresUnknownFields(t *testing.T) {
	redirectConfig(t)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	// Write a JSON file with an unknown field — should load cleanly.
	raw := map[string]any{
		"model":          "Next",
		"scale":          150,
		"some_new_field": "value-future-us-adds",
	}
	data, _ := json.MarshalIndent(raw, "", "  ")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load with unknown field: %v", err)
	}
	if c.Model != "Next" || c.Scale != 150 {
		t.Errorf("Load lost known fields: %+v", c)
	}
}

func TestLoadCorruptReturnsError(t *testing.T) {
	redirectConfig(t)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("Load on corrupt file should return error")
	}
}

func TestSaveAtomic(t *testing.T) {
	redirectConfig(t)
	c := &Config{Model: "48K"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file after Save: %s", e.Name())
		}
	}
}

// blockConfigDir sets up an env where os.UserConfigDir resolves
// under tmp, then writes a regular file where MkdirAll would put
// the "zx_go" subdir — forcing the MkdirAll inside Path() to fail.
// Returns the resolved clash path (for caller cleanup if desired).
func blockConfigDir(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("AppData", tmp)
	parent, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	clash := filepath.Join(parent, "zx_go")
	if err := os.WriteFile(clash, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write clash: %v", err)
	}
}

// TestPath_MkdirError forces Path's MkdirAll to fail by planting a
// regular file where the "zx_go" subdir should live.
func TestPath_MkdirError(t *testing.T) {
	blockConfigDir(t)
	if _, err := Path(); err == nil {
		t.Error("Path with file clash should return error")
	}
}

// TestSave_PropagatesPathError reuses the same clash trick on the
// Save path. Save first calls Path so a Path failure surfaces as a
// Save failure.
func TestSave_PropagatesPathError(t *testing.T) {
	blockConfigDir(t)
	c := &Config{Model: "Next"}
	if err := c.Save(); err == nil {
		t.Error("Save should fail when Path fails")
	}
}

// TestSave_WritesJSONContent checks that a successful Save produces
// the JSON we expect on disk.
func TestSave_WritesJSONContent(t *testing.T) {
	redirectConfig(t)
	c := &Config{Model: "Next", Scale: 300, NextSDDir: "/sd/dir"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p, _ := Path()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("written JSON does not parse: %v", err)
	}
	if got.Model != "Next" || got.Scale != 300 || got.NextSDDir != "/sd/dir" {
		t.Errorf("round-trip = %+v, want Model=Next Scale=300 NextSDDir=/sd/dir", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
