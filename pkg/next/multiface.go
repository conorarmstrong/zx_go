package next

import (
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/multiface"
)

// Multiface is the Next's Multiface: the FPGA state machine in
// pkg/multiface (device/multiface.vhd) fed with the events the emulator
// sees, its mf_enabled_o driving the memory's overlay.
//
// Each event is one clock with the input asserted, then an idle clock: an
// I/O cycle holds its strobe for several clocks, but port_io_dly limits the
// edge-triggered terms to the first and the level terms repeat the same
// result. port_multiface_io_en (NR$83 bit 1) is not modelled; the ports are
// always decoded, as for the rest of the Next's I/O.
type Multiface struct {
	core *multiface.Core
	mem  *memory.Memory
	mode func() byte // nr_0a_mf_type, NR$0A bits 7:6
}

// NewMultiface returns a Multiface in its reset state. mode reports NR$0A.
func NewMultiface(mem *memory.Memory, mode func() byte) *Multiface {
	c := multiface.NewCore()
	c.SetEnable(true)
	m := &Multiface{core: c, mem: mem, mode: mode}
	m.sync()
	return m
}

func (m *Multiface) clock(in multiface.Inputs) {
	m.core.SetMode(m.mode())
	m.core.Clock(in)
}

func (m *Multiface) idle() { m.clock(multiface.Inputs{M1n: true, MReqn: true}) }

func (m *Multiface) sync() { m.mem.SetMultifaceActive(m.core.MFEnabled()) }

// Button is nmi_mf_button: the arbiter latched a Multiface NMI.
func (m *Multiface) Button() {
	m.clock(multiface.Inputs{Button: true, M1n: true, MReqn: true})
	m.idle()
	m.sync()
}

// Fetch is an opcode fetch at pc. The fetch of $0066 with the NMI held pages
// the memory in, for that fetch already (mf_enable_eff, multiface.vhd:186).
func (m *Multiface) Fetch(pc uint16) {
	if pc != 0x0066 || !m.core.NMIActive() {
		return
	}
	m.clock(multiface.Inputs{A0066: true, M1n: false, MReqn: false})
	m.idle()
	m.sync()
}

// Port applies an IN (write false) or OUT at addr. It reports mf_port_en_o:
// an enable-port read that the Multiface answers with a paging register.
func (m *Multiface) Port(addr uint16, write bool) bool {
	en, dis := byte(0x3F), byte(0xBF) // zxnext.vhd:2612-2613
	switch t := m.mode(); {
	case t&2 != 0:
		en, dis = 0x9F, 0x1F
	case t&1 != 0:
		en, dis = 0xBF, 0x3F
	}
	lo := byte(addr)
	if lo != en && lo != dis {
		return false
	}
	in := multiface.Inputs{M1n: true, MReqn: true}
	switch {
	case lo == en && write:
		in.PortEnableWr = true
	case lo == en:
		in.PortEnableRd = true
	case write:
		in.PortDisableWr = true
	default:
		in.PortDisableRd = true
	}
	m.clock(in)
	answers := m.core.MFPortEn()
	m.idle()
	m.sync()
	return answers
}

// MultifaceActive is mf_is_active (zxnext.vhd:4305): memory paged in or the
// NMI held. RETNHook uses it to keep the RETN that ends a session away from
// the divMMC (:4111).
func (m *Multiface) MultifaceActive() bool { return m.core.MFEnabled() || m.core.NMIDisable() }

// Hold is nmi_disable_o, the Multiface's NMI hold.
func (m *Multiface) Hold() bool { return m.core.NMIDisable() }

// SetMultifaceActive(false) is the RETN input (multiface.vhd:144,179);
// true is the button.
func (m *Multiface) SetMultifaceActive(on bool) {
	if on {
		m.Button()
		return
	}
	m.clock(multiface.Inputs{RetnSeen: true, M1n: true, MReqn: true})
	m.idle()
	m.sync()
}

// Reset is the core reset.
func (m *Multiface) Reset() {
	m.core.Reset()
	m.sync()
}
