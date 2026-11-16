package next

// NMIController is the Next's NMI arbiter (zxnext.vhd:2088-2170): the
// first-come-first-served source latches nmi_mf and nmi_divmmc, and the
// four-state FSM that holds them from the request, through the $0066 vector
// fetch, until the owner releases its hold.
//
// The expansion-bus source (nmi_expbus) is not modelled: there is no
// expansion bus, so nmi_assert_expbus is always 0.
//
// The FSM is clocked at the CPU clock in the FPGA. Here it steps at each
// opcode fetch, which is where its only address-sensitive transition (the
// $0066 fetch) is decided. The END state lasts until the current I/O write
// completes (:2143); no write is in flight at an opcode fetch, so END is
// passed through at once.
type NMIController struct {
	mf, divmmc bool
	state      nmiState
}

type nmiState byte

const (
	nmiIdle nmiState = iota
	nmiFetch
	nmiHold
)

// NMISource picks which latch a request targets.
type NMISource byte

const (
	NMISourceMF     NMISource = iota // M1 button, F9 hotkey, NR$02 bit 3
	NMISourceDivMMC                  // DRIVE button, F10 hotkey, NR$02 bit 2
)

// NMIInputs are the signals the arbiter reads.
type NMIInputs struct {
	MFEnable   bool // NR$06 bit 3, nr_06_button_m1_nmi_en
	DivEnable  bool // NR$06 bit 4, nr_06_button_drive_nmi_en
	E3Conmem   bool // port $E3 bit 7
	MFActive   bool // mf_is_active: the Multiface is paged in or holding
	MFHold     bool // mf_nmi_hold, multiface nmi_disable_o
	DivHold    bool // divmmc_nmi_hold, divmmc o_disable_nmi
	ConfigMode bool // nr_03_config_mode
}

// Request asserts a source (zxnext.vhd:2090-2112). It reports whether the
// source latched, which is when /NMI falls (:2168) and the owner's button
// pulse fires (:2169-2170).
func (n *NMIController) Request(src NMISource, in NMIInputs) bool {
	if in.ConfigMode {
		n.Reset()
		return false
	}
	if n.Activated() {
		return false
	}
	switch src {
	case NMISourceMF:
		if in.MFEnable && !in.E3Conmem && !in.DivHold {
			n.mf = true
		}
	case NMISourceDivMMC:
		if in.DivEnable && !in.MFActive {
			n.divmmc = true
		}
	}
	if !n.Activated() {
		return false
	}
	n.state = nmiFetch
	return true
}

// Fetch steps the FSM at an opcode fetch from pc.
func (n *NMIController) Fetch(pc uint16, in NMIInputs) {
	if in.ConfigMode {
		n.Reset()
		return
	}
	switch n.state {
	case nmiFetch:
		if pc == 0x0066 {
			n.state = nmiHold
		}
	case nmiHold:
		hold := (n.mf && in.MFHold) || (n.divmmc && in.DivHold)
		if !hold {
			n.Reset() // S_NMI_END clears the latches, then S_NMI_IDLE
		}
	}
}

// Activated is nmi_activated: some source holds the NMI.
func (n *NMIController) Activated() bool { return n.mf || n.divmmc }

// MF and DivMMC report the source latches.
func (n *NMIController) MF() bool     { return n.mf }
func (n *NMIController) DivMMC() bool { return n.divmmc }

// AcceptCause is nmi_accept_cause (zxnext.vhd:2164): the NR$02 read-back
// source bits may latch only in IDLE or FETCH.
func (n *NMIController) AcceptCause() bool { return n.state != nmiHold }

// Busy reports whether the FSM has anything to step.
func (n *NMIController) Busy() bool { return n.state != nmiIdle }

// Reset returns the arbiter to idle with both latches clear.
func (n *NMIController) Reset() { *n = NMIController{} }
