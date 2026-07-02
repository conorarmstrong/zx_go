package install

import (
	"os"
	"path/filepath"
	"testing"
)

// A user running `zx_go --next` with no env/config should get a
// bootable card automatically when one is present at the standard
// install location (<install-dir>/sd.img): the FAT16 builder
// fallback is not bootable to NextZXOS, so this default matters.
func TestSDCardImageDefaultsToInstallDirSDImg(t *testing.T) {
	t.Setenv("ZX_GO_NEXT_SD_IMG", "")
	// Redirect the install dir to a temp dir so this test's sd.img
	// writes never touch a developer's real install directory.
	t.Setenv("ZX_GO_NEXT_ROM_DIR", t.TempDir())
	old := ConfiguredSDImage
	ConfiguredSDImage = ""
	t.Cleanup(func() { ConfiguredSDImage = old })

	dir, err := Path()
	if err != nil {
		t.Skipf("no install dir: %v", err)
	}
	img := filepath.Join(dir, "sd.img")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Skipf("cannot write install dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(img) })

	if got := SDCardImage(); got != img {
		t.Errorf("SDCardImage() = %q, want %q (install-dir default)", got, img)
	}
}
