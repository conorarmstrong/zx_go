package plus3fdc

import "testing"

// Reaching the end of the cylinder is an ABNORMAL termination. The host is
// expected to end a transfer by asserting Terminal Count; if the FDC instead
// runs past the last sector it stops of its own accord and says so, with
// ST0's interrupt code set to 01 alongside ST1.EN.
//
// Comando Quatro's loader proves it. Its read routine checks the three status
// bytes literally:
//
//	FDCC  LD A,($FECA) / CP $40 / JR NZ,fail   ; ST0 must be 0x40
//	FDD3  LD A,($FECB) / CP $80 / JR NZ,fail   ; ST1 must be 0x80
//	FDDA  LD A,($FECC) / AND A  / JR NZ,fail   ; ST2 must be 0x00
//
// Reporting a normal termination (ST0 = 0x00) failed that check, so the
// loader retried three times, gave up after reading one sector of the 40766
// bytes it wanted, and jumped into memory it had never filled.
func TestEndOfCylinderIsAnAbnormalTermination(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x46) // READ DATA
	f.WriteData(0x00)
	f.WriteData(0x00) // C
	f.WriteData(0x00) // H
	f.WriteData(0x01) // R
	f.WriteData(0x02) // N
	f.WriteData(0x01) // EOT = R: the transfer ends at the last sector
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	res := drainResult(t, f, 7)

	if res[1] != 0x80 {
		t.Errorf("ST1 = %02X, want 80 (EN alone)", res[1])
	}
	if res[2] != 0x00 {
		t.Errorf("ST2 = %02X, want 00", res[2])
	}
	if got := res[0] & 0xC0; got != 0x40 {
		t.Errorf("ST0 = %02X, interrupt code %02X — want 40, an abnormal termination", res[0], got)
	}
}

// A transfer that stops before the end of the cylinder is still normal.
func TestShortOfEndOfCylinderStaysNormal(t *testing.T) {
	f := NewUPD765()
	f.AttachDisk(0, loadTestDisk(t))

	f.WriteData(0x46)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x00)
	f.WriteData(0x01) // R = 1
	f.WriteData(0x02)
	f.WriteData(0x03) // EOT = 3, so sector 1 is not the end
	f.WriteData(0x2A)
	f.WriteData(0xFF)
	for i := 0; i < 512; i++ {
		f.ReadData()
	}
	if f.phase != phaseExecution {
		t.Fatal("transfer stopped at sector 1 despite EOT=3")
	}
}
