package multiface

// Core is a hardware-faithful model of the Spectrum Next Multiface, transcribed
// directly from the FPGA core's device/multiface.vhd (the authoritative oracle).
// It is the clocked NMI / invisible / mf_enable state machine: one Clock() call
// is one rising edge of the 28 MHz clock the module runs on.
//
// The module has three registered state bits (nmi_active, invisible, mf_enable)
// plus a one-cycle port-io edge delay (port_io_dly), and three combinational
// outputs (mf_enabled_o, nmi_disable_o, mf_port_en_o). Every equation below
// cites its multiface.vhd line. The replay test pkg/multiface fpga_golden_test.go
// drives this against vectors captured from the VHDL under GHDL.
//
// mf_mode_i encoding (multiface.vhd:67, decoded :112-116):
//
//	"00" -> mode_p3   (MF +3)
//	"11" -> mode_48   (MF 48)
//	else -> mode_128  (MF 128)
type Core struct {
	// configuration inputs (held between clocks)
	mode   byte // mf_mode_i, 2 bits
	enable bool // enable_i (port_multiface_io_en)

	// registered state (reset values per multiface.vhd:124-130,140,156,174)
	portIODly bool // port_io_dly  (:122-131)
	nmiActive bool // nmi_active   (:137-148)
	invisible bool // invisible    (:152-163)
	mfEnable  bool // mf_enable    (:171-184)

	// combinational outputs latched from the last Clock(), so the accessors are
	// stable to read after Clock() returns.
	outMFEnabled bool // mf_enabled_o  (:190 = mf_enable_eff)
	outNMIDis    bool // nmi_disable_o (:191 = nmi_active)
	outMFPortEn  bool // mf_port_en_o  (:195)
}

// Inputs is the per-clock CPU/bus input vector to the Multiface module — the
// non-configuration entity ports of multiface.vhd sampled at a rising edge.
type Inputs struct {
	A0066    bool // cpu_a_0066_i  (address == $0066)
	MReqn    bool // cpu_mreq_n_i
	M1n      bool // cpu_m1_n_i
	RetnSeen bool // cpu_retn_seen_i (one-cycle RETN-seen pulse)
	Button   bool // button_i (debounced red-button press request)

	// pre-decoded port strobes (zxnext.vhd:2730-2733): iord/iowr AND the
	// per-mode address match, already gated by the io-enable.
	PortEnableRd  bool
	PortEnableWr  bool
	PortDisableRd bool
	PortDisableWr bool
}

// NewCore returns a Core in the power-on reset state.
func NewCore() *Core {
	c := &Core{}
	c.Reset()
	return c
}

// SetMode sets mf_mode_i (low 2 bits used).
func (c *Core) SetMode(mode byte) { c.mode = mode & 0x3 }

// SetEnable sets enable_i (port_multiface_io_en). When low, the module is held
// in reset (multiface.vhd:103 reset <= reset_i or not enable_i).
func (c *Core) SetEnable(en bool) { c.enable = en }

// Reset forces the module's registers to their reset values (multiface.vhd
// :126,141,156,175) and recomputes the outputs.
func (c *Core) Reset() {
	c.portIODly = false
	c.nmiActive = false
	c.invisible = true // multiface.vhd:156 invisible <= '1'
	c.mfEnable = false
	c.recomputeOutputs(Inputs{M1n: true, MReqn: true})
}

// mode decode (multiface.vhd:105-118)
func (c *Core) modeP3() bool  { return c.mode == 0x0 }
func (c *Core) mode48() bool  { return c.mode == 0x3 }
func (c *Core) mode128() bool { return !c.modeP3() && !c.mode48() }

// Clock advances the module by one rising clock edge with the given input
// vector, then recomputes the combinational outputs (so the accessors reflect
// the post-edge state, exactly as the golden samples it).
func (c *Core) Clock(in Inputs) {
	// reset <= reset_i or not enable_i  (multiface.vhd:103). reset_i is only
	// pulsed by Reset(); here the live reset is "module disabled".
	reset := !c.enable

	// snapshot the pre-edge port_io_dly: the nmi_active/invisible edge guards
	// use the OLD port_io_dly value (it is a separate register updated by the
	// same edge). multiface.vhd:144,159 read port_io_dly, :128 updates it.
	portIODlyOld := c.portIODly

	// button_pulse <= button_i and not nmi_active  (multiface.vhd:135)
	buttonPulse := in.Button && !c.nmiActive

	// fetch_66 (combinational, multiface.vhd:169)
	fetch66 := in.A0066 && !in.M1n && c.nmiActive

	if reset {
		// all DUT registers take their reset value (multiface.vhd:126,141,156,175)
		c.portIODly = false
		c.nmiActive = false
		c.invisible = true
		c.mfEnable = false
		c.recomputeOutputs(in)
		return
	}

	// --- port_io_dly register (multiface.vhd:122-131) ---
	newPortIODly := in.PortEnableRd || in.PortEnableWr || in.PortDisableRd || in.PortDisableWr

	// --- nmi_active register (multiface.vhd:137-148) ---
	newNMIActive := c.nmiActive
	switch {
	case buttonPulse:
		newNMIActive = true
	case in.RetnSeen ||
		((in.PortEnableWr || in.PortDisableWr ||
			(in.PortDisableRd && c.modeP3())) && !portIODlyOld):
		newNMIActive = false
	}

	// --- invisible register (multiface.vhd:152-163) ---
	newInvisible := c.invisible
	switch {
	case buttonPulse:
		newInvisible = false
	case ((in.PortDisableWr && !c.modeP3()) ||
		(in.PortEnableWr && c.modeP3())) && !portIODlyOld:
		newInvisible = true
	}

	// --- mf_enable register (multiface.vhd:171-184) ---
	// invisible_eff uses the CURRENT (pre-edge) invisible (multiface.vhd:165,181)
	invisibleEff := c.invisible && !c.mode48()
	newMFEnable := c.mfEnable
	switch {
	case fetch66 && !in.MReqn:
		newMFEnable = true
	case in.PortDisableRd || in.RetnSeen:
		newMFEnable = false
	case in.PortEnableRd:
		newMFEnable = !invisibleEff
	}

	// commit registers
	c.portIODly = newPortIODly
	c.nmiActive = newNMIActive
	c.invisible = newInvisible
	c.mfEnable = newMFEnable

	c.recomputeOutputs(in)
}

// recomputeOutputs evaluates the three combinational outputs from the current
// register state and the live inputs (multiface.vhd:186,190-191,195).
func (c *Core) recomputeOutputs(in Inputs) {
	invisibleEff := c.invisible && !c.mode48()    // :165
	fetch66 := in.A0066 && !in.M1n && c.nmiActive // :169

	// mf_enable_eff <= mf_enable or fetch_66  (multiface.vhd:186)
	c.outMFEnabled = c.mfEnable || fetch66
	// nmi_disable_o <= nmi_active  (multiface.vhd:191)
	c.outNMIDis = c.nmiActive
	// mf_port_en_o (multiface.vhd:195)
	c.outMFPortEn = in.PortEnableRd && !invisibleEff && (c.mode128() || c.modeP3())
}

// MFEnabled reports mf_enabled_o — the MF ROM/RAM is paged in.
func (c *Core) MFEnabled() bool { return c.outMFEnabled }

// NMIDisable reports nmi_disable_o — the NMI is held and further button
// activations are blocked (1 = NMI asserted/held).
func (c *Core) NMIDisable() bool { return c.outNMIDis }

// MFPortEn reports mf_port_en_o — the MF "port read returns the paging shadow
// register" enable (MF 128 / MF +3 only, when not invisible).
func (c *Core) MFPortEn() bool { return c.outMFPortEn }

// NMIActive / Invisible / MFEnableLatch expose the registered state for tests.
func (c *Core) NMIActive() bool     { return c.nmiActive }
func (c *Core) Invisible() bool     { return c.invisible }
func (c *Core) MFEnableLatch() bool { return c.mfEnable }
