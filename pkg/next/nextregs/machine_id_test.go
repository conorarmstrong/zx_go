package nextregs

import "testing"

// NR$00 must publish the FPGA's machine id. zxnext_top_issue2.vhd:35:
// `g_machine_id : unsigned(7 downto 0) := X"0A"` ("ZX Spectrum Next");
// zxnext.vhd:5885 returns it verbatim on the NR$00 read. Publishing
// $08 ("Emulators") made NextZXOS branch into its emulator-compat path
// at ROM1 $1E69 (`cp $08`) right after the FW handoff reset, skipping
// the core-version check and ultimately driving the +3DOS re-boot
// retry loop ($1B04) and the Browser-ENTER welcome loop — the
// reference emulator (id $0A path) boots the same card straight to the Browser
// (the development log).
func TestMachineID_MatchesFPGA(t *testing.T) {
	d := New()
	if got := d.ReadReg(0x00); got != 0x0A {
		t.Fatalf("NR$00 = $%02X, want $0A (zxnext_top_issue2.vhd:35)", got)
	}
}
