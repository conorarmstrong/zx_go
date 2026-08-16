package divmmc

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Complete state capture, for rewind and for replaying execution forward from
// a captured point.
//
// Port $E3 and the 128 KB of divMMC RAM are the part a snapshot format would
// think of, and they are not what decides what the next M1 fetch reads. That
// comes from the automap engine, which is a two-phase machine: a delayed_on
// entry point (NR$BA bit clear — the variant NextZXOS runs its IM1 handler
// through) latches on one M1 fetch and only maps the overlay on the NEXT one.
// A machine captured between those two fetches is mid-operation in a way no
// port read-back describes, so pendingPageIn and the rom3 selection riding
// with it are captured explicitly. Without them a rewind lands with the trap
// in the wrong phase: the overlay never appears, or appears an instruction
// early, and the handler runs from the wrong map.
//
// Two more latches are invisible to the guest for the same reason. MAPRAM is
// sticky and survives the port write that dropped its bit, so lastE3 does not
// imply it. button_nmi is set by the NMI assertion and cleared by the automap
// that consumes it, so whether the next $0066 fetch traps is not a function of
// anything readable.
//
// Deliberately NOT captured, and why:
//
//   - card. The SD slot is a separate device with its own transaction state
//     (SPI shift register, response queue, the position of a CMD18 multi-block
//     stream). The pager only forwards ports $E7/$EB to it, so capturing it
//     here would give that state two owners and make the restore order matter.
//     See the report accompanying this change: the card has no
//     machinestate.Device of its own yet, so a rewind taken mid-SPI
//     transaction does not currently rewind the card. That is a gap in the
//     card, not something this device can fake.
//   - rom3Query, mfActiveFn, framesBump, writeLogger, pageLogger: live wiring
//     and diagnostics, installed once during construction. A rewind moves a
//     machine through time; it must not re-point it at different objects.
//
// pagerState is the wire form. Every field is exported because gob only
// encodes exported fields, and every field of the running pager that a guest
// can move is present: adding a field to Pager without adding it here is the
// failure this package's tests are built to catch.
type pagerState struct {
	// RAM is the 16 8 KB divMMC RAM banks. It is a [][]byte rather than the
	// array it comes from because gob encodes a []byte as one byte string and
	// a [N]byte element by element.
	RAM [][]byte
	// ROM is the divMMC ROM image. It is state, not a fixed part: while the
	// Next is in config mode a guest write to $0000-$3FFF lands here through
	// WriteROMByte (NR$04 = $04), which is how the firmware streams
	// enNxtmmc.rom off the SD card.
	ROM []byte

	PagedIn       bool
	Automap       bool
	PendingPageIn bool
	Rom3          bool
	PendingRom3   bool

	LastE3        byte
	MAPRAM        bool
	NMIButton     bool
	StubProtected bool
	Enabled       bool

	EntryPoints0 byte
	EntryPoints1 byte
	EPValid0     byte
	EPTiming0    byte

	// LastM1PC is the PC the pager attributes port-write events to. It is the
	// one field here nothing on the bus depends on, but it is two bytes and
	// leaving it out would make a page trace taken after a rewind name the
	// wrong instruction.
	LastM1PC uint16
}

// StateID identifies the pager in a captured machine state.
func (p *Pager) StateID() string { return "next.divmmc" }

// SaveState captures the complete pager state.
//
// The state struct references the live RAM banks and ROM rather than copying
// them: gob serialises them into the returned blob on the spot, so the copy
// happens once instead of twice.
func (p *Pager) SaveState() []byte {
	s := pagerState{
		RAM:           p.ram[:],
		ROM:           p.rom,
		PagedIn:       p.pagedIn,
		Automap:       p.automap,
		PendingPageIn: p.pendingPageIn,
		Rom3:          p.rom3,
		PendingRom3:   p.pendingRom3,
		LastE3:        p.lastE3,
		MAPRAM:        p.mapram,
		NMIButton:     p.nmiButton,
		StubProtected: p.stubProtected,
		Enabled:       p.enabled,
		EntryPoints0:  p.entryPoints0,
		EntryPoints1:  p.entryPoints1,
		EPValid0:      p.epValid0,
		EPTiming0:     p.epTiming0,
		LastM1PC:      p.lastM1PC,
	}

	var buf bytes.Buffer
	buf.Grow(NumBanks*BankSize + ROMSize + 512)
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		// Encoding byte slices and scalars cannot fail in practice, but this
		// runs from the CPU pre-fetch hook that drives capture, and that path
		// deliberately skips a failed capture rather than stopping the
		// machine. A nil blob is rejected by LoadState, so a bad capture
		// surfaces as a failed restore instead of a crash mid-frame.
		return nil
	}
	return buf.Bytes()
}

// LoadState restores a state captured by SaveState.
//
// The whole blob is decoded and checked before anything is written, so a state
// from a build with a different bank layout is refused rather than leaving the
// pager with some banks rewound and the rest running on from the present.
func (p *Pager) LoadState(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("divmmc: empty state (the capture failed)")
	}
	var s pagerState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		return fmt.Errorf("divmmc: decoding state: %w", err)
	}
	if len(s.RAM) != NumBanks {
		return fmt.Errorf("divmmc: state carries %d RAM banks, this pager has %d",
			len(s.RAM), NumBanks)
	}
	for i, bank := range s.RAM {
		if len(bank) != BankSize {
			return fmt.Errorf("divmmc: state RAM bank %d is %d bytes, want %d",
				i, len(bank), BankSize)
		}
	}

	// The banks are copied into rather than swapped in wholesale, because
	// other parts of the machine hold the bank slices: RAMBank hands out an
	// alias to the debugger and to the boot wiring that installs the TBBLUE.FW
	// snapshot, and replacing the slice would leave them reading an orphan.
	for i := range s.RAM {
		copy(p.ram[i], s.RAM[i])
	}
	// The ROM can change length across a capture — WriteROMByte grows a short
	// or absent image to 8 KB on first write — so it is only copied in place
	// when the lengths agree.
	if len(p.rom) == len(s.ROM) {
		copy(p.rom, s.ROM)
	} else {
		rom := make([]byte, len(s.ROM))
		copy(rom, s.ROM)
		p.rom = rom
	}

	p.pagedIn = s.PagedIn
	p.automap = s.Automap
	p.pendingPageIn = s.PendingPageIn
	p.rom3 = s.Rom3
	p.pendingRom3 = s.PendingRom3
	p.lastE3 = s.LastE3
	p.mapram = s.MAPRAM
	p.nmiButton = s.NMIButton
	p.stubProtected = s.StubProtected
	p.enabled = s.Enabled
	p.entryPoints0 = s.EntryPoints0
	p.entryPoints1 = s.EntryPoints1
	p.epValid0 = s.EPValid0
	p.epTiming0 = s.EPTiming0
	p.lastM1PC = s.LastM1PC
	return nil
}
