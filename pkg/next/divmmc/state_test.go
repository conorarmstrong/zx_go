package divmmc

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"reflect"
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

// The replay-traffic property above only observes a field when the drive
// happens to make it differ between the capture and the restore: a field left
// where it already was is already correct after a deleted restore, so the trace
// cannot tell a restored field from a forgotten one. A mutation audit found
// that hole in the ROM restore — every fixture above holds the image at 8 KB,
// so the branch that reallocates a resized image could be deleted with the
// whole suite still green.
//
// So the binding tests below stop asserting on behaviour and assert on the
// capture itself: drive the pager so every captured field changes, restore, and
// re-capture. If any field is not restored, the second blob differs from the
// first. That observes all seventeen fields rather than the subset the bus
// happens to expose, and the guard that follows keeps the claim honest by
// failing if any field stops being moved.

// decodeStateForTest reads a capture back as the struct it was made from, so a
// test can say which fields moved rather than only whether the blob changed.
func decodeStateForTest(t *testing.T, b []byte) pagerState {
	t.Helper()
	var s pagerState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}

// fieldsMoved names the captured fields two captures disagree on.
//
// It walks pagerState by reflection rather than by a hand-written list, so a
// field added to the capture and forgotten here does not quietly go uncovered:
// it shows up as a field no drive sequence moves, and the guard fails.
func fieldsMoved(t *testing.T, before, after []byte) map[string]bool {
	t.Helper()
	b := reflect.ValueOf(decodeStateForTest(t, before))
	a := reflect.ValueOf(decodeStateForTest(t, after))
	moved := map[string]bool{}
	for i := 0; i < b.NumField(); i++ {
		if !reflect.DeepEqual(b.Field(i).Interface(), a.Field(i).Interface()) {
			moved[b.Type().Field(i).Name] = true
		}
	}
	return moved
}

// stateCapturePoints are the fixtures the round trip and the guard share.
//
// Two are needed because the automap's arm and its held latch are mutually
// exclusive by construction, not by accident: Step only arms a delayed entry
// point while `!pagedIn && !pendingPageIn`, and pageOut clears pagedIn, rom3,
// pendingPageIn and pendingRom3 together. So no single pager can be captured
// with the overlay held AND an arm outstanding, and the fields each capture
// point can move are complementary.
var stateCapturePoints = []struct {
	name  string
	build func() *Pager
	// cannotMove are the fields this capture point leaves where the drive
	// sequence also leaves them, with the reason. Every one of them is moved
	// by the other capture point.
	cannotMove map[string]string
}{
	{
		name:  "armed",
		build: statePrimed,
		cannotMove: map[string]string{
			"PagedIn": "the arm has not taken effect yet, so the overlay is out here, " +
				"and the drive ends with automap off, which also leaves it out",
			"Rom3": "rom3 is only assigned when a page-in takes effect; this capture is " +
				"one M1 short of that, and the drive's last page-in is the divMMC-ROM variant",
		},
	},
	{
		name:  "mapped",
		build: statePrimedMapped,
		cannotMove: map[string]string{
			"PendingPageIn": "an arm cannot outlive the page-in that consumed it, and Step " +
				"refuses to arm again while the overlay is held, so it is false here and " +
				"false after the drive",
		},
	},
}

func TestEveryCapturedFieldSurvivesARoundTrip(t *testing.T) {
	for _, tc := range stateCapturePoints {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.build()
			want := p.SaveState()

			stateDrive(p)
			if bytes.Equal(p.SaveState(), want) {
				t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
			}

			if err := p.LoadState(want); err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			if got := p.SaveState(); !bytes.Equal(got, want) {
				t.Error("re-capturing after a restore did not reproduce the captured state: " +
					"some field is captured but not restored")
			}
		})
	}
}

// The guard the round trip depends on. If a later change retunes the drive
// sequence so a field stops differing, the round trip silently stops covering
// it, and the score stays at 100% while measuring less.
//
// It is stricter than a per-field list of "must move": every field must move
// under every capture point unless that point names it, with a reason, in
// cannotMove — and a named field that starts moving again fails too, so an
// exemption cannot outlive the constraint that justified it.
func TestTheDriveSequenceChangesEveryCapturedField(t *testing.T) {
	covered := map[string]bool{}

	for _, tc := range stateCapturePoints {
		p := tc.build()
		before := p.SaveState()
		stateDrive(p)
		moved := fieldsMoved(t, before, p.SaveState())

		for name, why := range tc.cannotMove {
			if moved[name] {
				t.Errorf("%s: %s now moves, so it no longer needs its exemption (%q) — "+
					"delete the exemption so the coverage claim stays honest", tc.name, name, why)
			}
		}
		for _, name := range stateFieldNames() {
			if moved[name] {
				covered[name] = true
				continue
			}
			if _, exempt := tc.cannotMove[name]; !exempt {
				t.Errorf("%s: %s is unchanged by the drive sequence, so a lost restore of it "+
					"would go undetected", tc.name, name)
			}
		}
	}

	for _, name := range stateFieldNames() {
		if !covered[name] {
			t.Errorf("%s is moved by no capture point, so nothing here would catch a lost "+
				"restore of it", name)
		}
	}
}

// stateFieldNames is every field a capture carries, read off the wire struct so
// adding a field to the capture without covering it fails the guard.
func stateFieldNames() []string {
	t := reflect.TypeOf(pagerState{})
	out := make([]string, t.NumField())
	for i := range out {
		out[i] = t.Field(i).Name
	}
	return out
}

// stateROMAbsent is the capture point for the other half of the ROM restore:
// the one where the image changes LENGTH.
//
// New(nil) is the install-time path — no enNxtmmc.rom in roms/next/, so the
// pager starts with no ROM image at all and boot.bin streams it off the SD card
// through WriteROMByte, which grows the buffer to 8 KB on the first write. Every
// other fixture here holds the length at 8 KB throughout and so only ever
// exercises the copy-in-place branch; a capture taken before the stream has to
// shrink the image back on restore, which is the branch that replaces the slice.
func stateROMAbsent() *Pager {
	p := New(nil)
	p.SetAutomap(true)
	p.Step(0x0100)
	return p
}

func TestARoundTripFromACaptureTakenBeforeTheROMArrived(t *testing.T) {
	p := stateROMAbsent()
	want := p.SaveState()
	if n := len(decodeStateForTest(t, want).ROM); n != 0 {
		t.Fatalf("the fixture captured a %d-byte ROM image, so it does not start from "+
			"the no-image state this test exists for", n)
	}

	// boot.bin streaming enNxtmmc.rom in, which allocates the image.
	for i, b := range []byte{0x18, 0x2A, 0xC9, 0xF3} {
		p.WriteROMByte(i, b)
	}
	if n := len(decodeStateForTest(t, p.SaveState()).ROM); n != ROMSize {
		t.Fatalf("streaming the ROM in left a %d-byte image, want %d: the restore branch "+
			"that reallocates the image is not being exercised", n, ROMSize)
	}

	if err := p.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := p.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after restoring a capture taken before the ROM arrived did " +
			"not reproduce it: the pager kept an image the captured machine did not have")
	}
	if got := p.ReadROMByte(0x00); got != 0xFF {
		t.Errorf("divMMC ROM $0000 = %#02x after restoring a capture with no image, want "+
			"%#02x (an absent image reads as floating high)", got, 0xFF)
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

// The reallocating restore branch has two directions, and only one of them was
// covered. The test above goes from an 8 KB image back to none, where the copy
// into the new slice moves zero bytes and so proves nothing about it. This goes
// the other way: back to a capture that HAS an image, from a pager whose image
// is a different length. If the contents are not copied, the pager comes back
// with the right-sized ROM full of zeroes and the guest executes nothing.
//
// Reachable exactly as written: streaming a ROM in grows an absent image to
// 8 KB, and restoring a capture taken before it arrived shrinks it again, so a
// machine can genuinely arrive at this branch with a longer image to restore.
func TestARoundTripBackToACaptureThatHasAROMImage(t *testing.T) {
	p := New(makeROM())
	withROM := p.SaveState()
	if n := len(decodeStateForTest(t, withROM).ROM); n != ROMSize {
		t.Fatalf("the fixture captured a %d-byte image, want %d", n, ROMSize)
	}

	// Shrink the live image by restoring a capture taken before one existed.
	if err := p.LoadState(stateROMAbsent().SaveState()); err != nil {
		t.Fatalf("LoadState (no image): %v", err)
	}
	if got := p.ReadROMByte(0x10); got != 0xFF {
		t.Fatalf("the image did not shrink, so the branch under test is not reached: "+
			"$0010 = %#02x", got)
	}

	if err := p.LoadState(withROM); err != nil {
		t.Fatalf("LoadState (with image): %v", err)
	}
	want := makeROM()
	for _, addr := range []int{0x0000, 0x0010, 0x1FFF} {
		if got := p.ReadROMByte(addr); got != want[addr] {
			t.Errorf("divMMC ROM $%04X = %#02x after restore, want %#02x: the reallocated "+
				"image was never filled in", addr, got, want[addr])
		}
	}
}
