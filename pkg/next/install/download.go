package install

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDistroURL is the official Spectrum Next "complete distro"
// archive. It contains the licensed NextZXOS ROMs (machines/next/
// enNextZX.rom, enNxtmmc.rom) plus the full SD-card tree. We do NOT
// bundle these ROMs with the emulator (they are not ours to
// redistribute); the user downloads them from this official source.
//
// 26.11a carries NextZXOS 2.10a and firmware 1.46 (core 3.03.03).
// It is the validated baseline: it cold-boots to the welcome/menu/Browser
// and the firmware's Info page reports the correct product/distro versions.
// The version in the URL moves on over time; any sn-complete release with
// machines/next/enNextZX.rom and enNxtmmc.rom works. Overridable via
// $ZX_GO_NEXT_DISTRO_URL so the UI / tests can point elsewhere without a
// rebuild.
const DefaultDistroURL = "https://www.specnext.com/distro/26.11a/sn-complete-26.11a.zip"

// DistroURL returns the configured distro download URL.
func DistroURL() string {
	if u := os.Getenv("ZX_GO_NEXT_DISTRO_URL"); u != "" {
		return u
	}
	return DefaultDistroURL
}

// distroROMPaths maps each install-dir ROM basename to the in-zip
// path suffix it is extracted from. Matching is case-insensitive and
// by suffix so a leading distro-root folder ("sn-complete-24.11/…")
// doesn't matter.
var distroROMPaths = map[string]string{
	DistroROM:    "machines/next/ennextzx.rom",
	DivMMCROM:    "machines/next/ennxtmmc.rom",
	MultifaceROM: "machines/next/ennextmf.rom",
}

// DownloadResult reports what DownloadNextROMs installed.
type DownloadResult struct {
	// InstalledROMs are the install-dir basenames written (e.g.
	// enNextZX.rom).
	InstalledROMs []string
	// SDFiles is the number of files extracted into the SD root.
	SDFiles int
	// SDRoot is the directory the SD-card tree was written to.
	SDRoot string
	// BytesDownloaded is the size of the fetched archive.
	BytesDownloaded int64
}

// DownloadNextROMs fetches the distro zip from url using client (pass
// nil for http.DefaultClient), then:
//
//   - installs machines/next/enNextZX.rom and enNxtmmc.rom into the
//     install dir (Path()), the two ROMs ModelNext needs to boot;
//   - extracts the FULL archive tree into the SD root (SDCardRoot()
//     or <install>/sd), which is the bootable card content.
//
// progress, if non-nil, is called with (bytesSoFar, totalBytes)
// during the download; totalBytes is -1 when the server doesn't send
// Content-Length. The function is cancellable via ctx.
//
// It is an error if the archive does not contain enNextZX.rom (the
// required boot ROM) — that means the URL didn't point at a Next
// distro.
func DownloadNextROMs(ctx context.Context, client *http.Client, url string, progress func(done, total int64)) (*DownloadResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if url == "" {
		url = DistroURL()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	body, err := readAllWithProgress(resp.Body, resp.ContentLength, progress)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	installDir, err := Path()
	if err != nil {
		return nil, err
	}
	sdRoot := SDCardRoot()
	if sdRoot == "" {
		sdRoot = filepath.Join(installDir, "sd")
	}

	res := &DownloadResult{SDRoot: sdRoot, BytesDownloaded: int64(len(body))}
	romHit := map[string]bool{}

	// Some distro archives wrap everything in a single top-level
	// folder (e.g. "sn-complete-24.11/…"); the official sn-complete
	// zip does NOT — its top level is machines/ apps/ nextzxos/ …
	// directly. Strip the wrapper ONLY when one genuinely exists, so
	// the SD tree keeps its real layout in both cases (an early
	// version always stripped the first path component and mangled
	// machines/next/… → next/… for the unwrapped distro).
	wrapper := commonWrapperDir(zr.File)

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		norm := normaliseZipPath(f.Name) // lower-cased, for MATCHING only

		// SD tree: write every file into sdRoot, preserving its
		// ORIGINAL-CASE path (minus any common wrapper folder). The
		// SD card content keeps its real filenames — lower-casing it
		// here (an earlier bug reused the match-normalised path for
		// the write) renames every file.
		orig := filepath.ToSlash(strings.ReplaceAll(f.Name, "\\", "/"))
		rel := stripWrapper(orig, wrapper)
		if rel != "" {
			if err := extractZipFile(f, filepath.Join(sdRoot, filepath.FromSlash(rel))); err != nil {
				return nil, err
			}
			res.SDFiles++
		}

		// Promote the licensed ROMs into the install dir.
		for base, suffix := range distroROMPaths {
			if strings.HasSuffix(norm, suffix) && !romHit[base] {
				if err := extractZipFile(f, filepath.Join(installDir, base)); err != nil {
					return nil, err
				}
				romHit[base] = true
				res.InstalledROMs = append(res.InstalledROMs, base)
			}
		}
	}

	if !romHit[DistroROM] {
		return nil, fmt.Errorf("archive does not contain %s (machines/next/%s) — not a Spectrum Next distro?",
			DistroROM, DistroROM)
	}
	if !romHit[DivMMCROM] {
		return nil, fmt.Errorf("archive does not contain %s (machines/next/%s) — not a bootable Spectrum Next distro?",
			DivMMCROM, DivMMCROM)
	}

	// The stock 26.11a distro ships machines/next/CONFIG.INI with
	// timing=8, the interactive testcard/video-mode wizard. On real
	// hardware that is deliberate: a monitor may not support the
	// stored mode. In an emulator the display is fixed, so landing on
	// the wizard instead of the NextZXOS menu is a poor out-of-box
	// experience. Patch the stock config to timing=3, preserving every
	// other setting, and handle the uppercase filename the distro uses
	// (case-sensitive hosts otherwise end up with two config files).
	if err := normaliseInstalledConfig(sdRoot); err != nil {
		return nil, fmt.Errorf("normalise machines/next config: %w", err)
	}
	return res, nil
}

// normaliseInstalledConfig makes the extracted distro boot straight to
// the NextZXOS menu. It:
//
//   - finds machines/next/{config.ini,CONFIG.INI} case-insensitively;
//   - patches the stock testcard setting timing=8 to timing=3;
//   - seeds defaultNextConfigINI when neither file is present.
//
// It deliberately leaves any other timing value alone, and never
// overwrites an existing config's other keys. DownloadNextROMs has just
// replaced the SD tree, so "existing" here means the distro's own file,
// not a user's hand-edited card.
func normaliseInstalledConfig(sdRoot string) error {
	nextDir := filepath.Join(sdRoot, "machines", "next")
	if st, err := os.Stat(nextDir); err != nil || !st.IsDir() {
		// A partial/odd archive without a machine directory: leave it
		// to the boot path to report. Seeding into a directory that
		// doesn't exist would be worse than useless.
		return nil
	}
	entries, err := os.ReadDir(nextDir)
	if err != nil {
		return err
	}
	var cfgPaths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), "config.ini") {
			cfgPaths = append(cfgPaths, filepath.Join(nextDir, e.Name()))
		}
	}
	if len(cfgPaths) == 0 {
		cfgPath := filepath.Join(nextDir, "config.ini")
		return os.WriteFile(cfgPath, []byte(defaultNextConfigINI), 0o644)
	}
	for _, cfgPath := range cfgPaths {
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			return err
		}
		patched := patchTimingTo3(data)
		if bytes.Equal(patched, data) {
			continue
		}
		if err := os.WriteFile(cfgPath, patched, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// patchTimingTo3 changes an exact `timing=8` line (the 26.11a stock
// testcard setting) to `timing=3`, leaving comments, CRLF endings and
// every other key untouched. Matching the whole trimmed line avoids
// turning values like `timing=80` into `timing=30`.
func patchTimingTo3(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	changed := false
	for i, line := range lines {
		cr := strings.HasSuffix(line, "\r")
		body := line
		if cr {
			body = strings.TrimSuffix(body, "\r")
		}
		if strings.TrimSpace(body) == "timing=8" {
			lines[i] = "timing=3"
			if cr {
				lines[i] += "\r"
			}
			changed = true
		}
	}
	if !changed {
		return data
	}
	return []byte(strings.Join(lines, "\n"))
}

// readAllWithProgress reads r fully, invoking progress as it goes.
func readAllWithProgress(r io.Reader, total int64, progress func(done, total int64)) ([]byte, error) {
	var buf bytes.Buffer
	chunk := make([]byte, 64*1024)
	var done int64
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if err == io.EOF {
			return buf.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// normaliseZipPath lower-cases and forward-slashes a zip entry name
// for suffix matching (case-insensitive; zip uses '/').
func normaliseZipPath(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
}

// commonWrapperDir returns the single top-level directory that ALL
// (non-directory) entries live under, or "" when entries span
// multiple top-level names — i.e. there is no wrapper to strip. Used
// to tell a "sn-complete-24.11/…" wrapped archive from the official
// distro whose top level is the SD-card contents directly.
func commonWrapperDir(files []*zip.File) string {
	wrapper := ""
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(f.Name, "\\", "/")))
		if clean == "." || strings.HasPrefix(clean, "../") {
			continue
		}
		i := strings.IndexByte(clean, '/')
		if i < 0 {
			// A top-level FILE → there is no single wrapping folder.
			return ""
		}
		top := clean[:i]
		if wrapper == "" {
			wrapper = top
		} else if !strings.EqualFold(wrapper, top) {
			return ""
		}
	}
	return wrapper
}

// stripWrapper removes the wrapper directory prefix (if any) from a
// normalised slash path so files land directly under the SD root.
// Returns "" for a path that escapes via "..".
func stripWrapper(slashPath, wrapper string) string {
	clean := filepath.ToSlash(filepath.Clean(slashPath))
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return ""
	}
	if wrapper != "" {
		prefix := strings.ToLower(wrapper) + "/"
		if strings.HasPrefix(strings.ToLower(clean), prefix) {
			return clean[len(prefix):]
		}
	}
	return clean
}

// extractZipFile writes one zip entry to dest, creating parent dirs.
// The caller is responsible for confining dest to its root — the SD
// path goes through stripWrapper (which rejects "../"-escaping
// entries) and the ROM path uses fixed basenames, so a malicious
// archive entry cannot write outside the install/SD directories.
func extractZipFile(f *zip.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// defaultNextConfigINI is the machines/next/config.ini we seed when the
// distro doesn't ship one. Values mirror the stock System/Next 26.11a
// CONFIG.INI (firmware 1.46) with one deliberate difference: timing=3
// instead of the stock timing=8. timing=8 is the interactive testcard /
// video-mode wizard, which makes no sense when the emulator's display is
// fixed; timing=3 boots straight to the NextZXOS welcome/menu. Keep the
// remaining keys in step with the distro's CONFIG.INI so new firmware
// settings (ps2, dma, ay48, uart_i2c, kmouse, mousedpi, expbus, pidebug)
// are present and explicit.
const defaultNextConfigINI = `; ZX Spectrum Next config.ini (seeded by zx_go on ROM download)
;
; Values mirror the stock System/Next 26.11a CONFIG.INI, except timing=3:
; the stock timing=8 is the interactive testcard wizard, which an
; emulator does not need. boot.bin skips the wizard and goes to the
; NextZXOS menu.
scandoubler=1
50_60hz=0
timex=1
psgmode=1
intsnd=1
stereomode=0
turbosound=1
divmmc=0
mf=0
joystick1=1
joystick2=3
ps2=1
dma=0
scanlines=0
turbokey=1
default=0
timing=3
keyb_issue=0
divports=1
dac=1
ay48=0
uart_i2c=1
kmouse=1
ulaplus=1
hdmisound=1
beepmode=0
buttonswap=0
mousedpi=1
espreset=0
expbus=0
pidebug=0
`
