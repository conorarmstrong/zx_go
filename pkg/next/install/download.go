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
// The version in the URL moves on over time; any sn-complete-2x.y
// release works. Overridable via $ZX_GO_NEXT_DISTRO_URL so the UI /
// tests can point elsewhere without a rebuild.
const DefaultDistroURL = "https://www.specnext.com/distro/24.11/sn-complete-24.11.zip"

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
	DistroROM: "machines/next/ennextzx.rom",
	DivMMCROM: "machines/next/ennxtmmc.rom",
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

	// The stock distro does NOT ship machines/next/config.ini; on real
	// hardware the firmware writes it after the first-run video-mode
	// wizard. Without it our cold boot lands on the testcard wizard
	// instead of the NextZXOS menu. Seed a sensible default so the
	// downloaded card boots straight to the menu. Only written when
	// absent, so a user's own config.ini is never overwritten.
	cfgPath := filepath.Join(sdRoot, "machines", "next", "config.ini")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if mkerr := os.MkdirAll(filepath.Dir(cfgPath), 0o755); mkerr == nil {
			if werr := os.WriteFile(cfgPath, []byte(defaultNextConfigINI), 0o644); werr == nil {
				res.SDFiles++
			}
		}
	}
	return res, nil
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

// defaultNextConfigINI is the machines/next/config.ini we seed when
// the distro doesn't ship one. boot.bin reads it at startup; with it
// present the cold boot skips the interactive testcard / video-mode
// wizard and goes straight to the NextZXOS menu. Values mirror a
// standard "Next, +3 timing, scandoubler on" setup.
const defaultNextConfigINI = `; ZX Spectrum Next config.ini (seeded by zx_go on ROM download)
;
; boot.bin reads this at startup; when present it skips the
; interactive testcard / video-mode wizard and boots to the menu.
scandoubler=1
50_60hz=0
timex=1
psgmode=0
stereomode=1
intsnd=1
turbosound=1
dac=1
divmmc=0
divports=1
mf=0
joystick1=0
joystick2=0
scanlines=0
turbokey=0
default=0
timing=3
keyb_issue=0
uart_i2c=0
kmouse=0
ulaplus=1
hdmisound=1
beepmode=0
buttonswap=0
mousedpi=0
espreset=0
`
