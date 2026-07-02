package divmmc

// TriggerPCs is the set of M1-fetch addresses that automatically
// page in the divMMC overlay. From the SpecNext Memory_map wiki and
// the FPGA core source:
//
//	0x0000 — RST 0 / power-on entry
//	0x0008 — RST 8 / esxDOS API entry point
//	0x0038 — IM1 interrupt vector
//	0x0066 — NMI vector
//	0x04C6 — ROM LD-BYTES inner loop hook
//	0x0562 — ROM LD-BYTES alternate entry
//
// Any M1 fetch at one of these PCs pages the overlay in. Any M1
// fetch with PC >= 0x4000 pages it back out.
var TriggerPCs = []uint16{0x0000, 0x0008, 0x0038, 0x0066, 0x04C6, 0x0562}

// BankSize is one 8 KB divMMC RAM bank. Real hardware has 8 banks
// of 8 KB (64 KB total); we model the full set because NextZXOS's
// boot uses MAPRAM mode, which exposes bank 3 at 0x0000-0x1FFF
// while bits 0-2 of port 0xE3 select which bank is visible at
// 0x2000-0x3FFF.
const BankSize = 0x2000

// NumBanks is the total number of 8 KB divMMC RAM banks. Real
// Spectrum Next hardware has 128 KB divMMC RAM = 16 banks. The
// low 4 bits of port 0xE3 select the bank; NextZXOS uses the
// full range (e.g. CONMEM writes routinely target banks 8-15
// per the bank-2 SD-driver init sequence).
const NumBanks = 16

// ROMSize is the divMMC ROM size at 0x0000-0x1FFF when paged in
// (and MAPRAM is clear).
const ROMSize = 0x2000

// MAPRAMBank is the divMMC RAM bank that is mapped at
// 0x0000-0x1FFF when the MAPRAM bit is set. Real hardware hard-
// wires this to bank 3.
const MAPRAMBank = 3

// RAMSize is the legacy single-bank size constant. Retained so
// existing tests that referenced "RAMSize" still build; new code
// should use BankSize.
const RAMSize = BankSize

// CardSlot is the minimum interface a virtual SD card exposes to
// the divMMC pager. The sdcard package's Card type satisfies this.
// Decoupling here means the divmmc package doesn't import sdcard,
// avoiding an import cycle when sdcard's host-fs Mount wants to
// reference divmmc state.
type CardSlot interface {
	WriteCS(val byte)
	WriteData(val byte)
	ReadData() byte
}

// Pager implements the Spectrum Next's divMMC SD-card controller's
// automatic ROM-paging behaviour. When the CPU fetches an M1 byte
// at one of the TriggerPCs, the pager swaps in 8 KB at
// 0x0000-0x1FFF (divMMC ROM or, when MAPRAM is set, RAM bank 3)
// and 8 KB of divMMC RAM at 0x2000-0x3FFF (the bank selected by
// port 0xE3 bits 0-2). It swaps the overlay back out when an M1
// fetch happens at a PC outside the bottom 16 KB.
//
// The pager additionally decodes the SD-card SPI ports:
//
//	port 0xE7 (write) — chip select
//	port 0xEB (read/write) — SPI data shift register
//
// These are routed to the CardSlot wired in via SetCard. When no
// card is wired, ReadData returns 0xFF (open bus / no media) and
// writes are absorbed silently.
//
// Wire-up during ModelNext construction is three calls:
//
//	pager := divmmc.New(romBytes)
//	cpu.AddPreFetchHook("divmmc", pager.Step)
//	mem.PeripheralRead = pager.HandleRead
//	mem.PeripheralWrite = pager.HandleWrite
//
// The pager intentionally does not depend on pkg/memory — it
// satisfies the (addr) -> (val, handled) shape memory expects.
type Pager struct {
	// ram holds the 16 8K divMMC RAM banks — SRAM physical pages 16-31
	// per zxnext.vhd:3093 ("00001" & divmmc_bank). This is a DEDICATED
	// region: main RAM lives at SRAM pages 128+ (zxnext.vhd:2964 adds
	// +128 to every MMU page), so divMMC RAM never aliases main-RAM
	// banks.
	ram     [NumBanks][]byte
	rom     []byte
	pagedIn bool
	automap bool
	// pendingPageIn models the "delayed_on" automap variant (NextReg
	// $BA / epTiming0 bit CLEAR): per zxnext.vhd 2898-2901 + divmmc.vhd
	// 129-148, a delayed entry point sets automap_held, which only takes
	// effect on the NEXT M1. So the trigger instruction itself still runs
	// from the pre-overlay map; the overlay pages in for the following
	// fetch. Converted to pagedIn at the top of the next Step.
	pendingPageIn bool
	// rom3 models the "rom3" automap variant (NextReg $B9 / epValid0 bit
	// CLEAR): per zxnext.vhd 2898-2901 + divmmc.vhd, a rom3 entry point
	// maps the MACHINE ROM (ROM3) at $0000-$1FFF, NOT the divMMC esxDOS
	// ROM — only the $2000-$3FFF RAM window is overlaid. When set,
	// HandleRead passes the low window through (so the normal map's ROM3
	// shows). CONMEM still forces the divMMC ROM in regardless. rom3 is
	// (re)assigned on every automap page-in, so it never goes stale.
	rom3 bool
	// pendingRom3 carries the rom3 selection for a delayed_on page-in
	// until pendingPageIn converts it to rom3 on the next M1.
	pendingRom3 bool
	// rom3Query reports whether a rom3 automap entry point at the given
	// trap PC is allowed to engage. Per zxnext.vhd:3138 this is more than
	// "ROM3 selected": it also requires sram_pre_override(0)/(2) — i.e. the
	// 8K slot covering the trap PC must be showing the machine ROM, not a
	// RAM bank the MMU mapped there. The PC is needed because that override
	// is per-slot (slot = pc>>13). nil = no gate (treat as engaged) —
	// production wires it to the memory's gate; unit tests set it explicitly.
	rom3Query func(pc uint16) bool
	// lastE3 stores the last byte written to port 0xE3 so IN A,(0xE3)
	// (issued by the divMMC IRQ handler at offset 0x0045) reads back
	// what was written, not floating-bus garbage.
	lastE3 byte
	// mapram is the latched MAPRAM bit. On real hardware MAPRAM is
	// "sticky": once set via port 0xE3 bit 3, it stays set until
	// hardware reset. Setting MAPRAM also write-protects RAM bank 3
	// (the bank that's about to appear at 0x0000-0x1FFF, simulating
	// ROM behaviour for any further trampoline code that runs there).
	mapram bool
	// nmiButton models divmmc.vhd's button_nmi latch: it is set when a
	// divMMC NMI is asserted (i_divmmc_button → NR$02 bit 2) and cleared
	// when the NMI automap engages, on RETN, or on reset. The $0066 NMI
	// automap entry points (divmmc.vhd:120-121) fire ONLY while it is set —
	// so a plain instruction fetch of $0066 (a program running its own
	// code there) does NOT page the esxDOS NMI overlay in.
	nmiButton bool
	// card is the SD slot. Nil = no card inserted.
	card CardSlot

	// writeLogger, if non-nil, is called on every successful write
	// to divMMC RAM. Diagnostic only.
	writeLogger func(bank int, addr uint16, val byte)

	// lastM1PC is the PC of the most recent M1 fetch (recorded by
	// Step). Used to attribute port-write events (conmem-on/off) to
	// the instruction that performed the OUT.
	lastM1PC uint16

	// pageLogger, if non-nil, is called on every overlay paged-state
	// transition (page-in, page-out, delayed-arm) with an event tag and
	// the triggering PC. Diagnostic only — wired from the
	// ZX_GO_DIVMMC_PAGE_TRACE env flag.
	pageLogger func(event string, pc uint16)

	// framesBump, if non-nil, is invoked from Step() whenever the
	// CPU M1-fetches at divMMC-RAM-bank-1 $2009 with the overlay
	// paged in. See SetFramesBumper.
	framesBump func()

	// mfActiveFn, if non-nil, reports whether the Next Multiface overlay is
	// the active NMI master. When it returns true the divMMC suppresses its
	// $0066 NMI-vector automap so the MF handler keeps the vector (the two
	// coexist — divMMC still automaps for esxDOS RST-$08). See SetMultifaceActiveFn.
	mfActiveFn func() bool

	// stubProtected, if true, write-protects divMMC-RAM-bank-1
	// offset $0009 (= $2009 when paged in). On real Spectrum Next
	// the FPGA shadow-protects this byte after TBBLUE.FW installs
	// its IRQ stub there — NextZXOS subsequently writes a $C9 (RET)
	// placeholder over it but the protected byte stays. Without
	// modelling that, the placeholder wins and the IRQ stub stops
	// working. SetStubProtected toggles this; we enable it when
	// LoadDivMMCRAMBank1 has installed a snapshot so the protection
	// only applies when there's something worth protecting.
	stubProtected bool

	// entryPoints0 mirrors NextReg $B8 (Divmmc Entry Points 0).
	// Each bit enables a specific instruction-fetch trap that
	// pages the divMMC overlay in:
	//
	//	bit 7 = trap on $0038 (IM 1 vector)
	//	bit 1 = trap on $0008 (RST 8 / esxDOS API)
	//	bit 0 = trap on $0000 (RST 0 / cold start)
	//
	// Default after soft reset: $83 (all three enabled).
	entryPoints0 byte

	// entryPoints1 mirrors NextReg $BB (Divmmc Entry Points 1).
	//
	//	bit 7 = trap on $3D00-$3DFF (TRDOS / NextBASIC ROM-3 hook)
	//	bit 6 = page-out on $1FF8-$1FFF M1 fetch
	//	bit 5 = trap on $056A (tape, ROM3)
	//	bit 4 = trap on $04D7 (NextZXOS, ROM3)
	//	bit 3 = trap on $0562 (tape, ROM3)
	//	bit 2 = trap on $04C6 (esxDOS, ROM3)
	//	bit 1 = trap on $0066 instant (NMI button)
	//	bit 0 = trap on $0066 delayed (NMI button)
	//
	// Default after soft reset: $CD ($3DXX trap, $1FF8 page-out,
	// $0562, $04C6, $0066 delayed all enabled). The $3DXX trap is
	// the load-bearing one for NextZXOS — the bank-2 IM 1 trampoline
	// does NEXTREG $8E,$03 to switch to bank 3, lands at $3D00
	// (which is the start of the character set in the 48K ROM
	// layout); the $3DXX trap pages in divMMC RAM, and the real
	// continuation code lives in divMMC RAM at $3D00-$3DFF.
	entryPoints1 byte

	// epValid0 mirrors NextReg $B9 (Divmmc Entry Points Validation 0).
	// Per zxnext.vhd:2892-2901 the page-IN gate for an RST entry point
	// is B8[n] ALONE; B9[n] and BA[n] select WHICH of the four automap
	// variants fires, not WHETHER it fires:
	//
	//	B9=1,BA=1 -> instant_on       (divMMC ROM, this M1)
	//	B9=1,BA=0 -> delayed_on       (divMMC ROM, next M1)
	//	B9=0,BA=1 -> rom3_instant_on  (machine ROM3 path, this M1)
	//	B9=0,BA=0 -> rom3_delayed_on  (machine ROM3 path, next M1)
	//
	// So B9 selects the divMMC-ROM (set) vs ROM3 (clear) path and BA the
	// instant (set) vs delayed (clear) timing. See Step's armRST0.
	//
	// Default after soft reset: $01 (RST $00 validated). Per zxnext.vhd:5088.
	epValid0 byte

	// epTiming0 mirrors NextReg $BA (Divmmc Entry Points Timing 0).
	// Controls per-RST automap timing/bank. See epValid0 comment for
	// the gating relationship.
	//
	// Default after soft reset: $00. Per zxnext.vhd:5089.
	epTiming0 byte

	// enabled mirrors the FPGA divMMC global enable i_en
	// (port_divmmc_io_en = internal_port_enable(8), the NR$82-85 port
	// decode enable). When false, divmmc.vhd gates o_divmmc_rom_en /
	// o_divmmc_ram_en to 0 regardless of CONMEM/automap. Defaults true
	// (the divMMC is enabled in every NextZXOS configuration); exposed
	// for faithfulness and golden-vector coverage via SetEnabled.
	enabled bool
}

// Decode mirrors the FPGA device/divmmc.vhd paging outputs for one CPU address:
// o_divmmc_rom_en / o_divmmc_ram_en / o_divmmc_rdonly / o_divmmc_ram_bank.
type Decode struct {
	ROMEn  bool // divMMC ROM mapped here ($0000-$1FFF, MAPRAM clear)
	RAMEn  bool // a divMMC RAM bank mapped here
	RDOnly bool // the mapped window is read-only
	Bank   int  // the selected divMMC RAM bank (0-15)
}

// New returns a fresh pager backed by the supplied ROM image with
// automap *disabled*. On the real Next, cold reset leaves NextReg
// $0A bit 4 = 1 (per the FPGA core), but the FPGA bootrom is
// overlaid at $0000-$3FFF and intercepts the first M1 fetch, so
// the automap trigger-PC at $0000 doesn't activate the divMMC
// overlay until much later (when guest code RST 8 / RST 38 / etc.
// is fetched after enNextZX is running).
//
// In our model, without the FPGA bootrom overlay, defaulting to
// automap=true at construction would cause the divMMC ROM to
// hijack the very first $0000 fetch (since 0x0000 is in
// TriggerPCs) and prevent enNextZX's reset entry from running.
// Path A (synthesised post-bootrom) instead enables automap
// explicitly *after* the CPU has already advanced past $0000, via
// pager.SetAutomap(true) in cmd/zx_go/next.go.
//
// A short ROM is padded conceptually — reads past its end return
// 0xFF (open-bus default). A nil rom is permitted: reads of the
// ROM area then return 0xFF for every byte, which is what real
// hardware does with no esxDOS image installed.
func New(rom []byte) *Pager {
	p := &Pager{
		rom:          rom,
		automap:      false,
		enabled:      true, // FPGA i_en — divMMC port/paging enabled by default
		entryPoints0: 0x83, // NextReg $B8 soft-reset default (traps $0000, $0008, $0038)
		entryPoints1: 0xCD, // NextReg $BB soft-reset default (traps $3DXX, $1FF8 page-out, $0562, $04C6, $0066-delayed)
		epValid0:     0x01, // NextReg $B9 default — only RST $00 is validated
		epTiming0:    0x00, // NextReg $BA default
	}
	for i := range p.ram {
		// Power-on content: the FBLabs FPGA core / reference emulator zero
		// the divMMC RAM (directly measured: every unwritten divMMC RAM
		// bank reads $00 on the reference, not $FF). NextZXOS's esxDOS directory
		// scan reads entries out of freshly-allocated divMMC RAM banks; a
		// $FF fill made every empty entry read as $FF ("end-of-directory /
		// no entry"), so the 128K-BASIC machine scan counted zero
		// available editors and fell back to the no-reveal built-in editor
		// (the black screen). Go's make() zero-fills, which is exactly the
		// hardware behaviour.
		p.ram[i] = make([]byte, BankSize)
	}
	return p
}

// SetAutomap enables or disables the automatic-paging mechanism.
// Disabling without CONMEM (port $E3 bit 7) also drops the
// currently-mapped overlay so the next M1 reads from the underlying
// memory — but if CONMEM is set the overlay stays forced in
// regardless of automap state. This matches real divMMC hardware:
// CONMEM is the "manual map" override and its priority is higher
// than the automap engine's gate.
//
// The divMMC ROM's exit routine relies on this: it sets CONMEM to
// $80 at port $E3, then writes NextReg $06 with bit 4 cleared to
// disable automap. The overlay MUST stay paged so the next
// instruction (still in divMMC ROM) can execute. Without the
// CONMEM guard the overlay drops mid-routine and the CPU falls
// into whatever NextZXOS bank is mapped at $0000 — typically
// NEXTREG $8E,$03 at bank-0 $00D9, which swaps ROM bank to 3
// and lands PC=$00DD on a data table, eventually triggering an
// infinite NEXTREG $02,$01 soft-reset loop.
func (p *Pager) SetAutomap(on bool) {
	p.automap = on
	if !on {
		// Disabling automap clears the automap-held latch: the FPGA
		// gates automap_held by the enable, so it cannot remain
		// asserted once the engine is off. The overlay still stays
		// mapped if CONMEM is forcing it — that's the `conmem` term
		// in IsPagedIn / HandleRead, independent of this latch — so
		// the divMMC ROM exit routine (CONMEM=$80, then disable
		// automap) keeps the overlay until it clears CONMEM.
		p.pageOut()
	}
}

// pageOut clears the automap-held latch and any not-yet-effective
// delayed_on page-in. A page-out (automap disable, $1FFx off-area, RETN)
// clears automap_held on real hardware, which also cancels a delayed arm
// that has been latched but not yet reflected in the memory map.
func (p *Pager) pageOut() {
	p.pagedIn = false
	p.pendingPageIn = false
	p.pendingRom3 = false
	p.rom3 = false
}

// AutomapEnabled reports whether automatic paging on trigger PCs
// is currently active.
func (p *Pager) AutomapEnabled() bool { return p.automap }

// IsPagedIn reports whether the divMMC overlay is currently
// masking memory at 0x0000–0x3FFF. This is the EFFECTIVE state —
// the FBLabs FPGA core maps the overlay when either the automap
// latch is held OR CONMEM (port $E3 bit 7) is forcing it in. The
// two sources are independent: CONMEM never sets the automap latch
// (see WritePort), so clearing CONMEM only drops the overlay when
// automap had not genuinely paged it in via a trigger fetch.
func (p *Pager) IsPagedIn() bool { return p.pagedIn || p.lastE3&0x80 != 0 }

// MAPRAM reports whether the MAPRAM bit is latched. Exposed for
// tests; production code shouldn't need it.
func (p *Pager) MAPRAM() bool { return p.mapram }

// ClearMAPRAM force-clears the MAPRAM latch. Required to model the
// NR$09 bit 3 side-effect per zxnext.vhd:
//
//	elsif nr_09_we = '1' and nr_wr_dat(3) = '1' then
//	    port_e3_reg(6) <= '0';
//
// where port_e3_reg(6) is the MAPRAM latch. The OS writes NR$09
// with bit 3 = 1 to escape MAPRAM mode without having to issue an
// OUT to port $E3 (which would also clobber CONMEM and the bank
// selector). Also clears the shadow bit in lastE3 so subsequent
// `LastE3` queries reflect the truth.
func (p *Pager) ClearMAPRAM() {
	p.mapram = false
	p.lastE3 &^= 0x40
}

// LastE3 returns the last byte written to port $E3. Bit 7 = CONMEM,
// bit 6 = MAPRAM, bits 3:0 = selected RAM bank. Debug-only accessor
// for the get-divmmc command.
func (p *Pager) LastE3() byte { return p.lastE3 }

// Step is the M1 pre-fetch hook. Registered via
// CPU.AddPreFetchHook("divmmc", pager.Step) during construction.
// Pages in on trigger PCs; pages out only when M1 fetches an
// instruction in the divMMC ROM "off-area" at 0x1FF8-0x1FFF. This
// matches the FBLabs diviface paging — esxDOS trap handlers
// deliberately JP into 0x1FFx to unmap once they're done. Pure
// PC ≥ 0x4000 is NOT a page-out trigger on real hardware (and
// using it as one would break esxDOS hook chains that JP from a
// trigger trap into RAM and only later return via the 0x1FFx
// off-area or RETN). No-op when automap is disabled.
func (p *Pager) Step(pc uint16) {
	// Remember the current instruction's PC for event attribution —
	// WritePort (an OUT handler with no pc of its own) stamps conmem
	// transition events with it. Recorded before the automap gate so
	// attribution works even while automap is disabled.
	p.lastM1PC = pc
	if !p.automap {
		return
	}
	// A "delayed_on" entry point armed on the PREVIOUS M1 takes effect
	// now (this fetch). Per zxnext.vhd 2898-2901 the delayed variants set
	// automap_held, which only gates the memory map on the NEXT M1 — so
	// the trigger instruction itself ran from the pre-overlay map and the
	// overlay pages in here, for the following fetch.
	if p.pendingPageIn {
		p.pendingPageIn = false
		p.pagedIn = true
		p.rom3 = p.pendingRom3
		if p.pageLogger != nil {
			p.pageLogger("in(delayed)", pc)
		}
	}
	// Trigger gating per NextReg $B8 (Divmmc Entry Points 0) and
	// $BB (Divmmc Entry Points 1). Each bit enables one specific
	// trap; on real hardware these registers are runtime-configurable
	// and NextZXOS toggles them based on which subsystem needs the
	// overlay. Triggers only fire when the overlay is not already
	// paged in (matching the FBLabs diviface auto-page
	// gate). Once paged in, subsequent trigger fetches are no-ops
	// until the overlay drops via the $1FF8-$1FFF off-area.
	// Per zxnext.vhd:2892-2901, an RST entry point pages the overlay in
	// whenever B8[n] = 1 — B9[n] and BA[n] select WHICH of the four
	// automap variants fires, not WHETHER it fires:
	//
	//   B9=1,BA=1 -> instant_on        (normal divMMC ROM, this M1)
	//   B9=1,BA=0 -> delayed_on        (normal divMMC ROM, next M1)
	//   B9=0,BA=1 -> rom3_instant_on   (ROM3 path, this M1)
	//   B9=0,BA=0 -> rom3_delayed_on   (ROM3 path, next M1)
	//
	// So the page-in gate is B8[n] alone. This is load-bearing for the
	// IM1 ($0038) handler: NextZXOS runs with B8=$82 (bits 1,7), B9=$00,
	// BA=$00, i.e. $0038 pages in via the rom3_delayed_on path. Gating
	// on B8[n] AND (B9[n] OR BA[n]) instead would never fire $0038 under
	// that configuration, so the IM1 handler would skip its SP-save and
	// hang.
	//
	// The instant-vs-delayed (BA/epTiming0) and rom-vs-rom3 (B9/epValid0)
	// variants ARE modelled for the ep0 RST entry points: armRST0 below
	// arms a delayed page-in via pendingPageIn (effective next M1) and
	// records the rom3 selection (low window maps the machine ROM, not the
	// divMMC esxDOS ROM — see HandleRead / the rom3 field). This is what
	// lets the $0038 IM1 entry (rom3_delayed_on) run the NextZXOS handler
	// from the machine ROM rather than derailing into the esxDOS ROM.
	// The ep1 entry points ($0066/$04C6/…/$3DXX) still page in immediately
	// (their boot paths don't depend on the variant yet).
	rstGate := p.entryPoints0
	// armRST0 pages the overlay in for an ep0 RST entry point per its
	// timing bit (NextReg $BA / epTiming0): instant_on (bit set) takes
	// effect this M1; delayed_on (bit clear) arms pendingPageIn so the
	// trigger instruction runs from the pre-overlay map and the overlay
	// pages in on the NEXT M1.
	armRST0 := func(bit byte) {
		// rom3 variant selected by epValid0 (NR$B9) bit CLEAR.
		rom3 := p.epValid0&bit == 0
		// A rom3 entry point engages automap ONLY when ROM3 is the
		// currently-selected machine ROM (zxnext.vhd:3138 sram_pre_rom3).
		// Otherwise the trigger is a no-op — the overlay stays out and the
		// machine ROM shows. (nil query = ungated, for unit tests.)
		if rom3 && p.rom3Query != nil && !p.rom3Query(pc) {
			if p.pageLogger != nil {
				p.pageLogger("DENY(rst)", pc)
			}
			return
		}
		if p.epTiming0&bit != 0 {
			p.pagedIn = true // instant_on
			p.rom3 = rom3
			if p.pageLogger != nil {
				p.pageLogger("in(rst-instant)", pc)
			}
		} else {
			p.pendingPageIn = true // delayed_on
			p.pendingRom3 = rom3
		}
	}
	if !p.pagedIn && !p.pendingPageIn {
		switch {
		case pc == 0x0000 && rstGate&0x01 != 0:
			armRST0(0x01)
			return
		case pc == 0x0008 && rstGate&0x02 != 0:
			armRST0(0x02)
			return
		case pc == 0x0038 && rstGate&0x80 != 0:
			armRST0(0x80)
			return
		case pc == 0x0066 && p.entryPoints1&0x03 != 0 && p.nmiButton && (p.mfActiveFn == nil || !p.mfActiveFn()):
			// $0066 NMI-vector trap. Per divmmc.vhd:120-121 the NMI automap
			// entry points are ANDed with button_nmi, so this fires ONLY while
			// a divMMC NMI is actually asserted (p.nmiButton) — never on a plain
			// fetch of $0066 by a program running its own code there (e.g. a
			// .nex game whose ISR spans $0066). Also NOT when the Multiface is
			// the active NMI master: the MF NMI (NR$02 bit 3) vectors to $0066
			// in the MF ROM and owns the vector; the divMMC still automaps for
			// the handler's esxDOS RST-$08 calls ($0008 etc.).
			p.pagedIn = true
			p.nmiButton = false // button_nmi clears once the automap engages (vhd:112)
			if p.pageLogger != nil {
				p.pageLogger("in(nmi)", pc)
			}
			return
		case pc == 0x04C6 && p.entryPoints1&0x04 != 0,
			pc == 0x0562 && p.entryPoints1&0x08 != 0,
			pc == 0x04D7 && p.entryPoints1&0x10 != 0,
			pc == 0x056A && p.entryPoints1&0x20 != 0:
			// The four NR$BB code entry points feed ONLY
			// divmmc_automap_rom3_delayed_on (zxnext.vhd:2901-2905), so
			// they are (a) ROM3-gated — a complete no-op unless ROM3
			// (the 48K ROM) is the selected machine ROM — and (b) the
			// delayed_on variant: the trigger instruction itself is
			// fetched from the pre-overlay map and the overlay pages in
			// on the NEXT M1. (a) is load-bearing for the NextZXOS cold
			// boot: ROM2 (+3DOS) executes its OWN code at $056A/$04C6/…
			// mid-mount, so trapping those fetches unconditionally would
			// hijack the DOS into the esxDOS tape loader instead.
			if p.rom3Query == nil || p.rom3Query(pc) {
				p.pendingPageIn = true
				p.pendingRom3 = true
			} else if p.pageLogger != nil {
				p.pageLogger("DENY(ep1)", pc)
			}
			return
		case pc >= 0x3D00 && pc <= 0x3DFF && p.entryPoints1&0x80 != 0:
			// $3DXX = rom3_instant_on (zxnext.vhd:2898: port_3dxx_msb
			// feeds divmmc_automap_rom3_instant_on, engaged via
			// i_automap_rom3_active). It pages the overlay in ONLY when
			// ROM3 is the selected machine ROM (the NEXTREG $8E,$03 →
			// $3D00 trampoline path). Outside ROM3 it is a no-op, so the
			// machine ROM at $3Dxx shows — without this gate ours paged in
			// the (empty) divMMC RAM window at $3D97 where the real machine
			// runs the ROM code. instant: takes effect this M1.
			if p.rom3Query == nil || p.rom3Query(pc) {
				p.pagedIn = true
				p.rom3 = true
				if p.pageLogger != nil {
					p.pageLogger("in(3dxx)", pc)
				}
			} else if p.pageLogger != nil {
				p.pageLogger("DENY(3dxx)", pc)
			}
			return
		}
	}
	// Page-out is handled by PostStep — see below. Per the FPGA VHDL
	// (divmmc.vhd), automap_held only drops AFTER the M1 cycle ends
	// (mreq goes high). The M1 fetch itself still reads divMMC ROM;
	// operand fetches (M2/M3) and subsequent M1s see the post-drop
	// state. If we drop pagedIn here in Step (a PreFetchHook fires
	// BEFORE the M1 read), the $1FFx opcode would be read from user
	// ROM instead of divMMC ROM — corrupting the $00EB→$1FFC tail
	// path the IRQ handler relies on for clean POP HL/POP AF/RET.
	// FPGA-firmware emulation: when the divMMC ROM's IM-1 wrapper
	// at $0056 issues "CALL $2009" the M1 fetch at $2009 lands on
	// the user stub. On real Spectrum Next hardware the FPGA pre-
	// installed an elaborate handler there (loaded from TBBLUE.FW);
	// NextZXOS subsequently writes $C9 to $2009 as a placeholder,
	// but the real-hardware behaviour is that the FPGA-installed
	// handler stays effective. We can't replicate that with a
	// synthesised-byte shadow (NextZXOS then loops re-installing
	// its placeholder, because reads return our shadow rather than
	// the $C9 it just wrote). Instead, detect the M1 fetch at
	// divMMC-RAM-bank-1 $2009 (the exact path the divMMC ROM uses)
	// and side-effect a FRAMES bump there, matching what the FPGA
	// handler would have done. The actual byte read is left alone
	// — the user's $C9 stays, so the verification loop sees what
	// it expects.
	if pc == 0x2009 && p.pagedIn && p.selectedBank() == 1 {
		// FRAMES++ side effect. This emulates the load-bearing
		// part of the TBBLUE.FW-installed stub: bumping the 24-bit
		// FRAMES counter at $5C78. Done out-of-band via the
		// framesBumper callback so divmmc doesn't need a direct
		// memory.Memory dep.
		if p.framesBump != nil {
			p.framesBump()
		}
	}
}

// PostStep is the PostFetchHook companion to Step. It applies the
// $1FF8-$1FFF page-out trigger AFTER the M1 opcode fetch has
// completed, matching the FPGA's automap_held register update which
// only takes effect on the rising edge of MREQ at end-of-M1
// (divmmc.vhd lines 138-143). The M1 fetch itself reads divMMC ROM
// (the IRQ-tail $E1 F1 FB C9 = POP HL / POP AF / EI / RET), and only
// the subsequent operand reads (POP HL's stack pops) and the next M1
// see the post-pageout memory map. Dropping pagedIn in Step
// (PreFetchHook) instead corrupts the opcode read: $1FFC would be
// fetched from user ROM (e.g. $1F = RRA) and the IRQ tail unwinds
// the wrong stack frames, drifting SP by 6 bytes per interrupt.
func (p *Pager) PostStep(pc uint16) {
	// The $1FF8-$1FFF off-area clears the automap-held latch (delayed_off),
	// which per device/divmmc.vhd:131 has NO CONMEM term — CONMEM is an
	// orthogonal force-in (lines 94-95), tracked separately in lastE3 and
	// OR'd by IsPagedIn/HandleRead/HandleWrite. So the page-out must clear
	// the latch even while CONMEM is set: the overlay stays mapped via the
	// CONMEM OR until CONMEM itself is cleared, at which point it drops.
	// (Guarding this on !CONMEM would strand the latch paged-in across a
	// CONMEM-held page-out: the esxDOS IM1 handler's JP C,$1FFC page-out
	// runs with CONMEM set, then a later trampoline clears CONMEM and the
	// RET would fall into divMMC RAM.)
	if p.pagedIn && pc >= 0x1FF8 && pc <= 0x1FFF && p.entryPoints1&0x40 != 0 {
		p.pageOut()
		if p.pageLogger != nil {
			p.pageLogger("out(offarea)", pc)
		}
	}
}

// SetPageLogger installs a diagnostic hook fired on overlay paged-state
// transitions (see pageLogger). Pass nil to disable.
func (p *Pager) SetPageLogger(fn func(event string, pc uint16)) { p.pageLogger = fn }

// SetFramesBumper installs a callback invoked whenever the CPU M1-
// fetches at divMMC-RAM-bank-1 $2009 with the overlay paged in.
// The callback should perform the equivalent of:
//
//	HL := mem.read16($5C78); HL++; mem.write16($5C78, HL)
//	if HL == 0 { (IY+$40)++ }
//
// I.e. bump the 24-bit FRAMES counter exactly as the bank-3 ROM
// IM-1 handler would. Pass nil to disable.
func (p *Pager) SetFramesBumper(fn func()) { p.framesBump = fn }

// SetMultifaceActiveFn wires a predicate reporting whether the Next Multiface
// overlay is the active NMI master. While it returns true the divMMC skips its
// $0066 NMI-vector automap (the MF owns that vector). See Step.
func (p *Pager) SetMultifaceActiveFn(fn func() bool) { p.mfActiveFn = fn }

// SetStubProtected toggles write-shadow protection on bank 1
// offset $0009. When true, any guest write to that byte is silently
// dropped. Enable this when a TBBLUE.FW snapshot has been loaded
// into bank 1 — NextZXOS will write a $C9 placeholder there during
// boot, and on real hardware the FPGA shadow keeps the protected
// stub byte; we have to emulate the same.
func (p *Pager) SetStubProtected(on bool) { p.stubProtected = on }

// StubProtected reports whether bank 1 $0009 is currently shadow-
// protected. Exposed for tests and debug snapshots.
func (p *Pager) StubProtected() bool { return p.stubProtected }

// SetEntryPoints0 updates NextReg $B8 (Divmmc Entry Points 0),
// reconfiguring which of $0000/$0008/$0038 fetches trigger the
// overlay. Called from the NextReg dispatcher's OnWrite handler.
func (p *Pager) SetEntryPoints0(val byte) { p.entryPoints0 = val }

// EntryPoints0 returns the current NextReg $B8 value (for OnRead
// and debug snapshots).
func (p *Pager) EntryPoints0() byte { return p.entryPoints0 }

// SetEntryPoints1 updates NextReg $BB (Divmmc Entry Points 1),
// reconfiguring the $3DXX trap, $1FF8 page-out, and the ROM-3
// fetch hooks ($04C6 / $04D7 / $0562 / $056A / $0066).
func (p *Pager) SetEntryPoints1(val byte) { p.entryPoints1 = val }

// EntryPoints1 returns the current NextReg $BB value.
func (p *Pager) EntryPoints1() byte { return p.entryPoints1 }

// SetEntryPointsValid0 updates NextReg $B9 (Divmmc EP Validation 0).
// Per zxnext.vhd:2891-2895, gates the RST entry points: RST n only
// fires when B8[n] AND (B9[n] OR BA[n]).
func (p *Pager) SetEntryPointsValid0(val byte) { p.epValid0 = val }

// EntryPointsValid0 returns the current NextReg $B9 value.
func (p *Pager) EntryPointsValid0() byte { return p.epValid0 }

// SetEntryPointsTiming0 updates NextReg $BA (Divmmc EP Timing 0).
// Cooperates with NR$B9 (epValid0) to gate the RST entry points.
func (p *Pager) SetEntryPointsTiming0(val byte) { p.epTiming0 = val }

// EntryPointsTiming0 returns the current NextReg $BA value.
func (p *Pager) EntryPointsTiming0() byte { return p.epTiming0 }

// IsRom3 reports whether the currently-paged overlay engaged via the rom3
// entry-point variant (NR$B9 bit clear). The divMMC ROM still shows; the
// flag only records which variant triggered it. Debug aid.
func (p *Pager) IsRom3() bool { return p.rom3 }

// SetRom3Query installs the predicate that reports whether ROM3 is the
// currently-selected machine ROM. rom3 entry points engage automap only
// when it returns true (zxnext.vhd:3138). nil leaves rom3 entries
// ungated (always engage) — the default for unit tests.
func (p *Pager) SetRom3Query(fn func(pc uint16) bool) { p.rom3Query = fn }

// AssertNMIButton latches button_nmi (divmmc.vhd:110-111): a divMMC NMI has
// been asserted (NR$02 bit 2 / the divMMC NMI button). It arms the $0066 NMI
// automap entry point for the upcoming NMI vector fetch; the latch clears
// once the automap engages, on RETN, or on reset. Call this when the divMMC
// NMI fires, NOT for a Multiface NMI (the MF owns its own $0066 vector).
func (p *Pager) AssertNMIButton() { p.nmiButton = true }

// HandleRETN is the post-RETN unmap hook. divMMC pages itself out
// when the CPU executes RETN from within the overlay — this is
// how the NMI handler (RST 0x66 → RETN) returns to the underlying
// code and surrenders the bus. The Z80 core calls this after every
// RETN. No-op when automap is disabled.
func (p *Pager) HandleRETN() {
	// button_nmi clears on RETN regardless of automap/CONMEM (divmmc.vhd:108).
	p.nmiButton = false
	if !p.automap {
		return
	}
	// RETN clears the automap-held latch unconditionally (divmmc.vhd:139,
	// i_retn_seen — no CONMEM term). CONMEM is an orthogonal force-in
	// (lines 94-95) carried separately in lastE3 and OR'd by IsPagedIn, so
	// clearing the latch here is safe under CONMEM: the overlay stays mapped
	// until CONMEM is cleared, then drops.
	if p.pageLogger != nil && p.pagedIn {
		p.pageLogger("out(retn)", 0)
	}
	p.pageOut()
}

// SetEnabled toggles the divMMC global enable (FPGA i_en). When false the
// overlay never maps — o_divmmc_rom_en/ram_en are gated to 0 — regardless of
// CONMEM or automap. Defaults true.
func (p *Pager) SetEnabled(on bool) { p.enabled = on }

// DisableNMI mirrors device/divmmc.vhd o_disable_nmi = automap OR button_nmi:
// the divMMC suppresses the maskable NMI whenever its overlay is engaged or a
// divMMC NMI is pending.
func (p *Pager) DisableNMI() bool { return p.pagedIn || p.nmiButton }

// DecodePaging reproduces device/divmmc.vhd's combinational paging decode for
// addr, from the live overlay state (the automap-held latch OR the CONMEM
// force-in), the MAPRAM latch, the $E3 bank and the divMMC enable. The result
// matches the FPGA o_divmmc_rom_en/ram_en/rdonly/ram_bank exactly (see the
// _tools/divmmc-vhdl-test golden). rdonly and bank are pure address+config
// decode and are reported even when the overlay is not enabled, matching the
// VHDL (only rom_en/ram_en are gated by i_en).
func (p *Pager) DecodePaging(addr uint16) Decode {
	conmem := p.lastE3&0x80 != 0
	in := conmem || p.pagedIn // conmem | automap
	page0 := addr < 0x2000
	page1 := addr >= 0x2000 && addr < 0x4000
	bank := int(p.lastE3) & (NumBanks - 1)
	if page0 {
		bank = MAPRAMBank // ram_bank <= X"3" when page0
	}
	romEn := page0 && in && !p.mapram
	ramEn := (page0 && in && p.mapram) || (page1 && in)
	if !p.enabled {
		romEn, ramEn = false, false
	}
	return Decode{
		ROMEn:  romEn,
		RAMEn:  ramEn,
		RDOnly: page0 || (p.mapram && bank == MAPRAMBank),
		Bank:   bank,
	}
}

// selectedBank returns the divMMC RAM bank index (0..NumBanks-1)
// that should be visible at 0x2000-0x3FFF, based on the low bits
// of port 0xE3.
func (p *Pager) selectedBank() int {
	return int(p.lastE3) & (NumBanks - 1)
}

// ReadRAM exposes one byte of divMMC RAM, addressed as a flat 128 KB
// buffer (idx 0..NumBanks*BankSize-1). The 16 × 8 KB banks are
// concatenated low-bank-first: bank 0 occupies 0..$1FFF, bank 1
// occupies $2000..$3FFF, and so on. Used by pkg/memory's config-mode
// dispatch so NR$04 = $08..$0F (the eight 16 KB SRAM banks that span
// divMMC RAM per FPGA nextreg.txt) can route guest writes/reads
// here. idx is masked to 17 bits to keep this safe under any caller.
func (p *Pager) ReadRAM(idx int) byte {
	idx &= 0x1FFFF
	return p.ram[idx>>13][idx&0x1FFF]
}

// WriteRAM is the dual of ReadRAM. Same flat-128 KB addressing.
// Without this, NR$04 = $08..$0F writes coming from boot.bin (which
// streams firmware modules into divMMC RAM) would be silently
// dropped, causing NextZXOS to fall back to 48K BASIC.
func (p *Pager) WriteRAM(idx int, val byte) {
	idx &= 0x1FFFF
	bank := idx >> 13
	off := idx & 0x1FFF
	// Mirror the HandleWrite stub-protection: config-mode writes
	// (NR$04 = $08..$0B from boot.bin / NextZXOS) come through here
	// instead of HandleWrite, so the same shadow needs to apply.
	if p.stubProtected && bank == 1 && off == 0x0009 {
		return
	}
	p.ram[bank][off] = val
	if p.writeLogger != nil {
		// Reconstruct a CPU-side address ($0000-$3FFF window)
		// from the flat index so the logger output is consistent
		// with the HandleWrite path.
		p.writeLogger(bank, uint16(off|0x2000), val)
	}
}

// RAMBank returns a slice aliasing the bytes of divMMC RAM bank
// `b` (0..NumBanks-1). Writes to the returned slice are visible
// to the emulator on the next ReadRAM. Out-of-range banks return
// nil. Used by the debugger to inspect divMMC RAM regardless of
// whether the pager is currently paged in.
func (p *Pager) RAMBank(b int) []byte {
	if b < 0 || b >= NumBanks {
		return nil
	}
	return p.ram[b][:]
}

// SnapshotRAM returns a deep copy of all divMMC RAM banks (the separate
// 16×8K pool), for faithful Next time-travel rewind. The SD driver's
// channel table + scratch structures live here, so a rewind that
// ignored divMMC RAM would diverge on the next SD operation.
func (p *Pager) SnapshotRAM() [][]byte {
	out := make([][]byte, NumBanks)
	for b := 0; b < NumBanks; b++ {
		cp := make([]byte, len(p.ram[b]))
		copy(cp, p.ram[b][:])
		out[b] = cp
	}
	return out
}

// RestoreRAM copies a SnapshotRAM result back. Short/nil entries are
// clamped by copy(), so a mismatched-size snapshot degrades gracefully.
func (p *Pager) RestoreRAM(src [][]byte) {
	for b := 0; b < len(src) && b < NumBanks; b++ {
		if src[b] == nil {
			continue
		}
		copy(p.ram[b][:], src[b])
	}
}

// ReadROMByte returns one byte from the 8 KB divMMC ROM image. Out-
// of-range reads return $FF (open-bus default, matching real
// hardware). Used by pkg/memory's NR$04 = $04 (RAMPAGE_DIVMMC_ROM)
// dispatch so boot.bin's SD-loaded enNxtmmc.rom content lands in
// the same buffer NextZXOS reads back to validate during init.
func (p *Pager) ReadROMByte(addr int) byte {
	if addr < 0 || addr >= len(p.rom) {
		return 0xFF
	}
	return p.rom[addr]
}

// WriteROMByte updates one byte of the 8 KB divMMC ROM image. The
// ROM buffer is allocated lazily to ROMSize (8K) on first write so
// boot.bin can stream enNxtmmc.rom into it even when divmmc.New was
// called with rom=nil (the install-time path that runs when the
// user hasn't dropped enNxtmmc.rom into roms/next/ but the SD has
// it). Writes past 8K are dropped — NextZXOS only validates the
// first 8K.
func (p *Pager) WriteROMByte(addr int, val byte) {
	if addr < 0 || addr >= ROMSize {
		return
	}
	if len(p.rom) < ROMSize {
		buf := make([]byte, ROMSize)
		copy(buf, p.rom)
		for i := len(p.rom); i < ROMSize; i++ {
			buf[i] = 0xFF
		}
		p.rom = buf
	}
	p.rom[addr] = val
}

// HandleRead is the memory.PeripheralRead hook. When paged in,
// 0x0000–0x1FFF reads come from divMMC ROM (or RAM bank 3 if MAPRAM
// is set — independent of CONMEM, see below) and 0x2000–0x3FFF reads
// come from the selected divMMC RAM bank. Addresses outside that
// range fall through to the normal memory map.
func (p *Pager) HandleRead(addr uint16) (byte, bool) {
	if addr >= 0x4000 {
		return 0, false
	}
	conmem := p.lastE3&0x80 != 0
	// Effective overlay-mapped = automap-held latch OR CONMEM force-in.
	// CONMEM is combinational (it never sets the latch — see WritePort)
	// so reads consult it directly here. Gated by the divMMC enable (FPGA
	// i_en): when disabled, o_divmmc_rom_en/ram_en are 0 regardless.
	pagedIn := p.pagedIn || conmem
	if !pagedIn || !p.enabled {
		return 0, false
	}
	if addr < 0x2000 {
		// divmmc.vhd:94-95: page0 ($0000-$1FFF) serves the divMMC ROM only
		// when MAPRAM is CLEAR (`rom_en = page0 & (conmem|automap) & !mapram`);
		// when MAPRAM is latched it serves RAM bank 3, INDEPENDENT of CONMEM
		// (`ram_en = page0 & (conmem|automap) & mapram`). CONMEM only gates
		// whether the overlay is paged in (the `conmem|automap` term it
		// shares with automap) — it never forces ROM over the MAPRAM bank-3
		// substitution.
		if p.mapram {
			return p.ram[MAPRAMBank][addr], true
		}
		// When the overlay is active the divMMC ROM shows at $0000-$1FFF
		// regardless of the rom3 variant (divmmc.vhd:94 rom_en has no rom3
		// term). rom3 only gates WHETHER automap engages (handled at the
		// entry-point trigger), not which ROM the low window serves.
		if int(addr) >= len(p.rom) {
			return 0xFF, true
		}
		return p.rom[addr], true
	}
	// $2000-$3FFF: the divMMC RAM-bank window. Reads return the
	// guest-visible RAM byte. On real Spectrum Next hardware
	// TBBLUE.FW pre-installs elaborate handler code in divMMC RAM
	// at FPGA-config time; NextZXOS then writes $C9 (RET) over
	// the stub address $2009 as a placeholder while keeping the
	// FPGA handler effective via FPGA-internal state we don't
	// model. Rather than returning a synthesised byte (which made
	// NextZXOS loop re-installing its placeholder), we emulate the
	// load-bearing FRAMES bump as a SIDE EFFECT of the M1 fetch in
	// Step() — see the framesBump path there.
	bank := p.selectedBank()
	off := addr - 0x2000
	return p.ram[bank][off], true
}

// HandleWrite is the memory.PeripheralWrite hook. Writes to the
// 0x2000-0x3FFF window land in the bank selected by port 0xE3
// EXCEPT when MAPRAM is set and the selected bank is bank 3 — in
// that case the write is dropped, matching the FBLabs diviface
// semantics. The 0x0000-0x1FFF window is read-only regardless.
// Writes outside 0x0000-0x3FFF fall through.
//
// CONMEM (port $E3 bit 7) forces divMMC RAM into $2000-$3FFF
// independent of overlay-paged state, so NextZXOS init can
// populate divMMC RAM banks BEFORE arming the overlay-on automap
// trigger.
func (p *Pager) HandleWrite(addr uint16, val byte) bool {
	conmem := p.lastE3&0x80 != 0
	// Effective overlay-mapped = automap-held latch OR CONMEM force-in
	// (CONMEM is combinational — see WritePort). Gated by the divMMC enable
	// (FPGA i_en) like the read path.
	pagedIn := p.pagedIn || conmem
	if !pagedIn || !p.enabled {
		return false
	}
	if addr < 0x2000 {
		// divMMC ROM (or MAPRAM-shadowed bank 3) — read-only to the
		// host: the write is absorbed (no-op), not passed through to
		// the underlying classic RAM.
		return true
	}
	if addr < 0x4000 {
		bank := p.selectedBank()
		if p.mapram && bank == MAPRAMBank {
			// MAPRAM write-protects bank 3 globally (low and high
			// windows), INDEPENDENT of CONMEM: divmmc.vhd:100
			// `rdonly = page0 | (mapram & ram_bank=3)` has no CONMEM
			// term.
			return true
		}
		// Stub protection: when a TBBLUE.FW snapshot is loaded we
		// must shadow-protect bank 1 offset $0009 (= $2009 paged
		// in) so NextZXOS's $C9 placeholder write doesn't clobber
		// the IRQ stub byte. Real FPGA does this internally.
		if p.stubProtected && bank == 1 && addr == 0x2009 {
			return true
		}
		p.ram[bank][addr-0x2000] = val
		if p.writeLogger != nil {
			p.writeLogger(bank, addr, val)
		}
		return true
	}
	return false
}

// SetWriteLogger installs a hook fired on every successful write
// to divMMC RAM. Used for diagnostics that need to know exactly
// which divMMC bank/offset was just populated.
func (p *Pager) SetWriteLogger(fn func(bank int, addr uint16, val byte)) {
	p.writeLogger = fn
}

// SetCard installs (or removes) the SD-card slot. The slot
// receives SPI traffic on ports 0xE7 (chip select) and 0xEB
// (data). Passing nil detaches any existing slot, after which
// IN A,(0xEB) returns 0xFF (no media) and OUT writes are dropped.
func (p *Pager) SetCard(c CardSlot) { p.card = c }

// Card returns the currently installed SD-card slot (nil if none). Exposed
// for instrumentation/debugging (e.g. attaching an SPI command logger).
func (p *Pager) Card() CardSlot { return p.card }

// ResetSPI models the divMMC slave-select register being reset by a
// core reset (hard or soft). On real hardware the FPGA does
// `port_e7_reg <= (others => '1')` whenever reset is asserted
// (zxnext.vhd:3308; reset = reset_hard OR reset_soft), i.e. every SPI
// chip-select is deasserted. Deasserting CS aborts any SD transaction
// in progress.
//
// This is load-bearing for the NextZXOS cold boot: the OS issues an
// NR$02 soft reset right after mounting the FAT32 card — often mid-way
// through a CMD18 multi-block stream. Without deasserting CS the card
// stays mid-stream and the post-reset SD re-init reads stale stream
// bytes instead of fresh command responses, spinning in an endless
// reset/re-init loop. We forward the deassert to the card, which (on
// the CS edge) flushes its response queue and transaction state.
func (p *Pager) ResetSPI() {
	if p.card != nil {
		p.card.WriteCS(0xFF) // all slave-selects deasserted (active-low)
	}
}

// WritePort is the ULA port-dispatch hook. divMMC decodes three
// I/O ports on the low byte:
//
//	0xE3 — control register (CONMEM/MAPRAM/bank)
//	0xE7 — SPI chip select
//	0xEB — SPI data
//
// Returns true when the port was a divMMC port, false otherwise.
//
//	bit 7 = CONMEM (force-paged-in: when set, the overlay is
//	 visible at every peek/poke regardless of the
//	 automap engine. Clearing it drops the overlay
//	 unless an automap trigger has paged it in.
//	 esxDOS's NMI handler uses CONMEM; cold boot
//	 does not.)
//	bit 6 = MAPRAM (sticky: once set, latched until hardware
//	 reset. When set AND the overlay is paged in
//	 (via automap OR CONMEM), 0x0000-0x1FFF maps to
//	 divMMC RAM bank 3 instead of divMMC ROM, and
//	 writes into bank 3 via the 0x2000-0x3FFF
//	 window are dropped. NextZXOS sets this during
//	 boot so its IRQ handler at 0x0038 can live in
//	 divMMC RAM bank 3.)
//	bits 5-4 = reserved.
//	bits 3-0 = divMMC RAM bank selector for 0x2000-0x3FFF
//	 (16 pages of 8K = 128K, zxnext.vhd port_e3_reg(3 downto 0)).
//
// Page-in state: writing to $E3 does NOT directly enable the
// overlay (that's what automap is for). It DOES force the overlay
// in via CONMEM, and clearing CONMEM drops it back to automap-
// controlled.
func (p *Pager) WritePort(port uint16, val byte) bool {
	switch port & 0xFF {
	case 0xE3:
		prevConmem := p.lastE3&0x80 != 0
		p.lastE3 = val
		if val&0x40 != 0 {
			// Sticky bit: once set, latched until hardware reset.
			p.mapram = true
		}
		if p.pageLogger != nil {
			if conmem := val&0x80 != 0; conmem != prevConmem {
				if conmem {
					p.pageLogger("conmem-on", p.lastM1PC)
				} else {
					p.pageLogger("conmem-off", p.lastM1PC)
				}
			}
		}
		// CONMEM (bit 7) is a combinational force-in override, NOT a
		// write into the automap paged-in latch. On the FBLabs FPGA
		// core the effective overlay-mapped signal is
		// `conmem OR automap_held`; CONMEM never sets automap_held.
		// So we deliberately leave p.pagedIn (the automap latch)
		// untouched here — HandleRead/HandleWrite/PostStep/HandleRETN
		// all consult CONMEM directly via lastE3 bit 7, and
		// IsPagedIn() reports the OR. This is load-bearing: NextZXOS's
		// $DB41 stub sets CONMEM, writes $2009, then clears CONMEM and
		// RETs into enNextZX ROM at $2000-$3FFF. If clearing CONMEM also
		// dropped the automap latch (were it set), that RET would land
		// in divMMC RAM instead of the intended ROM.
		return true
	case 0xE7:
		if p.card != nil {
			p.card.WriteCS(val)
		}
		return true
	case 0xEB:
		if p.card != nil {
			p.card.WriteData(val)
		}
		return true
	}
	return false
}

// ReadPort is the ULA port-read hook for divMMC. Port 0xE3 (on the
// low byte) returns the last byte written. Port 0xEB returns the
// next byte from the SD card's SPI response queue (or 0xFF if no
// card is wired). Other ports fall through.
func (p *Pager) ReadPort(port uint16) (byte, bool) {
	switch port & 0xFF {
	case 0xE3:
		// Port $E3 read-back faithfully mirrors the FBLabs FPGA core /
		// the reference (`port_e3_reg & ~0x30`): the reserved
		// bits 4-5 always read back 0, and bit 6 reflects the STICKY
		// MAPRAM latch rather than the raw last write (the latch only
		// clears on hardware reset / the NR$09 escape). NextZXOS's esxDOS
		// does a read-modify-write of $E3 around every SD/dir operation;
		// returning the raw bits 4-5 (or a non-sticky bit 6) made the
		// guest compute the wrong divMMC RAM bank for its directory
		// buffer, so the 128K-BASIC dir scan read an empty ($FF) bank and
		// fell back to the no-reveal built-in editor (black screen).
		v := p.lastE3 &^ 0x30
		if p.mapram {
			v |= 0x40
		} else {
			v &^= 0x40
		}
		return v, true
	case 0xEB:
		if p.card == nil {
			return 0xFF, true
		}
		return p.card.ReadData(), true
	}
	return 0, false
}
