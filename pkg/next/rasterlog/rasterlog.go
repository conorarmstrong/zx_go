// Package rasterlog records mid-frame changes to the Spectrum Next's visual
// state against the display row on which they happened, so a compositor that
// runs at the end of a frame can still reproduce them row by row.
//
// The Next layers are composited once, after the frame has executed, reading
// whatever the NextRegs hold at that point. That is fine for the Copper,
// which is stepped inside the compositor's row loop and so writes at the
// right moment, but a CPU write partway down the screen applied retroactively
// to the entire frame. Raster splits driven from an interrupt handler or a
// raster-wait loop therefore did not reproduce.
//
// A Log fixes that without a per-scanline renderer: each recorded change
// carries the undo and redo needed to move the state between "as it was at
// the start of the frame" and "as the guest left it". The compositor rewinds
// once, then walks down the screen applying each change as it reaches the row
// it belongs to, and finally applies the remainder so the next frame starts
// from the live state.
//
// Changes are recorded as closures rather than as register writes on purpose:
// replaying raw NextReg writes through the dispatcher would re-trigger their
// side effects (palette auto-increment, sprite uploads, MMU paging). A
// recorder captures the resolved effect instead.
package rasterlog

const (
	// RowBeforeDisplay marks a change made before the first display row (top
	// border or vblank). It is in force from row 0.
	RowBeforeDisplay = -1
	// RowAfterDisplay marks a change made after the last display row (bottom
	// border). It is not on screen this frame, but still has to be applied
	// before the frame ends so the live state is correct.
	RowAfterDisplay = 192
)

// MaxEntries bounds one frame's journal. A frame that writes more visual
// state than this is not doing anything a replay can represent usefully, and
// the cap keeps a pathological or runaway guest from growing the log without
// limit. Well above what real software uses: a per-scanline effect touching
// several registers a line needs only a few hundred.
const MaxEntries = 8192

// Log is a frame's worth of recorded visual changes, in the order they were
// made. Not safe for concurrent use; it is written from the emulation
// goroutine and replayed on the same one.
type Log struct {
	// row reports the display row currently being drawn, or RowBeforeDisplay
	// / RowAfterDisplay outside the active area.
	row func() int

	entries []entry
	// cursor is how far ApplyThrough has replayed, so a walk down the screen
	// stays linear rather than rescanning from the start for every row.
	cursor int
	// frame identifies the frame being recorded. Entries only ever describe
	// the CURRENT frame: when it changes, whatever was recorded for the
	// previous one is discarded.
	frame     func() uint64
	lastFrame uint64
	haveFrame bool

	// replaying suppresses recording while undo/redo run. Those closures
	// usually go back through the very setter whose observer recorded them,
	// so without this the log would journal its own replay and grow every
	// frame (or recurse).
	replaying bool
}

type entry struct {
	row  int
	undo func()
	redo func()
}

// New returns a Log that timestamps each change using row, with no frame
// scoping. Prefer NewWithFrame: without a frame source the log is only ever
// cleared by EndReplay, so any path that runs frames WITHOUT rendering piles
// entries up indefinitely.
func New(row func() int) *Log { return &Log{row: row} }

// NewWithFrame returns a Log that timestamps each change using row and scopes
// its entries to the frame reported by frame.
//
// The scoping matters more than it looks. The compositor clears the log in
// EndReplay, but plenty of paths execute frames without ever rendering one —
// headless stepping, fast tape turbo, the debugger, RZX playback. Without a
// frame source those writes accumulate forever: measured on a real Next boot
// the log reached 113,766 entries and was still climbing, which leaks memory
// and, worse, means the next rewind undoes state from thousands of frames
// ago. A frame that is never rendered has no journal worth keeping — its
// writes are already live — so they are dropped as soon as the frame moves on.
func NewWithFrame(row func() int, frame func() uint64) *Log {
	return &Log{row: row, frame: frame}
}

// Record captures a change that has just been made: undo restores the value
// it replaced, redo re-applies it. Both are retained for the rest of the
// frame, so they must not close over anything that changes underneath them.
//
// Safe on a nil Log, which lets callers stay unconditional on the hot path.
func (l *Log) Record(undo, redo func()) {
	if l == nil || l.replaying {
		return
	}
	if l.frame != nil {
		f := l.frame()
		if !l.haveFrame || f != l.lastFrame {
			// A new frame: anything held for the previous one was never
			// rendered, so it describes a frame nobody will replay.
			l.entries = l.entries[:0]
			l.cursor = 0
			l.lastFrame, l.haveFrame = f, true
		}
	}
	if len(l.entries) >= MaxEntries {
		return
	}
	r := RowBeforeDisplay
	if l.row != nil {
		r = l.row()
	}
	l.entries = append(l.entries, entry{row: r, undo: undo, redo: redo})
}

// BeginReplay rewinds to the frame-start state and opens the replay window.
//
// Recording is suppressed for the whole window, not just for the undo and
// redo closures: the compositor steps the Copper inside its row loop, and the
// Copper's own NextReg writes land at the right row by construction. Without
// the wider window they would be journalled too, growing the log by thousands
// of entries a frame for a Copper-heavy program.
func (l *Log) BeginReplay() {
	if l == nil {
		return
	}
	l.replaying = true
	for i := len(l.entries) - 1; i >= 0; i-- {
		l.entries[i].undo()
	}
	l.cursor = 0
}

// EndReplay applies whatever is left — including changes made below the last
// display row — drops the frame's entries and closes the window. After it the
// state is where the guest left it and recording resumes.
func (l *Log) EndReplay() {
	if l == nil {
		return
	}
	l.ApplyThrough(RowAfterDisplay)
	l.entries = l.entries[:0]
	l.cursor = 0
	l.replaying = false
}

// ApplyThrough re-applies every change recorded at or above row, in the order
// they were made. Rows must be visited in ascending order after a Rewind.
func (l *Log) ApplyThrough(row int) {
	if l == nil {
		return
	}
	for l.cursor < len(l.entries) && l.entries[l.cursor].row <= row {
		l.entries[l.cursor].redo()
		l.cursor++
	}
}

// Replaying reports whether a rewind or replay is in progress. Observers can
// use it to skip bookkeeping that only makes sense for real guest writes.
func (l *Log) Replaying() bool { return l != nil && l.replaying }

// Len reports how many changes are recorded. Zero on a nil Log.
func (l *Log) Len() int {
	if l == nil {
		return 0
	}
	return len(l.entries)
}
