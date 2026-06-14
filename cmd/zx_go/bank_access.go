package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/memory"
)

// emuBanks is the debugger.BankAccessor implementation that exposes
// physical RAM / ROM / divMMC-RAM / alt-ROM banks of the running
// emulator, independent of CPU mapping. Used by both the telnet
// (bank-peek / bank-poke) and visual debugger surfaces.
//
// Kinds:
//
//	"ram"        — 128 banks of 16 KB (Memory.GetPage)
//	"rom"        — 4 banks of 16 KB   (Memory.GetROMPage)
//	"divmmc-ram" — 8 banks of 8 KB    (divmmc.Pager.RAMBank)
//	"altrom"     — 2 banks of 16 KB   (Memory.GetAltROM)
type emuBanks struct {
	mem *memory.Memory
	emu *emulator // for divMMC pager lookup; nil for non-Next
}

// divMMCRAMBanker is satisfied by *divmmc.Pager. Interface-typed so
// this file doesn't take a build-time dependency on the divmmc
// package; the cast is cheap and only triggered when the kind says
// so.
type divMMCRAMBanker interface {
	RAMBank(int) []byte
}

func (b *emuBanks) Bank(kind string, bank int) ([]byte, error) {
	switch kind {
	case "ram":
		s := b.mem.GetPage(bank)
		if s == nil {
			return nil, debugger.ErrBankOutOfRange
		}
		return s, nil
	case "rom":
		s := b.mem.GetROMPage(bank)
		if s == nil {
			return nil, debugger.ErrBankOutOfRange
		}
		return s, nil
	case "altrom":
		s := b.mem.GetAltROM(bank)
		if s == nil {
			return nil, debugger.ErrBankOutOfRange
		}
		return s, nil
	case "divmmc-ram":
		if b.emu == nil || b.emu.ula == nil {
			return nil, debugger.ErrUnknownKind
		}
		p, ok := b.emu.ula.NextDivMMC().(divMMCRAMBanker)
		if !ok {
			return nil, debugger.ErrUnknownKind
		}
		s := p.RAMBank(bank)
		if s == nil {
			return nil, debugger.ErrBankOutOfRange
		}
		return s, nil
	}
	return nil, debugger.ErrUnknownKind
}

func (b *emuBanks) BankPeek(kind string, bank, off, n int) ([]byte, error) {
	return debugger.BankPeekFrom(b, kind, bank, off, n)
}

func (b *emuBanks) BankPoke(kind string, bank, off int, data []byte) error {
	return debugger.BankPokeFrom(b, kind, bank, off, data)
}

// banks returns a freshly-constructed accessor for the current
// emulator. Kept allocation-light because every debugger command
// builds one — these are tiny structs.
func (d *remoteDebugger) banks() *emuBanks {
	return &emuBanks{mem: d.emu.mem, emu: d.emu}
}

// cmdBankPeek parses `bank-peek KIND BANK OFF [LEN]` and returns
// either a hexdump line or an ERR.
func (d *remoteDebugger) cmdBankPeek(args []string) string {
	if len(args) < 3 {
		return "ERR usage: bank-peek KIND BANK OFF [LEN]  (kinds: ram rom altrom divmmc-ram)"
	}
	kind := strings.ToLower(args[0])
	bank, err := parseHex(args[1])
	if err != nil {
		return "ERR bad bank: " + err.Error()
	}
	off, err := parseHex(args[2])
	if err != nil {
		return "ERR bad off: " + err.Error()
	}
	n := uint16(16)
	if len(args) >= 4 {
		v, err := parseHex(args[3])
		if err != nil {
			return "ERR bad len: " + err.Error()
		}
		n = v
	}
	if n > 256 {
		n = 256
	}
	out, perr := d.banks().BankPeek(kind, int(bank), int(off), int(n))
	if perr != nil {
		return "ERR " + perr.Error()
	}
	var sb strings.Builder
	for i, by := range out {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02X", by)
	}
	return "OK " + sb.String()
}

// cmdBankPoke parses `bank-poke KIND BANK OFF BYTES...` where each
// BYTE is a single hex value (no 0x prefix needed but accepted).
func (d *remoteDebugger) cmdBankPoke(args []string) string {
	if len(args) < 4 {
		return "ERR usage: bank-poke KIND BANK OFF BYTE [BYTE...]"
	}
	kind := strings.ToLower(args[0])
	bank, err := parseHex(args[1])
	if err != nil {
		return "ERR bad bank: " + err.Error()
	}
	off, err := parseHex(args[2])
	if err != nil {
		return "ERR bad off: " + err.Error()
	}
	data := make([]byte, 0, len(args)-3)
	for _, b := range args[3:] {
		v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(b, "$"), "0x"), 16, 8)
		if err != nil {
			return "ERR bad byte " + b + ": " + err.Error()
		}
		data = append(data, byte(v))
	}
	if perr := d.banks().BankPoke(kind, int(bank), int(off), data); perr != nil {
		return "ERR " + perr.Error()
	}
	return fmt.Sprintf("OK wrote %d bytes", len(data))
}

// cmdDisasmBank disassembles N instructions starting at OFF inside
// the underlying buffer of KIND BANK, ignoring whatever the CPU has
// mapped. KIND values match `bank-peek`: ram / rom / altrom /
// divmmc-ram. Resolves the "what's the real code at enNextZX bank 3
// $1F01" question without manual file-offset arithmetic.
func (d *remoteDebugger) cmdDisasmBank(args []string) string {
	if len(args) < 3 {
		return "ERR usage: disasm-bank KIND BANK OFF [N]"
	}
	kind := strings.ToLower(args[0])
	bank, err := parseHex(args[1])
	if err != nil {
		return "ERR bad bank: " + err.Error()
	}
	off, err := parseHex(args[2])
	if err != nil {
		return "ERR bad off: " + err.Error()
	}
	count := 8
	if len(args) >= 4 {
		if v, err := strconv.Atoi(args[3]); err == nil && v > 0 {
			count = v
			if count > 64 {
				count = 64
			}
		}
	}
	buf, err := d.banks().Bank(kind, int(bank))
	if err != nil {
		return "ERR " + err.Error()
	}
	if int(off) >= len(buf) {
		return "ERR offset past end of bank"
	}
	read := func(a uint16) byte {
		idx := int(off) + int(a)
		if idx < 0 || idx >= len(buf) {
			return 0xFF
		}
		return buf[idx]
	}
	lines := debugger.Disassemble(read, 0, count)
	var b strings.Builder
	b.WriteString("OK\r\n")
	for _, l := range lines {
		// Rewrite the synthetic 0-based PC the disassembler used into
		// the bank-offset address the user actually asked about, so a
		// disasm at $1F01 reads as $1F01..$1F.. rather than $0000..
		realAddr := int(off) + int(l.Addr)
		hex := ""
		for _, by := range l.Bytes {
			hex += fmt.Sprintf("%02X ", by)
		}
		for len(hex) < 12 {
			hex += "   "
		}
		if l.Operand != "" {
			fmt.Fprintf(&b, "  %s:%d:%04X  %s %s %s\r\n",
				kind, bank, realAddr, hex, l.Mnem, l.Operand)
		} else {
			fmt.Fprintf(&b, "  %s:%d:%04X  %s %s\r\n",
				kind, bank, realAddr, hex, l.Mnem)
		}
	}
	return strings.TrimRight(b.String(), "\r\n")
}

// cmdListBanks describes the bank pools available on this emulator
// build. Helpful for users who don't remember what kinds are valid
// or how many banks each exposes.
func (d *remoteDebugger) cmdListBanks() string {
	var sb strings.Builder
	sb.WriteString("OK ")
	sb.WriteString("ram=128*16K rom=4*16K altrom=2*16K")
	if d.emu != nil && d.emu.ula != nil {
		if _, ok := d.emu.ula.NextDivMMC().(divMMCRAMBanker); ok {
			sb.WriteString(" divmmc-ram=8*8K")
		}
	}
	return sb.String()
}

// poolHit is one pool-scan match.
type poolHit struct {
	bank, off int
}

// poolScan searches every bank of the given kind for the byte pattern,
// returning up to max hits. Banks are probed until the accessor
// reports out-of-range, so it adapts to model RAM size.
func poolScan(b *emuBanks, kind string, pattern []byte, max int) []poolHit {
	var hits []poolHit
	for bank := 0; ; bank++ {
		page, err := b.Bank(kind, bank)
		if err != nil {
			break
		}
		for off := 0; ; {
			i := bytes.Index(page[off:], pattern)
			if i < 0 {
				break
			}
			hits = append(hits, poolHit{bank: bank, off: off + i})
			if len(hits) >= max {
				return hits
			}
			off += i + 1
		}
	}
	return hits
}

// cmdPoolScan parses `pool-scan HEXBYTES [KIND]` (kind defaults to
// ram) and reports every match as "KIND bank N off $XXXX".
func (d *remoteDebugger) cmdPoolScan(args []string) string {
	if len(args) < 1 {
		return "ERR usage: pool-scan HEXBYTES [KIND]"
	}
	hexStr := strings.NewReplacer(" ", "", "$", "", ",", "").Replace(args[0])
	pattern, err := hex.DecodeString(hexStr)
	if err != nil || len(pattern) == 0 {
		return "ERR bad hex pattern"
	}
	kind := "ram"
	if len(args) > 1 {
		kind = args[1]
	}
	hits := poolScan(d.banks(), kind, pattern, 32)
	if len(hits) == 0 {
		return "OK no match"
	}
	var sb strings.Builder
	sb.WriteString("OK")
	for _, h := range hits {
		fmt.Fprintf(&sb, " %s:%d:$%04X", kind, h.bank, h.off)
	}
	return sb.String()
}
