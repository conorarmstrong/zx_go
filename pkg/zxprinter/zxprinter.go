// Package zxprinter implements the Sinclair ZX Printer — the 1-bit
// thermal printer that plugged into the back of the 48K Spectrum.
// It's accessed via port 0xFB (only A2 is decoded, so any port
// with bit 2 clear matches):
//
//	OUT (0xFB), a:
//	  bit 7 = stylus (1 = darken / burn the current pixel)
//	  bit 2 = motor off (1 = stop; 0 = run)
//	  bit 1 = slow feed (1 = slow speed; 0 = fast speed)
//
// Port address decode: only A2 is checked, so any port with A2=0
// matches (0xFB is canonical; 0xF3, 0xFA, etc. also match).
//
//	IN (0xFB):
//	  bit 0 = drum has advanced past the last pixel written
//	  bit 6 = stylus-drum sync pulse (low during the
//	          sync window at the start of each line)
//	  bit 7 = 0 (for ROM read-back sanity)
//
// The ROM's printer driver polls the IN port in a tight loop, uses
// the drum-advance bit to know when to write the next pixel, and
// writes the stylus bit via OUT for each of 256 pixels per line.
// A line takes (440 * 384) = 168960 T-states at the lowest speed,
// one CPU frame's worth of cycles plus a bit — so the printer is
// the slowest peripheral on the Spectrum.
//
// Printed rows accumulate in a Go image buffer which the UI can
// save as a PNG whenever the user wants.
package zxprinter

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"sync"
)

// LineWidth is the fixed pixel width of a ZX Printer line. Every
// line is exactly 256 pixels wide; the drum advances 384 pixel
// positions per line, with the extra 128 being the return gap.
const LineWidth = 256

// TstatesPerFrame is the 48K Spectrum's frame length. The printer
// uses this to convert frame+cycle deltas into absolute T-state
// counts. This constant lives here (rather than being passed in)
// so the port model stays self-contained.
const TstatesPerFrame = 69888

// Printer is a ZX Printer instance. Safe for concurrent use: the
// emulation goroutine calls Read/Write/Frame, the UI goroutine
// calls Bitmap/Save. A single mutex guards all mutable state.
type Printer struct {
	mu sync.Mutex

	// speed is 0 when the motor is off, else the drum speed (1 or
	// 2). The ROM can change speed mid-print to adjust line
	// thickness; we defer new-speed to newSpeed until the next
	// line start.
	speed    int
	newSpeed int

	// Drum state at the moment the motor was (re-)started.
	// Everything is measured as deltas from these anchors.
	frameAt    int64
	tstatesAt  int64
	pixel      int  // fill cursor — pixels 0..pixel are finalized (-1 = empty)
	ackPos     int  // drum X at time of last OUT; bit 0 goes high when drum > ackPos
	stylusDark bool // current stylus state (darken next pixel?)

	// frames is the running frame counter, bumped by Frame().
	frames int64

	// line is the row currently being assembled — one byte per
	// pixel, 0 = white, 255 = black. Flushed to rows when pixel
	// reaches LineWidth.
	line [LineWidth]byte

	// rows is the list of completed rows, in print order. Grows
	// as the ROM prints. Bitmap() and Save() render these to an
	// image. Callers can Clear() to drop accumulated output.
	rows [][LineWidth]byte
}

// New constructs an idle printer. The motor is off, no rows are
// buffered. Attach via PeripheralManager.EnableZXPrinter.
func New() *Printer {
	return &Printer{pixel: -1, ackPos: -1}
}

// Reset clears transient drum state but KEEPS accumulated rows — a
// system reset should not throw away what's already been printed.
func (p *Printer) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.speed = 0
	p.newSpeed = 0
	p.pixel = -1
	p.ackPos = -1
	p.stylusDark = false
}

// Frame increments the internal frame counter. Called by the
// emulator once per emulator frame (50 Hz) — used for the
// T-state-to-drum-position math in Read/Write.
func (p *Printer) Frame() {
	p.mu.Lock()
	p.frames++
	p.mu.Unlock()
}

// HandlePortRead handles IN (0xFB). Returns the current drum /
// stylus status bits the ROM polls for. The port decode is loose
// — any port with bit 2 clear matches (the ZX Printer's own
// decoder only looks at A2), so we mirror that to stay compatible
// with software that hits non-canonical addresses.
//
// tstates is the CPU's current absolute T-state counter.
//
// Reads also advance the drum: the ROM polls this port in a tight
// loop while waiting for the drum to reach the next line's sync
// point, and during that window there are no writes. If we only
// advanced the drum on writes, the in-progress line would never
// flush. So we do the full drum-advance-and-wrap dance here too.
func (p *Printer) HandlePortRead(port uint16, tstates int64) (byte, bool) {
	if port&0x04 != 0 {
		return 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.speed == 0 {
		// Motor off — ROM reads this as "drum idle".
		return 0x3e, true
	}

	x := p.advance(tstates)
	ans := byte(0x3e)
	// Stylus-sync window at the very start of each line (before
	// the visible area): return the sync bit set so the ROM can
	// calibrate. Also light bit 7 when the stylus is darkening.
	if (x > -10 && x < 0) || p.stylusDark {
		ans = 0xbe
	}
	if x > p.ackPos {
		ans |= 0x01 // drum has advanced past last stylus write
	}
	return ans, true
}

// advance moves the drum forward to the position implied by the
// current T-state, filling pixels with the current stylus state
// and wrapping lines as needed. Returns the post-advance drum X
// (which may be in the sync area, x < 0, if a wrap just happened).
// Must be called with p.mu held and only when p.speed != 0.
//
// This function is called from BOTH HandlePortRead and
// HandlePortWrite because the ROM polls the IN port in a tight
// loop while waiting for line sync, during which there are no
// writes — so the only way the in-progress line ever gets flushed
// is if reads drive the wrap detection too.
func (p *Printer) advance(tstates int64) int {
	x := p.drumPos(tstates)
	p.fillPixelsTo(x)
	for x >= 320 {
		cpp := 440 / p.speed
		p.tstatesAt += int64(cpp * 384)
		if p.tstatesAt >= TstatesPerFrame {
			p.tstatesAt -= TstatesPerFrame
			p.frameAt++
		}
		x -= 384
		if p.newSpeed != 0 {
			p.speed = p.newSpeed
			p.newSpeed = 0
		}
		p.pixel = -1
		p.ackPos = -1
		if x > 0 {
			p.fillPixelsTo(x)
		}
	}
	return x
}

// HandlePortWrite handles OUT (0xFB). Updates drum state from
// elapsed cycles, writes pixels up to the current drum position,
// and starts a new line when the drum wraps.
func (p *Printer) HandlePortWrite(port uint16, val byte, tstates int64) bool {
	if port&0x04 != 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.speed == 0 {
		// Motor currently off. Writing with bit 2 clear turns
		// the motor on. Bits 0-1 pick the speed: bit 1 set = 1
		// (slow), else 2 (fast).
		if val&0x04 == 0 {
			if val&0x02 != 0 {
				p.speed = 1
			} else {
				p.speed = 2
			}
			p.frameAt = p.frames
			p.tstatesAt = tstates
			p.stylusDark = val&0x80 != 0
			p.pixel = -1
			p.ackPos = -1
		}
		return true
	}

	// Motor already running — apply pixels up to the current drum
	// position with the current stylus state, then pick up the
	// new stylus bit for future pixels.
	x := p.advance(tstates)

	// Bit 2 set = motor off request. Pad out the remaining
	// pixels on the current line with the last stylus state,
	// flush, and park the motor. Skip when the line is empty
	// (p.pixel == -1, fresh after a wrap): nothing to flush.
	if val&0x04 != 0 {
		if p.pixel >= 0 {
			p.fillPixelsTo(LineWidth - 1)
		}
		p.speed = 0
		p.pixel = -1
		p.ackPos = -1
	} else {
		p.stylusDark = val&0x80 != 0
		// Acknowledge the drum-advance signal — bit 0 of IN
		// won't go high again until the drum passes x.
		p.ackPos = x
		// Speed bit: before the visible line has started (x<0,
		// still in the sync window) there's no in-progress line to
		// protect, so the new speed applies immediately. Once the
		// line is under way it's queued in newSpeed and picked up
		// by advance() at the next line wrap — the drum is
		// mechanical and can't retime a line already spinning.
		requested := 2
		if val&0x02 != 0 {
			requested = 1
		}
		if x < 0 {
			p.speed = requested
		} else if requested != p.speed {
			p.newSpeed = requested
		} else {
			p.newSpeed = 0
		}
	}
	return true
}

// drumPos returns the current drum X coordinate (in pixels)
// relative to the visible line start. Ranges from -64 (before the
// line starts, in the sync area) through 0..255 (visible pixels)
// to 320+ (gap / next line).
//
// Must be called with p.mu held.
func (p *Printer) drumPos(tstates int64) int {
	frame := p.frames - p.frameAt
	if frame > 400 {
		frame = 400
	}
	cycles := tstates - p.tstatesAt + frame*TstatesPerFrame
	cpp := int64(440 / p.speed)
	return int(cycles/cpp) - 64
}

// fillPixelsTo writes the current stylus state into all pixels
// from p.pixel+1 through min(x, LineWidth-1), advances the fill
// cursor p.pixel, and flushes the line the first time it fills
// pixel LineWidth-1. After flush, p.pixel is set to LineWidth as
// a "line is done, awaiting wrap" sentinel; subsequent fills on
// the same line are no-ops until advance's wrap handler resets
// p.pixel = -1 for the next line.
// Must be called with p.mu held.
func (p *Printer) fillPixelsTo(x int) {
	if p.pixel >= LineWidth {
		return // line already flushed, waiting for wrap
	}
	start := p.pixel + 1
	if start < 0 {
		start = 0
	}
	end := x + 1
	if end > LineWidth {
		end = LineWidth
	}
	if end <= start {
		return
	}
	for i := start; i < end; i++ {
		if p.stylusDark {
			p.line[i] = 255
		}
	}
	p.pixel = end - 1
	if p.pixel == LineWidth-1 {
		p.flushLine()
		p.pixel = LineWidth // await wrap
	}
}

// flushLine copies the in-progress line into rows and resets it.
// Must be called with p.mu held.
func (p *Printer) flushLine() {
	var row [LineWidth]byte
	copy(row[:], p.line[:])
	p.rows = append(p.rows, row)
	p.line = [LineWidth]byte{}
}

// Bitmap renders all accumulated rows as a greyscale image. Each
// pixel is either 0xFF (dark, burned by the stylus) or 0x00
// (white). Returns an empty image when nothing has been printed.
func (p *Printer) Bitmap() *image.Gray {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.rows) == 0 {
		return image.NewGray(image.Rect(0, 0, LineWidth, 1))
	}
	img := image.NewGray(image.Rect(0, 0, LineWidth, len(p.rows)))
	for y, row := range p.rows {
		for x := 0; x < LineWidth; x++ {
			img.SetGray(x, y, color.Gray{Y: 255 - row[x]})
		}
	}
	return img
}

// Rows returns the number of complete printed rows.
func (p *Printer) Rows() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.rows)
}

// Clear drops all accumulated rows. The motor state is unaffected
// — an active print job continues on a fresh page.
func (p *Printer) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows = nil
	p.line = [LineWidth]byte{}
}

// Save writes the accumulated rows to a PNG file. Returns an error
// if nothing has been printed yet or the file can't be written.
func (p *Printer) Save(path string) error {
	img := p.Bitmap()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}
