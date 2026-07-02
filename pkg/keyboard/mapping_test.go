package keyboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
)

// Covers PressMatrixKey and Get/Set/Load/SaveOverrides.

func TestPressMatrixKey_PressAndRelease(t *testing.T) {
	k := New()
	// Drive row 3 mask 0x04 (the '5' key in the Spectrum matrix) low
	// to simulate a press, then release.
	k.PressMatrixKey(3, 0x04, true)
	if k.matrix[3]&0x04 != 0 {
		t.Errorf("after press: matrix[3] bit 0x04 = 1, want 0")
	}
	k.PressMatrixKey(3, 0x04, false)
	if k.matrix[3]&0x04 == 0 {
		t.Errorf("after release: matrix[3] bit 0x04 = 0, want 1")
	}
}

func TestPressMatrixKey_OutOfRangeRowIsNoOp(t *testing.T) {
	k := New()
	before := k.matrix
	k.PressMatrixKey(-1, 0x01, true)
	k.PressMatrixKey(8, 0x01, true)
	if k.matrix != before {
		t.Errorf("OOB rows mutated matrix: before=%v after=%v", before, k.matrix)
	}
}

func TestSetGetMapping_RoundTrip(t *testing.T) {
	k := New()
	entries := []MappingEntry{{Row: 0, Mask: 0x01}, {Row: 7, Mask: 0x10}}
	k.SetMapping(fyne.KeyF1, entries)
	got := k.GetMapping(fyne.KeyF1)
	if len(got) != 2 {
		t.Fatalf("GetMapping length = %d, want 2", len(got))
	}
	if got[0].Row != 0 || got[0].Mask != 0x01 || got[1].Row != 7 || got[1].Mask != 0x10 {
		t.Errorf("GetMapping = %+v, want round-trip", got)
	}
}

func TestSetMapping_EmptyClears(t *testing.T) {
	k := New()
	// Set then clear.
	k.SetMapping(fyne.KeyF2, []MappingEntry{{Row: 1, Mask: 0x02}})
	k.SetMapping(fyne.KeyF2, []MappingEntry{})
	if got := k.GetMapping(fyne.KeyF2); len(got) != 0 {
		t.Errorf("after empty SetMapping, GetMapping = %+v, want empty", got)
	}
}

func TestGetMapping_UnknownKeyReturnsEmpty(t *testing.T) {
	k := New()
	if got := k.GetMapping("KEY-THAT-NEVER-EXISTED"); len(got) != 0 {
		t.Errorf("unknown key GetMapping = %+v, want empty", got)
	}
}

func TestLoadOverrides_MissingFileIsNoOp(t *testing.T) {
	k := New()
	if err := k.LoadOverrides("/no/such/keymap.json"); err != nil {
		t.Errorf("LoadOverrides missing file = %v, want nil", err)
	}
}

func TestLoadOverrides_CorruptReturnsError(t *testing.T) {
	k := New()
	tmp := t.TempDir()
	p := filepath.Join(tmp, "bad.json")
	if err := os.WriteFile(p, []byte("{not valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := k.LoadOverrides(p); err == nil {
		t.Error("LoadOverrides on corrupt JSON should return error")
	}
}

func TestLoadSaveOverrides_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "overrides.json")
	overrides := map[string][]MappingEntry{
		string(fyne.KeyF1): {{Row: 0, Mask: 0x01}},
		string(fyne.KeyF2): {{Row: 7, Mask: 0x10}, {Row: 0, Mask: 0x02}},
	}
	if err := SaveOverrides(p, overrides); err != nil {
		t.Fatalf("SaveOverrides: %v", err)
	}
	// Confirm the JSON parses cleanly.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string][]MappingEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("written JSON parses: %v", err)
	}
	// LoadOverrides applies them to a fresh Keyboard.
	k := New()
	if err := k.LoadOverrides(p); err != nil {
		t.Fatalf("LoadOverrides: %v", err)
	}
	if entries := k.GetMapping(fyne.KeyF1); len(entries) != 1 || entries[0].Mask != 0x01 {
		t.Errorf("F1 after load = %+v, want one entry mask=0x01", entries)
	}
	if entries := k.GetMapping(fyne.KeyF2); len(entries) != 2 {
		t.Errorf("F2 after load len = %d, want 2", len(entries))
	}
}

func TestSaveOverrides_WriteError(t *testing.T) {
	// Write to a directory that doesn't exist → os.WriteFile fails.
	err := SaveOverrides("/no/such/dir/out.json", map[string][]MappingEntry{})
	if err == nil {
		t.Error("SaveOverrides to unwritable path should return error")
	}
}

func TestSetMapping_NilKeyMapInitializes(t *testing.T) {
	k := &Keyboard{}
	// keyMap is nil; SetMapping must allocate it.
	k.SetMapping("F1", []MappingEntry{{Row: 0, Mask: 0x01}})
	if k.keyMap == nil {
		t.Error("SetMapping with nil keyMap did not initialize it")
	}
}
