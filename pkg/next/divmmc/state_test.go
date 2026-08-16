package divmmc

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

var _ machinestate.Device = (*Pager)(nil)

// The divMMC pager is the device that proves why a rewind needs more than the
// guest-visible registers.
//
// Port $E3 is the only thing a guest can read back, and it says nothing about
// the two pieces of state that decide what the next M1 fetch reads: the
// automap-held latch, and the ARM that precedes it. A delayed_on entry point
// latches on one M1 and only maps the overlay on the NEXT one, so a machine
// captured between those two fetches is mid-operation in a way no port
// read-back describes. Restore the ports alone and the trap resumes in the
// wrong phase: the overlay either never appears or appears one instruction
// early, and the esxDOS handler runs from the wrong map.
//
// These tests are written as a replay property rather than a field-by-field
// comparison, because a field-by-field test only checks the fields someone
// remembered to add.

// statePrimed returns a pager in a mid-operation state that no port read-back
// could reconstruct.
//
// The capture point is between the arm and the map: $0038 is a rom3_delayed_on
// entry point here (NR$B9 bit clear, NR$BA bit clear — the variant NextZXOS
// runs its IM1 handler through), so the last fetch has latched the overlay and
// the ROM3 selection riding with it without mapping either. Both take effect on
// the next M1.
//
// MAPRAM is latched by a write that also carried a bank selection, then a
// second write drops the bit while the sticky latch stays. After that, lastE3
// no longer says whether MAPRAM is on — a capture that stored only the port
// register would restore a pager serving ROM where this one serves RAM bank 3.
func statePrimed() *Pager {
	p := New(makeROM())
	p.SetAutomap(true)
	p.SetEntryPoints0(0x83)       // NR$B8: traps at $0000/$0008/$0038
	p.SetEntryPoints1(0xCD)       // NR$BB: $3DXX, $1FFx page-out, $0562/$04C6, $0066 delayed
	p.SetEntryPointsValid0(0x02)  // NR$B9: RST $08 takes the divMMC-ROM path, RST $38 the ROM3 one
	p.SetEntryPointsTiming0(0x00) // NR$BA: every RST entry point is delayed_on
	p.SetStubProtected(true)

	// Distinctive contents per bank AND per offset, so a restore that lands on
	// the wrong bank reads a different byte rather than the same zero.
	for b := 0; b < NumBanks; b++ {
		bank := p.RAMBank(b)
		for off := range bank {
			bank[off] = byte(b*0x11 + off)
		}
	}

	p.WritePort(0x00E3, 0x45) // MAPRAM latched, bank 5
	p.WritePort(0x00E3, 0x05) // MAPRAM bit dropped; the latch is sticky and stays

	p.AssertNMIButton() // a divMMC NMI is pending, arming the $0066 entry point

	p.Step(0x0100) // an ordinary fetch, so lastM1PC is not a trigger address
	p.Step(0x0038) // rom3_delayed_on: ARMED, not yet mapped
	return p
}

// statePrimedMapped is the same session one M1 later: the arm has become the
// automap-held latch and the overlay is mapped.
//
// Capturing here is what covers the latch itself. The armed fixture cannot:
// the drive ends with automap off, which drops the latch, so a capture taken
// with the latch already clear leaves nothing for a forgotten restore to get
// wrong. Held-with-automap-off is not a state the hardware can reach, so the
// two have to be separate capture points.
func statePrimedMapped() *Pager {
	p := statePrimed()
	p.Step(0x0120)            // the armed entry point takes effect on this fetch
	p.WritePort(0x00E3, 0x02) // and the session moves to bank 2
	return p
}

// stateSnap records everything the rest of the machine can see of the pager at
// one moment: what each window serves, the $E3 read-back, the paging decode
// and the NMI suppression line.
func stateSnap(p *Pager, log *[]string, tag string) {
	for _, a := range []uint16{0x0000, 0x0007, 0x1FFF, 0x2000, 0x2009, 0x3FFF, 0x4000} {
		v, ok := p.HandleRead(a)
		*log = append(*log, fmt.Sprintf("%s read %04X = %02X %v", tag, a, v, ok))
	}
	e3, _ := p.ReadPort(0x00E3)
	*log = append(*log, fmt.Sprintf("%s e3=%02X in=%v rom3=%v nmiOff=%v low=%+v high=%+v",
		tag, e3, p.IsPagedIn(), p.IsRom3(), p.DisableNMI(),
		p.DecodePaging(0x0000), p.DecodePaging(0x2000)))
}

// stateDrive runs one full esxDOS-shaped session forward and returns everything
// observable along the way. It is a pure function of the pager's state, so two
// runs from the same state must produce the same trace.
//
// It ends by driving the pager into a state that shares no field value with
// either capture point. That is what gives the replay property its teeth: a
// restore that forgets a field leaves the hostile value in place, and the
// second run diverges from the first.
func stateDrive(p *Pager) []string {
	var log []string
	p.SetPageLogger(func(event string, pc uint16) {
		log = append(log, fmt.Sprintf("event %s @%04X", event, pc))
	})

	// Before anything is written: the bank selector and the sticky MAPRAM bit
	// as the capture left them. Driving a port write first would clobber $E3
	// before a single observation had depended on it, and the capture of the
	// register would then be untestable.
	stateSnap(p, &log, "entry")

	// CONMEM before any M1 fetch. The pager stamps the transition with the PC
	// of the last M1 it saw, which is state carried across the capture.
	p.WritePort(0x00E3, 0x85) // CONMEM on, bank 5 (bit 6 clear: the latch is untouched)
	stateSnap(p, &log, "conmem")
	p.WritePort(0x00E3, 0x05) // CONMEM off again
	stateSnap(p, &log, "conmem-off")

	// The armed delayed_on entry point maps the overlay on this fetch.
	p.Step(0x0120)
	stateSnap(p, &log, "armed-m1")
	p.PostStep(0x0120)

	// Writes through the high window, including the shadow-protected stub byte.
	p.WritePort(0x00E3, 0x01) // bank 1
	log = append(log, fmt.Sprintf("write 2009 handled=%v", p.HandleWrite(0x2009, 0x3C)))
	log = append(log, fmt.Sprintf("write 2010 handled=%v", p.HandleWrite(0x2010, 0x3C)))
	stateSnap(p, &log, "bank1")

	// MAPRAM serves RAM bank 3 in the low window; the NR$09 escape clears the
	// latch and the divMMC ROM comes back.
	p.WritePort(0x00E3, 0x05) // back to bank 5
	stateSnap(p, &log, "mapram")
	p.ClearMAPRAM()
	stateSnap(p, &log, "mapram-off")

	// Out through the $1FFx off-area, back in through the NMI vector — which
	// fires only while the divMMC NMI latch is set — and out again on RETN.
	p.PostStep(0x1FFC)
	stateSnap(p, &log, "offarea")
	p.Step(0x0066)
	stateSnap(p, &log, "nmi")
	p.HandleRETN()
	stateSnap(p, &log, "retn")

	// RST $38: NR$B9 bit clear, so this is the ROM3 variant, delayed.
	p.Step(0x0038)
	stateSnap(p, &log, "rst38-armed")
	p.Step(0x0300)
	stateSnap(p, &log, "rst38-mapped")
	p.HandleRETN()

	// RST $08: NR$B9 bit set, so this is the divMMC-ROM variant, delayed.
	p.Step(0x0008)
	p.Step(0x0301)
	stateSnap(p, &log, "rst08-mapped")

	// Leave the pager holding a different value in every captured field.
	p.SetEntryPoints0(0x00)
	p.SetEntryPoints1(0x00)
	p.SetEntryPointsValid0(0x00)
	p.SetEntryPointsTiming0(0xFF)
	p.SetStubProtected(false)
	p.SetEnabled(false)
	p.SetAutomap(false)       // drops the latch and any pending arm
	p.WritePort(0x00E3, 0x0A) // bank 10, CONMEM clear, MAPRAM bit clear
	p.WriteRAM(3*BankSize, 0xEE)
	p.WriteRAM(5*BankSize, 0xEE)
	p.WriteROMByte(0x0000, 0x00)
	p.WriteROMByte(0x0007, 0x00)
	p.WriteROMByte(0x1FFF, 0x00)
	p.Step(0x0000) // automap is off, so this only moves lastM1PC
	stateSnap(p, &log, "hostile")

	p.SetPageLogger(nil)
	return log
}

// The property that matters: from a captured state, the pager must serve the
// same bytes and take the same paging transitions it took the first time.
func TestReplayingFromCapturedStateReproducesTheSameBusTraffic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *Pager
	}{
		{"armed", statePrimed},        // between the arm and the map
		{"mapped", statePrimedMapped}, // overlay held, mid-session
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.build()

			st := p.SaveState()
			first := stateDrive(p)

			if err := p.LoadState(st); err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			second := stateDrive(p)

			if a, b := strings.Join(first, "\n"), strings.Join(second, "\n"); a != b {
				t.Errorf("the pager behaved differently on replay from the same captured "+
					"state: some part of it is not being captured\n%s", firstDiff(first, second))
			}
		})
	}
}

// The negative that gives the test above its teeth. Port $E3 and the RAM are
// what a snapshot format would store; this proves the trace depends on more
// than those, so the replay test is measuring something.
func TestPortStateAloneIsNotEnoughToReproduceTheTraffic(t *testing.T) {
	p := statePrimed()
	st := p.SaveState()
	want := stateDrive(p)

	// Rebuild a pager from what the guest can read back, the way a snapshot
	// format does: the ROM image, the RAM banks and port $E3.
	q := New(makeROM())
	q.SetAutomap(true)
	for b := 0; b < NumBanks; b++ {
		copy(q.RAMBank(b), p.RAMBank(b))
	}
	e3, _ := p.ReadPort(0x00E3)
	q.WritePort(0x00E3, e3)
	got := stateDrive(q)

	if strings.Join(want, "\n") == strings.Join(got, "\n") {
		t.Fatal("a port-only rebuild reproduced the trace, so this drive does not " +
			"exercise the hidden state and the replay test above proves nothing")
	}

	// ...and the full capture does what the ports alone could not.
	if err := p.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if a, b := strings.Join(want, "\n"), strings.Join(stateDrive(p), "\n"); a != b {
		t.Error("full state capture failed to reproduce the traffic it was taken from")
	}
}

// A capture must not alias the live pager. The registry copies the blob too,
// but a device handing back a view of its own RAM or ROM is a bug worth
// catching here.
func TestCaptureIsIndependentOfLaterChanges(t *testing.T) {
	p := statePrimed()
	st := p.SaveState()

	ramBefore := p.ReadRAM(5*BankSize + 0x40)
	romBefore := p.ReadROMByte(0x40)
	p.RAMBank(5)[0x40] = ramBefore ^ 0xFF
	p.WriteROMByte(0x40, romBefore^0xFF)
	p.WritePort(0x00E3, 0x0C)
	p.Step(0x0038)

	if err := p.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := p.ReadRAM(5*BankSize + 0x40); got != ramBefore {
		t.Errorf("divMMC RAM bank 5 $0040 = %#02x after restore, want %#02x", got, ramBefore)
	}
	if got := p.ReadROMByte(0x40); got != romBefore {
		t.Errorf("divMMC ROM $0040 = %#02x after restore, want %#02x", got, romBefore)
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New(nil).StateID(); got != "next.divmmc" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift",
			got, "next.divmmc")
	}
}

// A malformed blob must be reported and must leave the pager exactly as it
// was. A half-applied restore runs, which is the failure mode that does not
// announce itself.
func TestLoadStateRejectsMalformedStateWithoutApplyingIt(t *testing.T) {
	valid := statePrimed().SaveState()

	shortBanks := func() []byte {
		s := pagerState{RAM: make([][]byte, NumBanks)}
		for i := range s.RAM {
			s.RAM[i] = make([]byte, BankSize)
		}
		s.RAM[7] = s.RAM[7][:BankSize-1]
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(s); err != nil {
			t.Fatalf("encoding the short-bank fixture: %v", err)
		}
		return buf.Bytes()
	}()
	fewBanks := func() []byte {
		s := pagerState{RAM: make([][]byte, NumBanks-1)}
		for i := range s.RAM {
			s.RAM[i] = make([]byte, BankSize)
		}
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(s); err != nil {
			t.Fatalf("encoding the missing-bank fixture: %v", err)
		}
		return buf.Bytes()
	}()

	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"rubbish", []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{"truncated", valid[:len(valid)/2]},
		{"a bank of the wrong size", shortBanks},
		{"too few banks", fewBanks},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := statePrimed()
			before := p.SaveState()

			if err := p.LoadState(tc.blob); err == nil {
				t.Fatal("a malformed state blob must be reported, not half-applied")
			}
			if !bytes.Equal(before, p.SaveState()) {
				t.Error("the pager changed while refusing a malformed state: the restore " +
					"applied part of it")
			}
		})
	}
}

// firstDiff names the first line two traces disagree on, so a failure points at
// the transition that diverged rather than at the whole session.
func firstDiff(a, b []string) string {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return fmt.Sprintf("first divergence at step %d:\n  before: %s\n   after: %s", i, a[i], b[i])
		}
	}
	return fmt.Sprintf("traces are %d and %d steps long", len(a), len(b))
}
