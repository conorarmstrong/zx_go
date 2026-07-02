package sam

import (
	"image"
	"os"

	"github.com/conorarmstrong/zx_go/pkg/saa1099"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// CyclesPerFrame is the SAM frame length in CPU T-states: 6 MHz / ~50.08 Hz =
// 384 cycles/line × 312 lines.
const CyclesPerFrame = 119808

const (
	// samFrameIntTstate is the frame-relative T-state at which the maskable
	// frame interrupt fires — the end of the 192-line display region (line
	// 68+192 = 260 × 384). The exact value only matters for raster-precise
	// effects; the ROM services the INT once per frame wherever it lands.
	samFrameIntTstate = 99840
	// samIntActiveCycles is how long an interrupt is held active in the STATUS
	// register.
	samIntActiveCycles = 128
)

// Machine is a SAM Coupé: the shared Z80 core driving the SAM memory map, the
// I/O/ASIC ports, the key matrix and the SAM interrupt model. It implements
// z80.ULA (ReadPort/WritePort) itself, the way pkg/zx8x's Machine does.
type Machine struct {
	Mem *Memory
	CPU *z80.CPU
	Kbd *Keyboard
	SAA *saa1099.SAA
	FDC [2]*WD1772 // drive 1 (ports 0xE0-E7), drive 2 (ports 0xF0-F7)

	border byte     // last BORDER write (colour + MIC + BEEP + SOFF)
	clut   [16]byte // CLUT palette registers (7-bit indices)
	line   byte     // line-interrupt target line (>=192 = disabled)
	lpen   byte     // ASIC light-pen horizontal register (IN 0x0F8)
	hpen   byte     // ASIC light-pen line register (IN 0x1F8)

	// frameStart is the CPU T-state count at the start of the current frame,
	// captured in RunFrame so the STATUS register can report interrupt lines
	// relative to the frame.
	frameStart uint64

	// frameCount increments once per frame; the renderer derives the MODE 1/2
	// FLASH phase from it (toggles every 16 frames).
	frameCount uint64

	// frame is the line-accurate display buffer; renderCursor is the next scan
	// line still to render this frame (the lazy renderer's cursor).
	frame        *image.RGBA
	renderCursor int

	audioScratch []int16 // reused stereo scratch for GenerateAudioMono
}

// New builds a SAM machine from the two 16 KB ROM halves. z80.New resets the
// CPU (PC=$0000, ROM0 paged in) and the SAM frame interrupt is armed.
func New(rom0, rom1 []byte) *Machine {
	m := &Machine{
		Mem:  NewMemory(rom0, rom1),
		Kbd:  NewKeyboard(),
		SAA:  saa1099.New(),
		FDC:  [2]*WD1772{NewWD1772(), NewWD1772()},
		line: 0xFF, // line interrupt disabled
	}
	m.CPU = z80.New(m.Mem, m)
	// Maskable frame interrupt as a narrow pulse (the SAM ASIC pulses int_ula
	// once per frame, held ~128 cycles), reusing the shared Z80 timing hooks.
	m.CPU.IntAssertTstate = samFrameIntTstate
	m.CPU.IntPulseTstates = samIntActiveCycles
	// Line-accurate renderer: a write to displayed video memory flushes the
	// raster up to the current line before the memory changes.
	m.frame = image.NewRGBA(image.Rect(0, 0, samTotalWidth, samTotalHeight))
	m.Mem.SetVideoWriteHook(m.flushRaster)
	// ASIC memory + I/O contention (z80.New wired ContendMemory via the
	// SetTStatePtr/ContendMemory interfaces); MemContend gates the memory side.
	m.CPU.MemContend = true
	if os.Getenv("ZX_GO_SAM_NO_CONTENTION") != "" {
		m.Mem.SetContentionEnabled(false)
	}
	return m
}

// NewFromROM validates + splits a raw 32 KB SAM ROM image and builds the machine.
func NewFromROM(romImage []byte) (*Machine, error) {
	rom0, rom1, err := SplitROM(romImage)
	if err != nil {
		return nil, err
	}
	return New(rom0, rom1), nil
}

// RunFrame executes one 50 Hz SAM frame.
func (m *Machine) RunFrame() {
	m.frameStart = m.CPU.Tstates()
	m.renderCursor = 0
	m.CPU.ExecuteFrame(CyclesPerFrame)
	m.flushTo(samTotalHeight) // render any lines after the last state change
	m.frameCount++
	m.Kbd.Tick() // advance any typed-character symbol pulse
}

// Reset performs a cold reset: the Z80 restarts at $0000 with ROM0 paged in and
// the ASIC paging/registers, border, line interrupt and raster state return to
// power-on. Installed RAM is left for the boot ROM to re-initialise.
func (m *Machine) Reset() {
	m.CPU.Reset()
	m.Mem.Reset()
	m.border = 0
	m.line = 0xFF
	m.CPU.LineIntOffsetTstates = 0
	m.lpen, m.hpen = 0, 0
	m.frameStart = 0
	m.frameCount = 0
	m.renderCursor = 0
}

// InsertDisk loads a disk image into drive 1 (drive 0) or drive 2 (drive 1).
func (m *Machine) InsertDisk(drive int, d *Disk) {
	if drive == 0 || drive == 1 {
		m.FDC[drive].InsertDisk(d)
	}
}

// BorderColour returns the current 4-bit border CLUT index (BORDER bits map
// ((x&0x20)>>2)|(x&7)). The renderer resolves it through the CLUT.
func (m *Machine) BorderColour() byte { return (m.border&0x20)>>2 | (m.border & 0x07) }

// CLUT returns the 16 palette registers (7-bit master-palette indices).
func (m *Machine) CLUT() [16]byte { return m.clut }
