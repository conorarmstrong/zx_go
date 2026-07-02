package z80

import (
	"log"
	"sync/atomic"
	"time"
)

// Flag bit positions
const (
	FLAG_C  = 0x01 // Carry flag
	FLAG_N  = 0x02 // Add/Subtract flag
	FLAG_PV = 0x04 // Parity/Overflow flag
	FLAG_F3 = 0x08 // Undocumented flag 3
	FLAG_H  = 0x10 // Half-carry flag
	FLAG_F5 = 0x20 // Undocumented flag 5
	FLAG_Z  = 0x40 // Zero flag
	FLAG_S  = 0x80 // Sign flag
)

// Variant selects the CPU dispatch flavour. VariantZ80 is the
// standard Zilog Z80 with the existing zx_go undocumented-opcode
// behaviour. VariantZ80N is the ZX Spectrum Next's extended Z80 with
// the ~30 extra ED-prefixed opcodes; dispatch into those opcodes is
// gated on Variant==VariantZ80N so non-Next models stay bit-identical
// to today.
type Variant int

const (
	VariantZ80 Variant = iota
	VariantZ80N
)

// NextRegSink is the integration point for the Spectrum Next's
// NextReg register file from the CPU side. The Z80N NEXTREG opcodes
// (NEXTREG r,n and NEXTREG r,A) dispatch writes through this
// interface; reads from the NextReg file go via port 0x253B and are
// handled by the ULA's port dispatcher, not here. On non-Next models
// the field on CPU is nil and callers must nil-check before
// dispatching.
type NextRegSink interface {
	WriteReg(reg, val byte)
}

// CPU represents the Z80 processor.
type CPU struct {
	// 8-bit registers
	A, F, B, C, D, E, H, L byte
	// Alternate registers
	A_, F_, B_, C_, D_, E_, H_, L_ byte
	// Index registers
	IX, IY uint16
	// Stack Pointer and Program Counter
	SP, PC uint16
	// Interrupt Vector and Memory Refresh registers
	I, R byte
	// Interrupt flip-flops
	IFF1, IFF2 bool
	// Interrupt mode
	IM byte
	// Halted state
	Halted bool
	// nextFrameBoundary is the next T-state value at which
	// StepInstruction asserts a frame-start ULA INT. Set on
	// reset to one frame's worth of T-states; advanced by one
	// frame each time StepInstruction crosses it. Unused by
	// ExecuteFrame (which asserts INT at its own boundaries).
	nextFrameBoundary uint64
	// HaltWakeOnInt: when true, an /INT pulse exits HALT even with
	// IFF1=0 (matching the Z80 datasheet — the IRQ is rejected but
	// the wake-up signal still works). Default false to preserve
	// the long-standing classic-Spectrum emulator convention that
	// HALT-with-IFF1=0 stays halted. ModelNext warm-boot sets this
	// true because NextZXOS uses DI; HALT as an idle pattern.
	HaltWakeOnInt bool

	// RefreshDuringHalt, when true, advances the R register on each HALT cycle
	// (faithful Z80 refresh behaviour). Off by default; the ZX80/ZX81 enable it
	// so /INT — derived from R bit 6 — fires while the display loop is halted.
	RefreshDuringHalt bool

	// BranchSource records the cause of the most-recent non-
	// sequential PC change for the debugger's history annotation.
	// Set by JP/JR/CALL/RET/RST/INT/NMI/RESET paths; sampled and
	// cleared by the M1 prefetch hook in pkg/debugger before the
	// next instruction is fetched. Encoded as the same enum
	// pkg/debugger.PCSource defines (low byte) — kept as plain
	// uint8 here so pkg/z80 doesn't gain a pkg/debugger import.
	BranchSource uint8
	// BranchFrom is the PC of the instruction that effected the
	// branch (or 0 for non-instruction sources like INT / RESET).
	BranchFrom uint16

	// IRQPending is the ULA INT-line latch. ExecuteFrame asserts
	// it at frame start; the M1-boundary sample point inside the
	// loop clears it on acknowledgement (IFF1=1 → take), and the
	// end-of-frame sweep clears it on rejection (IFF1=0 across
	// the whole frame). Atomic because the visual debugger
	// inspects it from a separate goroutine.
	IRQPending atomic.Bool

	// eiDelay defers IRQ acknowledgement by one instruction after
	// EI executes — matching the Z80 datasheet's documented
	// "interrupts are not accepted during the execution of the
	// instruction following EI" rule. Standard `EI; RET` and
	// `EI; HALT` idioms rely on this to avoid an immediate IRQ
	// re-acknowledgement on the RET / HALT. Set true by EI, then
	// cleared by the M1 sample point as soon as one further
	// instruction has retired.
	eiDelay bool

	// LineIntOffsetTstates is the frame-relative tstate count at
	// which to assert IRQPending a SECOND time within a frame, for
	// Spectrum Next line interrupts (NR$22/$23). 0 disables. The
	// owning subsystem (pkg/next/wire) recomputes this whenever
	// NR$22/$23/$07 change. lineIntFired tracks whether the pulse
	// has already been asserted within the current frame so we only
	// fire once per frame even though the loop runs millions of
	// iterations.
	LineIntOffsetTstates uint64
	lineIntFired         bool

	// FrameIntDisabled mirrors NR$22 bit 6 (when 1, the ULA frame
	// interrupt is suppressed). Updated by pkg/next/wire on NR$22
	// writes. When true, ExecuteFrame skips the frame-start
	// IRQPending.Store(true) — only line interrupts can fire.
	FrameIntDisabled bool

	// --- Spec-faithful frame-INT timing (timing.md §1a/§1c) ---
	// The FPGA asserts the maskable frame INT for a NARROW pulse at a
	// specific point in the ULA frame, not "held the whole frame".
	//
	//   IntAssertTstate  = frame-relative tstate (3.5 MHz units) at which
	//                      the frame INT pulse begins. Derived from the
	//                      machine-timing (hc,vc) assert coordinate
	//                      (zxula_timing.vhd:551, c_int_h/c_int_v).
	//   IntPulseTstates  = pulse width in tstates; the INT line withdraws
	//                      after this many tstates if the CPU hasn't yet
	//                      accepted it (32 CPU cycles 48K/+3, 36 128K/
	//                      Pentagon — zxnext.vhd:2014-2033). A `DI` that
	//                      covers the whole pulse therefore MISSES the INT.
	//
	// Both default to 0, which preserves the legacy "assert at frame
	// start, hold the whole frame" behaviour for classic models and any
	// caller that hasn't opted in. pkg/next/wire sets them for ModelNext.
	IntAssertTstate uint64
	IntPulseTstates uint64
	frameIntFired   bool // narrow-pulse: pulse raised this frame
	frameIntDeasct  bool // narrow-pulse: pulse withdrawn this frame (one-shot)

	// WZ is the Z80's internal "MEMPTR" register. It's updated by
	// most operations that compute a 16-bit memory address (LD with
	// (nn), LD A,(BC/DE), 16-bit arithmetic into HL, jumps, calls,
	// block I/O, etc.) and observable only via the undocumented F3
	// and F5 bits of BIT n,(HL) and CCF/SCF. Implemented just thoroughly
	// enough to pass zexall's bit-n-(HL) test; programs that don't
	// query F3/F5 won't notice if a particular update site is missing.
	WZ uint16

	// Variant selects between the standard Z80 dispatch table and
	// the Z80N (Spectrum Next) extended set. Defaults to VariantZ80.
	Variant Variant

	// NextRegs is the Next register-file sink. Non-nil only when
	// Variant==VariantZ80N and a Next bus is wired up. Z80N opcodes
	// that touch NextRegs (NEXTREG r,n / NEXTREG r,A) nil-check before
	// dispatching; the field is intentionally a public interface so
	// it can be wired by cmd/zx_go without exposing the concrete Next
	// bus type to pkg/z80.
	NextRegs NextRegSink

	// speedSelect is the NextReg 0x07 speed selector (low 2 bits):
	// 0 = 3.5 MHz, 1 = 7 MHz, 2 = 14 MHz, 3 = 28 MHz. Default 0.
	// Reads return whatever was last written. SpeedMultiplier derives
	// the scale factor from it, and ExecuteFrame uses that to size the
	// per-frame T-state budget.
	speedSelect byte

	// speedLocked pins speedSelect across guest NR$07 writes (diagnostic).
	speedLocked bool

	// retnHook fires after RETN/RETI — see SetRETNHook.
	retnHook func()

	// Memory and ULA interfaces
	mem Memory
	ula ULA

	// contender, when non-nil, applies per-T-state ULA memory
	// contention at each memory access. Wired in New when the memory
	// backend implements ContendMemory (real Memory does; test mocks
	// omit it → nil → no contention, identical to the prior model).
	// Used by the m1/rd/wr/exec cycle helpers that converted opcodes
	// use in place of lump `c.tstates += N` accounting.
	contender memContender

	// MemContend gates per-access ULA memory contention. Default
	// false: the cycle helpers still advance T-states exactly as the
	// old lump accounting did, so machine behaviour is byte-identical
	// to the pre-contention model (matching the historically-deferred
	// memory-contention state — see memory.go contentionDelay). When
	// true, contended screen-RAM accesses ($4000-$7FFF) incur the
	// position-dependent ULA hold. The per-opcode contention tests
	// flip this on; enabling it machine-wide is gated on the turbo-
	// speed contention-magnitude work the memory pkg still defers.
	MemContend bool

	// T-state counter for timing
	tstates uint64

	// instructionCount is a free-running tally of opcode fetches
	// (M1 cycles), incremented in fetch() and NMI() alongside the
	// hardware R register. Unlike R it doesn't wrap, so RZX playback
	// can use it as a per-frame instruction counter without needing
	// the offset-tracking trick FUSE uses (z80.h:43).
	instructionCount uint64

	// IM2 interrupt vector low byte (0xFF on ZX Spectrum)
	IM2Vector byte

	// Breakpoint callback: called before each instruction during ExecuteFrame.
	// If it returns true, execution stops immediately (breakpoint hit).
	BreakpointCheck func(pc uint16) bool

	// TrapCheck is called before each instruction during ExecuteFrame.
	// If it returns true, the trap has handled the instruction (typically by
	// modifying CPU state and PC) and the normal fetch/execute cycle is
	// skipped. Used to short-circuit known ROM routines such as the 48K
	// LD-BYTES tape loader at 0x0556.
	TrapCheck func(pc uint16) bool

	// PreFetchHook fires before every opcode fetch with the current
	// PC. The fetched byte will come from whatever ROM/RAM the
	// memory map currently selects at PC, so this hook can change
	// that mapping (e.g. page in a peripheral ROM) and have the
	// effect take hold for the very next byte read. Used by
	// Interface 1 to page in the shadow ROM at 0x0008 / 0x1708.
	//
	// This is the legacy single-hook slot, retained for callers
	// (Interface 1, Disciple) that haven't migrated. New consumers
	// — divMMC in the Spectrum Next, Multiface 128 — should use
	// AddPreFetchHook by name instead, which permits multiple
	// peripherals to coexist.
	PreFetchHook func(pc uint16)

	// PostFetchHook fires after every opcode fetch but before the
	// instruction is dispatched. The pc parameter is the address
	// the opcode came from (i.e. the value of PC before fetch
	// incremented it). Used by Interface 1 to page out the shadow
	// ROM at 0x0700 — the byte fetched from the IF1 ROM still gets
	// executed, but subsequent fetches come from the host ROM.
	//
	// Same migration note as PreFetchHook: new consumers should
	// use AddPostFetchHook.
	PostFetchHook func(pc uint16)

	// M1FetchHook, if non-nil, fires after each M1 opcode fetch with the
	// fetch address and the byte read; the byte it returns is the opcode the
	// CPU actually executes. The ZX80/ZX81 use this to implement the
	// CPU-generated display: a fetch from $8000+ whose byte has bit 6 clear is
	// latched as a display character and replaced with NOP ($00), exactly as
	// the real ULA forces a NOP onto the bus during video generation.
	M1FetchHook func(pc uint16, opcode byte) byte

	// Named fetch-hook registries. Hooks fire in registration order
	// AFTER the legacy single-hook slots above. BreakpointCheck
	// pre-empts both: a breakpoint hit short-circuits all hooks and
	// the instruction itself.
	preFetchHooks  []fetchHook
	postFetchHooks []fetchHook

	// currentInstrPC is the PC of the instruction currently being
	// dispatched (captured before fetch). Used by OnSPLoad to report
	// the executing instruction's address rather than the post-fetch
	// PC. Plain field — only read inside the same goroutine that
	// drives executeInstruction.
	currentInstrPC uint16

	// OnSPLoad, if set, is called by the five explicit SP-set opcodes
	// (LD SP,nn / LD SP,HL / LD SP,(nn) / LD SP,IX / LD SP,IY) with
	// the PC of the executing instruction, the pre-write SP and the
	// post-write SP. Debug-only instrumentation for tracking stack
	// rebases — PUSH/POP/CALL/RET adjustments do NOT fire this hook.
	OnSPLoad func(pc, oldSP, newSP uint16)

	// PendingNMI is set from any goroutine to signal an NMI. The CPU
	// processes it at the next safe point in ExecuteFrame.
	PendingNMI atomic.Bool

	// NMICallback is called just before the NMI is executed, allowing
	// peripherals to page in their ROM at the exact right moment.
	NMICallback func()

	// NMIStackless models the ZX Spectrum Next "stackless NMI" (NextReg
	// $C0 bit 3): when true, NMI acceptance ALSO writes the return address
	// to NR$C2 (LSB)/NR$C3 (MSB) via StacklessWriteNR, and RETN takes its
	// PC from NR$C2/$C3 (read via StacklessReadNR) instead of the popped
	// stack value (the stack push/pop still happens so SP stays correct).
	// NextZXOS launches 128 BASIC this way: the MF-NMI handler rewrites
	// NR$C2/$C3 to the editor entry so RETN jumps there. The callbacks are
	// generic (reg,val) so pkg/z80 needn't depend on the NextReg package;
	// they are wired to the dispatcher in pkg/next. Matches a faithful
	// Z80N core (the NMI path writes $C2/$C3; RETN reads them back).
	NMIStackless     bool
	StacklessReadNR  func(reg byte) byte
	StacklessWriteNR func(reg, val byte)

	// For debugging
	logEnabled bool

	// Pre-computed lookup tables for flag calculation
	sz53Table         [256]byte // Sign, Zero, and undocumented flags table
	parityTable       [256]byte // Parity table
	halfcarryAddTable [8]byte   // Half-carry flag for ADD operations
	halfcarrySubTable [8]byte   // Half-carry flag for SUB operations
	overflowAddTable  [8]byte   // Overflow flag for ADD operations
	overflowSubTable  [8]byte   // Overflow flag for SUB operations
}

// ULA is an interface that the CPU uses to interact with the ULA.
type ULA interface {
	ReadPort(addr uint16) (byte, bool)
	WritePort(addr uint16, val byte)
}

// Memory is the interface the CPU uses to talk to the address space.
// Z80 memory accesses don't fail — every 16-bit address is valid — so
// Read/Write don't return errors. ContendPort applies the
// model-specific I/O contention timing for the given port address.
type Memory interface {
	Read(addr uint16) byte
	Write(addr uint16, val byte)
	ContendPort(port uint16)
}

// contentionWirer is an optional interface implemented by memory
// backends that participate in cycle-accurate contention timing. The
// CPU passes them a pointer to its T-state counter so reads/writes can
// add contention stalls relative to the current cycle. Memory
// implementations that don't model contention (e.g. test mocks) can
// simply omit this method.
type contentionWirer interface {
	SetTStatePtr(p *uint64)
}

// New creates a new Z80 CPU instance.
func New(mem Memory, ula ULA) *CPU {
	c := &CPU{
		mem: mem,
		ula: ula,
	}
	c.initTables()
	c.Reset()
	// Enable contention by sharing T-state counter with memory, if the
	// memory backend supports it.
	if cw, ok := mem.(contentionWirer); ok {
		cw.SetTStatePtr(&c.tstates)
	}
	if mc, ok := mem.(memContender); ok {
		c.contender = mc
	}
	return c
}

// memContender is the optional interface a memory backend implements
// to participate in per-access ULA memory contention. Mirrors
// contentionWirer's opt-in pattern: backends that don't model
// contention simply omit ContendMemory and the cycle helpers below
// treat every access as uncontended.
type memContender interface {
	ContendMemory(addr uint16)
}

// --- FUSE-style per-cycle timing helpers ---
//
// These advance the T-state counter ONE machine cycle at a time and
// apply ULA memory contention at the cycle's address before charging
// the cycle's duration — the faithful "contend then advance" model
// (Sean Young §contention / FUSE z80.c contend_read/contend_write).
//
// An opcode is "converted" to faithful per-opcode memory contention
// by replacing its lump `c.tstates += TOTAL` with the exact sequence
// of m1/rd/wr/exec calls that sum to TOTAL. When contention is 0
// (uncontended address, or outside the display window, or no
// contender wired) the sequence charges exactly TOTAL — identical to
// the prior accounting — so unconverted opcodes and tstate-0 timing
// tests are unaffected. Converted and unconverted opcodes coexist
// because each opcode's timing is self-contained.

// contendAt applies ULA memory contention for an access to addr at
// the current T-state position (no-op when no contender is wired).
func (c *CPU) contendAt(addr uint16) {
	if c.MemContend && c.contender != nil {
		c.contender.ContendMemory(addr)
	}
}

// m1 models the opcode-fetch machine cycle (4 T): contend the fetch
// address, then charge 4 T. The actual byte fetch + R-increment is
// done separately by fetch(); m1 only accounts the cycle's timing.
func (c *CPU) m1(addr uint16) {
	c.contendAt(addr)
	c.tstates += 4
}

// rd models a memory-read machine cycle (3 T): contend then read.
func (c *CPU) rd(addr uint16) byte {
	c.contendAt(addr)
	c.tstates += 3
	return c.mem.Read(addr)
}

// wr models a memory-write machine cycle (3 T): contend then write.
func (c *CPU) wr(addr uint16, val byte) {
	c.contendAt(addr)
	c.tstates += 3
	c.mem.Write(addr, val)
}

// exec models internal (no-MREQ) cycles: the ULA still holds the bus
// once per T-state for the value currently on the address bus, so
// contention is applied n times against addr. Used for the internal
// cycles of opcodes like INC (HL) (1 extra) and ADD HL,rr (7 extra).
func (c *CPU) exec(addr uint16, n uint64) {
	for i := uint64(0); i < n; i++ {
		c.contendAt(addr)
		c.tstates++
	}
}

// rdAddr fetches a 16-bit little-endian operand at PC through two
// contended read cycles (lo then hi), advancing PC by 2.
func (c *CPU) rdAddr() uint16 {
	lo := uint16(c.rd(c.PC))
	c.PC++
	hi := uint16(c.rd(c.PC))
	c.PC++
	return (hi << 8) | lo
}

// ir returns the I:R register pair value — the address the Z80 places
// on the bus during internal (no-MREQ) machine cycles, i.e. the value
// the ULA contends against for those cycles (FUSE z80.c).
func (c *CPU) ir() uint16 { return uint16(c.I)<<8 | uint16(c.R) }

// pushC pushes a 16-bit value through contended write cycles (hi byte
// first at SP-1, then lo at SP-2). Used by converted stack opcodes;
// plain push() is retained for the interrupt/NMI paths (outside the
// instruction-level timing scope).
func (c *CPU) pushC(val uint16) {
	c.SP--
	c.wr(c.SP, byte(val>>8))
	c.SP--
	c.wr(c.SP, byte(val))
}

// popC pops a 16-bit value through contended read cycles (lo at SP,
// hi at SP+1).
func (c *CPU) popC() uint16 {
	lo := uint16(c.rd(c.SP))
	c.SP++
	hi := uint16(c.rd(c.SP))
	c.SP++
	return (hi << 8) | lo
}

// Reset resets the CPU to its initial state.
func (c *CPU) Reset() {
	c.A, c.F, c.B, c.C, c.D, c.E, c.H, c.L = 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF
	c.A_, c.F_, c.B_, c.C_, c.D_, c.E_, c.H_, c.L_ = 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF
	c.IX, c.IY = 0xFFFF, 0xFFFF
	c.BranchSource = 8 // SourceReset — for history attribution
	c.BranchFrom = c.PC
	c.SP, c.PC = 0xFFFF, 0x0000
	c.I, c.R = 0, 0
	c.IFF1, c.IFF2 = false, false
	c.IM = 0
	c.Halted = false
	c.tstates = 0
	c.IM2Vector = 0xFF // ZX Spectrum ULA puts 0xFF on data bus during INTA
}

// SoftReset models the Spectrum Next's NextReg $02 bit 0 soft
// reset — a software-controlled CPU restart that is distinct
// from a hardware /RESET. The Next preserves nearly all CPU
// state across this reset; only PC is forced back to $0000.
//
// Specifically preserved across NextReg $02 soft reset:
//   - All general-purpose registers (A, F, BC, DE, HL, IX, IY)
//   - The alternate register set (A', F', BC', DE', HL')
//   - SP (the stack and stack pointer survive)
//   - I, R (interrupt vector base + refresh)
//   - IFF1, IFF2 (interrupt enable / NMI shadow)
//   - IM (interrupt mode)
//   - HALT (if HALTed, the soft reset un-halts via the new PC)
//   - IM2Vector
//
// This preservation is what NEXTBASIC's bank-3 $3BE8 trampoline
// relies on:
//   - The post-reset divMMC ROM init at $1FF9 uses
//     LD D,(IX+$1F) — needs IX intact.
//   - The 50 Hz IM 1 timer interrupt must keep firing after
//     soft-reset — needs IM=1 and IFF1=true preserved.
//   - The pre-reset return stack remains live — needs SP
//     intact.
//
// Use SoftReset for the NextReg $02 path. Reset() retains its
// "clobber everything to $FFFF, IM=0, IFF=0" behaviour for a
// full power-on / hard reset.
func (c *CPU) SoftReset() {
	// Z80 /RESET-faithful: PC, I, R, IFF1, IFF2, IM, and the HALT
	// state are cleared. SP, IX, IY, the GP register file and the
	// shadow set are unspecified by the datasheet; we leave them
	// intact so post-reset code that relies on retained values (e.g.
	// NEXTBASIC's bank-3 trampoline reads `(IX+$1F)` right after a
	// soft reset) sees what it expects.
	c.BranchSource = 8 // SourceReset — for the debugger's history
	c.BranchFrom = c.PC
	c.PC = 0x0000
	c.I = 0
	c.R = 0
	c.IFF1 = false
	c.IFF2 = false
	c.IM = 0
	c.Halted = false
}

// Run starts a standalone emulation loop at 50Hz.
// Note: the main application uses its own run loop instead of this method.
func (c *CPU) Run() {
	ticker := time.NewTicker(20 * time.Millisecond) // 50Hz
	defer ticker.Stop()

	for range ticker.C {
		c.ExecuteFrame(69888) // 48K: 69888, 128K+: 70908
	}
}

// InstructionCount returns the number of opcode fetches the CPU has
// performed since boot or the last ResetInstructionCount call. RZX
// playback uses this to time frame boundaries from the per-frame
// instruction counts in the recorded file. The value is the same metric
// libspectrum exposes via R + rzx_instructions_offset, but kept in a
// uint64 so it never wraps.
func (c *CPU) InstructionCount() uint64 {
	return c.instructionCount
}

// SetInstructionCount overwrites the free-running instruction
// counter. State-restore tools (the time-travel rewind) use this so
// a rewound machine's insn-gated diagnostics and subsequent
// snapshots stay on a consistent timeline; normal emulation never
// calls it.
func (c *CPU) SetInstructionCount(n uint64) {
	c.instructionCount = n
}

// Tstates returns the absolute T-state counter. Used by RZX recording
// to seed input recording blocks with the T-state offset at which the
// recording window opened (matches FUSE's libspectrum_rzx_start_input
// at libspectrum/rzx.c:268).
func (c *CPU) Tstates() uint64 {
	return c.tstates
}

// SetTstates overrides the T-state counter. Used by RZX playback to
// align timing with the recorded T-state offset before resuming the
// CPU from a snapshot.
func (c *CPU) SetTstates(t uint64) {
	c.tstates = t
}

// HL, BC, DE: 16-bit register-pair accessors. Exported convenience
// wrappers for callers outside pkg/z80 — the Spectrum Next esxDOS
// handlers reach for them when extracting path pointers / lengths
// from guest registers.

// HL returns the HL register pair.
func (c *CPU) HL() uint16 { return uint16(c.H)<<8 | uint16(c.L) }

// BC returns the BC register pair.
func (c *CPU) BC() uint16 { return uint16(c.B)<<8 | uint16(c.C) }

// DE returns the DE register pair.
func (c *CPU) DE() uint16 { return uint16(c.D)<<8 | uint16(c.E) }

// SetHL writes the HL register pair.
func (c *CPU) SetHL(val uint16) { c.H = byte(val >> 8); c.L = byte(val) }

// SetBC writes the BC register pair.
func (c *CPU) SetBC(val uint16) { c.B = byte(val >> 8); c.C = byte(val) }

// SetDE writes the DE register pair.
func (c *CPU) SetDE(val uint16) { c.D = byte(val >> 8); c.E = byte(val) }

// SpeedMultiplier returns how much faster than the 3.5 MHz reference
// the CPU is currently running. Always 1 on non-Next models. On
// ModelNext, mirrors the NextReg 0x07 speed selector: 0 -> 1,
// 1 -> 2, 2 -> 4, 3 -> 8. ExecuteFrame scales its per-frame T-state
// budget by this value, and fetch() adds the 28 MHz M1 wait state
// when the multiplier is 8.
func (c *CPU) SpeedMultiplier() int {
	switch c.speedSelect {
	case 1:
		return 2
	case 2:
		return 4
	case 3:
		return 8
	}
	return 1
}

// retnHook, when non-nil, fires after every RETN/RETI (all ED
// mirror encodings). The TBBlue T80N asserts I_RETN for BOTH
// instructions (t80n_mcode.vhd:660,2432) and zxnext.vhd routes it
// to the divMMC as i_retn_seen, clearing the automap latch — without
// this the overlay would stay latched across ISR exits where real
// hardware drops it.
//
// SetRETNHook installs it; pass nil to remove.
func (c *CPU) SetRETNHook(fn func()) { c.retnHook = fn }

// SetSpeedSelect installs the NextReg 0x07 speed selector. Only
// the low 2 bits are honoured; other bits are ignored. A no-op once
// the speed has been locked (see LockSpeedSelect) — used by the
// 128K-launch timing diagnostic to pin the rate across the guest's
// own NR$07 writes during the launch.
func (c *CPU) SetSpeedSelect(v byte) {
	if c.speedLocked {
		return
	}
	c.speedSelect = v & 0x03
}

// LockSpeedSelect pins the speed selector to v and ignores all
// subsequent SetSpeedSelect (guest NR$07) writes. Used by the timing
// diagnostic AND by the user-facing "CPU Speed" override, which lets a
// game be run at a fixed speed (e.g. forcing 3.5 MHz for a game that
// NextZXOS would otherwise run too fast at 28 MHz) — the emulator
// equivalent of the Next's menu speed selector / F8 speed hotkey.
func (c *CPU) LockSpeedSelect(v byte) {
	c.speedSelect = v & 0x03
	c.speedLocked = true
}

// UnlockSpeedSelect releases a LockSpeedSelect so guest NR$07 writes
// take effect again ("Auto" CPU speed). Leaves the current selector
// value in place until the next guest write.
func (c *CPU) UnlockSpeedSelect() { c.speedLocked = false }

// SpeedLocked reports whether the speed selector is pinned (LockSpeedSelect).
func (c *CPU) SpeedLocked() bool { return c.speedLocked }

// SpeedSelect returns the currently-installed NextReg 0x07 speed
// selector (0..3). Useful for snapshot save and for the dispatcher
// to read back what was written.
func (c *CPU) SpeedSelect() byte { return c.speedSelect }

// ResetInstructionCount zeros the instruction counter. RZX playback
// calls this at the start of each frame so the per-frame target from
// the recording can be compared directly against InstructionCount.
func (c *CPU) ResetInstructionCount() {
	c.instructionCount = 0
}

// ExecuteRZXFrame runs the CPU until it has executed exactly
// `instructions` opcode fetches relative to the count at entry, then
// fires an end-of-frame interrupt if interrupts are enabled. This
// replaces ExecuteFrame for RZX playback: the per-frame instruction
// count is pulled from the recorded file rather than from a fixed
// T-states-per-frame budget. Traps must be disabled by the caller for
// the duration of playback — short-circuiting ROM routines would skip
// instruction-count increments and desync the recording.
func (c *CPU) ExecuteRZXFrame(instructions uint64) {
	target := c.instructionCount + instructions
	for c.instructionCount < target {
		if c.Halted {
			// While halted, the real Z80 keeps issuing M1
			// cycles for the HALT opcode (FUSE models this
			// with the PC--/PC++ trick at
			// opcodes_base.c:523). Each iteration counts as
			// one instruction for RZX timing.
			if c.PendingNMI.CompareAndSwap(true, false) {
				if c.NMICallback != nil {
					c.NMICallback()
				}
				c.NMI()
				continue
			}
			// Per Sean Young §5: while halted, the CPU keeps
			// running M1 cycles internally for the HALT opcode.
			// Each M1 bumps R. Without this software using R
			// as a PRNG (ZX 48K keyboard scan after a HALT)
			// sees stale entropy.
			c.R = (c.R & 0x80) | ((c.R + 1) & 0x7F)
			c.instructionCount++
			c.tstates += 4
			continue
		}
		if c.BreakpointCheck != nil && c.BreakpointCheck(c.PC) {
			return
		}
		// Traps intentionally NOT consulted here — see doc comment.
		c.executeInstruction()
		if c.PendingNMI.CompareAndSwap(true, false) {
			if c.NMICallback != nil {
				c.NMICallback()
			}
			c.NMI()
		}
	}

	// End-of-frame interrupt — same as ExecuteFrame.
	if c.IFF1 {
		c.interrupt()
	} else {
		IntRejectCount++
		LastRejectPC = c.PC
		LastRejectInsns = c.instructionCount
	}
}

// frameIntPulse models the narrow maskable frame-INT pulse (timing.md
// §1c) within the frame whose tstate origin is frameStart. The FPGA
// raises int_ula for a fixed-width pulse at a fixed point in the ULA
// frame: we raise IRQPending at IntAssertTstate (a *wall-clock* tstate,
// so it is scaled to CPU cycles by the turbo SpeedMultiplier) and
// withdraw it once, IntPulseTstates later (in CPU cycles — NOT speed-
// scaled, since the FPGA counts the pulse on the CPU clock,
// zxnext.vhd:2035). A DI that covers the whole window therefore MISSES
// the interrupt. No-op unless IntPulseTstates>0. frameIntFired /
// frameIntDeasct are the per-frame one-shots, reset at each frame origin
// by ExecuteFrame / StepInstructionWithIRQ.
func (c *CPU) frameIntPulse(frameStart uint64) {
	if c.IntPulseTstates == 0 || c.FrameIntDisabled {
		return
	}
	assertAt := frameStart + c.IntAssertTstate*uint64(c.SpeedMultiplier())
	switch {
	case !c.frameIntFired && c.tstates >= assertAt:
		c.IRQPending.Store(true)
		c.frameIntFired = true
	case c.frameIntFired && !c.frameIntDeasct && c.tstates >= assertAt+c.IntPulseTstates:
		// pulse window elapsed; interrupt() clears IRQPending on
		// acceptance, so a still-set latch here means it was missed.
		c.IRQPending.Store(false)
		c.frameIntDeasct = true
	}
}

func (c *CPU) ExecuteFrame(tstatesPerFrame int) {
	// On the Spectrum Next at 7 / 14 / 28 MHz the CPU executes
	// 2 / 4 / 8 times more T-states per ULA frame than at the
	// 3.5 MHz reference. SpeedMultiplier returns 1 on every other
	// model so this is a no-op there.
	budget := uint64(tstatesPerFrame) * uint64(c.SpeedMultiplier())
	tstatesEnd := c.tstates + budget

	// ULA INT line: asserted at the start of every ULA frame and
	// held until the CPU acknowledges or the next frame begins.
	//
	// On real hardware the INT pulse is narrow (~32 ULA T-states
	// after which the line floats; the FPGA `int_ula` signal at
	// zxnext.vhd:559 is a single 7 MHz cycle wide, latched
	// downstream). For an instruction-stepping emulator the
	// hardware-faithful approximation is "INT line is active
	// across the whole frame, withdrawn at the next frame
	// boundary." That keeps the IRQ available to a guest doing
	// `EI` mid-frame (NextZXOS init, for instance) without making
	// the line spurious across multiple frames — `EI; HALT`
	// programs take the IRQ at the next M1 boundary inside the
	// frame; `EI; …; DI` programs take it iff the DI hasn't run
	// by the next M1 sample point. Per-frame rejection counting
	// matches the documented Spectrum behaviour where each frame
	// fires exactly one INT pulse.
	// Frame-INT assertion (timing.md §1c). Legacy (IntPulseTstates==0):
	// assert at frame start, hold the whole frame — classic 48K/128K/+3
	// and any caller that has not opted in. Narrow pulse
	// (IntPulseTstates>0): raise/withdraw a fixed-width pulse via
	// frameIntPulse() inside the loop, so a DI covering the pulse misses
	// the INT (matches the FPGA, zxnext.vhd:2014-2033).
	frameStart := tstatesEnd - budget
	narrowPulse := c.IntPulseTstates > 0
	c.frameIntFired = false
	c.frameIntDeasct = false
	if !c.FrameIntDisabled && !narrowPulse {
		c.IRQPending.Store(true)
	}

	// Line-interrupt scheduling. NR$22/$23 select a frame-relative
	// scan-line to pulse INT a second time. Pre-compute the absolute
	// tstate target once per frame so the hot loop only does a single
	// uint64 compare. lineIntFired is the per-frame once-flag.
	var lineIntAt uint64
	if off := c.LineIntOffsetTstates; off > 0 {
		lineIntAt = frameStart + off
	}
	c.lineIntFired = false

	for c.tstates < tstatesEnd {
		// Narrow-pulse frame INT: raise at the pulse start, withdraw once
		// at the pulse end if un-accepted. Runs BEFORE the line-INT block
		// so a later line INT still re-raises the shared latch.
		if narrowPulse {
			c.frameIntPulse(frameStart)
		}
		// Line-interrupt assertion at the target scan-line tstate.
		// One pulse per frame; subsequent loop iterations skip via
		// lineIntFired. Asserts the same IRQPending latch the frame
		// interrupt uses — the CPU's M1 sample point handles both
		// identically.
		if lineIntAt != 0 && !c.lineIntFired && c.tstates >= lineIntAt {
			c.IRQPending.Store(true)
			c.lineIntFired = true
		}
		// INT sample point — equivalent to the Z80's M1-boundary
		// check on real hardware. Always evaluated; the HALT branch
		// below ALSO samples so a halted CPU exits halt on IRQ.
		// IRQ sample: respect the one-instruction EI delay. If the
		// previous instruction was EI, defer acknowledgement by
		// clearing the latch on this M1 boundary but skipping the
		// take. The next M1 sees eiDelay=false and may acknowledge.
		if c.IRQPending.Load() && c.IFF1 && !c.eiDelay {
			c.interrupt()
			c.IRQPending.Store(false)
		}
		c.eiDelay = false
		if c.Halted {
			// NMI during HALT: the CPU is waiting with IFF1=true (after EI).
			// This is the ideal time for NMI — IFF2 captures IFF1=true so
			// RETN can restore interrupts correctly.
			if c.PendingNMI.CompareAndSwap(true, false) {
				if c.NMICallback != nil {
					c.NMICallback()
				}
				c.NMI()
				continue
			}
			// /INT pulse during HALT with IFF1=0: per Z80 spec the
			// CPU exits HALT and resumes after the HALT instruction
			// (the IRQ is rejected but the line pulse wakes the CPU).
			// This branch is gated on c.HaltWakeOnInt so older
			// classic-Spectrum tests that assume "HALT only exits
			// on accepted IRQ" still pass; warm-boot on Spectrum Next
			// turns it on because NextZXOS uses DI; HALT as an idle
			// pattern that depends on the wake-on-INT behaviour.
			if c.HaltWakeOnInt && c.IRQPending.Load() {
				c.Halted = false
				c.IRQPending.Store(false)
				continue
			}
			c.tstates++
			continue
		}
		if c.BreakpointCheck != nil && c.BreakpointCheck(c.PC) {
			return
		}
		if c.TrapCheck != nil && c.TrapCheck(c.PC) {
			// Trap handled the "instruction" (typically a ROM routine);
			// skip the real fetch/execute and continue the loop.
			continue
		}
		c.executeInstruction()
		// NMI check after each instruction (NMI is non-maskable)
		if c.PendingNMI.CompareAndSwap(true, false) {
			if c.NMICallback != nil {
				c.NMICallback()
			}
			c.NMI()
		}
	}

	// Frame ended. If the pulse never resolved (window expired
	// without IFF1 going set OR no M1 boundary fell inside the
	// window — extremely short frame budgets in tests), count a
	// rejection and clear the latch so the next frame can start a
	// fresh pulse.
	if c.IRQPending.Load() {
		if c.IFF1 {
			c.interrupt()
		} else {
			IntRejectCount++
			LastRejectPC = c.PC
			LastRejectInsns = c.instructionCount
		}
		c.IRQPending.Store(false)
	}

	c.tstates -= tstatesEnd
}

func (c *CPU) executeInstruction() {
	if c.PreFetchHook != nil {
		c.PreFetchHook(c.PC)
	}
	// Iteration over preFetchHooks captures the slice header but
	// NOT a copy of the underlying array; if a hook calls
	// AddPreFetchHook / RemovePreFetchHook during its own
	// callback, subsequent iterations may skip or repeat hooks
	// because RemovePreFetchHook shifts elements down within the
	// shared backing storage. No current consumer mutates the
	// list during dispatch. If a future consumer needs to,
	// snapshot c.preFetchHooks into a local before the loop.
	for _, h := range c.preFetchHooks {
		h.fn(c.PC)
	}
	pc := c.PC
	c.currentInstrPC = pc
	opcode := c.fetch()
	if c.M1FetchHook != nil {
		opcode = c.M1FetchHook(pc, opcode)
	}
	if c.PostFetchHook != nil {
		c.PostFetchHook(pc)
	}
	for _, h := range c.postFetchHooks {
		h.fn(pc)
	}
	c.executeBaseInstruction(opcode)
	// Classify any non-sequential PC transition the instruction
	// just caused, for the debugger's history attribution. Done
	// post-execute so we see the actual new PC and decide based on
	// the leading opcode byte alone — branch class is determined
	// by the opcode shape, no need to disassemble. Heuristic — see
	// pkg/debugger.PCSource for the enum, mirrored here.
	c.classifyBranch(pc, opcode)
}

// classifyBranch sets c.BranchSource / c.BranchFrom based on the
// just-executed instruction's opcode if the PC took an unexpected
// path. The classifier is leading-byte only:
//
//   - $C3 = JP nn ; $C2/$CA/$D2/$DA/$E2/$EA/$F2/$FA = JP cc,nn
//   - $E9 = JP (HL) ; (also DD/FD E9 = JP IX/IY)
//   - $18 = JR e ; $20/$28/$30/$38 = JR cc,e ; $10 = DJNZ
//   - $CD = CALL nn ; $C4/$CC/$D4/$DC/$E4/$EC/$F4/$FC = CALL cc,nn
//   - $ED 8A = Z80N PUSH NN (treated as CALL-equivalent)
//   - $C9 = RET ; $C0/$C8/$D0/$D8/$E0/$E8/$F0/$F8 = RET cc
//   - $ED 4D = RETI ; $ED 45 = RETN
//   - $C7/$CF/$D7/$DF/$E7/$EF/$F7/$FF = RST n
//
// Conditional branches that didn't fire leave BranchSource alone;
// the M1 prefetch hook will read SourceSequential.
func (c *CPU) classifyBranch(prevPC uint16, opcode byte) {
	// If SoftReset / Reset / NMI / interrupt has already classified
	// this transition (they set BranchSource=6/7/8 before changing
	// PC), don't overwrite that with an opcode-based heuristic —
	// the instruction's "normal" PC delta will mislead the
	// classifier (e.g. NEXTREG \$02,\$01 triggers SoftReset → PC=\$0000
	// inside execute, and the ED-opcode branch would read that as
	// RET because PC != prevPC+4).
	if c.BranchSource >= 6 {
		return
	}
	// Cheap early-out: if the new PC is exactly the next sequential
	// address for this opcode, no branch happened. Skip the more
	// expensive classification.
	switch opcode {
	case 0xC3:
		c.BranchSource, c.BranchFrom = 1, prevPC // SourceJP
	case 0xC2, 0xCA, 0xD2, 0xDA, 0xE2, 0xEA, 0xF2, 0xFA:
		if c.PC != prevPC+3 {
			c.BranchSource, c.BranchFrom = 1, prevPC
		}
	case 0xE9:
		c.BranchSource, c.BranchFrom = 1, prevPC // JP (HL)
	case 0x18:
		c.BranchSource, c.BranchFrom = 2, prevPC // SourceJR
	case 0x20, 0x28, 0x30, 0x38, 0x10:
		if c.PC != prevPC+2 {
			c.BranchSource, c.BranchFrom = 2, prevPC
		}
	case 0xCD:
		c.BranchSource, c.BranchFrom = 3, prevPC // SourceCall
	case 0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC:
		if c.PC != prevPC+3 {
			c.BranchSource, c.BranchFrom = 3, prevPC
		}
	case 0xC9:
		c.BranchSource, c.BranchFrom = 4, prevPC // SourceRet
	case 0xC0, 0xC8, 0xD0, 0xD8, 0xE0, 0xE8, 0xF0, 0xF8:
		if c.PC != prevPC+1 {
			c.BranchSource, c.BranchFrom = 4, prevPC
		}
	case 0xC7, 0xCF, 0xD7, 0xDF, 0xE7, 0xEF, 0xF7, 0xFF:
		c.BranchSource, c.BranchFrom = 5, prevPC // SourceRst
	case 0xED:
		// RETI ($45), RETN ($45 family — 4D/55/5D/65/6D/75/7D in
		// undocumented form), Z80N PUSH NN (8A). Sub-byte at
		// prevPC+1 disambiguates, but reading memory here is
		// avoidable: post-execute PC delta is enough — if PC
		// jumped, classify as RET. Z80N PUSH NN doesn't change PC
		// so won't trigger.
		if c.PC != prevPC+2 && c.PC != prevPC+4 {
			c.BranchSource, c.BranchFrom = 4, prevPC
		}
	case 0xDD, 0xFD:
		// JP (IX) / JP (IY) sub-byte $E9. Same heuristic as above.
		if c.PC != prevPC+2 && c.PC != prevPC+3 && c.PC != prevPC+4 {
			c.BranchSource, c.BranchFrom = 1, prevPC
		}
	}
}

// fetchHook is one registered entry in the pre/post-fetch hook chain.
// Identified by name so callers can later remove or replace it.
type fetchHook struct {
	name string
	fn   func(pc uint16)
}

// AddPreFetchHook registers a callback fired on every M1 fetch
// after the legacy CPU.PreFetchHook field. If a hook with the same
// name is already registered, this REPLACES its function — useful
// when the same peripheral is re-enabled without an intervening
// remove. Pass nil to clear; equivalent to RemovePreFetchHook.
//
// Hooks are called in registration order. The breakpoint check
// runs first (in ExecuteFrame), so a breakpoint short-circuits the
// entire chain.
func (c *CPU) AddPreFetchHook(name string, fn func(pc uint16)) {
	if fn == nil {
		c.RemovePreFetchHook(name)
		return
	}
	for i := range c.preFetchHooks {
		if c.preFetchHooks[i].name == name {
			c.preFetchHooks[i].fn = fn
			return
		}
	}
	c.preFetchHooks = append(c.preFetchHooks, fetchHook{name: name, fn: fn})
}

// RemovePreFetchHook deregisters a pre-fetch hook by name. Safe to
// call for a name that was never registered.
func (c *CPU) RemovePreFetchHook(name string) {
	for i := range c.preFetchHooks {
		if c.preFetchHooks[i].name == name {
			c.preFetchHooks = append(c.preFetchHooks[:i], c.preFetchHooks[i+1:]...)
			return
		}
	}
}

// AddPostFetchHook is the post-fetch analogue of AddPreFetchHook.
func (c *CPU) AddPostFetchHook(name string, fn func(pc uint16)) {
	if fn == nil {
		c.RemovePostFetchHook(name)
		return
	}
	for i := range c.postFetchHooks {
		if c.postFetchHooks[i].name == name {
			c.postFetchHooks[i].fn = fn
			return
		}
	}
	c.postFetchHooks = append(c.postFetchHooks, fetchHook{name: name, fn: fn})
}

// RemovePostFetchHook deregisters a post-fetch hook by name.
func (c *CPU) RemovePostFetchHook(name string) {
	for i := range c.postFetchHooks {
		if c.postFetchHooks[i].name == name {
			c.postFetchHooks = append(c.postFetchHooks[:i], c.postFetchHooks[i+1:]...)
			return
		}
	}
}

// StepInstruction executes exactly one Z80 instruction without
// checking for interrupts. Used by conformance tests (Zexdoc /
// Zexall) and unit tests — no IRQ sampling, no frame-boundary
// tracking. The debugger uses StepInstructionWithIRQ instead so
// debugger-driven single-stepping produces the same
// per-instruction state evolution as bulk-frame execution.
func (c *CPU) StepInstruction() {
	if c.Halted {
		c.tstates += 4
		return
	}
	c.executeInstruction()
}

// stepFrameBudget is the per-frame T-state count used by
// StepInstructionWithIRQ to assert IRQPending at each frame
// boundary during debugger-driven single-stepping. Matches the
// 70908 ULA-frame-T-states the Spectrum Next runs at (≈ 50 Hz);
// the same value is what ExecuteFrame is normally called with
// from the headless loop.
const stepFrameBudget = uint64(70908)

// StepInstructionWithIRQ executes one Z80 instruction at the M1
// sample point — mirrors the per-iteration body of ExecuteFrame
// so that debugger-driven single-stepping produces the same
// per-instruction state evolution as bulk-frame execution.
//
// Includes:
//   - IRQ assertion at each frame boundary (every stepFrameBudget
//     T-states), so single-stepping across a frame boundary still
//     fires the ULA INT — without this, NextZXOS's IM 1 IRQ never
//     runs under stepping and lockstep-vs-other-emulator diffs
//     after ~10K instructions due to compounded IRQ-acceptance
//     divergence.
//   - IRQ acceptance check at the M1 boundary (= IRQPending &&
//     IFF1 && !eiDelay).
//   - HALT handling — including the HaltWakeOnInt branch — so an
//     emulator paused inside HALT exits the same way it would
//     under continuous frame execution.
//   - NMI delivery during HALT (= same as ExecuteFrame).
//
// Use this from the debugger's single-step path. Conformance tests
// (Zexdoc / Zexall) keep using the plain StepInstruction because
// they expect IRQs to stay off during the test.
func (c *CPU) StepInstructionWithIRQ() {
	narrowPulse := c.IntPulseTstates > 0
	// One ULA frame is stepFrameBudget T-states at the 3.5 MHz reference,
	// but T-states accumulate on the CPU clock — at 7/14/28 MHz the CPU
	// burns SpeedMultiplier× more T-states per ULA frame. Scale the frame
	// boundary by SpeedMultiplier so the maskable INT fires exactly once
	// per ULA frame (50 Hz) regardless of turbo, matching ExecuteFrame
	// (which scales its budget identically). Without this the single-step
	// path would fire the frame INT SpeedMultiplier× too often at turbo —
	// e.g. 8× at 28 MHz — spurious interrupts that derail boot.
	frameBudget := stepFrameBudget * uint64(c.SpeedMultiplier())
	// Frame-boundary IRQ assertion. Legacy: assert at the boundary, hold.
	// Narrow pulse: only reset the per-frame one-shots here; the pulse
	// itself is raised/withdrawn by frameIntPulse below (timing.md §1c).
	if c.tstates >= c.nextFrameBoundary {
		if !c.FrameIntDisabled && !narrowPulse {
			c.IRQPending.Store(true)
		}
		c.nextFrameBoundary = ((c.tstates / frameBudget) + 1) * frameBudget
		c.lineIntFired = false
		c.frameIntFired = false
		c.frameIntDeasct = false
	}
	if narrowPulse {
		c.frameIntPulse(c.nextFrameBoundary - frameBudget)
	}

	// Line interrupt (NR$22/$23 scheduled second INT)
	if c.LineIntOffsetTstates > 0 && !c.lineIntFired {
		frameStart := c.nextFrameBoundary - frameBudget
		if c.tstates >= frameStart+c.LineIntOffsetTstates {
			c.IRQPending.Store(true)
			c.lineIntFired = true
		}
	}

	// IRQ sample point at M1 boundary
	if c.IRQPending.Load() && c.IFF1 && !c.eiDelay {
		c.interrupt()
		c.IRQPending.Store(false)
	}
	c.eiDelay = false

	if c.Halted {
		if c.PendingNMI.CompareAndSwap(true, false) {
			if c.NMICallback != nil {
				c.NMICallback()
			}
			c.NMI()
			return
		}
		if c.HaltWakeOnInt && c.IRQPending.Load() {
			c.Halted = false
			c.IRQPending.Store(false)
			return
		}
		if c.RefreshDuringHalt {
			// A real Z80 keeps executing internal NOPs while halted, so the
			// refresh register R keeps counting. The ZX80/ZX81 derive /INT from
			// R bit 6, so this must advance during HALT for the display loop's
			// HALT-waits-for-INT to wake. Opt-in (default off) to leave classic
			// Spectrum behaviour unchanged.
			c.R = (c.R & 0x80) | ((c.R + 1) & 0x7f)
		}
		c.tstates += 4
		return
	}

	c.executeInstruction()

	if c.PendingNMI.CompareAndSwap(true, false) {
		if c.NMICallback != nil {
			c.NMICallback()
		}
		c.NMI()
	}
}

func (c *CPU) executeBaseInstruction(opcode byte) {
	switch opcode {
	// 8-bit load group
	case 0x40: // LD B,B
		c.tstates += 4
	case 0x41: // LD B,C
		c.B = c.C
		c.tstates += 4
	case 0x42: // LD B,D
		c.B = c.D
		c.tstates += 4
	case 0x43: // LD B,E
		c.B = c.E
		c.tstates += 4
	case 0x44: // LD B,H
		c.B = c.H
		c.tstates += 4
	case 0x45: // LD B,L
		c.B = c.L
		c.tstates += 4
	case 0x46: // LD B,(HL)
		c.B = c.mem.Read(c.hl())
		c.tstates += 7
	case 0x47: // LD B,A
		c.B = c.A
		c.tstates += 4
	case 0x48: // LD C,B
		c.C = c.B
		c.tstates += 4
	case 0x49: // LD C,C
		c.tstates += 4
	case 0x4A: // LD C,D
		c.C = c.D
		c.tstates += 4
	case 0x4B: // LD C,E
		c.C = c.E
		c.tstates += 4
	case 0x4C: // LD C,H
		c.C = c.H
		c.tstates += 4
	case 0x4D: // LD C,L
		c.C = c.L
		c.tstates += 4
	case 0x4E: // LD C,(HL)
		c.C = c.mem.Read(c.hl())
		c.tstates += 7
	case 0x4F: // LD C,A
		c.C = c.A
		c.tstates += 4
	case 0x50: // LD D,B
		c.D = c.B
		c.tstates += 4
	case 0x51: // LD D,C
		c.D = c.C
		c.tstates += 4
	case 0x52: // LD D,D
		c.tstates += 4
	case 0x53: // LD D,E
		c.D = c.E
		c.tstates += 4
	case 0x54: // LD D,H
		c.D = c.H
		c.tstates += 4
	case 0x55: // LD D,L
		c.D = c.L
		c.tstates += 4
	case 0x56: // LD D,(HL)
		c.D = c.mem.Read(c.hl())
		c.tstates += 7
	case 0x57: // LD D,A
		c.D = c.A
		c.tstates += 4
	case 0x58: // LD E,B
		c.E = c.B
		c.tstates += 4
	case 0x59: // LD E,C
		c.E = c.C
		c.tstates += 4
	case 0x5A: // LD E,D
		c.E = c.D
		c.tstates += 4
	case 0x5B: // LD E,E
		c.tstates += 4
	case 0x5C: // LD E,H
		c.E = c.H
		c.tstates += 4
	case 0x5D: // LD E,L
		c.E = c.L
		c.tstates += 4
	case 0x5E: // LD E,(HL)
		c.E = c.mem.Read(c.hl())
		c.tstates += 7
	case 0x5F: // LD E,A
		c.E = c.A
		c.tstates += 4
	case 0x60: // LD H,B
		c.H = c.B
		c.tstates += 4
	case 0x61: // LD H,C
		c.H = c.C
		c.tstates += 4
	case 0x62: // LD H,D
		c.H = c.D
		c.tstates += 4
	case 0x63: // LD H,E
		c.H = c.E
		c.tstates += 4
	case 0x64: // LD H,H
		c.tstates += 4
	case 0x65: // LD H,L
		c.H = c.L
		c.tstates += 4
	case 0x66: // LD H,(HL)
		c.H = c.mem.Read(c.hl())
		c.tstates += 7
	case 0x67: // LD H,A
		c.H = c.A
		c.tstates += 4
	case 0x68: // LD L,B
		c.L = c.B
		c.tstates += 4
	case 0x69: // LD L,C
		c.L = c.C
		c.tstates += 4
	case 0x6A: // LD L,D
		c.L = c.D
		c.tstates += 4
	case 0x6B: // LD L,E
		c.L = c.E
		c.tstates += 4
	case 0x6C: // LD L,H
		c.L = c.H
		c.tstates += 4
	case 0x6D: // LD L,L
		c.tstates += 4
	case 0x6E: // LD L,(HL)
		c.L = c.mem.Read(c.hl())
		c.tstates += 7
	case 0x6F: // LD L,A
		c.L = c.A
		c.tstates += 4
	case 0x70: // LD (HL),B
		c.m1(c.currentInstrPC)
		c.wr(c.hl(), c.B)
	case 0x71: // LD (HL),C
		c.m1(c.currentInstrPC)
		c.wr(c.hl(), c.C)
	case 0x72: // LD (HL),D
		c.m1(c.currentInstrPC)
		c.wr(c.hl(), c.D)
	case 0x73: // LD (HL),E
		c.m1(c.currentInstrPC)
		c.wr(c.hl(), c.E)
	case 0x74: // LD (HL),H
		c.m1(c.currentInstrPC)
		c.wr(c.hl(), c.H)
	case 0x75: // LD (HL),L
		c.m1(c.currentInstrPC)
		c.wr(c.hl(), c.L)
	case 0x76: // HALT
		c.Halted = true
		c.tstates += 4
	case 0x77: // LD (HL),A
		c.m1(c.currentInstrPC)
		c.wr(c.hl(), c.A)
	case 0x78: // LD A,B
		c.A = c.B
		c.tstates += 4
	case 0x79: // LD A,C
		c.A = c.C
		c.tstates += 4
	case 0x7A: // LD A,D
		c.A = c.D
		c.tstates += 4
	case 0x7B: // LD A,E
		c.A = c.E
		c.tstates += 4
	case 0x7C: // LD A,H
		c.A = c.H
		c.tstates += 4
	case 0x7D: // LD A,L
		c.A = c.L
		c.tstates += 4
	case 0x7E: // LD A,(HL)
		c.m1(c.currentInstrPC)
		c.A = c.rd(c.hl())
	case 0x7F: // LD A,A
		c.tstates += 4

	// 8-bit load immediate
	case 0x06: // LD B,n
		c.B = c.readOperand()
		c.tstates += 7
	case 0x07: // RLCA
		c.rlca()
		c.tstates += 4
	case 0x0E: // LD C,n
		c.C = c.readOperand()
		c.tstates += 7
	case 0x0F: // RRCA
		c.rrca()
		c.tstates += 4
	case 0x16: // LD D,n
		c.D = c.readOperand()
		c.tstates += 7
	case 0x17: // RLA
		c.rla()
		c.tstates += 4
	case 0x1E: // LD E,n
		c.E = c.readOperand()
		c.tstates += 7
	case 0x1F: // RRA
		c.rra()
		c.tstates += 4
	case 0x26: // LD H,n
		c.H = c.readOperand()
		c.tstates += 7
	case 0x2E: // LD L,n
		c.L = c.readOperand()
		c.tstates += 7
	case 0x36: // LD (HL),n
		c.m1(c.currentInstrPC)
		n := c.rd(c.PC)
		c.PC++
		c.wr(c.hl(), n)
	case 0x3E: // LD A,n
		c.A = c.readOperand()
		c.tstates += 7

	// 16-bit load immediate
	case 0x01: // LD BC,nn
		c.setBC(c.fetch16())
		c.tstates += 10
	case 0x11: // LD DE,nn
		c.setDE(c.fetch16())
		c.tstates += 10
	case 0x21: // LD HL,nn
		c.setHL(c.fetch16())
		c.tstates += 10
	case 0x31: // LD SP,nn
		oldSP := c.SP
		c.SP = c.fetch16()
		c.tstates += 10
		if c.OnSPLoad != nil {
			c.OnSPLoad(c.currentInstrPC, oldSP, c.SP)
		}

	// Memory load/store. MEMPTR (WZ) updates follow the Z80
	// convention: for "LD A,(rr)" set WZ = rr+1; for "LD (rr),A"
	// set WZ = (A << 8) | ((rr+1) & 0xFF). 16-bit loads use nn+1.
	case 0x02: // LD (BC),A
		c.m1(c.currentInstrPC)
		c.wr(c.bc(), c.A)
		c.WZ = (uint16(c.A) << 8) | ((c.bc() + 1) & 0xFF)
	case 0x0A: // LD A,(BC)
		c.m1(c.currentInstrPC)
		c.A = c.rd(c.bc())
		c.WZ = c.bc() + 1
	case 0x12: // LD (DE),A
		c.m1(c.currentInstrPC)
		c.wr(c.de(), c.A)
		c.WZ = (uint16(c.A) << 8) | ((c.de() + 1) & 0xFF)
	case 0x1A: // LD A,(DE)
		c.m1(c.currentInstrPC)
		c.A = c.rd(c.de())
		c.WZ = c.de() + 1
	case 0x22: // LD (nn),HL
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.wr(addr, c.L)
		c.wr(addr+1, c.H)
		c.WZ = addr + 1
	case 0x2A: // LD HL,(nn)
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.L = c.rd(addr)
		c.H = c.rd(addr + 1)
		c.WZ = addr + 1
	case 0x32: // LD (nn),A
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.wr(addr, c.A)
		c.WZ = (uint16(c.A) << 8) | ((addr + 1) & 0xFF)
	case 0x3A: // LD A,(nn)
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.A = c.rd(addr)
		c.WZ = addr + 1

	// Arithmetic
	case 0x80: // ADD A,B
		c.add(c.B)
		c.tstates += 4
	case 0x81: // ADD A,C
		c.add(c.C)
		c.tstates += 4
	case 0x82: // ADD A,D
		c.add(c.D)
		c.tstates += 4
	case 0x83: // ADD A,E
		c.add(c.E)
		c.tstates += 4
	case 0x84: // ADD A,H
		c.add(c.H)
		c.tstates += 4
	case 0x85: // ADD A,L
		c.add(c.L)
		c.tstates += 4
	case 0x86: // ADD A,(HL)
		c.m1(c.currentInstrPC)
		c.add(c.rd(c.hl()))
	case 0x87: // ADD A,A
		c.add(c.A)
		c.tstates += 4
	case 0xC6: // ADD A,n
		c.add(c.readOperand())
		c.tstates += 7

	case 0x88: // ADC A,B
		c.adc(c.B)
		c.tstates += 4
	case 0x89: // ADC A,C
		c.adc(c.C)
		c.tstates += 4
	case 0x8A: // ADC A,D
		c.adc(c.D)
		c.tstates += 4
	case 0x8B: // ADC A,E
		c.adc(c.E)
		c.tstates += 4
	case 0x8C: // ADC A,H
		c.adc(c.H)
		c.tstates += 4
	case 0x8D: // ADC A,L
		c.adc(c.L)
		c.tstates += 4
	case 0x8E: // ADC A,(HL)
		c.m1(c.currentInstrPC)
		c.adc(c.rd(c.hl()))
	case 0x8F: // ADC A,A
		c.adc(c.A)
		c.tstates += 4
	case 0xCE: // ADC A,n
		c.adc(c.readOperand())
		c.tstates += 7

	case 0x90: // SUB B
		c.sub(c.B)
		c.tstates += 4
	case 0x91: // SUB C
		c.sub(c.C)
		c.tstates += 4
	case 0x92: // SUB D
		c.sub(c.D)
		c.tstates += 4
	case 0x93: // SUB E
		c.sub(c.E)
		c.tstates += 4
	case 0x94: // SUB H
		c.sub(c.H)
		c.tstates += 4
	case 0x95: // SUB L
		c.sub(c.L)
		c.tstates += 4
	case 0x96: // SUB (HL)
		c.m1(c.currentInstrPC)
		c.sub(c.rd(c.hl()))
	case 0x97: // SUB A
		c.sub(c.A)
		c.tstates += 4
	case 0xD6: // SUB n
		c.sub(c.readOperand())
		c.tstates += 7

	case 0x98: // SBC A,B
		c.sbc(c.B)
		c.tstates += 4
	case 0x99: // SBC A,C
		c.sbc(c.C)
		c.tstates += 4
	case 0x9A: // SBC A,D
		c.sbc(c.D)
		c.tstates += 4
	case 0x9B: // SBC A,E
		c.sbc(c.E)
		c.tstates += 4
	case 0x9C: // SBC A,H
		c.sbc(c.H)
		c.tstates += 4
	case 0x9D: // SBC A,L
		c.sbc(c.L)
		c.tstates += 4
	case 0x9E: // SBC A,(HL)
		c.m1(c.currentInstrPC)
		c.sbc(c.rd(c.hl()))
	case 0x9F: // SBC A,A
		c.sbc(c.A)
		c.tstates += 4
	case 0xDE: // SBC A,n
		c.sbc(c.readOperand())
		c.tstates += 7

	// Logical
	case 0xA0: // AND B
		c.and(c.B)
		c.tstates += 4
	case 0xA1: // AND C
		c.and(c.C)
		c.tstates += 4
	case 0xA2: // AND D
		c.and(c.D)
		c.tstates += 4
	case 0xA3: // AND E
		c.and(c.E)
		c.tstates += 4
	case 0xA4: // AND H
		c.and(c.H)
		c.tstates += 4
	case 0xA5: // AND L
		c.and(c.L)
		c.tstates += 4
	case 0xA6: // AND (HL)
		c.m1(c.currentInstrPC)
		c.and(c.rd(c.hl()))
	case 0xA7: // AND A
		c.and(c.A)
		c.tstates += 4
	case 0xE6: // AND n
		c.and(c.readOperand())
		c.tstates += 7

	case 0xA8: // XOR B
		c.xor(c.B)
		c.tstates += 4
	case 0xA9: // XOR C
		c.xor(c.C)
		c.tstates += 4
	case 0xAA: // XOR D
		c.xor(c.D)
		c.tstates += 4
	case 0xAB: // XOR E
		c.xor(c.E)
		c.tstates += 4
	case 0xAC: // XOR H
		c.xor(c.H)
		c.tstates += 4
	case 0xAD: // XOR L
		c.xor(c.L)
		c.tstates += 4
	case 0xAE: // XOR (HL)
		c.m1(c.currentInstrPC)
		c.xor(c.rd(c.hl()))
	case 0xAF: // XOR A
		c.xor(c.A)
		c.tstates += 4
	case 0xEE: // XOR n
		c.xor(c.readOperand())
		c.tstates += 7

	case 0xB0: // OR B
		c.or(c.B)
		c.tstates += 4
	case 0xB1: // OR C
		c.or(c.C)
		c.tstates += 4
	case 0xB2: // OR D
		c.or(c.D)
		c.tstates += 4
	case 0xB3: // OR E
		c.or(c.E)
		c.tstates += 4
	case 0xB4: // OR H
		c.or(c.H)
		c.tstates += 4
	case 0xB5: // OR L
		c.or(c.L)
		c.tstates += 4
	case 0xB6: // OR (HL)
		c.m1(c.currentInstrPC)
		c.or(c.rd(c.hl()))
	case 0xB7: // OR A
		c.or(c.A)
		c.tstates += 4
	case 0xF6: // OR n
		c.or(c.readOperand())
		c.tstates += 7

	case 0xB8: // CP B
		c.cp(c.B)
		c.tstates += 4
	case 0xB9: // CP C
		c.cp(c.C)
		c.tstates += 4
	case 0xBA: // CP D
		c.cp(c.D)
		c.tstates += 4
	case 0xBB: // CP E
		c.cp(c.E)
		c.tstates += 4
	case 0xBC: // CP H
		c.cp(c.H)
		c.tstates += 4
	case 0xBD: // CP L
		c.cp(c.L)
		c.tstates += 4
	case 0xBE: // CP (HL)
		c.m1(c.currentInstrPC)
		c.cp(c.rd(c.hl()))
	case 0xBF: // CP A
		c.cp(c.A)
		c.tstates += 4
	case 0xFE: // CP n
		c.cp(c.readOperand())
		c.tstates += 7

	// Inc/Dec
	case 0x03: // INC BC
		c.setBC(c.bc() + 1)
		c.tstates += 6
	case 0x04: // INC B
		c.B = c.inc(c.B)
		c.tstates += 4
	case 0x05: // DEC B
		c.B = c.dec(c.B)
		c.tstates += 4
	case 0x0B: // DEC BC
		c.setBC(c.bc() - 1)
		c.tstates += 6
	case 0x0C: // INC C
		c.C = c.inc(c.C)
		c.tstates += 4
	case 0x0D: // DEC C
		c.C = c.dec(c.C)
		c.tstates += 4
	case 0x13: // INC DE
		c.setDE(c.de() + 1)
		c.tstates += 6
	case 0x14: // INC D
		c.D = c.inc(c.D)
		c.tstates += 4
	case 0x15: // DEC D
		c.D = c.dec(c.D)
		c.tstates += 4
	case 0x1B: // DEC DE
		c.setDE(c.de() - 1)
		c.tstates += 6
	case 0x1C: // INC E
		c.E = c.inc(c.E)
		c.tstates += 4
	case 0x1D: // DEC E
		c.E = c.dec(c.E)
		c.tstates += 4
	case 0x23: // INC HL
		c.setHL(c.hl() + 1)
		c.tstates += 6
	case 0x24: // INC H
		c.H = c.inc(c.H)
		c.tstates += 4
	case 0x25: // DEC H
		c.H = c.dec(c.H)
		c.tstates += 4
	case 0x2B: // DEC HL
		c.setHL(c.hl() - 1)
		c.tstates += 6
	case 0x2C: // INC L
		c.L = c.inc(c.L)
		c.tstates += 4
	case 0x2D: // DEC L
		c.L = c.dec(c.L)
		c.tstates += 4
	case 0x33: // INC SP
		c.SP++
		c.tstates += 6
	case 0x34: // INC (HL)
		c.m1(c.currentInstrPC)
		addr := c.hl()
		v := c.inc(c.rd(addr))
		c.exec(addr, 1) // 1 internal cycle (M2 = 4T total on read M-cycle)
		c.wr(addr, v)
	case 0x35: // DEC (HL)
		c.m1(c.currentInstrPC)
		addr := c.hl()
		v := c.dec(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x3B: // DEC SP
		c.SP--
		c.tstates += 6
	case 0x3C: // INC A
		c.A = c.inc(c.A)
		c.tstates += 4
	case 0x3D: // DEC A
		c.A = c.dec(c.A)
		c.tstates += 4

	// 16-bit arithmetic
	case 0x09: // ADD HL,BC
		c.setHL(c.add16(c.hl(), c.bc()))
		c.tstates += 11
	case 0x19: // ADD HL,DE
		c.setHL(c.add16(c.hl(), c.de()))
		c.tstates += 11
	case 0x29: // ADD HL,HL
		c.setHL(c.add16(c.hl(), c.hl()))
		c.tstates += 11
	case 0x39: // ADD HL,SP
		c.setHL(c.add16(c.hl(), c.SP))
		c.tstates += 11

	// Jumps
	case 0x18: // JR n
		offset := int8(c.readOperand())
		c.PC = uint16(int32(c.PC) + int32(offset))
		c.WZ = c.PC
		c.tstates += 12
	case 0x20: // JR NZ,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_Z) == 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.WZ = c.PC
			c.tstates += 12
		} else {
			c.tstates += 7
		}
	case 0x28: // JR Z,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_Z) != 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.WZ = c.PC
			c.tstates += 12
		} else {
			c.tstates += 7
		}
	case 0x30: // JR NC,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_C) == 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.WZ = c.PC
			c.tstates += 12
		} else {
			c.tstates += 7
		}
	case 0x38: // JR C,n
		offset := int8(c.readOperand())
		if (c.F & FLAG_C) != 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.WZ = c.PC
			c.tstates += 12
		} else {
			c.tstates += 7
		}

	case 0xC2: // JP NZ,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_Z) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xC3: // JP nn
		addr := c.fetch16()
		c.WZ = addr
		c.PC = addr
		c.tstates += 10
	case 0xCA: // JP Z,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_Z) != 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xD2: // JP NC,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_C) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xDA: // JP C,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_C) != 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xE2: // JP PO,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_PV) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xE9: // JP (HL)
		c.PC = c.hl()
		c.tstates += 4
	case 0xEA: // JP PE,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_PV) != 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xF2: // JP P,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_S) == 0 {
			c.PC = addr
		}
		c.tstates += 10
	case 0xFA: // JP M,nn
		addr := c.fetch16()
		c.WZ = addr
		if (c.F & FLAG_S) != 0 {
			c.PC = addr
		}
		c.tstates += 10

	// Calls and returns
	case 0xC4: // CALL NZ,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_Z) == 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xC9: // RET
		c.m1(c.currentInstrPC)
		c.PC = c.popC()
		c.WZ = c.PC
	case 0xCC: // CALL Z,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_Z) != 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xCD: // CALL nn
		c.m1(c.currentInstrPC)
		lo := uint16(c.rd(c.PC))
		c.PC++
		hi := uint16(c.rd(c.PC))
		c.PC++
		addr := (hi << 8) | lo
		c.WZ = addr
		c.exec(c.ir(), 1) // internal cycle before the push
		c.pushC(c.PC)
		c.PC = addr
	case 0xD4: // CALL NC,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_C) == 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xDC: // CALL C,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_C) != 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xE4: // CALL PO,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_PV) == 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xEC: // CALL PE,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_PV) != 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xF4: // CALL P,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_S) == 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xFC: // CALL M,nn
		c.m1(c.currentInstrPC)
		addr := c.rdAddr()
		c.WZ = addr
		if (c.F & FLAG_S) != 0 {
			c.exec(c.ir(), 1)
			c.pushC(c.PC)
			c.PC = addr
		}
	case 0xC0: // RET NZ
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_Z) == 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}
	case 0xC8: // RET Z
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_Z) != 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}
	case 0xD0: // RET NC
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_C) == 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}
	case 0xD8: // RET C
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_C) != 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}
	case 0xE0: // RET PO
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_PV) == 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}
	case 0xE8: // RET PE
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_PV) != 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}
	case 0xF0: // RET P
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_S) == 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}
	case 0xF8: // RET M
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		if (c.F & FLAG_S) != 0 {
			c.PC = c.popC()
			c.WZ = c.PC
		}

	// Stack operations
	case 0xC1: // POP BC
		c.m1(c.currentInstrPC)
		c.setBC(c.popC())
	case 0xC5: // PUSH BC
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.bc())
	case 0xD1: // POP DE
		c.m1(c.currentInstrPC)
		c.setDE(c.popC())
	case 0xD5: // PUSH DE
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.de())
	case 0xE1: // POP HL
		c.m1(c.currentInstrPC)
		c.setHL(c.popC())
	case 0xE5: // PUSH HL
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.hl())
	case 0xF1: // POP AF
		c.m1(c.currentInstrPC)
		c.setAF(c.popC())
	case 0xF5: // PUSH AF
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.af())

	// Miscellaneous
	case 0x00: // NOP
		c.tstates += 4
	case 0x08: // EX AF,AF'
		c.A, c.A_ = c.A_, c.A
		c.F, c.F_ = c.F_, c.F
		c.tstates += 4
	case 0x10: // DJNZ n
		c.B--
		offset := int8(c.readOperand())
		if c.B != 0 {
			c.PC = uint16(int32(c.PC) + int32(offset))
			c.WZ = c.PC
			c.tstates += 13
		} else {
			c.tstates += 8
		}
	case 0x27: // DAA
		c.daa()
		c.tstates += 4
	case 0x2F: // CPL
		c.A = ^c.A
		c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV | FLAG_C)) | (c.A & (FLAG_F5 | FLAG_F3)) | FLAG_H | FLAG_N
		c.tstates += 4
	case 0x37: // SCF
		c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & (FLAG_F5 | FLAG_F3)) | FLAG_C
		c.tstates += 4
	case 0x3F: // CCF
		// Z80 spec: H ← old C; C ← !old C; N ← 0; S,Z,PV unchanged;
		// F3,F5 from A.
		oldC := c.F & FLAG_C
		f := c.F & (FLAG_S | FLAG_Z | FLAG_PV)
		if oldC != 0 {
			f |= FLAG_H
		}
		f |= oldC ^ FLAG_C
		f |= c.A & (FLAG_F5 | FLAG_F3)
		c.F = f
		c.tstates += 4

	// Interrupts
	case 0xF3: // DI
		c.IFF1, c.IFF2 = false, false
		c.tstates += 4
	case 0xFB: // EI
		c.IFF1, c.IFF2 = true, true
		c.eiDelay = true // Defer IRQ ack by one instruction.
		c.tstates += 4

	// I/O
	case 0xD3: // OUT (n),A
		n := c.readOperand()
		port := uint16(n) | (uint16(c.A) << 8)
		// MEMPTR/WZ per Sean Young §A.1: WZ_low = (n+1) & $FF;
		// WZ_high = A. Important for BIT n,(HL) F5/F3.
		c.WZ = (uint16(c.A) << 8) | uint16(byte(n+1))
		c.mem.ContendPort(port)
		c.ula.WritePort(port, c.A)
		c.tstates += 11
	case 0xDB: // IN A,(n)
		n := c.readOperand()
		port := uint16(n) | (uint16(c.A) << 8)
		// MEMPTR/WZ: WZ = (A<<8 | n) + 1.
		c.WZ = port + 1
		c.mem.ContendPort(port)
		val, _ := c.ula.ReadPort(port)
		c.A = val
		c.tstates += 11

	// Exchange
	case 0xD9: // EXX
		c.B, c.B_ = c.B_, c.B
		c.C, c.C_ = c.C_, c.C
		c.D, c.D_ = c.D_, c.D
		c.E, c.E_ = c.E_, c.E
		c.H, c.H_ = c.H_, c.H
		c.L, c.L_ = c.L_, c.L
		c.tstates += 4
	case 0xE3: // EX (SP),HL
		// 19 T: M1(4) + rd SP(3) + rd SP+1(3) + 1 internal + wr SP+1(3)
		// + wr SP(3) + 2 internal (FUSE z80.c EX (SP),HL timing).
		c.m1(c.currentInstrPC)
		lo := c.rd(c.SP)
		hi := c.rd(c.SP + 1)
		c.exec(c.SP+1, 1)
		c.wr(c.SP+1, c.H)
		c.wr(c.SP, c.L)
		c.exec(c.SP, 2)
		c.L = lo
		c.H = hi
		// MEMPTR/WZ per Sean Young §A.1: WZ = new HL value.
		c.WZ = c.hl()
	case 0xEB: // EX DE,HL
		c.D, c.H = c.H, c.D
		c.E, c.L = c.L, c.E
		c.tstates += 4
	case 0xF9: // LD SP,HL
		oldSP := c.SP
		c.SP = c.hl()
		c.tstates += 6
		if c.OnSPLoad != nil {
			c.OnSPLoad(c.currentInstrPC, oldSP, c.SP)
		}

	// RST instructions. Per Sean Young §A.1, WZ = vector.
	case 0xC7: // RST 0x00
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x00
		c.WZ = 0x00
	case 0xCF: // RST 0x08
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x08
		c.WZ = 0x08
	case 0xD7: // RST 0x10
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x10
		c.WZ = 0x10
	case 0xDF: // RST 0x18
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x18
		c.WZ = 0x18
	case 0xE7: // RST 0x20
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x20
		c.WZ = 0x20
	case 0xEF: // RST 0x28
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x28
		c.WZ = 0x28
	case 0xF7: // RST 0x30
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x30
		c.WZ = 0x30
	case 0xFF: // RST 0x38
		c.m1(c.currentInstrPC)
		c.exec(c.ir(), 1)
		c.pushC(c.PC)
		c.PC = 0x38
		c.WZ = 0x38

	// Extended instruction sets
	case 0xCB: // CB prefix
		c.executeCBInstruction(c.fetch())
	case 0xED: // ED prefix
		c.executeEDInstruction(c.fetch())
	case 0xDD: // DD prefix (IX instructions)
		c.executeDDInstruction(c.fetch())
	case 0xFD: // FD prefix (IY instructions)
		c.executeFDInstruction(c.fetch())

	default:
		if !c.logEnabled {
			log.Printf("Unknown opcode: 0x%02X at PC: 0x%04X\n", opcode, c.PC-1)
			c.logEnabled = true
		}
		c.tstates += 4
	}
}

// DAA (Decimal Adjust Accumulator) implementation
func (c *CPU) daa() {
	// Compute the BCD correction value and the outgoing C flag,
	// then delegate to add/sub so H comes out of the same lookup
	// tables that drive every other arithmetic op. The post-pass
	// patches C (which our correction logic computed independently)
	// and replaces overflow-PV with parity-PV (DAA reports parity,
	// not overflow). N is preserved by the choice of add vs sub.
	add := byte(0)
	carry := c.F & FLAG_C
	if (c.F&FLAG_H) != 0 || (c.A&0x0F) > 9 {
		add = 0x06
	}
	if carry != 0 || c.A > 0x99 {
		add |= 0x60
	}
	if c.A > 0x99 {
		carry = FLAG_C
	}
	if (c.F & FLAG_N) != 0 {
		c.sub(add)
	} else {
		c.add(add)
	}
	c.F = (c.F &^ (FLAG_C | FLAG_PV)) | carry | c.parityTable[c.A]
}

// CB prefix instructions (bit manipulation, shifts, rotates)
func (c *CPU) executeCBInstruction(opcode byte) {
	switch opcode {
	// Rotate left circular
	case 0x00: // RLC B
		c.B = c.rlc(c.B)
		c.tstates += 8
	case 0x01: // RLC C
		c.C = c.rlc(c.C)
		c.tstates += 8
	case 0x02: // RLC D
		c.D = c.rlc(c.D)
		c.tstates += 8
	case 0x03: // RLC E
		c.E = c.rlc(c.E)
		c.tstates += 8
	case 0x04: // RLC H
		c.H = c.rlc(c.H)
		c.tstates += 8
	case 0x05: // RLC L
		c.L = c.rlc(c.L)
		c.tstates += 8
	case 0x06: // RLC (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.rlc(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x07: // RLC A
		c.A = c.rlc(c.A)
		c.tstates += 8

	// Rotate right circular
	case 0x08: // RRC B
		c.B = c.rrc(c.B)
		c.tstates += 8
	case 0x09: // RRC C
		c.C = c.rrc(c.C)
		c.tstates += 8
	case 0x0A: // RRC D
		c.D = c.rrc(c.D)
		c.tstates += 8
	case 0x0B: // RRC E
		c.E = c.rrc(c.E)
		c.tstates += 8
	case 0x0C: // RRC H
		c.H = c.rrc(c.H)
		c.tstates += 8
	case 0x0D: // RRC L
		c.L = c.rrc(c.L)
		c.tstates += 8
	case 0x0E: // RRC (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.rrc(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x0F: // RRC A
		c.A = c.rrc(c.A)
		c.tstates += 8

	// Rotate left through carry
	case 0x10: // RL B
		c.B = c.rl(c.B)
		c.tstates += 8
	case 0x11: // RL C
		c.C = c.rl(c.C)
		c.tstates += 8
	case 0x12: // RL D
		c.D = c.rl(c.D)
		c.tstates += 8
	case 0x13: // RL E
		c.E = c.rl(c.E)
		c.tstates += 8
	case 0x14: // RL H
		c.H = c.rl(c.H)
		c.tstates += 8
	case 0x15: // RL L
		c.L = c.rl(c.L)
		c.tstates += 8
	case 0x16: // RL (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.rl(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x17: // RL A
		c.A = c.rl(c.A)
		c.tstates += 8

	// Rotate right through carry
	case 0x18: // RR B
		c.B = c.rr(c.B)
		c.tstates += 8
	case 0x19: // RR C
		c.C = c.rr(c.C)
		c.tstates += 8
	case 0x1A: // RR D
		c.D = c.rr(c.D)
		c.tstates += 8
	case 0x1B: // RR E
		c.E = c.rr(c.E)
		c.tstates += 8
	case 0x1C: // RR H
		c.H = c.rr(c.H)
		c.tstates += 8
	case 0x1D: // RR L
		c.L = c.rr(c.L)
		c.tstates += 8
	case 0x1E: // RR (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.rr(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x1F: // RR A
		c.A = c.rr(c.A)
		c.tstates += 8

	// Shift left arithmetic
	case 0x20: // SLA B
		c.B = c.sla(c.B)
		c.tstates += 8
	case 0x21: // SLA C
		c.C = c.sla(c.C)
		c.tstates += 8
	case 0x22: // SLA D
		c.D = c.sla(c.D)
		c.tstates += 8
	case 0x23: // SLA E
		c.E = c.sla(c.E)
		c.tstates += 8
	case 0x24: // SLA H
		c.H = c.sla(c.H)
		c.tstates += 8
	case 0x25: // SLA L
		c.L = c.sla(c.L)
		c.tstates += 8
	case 0x26: // SLA (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.sla(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x27: // SLA A
		c.A = c.sla(c.A)
		c.tstates += 8

	// Shift right arithmetic
	case 0x28: // SRA B
		c.B = c.sra(c.B)
		c.tstates += 8
	case 0x29: // SRA C
		c.C = c.sra(c.C)
		c.tstates += 8
	case 0x2A: // SRA D
		c.D = c.sra(c.D)
		c.tstates += 8
	case 0x2B: // SRA E
		c.E = c.sra(c.E)
		c.tstates += 8
	case 0x2C: // SRA H
		c.H = c.sra(c.H)
		c.tstates += 8
	case 0x2D: // SRA L
		c.L = c.sra(c.L)
		c.tstates += 8
	case 0x2E: // SRA (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.sra(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x2F: // SRA A
		c.A = c.sra(c.A)
		c.tstates += 8

	// Shift left logical (undocumented)
	case 0x30: // SLL B
		c.B = c.sll(c.B)
		c.tstates += 8
	case 0x31: // SLL C
		c.C = c.sll(c.C)
		c.tstates += 8
	case 0x32: // SLL D
		c.D = c.sll(c.D)
		c.tstates += 8
	case 0x33: // SLL E
		c.E = c.sll(c.E)
		c.tstates += 8
	case 0x34: // SLL H
		c.H = c.sll(c.H)
		c.tstates += 8
	case 0x35: // SLL L
		c.L = c.sll(c.L)
		c.tstates += 8
	case 0x36: // SLL (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.sll(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x37: // SLL A
		c.A = c.sll(c.A)
		c.tstates += 8

	// Shift right logical
	case 0x38: // SRL B
		c.B = c.srl(c.B)
		c.tstates += 8
	case 0x39: // SRL C
		c.C = c.srl(c.C)
		c.tstates += 8
	case 0x3A: // SRL D
		c.D = c.srl(c.D)
		c.tstates += 8
	case 0x3B: // SRL E
		c.E = c.srl(c.E)
		c.tstates += 8
	case 0x3C: // SRL H
		c.H = c.srl(c.H)
		c.tstates += 8
	case 0x3D: // SRL L
		c.L = c.srl(c.L)
		c.tstates += 8
	case 0x3E: // SRL (HL)
		c.m1(c.currentInstrPC)
		c.m1(c.currentInstrPC + 1)
		addr := c.hl()
		v := c.srl(c.rd(addr))
		c.exec(addr, 1)
		c.wr(addr, v)
	case 0x3F: // SRL A
		c.A = c.srl(c.A)
		c.tstates += 8

	// Bit test operations (0x40-0x7F)
	default:
		if opcode >= 0x40 && opcode <= 0x7F {
			bit := int((opcode - 0x40) / 8)
			reg := int((opcode - 0x40) % 8)
			if reg == 6 { // BIT n,(HL): 12 T = 2×M1(8) + read(3) + 1 internal.
				c.m1(c.currentInstrPC)
				c.m1(c.currentInstrPC + 1)
				addr := c.hl()
				c.bit(bit, c.rd(addr))
				c.exec(addr, 1)
				if c.Variant == VariantZ80N {
					// T80N core (t80n_alu.vhd "BIT"): the reg==6 form
					// clears F3/F5 (it does not contaminate from MEMPTR).
					c.F &^= FLAG_F3 | FLAG_F5
				} else {
					// NMOS: F3/F5 from MEMPTR high byte (undocumented).
					c.F = (c.F &^ (FLAG_F3 | FLAG_F5)) | byte(c.WZ>>8)&(FLAG_F3|FLAG_F5)
				}
			} else {
				c.bit(bit, c.getRegister8(reg))
				c.tstates += 8
			}
		} else if opcode >= 0x80 && opcode <= 0xBF {
			// Reset bit operations (0x80-0xBF)
			bit := int((opcode - 0x80) / 8)
			reg := int((opcode - 0x80) % 8)
			if reg == 6 { // RES n,(HL): 15 T = 2×M1(8) + read(3) + 1 internal + write(3).
				c.m1(c.currentInstrPC)
				c.m1(c.currentInstrPC + 1)
				addr := c.hl()
				v := c.res(bit, c.rd(addr))
				c.exec(addr, 1)
				c.wr(addr, v)
			} else {
				c.setRegister8(reg, c.res(bit, c.getRegister8(reg)))
				c.tstates += 8
			}
		} else { // opcode >= 0xC0
			// Set bit operations (0xC0-0xFF)
			bit := int((opcode - 0xC0) / 8)
			reg := int((opcode - 0xC0) % 8)
			if reg == 6 { // SET n,(HL): 15 T (same shape as RES n,(HL)).
				c.m1(c.currentInstrPC)
				c.m1(c.currentInstrPC + 1)
				addr := c.hl()
				v := c.set(bit, c.rd(addr))
				c.exec(addr, 1)
				c.wr(addr, v)
			} else {
				c.setRegister8(reg, c.set(bit, c.getRegister8(reg)))
				c.tstates += 8
			}
		}
	}
}

func (c *CPU) executeEDInstruction(opcode byte) {
	switch opcode {
	// 16-bit loads. All set MEMPTR (WZ) = addr + 1.
	case 0x43: // LD (nn),BC
		addr := c.fetch16()
		c.mem.Write(addr, c.C)
		c.mem.Write(addr+1, c.B)
		c.WZ = addr + 1
		c.tstates += 20
	case 0x53: // LD (nn),DE
		addr := c.fetch16()
		c.mem.Write(addr, c.E)
		c.mem.Write(addr+1, c.D)
		c.WZ = addr + 1
		c.tstates += 20
	case 0x4B: // LD BC,(nn)
		addr := c.fetch16()
		c.C = c.mem.Read(addr)
		c.B = c.mem.Read(addr + 1)
		c.WZ = addr + 1
		c.tstates += 20
	case 0x5B: // LD DE,(nn)
		addr := c.fetch16()
		c.E = c.mem.Read(addr)
		c.D = c.mem.Read(addr + 1)
		c.WZ = addr + 1
		c.tstates += 20
	case 0x63: // LD (nn),HL
		addr := c.fetch16()
		c.mem.Write(addr, c.L)
		c.mem.Write(addr+1, c.H)
		c.WZ = addr + 1
		c.tstates += 20
	case 0x6B: // LD HL,(nn)
		addr := c.fetch16()
		c.L = c.mem.Read(addr)
		c.H = c.mem.Read(addr + 1)
		c.WZ = addr + 1
		c.tstates += 20

	// ALU operations with carry/borrow. All set MEMPTR = HL_old + 1.
	case 0x42: // SBC HL,BC
		c.WZ = c.hl() + 1
		c.sbc16(c.hl(), c.bc())
		c.tstates += 15
	case 0x52: // SBC HL,DE
		c.WZ = c.hl() + 1
		c.sbc16(c.hl(), c.de())
		c.tstates += 15
	case 0x62: // SBC HL,HL
		c.WZ = c.hl() + 1
		c.sbc16(c.hl(), c.hl())
		c.tstates += 15
	case 0x4A: // ADC HL,BC
		c.WZ = c.hl() + 1
		c.adc16(c.hl(), c.bc())
		c.tstates += 15
	case 0x5A: // ADC HL,DE
		c.WZ = c.hl() + 1
		c.adc16(c.hl(), c.de())
		c.tstates += 15
	case 0x6A: // ADC HL,HL
		c.WZ = c.hl() + 1
		c.adc16(c.hl(), c.hl())
		c.tstates += 15
	case 0x7A: // ADC HL,SP
		c.WZ = c.hl() + 1
		c.adc16(c.hl(), c.SP)
		c.tstates += 15
	case 0x72: // SBC HL,SP
		c.WZ = c.hl() + 1
		c.sbc16(c.hl(), c.SP)
		c.tstates += 15
	case 0x73: // LD (nn),SP
		addr := c.fetch16()
		c.mem.Write(addr, byte(c.SP))
		c.mem.Write(addr+1, byte(c.SP>>8))
		c.WZ = addr + 1
		c.tstates += 20
	case 0x7B: // LD SP,(nn)
		oldSP := c.SP
		addr := c.fetch16()
		low := c.mem.Read(addr)
		high := c.mem.Read(addr + 1)
		c.SP = uint16(high)<<8 | uint16(low)
		c.WZ = addr + 1
		c.tstates += 20
		if c.OnSPLoad != nil {
			c.OnSPLoad(c.currentInstrPC, oldSP, c.SP)
		}

	// Block operations. Each helper performs its own M1 + memory-access
	// accounting through c.m1/c.rd/c.wr/c.exec so the (HL)/(DE) accesses
	// participate in per-access ULA memory contention.
	case 0xB0: // LDIR - Load, increment and repeat
		c.ldir()
	case 0xB8: // LDDR - Load, decrement and repeat
		c.lddr()
	case 0xA0: // LDI - Load and increment
		c.ldi()
	case 0xA8: // LDD - Load and decrement
		c.ldd()
	case 0xA1: // CPI - Compare and increment
		c.cpi()
	case 0xA9: // CPD - Compare and decrement
		c.cpd()
	case 0xB1: // CPIR - Compare, increment and repeat
		c.cpir()
	case 0xB9: // CPDR - Compare, decrement and repeat
		c.cpdr()
	case 0xA3: // OUTI - Output and increment
		c.outi()
	case 0xAB: // OUTD - Output and decrement
		c.outd()
	case 0xB3: // OTIR - Output, increment and repeat
		c.otir()
	case 0xBB: // OTDR - Output, decrement and repeat
		c.otdr()
	case 0xA2: // INI - Input and increment
		c.ini()
	case 0xAA: // IND - Input and decrement
		c.ind()
	case 0xB2: // INIR - Input, increment and repeat
		c.inir()
	case 0xBA: // INDR - Input, decrement and repeat
		c.indr()
	case 0x67: // RRD - Rotate right decimal
		c.rrd()
		c.tstates += 18
	case 0x6F: // RLD - Rotate left decimal
		c.rld()
		c.tstates += 18

	// NEG and all 7 undocumented mirror encodings. Per Sean Young
	// §A.1, ED $44 / $4C / $54 / $5C / $64 / $6C / $74 / $7C all
	// negate A with identical flag semantics and 8 t-state cost.
	case 0x44, 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C:
		orig := c.A
		c.F = 0
		if orig == 0x80 {
			c.F |= FLAG_PV // Overflow if A was 0x80
		}
		if orig != 0x00 {
			c.F |= FLAG_C // Carry if A was not zero
		}
		c.A = -orig // Two's complement negation
		c.F |= c.sz53Table[c.A]
		if (orig & 0x0F) != 0 {
			c.F |= FLAG_H // Half-carry if lower nibble of original was non-zero
		}
		c.F |= FLAG_N // Subtraction flag is always set for NEG
		c.tstates += 8

	// Interrupt mode and register operations
	case 0x47: // LD I,A
		c.I = c.A
		c.tstates += 9
	case 0x4F: // LD R,A
		c.R = c.A
		c.tstates += 9
	case 0x57: // LD A,I
		c.A = c.I
		c.F = (c.F & FLAG_C) | c.sz53Table[c.A]
		if c.IFF2 {
			c.F |= FLAG_PV
		}
		c.tstates += 9
	case 0x5F: // LD A,R
		c.A = c.R
		c.F = (c.F & FLAG_C) | c.sz53Table[c.A]
		if c.IFF2 {
			c.F |= FLAG_PV
		}
		c.tstates += 9

	// Interrupt modes. Per Sean Young §A.1 every IM has multiple
	// ED-prefix encodings; canonical + mirrors are functionally
	// identical (same mode + 8 t).
	case 0x46, 0x4E, 0x66, 0x6E: // IM 0 (canonical + mirrors)
		c.IM = 0
		c.tstates += 8
	case 0x56, 0x76: // IM 1 (canonical + mirror)
		c.IM = 1
		c.tstates += 8
	case 0x5E, 0x7E: // IM 2 (canonical + mirror)
		c.IM = 2
		c.tstates += 8

	// I/O operations — all set WZ = BC + 1 per Sean Young §A.1.
	case 0x40: // IN B,(C)
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.B = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.B] | c.parityTable[c.B]
		c.tstates += 12
	case 0x48: // IN C,(C)
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.C = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.C] | c.parityTable[c.C]
		c.tstates += 12
	case 0x50: // IN D,(C)
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.D = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.D] | c.parityTable[c.D]
		c.tstates += 12
	case 0x58: // IN E,(C)
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.E = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.E] | c.parityTable[c.E]
		c.tstates += 12
	case 0x60: // IN H,(C)
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.H = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.H] | c.parityTable[c.H]
		c.tstates += 12
	case 0x68: // IN L,(C)
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.L = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.L] | c.parityTable[c.L]
		c.tstates += 12
	case 0x78: // IN A,(C)
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.A = val
		c.F = (c.F & FLAG_C) | c.sz53Table[c.A] | c.parityTable[c.A]
		c.tstates += 12
	case 0x70: // IN F,(C) - special case, only affects flags
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		val, _ := c.ula.ReadPort(c.bc())
		c.F = (c.F & FLAG_C) | c.sz53Table[val] | c.parityTable[val]
		c.tstates += 12

	// Output instructions — all set WZ = BC + 1.
	case 0x41: // OUT (C), B
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.B)
		c.tstates += 12
	case 0x49: // OUT (C), C
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.C)
		c.tstates += 12
	case 0x51: // OUT (C), D
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.D)
		c.tstates += 12
	case 0x59: // OUT (C), E
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.E)
		c.tstates += 12
	case 0x61: // OUT (C), H
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.H)
		c.tstates += 12
	case 0x69: // OUT (C), L
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.L)
		c.tstates += 12
	case 0x71: // OUT (C), 0 - outputs zero
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), 0)
		c.tstates += 12
	case 0x79: // OUT (C), A
		c.WZ = c.bc() + 1
		c.mem.ContendPort(c.bc())
		c.ula.WritePort(c.bc(), c.A)
		c.tstates += 12

	// Return from interrupt. Per Sean Young's "Undocumented Z80
	// Documented" §A.1, ED $4D/$5D/$6D/$7D are all RETI mirrors and
	// ED $45/$55/$65/$75 are all RETN mirrors. Each takes 14 t and
	// performs the same pop + IFF restoration.
	case 0x4D, 0x5D, 0x6D, 0x7D: // RETI mirrors
		c.PC = c.pop()
		c.WZ = c.PC // per Sean Young §3.4: RETI sets MEMPTR = popped PC
		// RETI restores IFF1 from IFF2 — faithful Z80 behaviour
		// (IFF1 <- IFF2), the same as RETN.
		c.IFF1 = c.IFF2
		c.tstates += 14
		if c.retnHook != nil {
			c.retnHook() // t80n_mcode.vhd asserts I_RETN for RETI too (line 660)
		}
	case 0x45, 0x55, 0x65, 0x75: // RETN mirrors
		popped := c.pop() // always pop (SP stays correct)
		if c.NMIStackless && c.StacklessReadNR != nil {
			// Stackless NMI: RETN returns to NR$C2/$C3, not the stack —
			// NextZXOS rewrites them in the MF-NMI handler to launch the
			// 128 editor. SP is still popped above.
			c.PC = uint16(c.StacklessReadNR(0xC3))<<8 | uint16(c.StacklessReadNR(0xC2))
		} else {
			c.PC = popped
		}
		c.WZ = c.PC // per Sean Young §3.4: RETN sets MEMPTR = popped PC
		c.IFF1 = c.IFF2
		c.tstates += 14
		if c.retnHook != nil {
			c.retnHook()
		}

	default:
		if c.Variant == VariantZ80N && c.executeZ80NEDInstruction(opcode) {
			return
		}
		// Invalid ED-prefix opcodes execute as "NONI NOP" on the real
		// Z80 — 8 T-states, no register / memory effect. NextZXOS hits
		// `ED 00` repeatedly as part of its scrambler / token tables, so
		// this stays silent rather than logging each occurrence.
		c.tstates += 8
	}
}

func (c *CPU) executeDDInstruction(opcode byte) {
	switch opcode {
	// ADD IX,rr instructions
	case 0x09: // ADD IX,BC
		c.addIX(c.bc())
	case 0x19: // ADD IX,DE
		c.addIX(c.de())
	case 0x29: // ADD IX,IX
		c.addIX(c.IX)
	case 0x39: // ADD IX,SP
		c.addIX(c.SP)

	// LD IX,nn / LD (nn),IX / LD IX,(nn)
	case 0x21: // LD IX,nn
		c.IX = c.fetch16()
		c.tstates += 14
	case 0x22: // LD (nn),IX
		addr := c.fetch16()
		c.mem.Write(addr, byte(c.IX))
		c.mem.Write(addr+1, byte(c.IX>>8))
		c.tstates += 20
	case 0x2A: // LD IX,(nn)
		addr := c.fetch16()
		c.IX = uint16(c.mem.Read(addr)) | (uint16(c.mem.Read(addr+1)) << 8)
		c.tstates += 20

	// INC/DEC IX
	case 0x23: // INC IX
		c.IX++
		c.tstates += 10
	case 0x2B: // DEC IX
		c.IX--
		c.tstates += 10

	// INC/DEC (IX+d)
	case 0x34: // INC (IX+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IX) + int32(d))
		c.WZ = addr     // per Sean Young §3.4
		c.tstates += 17 // base minus read(3)+write(3) below
		val := c.inc(c.rd(addr))
		c.wr(addr, val)
	case 0x35: // DEC (IX+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IX) + int32(d))
		c.WZ = addr     // per Sean Young §3.4
		c.tstates += 17 // base minus read(3)+write(3) below
		val := c.dec(c.rd(addr))
		c.wr(addr, val)

	// LD (IX+d),n
	case 0x36: // LD (IX+d),n
		d := int8(c.readOperand())
		n := c.readOperand()
		addr := uint16(int32(c.IX) + int32(d))
		c.WZ = addr     // per Sean Young §3.4
		c.tstates += 16 // base minus the 3-T contended write below
		c.wr(addr, n)

	// LD r,(IX+d) instructions - Load from (IX+d) to register.
	// loadIXd adds 15 t; LD r,(IX+d) total = 19 per Z80 spec, so
	// add the missing 4 for the DD prefix M1 cycle.
	case 0x46: // LD B,(IX+d)
		c.B = c.loadIXd()
		c.tstates += 4
	case 0x4E: // LD C,(IX+d)
		c.C = c.loadIXd()
		c.tstates += 4
	case 0x56: // LD D,(IX+d)
		c.D = c.loadIXd()
		c.tstates += 4
	case 0x5E: // LD E,(IX+d)
		c.E = c.loadIXd()
		c.tstates += 4
	case 0x66: // LD H,(IX+d)
		c.H = c.loadIXd()
		c.tstates += 4
	case 0x6E: // LD L,(IX+d)
		c.L = c.loadIXd()
		c.tstates += 4
	case 0x7E: // LD A,(IX+d)
		c.A = c.loadIXd()
		c.tstates += 4

	// LD (IX+d),r instructions - Store register to (IX+d)
	case 0x70: // LD (IX+d),B
		c.storeIXd(c.B)
	case 0x71: // LD (IX+d),C
		c.storeIXd(c.C)
	case 0x72: // LD (IX+d),D
		c.storeIXd(c.D)
	case 0x73: // LD (IX+d),E
		c.storeIXd(c.E)
	case 0x74: // LD (IX+d),H
		c.storeIXd(c.H)
	case 0x75: // LD (IX+d),L
		c.storeIXd(c.L)
	case 0x77: // LD (IX+d),A
		c.storeIXd(c.A)

	// Arithmetic/Logic operations with (IX+d)
	case 0x86: // ADD A,(IX+d)
		c.add(c.loadIXd())
		c.tstates += 4 // loadIXd already adds 15, need 4 more for total 19
	case 0x8E: // ADC A,(IX+d)
		c.adc(c.loadIXd())
		c.tstates += 4
	case 0x96: // SUB (IX+d)
		c.sub(c.loadIXd())
		c.tstates += 4
	case 0x9E: // SBC A,(IX+d)
		c.sbc(c.loadIXd())
		c.tstates += 4
	case 0xA6: // AND (IX+d)
		c.and(c.loadIXd())
		c.tstates += 4
	case 0xAE: // XOR (IX+d)
		c.xor(c.loadIXd())
		c.tstates += 4
	case 0xB6: // OR (IX+d)
		c.or(c.loadIXd())
		c.tstates += 4
	case 0xBE: // CP (IX+d)
		c.cp(c.loadIXd())
		c.tstates += 4

	// Stack operations
	case 0xE1: // POP IX
		c.IX = c.pop()
		c.tstates += 14
	case 0xE5: // PUSH IX
		c.push(c.IX)
		c.tstates += 15

	// Jump
	case 0xE9: // JP (IX)
		c.PC = c.IX
		c.tstates += 8

	// Exchange
	case 0xE3: // EX (SP),IX
		temp := c.IX
		c.IX = uint16(c.mem.Read(c.SP)) | (uint16(c.mem.Read(c.SP+1)) << 8)
		c.mem.Write(c.SP, byte(temp))
		c.mem.Write(c.SP+1, byte(temp>>8))
		c.WZ = c.IX // per Sean Young §A.1: WZ = new IX
		c.tstates += 23

	// Stack-pointer load
	case 0xF9: // LD SP,IX
		oldSP := c.SP
		c.SP = c.IX
		c.tstates += 10
		if c.OnSPLoad != nil {
			c.OnSPLoad(c.currentInstrPC, oldSP, c.SP)
		}

	// DD CB prefix (IX bit operations).
	// The instruction adds its own 23 T-states internally to mirror
	// the FD CB dispatch (see executeFDCBInstruction).
	case 0xCB:
		d := int8(c.readOperand())
		// Per Sean Young §A.1 the DD CB d xx sequence has exactly
		// TWO M1 cycles (DD prefix and CB prefix); d and xx are
		// operand bytes and must not increment R.
		opcode := c.readOperand()
		addr := uint16(int32(c.IX) + int32(d))
		c.executeDDCBInstruction(opcode, addr)

	// Undocumented 8-bit IXh / IXl operations: DD-prefixed forms of any
	// base instruction that operates on H or L access IXh / IXl instead.
	// These are widely used by commercial software (e.g. Cobra +3).
	case 0x24: // INC IXh
		c.setIXh(c.inc(c.ixh()))
		c.tstates += 8
	case 0x25: // DEC IXh
		c.setIXh(c.dec(c.ixh()))
		c.tstates += 8
	case 0x26: // LD IXh, n
		c.setIXh(c.readOperand())
		c.tstates += 11
	case 0x2C: // INC IXl
		c.setIXl(c.inc(c.ixl()))
		c.tstates += 8
	case 0x2D: // DEC IXl
		c.setIXl(c.dec(c.ixl()))
		c.tstates += 8
	case 0x2E: // LD IXl, n
		c.setIXl(c.readOperand())
		c.tstates += 11
	case 0x44: // LD B, IXh
		c.B = c.ixh()
		c.tstates += 8
	case 0x45: // LD B, IXl
		c.B = c.ixl()
		c.tstates += 8
	case 0x4C: // LD C, IXh
		c.C = c.ixh()
		c.tstates += 8
	case 0x4D: // LD C, IXl
		c.C = c.ixl()
		c.tstates += 8
	case 0x54: // LD D, IXh
		c.D = c.ixh()
		c.tstates += 8
	case 0x55: // LD D, IXl
		c.D = c.ixl()
		c.tstates += 8
	case 0x5C: // LD E, IXh
		c.E = c.ixh()
		c.tstates += 8
	case 0x5D: // LD E, IXl
		c.E = c.ixl()
		c.tstates += 8
	case 0x60: // LD IXh, B
		c.setIXh(c.B)
		c.tstates += 8
	case 0x61: // LD IXh, C
		c.setIXh(c.C)
		c.tstates += 8
	case 0x62: // LD IXh, D
		c.setIXh(c.D)
		c.tstates += 8
	case 0x63: // LD IXh, E
		c.setIXh(c.E)
		c.tstates += 8
	case 0x64: // LD IXh, IXh (no-op)
		c.tstates += 8
	case 0x65: // LD IXh, IXl
		c.setIXh(c.ixl())
		c.tstates += 8
	case 0x67: // LD IXh, A
		c.setIXh(c.A)
		c.tstates += 8
	case 0x68: // LD IXl, B
		c.setIXl(c.B)
		c.tstates += 8
	case 0x69: // LD IXl, C
		c.setIXl(c.C)
		c.tstates += 8
	case 0x6A: // LD IXl, D
		c.setIXl(c.D)
		c.tstates += 8
	case 0x6B: // LD IXl, E
		c.setIXl(c.E)
		c.tstates += 8
	case 0x6C: // LD IXl, IXh
		c.setIXl(c.ixh())
		c.tstates += 8
	case 0x6D: // LD IXl, IXl (no-op)
		c.tstates += 8
	case 0x6F: // LD IXl, A
		c.setIXl(c.A)
		c.tstates += 8
	case 0x7C: // LD A, IXh
		c.A = c.ixh()
		c.tstates += 8
	case 0x7D: // LD A, IXl
		c.A = c.ixl()
		c.tstates += 8
	case 0x84: // ADD A, IXh
		c.add(c.ixh())
		c.tstates += 8
	case 0x85: // ADD A, IXl
		c.add(c.ixl())
		c.tstates += 8
	case 0x8C: // ADC A, IXh
		c.adc(c.ixh())
		c.tstates += 8
	case 0x8D: // ADC A, IXl
		c.adc(c.ixl())
		c.tstates += 8
	case 0x94: // SUB IXh
		c.sub(c.ixh())
		c.tstates += 8
	case 0x95: // SUB IXl
		c.sub(c.ixl())
		c.tstates += 8
	case 0x9C: // SBC A, IXh
		c.sbc(c.ixh())
		c.tstates += 8
	case 0x9D: // SBC A, IXl
		c.sbc(c.ixl())
		c.tstates += 8
	case 0xA4: // AND IXh
		c.and(c.ixh())
		c.tstates += 8
	case 0xA5: // AND IXl
		c.and(c.ixl())
		c.tstates += 8
	case 0xAC: // XOR IXh
		c.xor(c.ixh())
		c.tstates += 8
	case 0xAD: // XOR IXl
		c.xor(c.ixl())
		c.tstates += 8
	case 0xB4: // OR IXh
		c.or(c.ixh())
		c.tstates += 8
	case 0xB5: // OR IXl
		c.or(c.ixl())
		c.tstates += 8
	case 0xBC: // CP IXh
		c.cp(c.ixh())
		c.tstates += 8
	case 0xBD: // CP IXl
		c.cp(c.ixl())
		c.tstates += 8

	default:
		// On real Z80 hardware, a DD prefix followed by an opcode that
		// doesn't use IX is a NONI: the prefix consumes 4 T-states and
		// one M1 cycle (R bump from the fetch), and the base opcode
		// executes normally on top. Per Sean Young §5.7.
		c.tstates += 4
		c.executeBaseInstruction(opcode)
	}
}

// Helper functions for IX operations
func (c *CPU) addIX(value uint16) {
	c.WZ = c.IX + 1 // per Sean Young §3.4: ADD IX,rr sets MEMPTR = IX + 1
	result := uint32(c.IX) + uint32(value)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) // Preserve S, Z, PV
	if result > 0xFFFF {
		c.F |= FLAG_C
	}
	if ((c.IX & 0x0FFF) + (value & 0x0FFF)) > 0x0FFF {
		c.F |= FLAG_H
	}
	c.IX = uint16(result)
	c.F |= byte(c.IX>>8) & (FLAG_F3 | FLAG_F5) // Set undocumented flags
	c.tstates += 15
}

func (c *CPU) loadIXd() byte {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IX) + int32(d))
	c.WZ = addr     // per Sean Young §3.4: indexed addressing sets WZ = IX + d
	c.tstates += 12 // base minus the 3-T contended read cycle below
	return c.rd(addr)
}

func (c *CPU) storeIXd(value byte) {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IX) + int32(d))
	c.WZ = addr     // per Sean Young §3.4: indexed addressing sets WZ = IX + d
	c.tstates += 16 // base minus the 3-T contended write cycle below
	c.wr(addr, value)
}

// Helper functions for IY operations
func (c *CPU) addIY(value uint16) {
	c.WZ = c.IY + 1 // per Sean Young §3.4: ADD IY,rr sets MEMPTR = IY + 1
	result := uint32(c.IY) + uint32(value)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) // Preserve S, Z, PV
	if result > 0xFFFF {
		c.F |= FLAG_C
	}
	if ((c.IY & 0x0FFF) + (value & 0x0FFF)) > 0x0FFF {
		c.F |= FLAG_H
	}
	c.IY = uint16(result)
	c.F |= byte(c.IY>>8) & (FLAG_F3 | FLAG_F5) // Set undocumented flags
	c.tstates += 15
}

func (c *CPU) loadIYd() byte {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IY) + int32(d))
	c.WZ = addr     // per Sean Young §3.4: indexed addressing sets WZ = IY + d
	c.tstates += 12 // base minus the 3-T contended read cycle below
	return c.rd(addr)
}

func (c *CPU) storeIYd(value byte) {
	d := int8(c.readOperand())
	addr := uint16(int32(c.IY) + int32(d))
	c.WZ = addr     // per Sean Young §3.4: indexed addressing sets WZ = IY + d
	c.tstates += 16 // base minus the 3-T contended write cycle below
	c.wr(addr, value)
}

// executeDDCBInstruction handles DD CB prefixed instructions (IX
// indexed bit / shift / rotate operations). Every opcode in this
// space operates on (IX+d); for the shift/rotate, RES, and SET
// flavours, when the low 3 bits of the opcode are non-6 the result
// is ALSO copied into the named 8-bit register (undocumented but
// real-hardware behaviour). BIT n,(IX+d) ignores the register
// field.
func (c *CPU) executeDDCBInstruction(opcode byte, addr uint16) {
	// Per Sean Young §3.4, every DDCB instruction sets
	// MEMPTR = IX + d as part of the effective-address calculation.
	c.WZ = addr
	val := c.mem.Read(addr)
	bit := int((opcode >> 3) & 7)
	reg := int(opcode & 7)

	switch opcode >> 6 {
	case 0: // shift / rotate
		switch (opcode >> 3) & 7 {
		case 0:
			val = c.rlc(val)
		case 1:
			val = c.rrc(val)
		case 2:
			val = c.rl(val)
		case 3:
			val = c.rr(val)
		case 4:
			val = c.sla(val)
		case 5:
			val = c.sra(val)
		case 6:
			val = c.sll(val)
		case 7:
			val = c.srl(val)
		}
		c.mem.Write(addr, val)
		if reg != 6 {
			c.setRegister8(reg, val)
		}
	case 1: // BIT n,(IX+d)
		c.bit(bit, val) // bit() sets F3/F5 from the operand value
		if c.Variant == VariantZ80N {
			// T80N core (t80n_alu.vhd "BIT"): F3/F5 come from the operand
			// value when the opcode's reg field != 6, and are cleared to 0
			// when it is 6 (the canonical (IX+d) encoding). bit() already
			// set the operand bits; clear them for the reg==6 form.
			if reg == 6 {
				c.F &^= FLAG_F3 | FLAG_F5
			}
		} else {
			// NMOS: F3/F5 from the address-high byte (MEMPTR).
			c.F = (c.F &^ (FLAG_F3 | FLAG_F5)) | byte(addr>>8)&(FLAG_F3|FLAG_F5)
		}
		// BIT n,(IX+d) is 20 t-states (no memory writeback) vs the
		// 23 t-states of shift/RES/SET. Charge here and return so
		// the post-switch +23 isn't applied to BIT.
		c.tstates += 20
		return
	case 2: // RES n,(IX+d) [+ register copy]
		val &^= 1 << bit
		c.mem.Write(addr, val)
		if reg != 6 {
			c.setRegister8(reg, val)
		}
	case 3: // SET n,(IX+d) [+ register copy]
		val |= 1 << bit
		c.mem.Write(addr, val)
		if reg != 6 {
			c.setRegister8(reg, val)
		}
	}
	c.tstates += 23
}

func (c *CPU) executeFDInstruction(opcode byte) {
	switch opcode {
	// ADD IY,rr instructions
	case 0x09: // ADD IY,BC
		c.addIY(c.bc())
	case 0x19: // ADD IY,DE
		c.addIY(c.de())
	case 0x29: // ADD IY,IY
		c.addIY(c.IY)
	case 0x39: // ADD IY,SP
		c.addIY(c.SP)

	// LD IY,nn / LD (nn),IY / LD IY,(nn)
	case 0x21: // LD IY,nn
		c.IY = c.fetch16()
		c.tstates += 14
	case 0x22: // LD (nn),IY
		addr := c.fetch16()
		c.mem.Write(addr, byte(c.IY))
		c.mem.Write(addr+1, byte(c.IY>>8))
		c.tstates += 20
	case 0x2A: // LD IY,(nn)
		addr := c.fetch16()
		c.IY = uint16(c.mem.Read(addr)) | (uint16(c.mem.Read(addr+1)) << 8)
		c.tstates += 20

	// INC/DEC IY
	case 0x23: // INC IY
		c.IY++
		c.tstates += 10
	case 0x2B: // DEC IY
		c.IY--
		c.tstates += 10

	// INC/DEC (IY+d)
	case 0x34: // INC (IY+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IY) + int32(d))
		c.WZ = addr     // per Sean Young §3.4
		c.tstates += 17 // base minus read(3)+write(3) below
		val := c.inc(c.rd(addr))
		c.wr(addr, val)
	case 0x35: // DEC (IY+d)
		d := int8(c.readOperand())
		addr := uint16(int32(c.IY) + int32(d))
		c.WZ = addr     // per Sean Young §3.4
		c.tstates += 17 // base minus read(3)+write(3) below
		val := c.dec(c.rd(addr))
		c.wr(addr, val)

	// LD (IY+d),n
	case 0x36: // LD (IY+d),n
		d := int8(c.readOperand())
		n := c.readOperand()
		addr := uint16(int32(c.IY) + int32(d))
		c.WZ = addr     // per Sean Young §3.4
		c.tstates += 16 // base minus the 3-T contended write below
		c.wr(addr, n)

	// LD r,(IY+d) instructions - Load from (IY+d) to register.
	// loadIYd adds 15 t; LD r,(IY+d) total = 19 per Z80 spec, so
	// add the missing 4 for the FD prefix M1 cycle.
	case 0x46: // LD B,(IY+d)
		c.B = c.loadIYd()
		c.tstates += 4
	case 0x4E: // LD C,(IY+d)
		c.C = c.loadIYd()
		c.tstates += 4
	case 0x56: // LD D,(IY+d)
		c.D = c.loadIYd()
		c.tstates += 4
	case 0x5E: // LD E,(IY+d)
		c.E = c.loadIYd()
		c.tstates += 4
	case 0x66: // LD H,(IY+d)
		c.H = c.loadIYd()
		c.tstates += 4
	case 0x6E: // LD L,(IY+d)
		c.L = c.loadIYd()
		c.tstates += 4
	case 0x7E: // LD A,(IY+d)
		c.A = c.loadIYd()
		c.tstates += 4

	// LD (IY+d),r instructions - Store register to (IY+d)
	case 0x70: // LD (IY+d),B
		c.storeIYd(c.B)
	case 0x71: // LD (IY+d),C
		c.storeIYd(c.C)
	case 0x72: // LD (IY+d),D
		c.storeIYd(c.D)
	case 0x73: // LD (IY+d),E
		c.storeIYd(c.E)
	case 0x74: // LD (IY+d),H
		c.storeIYd(c.H)
	case 0x75: // LD (IY+d),L
		c.storeIYd(c.L)
	case 0x77: // LD (IY+d),A
		c.storeIYd(c.A)

	// Arithmetic/Logic operations with (IY+d)
	case 0x86: // ADD A,(IY+d)
		c.add(c.loadIYd())
		c.tstates += 4 // loadIYd already adds 15, need 4 more for total 19
	case 0x8E: // ADC A,(IY+d)
		c.adc(c.loadIYd())
		c.tstates += 4
	case 0x96: // SUB (IY+d)
		c.sub(c.loadIYd())
		c.tstates += 4
	case 0x9E: // SBC A,(IY+d)
		c.sbc(c.loadIYd())
		c.tstates += 4
	case 0xA6: // AND (IY+d)
		c.and(c.loadIYd())
		c.tstates += 4
	case 0xAE: // XOR (IY+d)
		c.xor(c.loadIYd())
		c.tstates += 4
	case 0xB6: // OR (IY+d)
		c.or(c.loadIYd())
		c.tstates += 4
	case 0xBE: // CP (IY+d)
		c.cp(c.loadIYd())
		c.tstates += 4

	// Stack operations
	case 0xE1: // POP IY
		c.IY = c.pop()
		c.tstates += 14
	case 0xE5: // PUSH IY
		c.push(c.IY)
		c.tstates += 15

	// Jump
	case 0xE9: // JP (IY)
		c.PC = c.IY
		c.tstates += 8

	// Exchange
	case 0xE3: // EX (SP),IY
		temp := c.IY
		c.IY = uint16(c.mem.Read(c.SP)) | (uint16(c.mem.Read(c.SP+1)) << 8)
		c.mem.Write(c.SP, byte(temp))
		c.mem.Write(c.SP+1, byte(temp>>8))
		c.WZ = c.IY // per Sean Young §A.1: WZ = new IY
		c.tstates += 23

	// Special
	case 0xF9: // LD SP,IY
		oldSP := c.SP
		c.SP = c.IY
		c.tstates += 10
		if c.OnSPLoad != nil {
			c.OnSPLoad(c.currentInstrPC, oldSP, c.SP)
		}

	// FD CB prefix (IY bit operations) — executeFDCBInstruction
	// charges the full 23 t (or 20 for BIT) per spec, just like
	// DD CB, so no extra t-states are added here.
	case 0xCB: // FD CB prefix
		d := int8(c.readOperand()) // Displacement comes first
		// FD CB d xx has TWO M1 cycles (FD and CB); the inner xx
		// opcode is an operand byte that does NOT increment R per
		// Sean Young §A.1.
		opcode := c.readOperand()
		c.executeFDCBInstruction(opcode, d)

	// Undocumented 8-bit IYh / IYl operations (FD-prefixed forms of any
	// base instruction that operates on H or L access IYh / IYl instead).
	case 0x24: // INC IYh
		c.setIYh(c.inc(c.iyh()))
		c.tstates += 8
	case 0x25: // DEC IYh
		c.setIYh(c.dec(c.iyh()))
		c.tstates += 8
	case 0x26: // LD IYh, n
		c.setIYh(c.readOperand())
		c.tstates += 11
	case 0x2C: // INC IYl
		c.setIYl(c.inc(c.iyl()))
		c.tstates += 8
	case 0x2D: // DEC IYl
		c.setIYl(c.dec(c.iyl()))
		c.tstates += 8
	case 0x2E: // LD IYl, n
		c.setIYl(c.readOperand())
		c.tstates += 11
	case 0x44: // LD B, IYh
		c.B = c.iyh()
		c.tstates += 8
	case 0x45: // LD B, IYl
		c.B = c.iyl()
		c.tstates += 8
	case 0x4C: // LD C, IYh
		c.C = c.iyh()
		c.tstates += 8
	case 0x4D: // LD C, IYl
		c.C = c.iyl()
		c.tstates += 8
	case 0x54: // LD D, IYh
		c.D = c.iyh()
		c.tstates += 8
	case 0x55: // LD D, IYl
		c.D = c.iyl()
		c.tstates += 8
	case 0x5C: // LD E, IYh
		c.E = c.iyh()
		c.tstates += 8
	case 0x5D: // LD E, IYl
		c.E = c.iyl()
		c.tstates += 8
	case 0x60: // LD IYh, B
		c.setIYh(c.B)
		c.tstates += 8
	case 0x61: // LD IYh, C
		c.setIYh(c.C)
		c.tstates += 8
	case 0x62: // LD IYh, D
		c.setIYh(c.D)
		c.tstates += 8
	case 0x63: // LD IYh, E
		c.setIYh(c.E)
		c.tstates += 8
	case 0x64: // LD IYh, IYh (no-op)
		c.tstates += 8
	case 0x65: // LD IYh, IYl
		c.setIYh(c.iyl())
		c.tstates += 8
	case 0x67: // LD IYh, A
		c.setIYh(c.A)
		c.tstates += 8
	case 0x68: // LD IYl, B
		c.setIYl(c.B)
		c.tstates += 8
	case 0x69: // LD IYl, C
		c.setIYl(c.C)
		c.tstates += 8
	case 0x6A: // LD IYl, D
		c.setIYl(c.D)
		c.tstates += 8
	case 0x6B: // LD IYl, E
		c.setIYl(c.E)
		c.tstates += 8
	case 0x6C: // LD IYl, IYh
		c.setIYl(c.iyh())
		c.tstates += 8
	case 0x6D: // LD IYl, IYl (no-op)
		c.tstates += 8
	case 0x6F: // LD IYl, A
		c.setIYl(c.A)
		c.tstates += 8
	case 0x7C: // LD A, IYh
		c.A = c.iyh()
		c.tstates += 8
	case 0x7D: // LD A, IYl
		c.A = c.iyl()
		c.tstates += 8
	case 0x84: // ADD A, IYh
		c.add(c.iyh())
		c.tstates += 8
	case 0x85: // ADD A, IYl
		c.add(c.iyl())
		c.tstates += 8
	case 0x8C: // ADC A, IYh
		c.adc(c.iyh())
		c.tstates += 8
	case 0x8D: // ADC A, IYl
		c.adc(c.iyl())
		c.tstates += 8
	case 0x94: // SUB IYh
		c.sub(c.iyh())
		c.tstates += 8
	case 0x95: // SUB IYl
		c.sub(c.iyl())
		c.tstates += 8
	case 0x9C: // SBC A, IYh
		c.sbc(c.iyh())
		c.tstates += 8
	case 0x9D: // SBC A, IYl
		c.sbc(c.iyl())
		c.tstates += 8
	case 0xA4: // AND IYh
		c.and(c.iyh())
		c.tstates += 8
	case 0xA5: // AND IYl
		c.and(c.iyl())
		c.tstates += 8
	case 0xAC: // XOR IYh
		c.xor(c.iyh())
		c.tstates += 8
	case 0xAD: // XOR IYl
		c.xor(c.iyl())
		c.tstates += 8
	case 0xB4: // OR IYh
		c.or(c.iyh())
		c.tstates += 8
	case 0xB5: // OR IYl
		c.or(c.iyl())
		c.tstates += 8
	case 0xBC: // CP IYh
		c.cp(c.iyh())
		c.tstates += 8
	case 0xBD: // CP IYl
		c.cp(c.iyl())
		c.tstates += 8

	default:
		// On real Z80 hardware, an FD prefix followed by an opcode that
		// doesn't use IY is a NONI: prefix consumes 4 T-states + one M1
		// cycle, base opcode executes on top. Per Sean Young §5.7.
		c.tstates += 4
		c.executeBaseInstruction(opcode)
	}
}

func (c *CPU) executeFDCBInstruction(opcode byte, d int8) {
	// FD CB prefix instructions - IY indexed bit operations.
	// Per Sean Young §3.4, every FDCB instruction sets
	// MEMPTR = IY + d as part of the effective-address calculation.
	addr := uint16(int32(c.IY) + int32(d))
	c.WZ = addr
	val := c.mem.Read(addr)

	bit := int((opcode >> 3) & 7)
	reg := int(opcode & 7)

	switch opcode >> 6 {
	case 0: // Rotate/shift operations
		switch (opcode >> 3) & 7 {
		case 0:
			val = c.rlc(val) // RLC (IY+d)
		case 1:
			val = c.rrc(val) // RRC (IY+d)
		case 2:
			val = c.rl(val) // RL (IY+d)
		case 3:
			val = c.rr(val) // RR (IY+d)
		case 4:
			val = c.sla(val) // SLA (IY+d)
		case 5:
			val = c.sra(val) // SRA (IY+d)
		case 6:
			val = c.sll(val) // SLL (IY+d)
		case 7:
			val = c.srl(val) // SRL (IY+d)
		}
		c.mem.Write(addr, val)
		if reg != 6 { // Copy to register if not (HL)
			c.setRegister8(reg, val)
		}
	case 1: // BIT n,(IY+d)
		c.bit(bit, val) // bit() sets F3/F5 from the operand value
		if c.Variant == VariantZ80N {
			// Z80N core: F3/F5 from the operand value when reg field != 6,
			// else cleared to 0 (see DDCB above; t80n_alu.vhd "BIT").
			if reg == 6 {
				c.F &^= FLAG_F3 | FLAG_F5
			}
		} else {
			c.F = (c.F &^ (FLAG_F3 | FLAG_F5)) | byte(addr>>8)&(FLAG_F3|FLAG_F5)
		}
		// BIT n,(IY+d) = 20 t (no memory writeback). Charge and
		// return so post-switch +23 isn't applied.
		c.tstates += 20
		return
	case 2: // RES operations
		val &= ^(1 << bit)
		c.mem.Write(addr, val)
		if reg != 6 {
			c.setRegister8(reg, val)
		}
	case 3: // SET operations
		val |= (1 << bit)
		c.mem.Write(addr, val)
		if reg != 6 {
			c.setRegister8(reg, val)
		}
	}
	c.tstates += 23
}

// fetch reads the next byte at PC and increments R (opcode fetch only).
func (c *CPU) fetch() byte {
	val := c.mem.Read(c.PC)
	c.PC++
	c.R = (c.R & 0x80) | ((c.R + 1) & 0x7f)
	c.instructionCount++
	// 28 MHz Z80N adds one CPU T-state per M1 fetch as a wait
	// state, modelling the FPGA's slower memory bus relative to
	// the CPU clock. Only Variant==VariantZ80N at speedSelect==3
	// (28 MHz) triggers it; every other configuration stays
	// bit-identical to its prior behaviour.
	if c.Variant == VariantZ80N && c.speedSelect == 0x03 {
		c.tstates++
	}
	return val
}

// readOperand reads the next byte at PC without incrementing R (for immediate operands).
func (c *CPU) readOperand() byte {
	val := c.mem.Read(c.PC)
	c.PC++
	return val
}

// readOperand16 reads 2 bytes (little-endian) without incrementing R.
func (c *CPU) readOperand16() uint16 {
	lo := uint16(c.readOperand())
	hi := uint16(c.readOperand())
	return (hi << 8) | lo
}

func (c *CPU) fetch16() uint16 {
	return c.readOperand16()
}

// Helper functions for register pairs
func (c *CPU) af() uint16 { return (uint16(c.A) << 8) | uint16(c.F) }
func (c *CPU) bc() uint16 { return (uint16(c.B) << 8) | uint16(c.C) }
func (c *CPU) de() uint16 { return (uint16(c.D) << 8) | uint16(c.E) }
func (c *CPU) hl() uint16 { return (uint16(c.H) << 8) | uint16(c.L) }

func (c *CPU) setAF(val uint16) { c.A = byte(val >> 8); c.F = byte(val) }
func (c *CPU) setBC(val uint16) { c.B = byte(val >> 8); c.C = byte(val) }
func (c *CPU) setDE(val uint16) { c.D = byte(val >> 8); c.E = byte(val) }
func (c *CPU) setHL(val uint16) { c.H = byte(val >> 8); c.L = byte(val) }

func (c *CPU) ixh() byte     { return byte(c.IX >> 8) }
func (c *CPU) ixl() byte     { return byte(c.IX) }
func (c *CPU) iyh() byte     { return byte(c.IY >> 8) }
func (c *CPU) iyl() byte     { return byte(c.IY) }
func (c *CPU) setIXh(v byte) { c.IX = (uint16(v) << 8) | (c.IX & 0xFF) }
func (c *CPU) setIXl(v byte) { c.IX = (c.IX & 0xFF00) | uint16(v) }
func (c *CPU) setIYh(v byte) { c.IY = (uint16(v) << 8) | (c.IY & 0xFF) }
func (c *CPU) setIYl(v byte) { c.IY = (c.IY & 0xFF00) | uint16(v) }

// initTables initializes the lookup tables for flag calculation
func (c *CPU) initTables() {
	// Initialize sz53Table: Sign, Zero, and undocumented flags (bits 3 and 5)
	for i := 0; i < 256; i++ {
		val := byte(i)
		flags := val & (FLAG_S | FLAG_F5 | FLAG_F3) // Copy S, F5, F3
		if val == 0 {
			flags |= FLAG_Z // Set Zero flag
		}
		c.sz53Table[i] = flags
	}

	// Initialize parity table
	for i := 0; i < 256; i++ {
		parity := 0
		val := i
		for val != 0 {
			parity ^= 1
			val &= val - 1 // Clear lowest set bit
		}
		if parity == 0 {
			c.parityTable[i] = FLAG_PV
		} else {
			c.parityTable[i] = 0
		}
	}

	// Halfcarry lookup tables. Indexed by 3 bits packed as
	// (r3 << 2) | (v3 << 1) | a3 — see the `lookup` expression in
	// add/adc/sub/sbc/cp. Derived from the bit-3 arithmetic
	// r3 = a3 + v3 + carryIn3 - 2*carryOut3 (and the sub equivalent);
	// matches FUSE's z80_tables.c.
	c.halfcarryAddTable = [8]byte{0, FLAG_H, FLAG_H, FLAG_H, 0, 0, 0, FLAG_H}
	c.halfcarrySubTable = [8]byte{0, 0, FLAG_H, 0, FLAG_H, 0, FLAG_H, FLAG_H}

	// Initialize overflow tables for ADD operations
	c.overflowAddTable = [8]byte{0, 0, 0, FLAG_PV, FLAG_PV, 0, 0, 0}
	c.overflowSubTable = [8]byte{0, FLAG_PV, 0, 0, 0, 0, FLAG_PV, 0}
}

// Stack operations
func (c *CPU) push(val uint16) {
	c.SP--
	c.mem.Write(c.SP, byte(val>>8))
	c.SP--
	c.mem.Write(c.SP, byte(val))
}

func (c *CPU) pop() uint16 {
	lo := uint16(c.mem.Read(c.SP))
	c.SP++
	hi := uint16(c.mem.Read(c.SP))
	c.SP++
	return (hi << 8) | lo
}

// Inc/Dec operations with proper flag handling
func (c *CPU) inc(val byte) byte {
	result := val + 1
	c.F = (c.F & FLAG_C) | c.sz53Table[result]
	if result == 0x80 {
		c.F |= FLAG_PV // Set overflow if went from 0x7F to 0x80
	}
	if (val & 0x0F) == 0x0F {
		c.F |= FLAG_H // Set half-carry if carry from bit 3
	}
	return result
}

func (c *CPU) dec(val byte) byte {
	result := val - 1
	c.F = (c.F & FLAG_C) | FLAG_N | c.sz53Table[result]
	if val == 0x80 {
		c.F |= FLAG_PV // Set overflow if went from 0x80 to 0x7F
	}
	if (val & 0x0F) == 0x00 {
		c.F |= FLAG_H // Set half-carry if borrow from bit 4
	}
	return result
}

// 16-bit arithmetic
func (c *CPU) add16(reg1, reg2 uint16) uint16 {
	c.WZ = reg1 + 1 // per Sean Young §3.4: ADD HL,rr sets MEMPTR = HL + 1
	result := uint32(reg1) + uint32(reg2)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | byte((result>>8)&(FLAG_F5|FLAG_F3))
	if result > 0xFFFF {
		c.F |= FLAG_C
	}
	if ((reg1 & 0x0FFF) + (reg2 & 0x0FFF)) > 0x0FFF {
		c.F |= FLAG_H
	}
	return uint16(result)
}

// Add operation with carry handling
func (c *CPU) add(val byte) {
	a := uint32(c.A)
	result := a + uint32(val)
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = 0
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarryAddTable[lookup&0x07] | c.overflowAddTable[lookup>>4] | c.sz53Table[c.A]
}

// Add with carry
func (c *CPU) adc(val byte) {
	a := uint32(c.A)
	carry := uint32(c.F & FLAG_C)
	result := a + uint32(val) + carry
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = 0
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarryAddTable[lookup&0x07] | c.overflowAddTable[lookup>>4] | c.sz53Table[c.A]
}

// Subtract operation
func (c *CPU) sub(val byte) {
	a := uint32(c.A)
	result := a - uint32(val)
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = FLAG_N
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarrySubTable[lookup&0x07] | c.overflowSubTable[lookup>>4] | c.sz53Table[c.A]
}

// Subtract with carry (borrow)
func (c *CPU) sbc(val byte) {
	a := uint32(c.A)
	carry := uint32(c.F & FLAG_C)
	result := a - uint32(val) - carry
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.A = byte(result)
	c.F = FLAG_N
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarrySubTable[lookup&0x07] | c.overflowSubTable[lookup>>4] | c.sz53Table[c.A]
}

// Compare operation
func (c *CPU) cp(val byte) {
	a := uint32(c.A)
	result := a - uint32(val)
	lookup := ((a & 0x88) >> 3) | ((uint32(val) & 0x88) >> 2) | ((result & 0x88) >> 1)
	c.F = FLAG_N | (val & (FLAG_F5 | FLAG_F3)) // Keep original value's F5, F3
	if result > 0xFF {
		c.F |= FLAG_C
	}
	c.F |= c.halfcarrySubTable[lookup&0x07] | c.overflowSubTable[lookup>>4]
	if byte(result) == 0 {
		c.F |= FLAG_Z
	}
	if (result & 0x80) != 0 {
		c.F |= FLAG_S
	}
}

// Logical operations
func (c *CPU) and(val byte) {
	c.A &= val
	c.F = FLAG_H | c.sz53Table[c.A] | c.parityTable[c.A]
}

func (c *CPU) or(val byte) {
	c.A |= val
	c.F = c.sz53Table[c.A] | c.parityTable[c.A]
}

func (c *CPU) xor(val byte) {
	c.A ^= val
	c.F = c.sz53Table[c.A] | c.parityTable[c.A]
}

// Rotate and shift operations
func (c *CPU) rlca() {
	c.A = (c.A << 1) | (c.A >> 7)
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & (FLAG_F5 | FLAG_F3 | FLAG_C))
}

func (c *CPU) rrca() {
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & FLAG_C)
	c.A = (c.A >> 1) | (c.A << 7)
	c.F |= c.A & (FLAG_F5 | FLAG_F3)
}

func (c *CPU) rla() {
	carry := c.F & FLAG_C
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | ((c.A >> 7) & FLAG_C)
	c.A = (c.A << 1) | carry
	c.F |= c.A & (FLAG_F5 | FLAG_F3)
}

func (c *CPU) rra() {
	carry := c.F & FLAG_C
	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_PV)) | (c.A & FLAG_C)
	c.A = (c.A >> 1) | (carry << 7)
	c.F |= c.A & (FLAG_F5 | FLAG_F3)
}

// CB instruction helper methods
func (c *CPU) rlc(val byte) byte {
	result := (val << 1) | (val >> 7)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) rrc(val byte) byte {
	result := (val >> 1) | (val << 7)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) rl(val byte) byte {
	carry := c.F & FLAG_C
	result := (val << 1) | carry
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) rr(val byte) byte {
	carry := c.F & FLAG_C
	result := (val >> 1) | (carry << 7)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) sla(val byte) byte {
	result := val << 1
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) sra(val byte) byte {
	result := (val >> 1) | (val & 0x80)
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) sll(val byte) byte {
	result := (val << 1) | 1
	c.F = c.sz53Table[result] | c.parityTable[result] | (val >> 7)
	return result
}

func (c *CPU) srl(val byte) byte {
	result := val >> 1
	c.F = c.sz53Table[result] | c.parityTable[result] | (val & 1)
	return result
}

func (c *CPU) bit(bit int, val byte) {
	mask := byte(1 << bit)
	result := val & mask
	c.F = (c.F & FLAG_C) | FLAG_H | (val & (FLAG_F5 | FLAG_F3))
	if result == 0 {
		c.F |= FLAG_Z | FLAG_PV
	}
	if bit == 7 && result != 0 {
		c.F |= FLAG_S
	}
}

func (c *CPU) res(bit int, val byte) byte {
	return val & ^(1 << bit)
}

func (c *CPU) set(bit int, val byte) byte {
	return val | (1 << bit)
}

// Register access helpers for CB instructions
func (c *CPU) getRegister8(reg int) byte {
	switch reg {
	case 0:
		return c.B
	case 1:
		return c.C
	case 2:
		return c.D
	case 3:
		return c.E
	case 4:
		return c.H
	case 5:
		return c.L
	case 6:
		return c.mem.Read(c.hl()) // (HL)
	case 7:
		return c.A
	}
	return 0
}

func (c *CPU) setRegister8(reg int, val byte) {
	switch reg {
	case 0:
		c.B = val
	case 1:
		c.C = val
	case 2:
		c.D = val
	case 3:
		c.E = val
	case 4:
		c.H = val
	case 5:
		c.L = val
	case 6:
		c.mem.Write(c.hl(), val) // (HL)
	case 7:
		c.A = val
	}
}

// 16-bit subtract with carry. Undocumented F3/F5 come from the
// HIGH byte of the result (bits 11 and 13), not the low byte.
func (c *CPU) sbc16(hl, value uint16) {
	result := uint32(hl) - uint32(value)
	if (c.F & FLAG_C) != 0 {
		result--
	}

	c.F = FLAG_N
	if (result & 0x10000) != 0 {
		c.F |= FLAG_C
	}
	if ((hl ^ value ^ uint16(result)) & 0x1000) != 0 {
		c.F |= FLAG_H
	}
	if (hl^value)&(hl^uint16(result))&0x8000 != 0 {
		c.F |= FLAG_PV
	}
	if (result & 0x8000) != 0 {
		c.F |= FLAG_S
	}
	if uint16(result) == 0 {
		c.F |= FLAG_Z
	}
	c.F |= byte(result>>8) & (FLAG_F5 | FLAG_F3)

	c.setHL(uint16(result))
}

// 16-bit add with carry. Undocumented F3/F5 come from the HIGH
// byte of the result (bits 11 and 13), not the low byte.
func (c *CPU) adc16(hl, value uint16) {
	result := uint32(hl) + uint32(value)
	if (c.F & FLAG_C) != 0 {
		result++
	}

	c.F = 0
	if (result & 0x10000) != 0 {
		c.F |= FLAG_C
	}
	if ((hl ^ value ^ uint16(result)) & 0x1000) != 0 {
		c.F |= FLAG_H
	}
	if ^(hl^value)&(hl^uint16(result))&0x8000 != 0 {
		c.F |= FLAG_PV
	}
	if (result & 0x8000) != 0 {
		c.F |= FLAG_S
	}
	if uint16(result) == 0 {
		c.F |= FLAG_Z
	}
	c.F |= byte(result>>8) & (FLAG_F5 | FLAG_F3)

	c.setHL(uint16(result))
}

// Block load operations
func (c *CPU) ldi() {
	// Load and increment. Timing: 2×M1 (8) + MR (HL) (3) + MW (DE) (3)
	// + 2 internal cycles at the write address (2) = 16 T. Both memory
	// accesses route through c.rd/c.wr so contended screen RAM applies
	// its hold per access; the 2 internal no-MREQ cycles contend at DE
	// (FUSE z80_ops.c).
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	val := c.rd(c.hl())
	de := c.de()
	c.wr(de, val)
	c.exec(de, 2)
	c.setHL(c.hl() + 1)
	c.setDE(c.de() + 1)
	c.setBC(c.bc() - 1)

	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_C))
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	val += c.A
	c.F |= val & FLAG_F3
	c.F |= ((val << 4) & FLAG_F5)
}

func (c *CPU) ldd() {
	// Load and decrement. Same timing structure as ldi (16 T).
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	val := c.rd(c.hl())
	de := c.de()
	c.wr(de, val)
	c.exec(de, 2)
	c.setHL(c.hl() - 1)
	c.setDE(c.de() - 1)
	c.setBC(c.bc() - 1)

	c.F = (c.F & (FLAG_S | FLAG_Z | FLAG_C))
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	val += c.A
	c.F |= val & FLAG_F3
	c.F |= ((val << 4) & FLAG_F5)
}

func (c *CPU) ldir() {
	// Load, increment and repeat
	c.ldi()
	if c.bc() != 0 {
		c.PC -= 2 // Repeat the instruction
		// Extra 5 internal (no-MREQ) cycles on the taken path; FUSE
		// contends them at the address just written to ((DE-1) since
		// ldi already incremented DE).
		c.exec(c.de()-1, 5)
		// Per Sean Young §4.2: on every non-final iteration the
		// repeating block instruction sets MEMPTR = PC + 1 (PC has
		// already been adjusted by -2, so PC+1 is the address of the
		// LDIR opcode byte itself).
		c.WZ = c.PC + 1
	}
}

func (c *CPU) lddr() {
	// Load, decrement and repeat
	c.ldd()
	if c.bc() != 0 {
		c.PC -= 2           // Repeat the instruction
		c.exec(c.de()+1, 5) // 5 internal cycles at the written address
		c.WZ = c.PC + 1     // per Sean Young §4.2
	}
}

// Block search operations
func (c *CPU) cpi() {
	// Compare and increment. Timing: 2×M1 (8) + MR (HL) (3) + 5
	// internal cycles at HL (5) = 16 T. The (HL) read routes through
	// c.rd so contended screen RAM applies its hold.
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	hl := c.hl()
	val := c.rd(hl)
	c.exec(hl, 5)
	result := c.A - val

	c.setHL(c.hl() + 1)
	c.setBC(c.bc() - 1)
	c.WZ++ // per Sean Young §3.4: CPI sets MEMPTR += 1

	c.F = (c.F & FLAG_C) | FLAG_N
	if result == 0 {
		c.F |= FLAG_Z
	}
	if (result & 0x80) != 0 {
		c.F |= FLAG_S
	}
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	if ((c.A ^ val ^ result) & 0x10) != 0 {
		c.F |= FLAG_H
	}

	if c.Variant == VariantZ80N {
		// The Next's Z80N takes the undocumented F3/F5 of the block-compare
		// from the operand byte read (bits 3,5), like a normal CP — NOT from
		// NMOS's n = A-(HL)-H result byte. Verified bit-exact against the
		// tbblue T80N core via GHDL golden vectors (testdata/z80n_golden.txt).
		c.F |= val & (FLAG_F3 | FLAG_F5)
		return
	}

	// F3 and F5 flags are set from (A - (HL) - H flag)
	temp := result
	if (c.F & FLAG_H) != 0 {
		temp--
	}
	c.F |= temp & FLAG_F3
	c.F |= ((temp << 4) & FLAG_F5)
}

func (c *CPU) cpd() {
	// Compare and decrement. Same timing structure as cpi (16 T).
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	hl := c.hl()
	val := c.rd(hl)
	c.exec(hl, 5)
	result := c.A - val

	c.setHL(c.hl() - 1)
	c.setBC(c.bc() - 1)
	c.WZ-- // per Sean Young §3.4: CPD sets MEMPTR -= 1

	c.F = (c.F & FLAG_C) | FLAG_N
	if result == 0 {
		c.F |= FLAG_Z
	}
	if (result & 0x80) != 0 {
		c.F |= FLAG_S
	}
	if c.bc() != 0 {
		c.F |= FLAG_PV
	}
	if ((c.A ^ val ^ result) & 0x10) != 0 {
		c.F |= FLAG_H
	}

	if c.Variant == VariantZ80N {
		// The Next's Z80N takes the undocumented F3/F5 of the block-compare
		// from the operand byte read (bits 3,5), like a normal CP — NOT from
		// NMOS's n = A-(HL)-H result byte. Verified bit-exact against the
		// tbblue T80N core via GHDL golden vectors (testdata/z80n_golden.txt).
		c.F |= val & (FLAG_F3 | FLAG_F5)
		return
	}

	// F3 and F5 flags are set from (A - (HL) - H flag)
	temp := result
	if (c.F & FLAG_H) != 0 {
		temp--
	}
	c.F |= temp & FLAG_F3
	c.F |= ((temp << 4) & FLAG_F5)
}

func (c *CPU) cpir() {
	// Compare, increment and repeat
	c.cpi()
	if c.bc() != 0 && (c.F&FLAG_Z) == 0 {
		c.PC -= 2           // Repeat the instruction
		c.exec(c.hl()-1, 5) // 5 internal cycles at the address just read
		c.WZ = c.PC + 1     // per Sean Young §4.2
	}
}

func (c *CPU) cpdr() {
	// Compare, decrement and repeat
	c.cpd()
	if c.bc() != 0 && (c.F&FLAG_Z) == 0 {
		c.PC -= 2           // Repeat the instruction
		c.exec(c.hl()+1, 5) // 5 internal cycles at the address just read
		c.WZ = c.PC + 1     // per Sean Young §4.2
	}
}

// Block output operations
func (c *CPU) outi() {
	// Output and increment. Per Sean Young §3.4, on real hardware B is
	// decremented BEFORE the I/O write, so the port's high byte is the
	// post-decrement B (the opposite of the IN block ops, which use the
	// pre-decrement B). The SAM Coupé boot ROM relies on this when it loads
	// its 16-entry CLUT via OTDR (B = 0x10 → 0x00); writing with the
	// pre-decrement B shifts the whole palette by one. WZ = BC + 1 with the
	// post-decrement BC.
	//
	// Timing: 2×M1 (8) + 1 internal at I:R (1) + MR (HL) (3) + IO-out
	// machine cycle (4 base). The (HL) read routes through c.rd so
	// contended screen RAM applies its hold; ContendPort still adds the
	// ULA-port contention on top of the IO base. Total non-ULA,
	// uncontended = 8 + 1 + 3 + 4 = 16 T.
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	c.exec(c.ir(), 1)
	val := c.rd(c.hl())
	c.B-- // decrement before the OUT: the port high byte is the new B
	c.mem.ContendPort(c.bc())
	c.ula.WritePort(c.bc(), val)
	c.tstates += 4 // IO-out machine-cycle base (ContendPort adds ULA contention)
	c.setHL(c.hl() + 1)
	c.WZ = c.bc() + 1

	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	if (c.B & 0x80) != 0 {
		c.F |= FLAG_S
	}
	c.F |= FLAG_N

	// Complex flag calculation for block I/O
	temp := uint16(val) + uint16(c.L)
	if temp > 255 {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[(byte(temp)&7)^c.B]
}

func (c *CPU) outd() {
	// Output and decrement. Per Sean Young §3.4, B is decremented BEFORE the
	// I/O write, so the port high byte is the post-decrement B (see outi).
	// WZ = BC - 1 with the post-decrement BC.
	//
	// Timing: same structure as outi (16 T base).
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	c.exec(c.ir(), 1)
	val := c.rd(c.hl())
	c.B-- // decrement before the OUT: the port high byte is the new B
	c.mem.ContendPort(c.bc())
	c.ula.WritePort(c.bc(), val)
	c.tstates += 4 // IO-out machine-cycle base (ContendPort adds ULA contention)
	c.setHL(c.hl() - 1)
	c.WZ = c.bc() - 1

	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	if (c.B & 0x80) != 0 {
		c.F |= FLAG_S
	}
	c.F |= FLAG_N

	// Complex flag calculation for block I/O
	temp := uint16(val) + uint16(c.L)
	if temp > 255 {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[(byte(temp)&7)^c.B]
}

func (c *CPU) otir() {
	// Output, increment and repeat
	c.outi()
	if c.B != 0 {
		c.PC -= 2         // Repeat the instruction
		c.exec(c.bc(), 5) // 5 internal cycles at BC (FUSE z80_ops.c)
		c.WZ = c.PC + 1   // per Sean Young §4.2
	}
}

func (c *CPU) otdr() {
	// Output, decrement and repeat
	c.outd()
	if c.B != 0 {
		c.PC -= 2         // Repeat the instruction
		c.exec(c.bc(), 5) // 5 internal cycles at BC (FUSE z80_ops.c)
		c.WZ = c.PC + 1   // per Sean Young §4.2
	}
}

// Block input operations
func (c *CPU) ini() {
	// Input and increment. Flag semantics per Sean Young's
	// "Undocumented Z80 Documented" (the canonical reference for
	// the block-IO flag computations the Zilog manual leaves
	// unspecified):
	//
	//   k = val + ((C + 1) AND $FF)        ; modular C arithmetic
	//   S = B post-decrement bit 7
	//   Z = B post-decrement == 0
	//   F5 = B post-decrement bit 5         ; undocumented
	//   H = k > $FF
	//   F3 = B post-decrement bit 3         ; undocumented
	//   P/V = parity( (k AND 7) XOR B )
	//   N = val bit 7                        ; NOT always-set
	//   C = k > $FF
	//
	// N is the value's bit 7, not unconditionally 1 as some
	// simplified references claim; F5/F3 mirror B post-decrement.
	//
	// Timing: 2×M1 (8) + 1 internal at I:R (1) + IO-in machine cycle
	// (4 base) + MW (HL) (3). The (HL) write routes through c.wr so
	// contended screen RAM applies its hold; ContendPort still adds
	// the ULA-port contention on top of the IO base. Total non-ULA,
	// uncontended = 8 + 1 + 4 + 3 = 16 T.
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	c.exec(c.ir(), 1)
	c.WZ = c.bc() + 1 // per Sean Young §3.4: INI sets MEMPTR = BC + 1 (pre-decrement)
	c.mem.ContendPort(c.bc())
	val, _ := c.ula.ReadPort(c.bc())
	c.tstates += 4 // IO-in machine-cycle base (ContendPort adds ULA contention)
	c.wr(c.hl(), val)
	c.setHL(c.hl() + 1)
	c.B--

	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	// S, F5, F3 all come from B post-decrement directly via the
	// sz53Table (which packs S=B7, F5=B5, F3=B3). Z is set above.
	c.F |= c.sz53Table[c.B] &^ FLAG_Z // sz53Table includes Z for 0; we already set ours

	// k = val + ((C + 1) AND $FF) — modular at 8 bits.
	k := uint16(val) + uint16(byte(c.C+1))
	if k > 0xFF {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[byte(k&7)^c.B]
	// N from bit 7 of the value read (NOT always set).
	if val&0x80 != 0 {
		c.F |= FLAG_N
	}
}

func (c *CPU) ind() {
	// Input and decrement. Same flag semantics as INI except the
	// modular C arithmetic uses C-1 instead of C+1.
	// Timing: same structure as ini (16 T base).
	c.m1(c.currentInstrPC)
	c.m1(c.currentInstrPC + 1)
	c.exec(c.ir(), 1)
	c.WZ = c.bc() - 1 // per Sean Young §3.4: IND sets MEMPTR = BC - 1 (pre-decrement)
	c.mem.ContendPort(c.bc())
	val, _ := c.ula.ReadPort(c.bc())
	c.tstates += 4 // IO-in machine-cycle base (ContendPort adds ULA contention)
	c.wr(c.hl(), val)
	c.setHL(c.hl() - 1)
	c.B--

	c.F = 0
	if c.B == 0 {
		c.F |= FLAG_Z
	}
	c.F |= c.sz53Table[c.B] &^ FLAG_Z

	k := uint16(val) + uint16(byte(c.C-1))
	if k > 0xFF {
		c.F |= FLAG_H | FLAG_C
	}
	c.F |= c.parityTable[byte(k&7)^c.B]
	if val&0x80 != 0 {
		c.F |= FLAG_N
	}
}

func (c *CPU) inir() {
	// Input, increment and repeat
	c.ini()
	if c.B != 0 {
		c.PC -= 2
		c.exec(c.bc(), 5) // 5 internal cycles at BC (FUSE z80_ops.c)
		c.WZ = c.PC + 1   // per Sean Young §4.2
	}
}

func (c *CPU) indr() {
	// Input, decrement and repeat
	c.ind()
	if c.B != 0 {
		c.PC -= 2
		c.exec(c.bc(), 5) // 5 internal cycles at BC (FUSE z80_ops.c)
		c.WZ = c.PC + 1   // per Sean Young §4.2
	}
}

// Rotate decimal operations
func (c *CPU) rrd() {
	// Rotate right decimal. Per Sean Young §3.4: MEMPTR = HL + 1.
	val := c.mem.Read(c.hl())
	temp := c.A
	c.A = (c.A & 0xF0) | (val & 0x0F)
	c.mem.Write(c.hl(), (val>>4)|(temp<<4))
	c.WZ = c.hl() + 1

	c.F = (c.F & FLAG_C) | c.sz53Table[c.A] | c.parityTable[c.A]
}

func (c *CPU) rld() {
	// Rotate left decimal. Per Sean Young §3.4: MEMPTR = HL + 1.
	val := c.mem.Read(c.hl())
	temp := c.A
	c.A = (c.A & 0xF0) | (val >> 4)
	c.mem.Write(c.hl(), (val<<4)|(temp&0x0F))
	c.WZ = c.hl() + 1

	c.F = (c.F & FLAG_C) | c.sz53Table[c.A] | c.parityTable[c.A]
}

// IntFireCount counts how often interrupt() was successfully
// taken (IFF1 was 1 at entry). Diagnostic only.
var IntFireCount uint64

// IntRejectCount counts how often interrupt() was called but
// rejected (IFF1=false at the moment of the call). Diagnostic
// only — surfaced via the debugger's `irq-stats` command so
// "IRQ pulse missed every frame" patterns show up directly.
var IntRejectCount uint64

// LastIntPC, LastIntFrame, LastRejectPC, LastRejectFrame snapshot
// the PC at the last interrupt() entry (taken or rejected). Read
// by `irq-stats` to anchor the count to a recent moment in the
// trace.
var (
	LastIntPC       uint16
	LastIntInsns    uint64
	LastRejectPC    uint16
	LastRejectInsns uint64
)

// OnInterruptTaken is invoked at the end of a successful
// interrupt() call (IFF1 was set and the IRQ was acknowledged).
// Nil by default; the remote debugger installs it to implement
// `catch irq`. Called with the original PC pre-push so callers
// can log "IRQ at PC=X".
var OnInterruptTakenHook func(pcBeforePush uint16)

// Interrupt handling. Callers gate this on c.IFF1 before invocation
// (see ExecuteFrame); the IFF1 check inside is the safety net for
// any future caller that forgets. Rejection counting happens at the
// call sites — guarded paths track which branch fired.
func (c *CPU) interrupt() {
	if !c.IFF1 {
		return
	}
	IntFireCount++
	LastIntPC = c.PC
	LastIntInsns = c.instructionCount
	if OnInterruptTakenHook != nil {
		OnInterruptTakenHook(c.PC)
	}

	// Acknowledge: a maskable interrupt clears BOTH IFF1 and IFF2 —
	// faithful Z80 behaviour (an accepted interrupt clears both IFF1 and
	// IFF2; Zilog manual). A NextZXOS/48K IM1 handler re-enables
	// interrupts with an explicit `EI` before `RET`/`RETI`, so clearing
	// both here does not strand interrupts off (EI sets both, and
	// RETI's `IFF1=IFF2` then keeps them on).
	c.IFF1, c.IFF2 = false, false

	// Exit halt state if in it
	c.Halted = false

	c.BranchSource = 6 // SourceInt — for the debugger's history
	c.BranchFrom = c.PC

	// Per Sean Young §5: interrupt acceptance has one M1 cycle
	// (the INTA / vector-byte fetch) which bumps R like any other
	// M1. NMI already does this; INT was missing.
	c.R = (c.R & 0x80) | ((c.R + 1) & 0x7F)
	c.instructionCount++

	switch c.IM {
	case 0:
		// IM0: Execute RST 38h (like RST instruction)
		c.push(c.PC)
		c.PC = 0x38
		c.tstates += 13
	case 1:
		// IM1: Execute RST 38h (same as IM0)
		c.push(c.PC)
		c.PC = 0x38
		c.tstates += 13
	case 2:
		// IM2: Indirect jump using I register and data bus value.
		// On the ZX Spectrum, the ULA places 0xFF on the bus during INTA.
		addr := uint16(c.I)<<8 | uint16(c.IM2Vector)
		low := c.mem.Read(addr)
		high := c.mem.Read(addr + 1)
		c.push(c.PC)
		c.PC = uint16(high)<<8 | uint16(low)
		c.tstates += 19
	}
	// Per Sean Young §3.4: interrupt acceptance modifies MEMPTR
	// like CALL — the post-jump PC is loaded into WZ.
	c.WZ = c.PC
}

// NMI (Non-Maskable Interrupt) handling - for Multiface red button
func (c *CPU) NMI() {
	// NMI cannot be disabled and always executes.
	//
	// Standard Z80 documentation says IFF2 = IFF1 before clearing IFF1,
	// but we deliberately leave IFF2 unchanged (matching FUSE's approach).
	// Rationale: the Multiface 3 ROM re-enables interrupts with EI and
	// never uses RETN, so it does not rely on IFF2 to restore IFF1.
	// If we copied IFF1 into IFF2 here, an NMI that fires while IFF1 is
	// false (e.g. inside a DI section) would set IFF2=false, and any
	// subsequent RETN in the interrupted program would restore IFF1=false
	// — permanently disabling maskable interrupts and crashing the +3
	// (which needs IM2 interrupts for keyboard scanning).
	c.IFF1 = false

	// Exit halt state if in it
	c.Halted = false

	// Bump the R register like a real Z80 M1 cycle
	c.R = (c.R & 0x80) | ((c.R + 1) & 0x7F)
	c.instructionCount++

	c.BranchSource = 7 // SourceNmi — for the debugger's history
	c.BranchFrom = c.PC

	// Stackless NMI (NextReg $C0 bit 3): also store the return address in
	// NR$C2/$C3 so a RETN can return there. The stack push still happens
	// (SP stays correct) — only RETN's PC source changes (the NMI path
	// writes $C2/$C3 then takes the NMI normally).
	if c.NMIStackless && c.StacklessWriteNR != nil {
		c.StacklessWriteNR(0xC2, byte(c.PC))
		c.StacklessWriteNR(0xC3, byte(c.PC>>8))
	}

	// NMI always jumps to 0x0066
	c.push(c.PC)
	c.PC = 0x0066
	c.WZ = c.PC // per Sean Young §3.4: NMI acceptance behaves like CALL
	c.tstates += 11
}
