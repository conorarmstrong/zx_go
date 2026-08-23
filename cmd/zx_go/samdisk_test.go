package main

import (
	"slices"
	"strings"
	"testing"
)

// An SBT cannot be told apart from a disk image by looking at it: it carries no
// signature, and a 819200-byte one would be the same size as an MGT. So the
// file picker's extension is what decides, and everything else still goes to
// sam.LoadDisk unchanged.
func TestSamDiskFromFileRoutesBySuffix(t *testing.T) {
	if !slices.Contains(samDiskExtensions, ".sbt") {
		t.Errorf("the SAM disk file filter should offer .sbt, got %v", samDiskExtensions)
	}

	sbt := make([]byte, 512)
	copy(sbt[247:], "BOOT")
	d, err := samDiskFromFile("/games/Manic.sbt", sbt)
	if err != nil {
		t.Fatalf(".sbt should build a disk: %v", err)
	}
	sec, ok := d.ReadSector(4, 0, 1)
	if !ok || sec[0x100] != 'B' {
		t.Error(".sbt did not go through LoadSBT (no boot signature at cylinder 4 sector 1)")
	}

	mgt := make([]byte, 819200)
	mgt[0] = 0x5A
	if _, err := samDiskFromFile("/games/Tetris.mgt", mgt); err != nil {
		t.Errorf(".mgt should still go through LoadDisk: %v", err)
	}
	if _, err := samDiskFromFile("/games/broken.mgt", []byte{1, 2, 3}); err == nil {
		t.Error("an unrecognised image should still be refused")
	}

	// The refusal for a bad .sbt has to name the file, not the disk format.
	_, err = samDiskFromFile("/games/notaboot.sbt", make([]byte, 512))
	if err == nil || !strings.Contains(err.Error(), "notaboot.sbt") {
		t.Errorf("a non-bootable .sbt should be refused by name, got %v", err)
	}
}

// The SAM ROM's BOOT reads drive 1 only: $591E loads DE with track 4 / sector 1
// and the seek loop that follows polls drive 1's status port, so a disk in
// drive 2 is never consulted. Telling a user to type BOOT after loading into
// drive 2 sends them at an instruction that cannot work.
func TestSamBootHintNamesDriveOneOnly(t *testing.T) {
	one := samBootHint(0)
	if !strings.Contains(one, "BOOT") {
		t.Errorf("drive 1 hint = %q, want it to offer BOOT", one)
	}

	two := samBootHint(1)
	if strings.Contains(two, "boot it with") {
		t.Errorf("drive 2 hint = %q: BOOT only reads drive 1, so this is wrong advice", two)
	}
	if !strings.Contains(two, "drive 1") {
		t.Errorf("drive 2 hint = %q, want it to say BOOT needs drive 1", two)
	}
}
