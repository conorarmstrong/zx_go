package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// divInjectTarget is the subset of the divMMC pager the state transplant
// needs: writable RAM banks + the port-$E3 setter.
type divInjectTarget interface {
	RAMBank(int) []byte
	WritePort(uint16, byte) bool
}

// injectForeignState loads a captured "FX|" state export (produced by the
// state-export tool under _tools/) and transplants it into our emulator at
// the pre-ENTER checkpoint: working RAM banks 0-31, the AltROM (NR$56 banks
// 236-239), every divMMC-RAM bank, the MMU/AltROM NextRegs, the paging port
// $E3, and the full CPU register file. This lets a known-good external state
// be driven through our own execution/timing, isolating whether a boot
// divergence originates in state (what we start with) or execution (what we
// do with it).
func injectForeignState(path string, mem *memory.Memory, cpu *z80.CPU, nr *nextregs.Dispatcher, div divInjectTarget) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var nrBytes []byte
	cpuLine := ""
	e3 := byte(0xFF)
	ram := map[int][]byte{}
	divb := map[int][]byte{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 4)
		if len(parts) < 2 || parts[0] != "FX" {
			continue
		}
		switch parts[1] {
		case "NR":
			if nrBytes == nil {
				nrBytes, _ = hex.DecodeString(parts[2])
			}
		case "CPU":
			if cpuLine == "" {
				cpuLine = parts[2]
			}
		case "PORT":
			for _, kv := range strings.Fields(parts[2]) {
				if strings.HasPrefix(kv, "e3=") {
					v, _ := strconv.ParseUint(kv[3:], 16, 8)
					e3 = byte(v)
				}
			}
		case "RAM":
			b, _ := strconv.Atoi(parts[2])
			if _, ok := ram[b]; !ok {
				ram[b], _ = hex.DecodeString(parts[3])
			}
		case "DIV":
			b, _ := strconv.Atoi(parts[2])
			if _, ok := divb[b]; !ok {
				divb[b], _ = hex.DecodeString(parts[3])
			}
		}
	}

	// 1. working RAM banks 0-31
	for b, data := range ram {
		if b >= 0 && b < 224 && len(data) == 0x2000 {
			if pg := mem.RAM8KPage(b); pg != nil {
				copy(pg, data)
			}
		}
	}
	// 2. AltROM: banks 236/237 -> altROM0 (128), 238/239 -> altROM1 (48)
	altMap := []struct {
		bank, buf, off int
	}{{236, 0, 0}, {237, 0, 0x2000}, {238, 1, 0}, {239, 1, 0x2000}}
	for _, m := range altMap {
		if data, ok := ram[m.bank]; ok && len(data) == 0x2000 {
			if a := mem.GetAltROM(m.buf); a != nil && m.off+0x2000 <= len(a) {
				copy(a[m.off:m.off+0x2000], data)
			}
		}
	}
	// 3. divMMC RAM banks
	for b, data := range divb {
		if len(data) == 0x2000 {
			if bank := div.RAMBank(b); len(bank) >= 0x2000 {
				copy(bank, data)
			}
		}
	}
	// 4. NextRegs: MMU ($50-$57) + AltROM reg ($8C). Do NOT inject $8E —
	//    its SetROMBankExtended side-effects re-page slot 3 / port-7FFD and
	//    leave the map inconsistent with the explicit MMU banks.
	if len(nrBytes) == 256 {
		nr.WriteReg(0x8C, nrBytes[0x8C])
		for r := 0x50; r <= 0x57; r++ {
			nr.WriteReg(byte(r), nrBytes[r])
		}
	}
	// 5. divMMC port $E3 (CONMEM/MAPRAM/bank)
	if e3 != 0xFF {
		div.WritePort(0x00E3, e3)
	}
	// 6. CPU register file
	applyCPULine(cpuLine, cpu)

	slog.Info("INJECT", "ramBanks", len(ram), "divBanks", len(divb), "e3", fmt.Sprintf("%02X", e3), "pc", fmt.Sprintf("%04X", cpu.PC))
	return nil
}

func applyCPULine(line string, cpu *z80.CPU) {
	hi := func(v uint64) byte { return byte(v >> 8) }
	lo := func(v uint64) byte { return byte(v) }
	for _, kv := range strings.Fields(line) {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, valStr := kv[:eq], kv[eq+1:]
		v, _ := strconv.ParseUint(valStr, 16, 16)
		switch key {
		case "af":
			cpu.A, cpu.F = hi(v), lo(v)
		case "bc":
			cpu.B, cpu.C = hi(v), lo(v)
		case "de":
			cpu.D, cpu.E = hi(v), lo(v)
		case "hl":
			cpu.H, cpu.L = hi(v), lo(v)
		case "af_":
			cpu.A_, cpu.F_ = hi(v), lo(v)
		case "bc_":
			cpu.B_, cpu.C_ = hi(v), lo(v)
		case "de_":
			cpu.D_, cpu.E_ = hi(v), lo(v)
		case "hl_":
			cpu.H_, cpu.L_ = hi(v), lo(v)
		case "ix":
			cpu.IX = uint16(v)
		case "iy":
			cpu.IY = uint16(v)
		case "sp":
			cpu.SP = uint16(v)
		case "pc":
			cpu.PC = uint16(v)
		case "i":
			cpu.I = byte(v)
		case "r":
			cpu.R = byte(v)
		case "iff1":
			cpu.IFF1 = v != 0
		case "iff2":
			cpu.IFF2 = v != 0
		case "im":
			cpu.IM = byte(v)
		}
	}
}
