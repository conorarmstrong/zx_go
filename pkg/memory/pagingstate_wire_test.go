package memory

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// A PagingState is a private snapshot: a peripheral takes one when it seizes
// the machine and hands it back when it lets go. When the machine state is
// captured mid-seizure — a rewind landing inside a Multiface NMI session — the
// peripheral has to carry that snapshot into its own capture, and it cannot
// reach the fields to do so.
//
// The property is the one that matters at the far end: encode a snapshot,
// decode it into a fresh one, restore it, and the machine's paging must be back
// where the snapshot was taken. A field lost in the encoding restores a paging
// configuration the machine was never in.
func TestPagingStateSurvivesEncoding(t *testing.T) {
	m, err := New(t.TempDir(), roms.ModelPlus3)
	if err != nil {
		t.Fatalf("memory.New(ModelPlus3): %v", err)
	}

	// A configuration that uses every part of the snapshot: ROM select, screen
	// page 7, a RAM bank, and special paging on via $1FFD bit 0.
	m.PageMemory(0x1C)
	m.PageMemoryPlus3(0x05)
	want7, want1, wantSpecial := m.GetPortState()
	wantRd, wantWr := m.GetPageMap()
	wantScreen, wantEnabled := m.ScreenPage, m.PagingEnabled

	blob, err := m.SavePagingState().MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	// Move the machine somewhere else entirely, including locking paging.
	m.PageMemoryPlus3(0x00)
	m.PageMemory(0x22)

	var got PagingState
	if err := got.UnmarshalBinary(blob); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	m.RestorePagingState(&got)

	p7, p1, special := m.GetPortState()
	rd, wr := m.GetPageMap()
	if p7 != want7 || p1 != want1 || special != wantSpecial {
		t.Errorf("ports after restore = %02X/%02X special=%v, want %02X/%02X special=%v",
			p7, p1, special, want7, want1, wantSpecial)
	}
	if rd != wantRd || wr != wantWr {
		t.Errorf("page map after restore = %v/%v, want %v/%v", rd, wr, wantRd, wantWr)
	}
	if m.ScreenPage != wantScreen {
		t.Errorf("screen page after restore = %d, want %d", m.ScreenPage, wantScreen)
	}
	if m.PagingEnabled != wantEnabled {
		t.Errorf("paging enabled after restore = %v, want %v", m.PagingEnabled, wantEnabled)
	}
}

// The valid flag has to survive too, or a decoded snapshot restores nothing and
// does it silently: RestorePagingState ignores an invalid state by design.
func TestDecodedPagingStateActuallyRestores(t *testing.T) {
	m, err := New(t.TempDir(), roms.Model128K)
	if err != nil {
		t.Fatalf("memory.New(Model128K): %v", err)
	}
	m.PageMemory(0x14)
	blob, err := m.SavePagingState().MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	m.PageMemory(0x02)

	var got PagingState
	if err := got.UnmarshalBinary(blob); err != nil {
		t.Fatalf("UnmarshalBinary: %v", err)
	}
	m.RestorePagingState(&got)

	if p7, _, _ := m.GetPortState(); p7 != 0x14 {
		t.Errorf("$7FFD after restoring a decoded snapshot = %02X, want 14", p7)
	}
}

func TestUnmarshalPagingStateRejectsRubbish(t *testing.T) {
	var p PagingState
	if err := p.UnmarshalBinary([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed snapshot must be reported, not half-applied")
	}
}
