package debugger

import "sync"

// HistoryEntry is one captured CPU state at the moment of an M1
// fetch. The base fields (PC, SP, A, F, IFFIM, Insns, ROMBnk) are
// always populated; the optional wide fields (BC..IY) are only
// meaningful when the History was created with Wide=true at
// NewHistory time. Diagnostic questions like "what was IX at insn
// 16185730?" need the wide fields; the original "what PC sequence
// led up to this fault?" use case is fine with the base set.
type HistoryEntry struct {
	PC     uint16
	SP     uint16
	A      byte
	F      byte
	IFFIM  byte   // packed: bit 7 = IFF1, bit 6 = IFF2, bit 5 = Halted, bits 1:0 = IM
	Insns  uint64 // instruction counter at this fetch
	ROMBnk byte   // currently-mapped ROM bank (0..3 on Next)

	// Wide fields. Zero unless the producer populates them. A
	// History created with Wide=true requires the producer to fill
	// these on every Push; the visual / telnet history viewers
	// detect zero values and render "—" if all-wide is zero.
	BC uint16
	DE uint16
	HL uint16
	IX uint16
	IY uint16

	// Source classifies how PC reached this fetch from its
	// predecessor. Populated by the M1 prefetch hook from the
	// CPU's BranchSource latch (set by the last branching
	// instruction or interrupt event). SourceSequential when the
	// previous instruction fell through.
	Source     PCSource
	SourceFrom uint16 // PC of the instruction that caused the branch
}

// PCSource classifies a PC transition's cause for history display.
type PCSource byte

const (
	SourceSequential PCSource = iota
	SourceJP                  // JP nn or JP HL/IX/IY
	SourceJR                  // JR / DJNZ
	SourceCall                // CALL nn / CALL cc,nn / Z80N PUSH NN
	SourceRet                 // RET / RETI / RETN
	SourceRst                 // RST n
	SourceInt                 // interrupt taken (IM 0/1/2)
	SourceNmi                 // NMI taken
	SourceReset               // soft or hard reset
)

// String returns a short label for the source class. Used by
// history viewers when annotating entries.
func (s PCSource) String() string {
	switch s {
	case SourceJP:
		return "JP"
	case SourceJR:
		return "JR"
	case SourceCall:
		return "CALL"
	case SourceRet:
		return "RET"
	case SourceRst:
		return "RST"
	case SourceInt:
		return "INT"
	case SourceNmi:
		return "NMI"
	case SourceReset:
		return "RESET"
	default:
		return "seq"
	}
}

// IFF1 reports whether IFF1 was set at the time of capture.
func (e HistoryEntry) IFF1() bool { return e.IFFIM&0x80 != 0 }

// IFF2 reports whether IFF2 was set.
func (e HistoryEntry) IFF2() bool { return e.IFFIM&0x40 != 0 }

// Halted reports whether the CPU was halted.
func (e HistoryEntry) Halted() bool { return e.IFFIM&0x20 != 0 }

// IM returns the interrupt mode (0, 1, or 2).
func (e HistoryEntry) IM() int { return int(e.IFFIM & 0x03) }

// History is a fixed-size circular buffer of HistoryEntry records.
// Push is called from the CPU pre-fetch hook every M1 fetch; Last
// is called from debugger commands to read the most-recent N
// entries (in chronological order, oldest first).
//
// The buffer is goroutine-safe: Push uses a single mutex, Last
// takes a snapshot of the relevant slice under the same lock.
// For the production hot path (~1M entries / second) the mutex
// contention is negligible because Last is called rarely (once
// per `prev` debugger command); Push has no other contender.
type History struct {
	mu    sync.Mutex
	ring  []HistoryEntry
	head  int  // next write index
	full  bool // true once we've wrapped
	count int  // entries written (capped at len(ring))

	// wide reports whether producers are populating the
	// BC..IY fields. Set at NewHistoryWide time so viewers can
	// decide whether to render those columns.
	wide bool
}

// NewHistoryWide is like NewHistory but flags the buffer as
// "producers will populate BC/DE/HL/IX/IY on every Push". Viewers
// gate the wide columns on this so a narrow ring doesn't render
// stale zeros.
func NewHistoryWide(size int) *History {
	h := NewHistory(size)
	h.wide = true
	return h
}

// Wide reports whether this history's entries carry BC/DE/HL/IX/IY.
func (h *History) Wide() bool { return h.wide }

// NewHistory returns a History sized to hold size entries.
// size <= 0 disables recording (Push is a no-op).
func NewHistory(size int) *History {
	if size <= 0 {
		return &History{}
	}
	return &History{ring: make([]HistoryEntry, size)}
}

// Push appends an entry, overwriting the oldest if the ring is
// full. Hot path — keep allocations to zero.
func (h *History) Push(e HistoryEntry) {
	if len(h.ring) == 0 {
		return
	}
	h.mu.Lock()
	h.ring[h.head] = e
	h.head = (h.head + 1) % len(h.ring)
	if !h.full {
		h.count++
		if h.count >= len(h.ring) {
			h.full = true
		}
	}
	h.mu.Unlock()
}

// Last returns the last n entries in chronological order (oldest
// first, newest last). n is clamped to the number of entries
// currently stored. Returns a fresh slice; the caller may retain
// it without sync concerns.
func (h *History) Last(n int) []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	have := h.count
	if h.full {
		have = len(h.ring)
	}
	if n > have {
		n = have
	}
	if n <= 0 {
		return nil
	}
	out := make([]HistoryEntry, n)
	start := h.head - n
	if start < 0 {
		start += len(h.ring)
	}
	for i := 0; i < n; i++ {
		out[i] = h.ring[(start+i)%len(h.ring)]
	}
	return out
}

// Capacity returns the maximum number of entries the ring can hold.
// Used by the debugger command to print "of N capacity" in the
// help / list output.
func (h *History) Capacity() int { return len(h.ring) }

// Len returns the current number of stored entries.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.full {
		return len(h.ring)
	}
	return h.count
}

// Clear empties the ring. Useful for debugger-side resets without
// reallocating.
func (h *History) Clear() {
	h.mu.Lock()
	h.head = 0
	h.count = 0
	h.full = false
	h.mu.Unlock()
}

// PackIFFIM is the packing helper for HistoryEntry.IFFIM. Kept
// public so push sites in cmd/zx_go can build entries without
// duplicating the bit layout.
func PackIFFIM(iff1, iff2, halted bool, im int) byte {
	var b byte
	if iff1 {
		b |= 0x80
	}
	if iff2 {
		b |= 0x40
	}
	if halted {
		b |= 0x20
	}
	b |= byte(im) & 0x03
	return b
}
