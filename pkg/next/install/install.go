// Package install handles user-installed Spectrum Next ROM blobs.
// It does not import pkg/memory — so pkg/memory can call into it
// during ModelNext construction without creating a cycle through
// pkg/next (which itself imports pkg/memory for the MMU wiring). It
// does import pkg/roms (another leaf with no project imports) for the
// embedded GPL FPGA-loader fallback; that introduces no cycle.
package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Path returns the directory where Spectrum Next ROMs are installed.
// Resolution order:
//
//  1. $ZX_GO_NEXT_ROM_DIR if set (test sandbox override).
//  2. <repo-root>/roms/next if running inside a Go module —
//     walks up from cwd looking for go.mod. Lets `go test` find
//     the ROMs from any package directory and lets developers
//     run the binary from any subdirectory.
//  3. <cwd>/roms/next as a last resort.
//
// The directory is created if it does not already exist.
//
// History: earlier versions used os.UserConfigDir() (~/Library/
// Application Support/zx_go/next on macOS, $XDG_CONFIG_HOME/zx_go/
// next on Linux, %AppData%/zx_go/next on Windows). A test fixture
// missed its RedirectConfig call once and clobbered a developer's
// real installed ROM, so the layout moved repo-local where
// .gitignore can keep the binaries out of source control and a
// missing RedirectConfig at worst writes to ./roms/next/ inside
// the repo.
func Path() (string, error) {
	dir := os.Getenv("ZX_GO_NEXT_ROM_DIR")
	if dir == "" {
		if root, ok := findRepoRoot(); ok {
			dir = filepath.Join(root, "roms", "next")
		} else {
			abs, err := filepath.Abs(filepath.Join("roms", "next"))
			if err != nil {
				return "", fmt.Errorf("abs roms/next: %w", err)
			}
			dir = abs
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// findRepoRoot walks up from the current working directory looking
// for the nearest go.mod. Returns the absolute path of the
// containing directory and ok=true on success.
func findRepoRoot() (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// InstalledROM describes a ROM blob installed by InstallROM.
type InstalledROM struct {
	// DestPath is the absolute path of the installed file on the
	// host filesystem (Path() / basename of source).
	DestPath string
	// SHA256 is the lowercased hex digest of the file's contents.
	SHA256 string
	// Size is the file's byte size.
	Size int64
	// VersionWarning is set when a same-named ROM exists in the
	// configured SD card source and its bytes differ from the just-
	// installed file. Empty when no SD source is configured or when
	// the files match. NextZXOS hard-codes its own version strings
	// (e.g. "4206 enNextZX.rom") and traps with "Version mismatch:"
	// when a wrong-version enAltZX.rom is loaded from SD against a
	// running-OS version that disagrees — the trap is unrecoverable
	// and presents as an infinite border-red loop, with no clue that
	// the cause is ROM-version skew. This warning surfaces it at
	// install time instead of post-boot.
	VersionWarning string
}

// InstallROM reads the file at srcPath, copies it under Path()
// preserving the source basename, and computes the SHA-256 digest.
// The destination is overwritten if it already exists.
//
// The digest is reported to the caller (typically the install UI)
// so the user can record it; this function does NOT verify the
// digest against any pinned set of known-good values yet — that
// will become a gate on ModelNext boot in a future release.
func InstallROM(srcPath string) (*InstalledROM, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer func() { _ = src.Close() }()

	info, err := src.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", srcPath, err)
	}

	destDir, err := Path()
	if err != nil {
		return nil, err
	}
	destPath := filepath.Join(destDir, filepath.Base(srcPath))

	dest, err := os.Create(destPath)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", destPath, err)
	}
	defer func() { _ = dest.Close() }()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dest, hasher), src); err != nil {
		return nil, fmt.Errorf("copy %s: %w", srcPath, err)
	}
	if err := dest.Sync(); err != nil {
		return nil, fmt.Errorf("sync %s: %w", destPath, err)
	}

	installedHash := hex.EncodeToString(hasher.Sum(nil))
	return &InstalledROM{
		DestPath:       destPath,
		SHA256:         installedHash,
		Size:           info.Size(),
		VersionWarning: sdVersionWarning(filepath.Base(srcPath), installedHash),
	}, nil
}

// sdVersionWarning returns a user-facing warning string when the SD
// card source contains a same-named ROM whose bytes differ from the
// just-installed file. NextZXOS hard-codes its own version-string
// expectations and traps if the running OS (loaded from the install
// dir) disagrees with the version of e.g. enAltZX.rom on the SD card.
// Returns "" when no SD source is configured, no matching SD file
// exists, or the bytes match. Only fires for the two ROM filenames
// that participate in the cross-check: enNextZX.rom and enNxtmmc.rom.
func sdVersionWarning(basename, installedHash string) string {
	switch basename {
	case DistroROM, DivMMCROM:
	default:
		return ""
	}
	sdRoot := SDCardRoot()
	if sdRoot == "" {
		return ""
	}
	sdCopy := filepath.Join(sdRoot, "machines", "next", basename)
	data, err := os.ReadFile(sdCopy)
	if err != nil {
		// SD card has no copy of this ROM, or the read failed —
		// silently skip the cross-check.
		return ""
	}
	h := sha256.New()
	_, _ = h.Write(data)
	sdHash := hex.EncodeToString(h.Sum(nil))
	if sdHash == installedHash {
		return ""
	}
	return fmt.Sprintf(
		"Installed %s differs from %s. NextZXOS expects matching versions; mismatch will trap with \"Version mismatch:\" on boot. Re-install both enNextZX.rom and enNxtmmc.rom from the SAME distro as your SD card, or rebuild your SD card from the same distro as your installed ROMs.\n\n"+
			"installed SHA-256: %s\n"+
			"SD-card SHA-256:   %s",
		basename, sdCopy, installedHash, sdHash)
}

// Well-known Spectrum Next ROM filenames. The names match what
// ships in the official NextZXOS distribution image (verified
// against the sn-complete-24.11 distro at
// https://www.specnext.com/distro/24.11/). ModelNext construction
// (pkg/memory.setupNext) looks for these by basename in the
// install directory.
//
//   - DistroROM is the NextZXOS boot ROM. Distributed as a 64 KB
//     file in 24.11 (four 16 KB banks: 48K BASIC, 128K BASIC,
//     NextBASIC, NextZXOS shell). pkg/memory.setupNext currently
//     wires bank 0 only; multi-bank support arrives in Sprint 4.
//   - DivMMCROM is the 8 KB divMMC / esxDOS ROM the auto-pager
//     swaps in on M1 trigger PCs.
//   - FPGABootROM is the 8 KB FPGA boot loader that runs FIRST on
//     a real Spectrum Next cold reset. It does basic config, reads
//     TBBLUE.FW from the SD card, and hands off to the NextZXOS
//     firmware. Mirrored across $0000-$3FFF while active; clears
//     itself when NextReg $03 (machine type) is written. Optional
//     — if absent we fall back to direct enNextZX boot, which on
//     stock 24.11 distro hits the bank-2 → bank-3 $3D00 dispatch
//     that requires the bootrom-installed divMMC RAM trampoline.
const (
	DistroROM   = "enNextZX.rom"
	DivMMCROM   = "enNxtmmc.rom"
	FPGABootROM = "tbblue_loader.rom"
	// MultifaceROM is the 8 KB Multiface 128 ROM that real boot.bin
	// loads into RAMPAGE 0x05 lower half. NextZXOS uses Multiface
	// services (snapshot save, NMI menu) which read from this
	// position when the Multiface peripheral is enabled (NR\$06 bit 3).
	// Without it, those services read garbage.
	MultifaceROM = "enNextMF.rom"
	// DivMMCRAMBank1 is the OPTIONAL pre-populated divMMC RAM
	// bank-1 snapshot (8192 bytes). On real Spectrum Next, TBBLUE.FW
	// installs an elaborate IRQ stub at divMMC-RAM-bank-1 $2009
	// during FPGA config; the NextZXOS IRQ handler at $0044-$0060
	// calls into it on every frame for keyboard scan, esxDOS handle
	// maintenance, and autoexec gate signalling. If this file is
	// present at install-time we copy it into the pager at startup,
	// skipping the FPGA-bootrom -> TBBLUE.FW execution path. If
	// absent we fall back to the side-effect FRAMES bumper alone
	// (boot reaches IRQ idle but doesn't progress to Browser).
	DivMMCRAMBank1 = "divmmc_ram_bank1.bin"
	// MainRAMBankDF is the OPTIONAL pre-populated 8 KB main-RAM bank
	// content for the 8K bank our slot 7 maps to after the NextZXOS
	// memory sweep terminator (= bank \$DF). On real Spectrum Next,
	// TBBLUE.FW pre-loads valid handler code at offset \$0D82 (and
	// elsewhere); without it, the post-sweep RST \$20 dispatch to
	// \$ED82 lands in cold-RAM garbage and crashes. Extracted from a
	// working reference boot via:
	//   set-memory-zone 0
	//   read-memory 8192 8192   (= 8K bank 1 in zone 0 flat map)
	// then loaded here into our bank \$DF (= 16K bank 111 offset
	// \$2000-\$3FFF) so \$ED82 in slot 7 becomes a valid function
	// entry.
	MainRAMBankDF = "zes_main_bank_df.bin"
	// MainRAMBankDE is the companion to MainRAMBankDF — the 8 KB
	// main-RAM bank our slot 6 maps to (= bank \$DE = 16K bank 111
	// low half). The reference's slot 6 maps to bank \$00 which has
	// different content, so we populate the bank our boot will
	// actually read. Extracted via:
	//   set-memory-zone 0
	//   read-memory 0 8192   (= 8K bank 0 in zone 0 flat map)
	MainRAMBankDE = "zes_main_bank_de.bin"
	// WarmBootRAM is the OPTIONAL 2 MB Machine-RAM dump from a
	// working reference boot, loaded into all 128 RAM banks when
	// $ZX_GO_WARM_BOOT=1 is set. Used to bypass the FPGA-bootrom
	// path entirely. See cmd/zx_go/warmboot.go.
	WarmBootRAM = "zes_full_ram.bin"
	// WarmBootNRs is the companion NextReg + CPU register dump.
	// Format: header `# regs: PC=NN SP=NN ...` then 256 lines
	// `NR $NN = NNH`.
	WarmBootNRs = "zes_nextregs.txt"
	// WarmBootScreen is the captured ULA bank 5 BRAM content.
	// 6912 bytes: 6144 bytes pixels at the standard ZX ULA layout
	// (interleaved scanlines), followed by 768 bytes attrs. Stored
	// as a classic .SCR file because that's what the reference's
	// `save-screen` command produces. Used to populate
	// m.ram[5][0..0x1B00] in the warm-boot path — the ULA renders
	// from m.ram[5] but the reference's "Machine RAM" dump (zone 0)
	// doesn't include the bank-5 BRAM that holds the actual
	// displayed pixels. See docs/nextzxos-boot-flow.md iter 120.
	WarmBootScreen = "zes_screen.scr"
)

// ConfiguredSDDir, when non-empty, overrides the default SD-card
// search. Set by the GUI/CLI at startup from persisted config
// (Config.NextSDDir). Lives here rather than in pkg/config to
// avoid a back-import from pkg/next/install → pkg/config.
var ConfiguredSDDir string

// ConfiguredSDImage, when non-empty, points at a host .img/.mmc
// file that's served verbatim as the SD card (bypassing the
// FAT16 builder). Takes precedence over ConfiguredSDDir.
var ConfiguredSDImage string

// SDCardRoot returns the host directory whose contents are exposed
// to the emulator as a virtual FAT16 SD card. Resolution order:
//
//  1. $ZX_GO_NEXT_SD_DIR env var if set (test override).
//  2. ConfiguredSDDir (set by the GUI from persisted config).
//  3. <repo-root>/roms/next/sd if it exists.
//  4. "" — no SD card; the divMMC probe will see "no media" on its
//     SPI bus, and the boot falls into whatever ROM-only path the
//     Next firmware provides.
//
// The directory must already exist; SDCardRoot does not create it.
// Returns "" if no candidate exists.
func SDCardRoot() string {
	if dir := os.Getenv("ZX_GO_NEXT_SD_DIR"); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
		return ""
	}
	if ConfiguredSDDir != "" {
		if st, err := os.Stat(ConfiguredSDDir); err == nil && st.IsDir() {
			return ConfiguredSDDir
		}
	}
	romDir, err := Path()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(romDir, "sd")
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return candidate
	}
	return ""
}

// SDCardImage returns the host file path to use as a raw SD-card
// image, or "" if none configured. Resolution order:
//
//  1. $ZX_GO_NEXT_SD_IMG env var (test/CI override).
//  2. ConfiguredSDImage (persisted config).
//
// When this returns non-empty, the caller should serve the file
// directly via sdcard.NewImageSource and skip the BuildFAT16
// host-dir path.
func SDCardImage() string {
	if img := os.Getenv("ZX_GO_NEXT_SD_IMG"); img != "" {
		return img
	}
	if ConfiguredSDImage != "" {
		return ConfiguredSDImage
	}
	// 3. <install-dir>/sd.img — the standard local card. The FAT16
	// builder fallback is not bootable to NextZXOS (the development log);
	// shipping/copying a FAT32 card here makes `zx_go --next` work
	// out of the box.
	if dir, err := Path(); err == nil {
		img := filepath.Join(dir, "sd.img")
		if _, err := os.Stat(img); err == nil {
			return img
		}
	}
	return ""
}

// ErrROMNotInstalled is returned by LoadROM when the requested ROM
// file is absent from the install directory. Callers (typically
// pkg/memory.setupNext) wrap this with model-specific guidance so
// the user is told to run the install UI.
var ErrROMNotInstalled = errors.New("ROM file not installed")

// LoadROM reads a previously-installed ROM by basename. Returns
// ErrROMNotInstalled if the file is missing; any other error is
// wrapped with the offending path.
func LoadROM(filename string) ([]byte, error) {
	data, err := LoadInstalledROM(filename)
	if errors.Is(err, ErrROMNotInstalled) && filename == FPGABootROM {
		// The FPGA loader (tbblue_loader.rom) is GPLv3 open-source
		// firmware — NOT one of the licensed NextZXOS ROMs — so we
		// bundle it and fall back to the embedded copy when it's not
		// installed on disk. This makes the Next boot work out-of-box
		// once the user has the (downloaded) NextZXOS ROMs, without
		// shipping anything proprietary. See
		// LICENSES/tbblue_loader-NOTICE.md.
		if emb, eerr := roms.ReadEmbeddedROM(FPGABootROM); eerr == nil && len(emb) > 0 {
			return emb, nil
		}
	}
	return data, err
}

// LoadInstalledROM reads filename from the install dir ONLY — it does
// NOT fall back to any embedded copy. Use this when the question is
// literally "did the user install this ROM" (e.g. test harnesses that
// should boot the FPGA chain only with a genuinely-installed loader,
// not the bundled one). Returns ErrROMNotInstalled when absent.
func LoadInstalledROM(filename string) ([]byte, error) {
	dir, err := Path()
	if err != nil {
		return nil, err
	}
	full := filepath.Join(dir, filename)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrROMNotInstalled, full)
		}
		return nil, fmt.Errorf("read %s: %w", full, err)
	}
	return data, nil
}
