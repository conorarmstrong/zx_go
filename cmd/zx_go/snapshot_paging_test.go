package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// A 128K snapshot has to carry the live $7FFD latch. Recording zero
// there put bank 0 at $C000, ROM 0 in, screen page 5 and paging
// unlocked on every reload, whatever the machine was actually doing.
func TestCreateSnapshotRecordsLivePagingLatch(t *testing.T) {
	emu, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	// Bank 3 at $C000, screen page 7, ROM 1.
	emu.mem.PageMemory(0x1B)

	snap, err := createSnapshotFromEmulator(emu)
	if err != nil {
		t.Fatalf("createSnapshotFromEmulator: %v", err)
	}
	if snap.Memory.Port7FFD != 0x1B {
		t.Errorf("Port7FFD: got %#02x, want %#02x", snap.Memory.Port7FFD, 0x1B)
	}
}

// A +2A/+3 snapshot has to carry $1FFD too, or special paging and the
// ROM 2/3 selection are lost on reload.
func TestCreateSnapshotRecordsPlus3PagingLatch(t *testing.T) {
	emu, err := newEmulator(roms.ModelPlus3)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	emu.mem.PageMemoryPlus3(0x04) // ROM-select high bit
	emu.mem.PageMemory(0x02)

	snap, err := createSnapshotFromEmulator(emu)
	if err != nil {
		t.Fatalf("createSnapshotFromEmulator: %v", err)
	}
	if snap.Memory.Port1FFD != 0x04 {
		t.Errorf("Port1FFD: got %#02x, want %#02x", snap.Memory.Port1FFD, 0x04)
	}
	if snap.Memory.Port7FFD != 0x02 {
		t.Errorf("Port7FFD: got %#02x, want %#02x", snap.Memory.Port7FFD, 0x02)
	}
}

// Restoring a snapshot is the machine being placed into a state, not a
// guest port write, so the $7FFD lock bit must not block it. A 128K
// title that has locked paging (bit 5) used to make every later
// restore a silent no-op: the map stayed exactly as the running game
// had left it.
func TestApplySnapshotPagesThroughALockedLatch(t *testing.T) {
	emu, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	// The guest locks paging with bank 1 at $C000.
	emu.mem.PageMemory(0x21)
	if emu.mem.PagingEnabled {
		t.Fatal("setup: expected paging to be locked by bit 5")
	}

	snap := snapshot.New()
	snap.Memory.Is128K = true
	snap.Memory.Port7FFD = 0x03 // bank 3, paging still unlocked

	if err := applySnapshotToEmulator(emu, snap); err != nil {
		t.Fatalf("applySnapshotToEmulator: %v", err)
	}
	got7FFD, _, _ := emu.mem.GetPortState()
	if got7FFD != 0x03 {
		t.Errorf("$7FFD after restore: got %#02x, want %#02x", got7FFD, 0x03)
	}
	if !emu.mem.PagingEnabled {
		t.Error("paging should be unlocked again: the restored latch has bit 5 clear")
	}
}
