package memory

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// The RAM pool is the large part and the easy part: nobody forgets the RAM.
// What decides what the CPU actually reads is the mapping laid over it — the
// two 16K page maps, the eight 8K MMU slots with their per-slot "the MMU wrote
// this last" flags, and the overlays stacked above both (FPGA bootrom,
// config-mode window, Multiface, Alt-ROM, Layer-2 mapping, Beta). A capture
// that stopped at the pool and the paging ports would restore the right bytes
// behind the wrong window.
//
// The maps are captured directly rather than re-derived from the ports on
// restore, and they have to be, because the window is not a function of the
// ports:
//
//   - a NextReg $50-$57 write overrides one 8K slot independently of $7FFD,
//     and outlives every port write until a classic write to the same 16K page
//     reclaims it;
//   - a locked $7FFD (bit 5) or a frozen pager (Multiface) makes PageMemory
//     drop or partly apply the write it is given, so replaying the captured
//     port value need not reproduce the captured map;
//   - +3 special paging and the NextReg $8E MMU6/7 suppression make the map
//     depend on the order the writes arrived in, not on their final values.
//
// So the ports are restored as the ports and the window as the window, and
// LoadState calls none of the paging routines.
//
// Deliberately NOT captured, because none of it is machine state this device
// owns:
//
//   - The installed overlay images: the FPGA bootrom (SetFPGABootROM), the
//     Multiface ROM (SetMultifaceROM) and the Beta TR-DOS ROM (EnableBeta).
//     No guest write can reach any of them — the write paths either drop the
//     write or route it elsewhere — so they are hardware fitted to the
//     machine, like a cartridge, not state that moves while it runs. The
//     flags saying whether each is currently paged in ARE captured.
//   - betaBasicPage. EnableBeta derives it from the machine model and nothing
//     else ever writes it, so for a fixed machine it cannot differ between
//     capture and restore; restoring it could only ever write back the value
//     already there. (A state from a different model is a different machine,
//     which Registry.Restore rejects on the device set.)
//   - The model and everything setupModel derives from it: currentModel, the
//     contention timing personality, contendTPerLine, contendStart,
//     contentionDisabled and ContentionEnabled. Same argument: a rewind moves
//     a machine through time, it does not turn it into another machine.
//     RAMContentionDisabled is different — NextReg $08 bit 6 lets the guest
//     move it — and is captured.
//   - Live wiring: the CPU T-state pointer, SpeedMultiplier, the ROM manager,
//     the Opus overlay, the divMMC accessor, the Layer-2 bank getters, the
//     peripheral read/write hooks, and every debug hook, observer and tracer.
//     A rewind must not re-point the machine at different objects.
//   - TrackUninit and its ramWritten shadow. That is debug instrumentation
//     (EnableUninitTracking), off in normal runs, and its shadow is as large
//     as the pool. After a rewind the detector reports against marks
//     accumulated up to the newest execution rather than the restored point —
//     a known limitation of the tool, not of the machine.

// memState is the wire form. Every field is exported because gob only encodes
// exported fields, and every field of the running device that a guest can move
// is present: adding a field to Memory without adding it here is the failure
// this package's tests are built to catch.
//
// The bulk fields are slices rather than the arrays they come from because gob
// encodes a []byte as one byte string and a [N]byte element by element.
type memState struct {
	// RAM is the unified pool, one entry per 16K bank; entries are empty for
	// banks the model never allocated (8..127 off the Next).
	RAM [][]byte
	// ROM is the four legacy 16K ROM banks. They are state, not a fixed
	// image: while the Next is in config mode a guest write to $0000-$3FFF
	// lands in rom[NR$04] (configModePageBacking), which is how the firmware
	// loads each personality ROM off the SD card.
	ROM [][]byte

	ReadMap  [4]int
	WriteMap [4]int

	Port7FFD      byte
	Port1FFD      byte
	PortDFFD      byte
	PortEFF7Reg2  bool
	PortEFF7Reg3  bool
	SpecialPaging bool
	PagingEnabled bool
	PagingFrozen  bool
	ScreenPage    int

	SlotBank    [8]byte
	MMUOverride [8]bool

	AltROMReg byte
	AltROM0   []byte
	AltROM1   []byte

	FPGABootROMActive bool
	ConfigModeActive  bool
	ConfigModeRAMPage byte

	MFActive bool
	MFRAM    []byte

	L2WrEn       bool
	L2RdEn       bool
	L2Shadow     bool
	L2Segment    byte
	L2Offset     byte
	L2ActiveBank byte
	L2ShadowBank byte

	BetaEnabled bool
	BetaActive  bool

	RAMContentionDisabled bool
}

// StateID identifies the memory system in a captured machine state.
func (m *Memory) StateID() string { return "memory" }

// SaveState captures the complete memory state.
//
// The state struct references the live buffers rather than copying them: gob
// serialises them into the returned blob on the spot, so the copy happens once
// instead of twice. On a Next that is the difference between one 2 MB
// allocation per capture and two.
func (m *Memory) SaveState() []byte {
	s := memState{
		RAM:                   m.ram[:],
		ROM:                   m.rom[:],
		ReadMap:               m.memoryPageReadMap,
		WriteMap:              m.memoryPageWriteMap,
		Port7FFD:              m.port7FFD,
		Port1FFD:              m.port1FFD,
		PortDFFD:              m.portDFFD,
		PortEFF7Reg2:          m.portEFF7Reg2,
		PortEFF7Reg3:          m.portEFF7Reg3,
		SpecialPaging:         m.specialPaging,
		PagingEnabled:         m.PagingEnabled,
		PagingFrozen:          m.PagingFrozen,
		ScreenPage:            m.ScreenPage,
		SlotBank:              m.slotBank,
		MMUOverride:           m.mmuOverride,
		AltROMReg:             m.altROMReg,
		AltROM0:               m.altROM0[:],
		AltROM1:               m.altROM1[:],
		FPGABootROMActive:     m.fpgaBootROMActive,
		ConfigModeActive:      m.configModeActive,
		ConfigModeRAMPage:     m.configModeRAMPage,
		MFActive:              m.mfActive,
		MFRAM:                 m.mfRAM[:],
		L2WrEn:                m.l2WrEn,
		L2RdEn:                m.l2RdEn,
		L2Shadow:              m.l2Shadow,
		L2Segment:             m.l2Segment,
		L2Offset:              m.l2Offset,
		L2ActiveBank:          m.l2ActiveBank,
		L2ShadowBank:          m.l2ShadowBank,
		BetaEnabled:           m.betaEnabled,
		BetaActive:            m.betaActive,
		RAMContentionDisabled: m.RAMContentionDisabled,
	}

	var buf bytes.Buffer
	buf.Grow(m.stateSize())
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding fixed-size values and byte slices cannot fail for any
		// reason the caller could act on, and returning an error here would
		// push a dead branch into every caller.
		panic("memory: encoding state: " + err.Error())
	}
	return buf.Bytes()
}

// stateSize estimates the encoded size so the buffer is grown once rather than
// doubled a dozen times on the way to 2 MB.
func (m *Memory) stateSize() int {
	n := 4 * PageSize                    // ROM banks
	n += 2*PageSize + len(m.mfRAM) + 512 // Alt-ROM, Multiface RAM, scalars and gob's type descriptors
	for _, bank := range m.ram {
		n += len(bank)
	}
	return n
}

// LoadState restores a state captured by SaveState.
func (m *Memory) LoadState(b []byte) error {
	var s memState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("memory: decoding state: %w", err)
	}
	// A page-map entry indexes the RAM pool directly on the next read, so an
	// impossible one has to be refused here: accepted, it would surface as a
	// panic some instructions later with nothing left pointing at the cause.
	// -1 is the write map's "this slot is ROM, drop the write" marker.
	for i, p := range s.ReadMap {
		if p < -1 || p >= len(m.ram) {
			return fmt.Errorf("memory: read map slot %d names page %d, outside the %d-bank pool", i, p, len(m.ram))
		}
	}
	for i, p := range s.WriteMap {
		if p < -1 || p >= len(m.ram) {
			return fmt.Errorf("memory: write map slot %d names page %d, outside the %d-bank pool", i, p, len(m.ram))
		}
	}

	// The pool is copied into the live banks rather than swapped in wholesale,
	// because other devices hold the bank slices: the Next wiring aliases
	// divMMC RAM onto pool banks 8-15 through PoolBank, and replacing the
	// slice would leave them writing into an orphan. Short and absent entries
	// clamp through copy(), matching RestorePool.
	for i := 0; i < len(s.RAM) && i < len(m.ram); i++ {
		if len(s.RAM[i]) == 0 || m.ram[i] == nil {
			continue
		}
		copy(m.ram[i], s.RAM[i])
	}
	for i := 0; i < len(s.ROM) && i < len(m.rom); i++ {
		if len(s.ROM[i]) == 0 || m.rom[i] == nil {
			continue
		}
		copy(m.rom[i], s.ROM[i])
	}
	copy(m.altROM0[:], s.AltROM0)
	copy(m.altROM1[:], s.AltROM1)
	copy(m.mfRAM[:], s.MFRAM)

	m.memoryPageReadMap = s.ReadMap
	m.memoryPageWriteMap = s.WriteMap
	m.port7FFD = s.Port7FFD
	m.port1FFD = s.Port1FFD
	m.portDFFD = s.PortDFFD
	m.portEFF7Reg2 = s.PortEFF7Reg2
	m.portEFF7Reg3 = s.PortEFF7Reg3
	m.specialPaging = s.SpecialPaging
	m.PagingEnabled = s.PagingEnabled
	m.PagingFrozen = s.PagingFrozen
	m.ScreenPage = s.ScreenPage
	m.slotBank = s.SlotBank
	m.mmuOverride = s.MMUOverride
	m.altROMReg = s.AltROMReg
	m.fpgaBootROMActive = s.FPGABootROMActive
	m.configModeActive = s.ConfigModeActive
	m.configModeRAMPage = s.ConfigModeRAMPage
	m.mfActive = s.MFActive
	m.l2WrEn = s.L2WrEn
	m.l2RdEn = s.L2RdEn
	m.l2Shadow = s.L2Shadow
	m.l2Segment = s.L2Segment
	m.l2Offset = s.L2Offset
	m.l2ActiveBank = s.L2ActiveBank
	m.l2ShadowBank = s.L2ShadowBank
	m.betaEnabled = s.BetaEnabled
	m.betaActive = s.BetaActive
	m.RAMContentionDisabled = s.RAMContentionDisabled
	return nil
}
