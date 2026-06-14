package install

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// synthDistroZip builds an in-memory zip shaped like the official
// sn-complete distro: a leading root folder, machines/next/*.rom,
// and a couple of SD-tree files.
func synthDistroZip(t *testing.T, root string, withDivMMC bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add(root+"/machines/next/enNextZX.rom", bytes.Repeat([]byte{0xE1}, 65536))
	if withDivMMC {
		add(root+"/machines/next/enNxtmmc.rom", bytes.Repeat([]byte{0xD1}, 8192))
	}
	add(root+"/TBBLUE.FW", bytes.Repeat([]byte{0xFB}, 1024))
	add(root+"/nextzxos/enMenus-example.cfg", []byte("menu=example\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serveZip(t *testing.T, zipBytes []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", itoa(len(zipBytes)))
		_, _ = w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestDownloadNextROMsInstallsLicensedROMsAndSDTree(t *testing.T) {
	dir := redirectConfig(t)
	// SD root under the redirected install dir.
	t.Setenv("ZX_GO_NEXT_SD_DIR", filepath.Join(dir, "sd"))

	zipBytes := synthDistroZip(t, "sn-complete-24.11", true)
	srv := serveZip(t, zipBytes)

	var lastDone, lastTotal int64
	res, err := DownloadNextROMs(context.Background(), srv.Client(), srv.URL, func(done, total int64) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("DownloadNextROMs: %v", err)
	}

	// Both licensed ROMs installed into the install dir.
	for _, base := range []string{DistroROM, DivMMCROM} {
		p := filepath.Join(dir, base)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not installed: %v", base, err)
		}
	}
	// enNextZX.rom content round-trips.
	got, err := os.ReadFile(filepath.Join(dir, DistroROM))
	if err != nil || len(got) != 65536 || got[0] != 0xE1 {
		t.Errorf("enNextZX.rom content wrong (len=%d err=%v)", len(got), err)
	}
	// SD tree extracted (leading distro folder stripped).
	if _, err := os.Stat(filepath.Join(dir, "sd", "TBBLUE.FW")); err != nil {
		t.Errorf("SD tree TBBLUE.FW missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sd", "nextzxos", "enMenus-example.cfg")); err != nil {
		t.Errorf("SD tree nextzxos/ missing: %v", err)
	}
	if res.SDFiles < 3 {
		t.Errorf("SDFiles = %d, want ≥3", res.SDFiles)
	}
	if len(res.InstalledROMs) != 2 {
		t.Errorf("InstalledROMs = %v, want 2", res.InstalledROMs)
	}
	if lastTotal != int64(len(zipBytes)) || lastDone != int64(len(zipBytes)) {
		t.Errorf("progress final = %d/%d, want %d/%d", lastDone, lastTotal, len(zipBytes), len(zipBytes))
	}
}

func TestDownloadNextROMsRejectsNonDistroArchive(t *testing.T) {
	redirectConfig(t)
	// A zip with NO enNextZX.rom.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("random/file.txt")
	_, _ = w.Write([]byte("hi"))
	_ = zw.Close()
	srv := serveZip(t, buf.Bytes())

	_, err := DownloadNextROMs(context.Background(), srv.Client(), srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "not a Spectrum Next distro") {
		t.Errorf("want 'not a Spectrum Next distro' error, got %v", err)
	}
}

func TestDownloadNextROMsHTTPError(t *testing.T) {
	redirectConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := DownloadNextROMs(context.Background(), srv.Client(), srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("want HTTP 404 error, got %v", err)
	}
}

func TestDownloadNextROMsCancellation(t *testing.T) {
	redirectConfig(t)
	srv := serveZip(t, synthDistroZip(t, "root", true))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := DownloadNextROMs(ctx, srv.Client(), srv.URL, nil)
	if err == nil {
		t.Error("want error from cancelled context, got nil")
	}
}

func TestDistroURLOverride(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_DISTRO_URL", "http://example.test/x.zip")
	if got := DistroURL(); got != "http://example.test/x.zip" {
		t.Errorf("DistroURL() = %q, want override", got)
	}
	t.Setenv("ZX_GO_NEXT_DISTRO_URL", "")
	if got := DistroURL(); got != DefaultDistroURL {
		t.Errorf("DistroURL() = %q, want default", got)
	}
}

func TestStripWrapper(t *testing.T) {
	cases := []struct {
		path, wrapper, want string
	}{
		// Wrapped archive: strip the wrapper folder.
		{"root/machines/next/x.rom", "root", "machines/next/x.rom"},
		{"root/TBBLUE.FW", "root", "TBBLUE.FW"},
		// Unwrapped (the real distro): no wrapper, keep the full path.
		{"machines/next/x.rom", "", "machines/next/x.rom"},
		{"TBBLUE.FW", "", "TBBLUE.FW"},
		// Traversal escapes are dropped.
		{"../escape", "", ""},
	}
	for _, c := range cases {
		if got := stripWrapper(c.path, c.wrapper); got != c.want {
			t.Errorf("stripWrapper(%q, %q) = %q, want %q", c.path, c.wrapper, got, c.want)
		}
	}
}

// The official sn-complete distro has NO wrapper folder — its top
// level is machines/ apps/ nextzxos/ directly. Verify the SD tree is
// extracted with its real layout (machines/next/… NOT next/…) and the
// ROMs still install. (An early version always stripped the first
// path component and corrupted the whole tree.)
func TestDownloadNextROMsUnwrappedDistro(t *testing.T) {
	dir := redirectConfig(t)
	t.Setenv("ZX_GO_NEXT_SD_DIR", filepath.Join(dir, "sd"))

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		w, _ := zw.Create(name)
		_, _ = w.Write(data)
	}
	// No leading folder — mirrors the real distro.
	add("machines/next/enNextZX.rom", bytes.Repeat([]byte{0xE1}, 65536))
	add("machines/next/enNxtmmc.rom", bytes.Repeat([]byte{0xD1}, 8192))
	add("TBBLUE.FW", []byte("fw"))
	add("nextzxos/enMenus-example.cfg", []byte("menu\n"))
	_ = zw.Close()

	srv := serveZip(t, buf.Bytes())
	res, err := DownloadNextROMs(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("DownloadNextROMs: %v", err)
	}
	// ROM installed.
	if _, err := os.Stat(filepath.Join(dir, DistroROM)); err != nil {
		t.Errorf("enNextZX.rom not installed: %v", err)
	}
	// SD tree keeps the REAL layout — machines/next/, not next/.
	if _, err := os.Stat(filepath.Join(dir, "sd", "machines", "next", "enNextZX.rom")); err != nil {
		t.Errorf("SD tree machines/next/ layout wrong (the strip bug): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sd", "TBBLUE.FW")); err != nil {
		t.Errorf("SD tree TBBLUE.FW missing: %v", err)
	}
	if res.SDFiles < 4 {
		t.Errorf("SDFiles = %d, want ≥4", res.SDFiles)
	}
}

// The download seeds a default config.ini when the distro omits it
// (the stock distro does), so the card boots to the menu rather than
// the testcard wizard.
func TestDownloadNextROMsSeedsConfigINI(t *testing.T) {
	dir := redirectConfig(t)
	t.Setenv("ZX_GO_NEXT_SD_DIR", filepath.Join(dir, "sd"))
	// Distro with NO machines/next/config.ini (like the real one).
	srv := serveZip(t, synthDistroZip(t, "", true))
	if _, err := DownloadNextROMs(context.Background(), srv.Client(), srv.URL, nil); err != nil {
		t.Fatalf("DownloadNextROMs: %v", err)
	}
	cfg := filepath.Join(dir, "sd", "machines", "next", "config.ini")
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("config.ini not seeded: %v", err)
	}
}
