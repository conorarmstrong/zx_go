package plus3fdc

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Plus3FDC public-API coverage: LoadDisk + loadDiskByPath dispatch
// (signature-first then extension fallback for MGT / IMG / TRD / D40 /
// D80), EjectDisk, SaveDisk, SetWriteProtect, SetSpeedlockEnabled.

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

// TestSaveDisk_ConcurrentWithWriteData drives a WRITE DATA command from
// one goroutine while SaveDisk serialises the same disk from another,
// the way the CPU goroutine and the UI thread can overlap in practice.
// SaveDisk must read a consistent snapshot of the track bytes rather
// than racing the in-progress write — run with -race to check.
func TestSaveDisk_ConcurrentWithWriteData(t *testing.T) {
	syn, _ := buildSyntheticDSK(t, false)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.dsk")
	if err := os.WriteFile(in, syn, 0o644); err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.LoadDisk(0, in); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			p.fdc.WriteData(0x45) // WRITE DATA (MFM)
			p.fdc.WriteData(0x00) // HDS
			p.fdc.WriteData(0x00) // C
			p.fdc.WriteData(0x00) // H
			p.fdc.WriteData(0x01) // R
			p.fdc.WriteData(0x02) // N
			p.fdc.WriteData(0x01) // EOT
			p.fdc.WriteData(0x4E) // GPL
			p.fdc.WriteData(0xFF) // DTL
			for j := 0; j < 512; j++ {
				p.fdc.WriteData(byte(i ^ j))
			}
			for k := 0; k < 7; k++ {
				p.fdc.ReadData()
			}
		}
	}()
	go func() {
		defer wg.Done()
		out := filepath.Join(dir, "out.dsk")
		for i := 0; i < 50; i++ {
			if err := p.SaveDisk(0, out); err != nil {
				t.Errorf("SaveDisk: %v", err)
				return
			}
		}
	}()
	wg.Wait()
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
