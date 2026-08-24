// Package dma implements the Spectrum Next's zxnDMA controller driven
// through its Z80-DMA-compatible command protocol on I/O ports 0x6B and
// 0x0B. The access port selects the mode (ports.txt 0x0b/0x6b, zxnext.vhd
// dma_mode): $6B = zxn dma (a block moves length bytes), $0B = Z80-DMA
// compatible (the counter loads -1, so a block moves length+1 bytes).
//
// The controller is programmed by a stream of bytes written to the
// port. Each byte is either a base register byte (WR0..WR6) — whose bit
// pattern both selects the register group and flags which extra
// "follow" bytes come next — or one of those announced follow bytes, or
// (for WR6) a command byte: RESET / LOAD / ENABLE / .... A memory
// transfer runs when an ENABLE command arrives; a LOAD before it latches
// the configured addresses into the live pointers, but nothing requires
// one (dma.vhd:724 has no armed flag to test).
//
// The command stream is variable-length: which follow bytes appear
// depends on the bits set in the preceding base byte, so a fixed-size
// command framing cannot parse it.
//
// Supported (the subset NextZXOS actually drives):
//
//   - WR0: transfer direction (A->B / B->A), port A start address,
//     block length
//   - WR1/WR2: port A/B address mode (increment / decrement / fixed),
//     with the optional variable-timing (+ prescaler) follow bytes
//     parsed and skipped
//   - WR4: port B start address (+ transfer-mode bits)
//   - WR6 commands: RESET ($C3), reset port A/B timing ($C7/$CB),
//     LOAD ($CF), ENABLE ($87); other status/continue commands are
//     accepted as no-ops
//
// Memory<->memory transfers run synchronously to completion; IO-port
// endpoints and burst+prescaler transfers interleave with the CPU via
// Step.
//
// Bus arbitration is modelled, and one half of it is live while the other is
// not. What runs in the shipped emulator: a block holds the bus for its bytes
// and charges that to the CPU, burst mode gives the bus back only where the
// FPGA does (WAITING_CYCLES, so only with a prescaler), and $83 abandons a
// block where it stands. The per-block bus acquisition is derived but not
// charged; see busAcquisitionCycles.
//
// What is modelled but never exercised: the dma_delay_i pin and everything
// behind it (SetBusDelay, BusRequested, the stalled state and its capture).
// Nothing in the emulator drives that pin, because its source does not exist as
// a live signal here -- pkg/next.IM2DaisyChain is a GHDL-golden reference model
// that is itself unwired, and NR$CC/$CD/$CE are stored-only. The behaviour is
// kept because it is the device's, and pinned by tests so it stays correct
// until the IM2 chain is connected, but no guest can reach it today. Do not
// read the presence of this code as a claim that a Next program's DMA can be
// held off the bus by an interrupt here. Recorded as ROADMAP item 2, which is
// the work that makes it reachable.
//
// The Z80 DMA's interrupt and match logic is deliberately absent, because the
// FPGA does not implement it either. The entity has no interrupt output and no
// IEI/IEO daisy-chain pins (dma.vhd:29-61); R3's interrupt-enable,
// stop-on-match and mask/match registers are declared and commented out
// (dma.vhd:87-90, :582-583, :802-813), so WR3's mask and match follow bytes are
// consumed and discarded; R4's interrupt-control, pulse-control and vector
// registers and the three states that would read them are commented out too
// (dma.vhd:94-96, :835-857); and the five WR6 interrupt commands $AF/$AB/$A3/
// $B7/$B3 decode into empty branches (dma.vhd:679-686, :722). On the machine
// side the daisy chain is inert as well: bus_busreq_n_i is tied high and
// cpu_bao_n is left open, "no dma controller on the expansion bus at this time"
// (zxnext.vhd:1787, :1791, :1822). dma_delay_i is the only interrupt-related
// pin that does anything, and it is an interrupt pausing the DMA rather than
// the DMA raising one.
//
// Two things the FPGA does that this model cannot express, both of which need
// a cycle-stepped engine rather than a block-at-a-time one: auto-restart never
// returns to IDLE, so the hardware holds the bus indefinitely and reports
// status $1B (dma.vhd:473-495); and wait_n_i stalls the transfer states
// (dma.vhd:342, :410), so a real bus hold stretches under ULA contention and
// SPI waits (zxnext.vhd:1844).
package dma

import (
	"fmt"
	"os"
)

// dmaTrace logs every port-0x6B byte to stderr when ZX_GO_DMA_TRACE is
// set, for diagnosing zxnDMA command-stream issues.
var dmaTrace = os.Getenv("ZX_GO_DMA_TRACE") != ""

func dmaLog(val byte) { fmt.Fprintf(os.Stderr, "DMA<-%02X\n", val) }

// Port address modes, decoded from a WR1/WR2 byte's bits 5..4.
const (
	addrDecrement byte = iota
	addrIncrement
	addrFixed
)

// MemoryBus is the contract DMA needs to move bytes between RAM
// locations. pkg/memory.Memory satisfies it.
type MemoryBus interface {
	Read(addr uint16) byte
	Write(addr uint16, val byte)
}

// IOBus is the contract DMA needs when a port is configured as an IO endpoint
// (WR1/WR2 D3 = 1) — e.g. DMA uploads to the sprite-image ($5B), Layer 2
// ($123B / $253B) or DAC ports. The 16-bit port number is the port's address
// register. pkg/ula.ULA satisfies it. May be nil; an IO endpoint with no bus
// degrades to a no-op read/write rather than corrupting memory.
type IOBus interface {
	ReadPort(port uint16) byte
	WritePort(port uint16, val byte)
}

// DMA is the zxnDMA controller: the configuration latched from the
// WR-register stream plus the small follow-byte state machine.
type DMA struct {
	mem MemoryBus
	io  IOBus

	portAStart uint16 // WR0: port A start address
	portBStart uint16 // WR4: port B start address
	blockLen   uint16 // WR0: block length (0 == 65536)
	aToB       bool   // WR0 bit 2: transfer port A -> port B
	aMode      byte   // WR1: port A address mode
	bMode      byte   // WR2: port B address mode
	aIsIO      bool   // WR1 D3: port A is an IO endpoint (else memory)
	bIsIO      bool   // WR2 D3: port B is an IO endpoint (else memory)

	// zMode is the FPGA's dma_mode input: false = zxn dma, true = Z80-DMA
	// compatible. The access port selects it — $6B = zxn, $0B = z80
	// (zxnext.vhd:1817 dma_mode <= port_0b_lsb) — so the ULA re-latches it on
	// every DMA port read/write. In z80 mode LOAD/CONTINUE/auto-restart seed
	// the byte counter with -1 (dma.vhd:664 "z80 dma loads -1"), making a
	// block move length+1 bytes, the classic Z80 DMA convention.
	zMode bool

	// Internal counters the chip exposes via the read mask. LOAD copies the
	// start addresses into curA/curB and zeroes counter; a transfer advances
	// them; Continue zeroes the counter without touching the pointers.
	curA, curB uint16
	counter    uint16

	// Timing: per-port cycle length (2..4) and the zxnDMA fixed-time
	// prescaler, plus the transfer mode (continuous/burst).
	aCycleLen byte
	bCycleLen byte
	prescaler byte
	mode      byte

	autoRestart bool // WR5 D5: reload + repeat at end of block

	// endOfBlock mirrors the chip's status_endofblock_n bit (inverted): the
	// FPGA sets status_endofblock_n='0' when a block finishes (dma.vhd:471) and
	// back to '1' on RESET / LOAD ($CF) / CONTINUE ($D3) / reinit-status ($8B)
	// (dma.vhd:639/654/671/691). It surfaces in the status read-back register
	// as bit 5 (active-low: 1 = not at end, 0 = end-of-block reached).
	endOfBlock bool

	// lastDuration is the T-state cost of the most recent transfer, derived
	// from the per-byte cycle cost (read + write cycle lengths, or the
	// prescaler if larger). The emulator charges it to the CPU clock.
	lastDuration uint64

	// Read-back: a WR6 "read mask follows" ($BB) selects which of the seven
	// internal registers (status, byte-counter lo/hi, port A lo/hi, port B
	// lo/hi — bits 0..6) appear in the read sequence; IO reads of the DMA
	// port return them in order, cycling. Power-up mask is 0x7F. readReg is
	// the FPGA's reg_rd_seq_s: the register (0..6) the NEXT read returns.
	readMask byte
	readReg  int

	// cycleSink, when set, is called with a block's T-state duration so the
	// emulator can charge it to the CPU clock: while the DMA holds the bus the
	// CPU is frozen outright (zxnext.vhd:1824-1835). Every block that runs
	// end to end is charged, in burst mode as much as in continuous. The only
	// state that gives the bus back mid-block is WAITING_CYCLES, which needs a
	// prescaler (dma.vhd:424, :441-449), and that is the interleaved path
	// Step() runs instead.
	cycleSink func(uint64)

	// clock returns the current CPU T-state count. When set, a burst-mode
	// transfer with a non-zero prescaler is interleaved with the CPU: it pumps
	// one byte every prescaler T-states from Step(), letting the CPU run in the
	// gaps (so DMA-streamed audio is paced across the CPU timeline).
	clock func() uint64

	// speedMul returns the CPU's speed multiplier: 1, 2, 4 or 8 for 3.5, 7, 14
	// and 28 MHz. The prescaler needs it because its period is a fixed wall
	// time, not a fixed T-state count: DMA_timer_s advances by 8/4/2/1 per CPU
	// clock across those speeds (dma.vhd:250-254) against a fixed compare, so
	// the same prescaler value costs 4x its value in T-states at 3.5 MHz and
	// 32x at 28 MHz. Nil means 3.5 MHz, the FPGA's turbo "00".
	speedMul func() int

	// Transfer-engine position. remaining is the bytes the current block still
	// owes, which outlives the call that started the block in two cases: an
	// interleaved burst (activeBurst, with nextDue the absolute T-state the next
	// byte falls due) and a block parked by the bus delay (see stalled).
	activeBurst bool
	remaining   int
	nextDue     uint64

	// inTransfer is set while a block is moving. An IO endpoint can be any
	// port, including the DMA's own command port ($6B / $0B) — the emulator
	// hands IO-endpoint accesses to the ULA's port dispatch, which routes
	// those two straight back to WriteCommand. A transferred byte that
	// happens to be ENABLE ($87) would then start a nested Trigger, and the
	// nesting is unbounded: `fatal error: stack overflow`, which no recover()
	// can catch. The FPGA has no such hazard because it is a state machine
	// already sitting in its transfer state, so an ENABLE arriving mid-block
	// is not a second transfer. This flag reproduces that.
	inTransfer bool

	// atLeastOne mirrors the chip's status_atleastone bit, status byte bit 0.
	// The FPGA raises it in TRANSFERING_WRITE_4 (dma.vhd:412), after the first
	// byte of the block has been written rather than at the ENABLE, and clears it
	// again in IDLE (dma.vhd:265), on $C3 (dma.vhd:640) and on $8B
	// (dma.vhd:692). A driver polling the status register while an interleaved
	// burst streams sees it set for the whole of the block.
	atLeastOne bool

	// busDelay is the FPGA's dma_delay_i input pin. On the Next it is
	// im2_dma_delay (zxnext.vhd:1785, :2001-2010): an IM2 peripheral whose
	// NR$CC/$CD/$CE dma-int-enable bit is set and whose FSM is out of idle
	// (im2_device.vhd:151, im2_control.vhd:238), an NMI with NR$CC bit 7, or a
	// RETI pop still in progress. It is an interrupt holding the DMA off the
	// bus so the CPU can be serviced, not the DMA raising one; this device has
	// no interrupt output at all (dma.vhd:29-61 has no INT pin and no IEI/IEO).
	//
	// stalled is the transfer FSM sitting in START_DMA because of it, with the
	// block part done and the bus handed back. It is a position no register can
	// describe: the pointers and the byte counter say where the block got to,
	// but only this says the block is still going.
	busDelay bool
	stalled  bool

	// wedged is the FPGA's register-write sequencer parked in R4_BYTE_2, a state
	// whose case branch is one of the commented-out interrupt-control ones
	// (dma.vhd:835-844). A WR4 byte with D4 set and D2/D3 clear sends it there
	// (dma.vhd:607-608) and nothing brings it back: dma.vhd:891's
	// "when others => null" swallows every subsequent command byte, and the only
	// assignment of IDLE outside the sequencer's own IDLE branch is under the
	// reset pin (dma.vhd:229). So even a $C3 RESET is swallowed; see Reset().
	wedged bool

	// pending holds the follow bytes the most recent base byte announced;
	// each subsequent WriteCommand consumes one.
	pending []pendingOp
}

// pendingOp names one follow byte the command stream has announced but not yet
// delivered.
//
// The queue holds these codes rather than the setter closures it once held
// because it is live chip state that has to survive a state capture: one OUT
// delivers one byte, so a capture lands between a base byte and the bytes it
// announced as often as not, and a controller restored without the queue reads
// the next address byte as a base byte and silently reprograms itself.
type pendingOp byte

const (
	opIgnore       pendingOp = iota // announced, accepted, discarded
	opPortALow                      // WR0 port A start address, low byte
	opPortAHigh                     // WR0 port A start address, high byte
	opBlockLenLow                   // WR0 block length, low byte
	opBlockLenHigh                  // WR0 block length, high byte
	opPortBLow                      // WR4 port B start address, low byte
	opPortBHigh                     // WR4 port B start address, high byte
	opATiming                       // WR1 port A variable-timing byte
	opBTiming                       // WR2 port B variable-timing byte
	opPrescaler                     // zxnDMA fixed-time prescaler byte

	// opRetiredInterruptControl is code 10, which the WR4 interrupt-control
	// follow byte used to occupy. Nothing queues it any more: the FPGA never
	// reads that byte, so WR4's D4 no longer announces one. The slot is kept
	// rather than closed up because these codes are a wire format -- SaveState
	// writes them into dmaState.Pending and LoadState validates them against
	// opLast -- so closing the gap would renumber every code above it and make
	// an older capture mean something different. An older capture carrying this
	// one still has to swallow the byte it announced, which applyPending's
	// discard arm does. TestPendingOpCodesAreAStableWireFormat pins the numbers.
	opRetiredInterruptControl

	opReadMask // WR6 $BB read-mask byte
)

// opLast bounds the valid codes, so a decoded state carrying a code this build
// does not know is refused rather than dispatched nowhere.
const opLast = opReadMask

// Transfer modes (WR4 D6:D5).
const (
	modeContinuous byte = iota
	modeBurst
)

// busAcquisitionCycles is what one trip around the bus costs on top of the
// bytes: START_DMA asserting cpu_busreq_n_s (dma.vhd:267-283), WAITING_ACK
// latching the source address while the acknowledge arrives (dma.vhd:285-305),
// and FINISH_DMA before the return to IDLE that releases the bus again
// (dma.vhd:469-495, :260-265). The DMA owns the bus in all three, so the CPU
// is stopped for them.
//
// It is derived, recorded, and NOT charged, and the reason is not the one first
// written here. The original note said charging it broke TX-1696, which implied
// the charge was at fault. Measurement says otherwise: perturbing the per-block
// cost by every value from -5 to +10 T-states breaks that title at -4, -2, +3,
// +4, +6, +8, +9 and +10, and leaves it working at -5, -3, -1, 0, +1, +2, +5
// and +7. Seven of sixteen, scattered, with no monotonic relationship to how
// much time is added. TX-1696 is balanced on a phase edge, and the fact that
// zero happens to be one of the working phases is luck, not correctness.
//
// So charging these three cycles is not wrong; it lands on one failing phase
// among many. It stays deferred because doing it would knowingly regress a
// working title while the underlying marginality is unexplained, and fixing
// that marginality is the actual defect. See ROADMAP item 1.
// TestBusAcquisitionIsDerivedButNotCharged pins the deferral.
const busAcquisitionCycles = 3

// resetCycleLen is the per-port cycle length both ports come up with and go
// back to. The reset pin and the $C3 command both write "01" into the two
// timing registers (dma.vhd:233-234, :641-642), and the transfer FSM decodes
// "01" as three cycles (dma.vhd:314, :321).
const resetCycleLen = 3

// New returns a fresh DMA with no transfer queued.
func New(mem MemoryBus) *DMA {
	return &DMA{mem: mem, aCycleLen: resetCycleLen, bCycleLen: resetCycleLen, readMask: 0x7F}
}

// SetIOBus attaches the port bus used for IO endpoints. Optional.
func (d *DMA) SetIOBus(io IOBus) { d.io = io }

// SetZ80Mode latches the DMA mode (the FPGA's dma_mode): false = zxn dma,
// true = Z80-DMA compatible. The ULA calls it on every DMA port access with
// port == $0B, mirroring zxnext.vhd:1817 (dma_mode <= port_0b_lsb on any DMA
// port read or write).
func (d *DMA) SetZ80Mode(on bool) { d.zMode = on }

// SetCycleSink attaches the callback used to charge a block's T-state duration
// to the CPU clock, which is what the CPU loses to the DMA holding the bus.
// Optional.
func (d *DMA) SetCycleSink(sink func(uint64)) { d.cycleSink = sink }

// SetBusDelay drives the controller's dma_delay_i pin (zxnext.vhd:1785). While
// it is high the DMA will not ask for the bus, and a block already in flight
// parks itself in START_DMA with its pointers where they stand; dropping it
// resumes that block from exactly there.
//
// It is an injectable input rather than a wire to the IM2 daisy chain because
// the source is not connected on our side yet: pkg/next.IM2DaisyChain is an
// unwired reference model and NR$CC/$CD/$CE are stored-only.
func (d *DMA) SetBusDelay(on bool) {
	d.busDelay = on
	if !on && d.stalled {
		d.stalled = false
		d.runBlock()
	}
}

// BusRequested reports whether the DMA is holding the CPU off the bus right
// now: cpu_busreq_n_s asserted and the bus acknowledged, which on the Next
// freezes the CPU outright (zxnext.vhd:1824-1835, :1842).
//
// Nothing in the emulator calls this yet; it is the read side of the same
// not-yet-wired arbitration as SetBusDelay. See the package comment.
//
// It reads as true only from inside a transfer: an IO endpoint's port
// callback, which is the one place the rest of the machine is re-entered
// mid-block, because everything between the request and the release happens
// inside one call here. A stalled block is NOT holding the bus: START_DMA
// deasserts the request while dma_delay_i is high (dma.vhd:269-273).
func (d *DMA) BusRequested() bool { return d.inTransfer }

// SetClock attaches a CPU-T-state source. With it, burst-mode + prescaler
// transfers interleave with the CPU via Step(). Optional — without it, burst
// transfers run to completion at ENABLE.
func (d *DMA) SetClock(clock func() uint64) { d.clock = clock }

// SetSpeedMultiplier attaches the CPU's speed multiplier (1, 2, 4 or 8).
// Without it the controller paces its prescaler as if the CPU were at 3.5 MHz.
func (d *DMA) SetSpeedMultiplier(m func() int) { d.speedMul = m }

// prescalerTStates is the fixed-time prescaler's period in CPU T-states.
//
// The FPGA waits until DMA_timer_s(13 downto 5) reaches the prescaler
// (dma.vhd:424, :451). Comparing bits 13:5 divides by 32, so the wait ends when
// the timer reaches prescaler*32, and DMA_timer_s advances by 8/4/2/1 per CPU
// clock at 3.5/7/14/28 MHz (dma.vhd:250-254). That is prescaler*32/increment
// ticks of the CPU clock, which is 4*prescaler*multiplier in every case: a
// constant wall-clock period, which is what scaling the increment is for.
func (d *DMA) prescalerTStates() uint64 {
	mul := 1
	if d.speedMul != nil {
		if m := d.speedMul(); m > 0 {
			mul = m
		}
	}
	return 4 * uint64(d.prescaler) * uint64(mul)
}

// Reset drives the controller's reset pin, the FPGA's reset_i, whose branch is
// dma.vhd:211-245. It is not the $C3 RESET command. On the FPGA, $C3 is decoded
// inside the register-write sequencer and assigns exactly eight signals
// (dma.vhd:638-645) -- the FSM to IDLE, both status bits, both port timings, the
// prescaler, ce_wait and auto-restart -- a strict subset of this branch, and a
// sequencer wedged in R4_BYTE_2 never decodes it at all.
//
// That subset relationship does not hold in this model yet: command()'s $C3
// rebuilds the whole struct, so it clears the byte counter, the transfer mode,
// the read mask and its cursor, and every latched address, length, direction
// and port mode, all of which the FPGA's $C3 leaves standing. Recorded as
// ROADMAP item 4.
//
// The branch restores the transfer FSM and the register-write sequencer to
// IDLE, both port timings to "01" (3 cycles), the prescaler to zero, the
// transfer mode to continuous, auto-restart off, the read mask to all seven
// registers with the read sequence back at the status byte, the byte counter to
// zero and both status bits to their idle values. What it does NOT name
// survives: the latched port A / port B addresses, the block length, the
// direction and both ports' address / memory-or-IO modes, and the live transfer
// pointers dma_src_s / dma_dest_s.
func (d *DMA) Reset() {
	d.wedged = false
	d.pending = nil
	d.activeBurst = false
	d.inTransfer = false
	d.remaining = 0
	d.nextDue = 0
	d.counter = 0
	d.aCycleLen = resetCycleLen
	d.bCycleLen = resetCycleLen
	d.prescaler = 0
	d.mode = modeContinuous
	d.autoRestart = false
	d.readMask = 0x7F
	d.readReg = 0
	d.endOfBlock = false
	d.atLeastOne = false
	d.stalled = false
}

// WriteCommand accepts one byte of the port-0x6B command stream. Wired
// via ULA.SetNextDMA / the routing in ULA.WritePort.
func (d *DMA) WriteCommand(val byte) {
	if dmaTrace {
		dmaLog(val)
	}
	if d.wedged {
		// The register-write sequencer is parked in R4_BYTE_2, so the byte falls
		// through dma.vhd:891's "when others => null" and nothing at all happens.
		// The read path is a separate branch of the same process (dma.vhd:895
		// "elsif cs_rd_v = ..."), so read-back keeps working.
		return
	}
	if len(d.pending) > 0 {
		op := d.pending[0]
		d.pending = d.pending[1:]
		d.applyPending(op, val)
		return
	}
	d.decodeBase(val)
}

// applyPending consumes one announced follow byte. A timing or interrupt-control
// byte can itself announce further bytes, which it appends to the queue the
// caller has already popped from.
func (d *DMA) applyPending(op pendingOp, val byte) {
	switch op {
	case opPortALow:
		d.portAStart = (d.portAStart &^ 0x00FF) | uint16(val)
	case opPortAHigh:
		d.portAStart = (d.portAStart &^ 0xFF00) | uint16(val)<<8
	case opBlockLenLow:
		d.blockLen = (d.blockLen &^ 0x00FF) | uint16(val)
	case opBlockLenHigh:
		d.blockLen = (d.blockLen &^ 0xFF00) | uint16(val)<<8
	case opPortBLow:
		d.portBStart = (d.portBStart &^ 0x00FF) | uint16(val)
	case opPortBHigh:
		d.portBStart = (d.portBStart &^ 0xFF00) | uint16(val)<<8
	case opATiming: // port A's cycle length; port A has no prescaler
		d.aCycleLen = cycleLen(val)
	case opBTiming: // port B's cycle length, and D5 = the prescaler byte follows
		d.bCycleLen = cycleLen(val)
		if val&0x20 != 0 {
			d.pending = append(d.pending, opPrescaler)
		}
	case opPrescaler:
		d.prescaler = val
	case opReadMask:
		d.readMask = val & 0x7F
		d.readReg = d.firstReadReg()
	default: // opIgnore — an announced byte we accept and discard
	}
}

// addrMode decodes a WR1/WR2 byte's address-mode bits (D5 D4): bit 5 set
// = fixed; else bit 4 set = increment; else decrement.
func addrMode(val byte) byte {
	switch {
	case val&0x20 != 0:
		return addrFixed
	case val&0x10 != 0:
		return addrIncrement
	default:
		return addrDecrement
	}
}

// decodeBase interprets a base register byte and queues its follow
// bytes (or, for WR6, executes the command immediately).
func (d *DMA) decodeBase(val byte) {
	if val&0x80 == 0 {
		switch {
		case val&0x03 != 0: // WR0 — transfer setup
			d.aToB = val&0x04 != 0
			var p []pendingOp
			if val&0x08 != 0 {
				p = append(p, opPortALow)
			}
			if val&0x10 != 0 {
				p = append(p, opPortAHigh)
			}
			if val&0x20 != 0 {
				p = append(p, opBlockLenLow)
			}
			if val&0x40 != 0 {
				p = append(p, opBlockLenHigh)
			}
			d.pending = p
		case val&0x07 == 0x04: // WR1 — port A config
			d.aMode = addrMode(val)
			d.aIsIO = val&0x08 != 0
			if val&0x40 != 0 { // variable-timing byte follows
				d.pending = []pendingOp{opATiming}
			}
		case val&0x07 == 0x00: // WR2 — port B config
			d.bMode = addrMode(val)
			d.bIsIO = val&0x08 != 0
			if val&0x40 != 0 {
				d.pending = []pendingOp{opBTiming}
			}
		}
		return
	}
	switch val & 0x03 {
	case 0x00: // WR3 — match/mask (accepted; follow bytes skipped)
		var p []pendingOp
		if val&0x08 != 0 {
			p = append(p, opIgnore)
		}
		if val&0x10 != 0 {
			p = append(p, opIgnore)
		}
		d.pending = p
		// D6 is a second ENABLE. The FPGA latches it into R3_dma_en_s and, when
		// it is set, drops the transfer FSM into START_DMA (dma.vhd:576-580),
		// the same state $87 jumps to (dma.vhd:724). R3_dma_en_s itself is
		// written at :576 and read nowhere, so it is dead state: a WR3 byte with
		// D6 clear starts nothing and stops nothing.
		if val&0x40 != 0 {
			d.Trigger()
		}
	case 0x01: // WR4 — port B address + transfer mode
		switch (val >> 5) & 0x03 {
		case 0x02: // 10 = burst
			d.mode = modeBurst
		default: // 01 continuous; 00/11 "do not use" behave continuous
			d.mode = modeContinuous
		}
		var p []pendingOp
		if val&0x04 != 0 {
			p = append(p, opPortBLow)
		}
		if val&0x08 != 0 {
			p = append(p, opPortBHigh)
		}
		// D4 announces the Z80 DMA's interrupt-control byte, and the FPGA never
		// reads it. Both port B address bytes ARE consumed first: R4_BYTE_0
		// takes the low byte (dma.vhd:816) and hands over to R4_BYTE_1, which
		// takes the high byte (dma.vhd:827). What R4_BYTE_1 then does is return
		// to IDLE unconditionally (dma.vhd:832) instead of going on to
		// R4_BYTE_2, and the R4_BYTE_2 branch that would have consumed the
		// interrupt-control byte is commented out
		// (dma.vhd:835-844), as are R4_interrupt_control_s / pulse_control_s /
		// interrupt_vector_s themselves (dma.vhd:94-96). The byte the guest
		// meant as interrupt control is decoded as the next base byte.
		//
		// Unless it is the only follow byte announced. The decode is an elsif
		// ladder (dma.vhd:603-611), so D4 without D2 or D3 selects R4_BYTE_2, the
		// state whose branch is commented out, and the sequencer wedges.
		if len(p) == 0 && val&0x10 != 0 {
			d.wedged = true
		}
		d.pending = p
	case 0x02: // WR5 — ready/wait/auto-restart (no follow bytes)
		d.autoRestart = val&0x20 != 0 // D5: 0 = stop on end, 1 = auto-restart
	case 0x03: // WR6 — command
		d.command(val)
	}
}

// cycleLen decodes a timing byte's D1:D0 into a read/write cycle count. The
// FPGA's decode is a case statement over the two bits with an explicit arm for
// 00, 01 and 10 and a "when others" for the rest, and that last arm goes to the
// four-cycle state (dma.vhd:314-317, :321-324). So the Z80 DMA's "do not use"
// code 11 is not undefined here: it is four cycles, the same as 00.
func cycleLen(v byte) byte {
	switch v & 0x03 {
	case 0x01:
		return 3
	case 0x02:
		return 2
	default: // 00, and 11 through the FPGA's "when others" arm
		return 4
	}
}

// command executes a WR6 command byte.
func (d *DMA) command(val byte) {
	switch val {
	case 0xC3: // RESET — clear configuration + state machine (keep the buses;
		// dma_mode and dma_delay_i are pins outside the core in the FPGA, so
		// they survive too)
		*d = DMA{mem: d.mem, io: d.io, cycleSink: d.cycleSink, clock: d.clock,
			speedMul: d.speedMul, zMode: d.zMode, busDelay: d.busDelay,
			aCycleLen: resetCycleLen, bCycleLen: resetCycleLen, readMask: 0x7F}
	case 0xCF: // LOAD — latch the start addresses into the internal pointers
		d.curA = d.portAStart
		d.curB = d.portBStart
		d.counter = d.counterInit()
		d.endOfBlock = false // dma.vhd:654: LOAD clears status_endofblock_n='1'
	case 0xD3: // CONTINUE — reseed the byte counter; a following ENABLE repeats
		// the block from the CURRENT pointers (not the start addresses).
		d.counter = d.counterInit()
		d.endOfBlock = false // dma.vhd:671: Continue clears status_endofblock_n='1'
	case 0x87: // ENABLE is dma_seq_s <= START_DMA, unconditionally
		// (dma.vhd:724-725). There is no armed flag in the device: an ENABLE
		// with no LOAD behind it, or after a block has already ended, still
		// enters the transfer loop, and the loop always writes one byte before
		// it tests the counter against the block length (dma.vhd:426, :433).
		d.Trigger()
	case 0xC7: // RESET PORT A TIMING (dma.vhd:647-648): back to "01", 3 cycles
		d.aCycleLen = resetCycleLen
	case 0xCB: // RESET PORT B TIMING (dma.vhd:650-651). Per port, so it leaves
		// port A's programmed timing alone.
		d.bCycleLen = resetCycleLen
	case 0xBB: // READ MASK FOLLOWS — next byte sets the read mask
		d.pending = []pendingOp{opReadMask}
	case 0xA7: // INITIATE READ SEQUENCE — reset the read cursor
		d.readReg = d.firstReadReg()
	case 0xBF: // READ STATUS BYTE (dma.vhd:687): the next port read returns
		// the status register, wherever the sequence stood.
		d.readReg = 0
	case 0x8B: // REINITIALIZE STATUS BYTE (dma.vhd:691-692): endofblock_n='1'
		// and atleastone='0'
		d.endOfBlock = false
		d.atLeastOne = false
	case 0x83: // DISABLE DMA (dma.vhd:727-728) is one assignment: dma_seq_s <=
		// IDLE. The transfer FSM leaves mid-flight, so the bytes still owed are
		// abandoned and the bus request drops (dma.vhd:260-262). FINISH_DMA never
		// runs, so end-of-block does not latch and auto-restart does not reload.
		d.activeBurst = false
		d.remaining = 0
		d.stalled = false
		d.inTransfer = false // no longer in the transfer states; see runBlock
		d.atLeastOne = false // IDLE clears status_atleastone (dma.vhd:265)
	default:
		// The remaining WR6 commands are the interrupt ones, and the FPGA
		// decodes them into empty branches: $AF disable, $AB enable, $A3 reset
		// and disable, $B7 enable after RETI (dma.vhd:679-686) and $B3 force
		// ready (dma.vhd:722). There is no interrupt logic behind them to drive
		// no interrupt output, no IEI/IEO, no vector (dma.vhd:29-61, :94-96).
	}
}

// Trigger runs the configured transfer to completion from the current internal
// pointers. Each byte is read from the source endpoint (memory or IO) and
// written to the destination endpoint, advancing each port's pointer per its
// address mode. On end of block the DMA either auto-restarts (reload the start
// addresses and go round again) or returns to IDLE.
//
// The byte count mirrors the FPGA transfer loop (dma.vhd TRANSFERING_WRITE_1
// increments the counter; TRANSFERING_WRITE_4 repeats while counter <
// block length): the counter was seeded by LOAD/CONTINUE — 0 in zxn mode, -1
// in z80 mode — so zxn moves exactly blockLen bytes and z80 moves blockLen+1.
// The FSM always moves one byte before testing, so a zero length moves one.
func (d *DMA) Trigger() {
	if d.inTransfer || d.activeBurst || d.stalled {
		// Re-entered from an IO endpoint pointed at our own command port, or
		// an ENABLE arrived while an interleaved burst is still draining, or
		// while a block sits in START_DMA waiting for the bus delay to drop.
		// The chip is already in the middle of this block; $87 only re-enters
		// START_DMA (dma.vhd:724), it does not start a second one.
		return
	}
	moved := 0
	for c := d.counter; ; {
		moved++
		c++
		if c >= d.blockLen {
			break
		}
	}
	if dmaTrace {
		fmt.Fprintf(os.Stderr, "DMA xfer A=%04X B=%04X len=%d aIO=%v bIO=%v mode=%d presc=%d\n",
			d.curA, d.curB, moved, d.aIsIO, d.bIsIO, d.mode, d.prescaler)
	}
	// busAcquisitionCycles is deliberately absent from this sum; see
	// TestBusAcquisitionIsDerivedButNotCharged for why.
	d.lastDuration = uint64(moved) * d.perByteCycles()

	// Burst mode with a fixed-time prescaler interleaves with the CPU: defer
	// the bytes to Step(), which pumps one every prescaler T-states while the
	// CPU runs in the gaps (the spec's only case where burst yields the bus).
	// Without a clock or prescaler it runs to completion like continuous.
	if d.mode == modeBurst && d.prescaler != 0 && d.clock != nil {
		d.activeBurst = true
		d.remaining = moved
		d.nextDue = d.clock()
		return
	}

	d.remaining = moved
	d.runBlock()
}

// runBlock moves the bytes the current block still owes, from START_DMA to
// FINISH_DMA, unless the bus delay stops it.
//
// It is one call for a whole block because nothing in the FPGA's transfer loop
// gives the bus back once it has it: WAITING_CYCLES is the only state that
// deasserts cpu_busreq_n_s (dma.vhd:441-449) and it is reachable only with a
// non-zero prescaler (dma.vhd:424), which is the interleaved path Step() runs
// instead. So the CPU is stopped for the whole of this, in burst mode exactly
// as much as in continuous, and the sink is charged once the block completes.
func (d *DMA) runBlock() {
	if d.busDelay {
		// START_DMA holds cpu_busreq_n_s deasserted while dma_delay_i is high
		// (dma.vhd:269-273): the DMA does not merely fail to win the bus, it
		// does not ask for it. It sits here until the pin drops.
		d.stalled = true
		return
	}
	d.stalled = false
	srcIsA := d.aToB // port A is the source when transferring A -> B
	d.inTransfer = true
	for d.remaining > 0 {
		b := d.portRead(srcIsA)
		// The byte counter goes up in TRANSFERING_WRITE_1 (dma.vhd:361), the
		// first of the write states, so it counts the byte before the write
		// cycle it belongs to has finished.
		d.counter++
		d.remaining--
		d.portWrite(!srcIsA, b)
		if !d.inTransfer {
			// The byte just written was a command that took the transfer FSM
			// out of the transfer states: $83's dma_seq_s <= IDLE, or a $C3
			// that rebuilt the device underneath us. Only an IO endpoint
			// pointed at the DMA's own command port can reach this. Everything
			// below is downstream of the transfer states -- status_atleastone,
			// FINISH_DMA, end-of-block, auto-restart, the bus charge -- and
			// IDLE reaches none of it (dma.vhd:260-265, :469-495).
			return
		}
		// status_atleastone goes up in TRANSFERING_WRITE_4 (dma.vhd:412), the
		// same state that drops dma_write_cycle: the byte's write cycle has
		// completed. Whether it is visible to the target DURING that write is
		// not a question the hardware can answer, because the CPU is frozen
		// while the DMA holds the bus (zxnext.vhd:1824-1835) and only this
		// model re-enters the machine from inside the write. What is answerable
		// is that a read taken after a byte has moved sees it set.
		d.atLeastOne = true
		if d.busDelay && d.remaining > 0 {
			// TRANSFERING_WRITE_4 goes back to START_DMA rather than on to the
			// next byte when dma_delay_i is high (dma.vhd:427-428), so a delay
			// raised mid-block parks the transfer and hands the bus back with
			// the pointers where they stand.
			d.inTransfer = false
			d.stalled = true
			return
		}
	}
	d.inTransfer = false
	if d.cycleSink != nil {
		d.cycleSink(d.lastDuration)
	}
	d.finishBlock()
}

// finishBlock applies the auto-restart-or-stop policy after a transfer's last
// byte: auto-restart reloads the start addresses and goes round again;
// otherwise the FSM returns to IDLE. Either way the end-of-block bit latches (the FPGA
// sets status_endofblock_n='0' at FINISH_DMA, dma.vhd:471, regardless of the
// auto-restart branch).
func (d *DMA) finishBlock() {
	d.endOfBlock = true
	if d.autoRestart {
		d.curA = d.portAStart
		d.curB = d.portBStart
		d.counter = d.counterInit() // dma.vhd:482: z80 mode reloads -1
		// status_atleastone stays set: FINISH_DMA under auto-restart goes to
		// START_DMA or WAITING_ACK (dma.vhd:490-494), never to IDLE, so the bit
		// IDLE would have cleared (dma.vhd:265) never gets cleared and the
		// status register reads $1B for as long as the restarts continue.
	} else {
		d.atLeastOne = false // FINISH_DMA -> IDLE (dma.vhd:495, :265)
	}
}

// counterInit is the byte-counter seed LOAD/CONTINUE/auto-restart apply:
// 0 in zxn mode, -1 in z80 mode (dma.vhd:664 "z80 dma loads -1" — the source
// of the Z80 DMA's length+1 transfer convention).
func (d *DMA) counterInit() uint16 {
	if d.zMode {
		return 0xFFFF
	}
	return 0
}

// Step advances an interleaved burst transfer: it transfers every byte whose
// due time has arrived by `now` (the current CPU T-state), spacing them by the
// prescaler. No-op unless a burst+prescaler transfer is in flight. Call it from
// the CPU's per-instruction hook so DMA-streamed audio is paced correctly and
// the CPU runs between bytes.
func (d *DMA) Step(now uint64) {
	if !d.activeBurst {
		return
	}
	if d.busDelay {
		// TRANSFERING_WRITE_4 and WAITING_CYCLES both hand a burst back to
		// START_DMA while dma_delay_i is high (dma.vhd:427-428, :456-461), and
		// START_DMA does not ask for the bus (dma.vhd:269-273). Nothing falls
		// due until the pin drops. The due time follows `now` while it is
		// parked because the fixed-time counter DMA_timer_s is cleared when the
		// next read finally starts (dma.vhd:309), not while the DMA waits: a
		// long pause leaves no backlog to dump in one go.
		d.nextDue = now
		return
	}
	per := d.perByteCycles()
	srcIsA := d.aToB
	// Same self-port re-entry hazard as Trigger: a pumped byte can land back
	// in WriteCommand through an IO endpoint aimed at $6B / $0B.
	d.inTransfer = true
	for d.remaining > 0 && now >= d.nextDue {
		b := d.portRead(srcIsA)
		d.counter++ // dma.vhd:361, in TRANSFERING_WRITE_1
		d.remaining--
		d.portWrite(!srcIsA, b)
		if !d.inTransfer {
			// The pumped byte was a command that took the FSM to IDLE; see
			// the same guard in runBlock.
			return
		}
		d.atLeastOne = true // dma.vhd:412, in TRANSFERING_WRITE_4
		d.nextDue += per
	}
	d.inTransfer = false
	if d.remaining == 0 {
		d.activeBurst = false
		d.finishBlock()
	}
}

// portRead reads one byte from port A (a=true) or port B, from memory or IO per
// that port's configuration, then advances the port's pointer.
func (d *DMA) portRead(a bool) byte {
	if a {
		v := d.endpointRead(d.aIsIO, d.curA)
		d.curA = stepAddr(d.curA, d.aMode)
		return v
	}
	v := d.endpointRead(d.bIsIO, d.curB)
	d.curB = stepAddr(d.curB, d.bMode)
	return v
}

// portWrite writes one byte to port A (a=true) or port B, then advances its
// pointer.
func (d *DMA) portWrite(a bool, val byte) {
	if a {
		d.endpointWrite(d.aIsIO, d.curA, val)
		d.curA = stepAddr(d.curA, d.aMode)
		return
	}
	d.endpointWrite(d.bIsIO, d.curB, val)
	d.curB = stepAddr(d.curB, d.bMode)
}

func (d *DMA) endpointRead(isIO bool, addr uint16) byte {
	if isIO {
		if d.io != nil {
			return d.io.ReadPort(addr)
		}
		return 0xFF
	}
	return d.mem.Read(addr)
}

func (d *DMA) endpointWrite(isIO bool, addr uint16, val byte) {
	if isIO {
		if d.io != nil {
			d.io.WritePort(addr, val)
		}
		return
	}
	d.mem.Write(addr, val)
}

// perByteCycles is the T-state cost of moving one byte: the source read cycle
// length plus the destination write cycle length, or the fixed-time prescaler
// if it is larger (zxnDMA "the transfer takes at least <prescaler> cycles per
// byte" — the sampled-audio feature).
func (d *DMA) perByteCycles() uint64 {
	srcCyc, dstCyc := d.aCycleLen, d.bCycleLen
	if !d.aToB { // B is the source
		srcCyc, dstCyc = d.bCycleLen, d.aCycleLen
	}
	per := uint64(srcCyc) + uint64(dstCyc)
	// DMA_timer_s is cleared at TRANSFERING_READ_1 (dma.vhd:309), so the
	// prescaler wait overlaps the byte's own cycles rather than following them:
	// the slower of the two sets the pace.
	if p := d.prescalerTStates(); p > per {
		per = p
	}
	return per
}

func stepAddr(a uint16, mode byte) uint16 {
	switch mode {
	case addrIncrement:
		return a + 1
	case addrDecrement:
		return a - 1
	default: // addrFixed
		return a
	}
}

// Source / Destination / Length — accessors for tests. Source is the
// port A start address, Destination the port B start address.
func (d *DMA) Source() uint16      { return d.portAStart }
func (d *DMA) Destination() uint16 { return d.portBStart }
func (d *DMA) Length() uint16      { return d.blockLen }

// ByteCounter / CurrentA / CurrentB expose the chip's internal counters (the
// values the read mask returns): bytes transferred in the current operation and
// the live port A / port B pointers.
func (d *DMA) ByteCounter() uint16 { return d.counter }
func (d *DMA) CurrentA() uint16    { return d.curA }
func (d *DMA) CurrentB() uint16    { return d.curB }

// Duration returns the T-state cost of the most recent transfer: the per-byte
// cycle cost times the bytes moved. The emulator charges this to the CPU clock,
// because that is the time the CPU spent frozen. It does NOT include the
// per-block bus acquisition, which is derived but deliberately not charged; see
// busAcquisitionCycles and TestBusAcquisitionIsDerivedButNotCharged.
func (d *DMA) Duration() uint64 { return d.lastDuration }

// Mode returns the transfer mode (continuous or burst) from the last WR4 write.
func (d *DMA) Mode() byte { return d.mode }

// ReadCommand returns the next register value in the read sequence (an IO
// read of the DMA port), then advances the cursor exactly as the FPGA's read
// FSM does. The read mask selects which of the seven registers participate;
// with an empty mask every read returns the status byte (the FSM's RD_STATUS
// fallback) — never a floating value.
func (d *DMA) ReadCommand() byte {
	v := d.regValue(d.readReg)
	d.readReg = d.nextReadReg(d.readReg)
	return v
}

// nextReadReg mirrors the FPGA read FSM's next-state chains (dma.vhd RD_*):
// the first mask-selected register scanning forward from cur, wrapping past
// port B high back to status; status (0) when the mask selects nothing.
func (d *DMA) nextReadReg(cur int) int {
	for i := 1; i <= 7; i++ {
		reg := (cur + i) % 7
		if d.readMask&(1<<reg) != 0 {
			return reg
		}
	}
	return 0
}

// firstReadReg is the sequence-start register applied by a new read mask or
// $A7 (initiate read sequence): the lowest mask-selected register, or status
// when the mask is empty (dma.vhd R6_BYTE_0 / the $A7 handler).
func (d *DMA) firstReadReg() int {
	for reg := 0; reg < 7; reg++ {
		if d.readMask&(1<<reg) != 0 {
			return reg
		}
	}
	return 0
}

// regValue returns the current value of read-mask register reg:
// 0=status, 1/2=byte counter lo/hi, 3/4=port A addr lo/hi, 5/6=port B addr lo/hi.
func (d *DMA) regValue(reg int) byte {
	switch reg {
	case 1:
		return byte(d.counter)
	case 2:
		return byte(d.counter >> 8)
	case 3:
		return byte(d.curA)
	case 4:
		return byte(d.curA >> 8)
	case 5:
		return byte(d.curB)
	case 6:
		return byte(d.curB >> 8)
	default: // 0 = status byte
		return d.statusByte()
	}
}

// statusByte builds the read-mask status register exactly as the FPGA does
// (dma.vhd:902): "00" & status_endofblock_n & "1101" & status_atleastone.
//
//	bits 7:6 = 00
//	bit 5    = status_endofblock_n (1 = not at end, 0 = block finished)
//	bits 4:1 = 1101 (fixed)
//	bit 0    = status_atleastone, raised after the block's first byte is
//	           written (dma.vhd:412) and cleared again when the FSM returns to
//	           IDLE (dma.vhd:265). A burst interleaved with the CPU is exactly
//	           the case a driver can poll it in.
func (d *DMA) statusByte() byte {
	const fixed = 0x1A // bits 4:1 = 1101, bits 5/0 = 0
	s := byte(fixed)
	if !d.endOfBlock { // status_endofblock_n = 1
		s |= 0x20
	}
	if d.atLeastOne {
		s |= 0x01
	}
	return s
}
