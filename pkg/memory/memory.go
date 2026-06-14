package memory

import (
	"fmt"
	"os"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

const (
	// PageSize is the size of a memory page in bytes (16KB). This is
	// the classic Spectrum 16K paging granularity used by 7FFD/1FFD.
	PageSize = 0x4000
	// BankSize is the size of one 8K bank — half a classic 16K page.
	// The Spectrum Next's MMU pages at this granularity (NextRegs
	// 0x50–0x57, eight 8K slots covering the full 64K Z80 address
	// space). Pre-Next models keep using PageSize; BankSize is a
	// scaffolding constant Sprint 2 will build the real 8K MMU on
	// top of, so callers that need to enumerate banks in 8K units
	// (e.g. the upcoming Layer 2 paging or.NEX loader) don't have
	// to litter `PageSize/2` magic numbers.
	BankSize = 0x2000
	// BanksPer16KPage is two — kept as a named constant so the
	// Sprint 2 MMU code can express "this 16K page is two adjacent
	// 8K banks" without a bare integer.
	BanksPer16KPage = PageSize / BankSize
	// RAMSize48K is the total RAM for a 48K Spectrum (48KB).
	RAMSize48K = 0xC000
	// RAMSize128K is the total RAM for a 128K Spectrum (128KB).
	RAMSize128K = 0x20000
)

// SlotIndex returns which 8K slot (0..7) covers the given Z80 address.
// Sprint 2's MMU dispatch routes through this so the 8K page-table
// lookup has one canonical source. On pre-Next models the result is
// informational only (the active read/write paths still use the 16K
// PageSize-based maps).
func SlotIndex(addr uint16) int {
	return int(addr >> 13)
}

// SlotOffset returns the byte offset within an 8K slot for the given
// Z80 address. Pair with SlotIndex when building 8K-granularity
// lookups.
func SlotOffset(addr uint16) uint16 {
	return addr & (BankSize - 1)
}

// Memory represents the computer's memory, including RAM and ROM.
//
// The ram array is sized for the Spectrum Next (128 × 16K = 2 MB)
// even on classic models. Non-Next models leave banks 8..127 as
// nil slices (zero allocation), so the worst-case memory cost on a
// 48K Spectrum is 120 nil slice headers (~960 bytes). The Next
// constructor in pkg/memory/memory.go allocates banks 8..127 only
// when model == roms.ModelNext, keeping classic models bit-identical
// in heap footprint to before this change.
type Memory struct {
	// RAM banks. Banks 0..7 are always allocated (classic 128K
	// layout). Banks 8..127 are allocated only for ModelNext to
	// support.NEX files using extended banks and 8K-MMU dispatch
	// into the full 2 MB.
	ram [128][]byte
	// ROM banks (4 pages of 16KB for various models)
	rom [4][]byte

	// memoryPageReadMap maps the 4 memory slots (0-3) to the actual memory page index.
	// Slot 0: 0x0000-0x3FFF
	// Slot 1: 0x4000-0x7FFF
	// Slot 2: 0x8000-0xBFFF
	// Slot 3: 0xC000-0xFFFF
	memoryPageReadMap [4]int
	// memoryPageWriteMap is similar to memoryPageReadMap but for write operations.
	memoryPageWriteMap [4]int

	// PagingEnabled indicates if memory paging is allowed.
	PagingEnabled bool
	// ScreenPage is the RAM page currently used for the display (5 or 7).
	ScreenPage int
	// Plus3 special paging mode
	specialPaging bool
	port7FFD      byte
	port1FFD      byte

	// ROM manager for handling multiple ROM types
	romManager *roms.ROMManager
	// Current Spectrum model
	currentModel roms.SpectrumModel

	// Contention: T-state counter reference set by CPU each frame
	ContentionEnabled bool
	TStates           *uint64 // Pointer to CPU's T-state counter

	// RAMContentionDisabled, when set, overrides ContentionEnabled
	// and suppresses the per-T-state contention pattern on RAM
	// accesses. ModelNext writes this via NextReg 0x08 bit 1; on
	// other models it stays false.
	RAMContentionDisabled bool

	// SpeedMultiplier returns the CPU speed multiplier relative
	// to the 3.5 MHz reference. ContendMemory divides the raw
	// CPU T-state counter by this value so the contention
	// pattern still fires once per ULA scanline regardless of
	// turbo. Set by ModelNext construction to cpu.SpeedMultiplier;
	// nil on every other model (treated as 1).
	//
	// NOTE: pkg/ula's scanline / audio-event tracking does NOT
	// yet honour this multiplier — turbo-mode border timing and
	// audio offsets are wrong above 3.5 MHz. Sprint 6 (video) is
	// the natural place to land the rest of the ULA-side scaling.
	SpeedMultiplier func() int

	// PeripheralRead is called for reads in the ROM area (0x0000-0x3FFF).
	// If it returns (value, true), the peripheral ROM overrides the normal ROM.
	PeripheralRead func(addr uint16) (byte, bool)

	// PeripheralWrite is called for writes in the peripheral area.
	// If it returns true, the peripheral handled the write.
	PeripheralWrite func(addr uint16, val byte) bool

	// WriteObserver, when non-nil, fires AFTER every Write that
	// touches RAM (peripheral-handled writes still call it too, so
	// observers see the value that was offered to the peripheral
	// even if the peripheral absorbed the write). Read-only —
	// debug-only tool. Pass nil to disable.
	WriteObserver func(addr uint16, val byte, pc uint16)

	// --- Uninitialised-RAM read detector (debug-only) ---
	//
	// TrackUninit gates write-tracking + uninitialised-read detection
	// over the unified RAM pool. Default false → zero hot-path cost.
	// EnableUninitTracking() turns it on and marks all RAM as
	// "not yet written by the guest"; the power-on cold-RAM fill does
	// NOT count as a guest write, so a Read of any byte the guest has
	// not written since fires UninitReadObserver. This answers the
	// cold-boot question "is this garbage pointer sourced from
	// uninitialised RAM (a missing init step) or computed from written
	// bytes (a logic bug)?". See DEBUGGER.md.
	TrackUninit bool
	// ramWritten shadows m.ram: ramWritten[b][o] == true iff a guest
	// Write touched ram[b][o] since EnableUninitTracking. Allocated
	// lazily by EnableUninitTracking; nil banks are simply not tracked.
	ramWritten [128][]bool
	// UninitReadObserver fires on every Read of a RAM byte never
	// written by the guest since tracking was enabled. addr is the CPU
	// address; bank16k/off locate the byte in the unified pool. PC is
	// not passed — the installing closure captures the CPU and reads
	// cpu.PC (mirrors WriteObserver). nil disables.
	UninitReadObserver func(addr uint16, bank16k int, off uint16)

	// PagingFrozen: when true, PageMemory and PageMemoryPlus3 are no-ops.
	// Used by peripherals (Multiface) to prevent paging changes while
	// their ROM is overlaying the memory map.
	PagingFrozen bool

	// fpgaBootROM is the 8 KB Spectrum Next FPGA bootrom (typically
	// tbblue_loader.rom). When fpgaBootROMActive is true, reads in
	// $0000-$3FFF read from this slice (mirrored — bootrom byte
	// offset is addr % 8192), bypassing the normal ROM-bank /
	// divMMC overlay paths. Writes are ignored.
	//
	// The bootrom mode clears when NextReg $03 (machine type) is
	// written by the loader after it has loaded the firmware
	// payload and is ready to hand off. This matches the reference Spectrum Next
	// tbblue.c behaviour (tbblue_bootrom.v=1/0).
	fpgaBootROM       []byte
	fpgaBootROMActive bool

	// Spectrum Next config-mode RAM-page mapping at $0000-$3FFF.
	//
	// On ModelNext, the machine boots in "configuration mode"
	// (NextReg $03 bits 2-0 = 0). While in this mode, the 16 KB
	// window at $0000-$3FFF is a scratchpad mapped to whichever
	// 16 KB RAM page NextReg $04 (REG_RAMPAGE) selects. The FPGA
	// bootrom (when loaded) overlays the window for its own
	// reads, but writes still hit the selected RAM page — the
	// loader uses this to copy boot.bin into $6000 (no overlay
	// there) and the post-handoff firmware (boot.bin) uses it
	// to copy each ROM image into the page that the chosen
	// machine personality will swap in once config mode exits.
	//
	// Config mode exits when guest code writes NextReg $03 with
	// non-zero bits 2-0 (a real machine personality). At that
	// point the window reverts to whatever the personality's
	// classic-paging map dictates. The page selection is latched
	// so a guest can re-enter config mode (machine type 0) and
	// pick up the previous page.
	//
	// Cross-reference:
	// - the reference Spectrum Next emulator `src/machines/tbblue.c:5013` calls
	// `tbblue_set_memory_pages()` from the NextReg $03 OnWrite
	// handler whenever bits 2-0 = 0.
	// - TBBlue `src/firmware/app/src/boot.c` loadFile() sets
	// REG_RAMPAGE then issues 16 KB of writes into $0000-$3FFF
	// for every ROM file it pulls off the SD card.
	configModeActive  bool
	configModeRAMPage byte
	configWriteHook   func(page byte, addr uint16, val byte)
	// ramWriteHook, when non-nil, fires on every successful RAM
	// write where the destination bank index matches. Used by
	// debug paths (e.g. tracking Browser binary load destination
	// after NextZXOS bank-0 init's SD-read step).
	ramWriteHook func(bank int, addr uint16, val byte)
	// ramReadHook, when non-nil, fires on every successful RAM
	// READ returned via the Memory.Read path (mirrors ramWriteHook).
	// Used by the debugger's read-watchpoint to halt on the exact
	// instruction that reads a watched address — e.g. the byte a
	// validation routine reads from a buffer and rejects.
	ramReadHook func(bank int, addr uint16, val byte)
	// allReadHook, when non-nil, fires on EVERY Read (any address,
	// ROM/RAM/divMMC alike) with the logical address + value. Used by
	// --read-tape-all for bank-agnostic instruction-level lockstep vs a
	// reference emulator: ROM reads match by construction, so the first
	// value divergence is the functional root cause regardless of how
	// the code is banked through a window (e.g. the divMMC $2000-$3FFF
	// dot-command window).
	allReadHook func(addr uint16, val byte)
	// divMMCRAM, when non-nil, routes config-mode reads/writes
	// for NR$04 16K-bank values $08-$0B (RAMPAGE_RAMDIVMMC) into
	// divMMC's 64 KB RAM. Wired from cmd/zx_go after the divMMC
	// pager is constructed; nil-safe so unit tests that don't need
	// divMMC still work.
	divMMCRAM DivMMCAccessor

	// Spectrum Next Alt-ROM mapping (NextReg $8C).
	//
	// The Alt-ROM facility lets guest code substitute a 32 KB
	// alternative ROM image (two 16 KB banks: Alt-ROM 0 for the
	// 128K-style ROM, Alt-ROM 1 for the 48K-style ROM) in place of
	// the normal ROMs at $0000-$3FFF, or redirect WRITES to that
	// area into the Alt-ROM RAM (a "RAM disk masquerading as ROM").
	// The NextZXOS bank-2 init writes $C0 to $8C — bit 7 (enable
	// alt-rom) + bit 6 (alt-rom visible during writes only) — to
	// patch its own ROM at runtime without affecting reads.
	//
	// `altROMReg` shadows the last-written $8C value:
	// bit 7 = alt-rom enabled
	// bit 6 = 1 → alt-rom active on writes only; 0 → on reads only
	// bit 5 = lock ROM1 (force the 48K-style alt-rom)
	// bit 4 = lock ROM0 (force the 128K-style alt-rom)
	//
	// `altROM0` and `altROM1` are the two 16 KB Alt-ROM banks. They
	// start zero-filled; the guest writes patches into them via the
	// $C0 (write-redirect) mode, then reads them back via the $80
	// (read-redirect) mode.
	altROMReg byte
	altROM0   [PageSize]byte
	altROM1   [PageSize]byte

	// Next Multiface overlay. When the MF NMI fires (NR$02 bit 3 or the
	// M1 button), the FPGA pages the Multiface in over $0000-$3FFF:
	// mfROM (= enNextMF.rom, 8 KB) at $0000-$1FFF and 8 KB of MF scratch
	// RAM at $2000-$3FFF. The NMI vectors to the MF ROM's $0066 handler
	// (JP $006D; LD ($3FB1),SP; LD SP,$3FC4; …), which saves state into
	// the MF RAM and loads the editor snapshot — this is how NextZXOS's
	// 128K-BASIC launch enters the editor. Paged out on RETN (ED 45;
	// zxnext changelog 3.01.09). mfActive gates the overlay.
	mfActive bool
	mfROM    []byte
	mfRAM    [0x2000]byte

	// Spectrum Next 8K MMU shadow. slotBank[i] holds the bank ID
	// currently mapped at 8K slot i (0..7 covering the full 64K Z80
	// address space). Bank values 0..223 are 8K RAM banks; 0xFF is
	// the ROM marker (the slot's half of the active 16K ROM bank).
	// mmuOverride[i] is true when slot i was last written by a Next
	// NextReg 0x50..0x57 write; classic paging writes (7FFD / 1FFD)
	// clear mmuOverride for the slots they touch — last-writer-wins.
	//
	// Sprint 2 maintains the shadow as a side effect of classic
	// paging; Read/Write do NOT yet consult it for any model
	// (Sprint 3 wires Read/Write for ModelNext). The MMU state is
	// observable through GetMMU and writable through SetMMU so
	// pkg/next/nextregs can install handlers for registers 0x50–
	// 0x57 right now and have them work in isolation.
	slotBank    [8]byte
	mmuOverride [8]bool

	// bankTracer, when non-nil, fires after every change to the
	// ROM-bank selection (port 7FFD/1FFD writes, NextReg $8E
	// handler via SetROMBank) and every MMU slot reassignment
	// (NextReg $50..$57 handler via SetMMU). Set via SetBankTracer.
	// nil at zero-value so the trace path is one nil check per
	// switch when disabled.
	bankTracer BankTracer

	// pagingTracer, when non-nil, fires on EVERY classic-paging
	// port write (PageMemory / PageMemoryPlus3) — including writes
	// dropped by the 7FFD bit-5 lock — reporting whether the write
	// took effect and the special-paging state around it. Unlike
	// bankTracer it observes the write itself, not a resulting
	// bank change, so a launch-era trace can prove whether the
	// guest's $1FFD special-mode writes were made and honoured
	// (the development log). Set via SetPagingTracer.
	pagingTracer PagingTracer
}

// PagingTracer is the callback signature for classic-paging port
// write tracing. source is "7FFD" or "1FFD"; applied is false when
// the write was dropped (paging locked via 7FFD bit 5, or frozen by
// Multiface); specialBefore/specialAfter capture the +3 special-
// paging flag around the write.
type PagingTracer func(source string, val byte, applied, specialBefore, specialAfter bool)

// SetPagingTracer installs a per-write callback on the classic
// paging ports. Pass nil to disable.
func (m *Memory) SetPagingTracer(fn PagingTracer) { m.pagingTracer = fn }

// SetAltROMReg writes the NextReg $8C value and updates Alt-ROM
// state. Called from the NextReg $8C OnWrite handler.
// SetMultifaceROM installs the Next Multiface ROM (enNextMF.rom, 8 KB).
func (m *Memory) SetMultifaceROM(rom []byte) { m.mfROM = rom }

// MultifaceActive reports whether the MF overlay is currently paged in.
func (m *Memory) MultifaceActive() bool { return m.mfActive }

// SetMultifaceActive pages the Next Multiface overlay in (true — done on
// the MF NMI, before it vectors to $0066) or out (false — done on RETN).
// The MF RAM is persistent SRAM (zero at power-on), not cleared per NMI.
func (m *Memory) SetMultifaceActive(on bool) { m.mfActive = on }

func (m *Memory) SetAltROMReg(val byte) { m.altROMReg = val }

// AltROMReg returns the current NextReg $8C value.
func (m *Memory) AltROMReg() byte { return m.altROMReg }

// altROMSelect returns which of the two Alt-ROM banks ($0/$1) is
// currently selected, based on the $8C lock bits and the legacy
// 7FFD bit 4 (low ROM-select bit). When neither lock bit is set,
// the selection follows the active ROM bank.
//
// Returns (bank, true) where bank is 0 (Alt-ROM 0 / 128K-style) or
// 1 (Alt-ROM 1 / 48K-style), or (0, false) if alt-rom is disabled.
func (m *Memory) altROMSelect() (int, bool) {
	if m.altROMReg&0x80 == 0 {
		return 0, false
	}
	if m.altROMReg&0x20 != 0 { // lock ROM1 (48K)
		return 1, true
	}
	if m.altROMReg&0x10 != 0 { // lock ROM0 (128K)
		return 0, true
	}
	// Neither lock bit: follow port 7FFD bit 4 (low ROM-select).
	if m.port7FFD&0x10 != 0 {
		return 1, true
	}
	return 0, true
}

// altROMRedirectsReads reports whether $0000-$3FFF reads should
// come from the Alt-ROM (bit 7 enabled, bit 6 cleared = "alt-rom
// visible during reads"). Bit 6 set means alt-rom is visible only
// during writes — reads go through the normal ROM path.
func (m *Memory) altROMRedirectsReads() bool {
	return m.altROMReg&0xC0 == 0x80
}

// altROMRedirectsWrites reports whether writes to $0000-$3FFF
// should go to the Alt-ROM bank instead of being dropped (bit 7
// enabled + bit 6 set = "alt-rom visible during writes only" —
// writes land in alt-rom RAM, reads pass through normally).
func (m *Memory) altROMRedirectsWrites() bool {
	return m.altROMReg&0xC0 == 0xC0
}

// DivMMCRom3Gate reports whether a rom3-variant divMMC automap entry
// point (NR$B9 bit CLEAR) is allowed to engage, per zxnext.vhd:3138:
//
//	(sram_altrom_en and sram_pre_alt_128_n) or (sram_pre_rom3 and not sram_altrom_en)
//
// An automap trigger is an M1 FETCH (a read), so sram_altrom_en here is
// the read-replacement form (zxnext.vhd:3078): NR$8C bit 7 set with
// bit 6 clear. When the alt-rom intercepts reads, the gate follows
// alt_128_n instead of the actual ROM bank — for the +3/Next
// personality (zxnext.vhd:2987-2996) alt_128_n is the lock_rom1 bit
// when either lock is set, else ROM-bank bit 0. Without alt-rom the
// gate is the plain "ROM3 is the selected machine ROM" test.
//
// This is THE menu-era $0038 IM1 automap gate: NextZXOS stages an
// alt-rom configuration whose promote (NR$8C reset nibble latch) can
// open the altrom arm even while ROM 0 is paged (the development log).
func (m *Memory) DivMMCRom3Gate() bool {
	if m.altROMRedirectsReads() {
		if m.altROMReg&0x30 != 0 { // either lock bit set
			return m.altROMReg&0x20 != 0 // alt_128_n = lock_rom1
		}
		return m.GetROMBank()&1 != 0 // alt_128_n = port_1ffd_rom(0)
	}
	return m.GetROMBank() == 3
}

// altROMBuffer returns the alt-rom 16K buffer for the currently-
// selected alt-rom (0 or 1). Used by both the read and write paths.
func (m *Memory) altROMBuffer(bank int) *[PageSize]byte {
	if bank == 1 {
		return &m.altROM1
	}
	return &m.altROM0
}

// GetAltROM exposes one of the two alt-ROM 16K buffers as a slice
// for the debugger's bank-inspect path. Bank 0/1 only; out-of-range
// returns nil.
func (m *Memory) GetAltROM(bank int) []byte {
	if bank < 0 || bank > 1 {
		return nil
	}
	return m.altROMBuffer(bank)[:]
}

// RAM8KPage returns the 8 KB main-RAM page addressed by the hardware
// MMU bank index `bank8k` (the value an NR$50-57 / port-7FFD write
// selects). The Next addresses RAM in 8 KB banks but our backing store
// is 16 KB pages, so bank8k maps to m.ram[bank8k>>1] at offset
// (bank8k&1)*0x2000 — see readValue (memory.go:967). Read-only; used
// by the full-state ours-vs-reference identity audit to dump every page
// with the SAME bank numbering the reference's NR$56 sweep uses. Returns nil
// for an out-of-range or unallocated bank.
func (m *Memory) RAM8KPage(bank8k int) []byte {
	idx := bank8k >> 1
	if bank8k < 0 || idx >= len(m.ram) || m.ram[idx] == nil {
		return nil
	}
	off := (bank8k & 1) * 0x2000
	return m.ram[idx][off : off+0x2000]
}

// BankTracer is the callback signature for memory bank-switch
// tracing. Slot is the 8K MMU slot index 0..7 for MMU writes, or
// 0xFF for classic ROM-bank toggles. Source names the trigger so
// consumers can correlate with the port/nextreg event streams.
type BankTracer func(slot byte, newBank int, source string)

// SetMMU writes the bank ID for an 8K MMU slot. Called from the
// NextReg 0x50..0x57 OnWrite handlers when guest code writes to
// those registers (either via port 0x253B or via the Z80N NEXTREG
// opcodes). Setting any MMU slot marks it as MMU-overridden so the
// next classic-paging write to a coincident 16K slot doesn't
// clobber it — "last writer wins".
//
// Slot indexes outside 0..7 are ignored.
func (m *Memory) SetMMU(slot, bank byte) {
	if slot > 7 {
		return
	}
	m.slotBank[slot] = bank
	m.mmuOverride[slot] = true
	if m.bankTracer != nil {
		m.bankTracer(slot, int(bank), "NEXTREG_50+slot")
	}
}

// SetBankTracer installs a per-switch callback fired whenever the
// ROM bank in slot 0 changes (via port 7FFD bit 4, port 1FFD bit 2,
// or NextReg \$8E) or any 8K MMU slot is reassigned (NextReg
// \$50..\$57). Pass nil to disable. Used by the `--trace=bankswitch`
// CLI path.
func (m *Memory) SetBankTracer(fn BankTracer) { m.bankTracer = fn }

// GetMMU returns the bank ID currently mapped at the given 8K MMU
// slot. Used to service NextReg 0x50..0x57 reads. Slot indexes
// outside 0..7 return 0.
func (m *Memory) GetMMU(slot byte) byte {
	if slot > 7 {
		return 0
	}
	return m.slotBank[slot]
}

// IsMMUOverridden reports whether the given 8K slot was last
// written through the MMU rather than through classic paging.
// Exposed mainly for tests; production callers should not care.
func (m *Memory) IsMMUOverridden(slot byte) bool {
	if slot > 7 {
		return false
	}
	return m.mmuOverride[slot]
}

// syncMMUFromPage updates the two 8K MMU slots that correspond to
// the given 16K page slot (0..3) from the current
// memoryPageReadMap, and clears mmuOverride for those two slots.
// Called from each classic-paging code path so the shadow always
// reflects the last classic write — unless the MMU later overwrites
// it.
//
// ROM pages (memoryPageReadMap >= 16) translate to 0xFF for both
// 8K halves. RAM page N translates to 8K banks 2N and 2N+1.
func (m *Memory) syncMMUFromPage(page16k int) {
	if page16k < 0 || page16k > 3 {
		return
	}
	s := page16k * 2
	rmap := m.memoryPageReadMap[page16k]
	var lo, hi byte
	if rmap >= 16 {
		lo, hi = 0xFF, 0xFF
	} else {
		lo = byte(rmap * 2)
		hi = byte(rmap*2 + 1)
	}
	m.slotBank[s] = lo
	m.slotBank[s+1] = hi
	m.mmuOverride[s] = false
	m.mmuOverride[s+1] = false
}

// syncAllMMU resyncs all 8 MMU slots from the classic page maps.
// Called once at the end of each setupNN configuration and from
// Reset(); not called from PageMemory/PageMemoryPlus3 which use
// the more precise per-page sync.
func (m *Memory) syncAllMMU() {
	for i := 0; i < 4; i++ {
		m.syncMMUFromPage(i)
	}
}

// SetTStatePtr wires this memory to a CPU's T-state counter and
// enables contention. Implements the optional contention-wiring
// interface that z80.New looks for; callers don't normally need to
// invoke this themselves.
func (m *Memory) SetTStatePtr(p *uint64) {
	m.TStates = p
	m.ContentionEnabled = true
}

// Contention delay pattern (repeats every 8 T-states in contended region)
var contentionPattern = [8]uint64{6, 5, 4, 3, 2, 1, 0, 0}

// isContendedAddr returns true if the address is in contended memory (screen RAM).
func (m *Memory) isContendedAddr(addr uint16) bool {
	// 0x4000-0x7FFF is always contended (bank 5 = screen memory).
	// On 128K, the contended banks are 1, 3, 5, 7 when paged into 0xC000.
	return addr >= 0x4000 && addr < 0x8000
}

// contentionDelay returns the contention delay for the current T-state position.
//
// Turbo speeds: the contention pattern keys off ULA-scale T-states (CPU
// T-states / SpeedMultiplier) so it still fires once per ULA scanline at
// any clock, AND the returned stall magnitude is scaled back up by the
// multiplier — a 6-cycle ULA hold at 7 MHz adds 12 CPU T-states. ×1 at
// 3.5 MHz. (Most turbo software disables contention via NextReg 0x08
// anyway.)
func (m *Memory) contentionDelay() uint64 {
	if m.TStates == nil {
		return 0
	}
	mult := uint64(1)
	if m.SpeedMultiplier != nil {
		if v := m.SpeedMultiplier(); v > 0 {
			mult = uint64(v)
		}
	}
	// ULA memory contention is DISABLED at every turbo speed on the
	// Next: the FPGA only asserts i_contention_en when cpu_speed = "00"
	// (3.5 MHz) — zxnext.vhd:4481 ANDs in (not cpu_speed(1)) and
	// (not cpu_speed(0)). At 7/14/28 MHz the CPU runs uncontended. The
	// earlier "scale the pattern by mult" model was wrong: it injected
	// ~35% phantom T-states per frame at 28 MHz, which shifted where the
	// frame interrupt landed in the instruction stream and derailed the
	// interrupt-driven 128K-BASIC launch (it forks on the interrupted PC).
	if mult > 1 {
		return 0
	}
	tstate := *m.TStates / mult
	if tstate >= 14335 && tstate < 57344 {
		line := (tstate - 14335) / 228
		if line < 192 {
			pos := (tstate - 14335) % 228
			if pos < 128 {
				// Scale the ULA hold (in ULA cycles) up to CPU T-states:
				// at N× turbo a 6-cycle ULA hold stalls the CPU for 6*N
				// of its own (faster) T-states. ×1 at 3.5 MHz.
				return contentionPattern[pos%8] * mult
			}
		}
	}
	return 0
}

// ContendMemory adds contention delay if the address is in contended memory.
func (m *Memory) ContendMemory(addr uint16) {
	if !m.ContentionEnabled || m.TStates == nil || m.RAMContentionDisabled {
		return
	}
	if m.isContendedAddr(addr) {
		*m.TStates += m.contentionDelay()
	}
}

// ContendPort adds contention delay for I/O port access.
// On 48K/128K/+2: even ports (bit 0 = 0) are ULA ports and always contended.
// Additionally, if the port address is in contended memory range, extra contention applies.
// On +2A/+3: no ports are treated as ULA ports (different ULA design).
func (m *Memory) ContendPort(addr uint16) {
	if !m.ContentionEnabled || m.TStates == nil {
		return
	}

	// +2A/+3 have no ULA port contention
	if m.currentModel == roms.ModelPlus3 || m.currentModel == roms.ModelPlus2A {
		return
	}

	isULAPort := (addr & 0x01) == 0
	isContended := m.isContendedAddr(addr)

	if isContended && isULAPort {
		// Contended address, ULA port: C:1, C:3
		*m.TStates += m.contentionDelay()
		*m.TStates++ // io cycle
		*m.TStates += m.contentionDelay()
		*m.TStates += 3
	} else if isContended {
		// Contended address, non-ULA port: C:1, C:1, C:1, C:1
		*m.TStates += m.contentionDelay()
		*m.TStates++
		*m.TStates += m.contentionDelay()
		*m.TStates++
		*m.TStates += m.contentionDelay()
		*m.TStates++
		*m.TStates += m.contentionDelay()
		*m.TStates++
	} else if isULAPort {
		// Non-contended address, ULA port: N:1, C:3
		*m.TStates++ // io cycle
		*m.TStates += m.contentionDelay()
		*m.TStates += 3
	}
	// Non-contended, non-ULA: no contention (just the standard T-states)
}

// New creates a new Memory instance for a given machine model.
func New(romPath string, model roms.SpectrumModel) (*Memory, error) {
	m := &Memory{
		romManager:   roms.NewROMManager(romPath),
		currentModel: model,
	}

	// Initialize RAM pages. Classic models allocate banks 0..7
	// (128K total). ModelNext additionally allocates banks 8..127
	// for the full 2 MB Next memory map; without these the.NEX
	// loader can't satisfy bank reads ≥8 and the 8K MMU has nowhere
	// to map.
	numBanks := 8
	if model == roms.ModelNext {
		numBanks = 128
	}
	for i := 0; i < numBanks; i++ {
		m.ram[i] = make([]byte, PageSize)
	}
	// Cold-boot RAM defaults to ZERO — matching the reference emulator
	// (its SDRAM powers up zeroed) and verified by the ours<->reference
	// first-divergence audit: under zero-fill ours' NR\$56/\$57 and the
	// whole 2 MB pool match the reference, NextZXOS's RAM-sizing test at ROM0
	// \$0190 still exits correctly (the early-exit restores NR\$57), and
	// the boot reaches the welcome cleanly. An earlier pseudo-random
	// (\$C0FFEE xorshift) fill was a workaround for a since-fixed banking
	// bug; it made ours diverge from the oracle across all 256 banks and
	// seeded garbage into NextZXOS sysvars. The random pattern stays
	// available behind ZX_GO_COLD_RANDOM for the uninitialised-read
	// detector's "looks like garbage" heuristic.
	if model == roms.ModelNext && os.Getenv("ZX_GO_COLD_RANDOM") != "" {
		// Deterministic seed = 0xC0FFEE — reproducible across runs.
		s := uint32(0xC0FFEE)
		for i := 0; i < numBanks; i++ {
			for j := range m.ram[i] {
				// xorshift32 — cheap, good enough entropy, no dep.
				s ^= s << 13
				s ^= s >> 17
				s ^= s << 5
				m.ram[i][j] = byte(s)
			}
		}
	}

	// Initialize ROM pages
	for i := 0; i < 4; i++ {
		m.rom[i] = make([]byte, PageSize)
	}

	// Load ROMs for the specified model
	if err := m.romManager.LoadROMsForModel(model); err != nil {
		return nil, fmt.Errorf("failed to load ROMs for model %s: %w", roms.GetModelName(model), err)
	}

	// Load peripheral ROMs (optional, errors are logged as warnings)
	_ = m.romManager.LoadPeripheralROMs()

	// Set up memory mapping for the model
	if err := m.setupModel(model); err != nil {
		return nil, err
	}
	m.syncAllMMU()

	return m, nil
}

// NewLegacy creates a Memory instance with the legacy interface for compatibility
func NewLegacy(romPath string, is128k bool) (*Memory, error) {
	model := roms.Model48K
	if is128k {
		model = roms.Model128K
	}
	return New(romPath, model)
}

// setupModel configures memory layout and loads ROMs for the specified model
func (m *Memory) setupModel(model roms.SpectrumModel) error {
	switch model {
	case roms.Model48K:
		return m.setup48K()
	case roms.Model128K:
		return m.setup128K()
	case roms.ModelPlus2:
		return m.setupPlus2()
	case roms.ModelPlus2A:
		return m.setupPlus2A()
	case roms.ModelPlus3:
		return m.setupPlus3()
	case roms.ModelNext:
		return m.setupNext()
	default:
		return fmt.Errorf("unsupported model: %d", model)
	}
}

// setup48K configures memory for 48K model
func (m *Memory) setup48K() error {
	if rom, exists := m.romManager.GetROM(roms.ROM48K); exists {
		copy(m.rom[0], rom)
	} else {
		return fmt.Errorf("48K ROM not found")
	}

	// 48K memory layout
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}  // ROM 0, RAM 5, RAM 2, RAM 0
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0} // ROM not writable
	m.PagingEnabled = false
	m.ScreenPage = 5
	return nil
}

// setup128K configures memory for 128K model
func (m *Memory) setup128K() error {
	if rom, exists := m.romManager.GetROM(roms.ROM128K_0); exists {
		copy(m.rom[0], rom)
	} else {
		return fmt.Errorf("128K ROM 0 not found")
	}

	if rom, exists := m.romManager.GetROM(roms.ROM128K_1); exists {
		copy(m.rom[1], rom)
	} else {
		return fmt.Errorf("128K ROM 1 not found")
	}

	// 128K memory layout - paging must be enabled for 128K models
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}  // ROM 0, RAM 5, RAM 2, RAM 0
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0} // ROM not writable
	m.PagingEnabled = true                     // 128K models have paging available
	m.ScreenPage = 5
	return nil
}

// setupPlus2 configures memory for +2 model
func (m *Memory) setupPlus2() error {
	if rom, exists := m.romManager.GetROM(roms.ROMPLUS2_0); exists {
		copy(m.rom[0], rom)
	} else {
		return fmt.Errorf("+2 ROM 0 not found")
	}

	if rom, exists := m.romManager.GetROM(roms.ROMPLUS2_1); exists {
		copy(m.rom[1], rom)
	} else {
		return fmt.Errorf("+2 ROM 1 not found")
	}

	// +2 memory layout (similar to 128K)
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0}
	m.PagingEnabled = true
	m.ScreenPage = 5
	return nil
}

// setupPlus2A configures memory for +2A model
func (m *Memory) setupPlus2A() error {
	for i := 0; i < 4; i++ {
		romType := roms.ROMType(int(roms.ROMPLUS2A_0) + i)
		if rom, exists := m.romManager.GetROM(romType); exists {
			copy(m.rom[i], rom)
		} else {
			return fmt.Errorf("+2A ROM %d not found", i)
		}
	}

	// +2A memory layout
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0}
	m.PagingEnabled = true
	m.ScreenPage = 5
	return nil
}

// setupPlus3 configures memory for +3 model
func (m *Memory) setupPlus3() error {
	for i := 0; i < 4; i++ {
		romType := roms.ROMType(int(roms.ROMPLUS3_0) + i)
		if rom, exists := m.romManager.GetROM(romType); exists {
			copy(m.rom[i], rom)
		} else {
			return fmt.Errorf("+3 ROM %d not found", i)
		}
	}

	// +3 memory layout
	m.memoryPageReadMap = [4]int{16, 5, 2, 0}
	m.memoryPageWriteMap = [4]int{-1, 5, 2, 0}
	m.PagingEnabled = true
	m.ScreenPage = 5
	return nil
}

// GetCurrentModel returns the current Spectrum model
func (m *Memory) GetCurrentModel() roms.SpectrumModel {
	return m.currentModel
}

// GetROMManager returns the ROM manager
func (m *Memory) GetROMManager() *roms.ROMManager {
	return m.romManager
}

// Reset restores the paging state to the model's power-on defaults:
// port 7FFD = 0, port 1FFD = 0, special paging disabled, page map back to
// the standard ROM/screen/bank layout. Equivalent to the /RESET pulse on
// real hardware. RAM contents are NOT cleared (real hardware preserves
// them across reset too); the BIOS / BASIC ROM is responsible for
// reinitialising system variables.
func (m *Memory) Reset() error {
	m.port7FFD = 0
	m.port1FFD = 0
	m.specialPaging = false
	m.PagingFrozen = false
	for i := range m.slotBank {
		m.slotBank[i] = 0
		m.mmuOverride[i] = false
	}
	// Per zxnext.vhd alt-ROM reset semantic: bits 7:4 are reloaded
	// from bits 3:0 on every reset. Bits 3:0 are the "default"
	// configuration that survives reset; bits 7:4 are the "live"
	// configuration that the OS toggles at runtime.
	defaults := m.altROMReg & 0x0F
	m.altROMReg = defaults | (defaults << 4)
	if err := m.setupModel(m.currentModel); err != nil {
		return err
	}
	m.syncAllMMU()
	return nil
}

// SwitchModel changes the current Spectrum model and reconfigures memory
func (m *Memory) SwitchModel(model roms.SpectrumModel) error {
	// Load ROMs for the new model if not already loaded
	if err := m.romManager.LoadROMsForModel(model); err != nil {
		return fmt.Errorf("failed to load ROMs for model %s: %w", roms.GetModelName(model), err)
	}

	// Lazily allocate the extended RAM banks the new model needs.
	// New() only allocates banks 0..7 for classic models; switching
	// up to ModelNext at runtime needs banks 8..127 for the 2 MB
	// Next memory map and the 8K MMU. Allocate on demand and leave
	// existing banks intact so they retain whatever state the
	// previous model wrote — switching back to a classic model
	// then back to Next preserves the high-bank contents.
	needBanks := 8
	if model == roms.ModelNext {
		needBanks = 128
	}
	// Newly-allocated banks default to zero (Go's make), matching the
	// cold-boot zero-fill at New(). ZX_GO_COLD_RANDOM seeds the same
	// deterministic pseudo-random pattern as New() for the uninit-read
	// detector; the seed deviates per bank index so a switch-back sees
	// fresh per-bank entropy.
	for i := 0; i < needBanks; i++ {
		if m.ram[i] == nil {
			m.ram[i] = make([]byte, PageSize)
			if model == roms.ModelNext && os.Getenv("ZX_GO_COLD_RANDOM") != "" {
				s := uint32(0xC0FFEE) + uint32(i)*0x9E3779B9
				for j := range m.ram[i] {
					s ^= s << 13
					s ^= s >> 17
					s ^= s << 5
					m.ram[i][j] = byte(s)
				}
			}
		}
	}

	m.currentModel = model
	if err := m.setupModel(model); err != nil {
		return err
	}
	m.syncAllMMU()
	return nil
}

// Read returns the byte at the given address. It is the z80.Memory
// interface entry point; allReadHook (when set, by --read-tape-all)
// observes EVERY read's logical address + value for bank-agnostic
// first-divergence comparison against a reference emulator. The
// per-read nil check is free when the hook is unset.
func (m *Memory) Read(addr uint16) byte {
	v := m.readValue(addr)
	if m.allReadHook != nil {
		m.allReadHook(addr, v)
	}
	return v
}

// readValue is the dispatch body for Read.
//
// On ModelNext, when the 8K slot covering this address has been
// MMU-overridden via a NextReg 0x50..0x57 write, the read is
// dispatched through the slotBank shadow — this is what makes
// banks 8..127 (the.NEX extended-bank space) reachable from CPU
// execution. Slots not MMU-overridden fall through to the classic
// 16K-slot dispatch.
func (m *Memory) readValue(addr uint16) byte {
	if m.currentModel == roms.ModelNext {
		// FPGA bootrom mode: $0000-$3FFF reads come from the
		// 8 KB loader image mirrored. Bootrom mode predates all
		// the other ModelNext paging machinery (it's what runs
		// before the user-selectable machine personality is
		// installed), so it short-circuits everything else.
		if m.fpgaBootROMActive && addr < 0x4000 && len(m.fpgaBootROM) > 0 {
			return m.fpgaBootROM[int(addr)%len(m.fpgaBootROM)]
		}
		// divMMC overlay READ priority. Per zxnext.vhd's final memory mux
		// (3084-3130) divMMC is tested FIRST — divmmc_rom_en, then
		// divmmc_ram_en, BEFORE layer2, romcs and sram_altrom_en — so a
		// paged-in divMMC overlay beats the Alt-ROM read redirect,
		// config-mode AND an MMU8-mapped RAM bank at $0000-$3FFF.
		// PeripheralRead/HandleRead returns ok=false unless the overlay
		// is paged in, so non-divMMC reads fall through unchanged.
		// Previously divMMC was consulted only inside the ROM-half ($FF)
		// sub-case below, so $2000-$3FFF reads under an MMU RAM override
		// returned MMU-RAM zeros and the cold boot NOP-slid at $2401
		// (VHDL conformance audit, Rank 3). And until the development log the
		// Alt-ROM redirect was (wrongly) checked before divMMC: with the
		// Browser launcher's NR$8C read-redirect active, the IM1
		// wrapper's `out ($E3),$80` fetched alt-ROM bytes instead of the
		// esxDOS ROM at $0046+, NOP-slid through its own body, and the
		// menu-item launch derailed (#255). bootrom (read mask) is still
		// higher priority — it returns above.
		if addr < 0x4000 && m.PeripheralRead != nil {
			if val, ok := m.PeripheralRead(addr); ok {
				return val
			}
		}
		// Next Multiface overlay. When the MF NMI has paged it in (NR$02=$08),
		// $0000-$1FFF reads enNextMF.rom and $2000-$3FFF the MF RAM, so the
		// $0066 handler runs (NextZXOS's 128K-BASIC editor-snapshot loader).
		// BELOW the divMMC overlay above: the MF handler's esxDOS RST-$08 calls
		// automap the divMMC over $0000-$1FFF and those must win while paged in
		// (FPGA: MF + divMMC coexist, changelog 3.01.07). The divMMC's own
		// $0066 NMI-vector automap is SUPPRESSED while the MF is master (see
		// divmmc.go), so at $0066 the divMMC is absent and the MF ROM is read
		// here. Above Alt-ROM / config / normal ROM. Without the MF overlay
		// $0066 read the normal-ROM stub F5F1ED45 → $FF00 NOP-slide hang →
		// black (the development log; the reference's $0066 = JP $006D real handler). Paged out
		// on RETN (changelog 3.01.09).
		if m.mfActive && addr < 0x4000 && len(m.mfROM) > 0 {
			if addr < 0x2000 {
				return m.mfROM[int(addr)&0x1FFF]
			}
			return m.mfRAM[addr&0x1FFF]
		}
		// Alt-ROM read redirect (NextReg $8C bit 7 set, bit 6
		// clear): $0000-$3FFF reads come from the alt-rom 16K
		// bank instead of the normal ROM. Below the divMMC overlay
		// (zxnext.vhd mux order, the development log), above config-mode
		// and the classic ROM dispatch.
		if addr < 0x4000 && m.altROMRedirectsReads() {
			bank, _ := m.altROMSelect()
			return m.altROMBuffer(bank)[addr]
		}
		// Config-mode RAM-page window (NextReg $03 bits 2-0 = 0).
		// When config mode is active and the FPGA bootrom isn't
		// masking the window, $0000-$3FFF reads come from a
		// 16 KB backing store selected by NextReg $04
		// (REG_RAMPAGE). See configModePageBacking() for the
		// page-number-to-backing-store mapping; it mirrors the
		// TBBlue firmware hardware.h RAMPAGE_* layout so boot.bin
		// can address Spectrum ROM banks, divMMC RAM, Alt-ROM,
		// and the standard / extra RAM IC space with the same
		// register.
		if m.configModeActive && addr < 0x4000 {
			if val, ok := m.configModeReadByte(addr); ok {
				return val
			}
		}
		slot8k := addr >> 13
		if m.mmuOverride[slot8k] {
			bank8k := m.slotBank[slot8k]
			offset := addr & 0x1FFF
			if bank8k == 0xFF {
				// 0xFF marks "ROM half" — synthesise the address
				// from the currently-active 16K ROM via the
				// classic page map for the corresponding 16K slot.
				if m.PeripheralRead != nil {
					if val, ok := m.PeripheralRead(addr); ok {
						return val
					}
				}
				slot16k := int(slot8k >> 1)
				pageIndex := m.memoryPageReadMap[slot16k]
				if pageIndex >= 16 {
					romOffset := (uint16(slot8k&1) << 13) | offset
					return m.rom[pageIndex-16][romOffset]
				}
				// Fall through (extremely unusual): no ROM, treat
				// as RAM of the 16K bank corresponding to this 8K.
			}
			bank16k := int(bank8k) >> 1
			pageOffset := (uint16(bank8k&1) << 13) | offset
			if m.TrackUninit {
				m.checkRAMUninit(addr, bank16k, pageOffset)
			}
			v := m.ram[bank16k][pageOffset]
			if m.ramReadHook != nil {
				m.ramReadHook(bank16k, pageOffset, v)
			}
			return v
		}
	}

	pageIndex := m.memoryPageReadMap[addr>>14]
	offset := addr & (PageSize - 1)

	if pageIndex >= 16 { // ROM area
		// Check if a peripheral ROM overrides this address
		if m.PeripheralRead != nil {
			if val, ok := m.PeripheralRead(addr); ok {
				return val
			}
		}
		return m.rom[pageIndex-16][offset]
	}
	// RAM
	if m.TrackUninit {
		m.checkRAMUninit(addr, pageIndex, offset)
	}
	v := m.ram[pageIndex][offset]
	if m.ramReadHook != nil {
		m.ramReadHook(pageIndex, offset, v)
	}
	return v
}

// Write sets the byte at the given address. ModelNext MMU dispatch
// mirrors Read: an MMU-overridden 8K slot routes through the
// slotBank shadow into banks 0..223; non-overridden slots use the
// classic 16K dispatch.
func (m *Memory) Write(addr uint16, val byte) {
	if m.WriteObserver != nil {
		// PC isn't directly available here; pass 0 — callers
		// that want a PC must wire WriteObserver via a closure
		// that captures a CPU pointer.
		m.WriteObserver(addr, val, 0)
	}
	// FPGA bootrom: per zxnext.vhd:1856 `bootrom_en` gates ONLY the read
	// mux (cpu_di); the SRAM write path (sram_req, 3154) is
	// bootrom-independent. So the bootrom does NOT drop writes — they route
	// through the normal write mux. The one case it appears to "drop" is a
	// write to a ROM-covered slot, which the dispatch below ignores anyway.
	// Crucially, at power-on bootrom_en AND config_mode are BOTH '1', so a
	// $0000-$3FFF write must fall through to the config-mode page (boot.bin
	// streams its ROM/divMMC-window images there while the bootrom is still
	// active). Only short-circuit when config-mode is OFF (classic ROM area)
	// — gating this on !configModeActive fixes the cold-boot $2401 NOP-slide
	// (VHDL conformance audit, memory-paging Rank 1).
	if m.currentModel == roms.ModelNext && m.fpgaBootROMActive && !m.configModeActive && addr < 0x4000 {
		return
	}
	// divMMC overlay WRITE priority. Per zxnext.vhd's final memory mux
	// (3084-3130) the divMMC override is tested FIRST — the
	// `divmmc_ram_en` branch (writeable, modulo divmmc_rdonly) selects
	// the divMMC RAM bank BEFORE `sram_altrom_en` is even considered,
	// for WRITES as much as reads (the read path above already encodes
	// this priority with the same citation). Previously the Alt-ROM
	// write redirect short-circuited first, so with the overlay paged
	// in AND NextZXOS's runtime ROM-patch redirect active (NR$8C bits
	// 7|6), esxDOS window writes (e.g. the sector-cache seek field at
	// $2335) were silently stolen into the alt-ROM buffer — the fetch
	// saw the overlay, the write didn't, and the mount's root-dir seek
	// was lost (the development log: the "Error reading file: …enAltZX.rom"
	// screen).
	if m.currentModel == roms.ModelNext && addr < 0x4000 && m.PeripheralWrite != nil {
		// divMMC beats even an MMU-mapped slot: the final SRAM mux
		// (zxnext.vhd:3084-3092) tests `sram_pre_override(2)='1' and
		// divmmc_ram_en='1'` FIRST, and the MMU arm sets override =
		// "110" (bit 2 = divmmc MAY override). So while the overlay
		// is paged in, bottom-16K writes go to divMMC RAM regardless
		// of NR$50/$51 (the development log — an earlier reading inverted
		// this and broke boot).
		if m.PeripheralWrite(addr, val) {
			return
		}
	}
	// Next Multiface overlay WRITE (mirrors the read path, below the divMMC
	// overlay above so the handler's esxDOS calls win while the divMMC is
	// paged): while the MF is paged in, $2000-$3FFF writes land in the 8 KB MF
	// RAM (the $0066 handler's SP-save / scratch); $0000-$1FFF is read-only MF
	// ROM. Above Alt-ROM / config / normal ROM.
	if m.currentModel == roms.ModelNext && m.mfActive && addr < 0x4000 {
		if addr >= 0x2000 {
			m.mfRAM[addr&0x1FFF] = val
		}
		return
	}
	// Alt-ROM write redirect (NextReg $8C bit 7 set, bit 6 also
	// set): writes to $0000-$3FFF land in the alt-rom 16K bank
	// instead of being dropped. NextZXOS uses this to patch its
	// own ROM at runtime without disturbing reads.
	if m.currentModel == roms.ModelNext && addr < 0x4000 && m.altROMRedirectsWrites() {
		bank, _ := m.altROMSelect()
		m.altROMBuffer(bank)[addr] = val
		return
	}
	// Config-mode RAM-page window (NextReg $03 bits 2-0 = 0).
	// While config mode is active and no higher-priority overlay
	// (FPGA bootrom, Alt-ROM write) is in effect, writes to
	// $0000-$3FFF land in the backing store selected by NextReg
	// $04 (REG_RAMPAGE) — see configModePageBacking. boot.bin
	// uses this to fill each personality ROM image into the
	// page that classic 7FFD / 1FFD paging will swap in once
	// config mode exits.
	if m.currentModel == roms.ModelNext && m.configModeActive && addr < 0x4000 {
		m.configModeWriteByte(addr, val)
		return
	}

	// General peripheral write hook (classic-model peripherals: IF1,
	// DISCiPLE, Multiface shadow RAM, …). For ModelNext the divMMC
	// window was already offered the write above (VHDL mux priority);
	// peripherals that declined there decline again here, so this stays
	// a no-op for Next window writes and preserves classic dispatch.
	if m.PeripheralWrite != nil {
		if m.PeripheralWrite(addr, val) {
			return
		}
	}

	if m.currentModel == roms.ModelNext {
		slot8k := addr >> 13
		if m.mmuOverride[slot8k] {
			bank8k := m.slotBank[slot8k]
			if bank8k == 0xFF {
				return // write to ROM — ignored
			}
			bank16k := int(bank8k) >> 1
			pageOffset := (uint16(bank8k&1) << 13) | (addr & 0x1FFF)
			m.ram[bank16k][pageOffset] = val
			if m.TrackUninit {
				m.markRAMWritten(bank16k, pageOffset)
			}
			if m.ramWriteHook != nil {
				m.ramWriteHook(bank16k, pageOffset, val)
			}
			return
		}
	}

	pageIndex := m.memoryPageWriteMap[addr>>14]
	if pageIndex == -1 {
		// Attempt to write to ROM, ignore.
		return
	}
	offset := addr & (PageSize - 1)
	m.ram[pageIndex][offset] = val
	if m.TrackUninit {
		m.markRAMWritten(pageIndex, offset)
	}
	if m.ramWriteHook != nil {
		m.ramWriteHook(pageIndex, offset, val)
	}
}

// SetRAMWriteHook installs a debug callback fired on every
// successful RAM write that lands via the Memory.Write path. bank
// is the 16K SRAM index, addr is the byte offset within that 16K.
// Pass nil to remove. Headless debug paths use this to find where
// NextZXOS's SD-load deposits files when the destination isn't
// obvious from the NextReg sequence alone.
func (m *Memory) SetRAMWriteHook(fn func(bank int, addr uint16, val byte)) {
	m.ramWriteHook = fn
}

// GetRAMWriteHook returns the currently-installed RAM-write hook,
// or nil if none. Useful for layering: e.g. the debugger can chain
// its watchpoint check on top of the env-var diagnostic tracer
// without clobbering it.
func (m *Memory) GetRAMWriteHook() func(bank int, addr uint16, val byte) {
	return m.ramWriteHook
}

// SetRAMReadHook installs a debug callback fired on every successful
// RAM read returned via the Memory.Read path. bank is the 16K SRAM
// index, addr the byte offset within it, val the byte read. Pass nil
// to remove. The read-watchpoint uses this to halt on the exact
// instruction that reads a watched address. NOTE: reads are far more
// frequent than writes (every opcode fetch is a read), so the hook is
// only installed on first use of `watch-read` and no-ops when the
// active spec is nil.
func (m *Memory) SetRAMReadHook(fn func(bank int, addr uint16, val byte)) {
	m.ramReadHook = fn
}

// SetAllReadHook installs a callback fired on EVERY Read (logical
// address + value), regardless of ROM/RAM/divMMC routing. Pass nil to
// remove. Used by --read-tape-all for bank-agnostic first-divergence
// lockstep against a reference emulator (ROM reads match, so the first
// divergent value is the bug even when code is banked through a window).
func (m *Memory) SetAllReadHook(fn func(addr uint16, val byte)) {
	m.allReadHook = fn
}

// GetRAMReadHook returns the currently-installed RAM-read hook, or nil.
// Allows chaining without clobbering a prior hook.
func (m *Memory) GetRAMReadHook() func(bank int, addr uint16, val byte) {
	return m.ramReadHook
}

// GetPage returns a direct pointer to a RAM page. Page indices
// outside 0..127 return nil. Indices 8..127 return nil on classic
// models (only allocated for ModelNext) and a 16K slice on Next.
// EnableUninitTracking turns the uninitialised-RAM read detector on
// and (re)marks every allocated RAM bank as guest-unwritten. Call
// after construction / cold-RAM fill so power-on bytes count as
// "uninitialised". Idempotent.
func (m *Memory) EnableUninitTracking() {
	for b := range m.ram {
		if m.ram[b] == nil {
			m.ramWritten[b] = nil
			continue
		}
		if len(m.ramWritten[b]) != len(m.ram[b]) {
			m.ramWritten[b] = make([]bool, len(m.ram[b]))
		} else {
			for i := range m.ramWritten[b] {
				m.ramWritten[b][i] = false
			}
		}
	}
	m.TrackUninit = true
}

// markRAMWritten records a guest write to pool byte (bank16k, off).
// Caller gates on m.TrackUninit.
func (m *Memory) markRAMWritten(bank16k int, off uint16) {
	if bank16k >= 0 && bank16k < len(m.ramWritten) {
		if w := m.ramWritten[bank16k]; w != nil && int(off) < len(w) {
			w[off] = true
		}
	}
}

// checkRAMUninit fires UninitReadObserver if pool byte (bank16k, off)
// was never written by the guest. Caller gates on m.TrackUninit.
func (m *Memory) checkRAMUninit(addr uint16, bank16k int, off uint16) {
	if m.UninitReadObserver == nil || bank16k < 0 || bank16k >= len(m.ramWritten) {
		return
	}
	if w := m.ramWritten[bank16k]; w != nil && int(off) < len(w) && !w[off] {
		m.UninitReadObserver(addr, bank16k, off)
	}
}

func (m *Memory) GetPage(pageIndex int) []byte {
	if pageIndex < 0 || pageIndex >= len(m.ram) {
		return nil
	}
	return m.ram[pageIndex]
}

// PoolBank returns the live backing slice of one 16K bank in the unified
// RAM pool (nil if the bank is unallocated for this model). Used by the
// Next wiring to alias divMMC RAM onto SRAM 8K pages 16-31 (= 16K banks
// 8-15) per zxnext.vhd:3093, and by debug tooling. Callers must not
// resize the slice.
func (m *Memory) PoolBank(b int) []byte {
	if b < 0 || b >= len(m.ram) {
		return nil
	}
	return m.ram[b]
}

// SnapshotPool returns a deep copy of every 16K bank in the unified RAM
// pool, so the whole 2 MB can be restored later — the foundation of
// faithful Next time-travel rewind (Phase 2b), where banks outside the
// visible 8-page window still matter. Nil pool banks copy as nil.
func (m *Memory) SnapshotPool() [][]byte {
	out := make([][]byte, len(m.ram))
	for i, bank := range m.ram {
		if bank == nil {
			continue
		}
		cp := make([]byte, len(bank))
		copy(cp, bank)
		out[i] = cp
	}
	return out
}

// RestorePool copies a SnapshotPool result back into the live pool.
// Short/nil entries and length mismatches are clamped by copy(), so a
// snapshot from a differently-sized pool degrades gracefully.
func (m *Memory) RestorePool(src [][]byte) {
	for i := 0; i < len(src) && i < len(m.ram); i++ {
		if src[i] == nil || m.ram[i] == nil {
			continue
		}
		copy(m.ram[i], src[i])
	}
}

// SnapshotMMU captures the 8-entry MMU8 slot table (NextReg $50-$57:
// which 8K pool bank backs each 8K CPU slot). Needed for faithful Next
// time-travel rewind — the pool bytes are meaningless without knowing
// which bank is mapped where.
func (m *Memory) SnapshotMMU() [8]byte {
	var out [8]byte
	for s := byte(0); s < 8; s++ {
		out[s] = m.GetMMU(s)
	}
	return out
}

// RestoreMMU re-applies a SnapshotMMU capture via SetMMU, so the slot
// mapping (and any derived read/write page maps) is rebuilt exactly.
func (m *Memory) RestoreMMU(slots [8]byte) {
	for s := byte(0); s < 8; s++ {
		m.SetMMU(s, slots[s])
	}
}

// GetROMPage returns a direct pointer to a ROM page (0-3).
func (m *Memory) GetROMPage(pageIndex int) []byte {
	if pageIndex < 0 || pageIndex >= 4 {
		return nil
	}
	return m.rom[pageIndex]
}

// GetPageMap returns the current read/write page map for debugging.
// Returns [4]int for read map and [4]int for write map.
// Values >= 16 indicate ROM pages (16=ROM0, 17=ROM1, etc.), others are RAM page indices.
func (m *Memory) GetPageMap() ([4]int, [4]int) {
	return m.memoryPageReadMap, m.memoryPageWriteMap
}

// GetPortState returns the current port 7FFD and 1FFD values for debugging.
func (m *Memory) GetPortState() (byte, byte, bool) {
	return m.port7FFD, m.port1FFD, m.specialPaging
}

// PagingState holds a snapshot of the memory paging configuration so it can
// be saved before a peripheral (e.g. Multiface 3) takes control and restored
// afterwards, preventing paging corruption.
type PagingState struct {
	port7FFD           byte
	port1FFD           byte
	specialPaging      bool
	memoryPageReadMap  [4]int
	memoryPageWriteMap [4]int
	screenPage         int
	pagingEnabled      bool
	valid              bool
}

// SavePagingState captures the current paging configuration into a PagingState.
func (m *Memory) SavePagingState() *PagingState {
	return &PagingState{
		port7FFD:           m.port7FFD,
		port1FFD:           m.port1FFD,
		specialPaging:      m.specialPaging,
		memoryPageReadMap:  m.memoryPageReadMap,
		memoryPageWriteMap: m.memoryPageWriteMap,
		screenPage:         m.ScreenPage,
		pagingEnabled:      m.PagingEnabled,
		valid:              true,
	}
}

// RestorePagingState restores a previously saved paging configuration.
// If state is nil or was never saved, this is a no-op.
func (m *Memory) RestorePagingState(state *PagingState) {
	if state == nil || !state.valid {
		return
	}
	m.port7FFD = state.port7FFD
	m.port1FFD = state.port1FFD
	m.specialPaging = state.specialPaging
	m.memoryPageReadMap = state.memoryPageReadMap
	m.memoryPageWriteMap = state.memoryPageWriteMap
	m.ScreenPage = state.screenPage
	m.PagingEnabled = state.pagingEnabled
}

// PageMemory handles the 128K memory paging mechanism.
func (m *Memory) PageMemory(val byte) {
	if !m.PagingEnabled {
		if m.pagingTracer != nil {
			m.pagingTracer("7FFD", val, false, m.specialPaging, m.specialPaging)
		}
		return
	}
	if m.pagingTracer != nil {
		// specialAfter == specialBefore: 7FFD writes never toggle
		// the special flag (only 1FFD bit 0 does).
		m.pagingTracer("7FFD", val, true, m.specialPaging, m.specialPaging)
	}

	if m.PagingFrozen {
		// When frozen (Multiface active), only allow ROM selection changes.
		// RAM page, screen page, and paging disable are blocked.
		m.port7FFD = val
		if !m.specialPaging {
			m.selectROM("7FFD")
		}
		return
	}

	oldRAMPage := m.port7FFD & 0x07
	m.port7FFD = val

	// Bit 3: Screen page (0 for page 5, 1 for page 7)
	if (val & 0x08) != 0 {
		m.ScreenPage = 7
	} else {
		m.ScreenPage = 5
	}

	// Only modify memory mapping when NOT in special paging mode.
	if !m.specialPaging {
		// Bits 0-2: RAM page to map into 0xC000-0xFFFF
		ramPage := int(val & 0x07)
		m.memoryPageReadMap[3] = ramPage
		m.memoryPageWriteMap[3] = ramPage
		// zxnext.vhd:4677-4681: on a port write the 8K MMU6/7 are
		// reset to the new 7FFD RAM bank ONLY when the RAM-page bits
		// (0-2) actually change (port_memory_ram_change_dly). A 7FFD
		// write that leaves the RAM page the same — a ROM- or
		// screen-bit change, or a re-write of the same value — must
		// NOT touch MMU6/7, so an NR$56/$57 (slot 6/7) override the
		// guest set via the 8K MMU survives it. NextZXOS dot commands
		// (e.g. the NextGuide viewer) keep their code in an MMU-paged
		// slot-6 overlay and do exactly this; resetting slot 6/7
		// unconditionally dropped the override, mapped guide *data*
		// into $C000-$DFFF, and the guest RET'd into that data — the
		// "Guide" menu-item crash.
		if byte(ramPage) != oldRAMPage {
			m.syncMMUFromPage(3)
		}

		// Bit 4: ROM select (syncs slot 0 internally)
		m.selectROM("7FFD")
	}

	// Bit 5: Paging disable
	if (val & 0x20) != 0 {
		m.PagingEnabled = false
	}
}

// selectROM sets the ROM page based on the current port values.
// +2A / +3 / Next models combine 7FFD bit 4 (low bit) with 1FFD
// bit 2 (high bit) for a full 4-bank selection; classic 128K /
// +2 toggle between just 2 ROMs via 7FFD bit 4. The Next's
// NextReg 0x8E handler writes through to the same 7FFD / 1FFD
// pair so this selectROM stays the single source of truth.
//
// source names the trigger (e.g. "7FFD", "1FFD", "NEXTREG_8E")
// so the bank-switch tracer can correlate the change with the
// inbound port/nextreg event.
func (m *Memory) selectROM(source string) {
	plus3Style := m.currentModel == roms.ModelPlus3 ||
		m.currentModel == roms.ModelPlus2A ||
		m.currentModel == roms.ModelNext
	prev := m.memoryPageReadMap[0]
	if plus3Style {
		// 4 ROMs: low bit from 7FFD bit 4, high bit from 1FFD bit 2
		romIndex := int((m.port7FFD >> 4) & 1)
		romIndex |= int((m.port1FFD >> 1) & 2)
		m.memoryPageReadMap[0] = 16 + romIndex
	} else {
		if (m.port7FFD & 0x10) != 0 {
			m.memoryPageReadMap[0] = 17 // ROM 1
		} else {
			m.memoryPageReadMap[0] = 16 // ROM 0
		}
	}
	m.syncMMUFromPage(0)
	if m.bankTracer != nil && m.memoryPageReadMap[0] != prev {
		m.bankTracer(0xFF, m.memoryPageReadMap[0]-16, source)
	}
}

// PageMemoryPlus3 handles the +3/+2A special paging via port 0x1FFD.
func (m *Memory) PageMemoryPlus3(val byte) {
	if !m.PagingEnabled {
		if m.pagingTracer != nil {
			m.pagingTracer("1FFD", val, false, m.specialPaging, m.specialPaging)
		}
		return
	}
	if m.PagingFrozen {
		// When frozen, only update port1FFD for ROM selection purposes.
		m.port1FFD = val
		if !m.specialPaging {
			m.selectROM("1FFD")
		}
		if m.pagingTracer != nil {
			m.pagingTracer("1FFD", val, false, m.specialPaging, m.specialPaging)
		}
		return
	}
	specialBefore := m.specialPaging
	m.port1FFD = val

	if val&0x01 != 0 {
		// Special paging mode: 4 predefined RAM configurations
		m.specialPaging = true
		mode := (val >> 1) & 0x03
		switch mode {
		case 0: // 0,1,2,3
			m.memoryPageReadMap = [4]int{0, 1, 2, 3}
			m.memoryPageWriteMap = [4]int{0, 1, 2, 3}
		case 1: // 4,5,6,7
			m.memoryPageReadMap = [4]int{4, 5, 6, 7}
			m.memoryPageWriteMap = [4]int{4, 5, 6, 7}
		case 2: // 4,5,6,3
			m.memoryPageReadMap = [4]int{4, 5, 6, 3}
			m.memoryPageWriteMap = [4]int{4, 5, 6, 3}
		case 3: // 4,7,6,3
			m.memoryPageReadMap = [4]int{4, 7, 6, 3}
			m.memoryPageWriteMap = [4]int{4, 7, 6, 3}
		}
		// Special paging touches all four 16K slots — resync all
		// eight 8K MMU slots.
		m.syncAllMMU()
	} else {
		// Normal paging mode. On real +2A/+3 hardware, writes to
		// 0x1FFD with bit 0 = 0 affect ROM selection (combined
		// with 7FFD bit 4) and bit 4 (disk motor) — they do NOT
		// reset the RAM bank in slot 3 or the screen/middle slots.
		// Reset the slot map ONLY when transitioning out of
		// special paging, so a saved return address on the stack
		// at 0xC000–0xFFFF doesn't get clobbered by a bank-swap
		// the user didn't request.
		//
		// (NextZXOS hits this: it pushes a stack frame at SP in
		// 0xFF55-0xFF58, then writes 0x1FFD with bit 0 = 0 to
		// toggle a ROM-selection bit. Previously we treated this
		// as "transition to normal" and reset slot 3 to bank
		// (7FFD & 7), which switched the stack out from under
		// NextZXOS — the next RET popped 0x0000 from a different
		// bank's zeroed RAM and the boot deadlocked.)
		if m.specialPaging {
			m.specialPaging = false
			m.memoryPageWriteMap[0] = -1
			m.memoryPageReadMap[1] = 5
			m.memoryPageWriteMap[1] = 5
			m.memoryPageReadMap[2] = 2
			m.memoryPageWriteMap[2] = 2
			ramPage := int(m.port7FFD & 0x07)
			m.memoryPageReadMap[3] = ramPage
			m.memoryPageWriteMap[3] = ramPage
			m.syncMMUFromPage(1)
			m.syncMMUFromPage(2)
			m.syncMMUFromPage(3)
		}
	}

	// Update ROM selection when 0x1FFD changes (bit 2 is high bit of ROM index)
	if !m.specialPaging {
		m.selectROM("1FFD")
	}
	if m.pagingTracer != nil {
		m.pagingTracer("1FFD", val, true, specialBefore, m.specialPaging)
	}
}
