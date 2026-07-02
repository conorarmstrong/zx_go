package main

import (
	"fmt"
	"log/slog"
	"strings"
)

// provenance_cmd.go wires the provenance tracker (provenance.go) into
// the debugger: the `provenance` command (arm / disarm / query a
// logical address) and the `why-pc` command (explain how PC got
// here by walking the return word off the stack to whoever pushed it).
//
// Arming chains a WriteObserver on top of any existing one (mirroring
// trace-writes) so other diagnostics keep working. The hot path is a
// single atomic load when disabled.

// ensureProvenanceHook installs the chained WriteObserver exactly
// once. Every CPU write stamps the last-writer table with the writing
// instruction's PC + counter + ROM-bank context.
func (d *remoteDebugger) ensureProvenanceHook() {
	if d.provenance.installed {
		return
	}
	d.provenance.installed = true
	d.provenance.prior = d.emu.mem.WriteObserver
	d.emu.mem.WriteObserver = func(addr uint16, val byte, pc uint16) {
		if d.provenance.prior != nil {
			d.provenance.prior(addr, val, pc)
		}
		if !d.provenance.enabled.Load() {
			return
		}
		cpu := d.emu.cpu
		d.provenance.record(addr, val, cpu.InstructionCount(), cpu.PC, d.currentROMBank())
	}

	// Phase 2: chain the RAM write hook for physical-pool keying. It
	// fires only on successful RAM writes, with the resolved 16K bank
	// + intra-bank offset — paging-independent, so it survives the
	// slot remapping that makes the logical table stale.
	d.provenance.phys = make(map[uint32]provRec)
	d.provenance.physInstalled = true
	d.provenance.physPrior = d.emu.mem.GetRAMWriteHook()
	d.emu.mem.SetRAMWriteHook(func(bank int, off uint16, val byte) {
		if d.provenance.physPrior != nil {
			d.provenance.physPrior(bank, off, val)
		}
		if !d.provenance.enabled.Load() {
			return
		}
		cpu := d.emu.cpu
		d.provenance.recordPhys(bank, off, val, cpu.InstructionCount(), cpu.PC)
	})
}

// cmdProvenance arms/disarms the tracer or queries one address.
//
//	provenance                show armed state
//	provenance on             arm (start recording last-writers)
//	provenance off            disarm
//	provenance $ADDR          last writer of logical $ADDR
//	provenance $ADDR word     last writers of $ADDR and $ADDR+1
func (d *remoteDebugger) cmdProvenance(args []string) string {
	if len(args) == 0 {
		if d.provenance.enabled.Load() {
			return "OK provenance on"
		}
		return "OK provenance off"
	}
	switch strings.ToLower(args[0]) {
	case "on", "arm":
		d.ensureProvenanceHook()
		d.provenance.enabled.Store(true)
		return "OK provenance on (recording last-writers)"
	case "off", "clear":
		d.provenance.enabled.Store(false)
		return "OK provenance off"
	}

	addr, err := parseHex(args[0])
	if err != nil {
		return "ERR bad address: " + err.Error()
	}
	word := len(args) > 1 && strings.EqualFold(args[1], "word")
	var b strings.Builder
	fmt.Fprintf(&b, "OK provenance $%04X: %s", uint16(addr), d.formatProv(uint16(addr)))
	if word {
		fmt.Fprintf(&b, "\n   $%04X: %s", uint16(addr)+1, d.formatProv(uint16(addr)+1))
	}
	return b.String()
}

// formatProv renders one last-writer record for the debugger reply.
func (d *remoteDebugger) formatProv(addr uint16) string {
	r := d.provenance.lookup(addr)
	if !r.valid {
		return "no recorded writer (untouched since provenance armed)"
	}
	return fmt.Sprintf("val=$%02X written by insn %d at PC=%s (rom_bank=%d)",
		r.val, r.insn, annotateAddr(r.pc), r.romBank)
}

// formatProvPhys renders one physical-pool last-writer record.
func (d *remoteDebugger) formatProvPhys(bank16k int, off uint16) string {
	r := d.provenance.lookupPhys(bank16k, off)
	if !r.valid {
		return "no recorded writer (untouched since provenance armed)"
	}
	return fmt.Sprintf("val=$%02X written by insn %d at PC=%s",
		r.val, r.insn, annotateAddr(r.pc))
}

// cmdProvenancePhys queries the physical-pool last-writer table by
// 16K bank + intra-bank offset, ignoring paging:
//
//	provenance-phys BANK OFF      who last wrote RAM bank BANK at OFF
func (d *remoteDebugger) cmdProvenancePhys(args []string) string {
	if len(args) < 2 {
		return "ERR usage: provenance-phys BANK OFF"
	}
	bank, err := parseHex(args[0])
	if err != nil {
		return "ERR bad bank: " + err.Error()
	}
	off, err := parseHex(args[1])
	if err != nil {
		return "ERR bad offset: " + err.Error()
	}
	return fmt.Sprintf("OK provenance-phys bank=%d off=$%04X: %s",
		bank, uint16(off), d.formatProvPhys(int(bank), uint16(off)))
}

// resolvePhysical maps a logical CPU address to its current physical
// pool location (16K bank + intra-bank offset) via the Next MMU8.
// isRAM is false when the slot maps ROM ($FF sentinel). Mirrors the
// resolution in Memory.Write's Next path.
func (d *remoteDebugger) resolvePhysical(addr uint16) (bank16k int, off uint16, isRAM bool) {
	slot := byte(addr >> 13)
	bank8k := d.emu.mem.GetMMU(slot)
	if bank8k == 0xFF {
		return 0, 0, false // ROM mapped at this slot
	}
	bank16k = int(bank8k) >> 1
	off = uint16(bank8k&1)<<13 | (addr & 0x1FFF)
	return bank16k, off, true
}

// cmdWhyPC explains how the CPU arrived at the current PC by examining
// the return word most recently consumed off the stack. If that word
// equals the current PC, the jump arrived via RET and the provenance
// records name the instruction that pushed it — the causal link behind
// a bad-jump / corrupt-stack trap (e.g. a bank-2 $0000 trap).
func (d *remoteDebugger) cmdWhyPC(_ []string) string {
	if !d.provenance.enabled.Load() {
		return "ERR provenance not armed — run `provenance on` before the run"
	}
	cpu := d.emu.cpu
	w := d.provenance.explainPoppedReturn(cpu.SP, d.emu.mem.Read)

	var b strings.Builder
	fmt.Fprintf(&b, "OK why-pc: PC=%s SP=$%04X\n", annotateAddr(cpu.PC), cpu.SP)
	fmt.Fprintf(&b, "   popped return word $%04X from stack [$%04X,$%04X]\n",
		w.addr, w.loAddr, w.hiAddr)
	if w.addr == cpu.PC {
		b.WriteString("   => reached via RET (return word == PC). Pushed by:\n")
	} else {
		b.WriteString("   => return word != PC: PC was NOT reached via this RET\n")
		b.WriteString("      (likely JP/JR/JP(HL) or an interrupt — disassemble preceding code).\n")
		b.WriteString("      Stack-word provenance shown anyway:\n")
	}
	fmt.Fprintf(&b, "      lo $%04X: %s\n", w.loAddr, d.formatProv(w.loAddr))
	fmt.Fprintf(&b, "      hi $%04X: %s", w.hiAddr, d.formatProv(w.hiAddr))
	return b.String()
}

// logEndOfRunProvenance emits the return-word provenance at the end of
// a headless run (when --provenance is armed). If the run ended stuck
// in a trap reached via a bad RET, this names the instruction that
// pushed the bad return address — the autonomous form of `why-pc`.
func (d *remoteDebugger) logEndOfRunProvenance() {
	cpu := d.emu.cpu
	w := d.provenance.explainPoppedReturn(cpu.SP, d.emu.mem.Read)
	lo := w.loProv
	hi := w.hiProv
	slog.Info("provenance end-of-run why-pc",
		"pc", fmt.Sprintf("$%04X", cpu.PC),
		"sp", fmt.Sprintf("$%04X", cpu.SP),
		"popped_return", fmt.Sprintf("$%04X", w.addr),
		"via_ret", w.addr == cpu.PC,
		"lo_addr", fmt.Sprintf("$%04X", w.loAddr),
		"lo_writer_pc", fmt.Sprintf("$%04X", lo.pc),
		"lo_writer_insn", lo.insn,
		"lo_writer_valid", lo.valid,
		"hi_writer_pc", fmt.Sprintf("$%04X", hi.pc),
		"hi_writer_insn", hi.insn,
		"hi_writer_valid", hi.valid)
}
