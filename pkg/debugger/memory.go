package debugger

import "github.com/conorarmstrong/zx_go/pkg/roms"

// Memory is the address space the debugger inspects.
//
// It is an interface rather than *memory.Memory because not every machine
// zx_go emulates has one. The SAM Coupé's RAM lives behind its own paging
// model, and cmd/zx_go installs a stand-in *memory.Memory on that machine
// so the Spectrum-shaped menus have something non-nil to talk to. The
// debugger reading that stand-in showed a blank 48K Spectrum whatever the
// SAM was doing, and a poke through it reported success and changed
// nothing.
//
// *memory.Memory satisfies this directly; the SAM adapter lives with the
// machine that needs it.
type Memory interface {
	Read(addr uint16) byte
	Write(addr uint16, val byte)

	// GetCurrentModel names the machine, which selects how the page map
	// is labelled.
	GetCurrentModel() roms.SpectrumModel

	// GetPageMap returns the read and write maps for the four 16K slots
	// at $0000/$4000/$8000/$C000. Values >= 16 are ROM pages.
	GetPageMap() ([4]int, [4]int)

	// GetPortState returns the machine's two paging latches and whether
	// special paging is active. On a Spectrum these are $7FFD and $1FFD.
	GetPortState() (byte, byte, bool)

	// ScreenPageIndex is the RAM page the display is being read from.
	ScreenPageIndex() int
}
