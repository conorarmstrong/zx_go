package if1

// IF1 port decode. From fuse-1.6.0/peripherals/if1.c:220-225 the IF1
// ULA decodes the port address by `port & 0x0018`:
//
//	0x0000 → MDR data port      (canonical address: 0xE7)
//	0x0008 → CTR control port   (canonical address: 0xEF)
//	0x0010 → NET RS-232/network (canonical address: 0xF7)
//
// The high bits of the port address are not decoded — any port whose
// low 5 bits match the pattern triggers the IF1. We mirror that
// behaviour so software that uses non-canonical addresses still works.
const (
	// PortMDR is the canonical microdrive data port. The IF1 ULA
	// also decodes any address whose low 5 bits match
	// (addr & 0x18) == 0x00.
	PortMDR uint16 = 0xE7
	// PortCTR is the canonical control / status port —
	// (addr & 0x18) == 0x08.
	PortCTR uint16 = 0xEF
	// PortNET is the canonical RS-232 / SinclairNET port —
	// (addr & 0x18) == 0x10.
	PortNET uint16 = 0xF7
)

const (
	ifPortMaskMDR = 0x0018
	ifPortValMDR  = 0x0000
	ifPortValCTR  = 0x0008
	ifPortValNET  = 0x0010
)

// portClass identifies which of the IF1's three port functions a
// given I/O address is targeting. PortUnknown means the address
// doesn't match any IF1 port at all.
type portClass int

const (
	portUnknown portClass = iota
	portMDR
	portCTR
	portNET
)

// decodePort returns the port class for an I/O address.
func decodePort(port uint16) portClass {
	switch port & ifPortMaskMDR {
	case ifPortValMDR:
		return portMDR
	case ifPortValCTR:
		return portCTR
	case ifPortValNET:
		return portNET
	}
	return portUnknown
}

// ULA is the Interface 1 ULA's I/O port handler. It owns the
// MicrodriveBus and the small set of bus-wide control bits the IF1
// ROM polls (DTR, busy).
//
// The microdrive — the IF1's primary function — is fully modelled. RS-232
// and SinclairNET are stubbed as "no peripheral connected": they bit-bang
// an external serial device, which (like the ESP UART, task #33) is out of
// scope for a reference emulator; the ROM sees no signal and proceeds. The
// CTR WAT bit (assert CPU WAIT to pace microdrive/serial byte I/O) is
// deliberately not modelled — our microdrive timing is emulator-driven, so
// no software needs the CPU stall for correctness.
type ULA struct {
	Bus *MicrodriveBus

	// commsClkHigh latches the previous level of the comms clock
	// (CTR port bit 1). The motor-rotation step fires on the
	// falling edge (clock high → low with comms-data low) — see
	// writeCTR — so we need the previous value to detect the
	// transition.
	commsClkHigh bool
}

// NewULA constructs a fresh IF1 ULA wrapping the supplied microdrive
// bus. If bus is nil a fresh one is allocated.
func NewULA(bus *MicrodriveBus) *ULA {
	if bus == nil {
		bus = NewMicrodriveBus()
	}
	return &ULA{Bus: bus}
}

// Reset returns the ULA to power-on state. The microdrive bus is
// reset too — see Bus.Reset for what that clears.
func (u *ULA) Reset() {
	u.commsClkHigh = false
	u.Bus.Reset()
}

// HandlePortRead processes an IN instruction targeting an IF1 port.
// Returns (value, true) if the address decodes to an IF1 port,
// (0xFF, false) otherwise — the caller should fall through to the
// next peripheral or the floating bus.
func (u *ULA) HandlePortRead(port uint16) (byte, bool) {
	switch decodePort(port) {
	case portMDR:
		val := u.Bus.ReadDataPort()
		return val, true
	case portCTR:
		val := u.readCTR()
		// FUSE calls microdrives_restart after every CTR access.
		u.Bus.RestartAll()
		return val, true
	case portNET:
		val := u.readNET()
		u.Bus.RestartAll()
		return val, true
	}
	return 0xFF, false
}

// HandlePortWrite processes an OUT instruction targeting an IF1 port.
// Returns true if the address decoded to an IF1 port (so the caller
// stops dispatching), false otherwise.
func (u *ULA) HandlePortWrite(port uint16, val byte) bool {
	switch decodePort(port) {
	case portMDR:
		u.Bus.WriteDataPort(val)
		return true
	case portCTR:
		u.writeCTR(val)
		u.Bus.RestartAll()
		return true
	case portNET:
		u.writeNET(val)
		u.Bus.RestartAll()
		return true
	}
	return false
}

// readCTR returns the IF1 CTR (control) port read value. From the
// FUSE comment at if1.c:114-117 the layout is:
//
//	bit 7..5 — unused, returns 1
//	bit 4    — BUSY (SinclairNET busy flag, always idle for us)
//	bit 3    — DTR  (RS-232 data terminal ready, always 0 for us)
//	bit 2    — GAP  (low during the GAP portion of a formatted block)
//	bit 1    — SYNC (low during the SYNC portion of a formatted block)
//	bit 0    — WPR  (write-protect, low when protected)
//
// The drive bits come from the AND-merge in MicrodriveBus.ReadStatusPort.
// We OR in the bus-wide bits afterward.
func (u *ULA) readCTR() byte {
	ret := u.Bus.ReadStatusPort()
	// Stubbed peripherals: DTR low (no serial connected), BUSY low.
	// FUSE clears these bits explicitly at if1.c:641-647 even when
	// serial is unconnected, so we match that behaviour.
	ret &^= 0x08 // DTR off → bit 3 low
	ret &^= 0x10 // BUSY off → bit 4 low
	return ret
}

// readNET returns the IF1 NET port read value. From if1.c:122-124:
//
//	bit 7    — TX  (RS-232 transmit clock)
//	bit 6..1 — unused
//	bit 0    — NET (SinclairNET data wire)
//
// TX and NET are read straight off their wires (not inverted), and
// both rest at the ~0V floating level when nothing is driving them —
// see the IF1 service manual schematic notes on the RX DATA/NET
// transistors. With no peripherals plugged in, both bits therefore
// read low; the IF1 ROM's serial polling sees "no signal" and times
// out.
func (u *ULA) readNET() byte {
	return 0x7E
}

// writeCTR processes a write to the CTR (control) port. From the
// FUSE comment at if1.c:114-117:
//
//	bit 5 — WAT  (assert WAIT to the CPU; not modelled)
//	bit 4 — CTS  (RS-232 clear-to-send to the connected device)
//	bit 3 — ERA  (erase head; ignored — we don't model magnetic erase)
//	bit 2 — R/W  (read/write; the data direction is implicit in MDR
//	             port reads vs writes for us, so we don't act on it)
//	bit 1 — CLK  (comms clock — falling edge rotates the motor select)
//	bit 0 — DTA  (comms data — the bit shifted into the daisy chain
//	             on the next falling clock edge)
//
// We only act on bits 0 and 1 — the motor-select sequence. WAT, CTS,
// and ERA matter only when serial peripherals are plugged in, which
// we don't support. Mirrors the gate in port_ctr_out at
// fuse-1.6.0/peripherals/if1.c:842-920.
func (u *ULA) writeCTR(val byte) {
	clkHigh := val&0x02 != 0
	dtaLow := val&0x01 == 0

	// Falling edge of comms clock → "select next drive". The new
	// drive 0 motor-on bit is the inverse of the data bit (active
	// low), exactly as port_ctr_out does at if1.c:846-852.
	if !clkHigh && u.commsClkHigh {
		u.Bus.AdvanceMotor(dtaLow)
	}
	u.commsClkHigh = clkHigh
}

// writeNET processes a write to the NET (RS-232 / SinclairNET) port.
// We don't have any serial peripherals plugged in, so this is a
// no-op. The IF1 ROM uses this port to bit-bang RS-232 frames; with
// nothing connected the writes simply go nowhere, which matches a
// real Spectrum with an unplugged Interface 1 RS-232 socket.
func (u *ULA) writeNET(_ byte) {
}
