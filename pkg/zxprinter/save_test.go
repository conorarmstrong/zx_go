package zxprinter

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// Covers Save() plus advance() wraparound and read-during-print states.

func TestSave_WritesValidPNG(t *testing.T) {
	p := New()
	// Print one line so Bitmap() has content.
	p.HandlePortWrite(0xFB, 0x80, 0)
	p.HandlePortWrite(0xFB, 0x84, 70400)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "out.png")
	if err := p.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Confirm the file decodes as PNG.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open written file: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := png.Decode(f); err != nil {
		t.Errorf("Save output does not decode as PNG: %v", err)
	}
}

func TestSave_BadPathReturnsError(t *testing.T) {
	p := New()
	p.HandlePortWrite(0xFB, 0x80, 0)
	if err := p.Save("/no/such/dir/out.png"); err == nil {
		t.Error("Save to unwritable path should return error")
	}
}

func TestHandlePortRead_DuringStylusDark(t *testing.T) {
	p := New()
	// Motor on, stylus dark.
	p.HandlePortWrite(0xFB, 0x80, 0)
	// Immediately read — drum should be in sync area or near it, so
	// the response includes the sync bit + stylus-dark bit 7.
	v, ok := p.HandlePortRead(0xFB, 0)
	if !ok {
		t.Fatal("HandlePortRead should accept the printer port")
	}
	// Sync window or stylus-dark → ans starts at 0xBE.
	if v != 0xBE && v != 0x3E {
		t.Errorf("HandlePortRead during stylus-dark = %#x, want 0xBE or 0x3E", v)
	}
}

func TestHandlePortRead_AdvancesAcrossLineWrap(t *testing.T) {
	p := New()
	// Motor on at t=0, no writes after — only reads. The advance()
	// inside the read should still flush + wrap lines after the
	// scheduled tstates pass.
	p.HandlePortWrite(0xFB, 0x80, 0)
	_, _ = p.HandlePortRead(0xFB, 70400) // one frame worth = past line wrap
	// The read path should have wrapped at least once. Confirm
	// the printer has emitted a row.
	if p.Rows() < 1 {
		t.Errorf("read-only advance should have wrapped a line; rows = %d", p.Rows())
	}
}
