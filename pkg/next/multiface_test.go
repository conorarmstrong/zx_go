package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

func newMFFixture(t *testing.T, mode byte) (*Multiface, *memory.Memory) {
	t.Helper()
	mem, err := memory.New("", roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	return NewMultiface(mem, func() byte { return mode }), mem
}

// The red button raises nmi_active; the memory pages in on the $0066 fetch
// (multiface.vhd:169-186), not before.
func TestMultifacePagesInOnTheNMIVectorFetch(t *testing.T) {
	mf, mem := newMFFixture(t, 0)
	mf.Button()
	if mem.MultifaceActive() {
		t.Error("paged in before the $0066 fetch")
	}
	if !mf.MultifaceActive() {
		t.Error("mf_is_active is low with the NMI held")
	}
	mf.Fetch(0x0066)
	if !mem.MultifaceActive() {
		t.Error("the $0066 fetch did not page the Multiface in")
	}
}

// In +3 mode the enable port is $3F and the disable port $BF
// (zxnext.vhd:2612-2613). Reading the disable port pages the memory out;
// reading the enable port pages it back in while the unit is visible
// (multiface.vhd:179-182). Writing the enable port makes it invisible
// (:159), after which an enable read no longer pages it in.
func TestMultifacePlus3Ports(t *testing.T) {
	mf, mem := newMFFixture(t, 0)
	mf.Button()
	mf.Fetch(0x0066)
	mf.Port(0x00BF, false)
	if mem.MultifaceActive() {
		t.Fatal("a read of $BF left the Multiface paged in")
	}
	if !mf.Port(0x7F3F, false) {
		t.Error("an enable read while visible did not answer with the paging register")
	}
	if !mem.MultifaceActive() {
		t.Fatal("a read of $3F did not page the Multiface back in")
	}
	mf.Port(0x003F, true) // invisible
	mf.Port(0x00BF, false)
	if mf.Port(0x7F3F, false) || mem.MultifaceActive() {
		t.Error("an invisible Multiface answered or paged in on an enable read")
	}
}

// The 128 mode decodes $BF to enable and $3F to disable; mf_type 1x uses
// $9F and $1F.
func TestMultifacePortDecodeFollowsTheMode(t *testing.T) {
	for _, c := range []struct {
		mode     byte
		dis, ena uint16
	}{{1, 0x3F, 0xBF}, {2, 0x1F, 0x9F}} {
		mf, mem := newMFFixture(t, c.mode)
		mf.Button()
		mf.Fetch(0x0066)
		mf.Port(c.dis, false)
		if mem.MultifaceActive() {
			t.Errorf("mode %d: reading $%02X did not page out", c.mode, c.dis)
		}
		mf.Port(c.ena, false)
		if !mem.MultifaceActive() {
			t.Errorf("mode %d: reading $%02X did not page in", c.mode, c.ena)
		}
	}
}

// RETN ends the session (multiface.vhd:144,179) and a reset clears it (:126-175).
func TestMultifaceRETNAndReset(t *testing.T) {
	mf, mem := newMFFixture(t, 0)
	mf.Button()
	mf.Fetch(0x0066)
	mf.SetMultifaceActive(false) // the RETN input
	if mem.MultifaceActive() || mf.MultifaceActive() {
		t.Error("RETN left the Multiface active")
	}
	mf.Button()
	mf.Fetch(0x0066)
	mf.Reset()
	if mem.MultifaceActive() || mf.MultifaceActive() {
		t.Error("reset left the Multiface active")
	}
}
