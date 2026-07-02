package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/next/install"
)

// xrefHit is one match: a code site that statically references the
// queried address.
type xrefHit struct {
	kind   string // "rom", "divmmc-rom"
	bank   int    // 0..3 for rom; ignored for divmmc-rom
	offset uint16 // offset within bank
	bytes  []byte // raw instruction bytes (1-4)
	mnem   string // disassembly hint, e.g. "CALL $25B8" or "LD HL,$25B8"
}

// cmdXref scans the currently-loaded NextZXOS bank-ROMs and the
// divMMC ROM for any opcode that references the queried address.
// Covers: CALL/CALL-cc, JP/JP-cc, LD-HL/BC/DE-immediate (when
// nn==ADDR), LD HL,(nn) / LD A,(nn), LD (nn),HL / LD (nn),A,
// LD BC/DE/SP,(nn), LD (nn),BC/DE/SP, LD IX/IY,nn, LD IX/IY,(nn),
// LD (nn),IX/IY, and RST + DEFW.
//
// This is a static dump-walk; it doesn't care about reachability.
// The hits are ordered by kind then bank then offset.
func (d *remoteDebugger) cmdXref(args []string) string {
	if len(args) < 1 {
		return "ERR usage: xref ADDR    (scans loaded ROMs for opcode refs)"
	}
	target, err := parseHex(args[0])
	if err != nil {
		return "ERR " + err.Error()
	}
	hits := []xrefHit{}
	// Scan each NextZXOS ROM bank (4 × 16 KB).
	for bank := 0; bank < 4; bank++ {
		data := d.emu.mem.GetROMPage(bank)
		if data == nil {
			continue
		}
		hits = append(hits, scanForXref(data, target, "rom", bank)...)
	}
	// Scan the divMMC ROM if present. Routed via the pager's ROM
	// pointer (8 KB).
	if pager, ok := d.emu.ula.NextDivMMC().(*divmmc.Pager); ok && pager != nil {
		// The debugger.BankAccessor bridge doesn't currently surface
		// the divMMC ROM directly (the pager exposes RAMBank for RAM,
		// not the 8 KB ROM image).
		// Read the 8 KB divMMC ROM via the bank-peek path (kind
		// "altrom" is the wrong shape; the right access is via the
		// pager's RAMBank for RAM or a dedicated ROM accessor). For
		// now, use the install-time copy from disk if available.
		if rom := loadInstalledDivMMCROM(); rom != nil {
			hits = append(hits, scanForXref(rom, target, "divmmc-rom", 0)...)
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("OK no static references to $%04X found", target)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "OK %d references to $%04X:\r\n", len(hits), target)
	for _, h := range hits {
		if h.kind == "divmmc-rom" {
			fmt.Fprintf(&b, "  divmmc-rom $%04X: %s  %s\r\n",
				h.offset, fmtBytes(h.bytes), h.mnem)
		} else {
			fmt.Fprintf(&b, "  rom bank %d $%04X: %s  %s\r\n",
				h.bank, h.offset, fmtBytes(h.bytes), h.mnem)
		}
	}
	return strings.TrimRight(b.String(), "\r\n")
}

// scanForXref walks a ROM blob byte-by-byte looking for any opcode
// whose immediate-address operand equals target. It does NOT do
// real-instruction-flow disasm; every byte is treated as a potential
// opcode start. False positives are possible where a data byte
// happens to start a fake "LD HL,nn" — flagged with the bytes so
// the user can sanity-check.
func scanForXref(rom []byte, target uint16, kind string, bank int) []xrefHit {
	lo := byte(target & 0xFF)
	hi := byte(target >> 8)
	var hits []xrefHit
	for i := 0; i < len(rom); i++ {
		b0 := rom[i]
		// Single-byte opcodes followed by 2-byte immediate address.
		// Treat each pattern individually so we can emit a sensible
		// mnem string.
		switch b0 {
		case 0xC3: // JP nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("JP $%04X", target)))
			}
		case 0xCD: // CALL nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("CALL $%04X", target)))
			}
		case 0xC2, 0xCA, 0xD2, 0xDA, 0xE2, 0xEA, 0xF2, 0xFA: // JP cc,nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				cc := condName(b0, true)
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("JP %s,$%04X", cc, target)))
			}
		case 0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC: // CALL cc,nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				cc := condName(b0, false)
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("CALL %s,$%04X", cc, target)))
			}
		case 0x21: // LD HL,nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD HL,$%04X", target)))
			}
		case 0x01: // LD BC,nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD BC,$%04X", target)))
			}
		case 0x11: // LD DE,nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD DE,$%04X", target)))
			}
		case 0x31: // LD SP,nn
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD SP,$%04X", target)))
			}
		case 0x22: // LD (nn),HL
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD ($%04X),HL", target)))
			}
		case 0x2A: // LD HL,(nn)
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD HL,($%04X)", target)))
			}
		case 0x32: // LD (nn),A
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD ($%04X),A", target)))
			}
		case 0x3A: // LD A,(nn)
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("LD A,($%04X)", target)))
			}
		case 0xED:
			// ED-prefix opcodes: LD (nn),BC/DE/SP and LD BC/DE/SP,(nn)
			if i+3 >= len(rom) {
				continue
			}
			sub := rom[i+1]
			if rom[i+2] != lo || rom[i+3] != hi {
				continue
			}
			switch sub {
			case 0x43:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD ($%04X),BC", target)))
			case 0x53:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD ($%04X),DE", target)))
			case 0x73:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD ($%04X),SP", target)))
			case 0x4B:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD BC,($%04X)", target)))
			case 0x5B:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD DE,($%04X)", target)))
			case 0x7B:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD SP,($%04X)", target)))
			}
		case 0xDD, 0xFD:
			// DD/FD-prefix: LD IX/IY,nn  LD IX/IY,(nn)  LD (nn),IX/IY
			if i+3 >= len(rom) {
				continue
			}
			sub := rom[i+1]
			if rom[i+2] != lo || rom[i+3] != hi {
				continue
			}
			reg := "IX"
			if b0 == 0xFD {
				reg = "IY"
			}
			switch sub {
			case 0x21:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD %s,$%04X", reg, target)))
			case 0x2A:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD %s,($%04X)", reg, target)))
			case 0x22:
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+4],
					fmt.Sprintf("LD ($%04X),%s", target, reg)))
			}
		case 0xCF, 0xD7, 0xDF, 0xE7, 0xEF, 0xF7, 0xFF:
			// RST + DEFW (cross-bank dispatcher convention). The
			// dispatcher reads the 2 bytes after the RST as a target
			// address. We don't know if a given RST site uses this
			// convention without dynamic info; flag any RST whose
			// following 2 bytes equal target as a candidate.
			if i+2 < len(rom) && rom[i+1] == lo && rom[i+2] == hi {
				rst := (b0 - 0xC7) & 0x38
				hits = append(hits, mkHit(kind, bank, i, rom[i:i+3],
					fmt.Sprintf("RST $%02X; DEFW $%04X", rst, target)))
			}
		}
	}
	return hits
}

func mkHit(kind string, bank, i int, raw []byte, mnem string) xrefHit {
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return xrefHit{kind: kind, bank: bank, offset: uint16(i), bytes: cp, mnem: mnem}
}

// condName returns the Z80 condition mnemonic for a JP-cc / CALL-cc
// opcode. forJP toggles between JP-cc and CALL-cc encodings (they
// share the same cc bits but live in different opcode columns).
func condName(opcode byte, forJP bool) string {
	var bits byte
	if forJP {
		// JP cc: $C2/$CA/$D2/$DA/$E2/$EA/$F2/$FA  →  cc = (op-$C2)>>3
		bits = (opcode - 0xC2) >> 3
	} else {
		// CALL cc: $C4/$CC/$D4/$DC/$E4/$EC/$F4/$FC  →  cc = (op-$C4)>>3
		bits = (opcode - 0xC4) >> 3
	}
	switch bits {
	case 0:
		return "NZ"
	case 1:
		return "Z"
	case 2:
		return "NC"
	case 3:
		return "C"
	case 4:
		return "PO"
	case 5:
		return "PE"
	case 6:
		return "P"
	case 7:
		return "M"
	}
	return "?"
}

func fmtBytes(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, " ")
}

// loadInstalledDivMMCROM reads the divMMC ROM from the install path
// (same lookup the Spectrum Next emulator wiring uses). Returns nil
// if the file isn't installed — xref skips the divmmc-rom scan in
// that case. A read failure for any other reason is logged (rather
// than swallowed) since that signals a real problem with the install
// directory, not just an absent optional ROM.
func loadInstalledDivMMCROM() []byte {
	data, err := install.LoadROM(install.DivMMCROM)
	if err != nil {
		if !errors.Is(err, install.ErrROMNotInstalled) {
			slog.Warn("xref: divMMC ROM read failed", "err", err)
		}
		return nil
	}
	return data
}
