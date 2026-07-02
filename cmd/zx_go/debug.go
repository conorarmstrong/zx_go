package main

import (
	"bufio"
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/conorarmstrong/zx_go/pkg/debugger"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/divmmc"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// symbolTable maps addresses to human-readable names, populated
// by loadSymbolMap. The lookup is annotateAddr. Mutations during
// a debug session go through the symbolMu lock; the read path in
// annotateAddr also takes RLock so concurrent updates are safe.
var (
	symbolTable map[uint16]string
	symbolMu    sync.RWMutex
)

// loadSymbolMap reads `addr  name` lines into symbolTable. Lines
// starting with `#` or `;` are comments; blanks are skipped. The
// address is hex with optional `$` or `0x` prefix. Replaces any
// existing table — `reload-syms PATH` calls this to swap maps
// at runtime.
func loadSymbolMap(path string) {
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("symbol-map: open", "path", path, "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	tbl := make(map[uint16]string)
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		s := strings.TrimSpace(scanner.Text())
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(fields[0], "0x"), "$"), 16, 16)
		if err != nil {
			slog.Warn("symbol-map: bad addr", "line", line, "value", fields[0])
			continue
		}
		tbl[uint16(v)] = strings.Join(fields[1:], " ")
	}
	symbolMu.Lock()
	symbolTable = tbl
	symbolMu.Unlock()
	slog.Debug("symbol-map loaded", "path", path, "entries", len(tbl))
}

// annotateAddr returns "$XXXX (NAME)" if a symbol exists for the
// address, else just "$XXXX". Lock-free fast path when no symbols
// are loaded; takes RLock when the table exists.
func annotateAddr(addr uint16) string {
	symbolMu.RLock()
	name, ok := symbolTable[addr]
	symbolMu.RUnlock()
	if ok {
		return fmt.Sprintf("$%04X (%s)", addr, name)
	}
	return fmt.Sprintf("$%04X", addr)
}

// addSymbol adds or updates a single symbol-table entry. Used by
// the runtime `sym $ADDR NAME` debugger command so users can name
// addresses they've identified mid-session without rebuilding the
// symbol file.
func addSymbol(addr uint16, name string) {
	symbolMu.Lock()
	defer symbolMu.Unlock()
	if symbolTable == nil {
		symbolTable = make(map[uint16]string)
	}
	symbolTable[addr] = name
}

// clearSymbol removes a single symbol-table entry. No-op when the
// entry doesn't exist.
func clearSymbol(addr uint16) {
	symbolMu.Lock()
	defer symbolMu.Unlock()
	delete(symbolTable, addr)
}

// listSymbols returns every (addr, name) pair sorted by address.
// Used by the `sym` (no-arg) debugger command.
func listSymbols() []struct {
	Addr uint16
	Name string
} {
	symbolMu.RLock()
	defer symbolMu.RUnlock()
	out := make([]struct {
		Addr uint16
		Name string
	}, 0, len(symbolTable))
	for a, n := range symbolTable {
		out = append(out, struct {
			Addr uint16
			Name string
		}{a, n})
	}
	// Sort by address for stable output.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Addr > out[j].Addr; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// pcRange is a half-open address window [lo,hi].
type pcRange struct {
	name string
	lo   uint16
	hi   uint16
}

// pcTrigger holds the parsed --snapshot-on-pc trigger set and
// fires a callback the first time PC enters any of them.
type pcTrigger struct {
	ranges []pcRange
	hit    map[string]bool
	onHit  func(name string, pc uint16)
}

// parsePCTriggers reads "NAME@$XXXX" or "NAME@$XXXX-$YYYY" pairs
// from the comma-separated --snapshot-on-pc value.
func parsePCTriggers(spec string, onHit func(name string, pc uint16)) *pcTrigger {
	if spec == "" {
		return nil
	}
	t := &pcTrigger{
		hit:   make(map[string]bool),
		onHit: onHit,
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		at := strings.Index(part, "@")
		if at < 0 {
			at = strings.Index(part, ":")
		}
		if at < 0 {
			slog.Warn("snapshot-on-pc: malformed", "entry", part)
			continue
		}
		name := strings.TrimSpace(part[:at])
		rangeStr := strings.TrimSpace(part[at+1:])
		dash := strings.Index(rangeStr, "-")
		var lo, hi uint16
		if dash < 0 {
			v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(rangeStr, "0x"), "$"), 16, 16)
			if err != nil {
				slog.Warn("snapshot-on-pc: bad addr", "entry", part, "err", err)
				continue
			}
			lo = uint16(v)
			hi = lo
		} else {
			loV, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(rangeStr[:dash], "0x"), "$"), 16, 16)
			if err != nil {
				slog.Warn("snapshot-on-pc: bad lo", "entry", part)
				continue
			}
			hiV, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(rangeStr[dash+1:], "0x"), "$"), 16, 16)
			if err != nil {
				slog.Warn("snapshot-on-pc: bad hi", "entry", part)
				continue
			}
			lo = uint16(loV)
			hi = uint16(hiV)
		}
		t.ranges = append(t.ranges, pcRange{name: name, lo: lo, hi: hi})
	}
	return t
}

// Step is a CPU pre-fetch hook. Fires the trigger's onHit once
// for each range, the first time PC enters that range.
func (t *pcTrigger) Step(pc uint16) {
	for _, r := range t.ranges {
		if pc >= r.lo && pc <= r.hi && !t.hit[r.name] {
			t.hit[r.name] = true
			t.onHit(r.name, pc)
		}
	}
}

// loopDetector tracks consecutive M1 fetches at the same PC. When
// the same PC has been seen `threshold` times in a row, fires
// `onStall` exactly once per detected stall (re-armed when PC
// moves to a different address).
type loopDetector struct {
	threshold int
	lastPC    uint16
	count     int
	armed     bool
	onStall   func(pc uint16, count int)
}

func newLoopDetector(threshold int, onStall func(pc uint16, count int)) *loopDetector {
	if threshold <= 0 {
		return nil
	}
	return &loopDetector{threshold: threshold, armed: true, onStall: onStall}
}

func (ld *loopDetector) Step(pc uint16) {
	if pc == ld.lastPC {
		ld.count++
		if ld.armed && ld.count >= ld.threshold {
			ld.onStall(pc, ld.count)
			ld.armed = false
		}
	} else {
		ld.lastPC = pc
		ld.count = 1
		ld.armed = true
	}
}

// watchEntry pairs a human-readable label with a 16-bit address
// so periodic snapshots can show e.g. "ERR_NR" instead of "$5C3A".
// `word` selects 8- vs 16-bit reads — true → little-endian word.
type watchEntry struct {
	name string
	addr uint16
	word bool
}

// parseWatchSpec decodes the --watch flag value into a slice of
// watchEntries. The format is "name:hex,name:hex,...". Names
// containing a literal colon are not supported (the parse splits
// on the first colon only).
func parseWatchSpec(s string) []watchEntry {
	if s == "" {
		return defaultNextWatch()
	}
	var out []watchEntry
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		colon := strings.Index(part, ":")
		if colon < 0 {
			slog.Warn("watch: malformed entry; expected NAME:ADDR", "entry", part)
			continue
		}
		name := strings.TrimSpace(part[:colon])
		addrStr := strings.TrimSpace(part[colon+1:])
		// In the --watch flag, unprefixed values default to hex
		// since every Spectrum sysvar address is conventionally
		// written that way. `$XXXX` and `0xXXXX` still work.
		// Appending `:w` selects a 16-bit (little-endian) read.
		word := false
		if strings.HasSuffix(strings.ToLower(addrStr), ":w") {
			word = true
			addrStr = addrStr[:len(addrStr)-2]
		}
		v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(addrStr, "0x"), "$"), 16, 16)
		if err != nil {
			slog.Warn("watch: bad address", "entry", part, "err", err)
			continue
		}
		out = append(out, watchEntry{name: name, addr: uint16(v), word: word})
	}
	return out
}

// defaultNextWatch is the canonical Spectrum / Spectrum Next
// sysvar set worth tracking during boot. Picked to cover BASIC
// parser state (ERR_NR, FLAGS, CH-ADD, PROG, E-LINE), the
// NextZXOS extended FLAGS2 byte that gates autoexec, and a few
// MMU / NextReg shadows.
func defaultNextWatch() []watchEntry {
	return []watchEntry{
		{"ERR_NR", 0x5C3A, false},
		{"FLAGS", 0x5C3B, false},
		{"PROG", 0x5C53, true},
		{"E_LINE", 0x5C59, true},
		{"CH_ADD", 0x5C5D, true},
		{"MODE", 0x5C41, false},
		{"FLAGS2_ext", 0xD5D4, false},
	}
}

// snapshotConfig bundles snapshot scheduling state for the
// headless loop. The headless runner ticks this each frame; when
// frame % every == 0 a snapshot is emitted.
type snapshotConfig struct {
	every       int
	prefix      string
	saveScreens bool
	watch       []watchEntry
	emu         *emulator
	frameIdx    int

	// Previous watch values, for memory-diff emission. Indexed
	// by entry slot. Nil disables diff mode.
	prevWatchValues []uint16
}

func newSnapshotConfig(emu *emulator, f *cliFlags) *snapshotConfig {
	if f.snapshotEvery <= 0 {
		return nil
	}
	return &snapshotConfig{
		every:       f.snapshotEvery,
		prefix:      f.snapshotPrefix,
		saveScreens: f.snapshotScreens && f.snapshotPrefix != "",
		watch:       parseWatchSpec(f.watchSpec),
		emu:         emu,
	}
}

// Tick is called once per simulated frame. If the frame index is
// a multiple of `every`, a snapshot is emitted to stderr (via
// slog) and optionally a screenshot is written.
func (sc *snapshotConfig) Tick() {
	sc.frameIdx++
	if sc.frameIdx%sc.every != 0 {
		return
	}
	sc.emit()
}

func (sc *snapshotConfig) emit() {
	emu := sc.emu
	cpu := emu.cpu
	mem := emu.mem
	port7FFD, port1FFD, _ := mem.GetPortState()
	romBank := int((port7FFD>>4)&1) | int((port1FFD>>1)&2)
	slog.Debug("snapshot",
		"frame", sc.frameIdx,
		"pc", fmt.Sprintf("$%04X", cpu.PC),
		"sp", fmt.Sprintf("$%04X", cpu.SP),
		"af", fmt.Sprintf("$%02X%02X", cpu.A, cpu.F),
		"bc", fmt.Sprintf("$%02X%02X", cpu.B, cpu.C),
		"de", fmt.Sprintf("$%02X%02X", cpu.D, cpu.E),
		"hl", fmt.Sprintf("$%02X%02X", cpu.H, cpu.L),
		"ix", fmt.Sprintf("$%04X", cpu.IX),
		"iy", fmt.Sprintf("$%04X", cpu.IY),
		"iff1", cpu.IFF1, "im", cpu.IM, "halted", cpu.Halted,
		"insns", cpu.InstructionCount(),
		"rom_bank", romBank,
	)
	if mem.GetCurrentModel() == roms.ModelNext {
		mmu := [8]byte{}
		for i := byte(0); i < 8; i++ {
			mmu[i] = mem.GetMMU(i)
		}
		slog.Debug("snapshot.mmu", "frame", sc.frameIdx,
			"slots", fmt.Sprintf("%02X %02X %02X %02X %02X %02X %02X %02X",
				mmu[0], mmu[1], mmu[2], mmu[3], mmu[4], mmu[5], mmu[6], mmu[7]))
	}

	if len(sc.watch) > 0 {
		parts := make([]string, 0, len(sc.watch))
		// Read all current values first so we can produce both
		// full output and a diff line.
		curr := make([]uint16, len(sc.watch))
		for i, w := range sc.watch {
			if w.word {
				curr[i] = uint16(mem.Read(w.addr)) | uint16(mem.Read(w.addr+1))<<8
			} else {
				curr[i] = uint16(mem.Read(w.addr))
			}
			if w.word {
				parts = append(parts, fmt.Sprintf("%s=$%04X", w.name, curr[i]))
			} else {
				parts = append(parts, fmt.Sprintf("%s=$%02X", w.name, curr[i]))
			}
		}
		slog.Debug("snapshot.watch", "frame", sc.frameIdx,
			"vars", strings.Join(parts, " "))
		// Diff vs previous snapshot — only the entries that changed.
		// Lazy-allocate prevWatchValues on first snapshot.
		if sc.prevWatchValues != nil && len(sc.prevWatchValues) == len(curr) {
			diffs := []string{}
			for i, w := range sc.watch {
				if curr[i] != sc.prevWatchValues[i] {
					format := "%s: $%02X→$%02X"
					if w.word {
						format = "%s: $%04X→$%04X"
					}
					diffs = append(diffs, fmt.Sprintf(format, w.name, sc.prevWatchValues[i], curr[i]))
				}
			}
			if len(diffs) > 0 {
				slog.Debug("snapshot.watch.diff", "frame", sc.frameIdx,
					"changed", strings.Join(diffs, " "))
			}
		}
		sc.prevWatchValues = curr
	}

	if pager, ok := emu.ula.NextDivMMC().(*divmmc.Pager); ok && pager != nil {
		slog.Debug("snapshot.divmmc", "frame", sc.frameIdx,
			"paged_in", pager.IsPagedIn(),
			"mapram", pager.MAPRAM(),
			"automap", pager.AutomapEnabled())
	}

	// Disassembly window around PC. Shows 6 instructions starting
	// at PC so the reader can immediately tell what code we're
	// parked on without having to re-run with _tools/nextdis.
	read := func(addr uint16) byte { return mem.Read(addr) }
	lines := debugger.Disassemble(read, cpu.PC, 6)
	for _, l := range lines {
		slog.Debug("snapshot.disasm", "frame", sc.frameIdx, "line", l.String())
	}

	// Stack walk: read up to 16 16-bit words above SP. NextZXOS
	// puts return addresses there during cross-bank dispatcher
	// calls, so a stack walk often reveals the call chain that
	// led to the current PC. Annotated with the symbol map when
	// loaded.
	stack := []string{}
	for i := 0; i < 16; i++ {
		addr := cpu.SP + uint16(i*2)
		v := uint16(mem.Read(addr)) | uint16(mem.Read(addr+1))<<8
		stack = append(stack, annotateAddr(v))
	}
	slog.Debug("snapshot.stack", "frame", sc.frameIdx,
		"sp", fmt.Sprintf("$%04X", cpu.SP),
		"words", strings.Join(stack, " "))

	if sc.saveScreens {
		path := fmt.Sprintf("%s%06d.png", sc.prefix, sc.frameIdx)
		img := emu.ula.Render()
		fp, err := os.Create(path)
		if err != nil {
			slog.Warn("snapshot.screen: create failed", "path", path, "err", err)
			return
		}
		if err := png.Encode(fp, img); err != nil {
			slog.Warn("snapshot.screen: encode failed", "path", path, "err", err)
		}
		_ = fp.Close()
	}
}

// installBankSwitchLogger hooks memory.SetBankTracer to emit a
// slog event for every classic ROM-bank switch and every 8K-MMU
// slot reassignment.
//
// Replaces any previously-installed tracer (e.g. the JSON-Lines
// emitter from trace_wiring.go). Callers that want both should
// chain manually.
func installBankSwitchLogger(mem *memory.Memory) {
	mem.SetBankTracer(func(slot byte, bank int, source string) {
		slog.Debug("bank-switch",
			"slot", fmt.Sprintf("%d", slot),
			"new_bank", fmt.Sprintf("$%02X", bank&0xFF),
			"source", source,
		)
	})
}

// watchWriteEntry is one parsed --watch-writes target. If hasBreak
// is true, a write of breakValue to addr causes the headless run to
// dump full state and exit; otherwise the write is just logged.
type watchWriteEntry struct {
	name       string
	addr       uint16
	breakValue byte
	hasBreak   bool
}

// dumpMemRange is one parsed --dump-mem range. bank == -1 means
// "read via CPU bus" (the default); bank >= 0 reads the raw 16K
// SRAM page at that page index, bypassing MMU paging — useful for
// inspecting banks not currently mapped into the CPU's $0000-$FFFF
// window (e.g. bank 7 holding the tilemap behind the Browser).

type dumpMemRange struct {
	addr   uint16
	length uint16
	bank   int // -1 = CPU bus (default), >= 0 = raw SRAM page index
	rom    int // -1 = not a ROM dump, >= 0 = raw ROM bank index
}

// parseWatchWriteSpec parses the comma-separated --watch-writes
// argument. Each entry is `NAME(@|:)ADDR[=VAL]` where ADDR is a
// 16-bit hex address (optional `$` or `0x` prefix) and the
// optional `=VAL` is an 8-bit hex value that turns the entry into
// a break-on-write trap.
func parseWatchWriteSpec(spec string) []watchWriteEntry {
	if spec == "" {
		return nil
	}
	var entries []watchWriteEntry
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sep := strings.Index(part, "@")
		if sep < 0 {
			sep = strings.Index(part, ":")
		}
		if sep < 0 {
			slog.Warn("watch-writes: malformed entry; expected NAME@ADDR[=VAL]", "entry", part)
			continue
		}
		name := strings.TrimSpace(part[:sep])
		tail := strings.TrimSpace(part[sep+1:])
		addrStr, valStr := tail, ""
		if eq := strings.Index(tail, "="); eq >= 0 {
			addrStr = strings.TrimSpace(tail[:eq])
			valStr = strings.TrimSpace(tail[eq+1:])
		}
		addr, err := strconv.ParseUint(
			strings.TrimPrefix(strings.TrimPrefix(addrStr, "0x"), "$"), 16, 16)
		if err != nil {
			slog.Warn("watch-writes: bad address", "entry", part, "err", err)
			continue
		}
		e := watchWriteEntry{name: name, addr: uint16(addr)}
		if valStr != "" {
			v, err := strconv.ParseUint(
				strings.TrimPrefix(strings.TrimPrefix(valStr, "0x"), "$"), 16, 8)
			if err != nil {
				slog.Warn("watch-writes: bad value", "entry", part, "err", err)
				continue
			}
			e.breakValue = byte(v)
			e.hasBreak = true
		}
		entries = append(entries, e)
	}
	return entries
}

// parseDumpMemSpec parses the --dump-mem argument:
// `[bankN:|romN:]ADDR:LEN[,...]`, both ADDR and LEN hex (optional `$` /
// `0x` prefix). The optional `bankN:` prefix (decimal N, e.g.
// `bank7:`) reads the raw 16K SRAM page at index N. `romN:` reads
// the 16K ROM bank N (0-3 on Next, used to inspect what boot.bin
// loaded into the alt-ROM banks). Both bypass MMU paging.
func parseDumpMemSpec(spec string) []dumpMemRange {
	if spec == "" {
		return nil
	}
	var ranges []dumpMemRange
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bank := -1
		rom := -1
		if strings.HasPrefix(part, "bank") {
			rest := part[len("bank"):]
			colon := strings.Index(rest, ":")
			if colon < 0 {
				slog.Warn("dump-mem: bankN: prefix without trailing range", "entry", part)
				continue
			}
			n, err := strconv.Atoi(rest[:colon])
			if err != nil {
				slog.Warn("dump-mem: bad bank index", "entry", part, "err", err)
				continue
			}
			bank = n
			part = rest[colon+1:]
		} else if strings.HasPrefix(part, "rom") {
			rest := part[len("rom"):]
			colon := strings.Index(rest, ":")
			if colon < 0 {
				slog.Warn("dump-mem: romN: prefix without trailing range", "entry", part)
				continue
			}
			n, err := strconv.Atoi(rest[:colon])
			if err != nil {
				slog.Warn("dump-mem: bad rom index", "entry", part, "err", err)
				continue
			}
			rom = n
			part = rest[colon+1:]
		}
		colon := strings.Index(part, ":")
		if colon < 0 {
			slog.Warn("dump-mem: malformed range; expected ADDR:LEN", "entry", part)
			continue
		}
		addrStr := strings.TrimSpace(part[:colon])
		lenStr := strings.TrimSpace(part[colon+1:])
		addr, err := strconv.ParseUint(
			strings.TrimPrefix(strings.TrimPrefix(addrStr, "0x"), "$"), 16, 16)
		if err != nil {
			slog.Warn("dump-mem: bad address", "entry", part, "err", err)
			continue
		}
		length, err := strconv.ParseUint(
			strings.TrimPrefix(strings.TrimPrefix(lenStr, "0x"), "$"), 16, 16)
		if err != nil {
			slog.Warn("dump-mem: bad length", "entry", part, "err", err)
			continue
		}
		ranges = append(ranges, dumpMemRange{addr: uint16(addr), length: uint16(length), bank: bank, rom: rom})
	}
	return ranges
}

// pressKeyEntry is one parsed --press-key schedule entry.
type pressKeyEntry struct {
	frame int
	row   int  // 0..7
	mask  byte // 0x01..0x10
	name  string
}

// pressKeyMap is the Spectrum keyboard matrix lookup used by
// --press-key. Names are lowercase. row 0 = caps/Z/X/C/V; row 7 =
// space/sym/M/N/B; bit 0 of each row is the leftmost key (caps,
// A, Q, 1, 0, P, enter, space).
var pressKeyMap = map[string]struct {
	row  int
	mask byte
}{
	"caps": {0, 0x01}, "z": {0, 0x02}, "x": {0, 0x04}, "c": {0, 0x08}, "v": {0, 0x10},
	"a": {1, 0x01}, "s": {1, 0x02}, "d": {1, 0x04}, "f": {1, 0x08}, "g": {1, 0x10},
	"q": {2, 0x01}, "w": {2, 0x02}, "e": {2, 0x04}, "r": {2, 0x08}, "t": {2, 0x10},
	"1": {3, 0x01}, "2": {3, 0x02}, "3": {3, 0x04}, "4": {3, 0x08}, "5": {3, 0x10},
	"0": {4, 0x01}, "9": {4, 0x02}, "8": {4, 0x04}, "7": {4, 0x08}, "6": {4, 0x10},
	"p": {5, 0x01}, "o": {5, 0x02}, "i": {5, 0x04}, "u": {5, 0x08}, "y": {5, 0x10},
	"enter": {6, 0x01}, "l": {6, 0x02}, "k": {6, 0x04}, "j": {6, 0x08}, "h": {6, 0x10},
	"space": {7, 0x01}, "sym": {7, 0x02}, "m": {7, 0x04}, "n": {7, 0x08}, "b": {7, 0x10},
}

// parsePressKeySpec parses --press-key. Returns entries sorted by
// frame for the scheduler to step through linearly.
func parsePressKeySpec(spec string) []pressKeyEntry {
	if spec == "" {
		return nil
	}
	var out []pressKeyEntry
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		at := strings.Index(part, "@")
		if at < 0 {
			slog.Warn("press-key: malformed; expected NAME@FRAME", "entry", part)
			continue
		}
		name := strings.ToLower(strings.TrimSpace(part[:at]))
		frame, err := strconv.Atoi(strings.TrimSpace(part[at+1:]))
		if err != nil {
			slog.Warn("press-key: bad frame", "entry", part, "err", err)
			continue
		}
		// Chords: `caps+space@N` / `sym+p@N` press every named key
		// at the same frame (and release together), so the guest
		// sees a simultaneous combination — BREAK, quote, etc.
		for _, key := range strings.Split(name, "+") {
			key = strings.TrimSpace(key)
			mp, ok := pressKeyMap[key]
			if !ok {
				slog.Warn("press-key: unknown key", "name", key)
				continue
			}
			out = append(out, pressKeyEntry{frame: frame, row: mp.row, mask: mp.mask, name: key})
		}
	}
	return out
}

// installWriteWatchers parses --watch-writes and installs a
// WriteObserver that logs every write to one of the watched
// addresses. Each event includes the source PC (best-effort —
// captured via the supplied cpu closure since memory.Write
// doesn't carry one).
//
// Entries of the form `NAME@ADDR=VAL` upgrade from log-on-write to
// break-on-write: when a matching write fires, the run dumps full
// state (CPU + sysvars + NextRegs + every parsed --dump-mem range)
// and the configured breakOnWatch callback decides whether to halt.
// Headless mode wires this to "dump and os.Exit(0)" so a CI run can
// detect the bug; the GUI debugger leaves the callback nil (writes
// just log as before).
func installWriteWatchers(emu *emulator, spec string, onBreak func(e watchWriteEntry, pc uint16)) {
	entries := parseWatchWriteSpec(spec)
	if len(entries) == 0 {
		return
	}
	cpu := emu.cpu
	prev := emu.mem.WriteObserver
	emu.mem.WriteObserver = func(addr uint16, val byte, _ uint16) {
		if prev != nil {
			prev(addr, val, 0)
		}
		for _, e := range entries {
			if addr != e.addr {
				continue
			}
			// A bare addr/val/pc line can't say WHERE a $0000-$3FFF
			// write actually routed (altROM buffer, divMMC window,
			// MMU RAM, or dropped on ROM), so log the paging state
			// alongside.
			divInfo := "n/a"
			if emu.ula != nil {
				if q, ok := emu.ula.NextDivMMC().(interface {
					IsPagedIn() bool
					LastE3() byte
				}); ok && q != nil {
					divInfo = fmt.Sprintf("paged=%v,E3=$%02X", q.IsPagedIn(), q.LastE3())
				}
			}
			slog.Debug("watch-write",
				"name", e.name,
				"addr", fmt.Sprintf("$%04X", addr),
				"val", fmt.Sprintf("$%02X", val),
				"pc", fmt.Sprintf("$%04X", cpu.PC),
				"rom_bank", emu.mem.GetROMBank(),
				"alt", fmt.Sprintf("$%02X", emu.mem.AltROMReg()),
				"mmu0", fmt.Sprintf("$%02X", emu.mem.GetMMU(0)),
				"mmu1", fmt.Sprintf("$%02X", emu.mem.GetMMU(1)),
				// the slot routing the WATCHED address — a $C000-area
				// watch can't otherwise say which bank got the byte
				"mmuW", fmt.Sprintf("$%02X", emu.mem.GetMMU(byte(addr>>13))),
				"div", divInfo,
			)
			if e.hasBreak && val == e.breakValue && onBreak != nil {
				onBreak(e, cpu.PC)
			}
			return
		}
	}
}

// formatHexBytes is a tiny helper for hex dumps. n bytes
// formatted as "AA BB CC...".
func formatHexBytes(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%02X", v)
	}
	return strings.Join(parts, " ")
}
