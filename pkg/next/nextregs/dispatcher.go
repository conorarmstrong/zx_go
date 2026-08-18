package nextregs

// Dispatcher implements the Spectrum Next's NextReg register file:
// 256 hardware registers, the "selected register" latch behind ports
// 0x243B (select) and 0x253B (data), and a per-register callback
// table that subsystems wire up during construction. Default
// behaviour is storage-only: writes go into a 256-byte backing array
// and reads return whatever is stored. Subsystems with side effects
// (the 8K MMU at 0x50–0x57, CPU speed at 0x07, palette at 0x40–0x44,
// sprite attributes, Copper, …) install OnWrite / OnRead handlers
// per register.
//
// The Z80N NEXTREG opcodes write through WriteReg (implementing the
// z80.NextRegSink interface in pkg/z80). Port 0x243B writes go
// through Select; port 0x253B reads and writes go through ReadData
// and WriteData. WriteReg is equivalent to Select followed by
// WriteData except it does not disturb the latched selected register
// — matching the FPGA's behaviour where NEXTREG is its own dispatch
// path.
// TraceFunc is the signature of the optional trace callback. It
// fires after every read and every write that goes through the
// dispatcher (whether the access used a per-register handler or
// fell back to plain storage). The callback receives the register
// number, the value read or written, and whether the access was a
// write (true) or read (false). Setting tracer to nil disables
// emission with no cost.
type TraceFunc func(reg, val byte, write bool)

type Dispatcher struct {
	selected byte
	regs     [256]byte
	onWrite  [256]func(*Dispatcher, byte)
	onRead   [256]func(*Dispatcher) byte
	onReset  []func()
	tracer   TraceFunc
}

// Hardware identity defaults (read-only registers the boot ROM
// inspects to decide what kind of Spectrum it's running on).
// Values match what the canonical `tbblue_reset_common` / `tbblue_hard_reset`
// publish at cold reset — see TBBlue FPGA verilog.
const (
	// MachineID at NextReg 0x00. The FPGA publishes its g_machine_id
	// generic verbatim (zxnext.vhd:5885); the board default is $0A =
	// "ZX Spectrum Next" (zxnext_top_issue2.vhd:35). NextZXOS checks
	// this right after the firmware handoff reset (ROM1 $1E69 `cp
	// $08`): $0A takes it straight to the Browser, while $08
	// ("Emulators") takes the emulator-compat branch into the +3DOS
	// re-boot retry loop ($1B04). Must stay $0A to match real hardware.
	defaultMachineID = 0x0A
	// CoreVersion at NextReg 0x01 (R). Encoded as major<<4 | minor.
	// 0x32 = core 3.02 — the current Spectrum Next core line (3.02.03 latest).
	defaultCoreVersion = 0x32
	// CoreSubMinor at NextReg 0x0E (R) — the third version number. 0x03 reports
	// core 3.02.03 (the latest release), comfortably above any game's minimum.
	defaultCoreSubMinor = 0x03
	// BoardID at NextReg 0x0F (R). bits 3:0 = board issue; 0x01 = ZXN Issue 3.
	defaultBoardID = 0x01
)

// New returns a fresh dispatcher with hardware-identity and
// reset-default registers seeded to the values the FPGA's
// `tbblue_reset_common` publishes at cold reset. NextZXOS reads
// several of these (e.g. $42 palette index, $4B transparent
// fallback, $4C clip-window, $B8/$BB stack-trap defaults) before
// the boot has had a chance to initialise them itself; if we
// leave them zero, the OS sees inconsistent state and the boot
// stalls.
//
// Subsystems that wire OnRead handlers on these registers (e.g.
// the 8K MMU on $50-$57, palette on $40-$44) override the
// defaults. Nothing else does.
func New() *Dispatcher {
	d := &Dispatcher{}
	applyResetDefaults(&d.regs)
	return d
}

// Reset restores the register file to power-on defaults, leaving
// OnRead/OnWrite handlers intact. Used by the GUI's "Reboot" menu
// item so a fresh boot doesn't inherit the previous session's
// tilemap / Layer 2 / paging configuration. The OnWrite handlers
// fire for every register so downstream subsystems (tilemap layer,
// Layer 2, MMU8, palette, divMMC pager…) also drop back to their
// power-on state — without this the tilemap would stay enabled and
// continue rendering stale bank-5 data over whatever the next boot
// path tries to display.
func (d *Dispatcher) Reset() {
	// Phase 1: drive every register through write() with the value 0
	// so handlers see the "back to defaults" event. Direct backing
	// writes alone wouldn't notify the tilemap layer / L2 / etc.
	for r := 0; r < 256; r++ {
		d.write(byte(r), 0)
	}
	// Phase 2: re-apply the non-zero tbblue_reset_common defaults,
	// again through write() so any handler that interprets the byte
	// (e.g. MMU8 page index → slotBank) propagates the value.
	var defaults [256]byte
	applyResetDefaults(&defaults)
	for r := 0; r < 256; r++ {
		if defaults[r] != 0 {
			d.write(byte(r), defaults[r])
		}
	}
	d.selected = 0
	// Phase 3: subsystem reset hooks run LAST, so a subsystem with state
	// the byte-oriented write() loops can't restore (e.g. the clip-window
	// read/write index + 4 coordinates) re-establishes its own defaults
	// after the generic reset has churned its registers.
	for _, fn := range d.onReset {
		fn()
	}
}

// applyResetDefaults seeds the FPGA's tbblue_reset_common power-on
// values. Shared between New() and Reset() so they cannot drift.
func applyResetDefaults(regs *[256]byte) {
	regs[0x00] = defaultMachineID
	regs[0x01] = defaultCoreVersion
	// NR$02 = "Reset". Per the Spectrum Next wiki:
	//   bit 0 = soft reset (read: last was soft; write 1: trigger soft)
	//   bit 1 = hard reset (read: last was hard; write 1: trigger hard)
	//
	// Default $01: the FPGA bootrom reads NR$02 at $0171 and expects
	// A=$01, reading "last reset was soft" even on cold boot — the
	// FPGA's reset cascade makes the initial cold reset look like a
	// soft reset to the bootrom, whose first action is to issue its
	// own soft reset to hand off to NextZXOS.
	//
	// WireReset's OnWrite must NOT use edge-detection on bit 0:
	// otherwise the bootrom's NR$02=$01 trigger at $6F5B finds old=$01
	// already set and silently does nothing. Fire-on-every-write is
	// required.
	regs[0x02] = 0x01
	regs[0x0E] = defaultCoreSubMinor // NR$0E = core version sub-minor
	regs[0x0F] = defaultBoardID      // NR$0F = board ID
	regs[0x05] = 0x01                // Peripheral 1: bit 0 = 50 Hz
	// Peripheral 2: bit 7 = CPU-speed hotkey enable, bit 5 = 50/60 Hz hotkey
	// enable. zxnext.vhd:5161-5165 resets both to 1 → read-back $A0 (the rest
	// of NR$06's read mux composes from bits that reset to 0).
	regs[0x06] = 0xA0
	regs[0x08] = 0x10 // Peripheral 3: bit 4 = AY3-8910 enabled
	regs[0x12] = 0x08 // Layer 2 RAM base bank
	regs[0x13] = 0x0B // Layer 2 shadow RAM base bank
	// NR$10 = Core ID (read-only). Per zxnext.vhd `nr_10_coreid` the
	// FPGA default is "00001" = $01 (Issue 2 board); we use $00
	// (generic/unspecified). The FPGA bootrom ANDs this with $03 at
	// $017E to derive its core-class decision; boot proceeds correctly
	// on either value.
	regs[0x10] = 0x00
	regs[0x14] = 0xE3 // Global transparent colour
	regs[0x42] = 0x07 // ULANext attribute byte format (zxnext.vhd:5002 = X"07")
	regs[0x4A] = 0xE3 // Transparency fallback colour (zxnext.vhd:5014 = X"E3")
	regs[0x4B] = 0xE3 // Transparent fallback colour
	regs[0x4C] = 0x0F // Tilemap transparency colour
	// Tilemap base ($2C) + tile-definitions base ($0C). zxnext.vhd:5041-5045
	// resets nr_6e_tilemap_base="101100" ($2C), nr_6f_tilemap_tiles="001100"
	// ($0C). NextZXOS reads these at boot without writing them first, so they
	// must be seeded to the FPGA reset values rather than left at zero. (bit 6
	// is masked off on write per the WireTilemapBase handler; $2C/$0C have bit
	// 6 clear so they round-trip.)
	regs[0x6E] = 0x2C
	regs[0x6F] = 0x0C
	regs[0x50] = 0xFF // MMU slot 0 = legacy ROM
	regs[0x51] = 0xFF // MMU slot 1 = legacy ROM
	regs[0x52] = 0x0A // MMU slot 2 = RAM 5 high
	regs[0x53] = 0x0B // MMU slot 3 = RAM 5 low
	regs[0x54] = 0x04 // MMU slot 4 = RAM 2 high
	regs[0x55] = 0x05 // MMU slot 5 = RAM 2 low
	regs[0x56] = 0x00 // MMU slot 6 = RAM 0 high
	regs[0x57] = 0x01 // MMU slot 7 = RAM 0 low
	regs[0xB8] = 0x83 // divMMC stack-trap enables: RST 0/8/38
	regs[0xBB] = 0xCD // divMMC stack-trap enables: NMI + 04C6/0562
	// Raspberry Pi GPIO output registers. zxnext.vhd:5070-5071 reset
	// nr_98_pi_gpio_o = X"FF", nr_99_pi_gpio_o = X"01". Not boot-relevant
	// (the Pi accelerator port), but a read-back faithfulness fix.
	regs[0x98] = 0xFF
	regs[0x99] = 0x01
	// NR$C4 = INT EN 0. nextreg.txt: "soft reset = 0x81" — bit 7 expansion-bus
	// /INT enable, bit 0 ULA interrupt enable, both default ON (bit 1 line-int
	// off). ours previously left it $00, mis-reporting the interrupt-enable
	// state on read-back (surfaced by the post-load FX snapshot diff vs the
	// FPGA-faithful oracle). WireInterruptEnable0's OnWrite masks to bits 7,1,0,
	// so seeding $81 round-trips. Read-back faithfulness; the live frame
	// interrupt is wired separately and already fires.
	regs[0xC4] = 0x81
}

// Select latches the register number for subsequent reads and writes
// via port 0x253B. Wired to ULA writes at port 0x243B.
func (d *Dispatcher) Select(reg byte) { d.selected = reg }

// Selected returns the currently latched register. Exposed for
// debugging and round-trip tests; production code should not need
// it.
func (d *Dispatcher) Selected() byte { return d.selected }

// WriteData writes to the currently selected register. Wired to ULA
// writes at port 0x253B. Goes through the optional OnWrite handler.
func (d *Dispatcher) WriteData(val byte) { d.write(d.selected, val) }

// ReadData reads from the currently selected register. Wired to ULA
// reads at port 0x253B. Goes through the optional OnRead handler.
func (d *Dispatcher) ReadData() byte { return d.read(d.selected) }

// WriteReg writes directly to a register without disturbing the
// selected latch. Implements z80.NextRegSink for the Z80N NEXTREG
// opcodes.
func (d *Dispatcher) WriteReg(reg, val byte) { d.write(reg, val) }

// ReadReg returns the value of a register without disturbing the
// selected latch. Used by snapshot save / debugger inspection.
func (d *Dispatcher) ReadReg(reg byte) byte { return d.read(reg) }

// SetOnWrite installs a write-side-effect callback for the given
// register. The callback replaces the default storage behaviour; if
// the handler wants the canonical storage updated too (the common
// case for "store the byte and also do something"), it must call
// Store itself. Pass nil to remove an installed handler.
func (d *Dispatcher) SetOnWrite(reg byte, fn func(*Dispatcher, byte)) {
	d.onWrite[reg] = fn
}

// OnWriteFn returns the currently-installed OnWrite callback for
// the given register, or nil if none. Used by callers that want
// to chain a new behaviour onto an existing handler (call prev
// first, then add their own side-effect, then re-install).
func (d *Dispatcher) OnWriteFn(reg byte) func(*Dispatcher, byte) {
	return d.onWrite[reg]
}

// SetOnRead installs a read-side computation for the given register.
// The callback's return value is what reads of the register observe;
// the backing storage is bypassed entirely. Pass nil to remove.
func (d *Dispatcher) SetOnRead(reg byte, fn func(*Dispatcher) byte) {
	d.onRead[reg] = fn
}

// SetOnReset registers a callback invoked at the END of Reset (after the
// generic register-zeroing + default-application passes). Subsystems that
// hold state outside the 256-byte backing array — the clip-window index +
// coordinates, for instance — use this to restore their own power-on
// defaults, which the byte-oriented reset passes cannot reach. Multiple
// hooks run in registration order.
func (d *Dispatcher) SetOnReset(fn func()) {
	d.onReset = append(d.onReset, fn)
}

// Store writes a byte directly to the backing storage, bypassing
// any OnWrite handler installed for that register. Subsystems use
// Store inside their own OnWrite handlers to record the canonical
// value after they've done their side effect.
func (d *Dispatcher) Store(reg, val byte) { d.regs[reg] = val }

// Raw returns the backing register byte without going through
// OnRead. Useful for debug snapshots and for handlers that
// recompute reads from stored state.
func (d *Dispatcher) Raw(reg byte) byte { return d.regs[reg] }

// GetTracer returns the currently-installed TraceFunc (or nil).
// Used by chained-tracer patterns so a new caller can wrap the
// existing tracer rather than replace it.
func (d *Dispatcher) GetTracer() TraceFunc { return d.tracer }

// SetTracer installs a per-access callback. Pass nil to disable.
// The callback fires AFTER the access — the value parameter is
// the value that was written or read, post-handler. Multiple
// SetTracer calls replace the prior callback.
func (d *Dispatcher) SetTracer(fn TraceFunc) { d.tracer = fn }

// readOnlyIdentity are the CORE-VERSION registers we protect from guest writes:
// NR$01 (major.minor) and NR$0E (sub-minor). They are fixed in the FPGA
// bitstream (nextreg.txt marks them (R)); Nextoid pokes NR$01 during play, and
// storing the poke corrupted the version it then checks, tripping "Core x.xx.xx
// needed". Internal seeding uses Store()/the init path, which bypass write().
//
// NB: NR$00 (Machine ID) and NR$0F (Board ID) are ALSO (R) on hardware, but we
// deliberately leave them writable: games poke NR$00 as an emulator/hardware
// probe (Nextoid writes it during boot) and branch on the read-back, and
// forcing the hardware value flipped Nextoid onto a path that rebooted to the
// NextZXOS welcome screen. Honouring the probe-write keeps such games running.
var readOnlyIdentity = [256]bool{0x01: true, 0x0E: true}

func (d *Dispatcher) write(reg, val byte) {
	if readOnlyIdentity[reg] {
		return
	}
	if fn := d.onWrite[reg]; fn != nil {
		fn(d, val)
	} else {
		d.regs[reg] = val
	}
	if d.tracer != nil {
		d.tracer(reg, val, true)
	}
}

func (d *Dispatcher) read(reg byte) byte {
	var v byte
	if fn := d.onRead[reg]; fn != nil {
		v = fn(d)
	} else {
		v = d.regs[reg]
	}
	if d.tracer != nil {
		d.tracer(reg, v, false)
	}
	return v
}
