package sdcard

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Cross-process SD persistence (todo: "guest writes to sd.img live
// only in RAM"). Policy: opt-in write-back. The source tracks
// whether any guest write landed; WriteBackTo persists the image,
// first moving the previous file to <path>.bak so a crash mid-write
// can't destroy the only copy of a 1 GB card.
func TestImageSourceDirtyTracking(t *testing.T) {
	img := make([]byte, 1024)
	src, err := NewImageSource(img, false)
	if err != nil {
		t.Fatal(err)
	}
	if src.Dirty() {
		t.Fatal("fresh source must not be dirty")
	}
	var blk [512]byte
	if err := src.ReadBlock(0, blk[:]); err != nil {
		t.Fatal(err)
	}
	if src.Dirty() {
		t.Fatal("reads must not mark dirty")
	}
	blk[0] = 0xAA
	if err := src.WriteBlock(1, blk[:]); err != nil {
		t.Fatal(err)
	}
	if !src.Dirty() {
		t.Fatal("write must mark dirty")
	}
}

func TestImageSourceWriteBackWithBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sd.img")
	orig := bytes.Repeat([]byte{0x11}, 1024)
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := append([]byte(nil), orig...)
	src, err := NewImageSource(loaded, false)
	if err != nil {
		t.Fatal(err)
	}
	var blk [512]byte
	for i := range blk {
		blk[i] = 0x22
	}
	if err := src.WriteBlock(0, blk[:]); err != nil {
		t.Fatal(err)
	}

	if err := src.WriteBackTo(path); err != nil {
		t.Fatalf("WriteBackTo: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:512], blk[:]) {
		t.Error("written image does not carry the guest write")
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if !bytes.Equal(bak, orig) {
		t.Error("backup does not preserve the original image")
	}
}
