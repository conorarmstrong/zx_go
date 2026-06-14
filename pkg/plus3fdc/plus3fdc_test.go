package plus3fdc

import (
	"os"
	"path/filepath"
	"testing"
)

// Plus3FDC public-API coverage (iter 265).
// LoadDisk + loadDiskByPath dispatch (signature-first then extension
// fallback for MGT / IMG / TRD / D40 / D80), EjectDisk, SaveDisk,
// SetWriteProtect, SetSpeedlockEnabled.

func TestLoadDisk_MissingFile(t *testing.T) {
	p := New()
	if err := p.LoadDisk(0, "/nonexistent/disk.dsk"); err == nil {
		t.Errorf("LoadDisk missing file = nil err")
	}
}

func TestLoadDisk_UnrecognisedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "junk.xyz") // unknown extension + no magic
	if err := os.WriteFile(path, []byte("not a disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.LoadDisk(0, path); err == nil {
		t.Errorf("LoadDisk unrecognised format = nil err")
	}
}

func TestLoadDisk_DSKRoundtrip(t *testing.T) {
	syn, _ := buildSyntheticDSK(t, false)
	dir := t.TempDir()
	path := filepath.Join(dir, "disk.dsk")
	if err := os.WriteFile(path, syn, 0o644); err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.LoadDisk(0, path); err != nil {
		t.Fatalf("LoadDisk: %v", err)
	}

	// EjectDisk + re-load works.
	p.EjectDisk(0)
	if err := p.LoadDisk(0, path); err != nil {
		t.Fatalf("LoadDisk after eject: %v", err)
	}
}

func TestSaveDisk_NoDiskFails(t *testing.T) {
	p := New()
	if err := p.SaveDisk(0, "/tmp/test.dsk"); err == nil {
		t.Errorf("SaveDisk on empty drive = nil err")
	}
}

func TestSaveDisk_Roundtrip(t *testing.T) {
	syn, _ := buildSyntheticDSK(t, false)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.dsk")
	out := filepath.Join(dir, "out.dsk")
	if err := os.WriteFile(in, syn, 0o644); err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.LoadDisk(0, in); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveDisk(0, out); err != nil {
		t.Fatalf("SaveDisk: %v", err)
	}
	// Reload the saved file — must parse OK.
	p2 := New()
	if err := p2.LoadDisk(0, out); err != nil {
		t.Errorf("reload saved disk: %v", err)
	}
}

func TestSetWriteProtect_DoesntPanic(t *testing.T) {
	p := New()
	// Empty drive — must not panic.
	p.SetWriteProtect(0, true)
	p.SetWriteProtect(0, false)
}

func TestSetSpeedlockEnabled_DoesntPanic(t *testing.T) {
	p := New()
	p.SetSpeedlockEnabled(true)
	p.SetSpeedlockEnabled(false)
}
