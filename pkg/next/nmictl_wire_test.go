package next

import "testing"

// With NR$06 bits 3 and 4 clear neither source fires, but the NR$02 read-back
// bits still latch: they follow nmi_accept_cause, not the arbiter
// (zxnext.vhd:3845, :3858).
func TestNR02NMINeedsTheNR06EnableButStillLatchesTheSourceBit(t *testing.T) {
	for _, bit := range []byte{0x08, 0x04} {
		d, cpu, _, rc := nmiRig(t)
		d.Store(0x06, 0x00)
		cpu.PendingNMI.Store(false)
		d.WriteReg(0x02, bit)
		if cpu.PendingNMI.Load() || rc.NMIActivated() {
			t.Errorf("NR$02=$%02X fired with NR$06 clear", bit)
		}
		if got := d.ReadReg(0x02) & bit; got != bit {
			t.Errorf("NR$02=$%02X: source bit not latched", bit)
		}
	}
}

// A Multiface NMI holds until the Multiface pages out after the $0066 fetch; a
// second request in that time is ignored and does not latch its source bit.
func TestNR02MultifaceNMIHoldsUntilTheMultifacePagesOut(t *testing.T) {
	d, cpu, mem, rc := nmiRig(t)
	d.WriteReg(0x02, 0x08)
	if !mem.MultifaceActive() || !rc.NMIActivated() {
		t.Fatal("MF NMI did not page the Multiface in")
	}
	rc.nmi.Fetch(0x0066, rc.nmiInputs()) // the vector fetch
	cpu.PendingNMI.Store(false)
	d.WriteReg(0x02, 0x00)
	d.WriteReg(0x02, 0x04) // divMMC request while the MF holds
	if cpu.PendingNMI.Load() {
		t.Error("a second NMI fired while the first was held")
	}
	if d.ReadReg(0x02)&0x04 != 0 {
		t.Error("source bit latched in HOLD")
	}
	mem.SetMultifaceActive(false) // RETN
	rc.nmi.Fetch(0x8000, rc.nmiInputs())
	if rc.NMIActivated() {
		t.Fatal("NMI still active after the Multiface paged out")
	}
	d.WriteReg(0x02, 0x04)
	if !cpu.PendingNMI.Load() {
		t.Error("a new NMI did not fire after release")
	}
}

// A soft reset clears the arbiter (zxnext.vhd:2097-2100).
func TestNR02ResetClearsTheNMIArbiter(t *testing.T) {
	d, _, _, rc := nmiRig(t)
	d.WriteReg(0x02, 0x04)
	d.WriteReg(0x02, 0x01) // soft reset
	if rc.NMIActivated() {
		t.Error("soft reset left the NMI active")
	}
}

// The DRIVE hotkey needs the divMMC ports (NR$83 bit 0, zxnext.vhd:6349); the
// M1 hotkey does not.
func TestNMIHotkeys(t *testing.T) {
	d, cpu, _, rc := nmiRig(t)
	d.Store(0x83, 0x00)
	rc.Hotkey(true)
	if cpu.PendingNMI.Load() {
		t.Error("DRIVE hotkey fired with the divMMC ports off")
	}
	d.Store(0x83, 0x01)
	rc.Hotkey(true)
	if !cpu.PendingNMI.Load() || !rc.nmi.DivMMC() {
		t.Error("DRIVE hotkey did not fire the divMMC NMI")
	}
	d2, cpu2, _, rc2 := nmiRig(t)
	d2.Store(0x83, 0x00)
	rc2.Hotkey(false)
	if !cpu2.PendingNMI.Load() || !rc2.nmi.MF() {
		t.Error("M1 hotkey did not fire the Multiface NMI")
	}
}

func TestNMIArbiterStateRoundTrip(t *testing.T) {
	d, _, _, rc := nmiRig(t)
	d.WriteReg(0x02, 0x04)
	rc.nmi.Fetch(0x0066, rc.nmiInputs())
	_, _, _, rc2 := nmiRig(t)
	if err := rc2.LoadState(rc.SaveState()); err != nil {
		t.Fatal(err)
	}
	if rc2.nmi != rc.nmi {
		t.Errorf("restored arbiter %+v, want %+v", rc2.nmi, rc.nmi)
	}
}

// dma_delay_i's NMI term: nmi_activated and NR$CC bit 7 (zxnext.vhd:2007).
func TestDMADelayNMITerm(t *testing.T) {
	for _, c := range []struct {
		cc, active, want bool
	}{{true, true, true}, {true, false, false}, {false, true, false}} {
		d := NewIM2Driver()
		active := c.active
		d.SetNMIActivated(func() bool { return active })
		d.SetDMANMIEnabled(c.cc)
		if got := d.DMADelay(); got != c.want {
			t.Errorf("CC.7=%v activated=%v: delay %v, want %v", c.cc, c.active, got, c.want)
		}
	}
}
