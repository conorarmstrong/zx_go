package ula

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/betadisk"
	"github.com/conorarmstrong/zx_go/pkg/keyboard"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The ULA routes the Beta ports to the FDC while the TR-DOS ROM is paged
// in; with the ROM unpaged the interface stays silent and $1F (Kempston)
// and friends fall through unchanged.
func TestULARoutesBetaPortsWhenActive(t *testing.T) {
	dir := "test_roms_beta"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	mem.EnableBeta(make([]byte, memory.PageSize))
	u := New(mem, keyboard.New())

	bd := betadisk.NewInterface()
	bd.FDC.Mount(0, betadisk.NewImage(80, 2))
	u.SetBetaDisk(bd)

	// Diskless + inactive: silent passthrough (Kempston $1F untouched).
	bare := betadisk.NewInterface()
	u2 := New(mem, keyboard.New())
	u2.SetBetaDisk(bare)
	mem.SetBetaActive(false)
	if _, handled := u2.ReadPort(0x001F); handled {
		t.Error("$1F read claimed by a diskless Beta while ROM not paged in")
	}

	// Active: writes reach the FDC (Restore raises INTRQ visible on
	// $FF bit 7), and the FDC ports are claimed.
	mem.SetBetaActive(true)
	u.WritePort(0x001F, 0x00) // Restore
	st, handled := u.ReadPort(0x00FF)
	if !handled {
		t.Fatal("$FF read not claimed by Beta while active")
	}
	if st&0x80 == 0 {
		t.Error("INTRQ (bit 7) not set after Restore routed through the ULA")
	}
}

// A full sector read flows through the ULA's port dispatch end-to-end.
func TestULABetaSectorReadEndToEnd(t *testing.T) {
	dir := "test_roms_beta2"
	createTestROMs(t, dir)
	defer cleanupTestROMs(dir)
	mem, err := memory.New(dir, roms.Model48K)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	mem.EnableBeta(make([]byte, memory.PageSize))
	mem.SetBetaActive(true)
	u := New(mem, keyboard.New())

	img := betadisk.NewImage(80, 2)
	want := make([]byte, betadisk.SectorSize)
	for i := range want {
		want[i] = byte(i ^ 0x33)
	}
	if err := img.WriteSector(0, 0, 1, want); err != nil {
		t.Fatal(err)
	}
	bd := betadisk.NewInterface()
	bd.FDC.Mount(0, img)
	u.SetBetaDisk(bd)

	u.WritePort(0x00FF, 0x10|0x04) // drive 0, side 0, no reset
	// RESTORE with head load FIRST: on the Beta (WD_FLAG_BETA128) Fuse's
	// disk_ready IS fdd_head_loaded (wd_fdc.c:198-201), and Type II/III
	// gate on it before spinning (cr_write :1004) — a cold READ SECTOR
	// with the head parked is rejected with sticky RNF on Fuse too.
	// TR-DOS always RESTOREs before reading.
	u.WritePort(0x001F, 0x18) // RESTORE H
	u.ReadPort(0x001F)        // consume the restore completion INTRQ
	u.WritePort(0x005F, 1)    // sector 1
	u.WritePort(0x001F, 0x80) // Read Sector
	for i := range want {
		got, _ := u.ReadPort(0x007F)
		if got != want[i] {
			t.Fatalf("byte %d via ULA ports = %02X, want %02X", i, got, want[i])
		}
	}
}
