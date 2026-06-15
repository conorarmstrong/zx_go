// Package config persists user settings across emulator runs.
// The file lives at $UserConfigDir/zx_go/config.json — same
// convention as pkg/next/install for the Next ROM blobs.
//
// Schema is intentionally additive: unknown fields are ignored
// on load and missing fields use zero values. This keeps forward
// and backward compatibility cheap as new settings appear.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds persisted user settings. Fields are explicitly
// tagged so changing the Go field name doesn't break existing
// config files on disk.
type Config struct {
	Model       string   `json:"model,omitempty"`        // "48K" / "128K" / "+2" / "+2A" / "+3" / "Next"
	Scale       int      `json:"scale,omitempty"`        // 100, 125, 150, 200, 300 (percent)
	Joystick    string   `json:"joystick,omitempty"`     // "None" / "Kempston" / "Sinclair1" / "Sinclair2" / "Cursor"
	CRTFilter   bool     `json:"crt_filter,omitempty"`   // CRT scanline post-process
	RecentFiles []string `json:"recent_files,omitempty"` // absolute paths, most-recent first

	// Peripheral enable states. Restored at startup, saved on each toggle.
	Disciple      bool   `json:"disciple,omitempty"`
	Multiface     string `json:"multiface,omitempty"` // "" / "MF1" / "MF128" / "MF3"
	Interface1    bool   `json:"interface1,omitempty"`
	KempstonMouse bool   `json:"kempston_mouse,omitempty"`
	ZXPrinter     bool   `json:"zx_printer,omitempty"`
	SpecDrum      bool   `json:"specdrum,omitempty"` // Cheetah SpecDrum DAC ($DF)
	Covox         bool   `json:"covox,omitempty"`    // Covox DAC ($FB)

	// Spectrum Next SD card. Exactly one of these is honoured at
	// boot; NextSDImage takes precedence if both are set. Empty
	// strings fall back to install.SDCardRoot()'s default search.
	NextSDDir   string `json:"next_sd_dir,omitempty"`   // host directory served as a virtual FAT16 SD
	NextSDImage string `json:"next_sd_image,omitempty"` // host .img/.mmc file served as-is
}

// MaxRecentFiles caps the recent-files MRU list. Anything older
// drops off the end on AddRecent.
const MaxRecentFiles = 10

// Path returns the absolute path to config.json. The parent
// directory is created if missing.
func Path() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	dir := filepath.Join(cfg, "zx_go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads config.json. If the file does not exist (fresh
// install) it returns a zero Config and no error. A corrupt file
// returns an error so the caller can decide whether to ignore it
// (use defaults) or report it to the user.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// Save writes config.json atomically (write-temp + rename) so a
// crash mid-write doesn't truncate the existing config.
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, p, err)
	}
	return nil
}

// AddRecent prepends path to RecentFiles. Existing entries are
// deduplicated and the list is capped at MaxRecentFiles.
func (c *Config) AddRecent(path string) {
	out := make([]string, 0, len(c.RecentFiles)+1)
	out = append(out, path)
	for _, p := range c.RecentFiles {
		if p != path {
			out = append(out, p)
		}
	}
	if len(out) > MaxRecentFiles {
		out = out[:MaxRecentFiles]
	}
	c.RecentFiles = out
}
