package next

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/conorarmstrong/zx_go/pkg/ay"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/compositor"
	"github.com/conorarmstrong/zx_go/pkg/next/copper"
	"github.com/conorarmstrong/zx_go/pkg/next/keymap"
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/next/rtc"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/next/tilemap"
	"github.com/conorarmstrong/zx_go/pkg/next/uart"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// AutomapSetter is the minimum surface this package needs from
// pkg/next/divmmc to drive divMMC automap on/off from NextReg
// writes. Keeps the import graph acyclic.
type AutomapSetter interface {
	SetAutomap(on bool)
}

// SPIResetter is implemented by the divMMC pager so a core reset can
// deassert the SD-card SPI chip-select (port $E7 → $FF), aborting any
// in-flight transaction — the FPGA does this on every reset
// (zxnext.vhd:3308). See WireReset and divmmc.Pager.ResetSPI.
type SPIResetter interface {
	ResetSPI()
}

// EntryPointsSetter extends AutomapSetter with the runtime-
// configurable trigger-PC gates exposed via NextReg $B8 and $BB.
// See pkg/next/divmmc/divmmc.go SetEntryPoints0 / SetEntryPoints1.
type EntryPointsSetter interface {
	AutomapSetter
	SetEntryPoints0(val byte)
	EntryPoints0() byte
	SetEntryPoints1(val byte)
	EntryPoints1() byte
	SetEntryPointsValid0(val byte)
	EntryPointsValid0() byte
	SetEntryPointsTiming0(val byte)
	EntryPointsTiming0() byte
}

// WirePeripheral2 installs the NextReg 0x06 OnWrite handler.
// NextReg 0x06 ("Peripheral 2 Setting") gates the AY audio-chip mode (bits
// 1:0; 11 = hold all AY in reset — NOT a chip index, and bit 2 is PS/2 mode),
// the DRIVE-button divMMC NMI, the M1-button Multiface NMI, the
// F3 / F5-F8 hotkey enables, and the BEEP-to-internal-speaker
// routing. It does NOT gate divMMC automap — that's NR$0A bit 4,
// driven from WirePeripheral1.
//
// Per nextreg.txt 0x06:
//
//	bit 7 = F8 / F5-F6 hotkey enable
//	bit 6 = BEEP to internal speaker
//	bit 5 = F3 50/60 Hz hotkey enable
//	bit 4 = Enable divmmc NMI by DRIVE button
//	bit 3 = Enable multiface NMI by M1 button
//	bit 2 = PS/2 mode select (config mode only)
//	bits 1:0 = Audio chip mode (00 YM, 01 AY, 10 ZXN-8950, 11 AY reset)
//
// We honour the AY mode selection via the engine; the NMI-enable
// and hotkey bits are stored verbatim. pager / engine may be nil on
// classic-model paths.
func WirePeripheral2(d *nextregs.Dispatcher, engine *ay.Engine, pager AutomapSetter, mem ConfigModeReader) {
	d.SetOnWrite(0x06, func(disp *nextregs.Dispatcher, val byte) {
		if engine != nil {
			engine.Select(val)
		}
		// Per zxnext.vhd:5161-5170, bit 2 (ps2_mode) only updates
		// when config_mode = '1'. Bits 7,6,5,4,3,1,0 always update.
		// When config_mode = '0', bit 2 freezes at its previous
		// value; all other bits take the new write.
		new := val
		if mem != nil && !mem.ConfigModeActive() {
			old := disp.Raw(0x06)
			new = (val &^ 0x04) | (old & 0x04)
		}
		disp.Store(0x06, new)
	})
}

// ConfigModeReader is the minimal memory interface for reading the
// current FPGA config-mode latch. Honoured by NextReg OnWrite
// handlers whose VHDL spec gates fields on `nr_03_config_mode = '1'`.
// Pass nil for handlers that don't need the gate (= a permissive
// "always update" mode, suitable for unit tests).
type ConfigModeReader interface {
	ConfigModeActive() bool
}

// MAPRAMClearer is the divMMC interface for the NR$09-bit-3
// side-effect ("clear the MAPRAM latch"). Implemented by
// divmmc.Pager.ClearMAPRAM. Pass nil if no divMMC is present.
type MAPRAMClearer interface {
	ClearMAPRAM()
}

// WireJoystickIOMode installs the NextReg $0B OnWrite handler.
// Per nextreg.txt + zxnext.vhd:
//
//	bit 7 = 1 to enable I/O mode
//	bits 5:4 = I/O Mode (00=bit-bang, 01=clock, others reserved)
//	bit 0 = pin-zero output value
//	bits 6, 3:1 = reserved
//
// The register is modelled exactly (writable bits 7, 5:4, 0 = mask 0xB1,
// matching the FPGA write at zxnext.vhd:5200-5203). The I/O-mode BEHAVIOUR
// — repurposing the physical joystick-port pins as GPIO / a UART for
// hardware add-ons (zxnext.vhd:3340-3347 routes them to uart0/1) — is out
// of scope: it serves external hardware via the (also out-of-scope) UART
// path, and no software/game uses it. The Kempston/Sinclair joystick is
// unaffected in the normal (I/O-mode-disabled) case.
func WireJoystickIOMode(d *nextregs.Dispatcher) {
	d.SetOnWrite(0x0B, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x0B, val&0xB1)
	})
}

// WireInterruptEnable0 installs the NextReg $C4 OnWrite handler.
//
// Per nextreg.txt 0xC4 + zxnext.vhd:
//
//	bit 7 = Expansion bus /INT enable
//	bits 6:2 = Reserved
//	bit 1 = Line INT enable (ALIASES nr_22_line_interrupt_en)
//	bit 0 = ULA INT enable
//
// The bit-1 alias is critical: NextZXOS may write NR$C4 to control
// line interrupts INSTEAD of NR$22. Per VHDL line 5610 the write
// directly updates the same nr_22_line_interrupt_en signal.
//
// We drive this by computing NR$22's new byte with bit 1 set/cleared
// to match and writing it through disp.WriteReg(0x22, ...), which
// triggers the existing NR$22 OnWrite handler to recompute
// cpu.LineIntOffsetTstates.
func WireInterruptEnable0(d *nextregs.Dispatcher) {
	d.SetOnWrite(0xC4, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0xC4, val&0x83) // bits 7, 1, 0 retained
		// Bit 1 of NR$C4 aliases NR$22 bit 1 (line int enable).
		// Update NR$22's storage to reflect this AND trigger its
		// OnWrite to recompute cpu.LineIntOffsetTstates.
		oldNR22 := disp.Raw(0x22)
		var newNR22 byte
		if val&0x02 != 0 {
			newNR22 = oldNR22 | 0x02
		} else {
			newNR22 = oldNR22 &^ 0x02
		}
		if newNR22 != oldNR22 {
			disp.WriteReg(0x22, newNR22)
		}
	})
}

// WireInterruptControl installs the NextReg $C0 OnWrite handler.
//
// Per nextreg.txt 0xC0 + zxnext.vhd:
//
//	bits 7:5 = Programmable upper 3 bits of IM 2 vector
//	bit 4 = Reserved (must be 0)
//	bit 3 = Enable stackless NMI response (NMI return address goes
//	        to NextReg instead of stack)
//	bits 2:1 = Current Z80 IM mode (READ-only, write ignored)
//	bit 0 = Maskable INT mode: 0=pulse, 1=hw IM 2
//
// At minimum we store the byte for read-back (with bits 2:1 reflecting
// the CPU's current IM). The stackless-NMI effect (bit 3) IS wired: it
// drives cpu.NMIStackless so NMI acceptance writes the return address to
// NR$C2/$C3 and RETN returns from there (see CPU.NMI/RETN). NextZXOS
// launches 128 BASIC via this (NR$C0=$08 + an MF-NMI whose handler
// rewrites NR$C2/$C3 to the 128-editor entry). The programmable IM-2
// vector (bits 7:5) is still unmodelled.
//
// Soft reset: all fields = 0 (and cpu.NMIStackless cleared).
func WireInterruptControl(d *nextregs.Dispatcher, cpu *z80.CPU) {
	// The CPU reads/writes NR$C2/$C3 through the dispatcher's backing store
	// for the stackless-NMI return address (generic callbacks so pkg/z80
	// stays independent of the NextReg package).
	if cpu != nil {
		cpu.StacklessReadNR = func(reg byte) byte { return d.Raw(reg) }
		cpu.StacklessWriteNR = func(reg, val byte) { d.Store(reg, val) }
	}
	d.SetOnWrite(0xC0, func(disp *nextregs.Dispatcher, val byte) {
		// Mask bits 2:1 — they're read-only "current IM mode" status
		// that we don't yet compute from CPU state. Store as 0 so
		// guest reads see a known-zero value (cleaner than echoing
		// whatever was written). All other writable bits stored as-is.
		disp.Store(0xC0, val&^0x06)
		if cpu != nil {
			cpu.NMIStackless = val&0x08 != 0 // bit 3 = stackless NMI
		}
	})
}

// WireInterruptEnable2 installs the NextReg $C6 OnWrite handler.
//
// Per nextreg.txt 0xC6 + zxnext.vhd:5615-5617 + 6244-6245:
//
//	bit 7 = Reserved (read as 0)
//	bits 6:4 = INT enable group A (UART RX1, UART TX1, ESP)
//	bit 3 = Reserved (read as 0)
//	bits 2:0 = INT enable group B (UART RX0, UART TX0, Audio I2S)
//
// The VHDL split is `nr_c6_int_en_2_654` (3 bits) and
// `nr_c6_int_en_2_210` (3 bits). On read, bits 7 and 3 are
// hard-zero ('0' & nr_c6_int_en_2_654 & '0' & nr_c6_int_en_2_210).
func WireInterruptEnable2(d *nextregs.Dispatcher) {
	d.SetOnWrite(0xC6, func(disp *nextregs.Dispatcher, val byte) {
		// Mask reserved bits 7 and 3 to 0 — they read as zero per VHDL.
		disp.Store(0xC6, val&0x77)
	})
}

// WirePeripheral3 installs the NextReg $09 OnWrite handler.
// NextReg $09 is "Peripheral 3" per nextreg.txt; the only field
// with a side-effect outside the dispatcher's stored byte is
// bit 3 (= "clear MAPRAM"). Per zxnext.vhd:
//
//	elsif nr_09_we = '1' and nr_wr_dat(3) = '1' then
//	    port_e3_reg(6) <= '0';
//
// The other bits (7-4, 2-0) are status / mode flags that we don't
// currently surface — they're stored verbatim. Bit 3 is sticky:
// it's NOT stored as a flag, it's a one-shot trigger.
func WirePeripheral3(d *nextregs.Dispatcher, pager MAPRAMClearer) {
	d.SetOnWrite(0x09, func(disp *nextregs.Dispatcher, val byte) {
		if pager != nil && val&0x08 != 0 {
			pager.ClearMAPRAM()
		}
		// bit 3 is a one-shot — store the value but mask it off so
		// reads don't see a "MAPRAM is being cleared" stuck bit.
		// (Per VHDL, nr_09 bits stored are 7:4 and 2:0; bit 3 is
		// the trigger, not stored.)
		disp.Store(0x09, val&^0x08)
	})
}

// WireJoystickMode installs the NextReg $05 OnWrite + OnRead
// handlers per zxnext.vhd:
//
//	WRITE:
//	    nr_05_joy0 <= nr_wr_dat(3) & nr_wr_dat(7 downto 6)
//	    nr_05_joy1 <= nr_wr_dat(1) & nr_wr_dat(5 downto 4)
//
//	READ:
//	    port_253b_dat <=
//	        nr_05_joy0(1 downto 0) & '0' & nr_05_joy0(2) &
//	        nr_05_joy1(1 downto 0) & '0' & nr_05_joy1(2)
//
// So the write packs bits (7,6,3) → joy0 [MSB:LSB] and (5,4,1) →
// joy1 [MSB:LSB], while the read places joy0 at bits (7,6,_,4) and
// joy1 at bits (3,2,_,0) with bits 5 and 1 always reading as 0.
// Net effect: writing $FF reads back as $DD (joy0=$7, joy1=$7).
//
// We store the SPEC-CORRECT READ-BACK byte directly so reads
// transparently see whatever the spec says reads should return.
func WireJoystickMode(d *nextregs.Dispatcher) {
	d.SetOnWrite(0x05, func(disp *nextregs.Dispatcher, val byte) {
		// Read-back per FPGA zxnext.vhd:5897:
		//   [7:6]=joy0[1:0] [5:4]=joy1[1:0] [3]=joy0[2] [2]=eff 50/60Hz
		//   [1]=joy1[2] [0]=eff scandouble.
		// The write decode (:5156-5158) places joy0/joy1 at exactly those
		// positions, so the joystick-mode bits read back where they were
		// written; only bits 2 and 0 reflect effective video config (not
		// modelled in NR$05), so the read is the written byte with those
		// two bits masked to 0.
		disp.Store(0x05, val&0xFA)
	})
}

// WireVideoTiming installs the NextReg $11 OnWrite handler per
// zxnext.vhd:5208-5217:
//
//	when X"11" =>
//	    if nr_03_config_mode = '1' then
//	        if nr_wr_dat(2 downto 0) = "111" then
//	            nr_11_video_timing <= "000";
//	        else
//	            nr_11_video_timing <= nr_wr_dat(2 downto 0);
//	        end if;
//	    end if;
//
// Read returns "00000" & nr_11_video_timing (bits 7:3 = 0).
// Writes outside config mode are silently ignored.
//
// (The g_video_inc = "10" branch in the VHDL is a synthesis-time
// generic gating a special case for issue-2-only boards; we don't
// model that — it would just substitute "00" & wr_dat(0) for the
// stored timing field in some configurations.)
func WireVideoTiming(d *nextregs.Dispatcher, mem ConfigModeReader) {
	d.SetOnWrite(0x11, func(disp *nextregs.Dispatcher, val byte) {
		if mem != nil && !mem.ConfigModeActive() {
			return // gated off; keep stored value
		}
		field := val & 0x07
		if field == 0x07 {
			field = 0
		}
		disp.Store(0x11, field) // bits 7:3 = 0, bits 2:0 = timing
	})
}

// WireMultiAY is the legacy entry point that wires only the AY
// chip-select side of NextReg 0x06. Retained as an alias for tests
// that only care about AY behaviour; production callers should use
// WirePeripheral2 directly so the divMMC enable bit is wired too.
func WireMultiAY(d *nextregs.Dispatcher, engine *ay.Engine) {
	WirePeripheral2(d, engine, nil, nil)
}

// WireDivMMCEntryPoints installs the NextReg $B8 / $BB handlers.
// These two registers gate which instruction-fetch PCs trigger the
// divMMC overlay paging-in (or, for one bit, paging-out). See the
// EntryPoints0 / EntryPoints1 field docs on divmmc.Pager for the
// full bit layout.
//
// Soft-reset defaults (set by divmmc.New): $B8 = $83, $BB = $CD.
// These are the canonical reset values (machines/tbblue.c lines 3957-
// 3958) and match the real TBBlue firmware.
//
// NextReg $B8 bit 7 enables the $0038 trap — the standard IM 1
// hook for esxDOS / NextZXOS. Bit 0 enables the $0000 cold-start
// trap. Bit 1 enables the $0008 RST 8 esxDOS API trap.
//
// NextReg $BB bit 7 enables the $3D00-$3DFF range trap (TRDOS /
// NextBASIC ROM-3 hook — load-bearing for the NextZXOS boot, since
// the bank-2 IM 1 trampoline does NEXTREG $8E,$03 to switch to
// bank 3 and lands at $3D00 expecting divMMC RAM to be paged in
// for the continuation). Bit 6 enables the $1FF8-$1FFF page-out.
// Bits 5-2 are the ROM-3 esxDOS hook traps; bits 1-0 gate the
// $0066 NMI traps.
func WireDivMMCEntryPoints(d *nextregs.Dispatcher, eps EntryPointsSetter) {
	d.SetOnWrite(0xB8, func(disp *nextregs.Dispatcher, val byte) {
		eps.SetEntryPoints0(val)
		disp.Store(0xB8, val)
	})
	d.SetOnRead(0xB8, func(disp *nextregs.Dispatcher) byte {
		return eps.EntryPoints0()
	})
	d.SetOnWrite(0xBB, func(disp *nextregs.Dispatcher, val byte) {
		eps.SetEntryPoints1(val)
		disp.Store(0xBB, val)
	})
	d.SetOnRead(0xBB, func(disp *nextregs.Dispatcher) byte {
		return eps.EntryPoints1()
	})
	// NR$B9 (epValid0) and NR$BA (epTiming0) co-gate the RST entry
	// points per zxnext.vhd:2891-2895: RST n only fires when
	// B8[n] AND (B9[n] OR BA[n]). Defaults at construction time
	// (B9 = $01, BA = $00) mean only RST $00 is validated by default
	// — guest code must write a non-default B9 / BA to enable
	// RST $08, $38, etc. automap.
	d.SetOnWrite(0xB9, func(disp *nextregs.Dispatcher, val byte) {
		eps.SetEntryPointsValid0(val)
		disp.Store(0xB9, val)
	})
	d.SetOnRead(0xB9, func(disp *nextregs.Dispatcher) byte {
		return eps.EntryPointsValid0()
	})
	d.SetOnWrite(0xBA, func(disp *nextregs.Dispatcher, val byte) {
		eps.SetEntryPointsTiming0(val)
		disp.Store(0xBA, val)
	})
	d.SetOnRead(0xBA, func(disp *nextregs.Dispatcher) byte {
		return eps.EntryPointsTiming0()
	})
	// Seed the dispatcher's stored values so a guest read before
	// any write sees the same defaults as eps holds.
	d.Store(0xB8, eps.EntryPoints0())
	d.Store(0xB9, eps.EntryPointsValid0())
	d.Store(0xBA, eps.EntryPointsTiming0())
	d.Store(0xBB, eps.EntryPoints1())
}

// WireMachineType installs the NextReg $03 OnWrite handler for the
// config-mode bookkeeping side of machine-type selection. Real
// hardware (per TBBlue FPGA verilog) only
// reapplies the memory map when one of three conditions holds:
//
//  1. previous machine-type bits 2-0 were already 0 (still in
//     config mode), OR
//  2. the FPGA bootrom mask is still active (we're handing off
//     for the first time), OR
//  3. the special "soft re-enter config" command — value & 7 == 7
//     — is written.
//
// Once guest code has selected a real personality (bits 2-0 != 0)
// and bootrom has cleared, subsequent writes with bits 2-0 = 0 are
// just timing / lock flags; the $0000-$3FFF window stays on the
// personality's classic 7FFD / 1FFD paging. NextZXOS bank-0 init
// at `$0125` writes `$03 = $B0` post-boot.bin specifically as a
// timing update and relies on this no-remap behaviour — without
// it, the window jumps back to a RAM page and the CPU executes
// garbage that eventually re-triggers `NEXTREG $02,$01` in an
// infinite soft-reset loop.
//
// Other side-effects (clearing the FPGA bootrom mask, applying
// display-timing changes, latching reset state) are wired
// elsewhere — this handler is intentionally scoped to the
// config-mode flag so each effect is composable and individually
// testable. Compose via dispatcher.OnWriteFn() if the caller also
// needs the bootrom-clear behaviour on the same write.
func WireMachineType(d *nextregs.Dispatcher, mem *memory.Memory) {
	// NR$03 is composed of four sub-fields per zxnext.vhd:5894 (the
	// READ format) and 5121-5151 (the WRITE rules). The dispatcher's
	// flat byte storage isn't enough — write rules update sub-fields
	// independently and reads RECONSTRUCT the byte. Closure-local
	// state below tracks each sub-field separately so the post-reset
	// "NEXTREG \$03,\$B0" write (bits 2:0 = 000) leaves
	// nr_03_machine_type intact, matching the FPGA's behaviour.
	//
	// Read format (bit 7 → 0): nr_palette_sub_idx[7] || timing[6:4] ||
	// user_dt_lock[3] || machine_type[2:0]. We don't model
	// nr_palette_sub_idx (NR$43-derived), so bit 7 reads as the last
	// written bit-7 value (typically 1 for "ZXN tag").
	//
	// Defaults from zxnext.vhd:1099-1103:
	//   nr_03_machine_timing = "011" (= $03, +3 mode timing)
	//   nr_03_machine_type   = "011" (= $03)
	//   nr_03_config_mode    = '1'   (initial = config mode on)
	//   nr_03_user_dt_lock   = '0'
	var (
		machineType   byte = 0x03
		machineTiming byte = 0x03
		userDTLock    byte
		topBit        byte = 1 // mirrors nr_palette_sub_idx; default 1
	)
	// Reconstruct the byte the way zxnext.vhd:5894 does.
	read := func() byte {
		return (topBit&1)<<7 |
			(machineTiming&0x07)<<4 |
			(userDTLock&1)<<3 |
			(machineType & 0x07)
	}
	d.SetOnWrite(0x03, func(disp *nextregs.Dispatcher, val byte) {
		bootromActive := mem.FPGABootROMActive()
		// Machine_timing: per zxnext.vhd:5124, only updated when
		// bit 7 set AND user_dt_lock == 0 AND bit 3 of write == 0.
		if val&0x80 != 0 && userDTLock == 0 && val&0x08 == 0 {
			t := (val >> 4) & 0x07
			// Per VHDL switch on bits 6:4: 000/001 → 001, others
			// pass through (with "others" → 011 default).
			switch t {
			case 0, 1:
				machineTiming = 1
			case 2, 3, 4:
				machineTiming = t
			default:
				machineTiming = 3
			}
		}
		// user_dt_lock XOR-toggles on every write whose bit 3 = 1.
		userDTLock ^= (val >> 3) & 1
		// Machine_type: ONLY updated when currently in config_mode AND
		// new value's bits 2:0 are in {1,2,3,4}. zxnext.vhd:5137-5145.
		if mem.ConfigModeActive() {
			t := val & 0x07
			if t >= 1 && t <= 4 {
				machineType = t
			}
		}
		// Config_mode transition: bits 2:0 = 111 re-enters, anything
		// else non-zero exits, 000 = no change. zxnext.vhd:5147-5151.
		switch val & 0x07 {
		case 0x00:
			// No change to config_mode bit.
		case 0x07:
			mem.EnterConfigMode()
		default:
			mem.ClearConfigMode()
		}
		// Top bit (nr_palette_sub_idx) — track the written value so
		// reads return what was last written. Real FPGA derives this
		// from NR$43; we don't model that, so mirror what NextZXOS
		// expects (always 1 for the ZXN signature).
		topBit = (val >> 7) & 1
		// Bootrom mask clears on the very first write to NR$03 (any
		// value). zxnext.vhd:5122.
		if bootromActive {
			mem.ClearFPGABootROM()
		}
		// Store the reconstructed byte so plain `Raw()` / debug
		// queries see the post-write value as a real machine would.
		disp.Store(0x03, read())
	})
	// Read handler so explicit nextreg-read returns the reconstructed
	// byte too, identical to FPGA port_253b_dat behaviour at line 5894.
	d.SetOnRead(0x03, func(_ *nextregs.Dispatcher) byte { return read() })
}

// WireConfigModeRAMPage installs the NextReg $04 OnWrite handler.
// NextReg $04 ("MMU Bank 0 selection in config mode" / REG_RAMPAGE)
// is the FPGA-firmware-only switch that picks which 16 KB RAM page
// is visible at $0000-$3FFF while the machine is in config mode
// (NextReg $03 bits 2-0 = 0). The TBBlue firmware boot.bin issues
// successive writes during its ROM-load loop — each ROM image
// gets loaded into its own personality-target page.
//
// On real hardware this register is gated by "config mode only";
// writes outside config mode are silent no-ops. We replicate that
// by always latching the value in the dispatcher, but only routing
// to memory while config mode is active. The latch lets a guest
// re-read its last RAMPAGE selection across an exit/re-enter cycle.
func WireConfigModeRAMPage(d *nextregs.Dispatcher, mem *memory.Memory) {
	d.SetOnWrite(0x04, func(disp *nextregs.Dispatcher, val byte) {
		mem.SetConfigModeRAMPage(val)
		disp.Store(0x04, val)
	})
}

// WirePeripheral1 installs the NextReg 0x0A OnWrite handler.
// NextReg 0x0A ("Peripheral 1 Setting") holds mouse / joystick /
// AY-mode bits; it does NOT control divMMC despite a widespread
// misreading of some reference sources. The divMMC master enable is
// NextReg $06 bit 4 (wired in WirePeripheral2), matching the real
// Spectrum Next hardware.
//
// We store the value verbatim so guest reads see what they wrote
// — joystick mode and mouse DPI come from this register but the
// rest of the emulator doesn't model those yet. The one bit we DO
// honour is bit 4 ("Enable divMMC automap"), driving the pager's
// auto-paging gate. Per nextreg.txt 0x0A bit 4 and zxnext.vhd
// line 4112 (`divmmc_automap_reset <= '1' when port_divmmc_io_en
// = '0' or nr_0a_divmmc_automap_en = '0'`), this is the canonical
// source of truth for whether the divMMC's M1-fetch-driven
// trampoline can fire. Hard-reset default is 0.
func WirePeripheral1(d *nextregs.Dispatcher, pager AutomapSetter, mem ConfigModeReader) {
	d.SetOnWrite(0x0A, func(disp *nextregs.Dispatcher, val byte) {
		if pager != nil {
			pager.SetAutomap(val&0x10 != 0)
		}
		// Per zxnext.vhd:5191-5198, bits 7:6 (mf_type) and bit 5
		// (sd_swap) only update when config_mode = '1'. Bits 4,3,
		// 1,0 always update. Bit 2 is reserved (reads as 0).
		// When config_mode = '0', bits 7:5 freeze at their old
		// values.
		new := val
		if mem != nil && !mem.ConfigModeActive() {
			old := disp.Raw(0x0A)
			new = (val &^ 0xE0) | (old & 0xE0)
		}
		// Bit 2 reads as 0 per zxnext.vhd:5908.
		new &^= 0x04
		disp.Store(0x0A, new)
	})
}

// WireContentionDisable installs the NextReg 0x08 OnWrite handler.
// NextReg 0x08 is officially "Peripheral 3" and exposes multiple
// behaviour bits (port-FF disable, RAM contention disable, lock
// 7FFD, etc.); we honour bit 6 (RAM contention disable) and bit 7
// (7FFD paging-lock clear). Other bits are stored verbatim but have
// no observable effect yet.
func WireContentionDisable(d *nextregs.Dispatcher, mem *memory.Memory) {
	d.SetOnWrite(0x08, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5176, contention disable is BIT 6 ($40),
		// not bit 1. Bit 1 ($02) is PSG TurboSound enable.
		mem.RAMContentionDisabled = val&0x40 != 0
		// Bit 7 = 1 clears the $7FFD paging lock (zxnext.vhd:
		// 3654-3656: nr_08_we and dat(7) → port_7ffd_reg(5) <= '0').
		// Bit 7 = 0 leaves the lock untouched — NextZXOS's staging
		// read-modify-write of this register relies on both directions.
		if val&0x80 != 0 {
			mem.PagingEnabled = true
		}
		disp.Store(0x08, val)
	})
	// Read side: bit 7 is a LIVE view of NOT port_7ffd_locked
	// (zxnext.vhd:5906), not a stored bit.
	d.SetOnRead(0x08, func(disp *nextregs.Dispatcher) byte {
		v := disp.Raw(0x08) & 0x7F
		if mem.PagingEnabled {
			v |= 0x80
		}
		return v
	})
}

// WireLayer2 installs the NextReg 0x12 / 0x13 / 0x69 OnWrite
// handlers. 0x12 / 0x13 select the active / shadow framebuffer
// banks; 0x69 bit 7 enables Layer 2 rendering. Without 0x69 wired
// guest software can't turn Layer 2 on, so the entire render
// pipeline would be unreachable.
//
// Other bits of 0x69 carry Display-Control state (timing, etc.)
// that we store but don't yet act on.
func WireLayer2(d *nextregs.Dispatcher, l *layer2.Layer2) {
	d.SetOnWrite(0x12, func(disp *nextregs.Dispatcher, val byte) {
		l.SetActiveBank(val)
		// Per zxnext.vhd read NR$12 = '0' || active_bank(6:0).
		// Bit 7 reserved.
		disp.Store(0x12, val&0x7F)
	})
	d.SetOnWrite(0x13, func(disp *nextregs.Dispatcher, val byte) {
		l.SetShadowBank(val)
		// Per zxnext.vhd read NR$13 = '0' || shadow_bank(6:0).
		// Bit 7 reserved.
		disp.Store(0x13, val&0x7F)
	})
	d.SetOnWrite(0x69, func(disp *nextregs.Dispatcher, val byte) {
		l.SetEnabled(val&0x80 != 0)
		disp.Store(0x69, val)
	})
	d.SetOnWrite(0x70, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5475-5477 (read at :6114): bits 5:4 = Layer 2
		// resolution (0 = 256×192, 1 = 320×256, 2 = 640×256), bits 3:0 =
		// palette offset, bits 7:6 reserved (read 0). Drive the live
		// resolution so the hi-res render path activates; store the
		// spec-masked byte for read-back.
		l.SetResolution((val >> 4) & 0x03)
		l.SetPaletteOffset(val & 0x0F)
		disp.Store(0x70, val&0x3F)
	})
	// Layer 2 X scroll is a 9-bit value: NR$71 bit0 (MSB) || NR$16 (low 8).
	// Per zxnext.vhd's layer2 instantiation (i_scroll_x => nr_71 & nr_16) and
	// the address generator (layer2.vhd:152). NR$17 is the 8-bit Y scroll.
	pushScrollX := func(disp *nextregs.Dispatcher) {
		l.SetScrollX(uint16(disp.Raw(0x71)&0x01)<<8 | uint16(disp.Raw(0x16)))
	}
	d.SetOnWrite(0x16, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x16, val)
		pushScrollX(disp)
	})
	d.SetOnWrite(0x17, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x17, val)
		l.SetScrollY(val)
	})
	d.SetOnWrite(0x71, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5479-5480 + read at :6117 — only bit 0 (Layer 2
		// scrollX MSB) is stored; bits 7:1 read as 0.
		disp.Store(0x71, val&0x01)
		pushScrollX(disp)
	})
}

// LayerPriority stores the NextReg 0x15 "Sprite / layers priority"
// byte and exposes the decoded ordering mode for the compositor.
// LayerPriority satisfies compositor.PrioritySource so it can be
// installed directly via compositor.SetPrioritySource.
type LayerPriority struct{ raw byte }

// NewLayerPriority returns a fresh priority store with all-zeros
// (the SLU "Sprites over Layer 2 over ULA" reset mode).
func NewLayerPriority() *LayerPriority { return &LayerPriority{} }

// Set updates the stored byte.
func (p *LayerPriority) Set(v byte) { p.raw = v }

// Get returns the raw byte.
func (p *LayerPriority) Get() byte { return p.raw }

// Mode returns the low 2 bits decoded as compositor.PriorityMode.
func (p *LayerPriority) Mode() compositor.PriorityMode {
	// The layer-priority selector is NR$15 bits 4:2 (zxnext.vhd:5230-5234),
	// NOT bits 1:0 (which are sprite-enable and "sprites over border"). Values
	// 0..3 of the 3-bit field map to our four pure modes SLU/LSU/SUL/LUS.
	return compositor.PriorityMode((p.raw >> 2) & 0x07)
}

// setSpriteAttrByte applies sprite attribute byte idx (0..4) to the
// currently-selected sprite. Shared by the NR$35-$39 (no auto-increment)
// and NR$75-$79 (auto-increment) register groups.
//
//	byte 0: X LSB
//	byte 1: Y LSB
//	byte 2: palette offset (7:4) + X mirror/Y mirror/rotate (3:1) + X9 (0)
//	        — FPGA sprites.vhd:813
//	byte 3: visible (7) + extended-5th-byte (6) + pattern[5:0]
//	byte 4: scale / N6 / 8bpp / Y-MSB — FPGA sprites.vhd:437, decoded in
//	        RenderScanline when the sprite is Extended
func setSpriteAttrByte(e *sprite.Engine, idx int, val byte) {
	e.ApplyAttrByte(idx, val)
}

// WireSprites installs the NextReg sprite-select (0x34), attribute-byte
// (0x35-0x39), and auto-incrementing attribute (0x75-0x79) handlers.
//
// NR$35-$39 write attribute bytes 0-4 to the selected sprite. NR$75-$79
// do the same but auto-increment the sprite index afterwards (FPGA: the
// register number's bit 6 drives mirror_inc), so software can stream
// consecutive sprites without re-selecting between each.
func WireSprites(d *nextregs.Dispatcher, e *sprite.Engine) {
	d.SetOnWrite(0x34, func(disp *nextregs.Dispatcher, val byte) {
		e.SelectSprite(val)
		// Per zxnext.vhd read NR$34 = '0' || sprite_mirror_id(6:0).
		// Bit 7 is reserved (always 0 on read).
		disp.Store(0x34, val&0x7F)
	})
	for i := 0; i < 5; i++ {
		idx := i
		base := byte(0x35 + i)
		alias := byte(0x75 + i)
		d.SetOnWrite(base, func(disp *nextregs.Dispatcher, val byte) {
			setSpriteAttrByte(e, idx, val)
			disp.Store(base, val)
		})
		d.SetOnWrite(alias, func(disp *nextregs.Dispatcher, val byte) {
			setSpriteAttrByte(e, idx, val)
			e.SelectSprite(e.SelectedSprite() + 1)
			disp.Store(base, val) // shares the base register's read-back
		})
	}
}

// WireLayerPriority installs the NextReg 0x15 OnWrite/OnRead
// handlers. The compositor reads p.Get() to drive the 5-source
// ordering rules from the NextReg 0x15 documentation.
func WireLayerPriority(d *nextregs.Dispatcher, p *LayerPriority, sprites *sprite.Engine) {
	d.SetOnWrite(0x15, func(disp *nextregs.Dispatcher, val byte) {
		p.Set(val)
		// NR$15 bit 0 = "Enable sprites" (registers.txt 0x15). This is the
		// sprite layer's master enable; without it the compositor's
		// doSprites gate (c.sprites.Enabled()) stays false and no sprite
		// renders. Sonic enables its sprites (sonic + HUD) via NR$15=$45.
		if sprites != nil {
			sprites.SetEnabled(val&0x01 != 0)
			// NR$15 bit 6 = "sprite_priority" (zero-on-top, sprites.vhd:73):
			// when set, sprite 0 is drawn in front of higher-numbered
			// overlapping sprites. Sonic sets it (NR$15=$45) to layer its
			// ring-counter HUD icons over the band sprites in the same cells.
			sprites.SetZeroOnTop(val&0x40 != 0)
			// NR$15 bit 1 = "sprites over border", bit 5 = "border clip enable"
			// (sprites.vhd:1043-1066). With over-border off (Sonic's NR$15=$45)
			// the clip window is shifted into the 256x192 paper area, hiding
			// sprites parked in the top-border band (Y<32) — Sonic's stray HUD
			// row at Y=1, the garbled top-right icons.
			sprites.SetOverBorder(val&0x02 != 0)
			sprites.SetBorderClip(val&0x20 != 0)
		}
		disp.Store(0x15, val)
	})
}

// WirePalette installs the NextReg 0x40 (index), 0x41 (8-bit
// value with auto-increment), 0x43 (palette select) and 0x44
// (9-bit value, two-byte sequence) handlers.
//
// NextReg 0x44 is two-byte: the first write captures the high
// byte and the second write applies both bytes to the active
// palette entry. We track that via a small two-step latch.
func WirePalette(d *nextregs.Dispatcher, b *palette.Bank) {
	d.SetOnWrite(0x40, func(disp *nextregs.Dispatcher, val byte) {
		b.SetIndex(val)
		disp.Store(0x40, val)
	})
	d.SetOnRead(0x40, func(_ *nextregs.Dispatcher) byte {
		return b.Index()
	})
	d.SetOnWrite(0x41, func(disp *nextregs.Dispatcher, val byte) {
		b.Write8(val)
		disp.Store(0x41, val)
	})
	// NR$41 read = palette value bits 8:1 at the current index
	// (zxnext.vhd:6038); reads do NOT auto-increment. Without this the
	// dispatcher returned the last-written byte, so NextZXOS's palette
	// read-back loop (IN A,($253B) over NR$41/$44) read garbage.
	d.SetOnRead(0x41, func(_ *nextregs.Dispatcher) byte {
		return b.Read8()
	})
	d.SetOnWrite(0x43, func(disp *nextregs.Dispatcher, val byte) {
		// Per FPGA zxnext.vhd:5388-5394 + 6952:
		// bit 7 = palette autoinc disable
		// bits 6:4 = write-target palette select (wsel, 3 bits)
		// bit 3 = active sprite palette
		// bit 2 = active layer 2 palette
		// bit 1 = active ULA palette
		// bit 0 = ulanext enable
		// The tilemap active-palette select is NOT in $43 — it
		// lives in NR$6B bit 4 (see WireTilemap).
		wsel := (val >> 4) & 0x07
		// Per FPGA zxnext.vhd:
		// line 6957 ULA+Tilemap SRAM write enable:
		// nr_ulatm_we =... and NOT (wsel(1) XOR wsel(0))
		// line 7011 L2+Sprite SRAM write enable:
		// nr_l2s_palette_we =... and (wsel(1) XOR wsel(0))
		// line 6952/7029 palette address:
		// bits 9:8 = wsel(1) & wsel(2)
		//
		// So wsel picks one of 8 banks across two SRAMs:
		// wsel 0 → ULA First, wsel 1 → L2 First,
		// wsel 2 → Sprite First, wsel 3 → Tilemap First,
		// wsel 4 → ULA Second, wsel 5 → L2 Second,
		// wsel 6 → Sprite Second,wsel 7 → Tilemap Second.
		// (Note Zeus enum order — what NextBASIC programmers see —
		// is not the same as the raw wsel order.)
		wselToZeus := [8]byte{
			byte(palette.PaletteULAFirst),
			byte(palette.PaletteLayer2First),
			byte(palette.PaletteSpritesFirst),
			byte(palette.PaletteTilemapFirst),
			byte(palette.PaletteULASecond),
			byte(palette.PaletteLayer2Second),
			byte(palette.PaletteSpritesSecond),
			byte(palette.PaletteTilemapSecond),
		}
		b.Select(wselToZeus[wsel])
		// bit 7 = palette auto-increment disable (zxnext.vhd:5389) — when set,
		// NR$41/$44 writes do not advance the index.
		b.SetAutoIncDisable(val&0x80 != 0)
		b.SetActive(palette.LayerULA, (val>>1)&1)
		b.SetActive(palette.LayerLayer2, (val>>2)&1)
		b.SetActive(palette.LayerSprites, (val>>3)&1)
		disp.Store(0x43, val)
	})
	d.SetOnWrite(0x44, func(disp *nextregs.Dispatcher, val byte) {
		b.WriteNR44(val)
		disp.Store(0x44, val)
	})
	// NR$44 read = priority(7:6) | value bit 0 (zxnext.vhd:6047); reads do
	// NOT auto-increment. Priority isn't stored yet (reads as 0); the
	// colour LSB is faithful.
	d.SetOnRead(0x44, func(_ *nextregs.Dispatcher) byte {
		return b.ReadNR44()
	})
}

// WireOpts is the configuration struct for the Wire umbrella.
// All fields except Dispatcher / Memory / CPU are optional —
// passing nil skips the corresponding handler registration.
//
// What's NOT here (intentionally):
// - DMA — port-mapped (port 0x6B), not NextReg-mapped, so the
// wiring goes through ULA.SetNextDMA instead. The testharness
// wires both halves; client code that builds its own bus
// should remember to wire ULA's port handler in addition to
// calling Wire().
// - Pi accelerator — the dispatcher's default storage already
// makes NextReg 0x90+ safe no-ops, so no consumer needs a
// dedicated handler.
type WireOpts struct {
	Dispatcher *nextregs.Dispatcher
	Memory     *memory.Memory
	CPU        *z80.CPU
	AYEngine   *ay.Engine
	Layer2     *layer2.Layer2
	Palette    *palette.Bank
	Priority   *LayerPriority
	Sprites    *sprite.Engine
	Copper     *copper.Copper
	RTC        *rtc.RTC
	UART       *uart.UART
	Keymap     *keymap.Map
	Tilemap    *tilemap.Tilemap
	// DivMMCPager, if set, has its SetAutomap toggled by NextReg
	// $06 bit 4 writes (the master "Enable DivMMC" gate; see
	// WirePeripheral2). If the value also satisfies
	// EntryPointsSetter, NextReg $B8 / $BB writes route through to
	// the pager's per-trap configuration too. Pass nil on
	// classic-model or test paths that don't construct a divMMC
	// overlay.
	DivMMCPager AutomapSetter
}

// Wire is the umbrella that installs every NextReg handler in one
// call. Callers who want finer-grained control can still call the
// individual Wire* helpers directly.
//
// Wire also installs the memory.SpeedMultiplier hook so RAM
// contention scales with the CPU's current speed.
func Wire(opts WireOpts) {
	WireMMU(opts.Dispatcher, opts.Memory)
	WireROMBank(opts.Dispatcher, opts.Memory)
	WireAltROM(opts.Dispatcher, opts.Memory)
	WireMachineType(opts.Dispatcher, opts.Memory)
	WireConfigModeRAMPage(opts.Dispatcher, opts.Memory)
	spiReset, _ := opts.DivMMCPager.(SPIResetter)
	WireReset(opts.Dispatcher, opts.Memory, opts.CPU, spiReset)
	WireCPUSpeed(opts.Dispatcher, opts.CPU)
	WireLineInterrupt(opts.Dispatcher, opts.CPU)
	WireContentionDisable(opts.Dispatcher, opts.Memory)
	WirePeripheral2(opts.Dispatcher, opts.AYEngine, opts.DivMMCPager, opts.Memory)
	WirePeripheral1(opts.Dispatcher, opts.DivMMCPager, opts.Memory)
	WireVideoTiming(opts.Dispatcher, opts.Memory)
	WireJoystickMode(opts.Dispatcher)
	WireJoystickIOMode(opts.Dispatcher)
	WireInterruptControl(opts.Dispatcher, opts.CPU)
	WireInterruptEnable0(opts.Dispatcher)
	WireInterruptEnable2(opts.Dispatcher)
	if mc, ok := opts.DivMMCPager.(MAPRAMClearer); ok {
		WirePeripheral3(opts.Dispatcher, mc)
	} else {
		WirePeripheral3(opts.Dispatcher, nil)
	}
	if eps, ok := opts.DivMMCPager.(EntryPointsSetter); ok {
		WireDivMMCEntryPoints(opts.Dispatcher, eps)
	}
	if opts.Layer2 != nil {
		WireLayer2(opts.Dispatcher, opts.Layer2)
		// Let memory's Layer-2 write/read paging track the live NR$12/$13
		// banks for its redirect into Layer-2 RAM.
		if opts.Memory != nil {
			opts.Memory.SetLayer2BankSource(opts.Layer2.ActiveBank, opts.Layer2.ShadowBank)
		}
	}
	if opts.Palette != nil {
		WirePalette(opts.Dispatcher, opts.Palette)
	}
	if opts.Priority != nil {
		WireLayerPriority(opts.Dispatcher, opts.Priority, opts.Sprites)
	}
	if opts.Sprites != nil {
		WireSprites(opts.Dispatcher, opts.Sprites)
	}
	if opts.Copper != nil {
		WireCopper(opts.Dispatcher, opts.Copper)
	}
	if opts.RTC != nil {
		WireRTC(opts.Dispatcher, opts.RTC)
	}
	if opts.UART != nil {
		WireUART(opts.Dispatcher, opts.UART)
	}
	if opts.Keymap != nil {
		WireKeymap(opts.Dispatcher, opts.Keymap)
	}
	if opts.Tilemap != nil {
		WireTilemap(opts.Dispatcher, opts.Tilemap, opts.Palette)
	}
	WirePeripheralMasks(opts.Dispatcher)
	WireClipWindows(opts.Dispatcher, opts.Tilemap, opts.Sprites)
	opts.Memory.SpeedMultiplier = opts.CPU.SpeedMultiplier
	applyTBBLUEFWBootDefaults(opts.Dispatcher)
}

// clipWindow models a Spectrum Next clip-window register (NR$18 Layer2,
// $19 sprite, $1A ULA, $1B tilemap). Each holds four sub-coordinates
// {x1,x2,y1,y2} addressed by a 2-bit index. Per zxnext.vhd 5242-5276 a
// WRITE stores coord[idx] then advances idx (wrapping at 4); per the read
// mux 5947-5975 a READ returns coord[idx] WITHOUT advancing. NR$1C resets
// the indices (write) and reads them back packed (read).
type clipWindow struct {
	coord [4]byte // x1, x2, y1, y2
	idx   byte    // 0..3
	def   [4]byte // power-on / reset coordinate defaults
}

func (c *clipWindow) write(v byte) {
	c.coord[c.idx&3] = v
	c.idx = (c.idx + 1) & 3
}

func (c *clipWindow) read() byte { return c.coord[c.idx&3] }

func (c *clipWindow) reset() {
	c.coord = c.def
	c.idx = 0
}

// WireClipWindows installs the faithful clip-window behaviour for NR$18-$1B
// plus the NR$1C index reset/read-back, replacing the old single-byte
// approximation. Reset defaults per zxnext.vhd 4959-4982: Layer2/sprite/ULA
// = {00,FF,00,BF}; tilemap = {00,9F,00,FF}.
func WireClipWindows(d *nextregs.Dispatcher, tmLayer *tilemap.Tilemap, sprites *sprite.Engine) {
	l2 := &clipWindow{def: [4]byte{0x00, 0xFF, 0x00, 0xBF}}
	spr := &clipWindow{def: [4]byte{0x00, 0xFF, 0x00, 0xBF}}
	ula := &clipWindow{def: [4]byte{0x00, 0xFF, 0x00, 0xBF}}
	tm := &clipWindow{def: [4]byte{0x00, 0x9F, 0x00, 0xFF}}
	all := []*clipWindow{l2, spr, ula, tm}
	for _, c := range all {
		c.reset()
	}
	// Push the tilemap clip coords into the live tilemap layer so NR$1B
	// actually clips the render (not just stores the register).
	pushTM := func() {
		if tmLayer != nil {
			tmLayer.SetClip(tm.coord[0], tm.coord[1], tm.coord[2], tm.coord[3])
		}
	}
	pushTM()
	// Push the sprite clip coords into the live sprite engine so NR$19
	// actually clips the render (not just stores the register) — without this
	// "parked"/off-window sprites the game expects hidden still draw.
	pushSpr := func() {
		if sprites != nil {
			sprites.SetClip(spr.coord[0], spr.coord[1], spr.coord[2], spr.coord[3])
		}
	}
	pushSpr()
	wire := func(reg byte, c *clipWindow) {
		d.SetOnWrite(reg, func(_ *nextregs.Dispatcher, v byte) { c.write(v) })
		d.SetOnRead(reg, func(_ *nextregs.Dispatcher) byte { return c.read() })
	}
	wire(0x18, l2)
	d.SetOnWrite(0x19, func(_ *nextregs.Dispatcher, v byte) { spr.write(v); pushSpr() })
	d.SetOnRead(0x19, func(_ *nextregs.Dispatcher) byte { return spr.read() })
	wire(0x1A, ula)
	d.SetOnWrite(0x1B, func(_ *nextregs.Dispatcher, v byte) { tm.write(v); pushTM() })
	d.SetOnRead(0x1B, func(_ *nextregs.Dispatcher) byte { return tm.read() })
	// NR$1C write: bits 0/1/2/3 reset the L2/sprite/ULA/tilemap index
	// (zxnext.vhd:5278-5290). NR$1C read: indices packed as
	// tm<<6 | ula<<4 | spr<<2 | l2 (zxnext.vhd:5975).
	d.SetOnWrite(0x1C, func(_ *nextregs.Dispatcher, v byte) {
		if v&0x01 != 0 {
			l2.idx = 0
		}
		if v&0x02 != 0 {
			spr.idx = 0
		}
		if v&0x04 != 0 {
			ula.idx = 0
		}
		if v&0x08 != 0 {
			tm.idx = 0
		}
	})
	d.SetOnRead(0x1C, func(_ *nextregs.Dispatcher) byte {
		return tm.idx<<6 | ula.idx<<4 | spr.idx<<2 | l2.idx
	})
	d.SetOnReset(func() {
		for _, c := range all {
			c.reset()
		}
	})
}

// WirePeripheralMasks installs OnWrite handlers for NR$81, $85, $89,
// $8A that apply the reserved-bit masks the FPGA enforces. Without
// these, the dispatcher's default store-as-is leaks reserved bits
// into subsequent reads, diverging from real hardware.
//
// VHDL zxnext.vhd:5491-5526. Read paths at lines 6125-6155 cite the
// exact bit shape returned.
//
//   - NR$81: store bits 6:3 only (bits 2:0 hardwired off, bit 7 from
//     external ROMCS pin so read-only as 0 in our model).
//   - NR$85: store bits 7 and 3:0 only (bits 6:4 reserved = 0).
//   - NR$89: same shape as $85.
//   - NR$8A: store bits 5:0 only (bits 7:6 reserved = 0).
//   - NR$4C: store bits 3:0 only (bits 7:4 reserved = 0) — tilemap
//     transparent palette index, only 16 palette entries.
func WirePeripheralMasks(d *nextregs.Dispatcher) {
	// NR$1C (clip-index reset/read) is owned by WireClipWindows, which
	// models the real 4-coordinate + 2-bit-index clip windows (NR$18-$1B)
	// rather than the old single-byte approximation.
	d.SetOnWrite(0x2F, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5330-5331 — only bits 1:0 stored (tilemap
		// scrollX high 2 bits). Read at :6017 returns "000000" || bits 1:0.
		disp.Store(0x2F, val&0x03)
	})
	d.SetOnWrite(0x4C, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x4C, val&0x0F)
	})
	// NR$70 (Layer 2 resolution + palette offset), NR$16/$17/$71 (Layer 2
	// scroll) are wired in WireLayer2, which holds the *layer2.Layer2 it
	// drives. WirePeripheralMasks runs after WireLayer2, so re-registering
	// them here would clobber the scroll push.
	d.SetOnWrite(0x81, func(disp *nextregs.Dispatcher, val byte) {
		// Writable: bits 6:3 (ula_override/nmi_debounce/clken/fdc). The
		// expansion-bus speed (bits 1:0) is forced to "00" by this FPGA
		// core (zxnext.vhd:5496 — the nr_wr_dat(1:0) assignment is
		// commented out), and bit 7 is the live i_BUS_ROMCS_n.
		disp.Store(0x81, val&0x78)
	})
	d.SetOnWrite(0x85, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x85, val&0x8F)
	})
	d.SetOnWrite(0x89, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x89, val&0x8F)
	})
	d.SetOnWrite(0x8A, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x8A, val&0x3F)
	})
	d.SetOnWrite(0xCC, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5628-5630 + read :6257 — bit 7 + bits 1:0
		// stored, bits 6:2 reserved.
		disp.Store(0xCC, val&0x83)
	})
	d.SetOnWrite(0xCD, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5632-5633 + read :6260 — all 8 bits stored
		// as nr_cd_dma_int_en_1. No reserved bits.
		disp.Store(0xCD, val)
	})
	d.SetOnWrite(0xCE, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5635-5637 — bits 6:4 + bits 2:0 stored,
		// bits 7 and 3 reserved (read as 0 per :6263:
		// '0' || bits 6:4 || '0' || bits 2:0).
		disp.Store(0xCE, val&0x77)
	})
	d.SetOnWrite(0xD8, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5639-5640 + read at :6265 — only bit 0
		// (FDC iotrap enable). Read returns "0000000" || bit 0.
		disp.Store(0xD8, val&0x01)
	})
	d.SetOnWrite(0xDA, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd read at :6271 — only bits 1:0 of
		// nr_da_iotrap_cause are stored; bits 7:2 reserved.
		disp.Store(0xDA, val&0x03)
	})
	d.SetOnWrite(0x6A, func(disp *nextregs.Dispatcher, val byte) {
		// Per zxnext.vhd:5455-5458 + read at :6101 — bits 5
		// (radastan), 4 (radastan_xor), 3:0 (lores palette offset).
		// Bits 7:6 reserved (read as 0).
		disp.Store(0x6A, val&0x3F)
	})
}

// applyTBBLUEFWBootDefaults sets the NextReg values that real
// TBBLUE.FW boot.bin writes via init_registers() (boot.c:279-407)
// before triggering the boot→NextZXOS soft-reset handoff. Without
// these, NextZXOS reads default-zero peripheral config and
// behaves wrong (50/60Hz, PS/2 mode, DAC, port decode, etc.).
//
// Values are extracted from a working reference session running
// the same SD-card distro at the post-init steady state. They
// represent the "config.ini defaults" boot.bin's
// init_registers() computes from the default settings file.
//
// Per WireReset's soft-edge handler, these NRs are preserved
// across NR\$02 soft reset (real FPGA does the same — see
// zxnext.vhd reset cascade).
func applyTBBLUEFWBootDefaults(disp *nextregs.Dispatcher) {
	// PERIPH1..PERIPH4 (NR$05..$09) — joystick/scandoubler/freq,
	// PS/2 mode, turbo key, BEEP, DivMMC enable, MF enable,
	// stereo, speaker, DAC, Timex, TurboSound, Issue 2/3,
	// scanlines, HDMI sound. Default config.ini values.
	disp.Store(0x05, 0x70)
	disp.Store(0x06, 0xA8)
	disp.Store(0x07, 0x33)
	disp.Store(0x08, 0x5E)
	disp.Store(0x09, 0x00)
	// REG_DECODE_INT0..3 (NR$80..$83) — bitmask of which legacy
	// ports the FPGA hardware-decodes for each machine type.
	// Values for the +3 personality (= NR$03 = $B3 default).
	disp.Store(0x80, 0x00)
	disp.Store(0x81, 0x00)
	disp.Store(0x82, 0xFF)
	disp.Store(0x83, 0xFF)
}

// WireTilemap installs the NextReg $6B / $6C / $6E / $6F OnWrite
// handlers that drive the Spectrum Next tilemap layer:
//
//	$6B bit 7 enable, bit 6 mode (0=40x32 / 1=80x32),
//	 bit 5 strip_flags, bit 3 textmode, bit 1 mode_512,
//	 bit 0 on_top
//	$6C default per-tile attribute (palette offset + mirror /
//	 rotate / priority bits)
//	$6E tile-map base — bit 7 selects bank (5 or 7), bits 5:0 are
//	 a 256-byte offset within the 16 KB bank
//	$6F tile-defs base — same encoding as $6E
//
// Matches FPGA verilog at cores/zxnext/src/zxnext.vhd:5461-5475 and
// the tilemap module itself at video/tilemap.vhd. NextZXOS Browser
// writes $6B=$A1 + $6E=$40 + $6F=$45 during bank-0 init to set up
// the file-picker menu.
func WireTilemap(d *nextregs.Dispatcher, t *tilemap.Tilemap, b *palette.Bank) {
	d.SetOnWrite(0x6B, func(disp *nextregs.Dispatcher, val byte) {
		t.SetEnabled(val&0x80 != 0)
		t.SetControl(val)
		if b != nil {
			// FPGA zxnext.vhd:6826: nr_6b_tm_control(4) selects the
			// tilemap active palette (first vs second).
			b.SetActive(palette.LayerTilemap, (val>>4)&1)
		}
		disp.Store(0x6B, val)
	})
	d.SetOnWrite(0x6C, func(disp *nextregs.Dispatcher, val byte) {
		t.SetDefaultAttr(val)
		disp.Store(0x6C, val)
	})
	d.SetOnWrite(0x6E, func(disp *nextregs.Dispatcher, val byte) {
		t.SetTileMapBase(val)
		// Per zxnext.vhd:5468-5469 + read at :6108 — bit 6 is NOT
		// stored (FPGA drops it). Read returns bit_7 || '0' || bits_5_0.
		// Mask before store so dispatcher OnRead default sees the
		// spec-correct shape.
		disp.Store(0x6E, val&0xBF)
	})
	d.SetOnWrite(0x6F, func(disp *nextregs.Dispatcher, val byte) {
		t.SetTilesBase(val)
		// Same as NR$6E — bit 6 dropped (zxnext.vhd:5472-5473, :6111).
		disp.Store(0x6F, val&0xBF)
	})
}

// WireKeymap installs the NextReg $28 / $29 / $2B OnWrite handlers
// that drive the PS/2 keymap RAM:
//
//	$28 — bit 7 selects joymap vs PS/2 keymap; bit 0 is addr[8].
//	$29 — addr[7:0].
//	$2B — writes a byte to (sel ? joymap : keymap)[addr] and then
//	 auto-increments addr (wraps at 9 bits).
//
// Matches FPGA verilog at cores/zxnext/src/zxnext.vhd:6294-6324.
// NextZXOS writes 22 sequential bytes to $2B during boot to
// populate the PS/2 scancode -> Spectrum-key table. Reads of these
// registers return zero in the FPGA, so no OnRead handler is
// installed — the dispatcher's default (return stored byte, which
// stays zero) matches.
func WireKeymap(d *nextregs.Dispatcher, m *keymap.Map) {
	d.SetOnWrite(0x28, func(disp *nextregs.Dispatcher, val byte) {
		m.WriteAddrHigh(val)
		disp.Store(0x28, val)
	})
	d.SetOnWrite(0x29, func(disp *nextregs.Dispatcher, val byte) {
		m.WriteAddrLow(val)
		disp.Store(0x29, val)
	})
	d.SetOnWrite(0x2B, func(disp *nextregs.Dispatcher, val byte) {
		m.WriteData(val)
		disp.Store(0x2B, val)
	})
}

// WireCopper installs the NextReg 0x60 / 0x61 / 0x62 OnWrite
// handlers that drive the Copper coprocessor: 0x60 is the data
// port (two consecutive writes form one 16-bit instruction),
// 0x61 is the low 8 bits of the write cursor, 0x62 is the high
// 2 bits + 2-bit start mode.
//
// OnRead handlers return the live cursor / mode rather than the
// last value written, so software polling for "where is the
// Copper now?" sees real state.
func WireCopper(d *nextregs.Dispatcher, c *copper.Copper) {
	d.SetOnWrite(0x60, func(disp *nextregs.Dispatcher, val byte) {
		c.WriteData(val)
		disp.Store(0x60, val)
	})
	d.SetOnWrite(0x61, func(disp *nextregs.Dispatcher, val byte) {
		c.SetWritePtrLow(val)
		disp.Store(0x61, val)
	})
	d.SetOnWrite(0x62, func(disp *nextregs.Dispatcher, val byte) {
		c.SetWritePtrHighAndMode(val)
		disp.Store(0x62, val)
	})
	d.SetOnRead(0x61, func(_ *nextregs.Dispatcher) byte {
		return byte(c.Cursor() & 0xFF)
	})
	d.SetOnRead(0x62, func(_ *nextregs.Dispatcher) byte {
		return byte((c.Cursor()>>8)&0x03) | (byte(c.Mode()) << 6)
	})
}

// WireRTC installs NextReg 0x10 / 0x11 storage handlers for the
// real-time-clock SCL / SDA i2c lines. We do NOT implement full i2c
// packet decoding — the bytes are stored verbatim so guest
// software that probes the registers gets stable behaviour, but
// software that bit-bangs a real i2c read of the DS1307 register
// set will not see RTC data through these registers.
//
// Guest software that wants the host clock should instead use the
// esxDOS M_GETDATE API (handler in pkg/next/esxdos), which reads
// rtc.RTC directly. WireRTC is here for completeness so the
// registers themselves don't fall into the "read returns 0xFF"
// unknown-register path.
func WireRTC(d *nextregs.Dispatcher, _ *rtc.RTC) {
	// Storage-only handler for NR$10 (Anti-Brick reset / i2c SCL —
	// version-dependent; we don't model either, just store).
	//
	// NR$11 is NOT i2c — it's the video-timing register per
	// zxnext.vhd:5208-5217. It's wired by WireVideoTiming, NOT here.
	d.SetOnWrite(0x10, func(disp *nextregs.Dispatcher, val byte) { disp.Store(0x10, val) })
}

// WireUART installs NextReg 0xA8 / 0xA9 OnWrite + OnRead handlers
// for the ESP UART. 0xA8 is the data byte (writes append to the
// outgoing FIFO; reads pop the incoming FIFO). 0xA9 is the status
// byte (read-only).
func WireUART(d *nextregs.Dispatcher, u *uart.UART) {
	d.SetOnWrite(0xA8, func(disp *nextregs.Dispatcher, val byte) {
		u.WriteData(val)
		disp.Store(0xA8, val)
	})
	d.SetOnRead(0xA8, func(_ *nextregs.Dispatcher) byte { return u.ReadData() })
	d.SetOnRead(0xA9, func(_ *nextregs.Dispatcher) byte { return u.Status() })
}

// WireCPUSpeed installs the NextReg 0x07 OnWrite handler that
// drives CPU.SetSpeedSelect from guest writes. The CPU's
// SpeedMultiplier method returns 1/2/4/8 based on the stored value
// and ExecuteFrame scales its T-state budget accordingly, so turbo
// modes (NR$07 = 14/28 MHz) take effect immediately on write.
func WireCPUSpeed(d *nextregs.Dispatcher, cpu *z80.CPU) {
	// ZX_GO_FORCE_SPEED=N locks the CPU speed selector to N (0=3.5,
	// 1=7, 2=14, 3=28 MHz), ignoring guest NR$07 writes — a diagnostic
	// for isolating whether a given emulation issue is CPU-speed-related.
	forced := -1
	if fs := os.Getenv("ZX_GO_FORCE_SPEED"); fs != "" {
		if len(fs) == 1 && fs[0] >= '0' && fs[0] <= '3' {
			forced = int(fs[0] - '0')
			cpu.SetSpeedSelect(byte(forced))
		}
	}
	d.SetOnWrite(0x07, func(disp *nextregs.Dispatcher, val byte) {
		sel := val & 0x03
		if forced >= 0 {
			sel = byte(forced)
		}
		cpu.SetSpeedSelect(sel)
		disp.Store(0x07, sel)
	})
	d.SetOnRead(0x07, func(_ *nextregs.Dispatcher) byte {
		// Per zxnext.vhd:5902 (read) — port_253b_dat <=
		//   "00" & cpu_speed & "00" & nr_07_cpu_speed
		// MSB-first: bits 7:6 = 00, bits 5:4 = CURRENT speed
		// (dynamic on real HW; static for us — equals the target),
		// bits 3:2 = 00, bits 1:0 = configured target.
		sel := cpu.SpeedSelect() & 0x03
		return (sel << 4) | sel
	})
}

// WireLineInterrupt installs NR$22 / NR$23 OnWrite handlers so the
// guest can program a per-frame "line interrupt" — a second INT
// pulse asserted when the ULA scan-line counter hits the target line.
// NextZXOS uses this in IM 2 mode (NR$C0 bit 3 = 1) to drive its
// per-frame scheduler step independently of the frame INT.
//
// NR$22 layout (Spectrum Next core register spec):
//
//	bit 7 = line interrupt enable (1 = enable)
//	bit 6 = ULA frame interrupt disable (1 = disable normal frame INT)
//	bit 0 = bit 8 of 9-bit target scan-line (MSB)
//	bits 5..1 = reserved
//
// NR$23 = bits 7..0 of target scan-line (LSB).
//
// 228 T-states per scan line at the 3.5 MHz reference; we scale by
// the CPU's SpeedMultiplier so the offset stays correct under turbo
// modes (NR$07 = 14 / 28 MHz). Must be called after WireCPUSpeed
// (so OnWriteFn(0x07) is non-nil and we can chain onto it).
func WireLineInterrupt(d *nextregs.Dispatcher, cpu *z80.CPU) {
	const tstatesPerScanLine = 228
	recompute := func() {
		nr22 := d.Raw(0x22)
		nr23 := d.Raw(0x23)
		// Per nextreg.txt 0x22 + zxnext.vhd:5297 the bit positions are:
		//   bit 2 = frame INT disable
		//   bit 1 = line INT enable
		//   bit 0 = MSB of 9-bit line target value
		//   bit 7 = (READ-only) "ULA is asserting INT" — not a write field
		cpu.FrameIntDisabled = nr22&0x04 != 0
		if nr22&0x02 == 0 {
			cpu.LineIntOffsetTstates = 0
			return
		}
		target := uint16(nr23) | (uint16(nr22&0x01) << 8)
		// Raster position: the target line interrupt fires at
		// vpos = target-1+min_vactive, i.e. the target line is relative
		// to the 256x192 active area (min_vactive=64 = our top border),
		// not the frame top. frameStart is absolute line 0.
		const minVactive = 64
		var lineAbs uint64
		if target != 0 {
			lineAbs = uint64(target-1) + minVactive
		}
		cpu.LineIntOffsetTstates = lineAbs * tstatesPerScanLine * uint64(cpu.SpeedMultiplier())
	}
	d.SetOnWrite(0x22, func(disp *nextregs.Dispatcher, v byte) {
		// Per zxnext.vhd:5905 the NR$22 read shape is
		//   bit 7 = (not pulse_int_n) dynamic — we don't model
		//   bits 6:3 = "0000" reserved
		//   bit 2 = frame INT disable
		//   bit 1 = line INT enable
		//   bit 0 = line interrupt MSB
		// Mask bits 7:3 off so readback matches.
		disp.Store(0x22, v&0x07)
		recompute()
	})
	d.SetOnWrite(0x23, func(disp *nextregs.Dispatcher, v byte) {
		disp.Store(0x23, v)
		recompute()
	})
	if prev := d.OnWriteFn(0x07); prev != nil {
		d.SetOnWrite(0x07, func(disp *nextregs.Dispatcher, v byte) {
			prev(disp, v)
			recompute()
		})
	}
}

// WireMMU installs OnWrite / OnRead handlers on the NextReg dispatcher
// for registers 0x50–0x57 so that guest code writing those registers
// (via port 0x253B or the Z80N NEXTREG opcodes) routes into the
// pkg/memory 8K MMU shadow. Call once during ModelNext construction
// after creating both the dispatcher and the memory bus.
//
// The OnWrite handler also calls Store so a subsequent read via 0x253B
// returns the latched value (matching FPGA behaviour where the MMU
// register file mirrors the slot table).
//
// WireROMBank installs the NextReg 0x8E (Standard ROM Mapping)
// OnWrite handler. NextZXOS uses this to swap among the 4 × 16 KB
// ROM banks of the distro at 0x0000-0x3FFF. The Next core's
// "Memory Map Mode 1" interpretation:
//
//	bit 7 = paging-locked (we ignore — paging stays enabled)
//	bit 4 = "use this register's ROM bank" enable
//	bits 1-0 = ROM bank index 0..3 to map at 0x0000-0x3FFF
//
// Real hardware also blends in port 0x7FFD bit 4 and port 0x1FFD
// bit 2 for backwards compatibility; this implementation lets the
// NextReg 0x8E value win (it's what NextZXOS uses for its own
// bank swaps during boot — the legacy ports stay relevant for
// 48K / 128K BASIC-mode software running under NextZXOS but those
// modes are reached via NextReg first).
//
// OnRead reconstructs the value LIVE from the paging ports
// (port_DFFD / port_7FFD / port_1FFD) per nextreg.txt:870-885 — the
// read is NOT the last byte written. Returning the stored byte would
// give a stale value whenever paging was last driven through the
// legacy ports instead of NR$8E itself. See memory.ROMMappingNR8E.
func WireROMBank(d *nextregs.Dispatcher, mem *memory.Memory) {
	d.SetOnWrite(0x8E, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0x8E, val)
		mem.SetROMBankExtended(val)
	})
	d.SetOnRead(0x8E, func(disp *nextregs.Dispatcher) byte {
		return mem.ROMMappingNR8E()
	})
}

// WireAltROM installs the NextReg $8C (Alternate ROM) OnWrite
// handler. Per the SpecNext wiki and TBBlue FPGA verilog,
// $8C controls a 32 KB alternative ROM facility — two 16 KB banks
// (Alt-ROM 0 = 128K-style, Alt-ROM 1 = 48K-style) that can either
// replace the normal ROMs at $0000-$3FFF (read-redirect) or
// receive writes to that area (write-redirect, a "RAM disk
// masquerading as ROM" pattern used by NextZXOS for runtime
// patches).
//
// Bit layout (immediate effect on write; hard-reset = 0):
//
//	bit 7 = enable alt-rom
//	bit 6 = 1 → alt-rom visible during writes only
//	 0 → alt-rom replaces ROM during reads
//	bit 5 = lock ROM1 (use Alt-ROM 1 / 48K-style)
//	bit 4 = lock ROM0 (use Alt-ROM 0 / 128K-style)
//	bits 3-0 = soft-reset latched values (copied into 7-4 on
//	 NextReg $02 bit 0 soft-reset; not modelled here)
//
// NextZXOS bank-2 init writes $C0 = enable + write-only — this
// lets it apply patches to its own ROM without disrupting the
// reads that the rest of the boot expects.
func WireAltROM(d *nextregs.Dispatcher, mem *memory.Memory) {
	d.SetOnWrite(0x8C, func(disp *nextregs.Dispatcher, val byte) {
		slog.Debug("nr8c-write", "val", fmt.Sprintf("$%02X", val))
		mem.SetAltROMReg(val)
		disp.Store(0x8C, val)
	})
	d.SetOnRead(0x8C, func(disp *nextregs.Dispatcher) byte {
		return mem.AltROMReg()
	})
}

// WireReset wires NextReg $02 (Reset). Per the SpecNext wiki at
// https://wiki.specnext.dev/Next_Reset_Register the register acts
// as a "sticky" reset-reason indicator on read and a reset-trigger
// on write:
//
//	WRITE bit 0 = 1 -> generate soft reset
//	WRITE bit 1 = 1 -> generate hard reset (precedence over soft)
//	READ bit 0 = 1 -> the last reset was a soft reset
//	READ bit 1 = 1 -> the last reset was a hard reset (only one
//	 of bits 1:0 is set at any time)
//
// NEXTBASIC's bank-3 trampoline at $3BE8 implements the "request
// soft reset" pattern:
//
//	LD A,$02; OUT (C),A ; select NextReg $02
//	INC B; IN A,(C) ; read it back
//	AND $80; OR $01 ; preserve bit 7 status, set bit 0
//	OUT (C),A ; trigger soft reset
//	<padding $FF bytes> ; should never execute on real HW
//
// On real silicon the OUT immediately asserts /RESET; the CPU
// re-enters from $0000 with the sticky reset-reason bits set so
// configuration-aware code can distinguish soft from cold reset.
//
// No code in the NextZXOS boot ROM reads NextReg $02 to alter the
// post-reset path — the only read of $02 anywhere in the distro is
// the one at $3BE8 itself (which preserves bit 7 across the write).
// So the soft-reset is a clean re-entry of the same boot path with
// NextReg state preserved — nothing is "remembered" to alter the
// post-reset code path.
//
// Our implementation: cpu.Reset() puts the Z80 in proper post-
// reset state (PC=0, IFF1/2=0, IM=0, registers=$FF), we switch
// the ROM page back to bank 0 so the post-reset fetch lands in
// the NextZXOS boot ROM, and the sticky bit is latched in the
// stored register value so read-back returns it (in case future
// software relies on it). Other NextReg state, RAM, and the
// divMMC pager all survive the reset (which is the whole point
// of using NextReg $02 instead of a power cycle).
// divmmcSPI, if non-nil, has its SPI chip-select deasserted on every
// reset (hard or soft) — modelling the FPGA's port_e7_reg reset. Pass
// nil on classic-model / SD-less / test paths.
func WireReset(d *nextregs.Dispatcher, mem *memory.Memory, cpu *z80.CPU, divmmcSPI SPIResetter) {
	// DIAGNOSTIC (not a fix): ZX_GO_SUPPRESS_RESET_PC="$XXXX" suppresses the
	// actual reset effect for soft-resets whose trigger PC matches, while
	// still latching NR$02. Used to test whether the recurring NextZXOS
	// soft-reset at $3BF5 is spurious (boot proceeds → it was lost sticky
	// state) or load-bearing (boot crashes → the reset is needed).
	var suppressPC uint16
	suppressOn := false
	if s := os.Getenv("ZX_GO_SUPPRESS_RESET_PC"); s != "" {
		var v uint64
		if _, err := fmt.Sscanf(s, "%x", &v); err == nil {
			suppressPC = uint16(v)
			suppressOn = true
		}
	}
	// nr_02_reset_type is a 3-bit SHIFT-HISTORY register, not a simple
	// sticky pair (zxnext.vhd:1306 init "100"; :1736 on soft reset:
	// reset_type <= '0' & old(2) & (old(1) or old(0))). Reads expose
	// only bits 1:0 (vhd:5891), so software observes the sequence
	// $00 (power-on) → $02 (after 1st soft = "came from hard") →
	// $01 (after 2nd+ soft). NextZXOS's boot uses this to distinguish
	// its first staging pass (read $02 → install the menu engine and
	// issue the $6D31 staging soft-reset) from the post-staging pass
	// (read $01 → continue into the engine-backed menu). Bit 7 of
	// every write latches nr_02_bus_reset (vhd:5119) and reads back
	// in bit 7 (the expansion-bus reset line).
	//
	// Seed: with the FPGA bootrom armed, the bootrom itself issues
	// the first soft reset (start at power-on "100"); the direct-boot
	// path skips the bootrom, so seed "010" = that one soft applied.
	var resetType byte = 0b010
	if mem.FPGABootROMActive() {
		resetType = 0b100
	}
	// nr02NMISource latches the NMI-source read-back bits (bit 4 = i/o
	// trap, bit 3 = multiface, bit 2 = divmmc; nextreg.txt:41-43). A
	// write that sets bit 3/2 fires the NMI and latches the source bit;
	// a write with the bit clear clears it ("write zero to clear").
	var nr02NMISource byte
	d.Store(0x02, resetType&0x03)
	d.SetOnWrite(0x02, func(disp *nextregs.Dispatcher, val byte) {
		triggerPC := cpu.PC // capture before any reset zeroes it
		// Capture the bytes the CPU sees at the trigger PC BEFORE the reset
		// remaps memory — reveals whether a $3BF5-style trigger is a
		// divMMC-RAM NOP-slide (non-bank0 paging → zeros decoding as
		// NEXTREG $02,$01) or a legitimate NEXTREG $02 in real code.
		var trigBytes [0x20]byte
		for i := range trigBytes {
			trigBytes[i] = mem.Read(triggerPC - 0x1A + uint16(i))
		}
		// divMMC paging context captured PRE-reset (the reset re-arms EPs /
		// deasserts CS, which can perturb post-reset reads).
		divInfo := "n/a"
		if q, ok := divmmcSPI.(interface {
			LastE3() byte
			IsPagedIn() bool
		}); ok {
			e3 := q.LastE3()
			divInfo = fmt.Sprintf("paged=%v E3=$%02X bank=%d conmem=%v", q.IsPagedIn(), e3, e3&0x0F, e3&0x80 != 0)
		}
		// Fire on every write that has the trigger bit set — the FPGA
		// does NOT use a bit-0→1-edge gate. The FPGA bootrom itself
		// relies on this: it reads NR$02 back as $01 on cold boot
		// (consistent with the shift-history after two soft resets)
		// and then writes $01 again to re-trigger a soft reset. Every
		// OUT is a separate trigger pulse on the bus, regardless of
		// the previously-stored value.
		softFire := val&0x01 != 0
		hardFire := val&0x02 != 0
		// NR$02 bits 3/2 generate the Multiface / divMMC NMI
		// (nextreg.txt:51-52): writing 1 fires the NMI and latches the
		// read-back source bit; writing 0 clears the source bit without
		// re-firing. An NMI is non-maskable (independent of IFF) and
		// vectors to $0066. NextZXOS's 128K-BASIC launch fires the
		// Multiface NMI here (NR$02=$08 at bank-2 $0AB6: OUT ($253B),A) to
		// enter its editor-snapshot loader: the FPGA pages the Multiface in
		// over $0000-$3FFF so the $0066 handler (enNextMF.rom: JP $006D; LD
		// ($3FB1),SP; LD SP,$3FC4; …) runs. Without paging the MF in, $0066
		// would read the normal ROM's stub (PUSH AF; POP AF; RETN) instead
		// and the launch would derail. The Multiface pages back out on
		// RETN (see CPU.RETN). nextreg.txt:56: the MF NMI is ignored if an
		// NMI master is already active — so only arm/fire when the MF
		// isn't already paged in.
		if val&0x08 != 0 && !mem.MultifaceActive() { // bit 3 = Generate multiface NMI
			nr02NMISource |= 0x08
			mem.SetMultifaceActive(true) // page MF ROM in so the NMI vectors into it
			cpu.PendingNMI.Store(true)
		} else if val&0x08 == 0 {
			nr02NMISource &^= 0x08
		}
		if val&0x04 != 0 { // bit 2 = Generate divmmc NMI
			nr02NMISource |= 0x04
			cpu.PendingNMI.Store(true)
			// Latch button_nmi so the $0066 NMI-vector automap engages for
			// the divMMC NMI (divmmc.vhd:110-120). Only the divMMC NMI does
			// this — a Multiface NMI (bit 3) owns its own $0066 vector.
			if q, ok := divmmcSPI.(interface{ AssertNMIButton() }); ok {
				q.AssertNMIButton()
			}
		} else {
			nr02NMISource &^= 0x04
		}
		// reset_type shift-history per zxnext.vhd:1736. A hard reset
		// re-runs the fabric's power-on init → back to "100".
		prevRT := resetType
		if hardFire {
			resetType = 0b100
		} else if softFire {
			b2 := resetType >> 2 & 1
			b10 := byte(0)
			if resetType&0x03 != 0 {
				b10 = 1
			}
			resetType = b2<<1 | b10
		}
		if os.Getenv("ZX_GO_NR02_TRACE") != "" && (softFire || hardFire) {
			fmt.Printf("NR02 write val=%02X softFire=%v hardFire=%v resetType %03b->%03b (reads %02X) pc=$%04X\n",
				val, softFire, hardFire, prevRT, resetType, resetType&0x03, triggerPC)
		}
		// Read composition (vhd:5891): bus_reset(b7, latched from this
		// write's bit 7 per vhd:5119) + iotrap(b4, not modelled, reads 0)
		// + mf_nmi(b3)/divmmc_nmi(b2) from nr02NMISource + reset_type(1:0).
		disp.Store(0x02, val&0x80|nr02NMISource|resetType&0x03)
		if suppressOn && softFire && !hardFire && triggerPC == suppressPC {
			// Diagnostic: latch NR$02 but skip the actual reset, to see if the
			// boot proceeds without this soft-reset.
			return
		}
		if (hardFire || softFire) && divmmcSPI != nil {
			// FPGA resets port_e7_reg to all-ones on any reset
			// (zxnext.vhd:3308) — deassert the SD SPI chip-select so any
			// in-flight transaction (e.g. a CMD18 stream interrupted by
			// NextZXOS's post-mount soft reset) is aborted and the
			// post-reset SD re-init starts clean.
			divmmcSPI.ResetSPI()
		}
		if hardFire || softFire {
			// NR$8C Alt-ROM staged-nibble promote (zxnext.vhd:2255): the
			// core `reset` signal — asserted for BOTH reset types
			// (zxnext_top_issue5.vhd:880 reset <= reset_hard or
			// reset_soft) — loads the active high nibble from the staged
			// low nibble; the staged nibble itself survives. NextZXOS
			// stages its after-reset Alt-ROM configuration here before
			// its one legitimate boot soft-reset ($6D31); without the
			// promote the active nibble goes stale and the altrom arm of
			// the divMMC rom3-automap gate (zxnext.vhd:3138) never opens
			// at menu time.
			promoted := mem.AltROMReg()&0x0F | mem.AltROMReg()<<4
			slog.Debug("nr8c-promote", "pre", fmt.Sprintf("$%02X", mem.AltROMReg()),
				"post", fmt.Sprintf("$%02X", promoted))
			mem.SetAltROMReg(promoted)
			disp.Store(0x8C, promoted)
		}
		if hardFire {
			// Hard reset = power-cycle equivalent. Wipe all CPU
			// registers as if /RESET had just been applied with
			// undefined-register semantics, then re-map ROM bank 0
			// so the post-reset fetch lands in the NextZXOS boot ROM.
			cpu.Reset()
			mem.SetROMBank(0)
			// zxnext.vhd:5108-5110: the NR reset block re-asserts
			// bootrom_en when nr_03_config_mode is active (reset =
			// hard OR soft). See the softFire branch for the
			// load-bearing case (config-menu machine selection).
			if mem.ConfigModeActive() {
				mem.RearmFPGABootROM()
			}
		} else if softFire {
			// Soft reset = strict Z80 /RESET semantics on the CPU
			// (PC, I, R, IFF1/2, IM, HALT cleared; SP and IX/IY
			// survive — needed by NEXTBASIC's bank-3 $3BE8 trampoline
			// which uses `(IX+$1F)` immediately after the reset),
			// plus the classic 128K paging latches (port 7FFD/1FFD)
			// returning to ROM 0 / RAM 0.
			//
			// NR-file reset policy across a soft reset — derived from
			// the FPGA, NOT guessed. The board asserts the core reset
			// on BOTH hard and soft reset:
			//   zxnext_top_issue5.vhd:880  reset <= reset_hard or reset_soft
			//   zxnext.vhd:1730            reset <= i_RESET
			// so the big `if reset='1' then …default` NR block runs on a
			// soft reset too. BUT two things it does NOT do:
			//   1. nr_03 (machine type / config_mode / timing) is NOT in
			//      that block — it is preserved across reset. This is
			//      load-bearing: the configured personality must survive,
			//      else the boot re-enters config mode, re-applies the
			//      personality, and soft-resets again forever.
			//   2. The few NR$82-$89 port-enable regs are reset-type
			//      conditional (nr_85/nr_89_*_reset_type).
			// We don't yet model the full per-register reset, so we keep
			// the rest of the NR file as-is (preserving nr_03 for free)
			// and re-arm the divMMC entry points, which the FPGA resets
			// unconditionally (zxnext.vhd:5087-5090, NR$B8/$B9/$BA/$BB ←
			// $83/$01/$00/$CD).
			cpu.SoftReset()
			mem.ResetPaging()
			mem.SetROMBank(0)
			// zxnext.vhd:5108-5110: `if nr_03_config_mode = '1' then
			// bootrom_en <= '1'` inside the shared reset block — a
			// soft reset issued while config mode is active drops the
			// machine back into the FPGA bootrom. THE firmware
			// config-menu machine selection depends on this: the
			// editor keeps config mode on and ends with NR$02 soft
			// reset; the bootrom then reloads TBBLUE.FW which boots
			// the personality the user picked. Without the re-arm the
			// CPU restarted at $0000 in the stale map and span in
			// firmware leftovers (the config-menu freeze).
			if mem.ConfigModeActive() {
				mem.RearmFPGABootROM()
			}
			// WriteReg fires the OnWrite handlers so the divMMC pager's
			// mirrored entry-point state is updated too, not just the
			// dispatcher's stored byte.
			disp.WriteReg(0xB8, 0x83)
			disp.WriteReg(0xB9, 0x01)
			disp.WriteReg(0xBA, 0x00)
			disp.WriteReg(0xBB, 0xCD)
			p7, p1, _ := mem.GetPortState()
			rb := int((p7>>4)&1) | int((p1>>1)&2)
			// Stack just below SP (post-reset SP is the OS stack at reset time)
			// — return addresses reveal the caller chain into the reset routine.
			sp := cpu.SP
			var stk [24]byte
			for i := range stk {
				stk[i] = mem.Read(sp + uint16(i))
			}
			slog.Debug("soft-reset complete (NR$03 preserved, divMMC EPs re-armed)",
				"rom_bank", rb, "port7FFD", p7, "port1FFD", p1,
				"trigger_pc", fmt.Sprintf("$%04X", triggerPC), "nr02_written", fmt.Sprintf("$%02X", val),
				"trigger_bytes", fmt.Sprintf("% 02X", trigBytes[:]), "divMMC", divInfo,
				"sp", fmt.Sprintf("$%04X", sp), "stack", fmt.Sprintf("% 02X", stk[:]))
		}
	})
}

// Other subsystems (CPU speed at NextReg 0x07, palette at 0x40–0x44,
// etc.) have their own Wire* functions below; this one is scoped to
// just the MMU registers.
func WireMMU(d *nextregs.Dispatcher, mem *memory.Memory) {
	for slot := byte(0); slot < 8; slot++ {
		reg := 0x50 + slot
		// Capture slot in the closure; do NOT reference the loop
		// variable directly.
		s := slot
		d.SetOnWrite(reg, func(disp *nextregs.Dispatcher, val byte) {
			mem.SetMMU(s, val)
			disp.Store(reg, val)
		})
		d.SetOnRead(reg, func(_ *nextregs.Dispatcher) byte {
			return mem.GetMMU(s)
		})
	}
}
