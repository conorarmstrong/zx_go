package main

import (
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/palette"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/trace"
)

// installTraceHooks wires the trace emitter into every subsystem
// enabled by f.traceChannels. Returns the emitter so the caller
// can flush at shutdown, plus a cleanup function the caller defers.
//
// Wiring is best-effort: subsystems that aren't present (e.g. the
// NextReg dispatcher on a classic-model emulator) are silently
// skipped.
func installTraceHooks(emu *emulator, f *cliFlags) (trace.Emitter, func()) {
	if f.traceChannels.Empty() {
		return trace.NoopEmitter{}, func() {}
	}

	var (
		out     = os.Stderr
		closeFn = func() {}
	)
	if f.traceOutput != "" {
		fp, err := os.Create(f.traceOutput)
		if err != nil {
			slog.Error("trace-output open failed", "path", f.traceOutput, "err", err)
			os.Exit(1)
		}
		out = fp
		closeFn = func() { _ = fp.Close() }
		slog.Info("trace output", "path", f.traceOutput)
	}
	em := trace.NewJSONEmitter(out, func(err error) {
		slog.Warn("trace write failed; further events dropped", "err", err)
	})

	// PC channel: install a CPU pre-fetch hook, filtered by range.
	if f.traceChannels.PC {
		cpu := emu.cpu
		mem := emu.mem
		// Pre-fetch hook fires on every M1. Filter cheaply.
		filterStart, filterEnd := f.tracePCStart, f.tracePCEnd
		filterOn := f.tracePCRange
		cpu.AddPreFetchHook("trace-pc", func(pc uint16) {
			if filterOn && (pc < filterStart || pc > filterEnd) {
				return
			}
			port7FFD, port1FFD, _ := mem.GetPortState()
			bank := int((port7FFD>>4)&1) | int((port1FFD>>1)&2)
			em.Emit(trace.PCEvent{
				PC:    pc,
				Bank:  bank,
				Insn:  cpu.InstructionCount(),
				Frame: int(atomic.LoadInt32(&emu.frameCounter)),
			})
		})
	}

	// NextReg channel: install a tracer on the dispatcher if one
	// is wired. The dispatcher's tracer fires after every read/
	// write with the value and direction.
	if f.traceChannels.NextReg && emu.nextRegs != nil {
		cpu := emu.cpu
		emu.nextRegs.SetTracer(func(reg, val byte, write bool) {
			em.Emit(trace.NextRegEvent{
				PC:    cpu.PC,
				Reg:   reg,
				Value: val,
				Write: write,
			})
		})
	}

	// Ports channel: install ULA port tracer. ULA WritePort and
	// ReadPort fire the callback for every access that completes
	// through the ULA dispatch. Filter to the "interesting" ports
	// so trace volume stays manageable.
	if f.traceChannels.Ports {
		cpu := emu.cpu
		emu.ula.SetPortTracer(func(addr uint16, val byte, write, _ bool) {
			if !isInterestingPort(addr) {
				return
			}
			em.Emit(trace.PortEvent{
				PC:    cpu.PC,
				Port:  addr,
				Value: val,
				Write: write,
			})
		})
	}

	// Bank-switch channel: memory layer fires the tracer on every
	// SetROMBank change and every MMU slot reassignment.
	if f.traceChannels.BankSwitch {
		cpu := emu.cpu
		emu.mem.SetBankTracer(func(slot byte, bank int, source string) {
			em.Emit(trace.BankSwitchEvent{
				PC:      cpu.PC,
				Slot:    slot,
				NewBank: bank,
				Source:  source,
			})
		})
	}

	return em, closeFn
}

// isInterestingPort returns true for the I/O ports we care about
// for emulator-state debugging. Filters out the high-volume
// "every memory access touches AY data" noise that floods the
// stream otherwise.
func isInterestingPort(addr uint16) bool {
	low := addr & 0xFF
	switch low {
	case 0xE3, 0xE7, 0xEB: // divMMC control / SD-SPI
		return true
	case 0xFE: // ULA: border, mic, speaker, keyboard rows
		return true
	}
	switch addr {
	case 0x7FFD, 0x1FFD: // 128K / +3 paging
		return true
	case 0x243B, 0x253B: // NextReg select / data
		return true
	case 0xBFFD, 0xFFFD: // AY register / data
		return true
	}
	return false
}

// dumpState emits a one-shot snapshot of every interesting piece
// of CPU and Next state. Called from main() when --dump-state is
// set. Output goes to stdout so it's easy to redirect for diffing.
func dumpState(emu *emulator) {
	cpu := emu.cpu
	mem := emu.mem
	port7FFD, port1FFD, special := mem.GetPortState()
	romBank := int((port7FFD>>4)&1) | int((port1FFD>>1)&2)
	fmt.Println("# zx_go state snapshot")
	fmt.Printf("\nCPU:\n  PC=%04X SP=%04X IFF1=%v IFF2=%v IM=%d Halted=%v\n",
		cpu.PC, cpu.SP, cpu.IFF1, cpu.IFF2, cpu.IM, cpu.Halted)
	fmt.Printf("  AF=%02X%02X BC=%02X%02X DE=%02X%02X HL=%02X%02X\n",
		cpu.A, cpu.F, cpu.B, cpu.C, cpu.D, cpu.E, cpu.H, cpu.L)
	fmt.Printf("  IX=%04X IY=%04X I=%02X R=%02X\n",
		cpu.IX, cpu.IY, cpu.I, cpu.R)
	fmt.Printf("  Instructions executed: %d\n", cpu.InstructionCount())

	fmt.Printf("\nPaging:\n  7FFD=%02X 1FFD=%02X specialPaging=%v ROM bank=%d ScreenPage=%d\n",
		port7FFD, port1FFD, special, romBank, mem.ScreenPage)
	if mem.GetCurrentModel() == roms.ModelNext {
		fmt.Printf("  MMU slots (8K):")
		for i := byte(0); i < 8; i++ {
			fmt.Printf(" %02X", mem.GetMMU(i))
		}
		fmt.Println()
	}

	if emu.nextRegs != nil {
		fmt.Printf("\nNextRegs (non-zero):\n")
		for r := 0; r < 256; r++ {
			v := emu.nextRegs.ReadReg(byte(r))
			if v != 0 {
				fmt.Printf("  $%02X = $%02X\n", r, v)
			}
		}
	}

	if pager, ok := emu.ula.NextDivMMC().(*divmmc.Pager); ok && pager != nil {
		fmt.Printf("\ndivMMC: paged=%v mapram=%v automap=%v lastE3=$%02X\n",
			pager.IsPagedIn(), pager.MAPRAM(), pager.AutomapEnabled(), pager.LastE3())
		// Handler bytes in bank 0 (= esxDOS ROM copy seeded at FPGA config
		// time). If these are $00 the seed was wiped → the $3BF5 slide returns.
		if b0 := pager.RAMBank(0); b0 != nil {
			fmt.Printf("  bank0[$13B4(=$33B4)]=$%02X [$1CFD(=$3CFD)]=$%02X [$1BF5(=$3BF5)]=$%02X (esxDOS ROM: $D5 $0E $3C)\n",
				b0[0x13B4], b0[0x1CFD], b0[0x1BF5])
		}
	}

	// Tilemap palette dump: 16 entries are enough to cover the
	// indices a Sprint-N strip_flags tilemap can address. If the
	// testcard / Browser palette isn't being initialised properly,
	// the non-zero entries here tell us which boot stage put what
	// where. Format: hex 9-bit packed RRRGGGBBB, with the decoded
	// 24-bit colour beside it.
	if emu.nextPalette != nil {
		dumpPaletteRow := func(label string, layer palette.Layer) {
			pal := emu.nextPalette.PaletteForLayer(layer)
			if pal == nil {
				return
			}
			fmt.Printf("\n%s palette (first 16): ", label)
			for i := 0; i < 16; i++ {
				fmt.Printf(" %03X", pal.Get(byte(i)))
			}
			fmt.Println()
		}
		dumpPaletteRow("Tilemap", palette.LayerTilemap)
		dumpPaletteRow("ULA    ", palette.LayerULA)
		dumpPaletteRow("Layer2 ", palette.LayerLayer2)
	}

	if emu.nextTilemap != nil {
		fmt.Printf("\nTilemap: enabled=%v\n", emu.nextTilemap.Enabled())
	}

	// Screen summary: counts of non-zero bitmap bytes + attr histogram
	bitmapNZ := 0
	for a := uint16(0x4000); a < 0x5800; a++ {
		if mem.Read(a) != 0 {
			bitmapNZ++
		}
	}
	attrHist := map[byte]int{}
	for a := uint16(0x5800); a < 0x5B00; a++ {
		attrHist[mem.Read(a)]++
	}
	fmt.Printf("\nScreen:\n  bitmap non-zero: %d / 6144\n  attr histogram: %v\n",
		bitmapNZ, attrHist)

	// Spectrum sysvars: ERR_NR ($5C3A), FLAGS ($5C3B), CH-ADD ($5C5D),
	// FLAGS2 ext ($D5D4), CURRENT-LINE ($5C53). Hugely useful for
	// figuring out where BASIC's parser is.
	chadd := uint16(mem.Read(0x5C5D)) | uint16(mem.Read(0x5C5E))<<8
	fmt.Printf("\nSysvars:\n  ERR_NR ($5C3A)=%02X  FLAGS ($5C3B)=%02X\n",
		mem.Read(0x5C3A), mem.Read(0x5C3B))
	fmt.Printf("  CH-ADD ($5C5D)=%04X  FLAGS2-ext ($D5D4)=%02X\n",
		chadd, mem.Read(0xD5D4))
	fmt.Printf("  CURRENT-LINE ($5C53)=%04X  E-LINE ($5C59)=%04X\n",
		uint16(mem.Read(0x5C53))|uint16(mem.Read(0x5C54))<<8,
		uint16(mem.Read(0x5C59))|uint16(mem.Read(0x5C5A))<<8)
	if chadd != 0 {
		fmt.Printf("  bytes around CH-ADD: ")
		for i := -4; i < 16; i++ {
			a := uint16(int(chadd) + i)
			mark := ""
			if i == 0 {
				mark = "["
			}
			fmt.Printf("%s%02X", mark, mem.Read(a))
			if i == 0 {
				fmt.Printf("]")
			} else {
				fmt.Printf(" ")
			}
		}
		fmt.Println()
	}
}

// dumpMemRanges prints each requested memory range as a 16-byte
// hex grid via the live Memory.Read path (so MMU paging, alt-ROM
// redirects, divMMC overlays, etc. all resolve the same way the
// guest sees them). Address arithmetic uses uint32 internally so
// requesting a range that wraps past $FFFF is handled gracefully
// (it just stops at $FFFF).
func dumpMemRanges(emu *emulator, ranges []dumpMemRange) {
	if len(ranges) == 0 {
		return
	}
	mem := emu.mem
	for _, r := range ranges {
		if r.rom >= 0 {
			page := mem.GetROMPage(r.rom)
			if page == nil {
				fmt.Printf("\nROM %d $%04X..: not allocated\n", r.rom, r.addr)
				continue
			}
			end := uint32(r.addr) + uint32(r.length)
			if end > uint32(len(page)) {
				end = uint32(len(page))
			}
			fmt.Printf("\nROM %d $%04X..$%04X:\n", r.rom, r.addr, end-1)
			for base := uint32(r.addr); base < end; base += 16 {
				fmt.Printf("  $%04X:", base)
				for off := uint32(0); off < 16 && base+off < end; off++ {
					fmt.Printf(" %02X", page[base+off])
				}
				fmt.Println()
			}
			continue
		}
		if r.bank >= 0 {
			page := mem.GetPage(r.bank)
			if page == nil {
				fmt.Printf("\nBank %d $%04X..: not allocated\n", r.bank, r.addr)
				continue
			}
			end := uint32(r.addr) + uint32(r.length)
			if end > uint32(len(page)) {
				end = uint32(len(page))
			}
			fmt.Printf("\nBank %d $%04X..$%04X:\n", r.bank, r.addr, end-1)
			for base := uint32(r.addr); base < end; base += 16 {
				fmt.Printf("  $%04X:", base)
				for off := uint32(0); off < 16 && base+off < end; off++ {
					fmt.Printf(" %02X", page[base+off])
				}
				fmt.Println()
			}
			continue
		}
		end := uint32(r.addr) + uint32(r.length)
		if end > 0x10000 {
			end = 0x10000
		}
		fmt.Printf("\nMemory $%04X..$%04X:\n", r.addr, end-1)
		for base := uint32(r.addr); base < end; base += 16 {
			fmt.Printf("  $%04X:", base)
			for off := uint32(0); off < 16 && base+off < end; off++ {
				fmt.Printf(" %02X", mem.Read(uint16(base+off)))
			}
			fmt.Println()
		}
	}
}
