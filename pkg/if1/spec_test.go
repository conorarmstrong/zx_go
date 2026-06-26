package if1

// Spec-faithful test suite for the ZX Interface 1 ports, control/status
// bits, motor-on/off + drive-select shift register, and the IF1 ROM
// paging behaviour.
//
// Oracle (no FPGA — documented IF1/Microdrive hardware behaviour):
//
//   - Sinclair ZX Interface 1, Interface 2 & Microdrive Service Manual
//     (the canonical EF control/status port bit functions).
//   - World of Spectrum "Hardware Ports" reference (ports.htm) and the
//     Sinclair ZX Wiki "ZX Interface 1" page (port decode + bit names):
//     control port 0xEF carries COMMS CLK / COMMS DATA / R/W / ERASE,
//     status port 0xEF reports GAP / SYNC / WRITE-PROTECT / DTR / BUSY;
//     data port 0xE7; network/RS-232 port 0xF7.
//   - The IF1 internal byte-level encoding used here is the established
//     reference encoding our implementation adopts, so the IF1 ROM and .mdr
//     cartridges interoperate. The status-byte bit ORDER follows that internal
//     encoding (WP=bit0, SYNC=bit1, GAP=bit2, DTR=bit3, BUSY=bit4) which is
//     what the ROM reads back;
//     the tests assert the encoding the ROM actually consumes.

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/microdrive"
)

// ---------------------------------------------------------------------------
// Port decode: $E7 (data), $EF (control/status), $F7 (network)
// ---------------------------------------------------------------------------

// TestPortDecodeCanonicalAddresses pins the three canonical IF1 port
// addresses to their functions. From the Sinclair ZX Wiki port table:
// 0xE7 = microdrive data, 0xEF = status/control, 0xF7 = TX/Net.
func TestPortDecodeCanonicalAddresses(t *testing.T) {
	if decodePort(0xE7) != portMDR {
		t.Errorf("0xE7 should decode as the microdrive data port")
	}
	if decodePort(0xEF) != portCTR {
		t.Errorf("0xEF should decode as the control/status port")
	}
	if decodePort(0xF7) != portNET {
		t.Errorf("0xF7 should decode as the network/RS-232 port")
	}
	// The exported canonical constants must agree with the decoder.
	if decodePort(PortMDR) != portMDR || decodePort(PortCTR) != portCTR || decodePort(PortNET) != portNET {
		t.Errorf("exported Port* constants disagree with decodePort")
	}
}

// TestPortDecodePartialDecoding verifies the IF1 only decodes address
// bits 3 and 4 (mask 0x18). The real IF1 ULA does not decode the high
// address bits, so any address sharing the low-bit pattern hits the
// same function. This is why the canonical addresses are E7/EF/F7 but
// software using other addresses with the same low bits still works.
func TestPortDecodePartialDecoding(t *testing.T) {
	cases := []struct {
		port uint16
		want portClass
	}{
		{0x0000, portMDR},     // bits 3,4 = 00 → data
		{0x0008, portCTR},     // bit 3 = 1   → control
		{0x0010, portNET},     // bit 4 = 1   → network
		{0x0018, portUnknown}, // bits 3,4 = 11 → neither
		// High bits don't matter — only (port & 0x18) is decoded.
		{0xFFE7, portMDR},
		{0xFFEF, portCTR},
		{0xFFF7, portNET},
		// The ULA keyboard port 0xFE must NOT be claimed by the IF1
		// (0xFE & 0x18 == 0x18 → unknown).
		{0x00FE, portUnknown},
	}
	for _, tc := range cases {
		if got := decodePort(tc.port); got != tc.want {
			t.Errorf("decodePort(%04X) = %d, want %d", tc.port, got, tc.want)
		}
	}
}

// TestDataPortDoesNotClaimControlPort is a guard against a decode
// regression that would route data-port traffic to the control port
// (or vice-versa): they must stay disjoint.
func TestDataPortDoesNotClaimControlPort(t *testing.T) {
	if decodePort(PortMDR) == decodePort(PortCTR) {
		t.Fatal("MDR and CTR ports decode to the same class")
	}
	if decodePort(PortCTR) == decodePort(PortNET) {
		t.Fatal("CTR and NET ports decode to the same class")
	}
}

// ---------------------------------------------------------------------------
// Control port ($EF) write bits: COMMS CLK, COMMS DATA, R/W, ERASE
// ---------------------------------------------------------------------------

// engageDrive0 pulses the COMMS CLK/DATA lines so drive 0's motor turns
// on, matching the IF1 ROM's bit-bang select sequence: clock high+data
// high, then a falling clock edge with data low.
func engageDrive0(u *ULA) {
	u.HandlePortWrite(PortCTR, 0x03) // CLK high (bit1), DATA high (bit0)
	u.HandlePortWrite(PortCTR, 0x00) // CLK low → falling edge, DATA low
}

// TestControlPortMotorSelectOnFallingClockEdge confirms the drive-select
// shift register only clocks on the FALLING edge of COMMS CLK, exactly
// as the IF1 hardware latches the daisy-chain bit. Holding the clock
// high (no edge) must not advance the chain.
func TestControlPortMotorSelectOnFallingClockEdge(t *testing.T) {
	u := NewULA(nil)
	u.Bus.Insert(0, microdrive.New(180))

	// Idle: clock high, data high. No edge yet — nothing selected.
	u.HandlePortWrite(PortCTR, 0x03)
	if u.Bus.AnyMotorOn() {
		t.Fatal("motor on before any clock edge")
	}
	// Writing the same value again — still no falling edge.
	u.HandlePortWrite(PortCTR, 0x03)
	if u.Bus.AnyMotorOn() {
		t.Fatal("motor on without a clock transition")
	}
	// Falling edge with DATA low → shift a motor-on bit into drive 0.
	u.HandlePortWrite(PortCTR, 0x00)
	if !u.Bus.Drive(0).MotorOn {
		t.Error("falling COMMS CLK edge did not engage drive 0")
	}
}

// TestControlPortRisingEdgeDoesNotSelect verifies that a low→high clock
// transition (rising edge) is ignored — only the falling edge matters.
func TestControlPortRisingEdgeDoesNotSelect(t *testing.T) {
	u := NewULA(nil)
	u.Bus.Insert(0, microdrive.New(180))

	u.HandlePortWrite(PortCTR, 0x00) // clock low, data low
	u.HandlePortWrite(PortCTR, 0x02) // clock HIGH → rising edge
	if u.Bus.AnyMotorOn() {
		t.Error("rising COMMS CLK edge engaged a drive (should be a no-op)")
	}
}

// TestControlPortEraseAndRWDoNotEngageMotor checks that toggling the
// ERASE (bit 3) and R/W (bit 2) control lines does not, on its own,
// disturb the motor-select shift register. Those bits gate the
// read/write head; only COMMS CLK/DATA drive the daisy chain. We write
// them with the clock held high (no falling edge) so any motor change
// would be a bug.
func TestControlPortEraseAndRWDoNotEngageMotor(t *testing.T) {
	u := NewULA(nil)
	u.Bus.Insert(0, microdrive.New(180))

	// Clock high throughout (bit1 set) so no falling edge occurs.
	for _, v := range []byte{
		0x02,               // CLK high only
		0x02 | 0x04,        // + R/W
		0x02 | 0x08,        // + ERASE
		0x02 | 0x04 | 0x08, // + R/W + ERASE
	} {
		u.HandlePortWrite(PortCTR, v)
		if u.Bus.AnyMotorOn() {
			t.Errorf("control write %02X (R/W & ERASE, clock high) engaged a drive", v)
		}
	}
}

// ---------------------------------------------------------------------------
// Status port ($EF) read bits: GAP, SYNC, WRITE-PROTECT, DTR, BUSY
// ---------------------------------------------------------------------------

// TestStatusPortDTRAndBusyAlwaysLow verifies the serial/network status
// bits read back low (no peripheral connected). The IF1 ROM polls these
// for RS-232 / SinclairNET activity; with nothing plugged in they must
// be idle so the ROM times out rather than hanging.
func TestStatusPortDTRAndBusyAlwaysLow(t *testing.T) {
	u := NewULA(nil)
	val, ok := u.HandlePortRead(PortCTR)
	if !ok {
		t.Fatal("CTR read not handled")
	}
	if val&0x08 != 0 { // DTR (bit 3 in the internal encoding)
		t.Errorf("DTR bit set (status=%02X), want low (no serial)", val)
	}
	if val&0x10 != 0 { // BUSY (bit 4)
		t.Errorf("BUSY bit set (status=%02X), want low (no SinclairNET)", val)
	}
}

// TestStatusPortWriteProtectBit confirms the WRITE-PROTECT status bit
// tracks the cartridge flag: low when the cartridge is protected, high
// when it is not. The bit is active-low on the bus.
func TestStatusPortWriteProtectBit(t *testing.T) {
	u := NewULA(nil)
	c := microdrive.New(180)
	u.Bus.Insert(0, c)
	engageDrive0(u)

	c.SetWriteProtect(true)
	val, _ := u.HandlePortRead(PortCTR)
	if val&0x01 != 0 {
		t.Errorf("write-protected cartridge: WP bit = 1 (status=%02X), want 0", val)
	}

	c.SetWriteProtect(false)
	val, _ = u.HandlePortRead(PortCTR)
	if val&0x01 == 0 {
		t.Errorf("unprotected cartridge: WP bit = 0 (status=%02X), want 1", val)
	}
}

// TestStatusPortGapSyncPulsePattern verifies the controller produces the
// alternating GAP / SYNC pulse pattern the IF1 ROM scans for on a
// formatted block. The ROM reads the status port repeatedly; it must
// see a run of "GAP present" (both bits high) followed by a run of
// "SYNC present" (GAP & SYNC bits low), then repeat — the signature of
// a real cartridge passing under the head.
func TestStatusPortGapSyncPulsePattern(t *testing.T) {
	bus := NewMicrodriveBus()
	bus.Insert(0, microdrive.New(180))
	d := bus.Drive(0)
	d.MotorOn = true // engage directly to isolate the status logic

	const (
		gapBit  = 0x04 // bit 2
		syncBit = 0x02 // bit 1
	)

	sawGapHigh := false    // both bits high (GAP region)
	sawGapSyncLow := false // both bits low (SYNC region)
	transitions := 0
	prevLow := false

	for i := 0; i < 64; i++ {
		s := bus.ReadStatusPort()
		bothHigh := s&gapBit != 0 && s&syncBit != 0
		bothLow := s&gapBit == 0 && s&syncBit == 0
		if bothHigh {
			sawGapHigh = true
		}
		if bothLow {
			sawGapSyncLow = true
		}
		if bothLow != prevLow {
			transitions++
		}
		prevLow = bothLow
	}

	if !sawGapHigh {
		t.Error("never saw the GAP region (both bits high) on a formatted block")
	}
	if !sawGapSyncLow {
		t.Error("never saw the SYNC region (GAP & SYNC bits low) on a formatted block")
	}
	if transitions < 2 {
		t.Errorf("GAP/SYNC pattern did not alternate (transitions=%d), ROM scan would stall", transitions)
	}
}

// TestStatusPortUnformattedBlockNoPulse confirms an UNFORMATTED block
// (no syncOK marker) never produces the GAP/SYNC pulse — the ROM must
// not mistake blank tape for a real block. We insert a cartridge but
// clear the formatting markers the way blank tape would be.
func TestStatusPortUnformattedBlockNoPulse(t *testing.T) {
	bus := NewMicrodriveBus()
	bus.Insert(0, microdrive.New(180))
	d := bus.Drive(0)
	d.MotorOn = true
	// Wipe the "assume formatted" markers Insert seeds, simulating
	// genuinely blank/unformatted tape under the head.
	d.pream = [512]byte{}

	for i := 0; i < 64; i++ {
		s := bus.ReadStatusPort()
		// WP bit (0) may vary; GAP (2) and SYNC (1) must stay high.
		if s&0x06 != 0x06 {
			t.Fatalf("unformatted block produced a GAP/SYNC pulse (status=%02X)", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Motor on/off + the 8-drive walking-bit shift register
// ---------------------------------------------------------------------------

// TestMotorWalkingBitSelectsEachDrive walks a single motor-on bit
// through all 8 daisy-chained drives and confirms exactly one drive is
// active at each step — the canonical "select drive N" sequence the IF1
// ROM uses (one COMMS-CLK pulse with data low to inject the bit, then
// data-high pulses to walk it down the chain).
func TestMotorWalkingBitSelectsEachDrive(t *testing.T) {
	bus := NewMicrodriveBus()
	for i := 0; i < NumDrives; i++ {
		bus.Insert(i, microdrive.New(180))
	}

	// Inject one motor-on bit at the head of the chain.
	bus.AdvanceMotor(true)
	// Then push it along with motor-off injections so only the walking
	// bit remains live.
	for step := 0; step < NumDrives; step++ {
		on := -1
		count := 0
		for i := 0; i < NumDrives; i++ {
			if bus.Drive(i).MotorOn {
				on = i
				count++
			}
		}
		if count != 1 {
			t.Fatalf("step %d: %d drives on, want exactly 1 (selected=%d)", step, count, on)
		}
		if on != step {
			t.Errorf("step %d: drive %d selected, want drive %d", step, on, step)
		}
		bus.AdvanceMotor(false) // walk the bit one slot further
	}

	// After walking past the last drive the chain is empty again.
	if bus.AnyMotorOn() {
		t.Error("motor bit did not fall off the end of the 8-drive chain")
	}
}

// TestMotorMultipleDrivesViaConsecutiveSelects confirms that injecting
// two motor-on bits in a row lights two adjacent drives — the daisy
// chain is a true shift register, not a one-hot selector. (This is the
// behaviour the existing daisy-chain test relies on, asserted here from
// the shift-register angle.)
func TestMotorMultipleDrivesViaConsecutiveSelects(t *testing.T) {
	bus := NewMicrodriveBus()
	for i := 0; i < NumDrives; i++ {
		bus.Insert(i, microdrive.New(180))
	}
	bus.AdvanceMotor(true)
	bus.AdvanceMotor(true)
	if !bus.Drive(0).MotorOn || !bus.Drive(1).MotorOn {
		t.Errorf("two consecutive selects: drives 0,1 = %v,%v, want both on",
			bus.Drive(0).MotorOn, bus.Drive(1).MotorOn)
	}
	if bus.Drive(2).MotorOn {
		t.Error("drive 2 should still be off after only two selects")
	}
}

// TestMotorOffStopsContributingToStatus verifies a drive with its motor
// off contributes 0xFF (all bus lines idle/high) to the AND-merged
// status, so a stopped drive never injects spurious GAP/SYNC/WP bits.
func TestMotorOffStopsContributingToStatus(t *testing.T) {
	bus := NewMicrodriveBus()
	c := microdrive.New(180)
	c.SetWriteProtect(true) // would pull WP low IF the motor were on
	bus.Insert(0, c)

	// Motor off: the protected cartridge must not pull the WP bit low.
	if got := bus.ReadStatusPort(); got&0x01 == 0 {
		t.Errorf("motor-off protected drive pulled WP low (status=%02X)", got)
	}

	// Motor on: now the WP bit reflects the cartridge.
	bus.Drive(0).MotorOn = true
	if got := bus.ReadStatusPort(); got&0x01 != 0 {
		t.Errorf("motor-on protected drive: WP bit high (status=%02X), want low", got)
	}
}

// TestMotorOffStopsDataStream confirms the data port returns the idle
// bus value (0xFF) once the motor is off — no bytes stream off a
// stopped tape.
func TestMotorOffStopsDataStream(t *testing.T) {
	bus := NewMicrodriveBus()
	c := microdrive.New(180)
	c.SetDataAt(0, 0x42)
	bus.Insert(0, c)

	// Motor on → reads the byte under the head.
	bus.Drive(0).MotorOn = true
	bus.RestartAll()
	if got := bus.ReadDataPort(); got != 0x42 {
		t.Fatalf("motor on: data port = %02X, want 0x42", got)
	}
	// Motor off → idle bus value regardless of tape contents.
	bus.Drive(0).MotorOn = false
	if got := bus.ReadDataPort(); got != 0xFF {
		t.Errorf("motor off: data port = %02X, want 0xFF (idle bus)", got)
	}
}

// ---------------------------------------------------------------------------
// IF1 ROM paging — the 8K ROM overlays the BASIC ROM via the M1 traps
// at 0x0008 (RST 8) and 0x1708 (close-channel), and pages out at 0x0700
// ---------------------------------------------------------------------------

// TestROMPageInTrapAddresses checks both documented M1 trigger addresses
// page the IF1 ROM in, and that an arbitrary PC does not.
func TestROMPageInTrapAddresses(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatal(err)
	}
	for _, pc := range []uint16{0x0008, 0x1708} {
		i.PageOut()
		i.PreFetchHook(pc)
		if !i.Active() {
			t.Errorf("M1 fetch at %04X did not page the IF1 ROM in", pc)
		}
	}
	i.PageOut()
	i.PreFetchHook(0x0009) // one past RST 8 — must not trap
	if i.Active() {
		t.Error("M1 fetch at 0x0009 wrongly paged the IF1 ROM in")
	}
}

// TestROMPageOutAtExitAddress confirms the IF1 ROM pages out when the
// CPU fetches at the documented exit address 0x0700.
func TestROMPageOutAtExitAddress(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatal(err)
	}
	i.PageIn()
	if !i.Active() {
		t.Fatal("setup: ROM not paged in")
	}
	i.PostFetchHook(0x0700)
	if i.Active() {
		t.Error("M1 fetch at 0x0700 did not page the IF1 ROM out")
	}
}

// TestROMOverlayHidesBasicROMWhenActive verifies the overlay actually
// substitutes IF1 ROM bytes for the BASIC ROM area (0x0000-0x3FFF) only
// while active, and that the 8K image is mirrored across the 16K window
// (the IF1 ROM is half the size of the area it overlays).
func TestROMOverlayHidesBasicROMWhenActive(t *testing.T) {
	i := New()
	if err := i.LoadROMBytes(fakeROM()); err != nil {
		t.Fatal(err)
	}

	// Inactive → never claims the ROM area (BASIC ROM stays visible).
	if _, ok := i.MemoryRead(0x0100); ok {
		t.Error("inactive IF1 claimed the ROM area")
	}

	i.PageIn()
	// Lower 8K and upper 8K of the 16K area both map to the same image.
	lo, okLo := i.MemoryRead(0x0123)
	hi, okHi := i.MemoryRead(0x2123) // 0x2123 & 0x1FFF == 0x0123
	if !okLo || !okHi {
		t.Fatal("active IF1 did not claim a ROM-area read")
	}
	if lo != hi {
		t.Errorf("8K mirror broken: [0x0123]=%02X [0x2123]=%02X, want equal", lo, hi)
	}
	if lo != 0x23 { // fakeROM byte = low 8 bits of address
		t.Errorf("overlay byte at 0x0123 = %02X, want 0x23", lo)
	}

	// RAM area (>= 0x4000) is never overlaid.
	if _, ok := i.MemoryRead(0x4000); ok {
		t.Error("IF1 overlay leaked into the RAM area (>= 0x4000)")
	}
}
