package next

import "fmt"

// im2SourceNames are the chain indices in priority order (nextreg.txt NR$C0).
var im2SourceNames = [IM2NumPeriph]string{
	"line", "uart0 rx", "uart1 rx",
	"ctc0", "ctc1", "ctc2", "ctc3", "ctc4", "ctc5", "ctc6", "ctc7",
	"ula", "uart0 tx", "uart1 tx",
}

var im2StateNames = map[im2DevState]string{
	im2S0: "idle", im2REQ: "requesting", im2ACK: "acknowledged", im2ISR: "in service",
}

// DebugLines describes the IM2 chain for a debugger: the mode, the vector it
// would place on the bus, the DMA delay pin, and one row per source. It reads
// only.
func (d *IM2Driver) DebugLines() []string {
	mode := "pulse (IM1-style)"
	if d.hwIM2 {
		mode = "hardware IM2"
	}
	// Read-only: DMAInt would refresh the cached enables, and this runs on
	// the debugger's goroutine. The enables shown are the last ones the
	// emulation side synced.
	dmaInt := d.chain.DMAInt(d.dmaEnabled)
	nmi := d.nmiActive != nil && d.nmiActive()
	out := []string{
		fmt.Sprintf("mode %s  vector base $%02X  /INT %v  bus vector $%02X",
			mode, d.vectorBase, !d.chain.intN, d.chain.VectorByte(d.vectorBase)),
		fmt.Sprintf("dma_int %v  NMI term enabled (NR$CC bit 7) %v  NMI active %v", dmaInt, d.dmaNMIEn, nmi),
		"idx source     state         en  dma  req  status",
	}
	for i := 0; i < IM2NumPeriph; i++ {
		p := d.chain.per[i]
		out = append(out, fmt.Sprintf("%2d  %-9s  %-12s  %s   %s    %s    %s",
			i, im2SourceNames[i], im2StateNames[p.state],
			yn(d.enabled[i]), yn(d.dmaEnabled[i]), yn(p.im2IntReq), yn(d.chain.status[i])))
	}
	return out
}

func yn(b bool) string {
	if b {
		return "y"
	}
	return "."
}

// NMIDebugLines describes the NMI arbiter and the NR$02 source bits.
func (r *ResetControl) NMIDebugLines() []string {
	state := [...]string{"idle", "fetch (waiting for $0066)", "hold"}[r.nmi.state]
	owner := "none"
	switch {
	case r.nmi.mf:
		owner = "multiface"
	case r.nmi.divmmc:
		owner = "divmmc"
	}
	out := []string{
		fmt.Sprintf("arbiter %s  owner %s  activated %v", state, owner, r.nmi.Activated()),
		fmt.Sprintf("NR$02 source bits: mf %v  divmmc %v  reset history %03b", r.nmiSource&0x08 != 0, r.nmiSource&0x04 != 0, r.resetType),
	}
	if r.nmiInputs != nil {
		in := r.nmiInputs()
		out = append(out, fmt.Sprintf("inputs: m1-en %v  drive-en %v  conmem %v  mf-active %v  divmmc-hold %v  config %v",
			in.MFEnable, in.DivEnable, in.E3Conmem, in.MFActive, in.DivHold, in.ConfigMode))
	}
	return out
}
