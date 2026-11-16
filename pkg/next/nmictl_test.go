package next

import "testing"

// nmiIn is an idle input set: both NR$06 enables on, no holds.
func nmiIn() NMIInputs { return NMIInputs{MFEnable: true, DivEnable: true} }

// A Multiface request latches nmi_mf, activates, and pulses the MF button once.
func TestNMIControllerMultifaceRequest(t *testing.T) {
	var n NMIController
	if !n.Request(NMISourceMF, nmiIn()) {
		t.Fatal("MF request did not fire")
	}
	if !n.Activated() || !n.MF() || n.DivMMC() {
		t.Fatalf("latches mf=%v div=%v activated=%v", n.MF(), n.DivMMC(), n.Activated())
	}
}

// NR$06 bit 3 / bit 4 gate the request (zxnext.vhd:2090-2091).
func TestNMIControllerNeedsTheNR06Enable(t *testing.T) {
	var n NMIController
	in := nmiIn()
	in.MFEnable = false
	if n.Request(NMISourceMF, in) || n.Activated() {
		t.Error("MF request fired with NR$06 bit 3 clear")
	}
	in = nmiIn()
	in.DivEnable = false
	if n.Request(NMISourceDivMMC, in) || n.Activated() {
		t.Error("divMMC request fired with NR$06 bit 4 clear")
	}
}

// The MF request is refused while port $E3 bit 7 (conmem) is set or the divMMC
// holds the NMI; the divMMC request is refused while the Multiface is active
// (zxnext.vhd:2107-2110).
func TestNMIControllerCrossBlocking(t *testing.T) {
	cases := []struct {
		name string
		src  NMISource
		mod  func(*NMIInputs)
	}{
		{"mf/conmem", NMISourceMF, func(i *NMIInputs) { i.E3Conmem = true }},
		{"mf/divhold", NMISourceMF, func(i *NMIInputs) { i.DivHold = true }},
		{"div/mfactive", NMISourceDivMMC, func(i *NMIInputs) { i.MFActive = true }},
	}
	for _, c := range cases {
		var n NMIController
		in := nmiIn()
		c.mod(&in)
		if n.Request(c.src, in) || n.Activated() {
			t.Errorf("%s: request fired", c.name)
		}
	}
}

// First come, first served: a second request while one is active is ignored.
func TestNMIControllerFirstComeFirstServed(t *testing.T) {
	var n NMIController
	n.Request(NMISourceDivMMC, nmiIn())
	if n.Request(NMISourceMF, nmiIn()) {
		t.Error("second request fired while the first was active")
	}
	if n.MF() || !n.DivMMC() {
		t.Errorf("latches mf=%v div=%v, want divMMC only", n.MF(), n.DivMMC())
	}
}

// The FSM: IDLE -> FETCH on activation, FETCH -> HOLD on the $0066 opcode fetch,
// HOLD while the owner's hold is up, then END clears the latches and returns
// to IDLE (zxnext.vhd:2120-2160).
func TestNMIControllerLifecycle(t *testing.T) {
	var n NMIController
	in := nmiIn()
	n.Request(NMISourceMF, in)
	in.MFHold = true
	n.Fetch(0x1234, in) // not the vector: stays FETCH
	if !n.AcceptCause() {
		t.Error("cause not accepted in FETCH")
	}
	n.Fetch(0x0066, in) // vector fetch: HOLD
	if n.AcceptCause() {
		t.Error("cause still accepted in HOLD")
	}
	n.Fetch(0x0067, in) // hold still up
	if !n.Activated() {
		t.Fatal("released while the MF still holds")
	}
	in.MFHold = false // RETN paged the MF out
	n.Fetch(0x8000, in)
	if n.Activated() || !n.AcceptCause() {
		t.Errorf("after the hold dropped: activated=%v accept=%v", n.Activated(), n.AcceptCause())
	}
	if !n.Request(NMISourceMF, nmiIn()) {
		t.Error("a new request after release did not fire")
	}
}

// nmi_hold follows the owner: the divMMC hold does not keep an MF NMI held.
func TestNMIControllerHoldFollowsTheOwner(t *testing.T) {
	var n NMIController
	in := nmiIn()
	n.Request(NMISourceMF, in)
	n.Fetch(0x0066, in)
	in.DivHold = true // not the owner's hold
	n.Fetch(0x0067, in)
	if n.Activated() {
		t.Error("divMMC hold kept a Multiface NMI held")
	}
}

// Config mode clears everything (zxnext.vhd:2102, :2157).
func TestNMIControllerConfigModeClears(t *testing.T) {
	var n NMIController
	in := nmiIn()
	n.Request(NMISourceMF, in)
	in.ConfigMode = true
	n.Fetch(0x1000, in)
	if n.Activated() {
		t.Error("config mode did not clear the NMI")
	}
	if n.Request(NMISourceMF, in) {
		t.Error("request fired in config mode")
	}
}

func TestNMIControllerReset(t *testing.T) {
	var n NMIController
	n.Request(NMISourceDivMMC, nmiIn())
	n.Reset()
	if n.Activated() || !n.AcceptCause() {
		t.Error("reset left the NMI active")
	}
}
