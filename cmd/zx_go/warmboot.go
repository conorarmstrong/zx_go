package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/install"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// applyWarmBoot loads a captured reference post-init state directly into
// our emulator, bypassing the full FPGA-bootrom -> NextZXOS boot path.
// Opt-in via $ZX_GO_WARM_BOOT=1.
//
// Requires two gitignored files in roms/next/:
//
//   - zes_full_ram.bin     2 MB Machine RAM dump from the reference emulator
//   - zes_nextregs.txt     Header line `# regs: PC=NN SP=NN ...`
//     followed by 256 lines `NR $NN = NNH`
//
// Captured via ZRCP on a running reference TBBlue instance:
//
//	set-memory-zone 0
//	save-binary /tmp/zes_full_ram.bin 0 2097152
//	(plus tbblue-get-register 0..255 loop)
func applyWarmBoot(cpu *z80.CPU, mem *memory.Memory, disp *nextregs.Dispatcher) error {
	ram, err := install.LoadROM(install.WarmBootRAM)
	if err != nil {
		return fmt.Errorf("ram dump: %w", err)
	}
	const ramSize = 2 * 1024 * 1024
	if len(ram) < ramSize {
		return fmt.Errorf("ram dump size %d < %d", len(ram), ramSize)
	}
	// the reference emulator's "Machine RAM" zone 0 lays out 2 MB of physical SRAM
	// where banks 0-15 (the first 256 KB) hold FPGA-internal areas
	// (ROM 0-3, divMMC ROM/RAM, alt-ROM, etc.) and OS-visible RAM
	// banks 0..111 live at offsets (N+16)*16384. This matches the
	// Spectrum Next FPGA's SRAM address formula
	// `mmu_A21_A13 = ("0001" + page[7..5]) & page[4..0]` which biases
	// every MMU8 page by +16 16K-banks before dereferencing SRAM
	// (zxnext.vhd:2964). Our m.ram[N] indexes by OS bank, so we apply
	// the +16 shift here.
	//
	// Without this shift, warm-boot would load FPGA ROM bytes into
	// m.ram[0..15] and shift all OS RAM by 16 banks — e.g. the ULA
	// screen 0 (= m.ram[5]) would receive boot code instead of the
	// welcome banner pixels, rendering as garbage even with a valid
	// snapshot.
	const fpgaBankShift = 16
	for bank := 0; bank < 128-fpgaBankShift; bank++ {
		page := mem.GetPage(bank)
		if len(page) < 0x4000 {
			continue
		}
		srcStart := (bank + fpgaBankShift) * 0x4000
		srcEnd := srcStart + 0x4000
		if srcEnd > len(ram) {
			break
		}
		copy(page, ram[srcStart:srcEnd])
	}

	// Overlay the captured ULA bank-5 BRAM content (if present).
	// The FPGA's ULA screen storage is a dedicated dual-port BRAM
	// not visible through ZRCP's Machine RAM zone, so the 2 MB dump
	// loaded above misses the live display content. the reference emulator's
	// `save-screen` ZRCP command exports the BRAM as a classic .SCR
	// file (6912 bytes: 6144 pixels + 768 attrs). We load it into
	// m.ram[5][0..0x1B00] so our ULA renderer (which reads m.ram[5])
	// shows the same image the user saw on the reference emulator.
	// the reference emulator's `save-screen` returns the currently-displayed bank
	// (either bank 5 BRAM or bank 7 BRAM depending on shadow-display
	// state). We don't know which without inspecting more state, so
	// load the SCR into BOTH banks at the standard ULA screen offset
	// $0000-$1AFF (= 6144 pixels + 768 attrs = 6912 bytes). The
	// non-display bank gets the same content which is harmless — the
	// OS will overwrite it if it needs to.
	if scr, err := install.LoadROM(install.WarmBootScreen); err == nil && len(scr) >= 6912 {
		for _, bank := range []int{5, 7} {
			if page := mem.GetPage(bank); len(page) >= 0x1B00 {
				copy(page[:0x1B00], scr[:0x1B00])
			}
		}
		slog.Info("warm-boot screen overlay",
			"bytes", 0x1B00,
			"first_pixel", fmt.Sprintf("$%02X", scr[0]),
			"first_attr", fmt.Sprintf("$%02X", scr[0x1800]),
			"banks", "5,7")
	} else {
		slog.Info("warm-boot screen overlay skipped",
			"err", err,
			"len", len(scr))
	}

	nrText, err := install.LoadROM(install.WarmBootNRs)
	if err != nil {
		return fmt.Errorf("nr dump: %w", err)
	}
	regs, nrs, err := parseWarmBootNRs(nrText)
	if err != nil {
		return fmt.Errorf("parse nrs: %w", err)
	}
	for reg, val := range nrs {
		disp.WriteReg(byte(reg), val)
	}

	cpu.PC = regs.PC
	cpu.SP = regs.SP
	cpu.A = byte(regs.AF >> 8)
	cpu.F = byte(regs.AF)
	cpu.SetBC(regs.BC)
	cpu.SetDE(regs.DE)
	cpu.SetHL(regs.HL)
	cpu.IX = regs.IX
	cpu.IY = regs.IY
	cpu.I = regs.I
	cpu.R = regs.R
	cpu.IM = byte(regs.IM)
	cpu.IFF1 = regs.IFF1
	cpu.IFF2 = regs.IFF2

	// the reference emulator captures snapshots mid-IRQ-acceptance (IM 0, IFF 0)
	// because the CPU is paused INSIDE the ISR at PC=$0038. Restoring
	// IM=0 leaves us in a mode where 50 Hz frame interrupts never
	// fire (we have no external IM-0 vector device), so the OS stalls
	// after the current ISR returns with EI; RET. Real Next runs in
	// IM 1 — force it here so post-warm-boot interrupts keep firing.
	if cpu.IM == 0 {
		cpu.IM = 1
		slog.Info("warm-boot: forced IM=1 (snapshot had IM=0 from mid-ISR capture)")
	}
	// NextZXOS uses DI; HALT as its idle pattern (Browser key wait,
	// etc.). Per Z80 spec the /INT pulse exits HALT even when IFF1=0;
	// our default-off HaltWakeOnInt preserves the classic-Spectrum
	// convention for tests, so enable it explicitly for warm-boot
	// where NextZXOS depends on the spec behaviour.
	cpu.HaltWakeOnInt = true

	slog.Info("warm-boot regs",
		"pc", fmt.Sprintf("$%04X", cpu.PC),
		"sp", fmt.Sprintf("$%04X", cpu.SP),
		"im", cpu.IM, "iff1", cpu.IFF1,
		"nr$57", fmt.Sprintf("$%02X", nrs[0x57]),
		"nr$03", fmt.Sprintf("$%02X", nrs[0x03]))
	return nil
}

type warmBootRegs struct {
	PC, SP, AF, BC, DE, HL, IX, IY uint16
	I, R                           byte
	IM                             int
	IFF1, IFF2                     bool
}

// parseWarmBootNRs reads the zes_nextregs.txt file format:
//
//	# the reference emulator state at ...
//	# regs: PC=0038 SP=ff3a AF=6320 BC=253b HL=532b DE=fd01 IX=5cb6 IY=5c3a ...
//	NR $00 = 0AH
//	...
//	NR $FF = 00H
func parseWarmBootNRs(data []byte) (warmBootRegs, map[int]byte, error) {
	var regs warmBootRegs
	nrs := map[int]byte{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "# regs:") {
			parseWarmBootRegLine(strings.TrimPrefix(line, "# regs:"), &regs)
			continue
		}
		if !strings.HasPrefix(line, "NR ") {
			continue
		}
		// NR $XX = YYH
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		regHex := strings.TrimSpace(line[3:eq])
		valHex := strings.TrimSpace(line[eq+1:])
		regHex = strings.TrimPrefix(regHex, "$")
		valHex = strings.TrimSuffix(valHex, "H")
		reg, err := strconv.ParseInt(regHex, 16, 16)
		if err != nil {
			continue
		}
		val, err := strconv.ParseInt(valHex, 16, 16)
		if err != nil {
			continue
		}
		nrs[int(reg)] = byte(val)
	}
	if regs.PC == 0 && regs.SP == 0 {
		return regs, nrs, fmt.Errorf("regs header missing or unparsed")
	}
	return regs, nrs, sc.Err()
}

func parseWarmBootRegLine(line string, regs *warmBootRegs) {
	for _, tok := range strings.Fields(line) {
		eq := strings.Index(tok, "=")
		if eq <= 0 {
			continue
		}
		key := strings.ToUpper(tok[:eq])
		val := tok[eq+1:]
		v, err := strconv.ParseUint(val, 16, 16)
		if err != nil {
			continue
		}
		switch key {
		case "PC":
			regs.PC = uint16(v)
		case "SP":
			regs.SP = uint16(v)
		case "AF":
			regs.AF = uint16(v)
		case "BC":
			regs.BC = uint16(v)
		case "DE":
			regs.DE = uint16(v)
		case "HL":
			regs.HL = uint16(v)
		case "IX":
			regs.IX = uint16(v)
		case "IY":
			regs.IY = uint16(v)
		case "I":
			regs.I = byte(v)
		case "R":
			regs.R = byte(v)
		case "IM0":
			regs.IM = 0
		case "IM1":
			regs.IM = 1
		case "IM2":
			regs.IM = 2
		}
	}
	if strings.Contains(line, "IM0 ") {
		regs.IM = 0
	} else if strings.Contains(line, "IM1 ") {
		regs.IM = 1
	} else if strings.Contains(line, "IM2 ") {
		regs.IM = 2
	}
	regs.IFF1 = strings.Contains(line, "IFF12") || strings.Contains(line, "IFF1 ")
	regs.IFF2 = strings.Contains(line, "IFF12") || strings.Contains(line, "IFF2 ")
}
