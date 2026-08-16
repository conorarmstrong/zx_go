package multiface

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The Multiface is a machine-state device.
var _ machinestate.Device = (*Multiface)(nil)

// The Multiface is not a register file, and a capture that thought it was would
// restore a machine that runs and then corrupts itself.
//
// What decides what happens next is where in an NMI session the unit is: the
// window paged in, the red button latched, the software lockout, whether a
// session is open at all, and — the one with teeth — the snapshot of the host
// paging taken when the window first appeared. That snapshot is what the OUT
// that re-arms the hardware puts back. Restore a machine mid-session without
// it and the session ends by restoring nothing, leaving the host running on
// whatever paging the MF ROM left behind. That is the shape of the 128K BASIC
// launch failure.
//
// So these tests are a replay property: drive the unit through a scripted
// session, record what the rest of the machine can see, restore, drive the
// same script again, and require the same observations. A field-by-field
// comparison would only check the fields someone remembered to add.

// busRead mirrors how pkg/peripherals routes memory while the Multiface window
// is up: $0000-$1FFF is the MF ROM and $2000-$3FFF the MF RAM. It is the only
// way the CPU sees the paged-in flag and the RAM contents, so the trace uses it
// rather than reading the fields.
func busRead(mf *Multiface, addr uint16) (byte, bool) {
	if !mf.IsROMPaged() {
		return 0, false
	}
	switch {
	case addr < 0x2000:
		return mf.GetROM()[addr], true
	case addr < 0x4000:
		return mf.GetRAM()[addr-0x2000], true
	}
	return 0, false
}

func busWrite(mf *Multiface, addr uint16, val byte) bool {
	if !mf.IsROMPaged() || addr < 0x2000 || addr >= 0x4000 {
		return false
	}
	mf.GetRAM()[addr-0x2000] = val
	return true
}

// MF128 and MF3 share the port decode (port & 0x0072 == 0x0032) but invert the
// meaning of A7 on IN: A7=1 pages the window in on MF1/128 and out on MF3.
const (
	mf128PageIn  = 0x00B2 // A7=1
	mf128PageOut = 0x0032 // A7=0
	mf3PageIn    = 0x0032 // A7=0
	mf3PageOut   = 0x00B2 // A7=1
)

// newMF128 builds a 128K machine with a Multiface 128 on it. The ROM is a
// dummy of a known fill so a bus read has something identifiable to return.
func newMF128(t *testing.T) (*Multiface, *memory.Memory) {
	t.Helper()
	dir := t.TempDir()
	writeDummyMFROM(t, dir, "mf128_official.rom", 0xA5)

	mem, err := memory.New(dir, roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New(Model128K): %v", err)
	}
	mf, err := NewMultiface(Multiface128, dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface(Multiface128): %v", err)
	}
	return mf, mem
}

// primedSession returns a Multiface part-way through an NMI session: window in,
// red button latched, lockout off, a session open with the host's pre-NMI
// paging held in its snapshot, RAM written, and the live host paging already
// moved on from the snapshot — which is what makes the snapshot observable.
func primedSession(t *testing.T) (*Multiface, *memory.Memory) {
	t.Helper()
	mf, mem := newMF128(t)

	// Host paging before the red button: ROM 1 at $0000, RAM bank 4 at $C000.
	mem.PageMemory(0x14)

	// Red button, then the CPU fetching the NMI vector.
	mf.HandleNMI()
	mf.HandleOpcodeRead(NMIVector1)

	// The MF ROM has written its working bytes over MF RAM. Every byte is
	// seeded, and seeded with something that varies: the addresses the script
	// touches must hold values a fresh unit would not have, or a dropped RAM
	// restore reads the same zero either way and the mutation passes silently.
	for i, ram := 0, mf.GetRAM(); i < len(ram); i++ {
		ram[i] = byte(i*7 + 3)
	}

	// The MF ROM swaps to ROM 0 while it runs. Paging is frozen for the
	// session, so only the ROM-select bits take effect — but $7FFD and the
	// ROM mapping now differ from the snapshot taken at page-in, so a lost
	// snapshot shows up the moment the session ends.
	mem.PageMemory(0x03)
	return mf, mem
}

// hostPaging is the part of the machine that is NOT this device: a rewind
// restores every device, so the harness puts the host paging back with
// memory's own snapshot API before replaying. Without this the second run
// would start from wherever the first run left the host.
type hostPaging struct {
	paging *memory.PagingState
	frozen bool
}

func saveHost(mem *memory.Memory) hostPaging {
	return hostPaging{paging: mem.SavePagingState(), frozen: mem.PagingFrozen}
}

func (h hostPaging) restore(mem *memory.Memory) {
	mem.RestorePagingState(h.paging)
	mem.PagingFrozen = h.frozen
}

// drive runs a fixed script of everything the rest of the machine does to a
// Multiface — the CPU fetching at the NMI vector, IN/OUT on the MF port, reads
// and writes through the paged-in window, the MF ROM's own paging writes, a
// second red button press — and records only what is observable from outside
// the device: handler return values, what the bus returns, and what the host
// paging ends up as. Nothing here reads a field of Multiface directly.
func drive(mf *Multiface, mem *memory.Memory, pageIn, pageOut uint16) string {
	var b strings.Builder
	step := 0

	snap := func(label string) {
		p7, p1, sp := mem.GetPortState()
		rd, wr := mem.GetPageMap()
		rom, romOK := busRead(mf, 0x0000)
		ram, ramOK := busRead(mf, 0x2100)
		fmt.Fprintf(&b, "%02d %-9s bus rom=%02X,%v ram=%02X,%v | host 7ffd=%02X 1ffd=%02X special=%v rd=%v wr=%v screen=%d paging=%v\n",
			step, label, rom, romOK, ram, ramOK, p7, p1, sp, rd, wr, mem.ScreenPage, mem.PagingEnabled)
		step++
	}
	// bump is a read-modify-write of MF RAM: the value written depends on the
	// value already there, so RAM restored one byte wrong diverges and stays
	// diverged.
	bump := func(addr uint16) {
		v, ok := busRead(mf, addr)
		fmt.Fprintf(&b, "%02d bump      %04X read %02X,%v", step, addr, v, ok)
		step++
		if ok {
			wrote := busWrite(mf, addr, v+0x11)
			after, _ := busRead(mf, addr)
			fmt.Fprintf(&b, " wrote=%v now %02X", wrote, after)
		}
		b.WriteByte('\n')
	}
	rec := func(label string, vals ...any) {
		fmt.Fprintf(&b, "%02d %-9s %v\n", step, label, vals)
		step++
	}

	snap("start")

	// The CPU fetches at $0066 with the session live.
	rec("fetch66", mf.HandleOpcodeRead(NMIVector1))
	bump(0x2100)
	bump(0x3FCC)

	// An IN takes the window away and another brings it back. The MF ROM does
	// this around every print to the host screen.
	v, ok := mf.HandlePortRead(pageOut)
	rec("in-out", v, ok)
	snap("paged-out")
	bump(0x2100)
	v, ok = mf.HandlePortRead(pageIn)
	rec("in-in", v, ok)
	snap("paged-in")

	// The MF ROM selects a different ROM mid-session; paging is frozen, so
	// only the ROM-select bits apply.
	mem.PageMemory(0x0D)
	snap("mf-rom-sel")

	// OUT re-arms the hardware: the session ends and the host paging captured
	// at page-in must come back. A7 means the same thing on an OUT for both
	// variants — it is the software lockout — so these two are addressed
	// directly rather than through the IN polarity.
	rec("out-armed", mf.HandlePortWrite(0x00B2, 0x00))
	snap("after-out")

	// With the session over, a full paging write must take effect again.
	mem.PageMemory(0x02)
	snap("host-pages")

	// Second red button press: a new session over the new host paging.
	rec("nmi2", mf.HandleNMI())
	rec("fetch67", mf.HandleOpcodeRead(NMIVector2))
	bump(0x2100)
	mem.PageMemory(0x11)
	snap("session2")

	// OUT with A7=0 also sets the software lockout (J2), on both variants.
	rec("out-lock", mf.HandlePortWrite(0x0032, 0x00))
	snap("after-lock")

	// Locked out, the page-in read must not bring the window back.
	v, ok = mf.HandlePortRead(pageIn)
	rec("in-locked", v, ok)
	snap("locked")

	// Disabled, the unit is off the bus entirely.
	mf.SetEnabled(false)
	v, ok = mf.HandlePortRead(pageIn)
	rec("in-off", v, ok)
	snap("disabled")

	return b.String()
}

// firstDifference reports the first line where two traces diverge, so a failure
// names the step rather than dumping two blocks of text.
func firstDifference(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) && i < len(lb); i++ {
		if la[i] != lb[i] {
			return fmt.Sprintf("first run:  %s\nsecond run: %s", la[i], lb[i])
		}
	}
	return fmt.Sprintf("traces differ in length: %d vs %d lines", len(la), len(lb))
}

// The property that matters: from a captured state, the unit must do what it
// did the first time.
func TestReplayingFromCapturedStateReproducesTheSameSession(t *testing.T) {
	mf, mem := primedSession(t)

	st := mf.SaveState()
	host := saveHost(mem)

	first := drive(mf, mem, mf128PageIn, mf128PageOut)

	if err := mf.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	host.restore(mem)

	second := drive(mf, mem, mf128PageIn, mf128PageOut)

	if first != second {
		t.Errorf("the Multiface behaved differently on replay from the same captured state: "+
			"some part of the unit is not being captured\n%s", firstDifference(first, second))
	}
}

// The same property on a +3, where the host paging the session is holding has
// parts a 128K machine does not: $1FFD and the special-paging RAM layout. The
// MF ROM moves $1FFD while it runs (frozen paging still latches the register
// for ROM selection), so the snapshot and the live value differ and the restore
// at the end of the session is visible in $1FFD as well as $7FFD.
//
// The 128K test above is the binding one for the session flags; this one is
// about the snapshot being carried whole.
func TestReplayingAPlus3SessionReproducesTheHostPaging(t *testing.T) {
	dir := t.TempDir()
	writeDummyMFROM(t, dir, "mf3_official.rom", 0x5C)

	mem, err := memory.New(dir, roms.ModelPlus3)
	if err != nil {
		t.Fatalf("memory.New(ModelPlus3): %v", err)
	}
	mf, err := NewMultiface(Multiface3, dir, mem)
	if err != nil {
		t.Fatalf("NewMultiface(Multiface3): %v", err)
	}

	// Host paging before the red button: special paging config 2, ROM select
	// from $1FFD bit 1, screen page 7.
	mem.PageMemory(0x1C)
	mem.PageMemoryPlus3(0x05)

	mf.HandleNMI()
	mf.HandleOpcodeRead(NMIVector1)
	for i, ram := 0, mf.GetRAM(); i < len(ram); i++ {
		ram[i] = byte(i*11 + 5)
	}
	// The MF ROM moves $1FFD mid-session; the snapshot still holds $05.
	mem.PageMemoryPlus3(0x02)

	st := mf.SaveState()
	host := saveHost(mem)

	first := drive(mf, mem, mf3PageIn, mf3PageOut)

	if err := mf.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	host.restore(mem)

	second := drive(mf, mem, mf3PageIn, mf3PageOut)

	if first != second {
		t.Errorf("the Multiface 3 behaved differently on replay from the same captured state\n%s",
			firstDifference(first, second))
	}
}

// The negative that gives the test above its teeth. A snapshot format would
// store the four guest-visible bits — paged in, red button, lockout, enabled —
// and call that the Multiface. This proves the script actually depends on the
// session and on the host paging the session is holding, so the replay test is
// measuring something.
func TestTheVisibleFlagsAloneDoNotReproduceTheSession(t *testing.T) {
	mf, mem := primedSession(t)
	want := drive(mf, mem, mf128PageIn, mf128PageOut)

	// Same machine, same host paging, same RAM, same four flags — but no open
	// session and no snapshot of the host's pre-NMI paging.
	mf2, mem2 := newMF128(t)
	mem2.PageMemory(0x14)
	mem2.PagingFrozen = true
	mem2.PageMemory(0x03)
	copy(mf2.GetRAM(), mf.GetRAM())
	mf2.enabled, mf2.romPaged, mf2.redButton, mf2.invisible = true, true, true, false

	if got := drive(mf2, mem2, mf128PageIn, mf128PageOut); got == want {
		t.Error("a flags-only restore reproduced the session exactly, so the replay test " +
			"is not exercising the session state it exists to protect")
	}
}

// A capture must not alias the live unit — most obviously its 8K of RAM, which
// SaveState must not hand back a view of.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	mf, mem := primedSession(t)
	st := mf.SaveState()

	before, _ := busRead(mf, 0x2100)
	busWrite(mf, 0x2100, before^0xFF)
	mf.HandlePortWrite(mf128PageIn, 0x00) // end the session, moving the host paging
	mem.PageMemory(0x02)

	if err := mf.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got, _ := busRead(mf, 0x2100); got != before {
		t.Errorf("MF RAM $2100 = %#02x after restore, want %#02x", got, before)
	}
}

func TestStateIDIsStable(t *testing.T) {
	mf, _ := newMF128(t)
	if got := mf.StateID(); got != "multiface" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "multiface")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	mf, _ := primedSession(t)
	before := mf.SaveState()

	if err := mf.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
	if !bytes.Equal(before, mf.SaveState()) {
		t.Error("a rejected state blob changed the unit: the restore must be all or nothing")
	}
}

// A blob that decodes but carries the wrong amount of RAM is not this unit's
// state. Taking the bytes it does carry would leave the rest of the RAM holding
// present-day values.
func TestLoadStateRejectsWrongSizedRAM(t *testing.T) {
	mf, _ := primedSession(t)
	before := mf.SaveState()

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(multifaceState{RAM: make([]byte, 16), Enabled: true}); err != nil {
		t.Fatalf("encoding the short-RAM blob: %v", err)
	}
	if err := mf.LoadState(buf.Bytes()); err == nil {
		t.Error("a state carrying 16 bytes of RAM must be refused")
	}
	if !bytes.Equal(before, mf.SaveState()) {
		t.Error("a rejected state blob changed the unit: the restore must be all or nothing")
	}
}
