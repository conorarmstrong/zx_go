package memory

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The memory system is where a partial capture hides best.
//
// Everyone remembers the RAM. What decides what the CPU actually reads is the
// mapping laid over it: two 16K page maps, eight 8K MMU slots each with a
// "the MMU wrote this last" flag, and the overlays stacked above both (FPGA
// bootrom, config-mode window, Multiface, Alt-ROM, Layer-2 mapping, Beta).
// Restore the pool and the paging ports alone and the machine still runs — it
// just runs with the right bytes behind the wrong window, which shows up as a
// game that loads and then jumps into a bank that was not there before.
//
// These tests are a replay property, not a field-by-field comparison: the
// device is driven forward from a captured point twice and the two transcripts
// of what the rest of the machine saw must be identical. A field-by-field test
// only checks the fields someone remembered to add.

// stateProbeAddrs is what the transcript samples after every operation: two
// addresses in each 8K MMU slot, and both sides of the boundaries where the
// Next overlays change hands ($1FFF/$2000 splits the Multiface ROM from its
// RAM, $3FFF/$4000 ends the bottom-16K window).
var stateProbeAddrs = [...]uint16{
	0x0000, 0x0123, 0x1FFF, 0x2000, 0x2ABC, 0x3FFF,
	0x4000, 0x5B00, 0x7FFE, 0x8000, 0x9ABC, 0xBFFF,
	0xC000, 0xD555, 0xE000, 0xFFFF,
}

// fillDistinctPattern gives every backing store its own byte pattern, so a
// read through the wrong bank cannot accidentally return the right value.
// Cold RAM is zero-filled, and two zeroed banks are indistinguishable — a
// fixture left that way would let a dropped MMU slot restore pass.
func fillDistinctPattern(m *Memory) {
	for b := range m.ram {
		if m.ram[b] == nil {
			continue
		}
		for i := range m.ram[b] {
			m.ram[b][i] = byte(b*31 + i*7 + 1)
		}
	}
	for r := range m.rom {
		for i := range m.rom[r] {
			m.rom[r][i] = byte(0xA0 + r*17 + i*3)
		}
	}
	for i := range m.altROM0 {
		m.altROM0[i] = byte(0x40 + i*5)
		m.altROM1[i] = byte(0x80 + i*5)
	}
	for i := range m.mfRAM {
		m.mfRAM[i] = byte(0xC0 + i*11)
	}
}

// primedNextMemory is a Next mid-flight: every overlay image installed, every
// backing store carrying its own pattern, and the paging already moved off its
// power-on values by a run of the driver.
func primedNextMemory(t *testing.T, ts *uint64) *Memory {
	t.Helper()
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}

	// The overlay images are hardware, not state: SaveState does not capture
	// them (see state.go), exactly as a real rewind does not re-seat the
	// cartridges. Both runs therefore see the same installed images.
	boot := make([]byte, 0x2000)
	for i := range boot {
		boot[i] = byte(0x10 + i*13)
	}
	m.SetFPGABootROM(boot)
	mf := make([]byte, 0x2000)
	for i := range mf {
		mf[i] = byte(0x20 + i*19)
	}
	m.SetMultifaceROM(mf)

	fillDistinctPattern(m)
	m.SetTStatePtr(ts)
	driveMemory(m, ts, 0x1234, 200)
	return m
}

// primedPlus3Memory is the classic counterpart: +3 timing (its own contention
// pattern), +3 special paging, and a Beta Disk interface fitted.
func primedPlus3Memory(t *testing.T, ts *uint64) *Memory {
	t.Helper()
	m, err := New("", roms.ModelPlus3)
	if err != nil {
		t.Fatalf("memory.New(ModelPlus3): %v", err)
	}
	fillDistinctPattern(m)
	m.SetTStatePtr(ts)
	driveMemory(m, ts, 0x51EE, 200)
	return m
}

// driveMemory runs a fixed sequence of guest-visible operations and returns a
// transcript of everything the rest of the machine can see afterwards: the
// bytes the CPU reads, the T-states a contended access costs it, the display
// page handed to the ULA, and the NextReg / port read-backs.
//
// Operations that only exist on the Next ($DFFD, NextReg $8E / $8C / $50-$57,
// the config-mode window, Layer-2 mapping, the Multiface and bootrom overlays)
// are driven only on a Next, so the classic run stays a faithful +3.
func driveMemory(m *Memory, ts *uint64, seed uint32, steps int) []byte {
	next := m.currentModel == roms.ModelNext
	nops := uint32(6)
	if next {
		nops = 16
	}

	rng := seed | 1
	rnd := func() uint32 {
		rng ^= rng << 13
		rng ^= rng >> 17
		rng ^= rng << 5
		return rng
	}

	betaImage := make([]byte, PageSize)
	for i := range betaImage {
		betaImage[i] = byte(0xB0 + i*23)
	}
	cfgPages := [...]byte{0x00, 0x01, 0x02, 0x03, 0x06, 0x07, 0x10, 0x2A, 0x7F}

	out := make([]byte, 0, steps*48)
	*ts = 0

	for step := 0; step < steps; step++ {
		v := byte(rnd())
		a := stateProbeAddrs[rnd()%uint32(len(stateProbeAddrs))]

		switch rnd() % nops {
		case 0:
			// Bit 5 locks paging for good, so it is left to case 2 rather
			// than fired at random early in the run.
			m.PageMemory(v &^ 0x20)
		case 1:
			m.PageMemoryPlus3(v)
		case 2:
			switch v & 0x03 {
			case 0:
				m.PageMemory(v | 0x20) // lock paging
			case 1:
				m.ResetPaging() // and the reset line unlocks it again
			}
		case 3:
			switch v & 0x03 {
			case 0:
				m.EnableBeta(betaImage)
			case 1:
				m.DisableBeta()
			case 2:
				m.BetaPreFetch(0x3D00 | uint16(v))
			case 3:
				m.BetaPreFetch(0x4000 | uint16(v))
			}
		case 4:
			m.RAMContentionDisabled = v&0x01 != 0
			m.PagingFrozen = v&0x02 != 0
		case 5:
			m.SetBetaActive(v&0x01 != 0)
		case 6:
			m.SetMMU(byte(rnd()%8), v)
		case 7:
			m.SetAltROMReg(v)
		case 8:
			m.SetLayer2MapControl(v)
			m.SetLayer2ActiveBank(byte(rnd()))
			m.SetLayer2ShadowBank(byte(rnd()))
		case 9:
			m.SetConfigModeRAMPage(cfgPages[rnd()%uint32(len(cfgPages))])
			if v&0x01 != 0 {
				m.EnterConfigMode()
			} else {
				m.ClearConfigMode()
			}
		case 10:
			m.SetMultifaceActive(v&0x01 != 0)
		case 11:
			if v&0x01 != 0 {
				m.RearmFPGABootROM()
			} else {
				m.ClearFPGABootROM()
			}
		case 12:
			m.SetDFFD(v)
		case 13:
			m.SetROMBankExtended(v)
		case 14:
			m.SetEFF7(v)
		case 15:
			m.SetROMBank(int(v & 0x03))
		}

		// Every step writes, so the pool, the ROM banks, the Alt-ROM buffers
		// and the Multiface RAM all keep moving underneath the capture. The
		// value is step-derived, so a byte captured at step N differs from the
		// same byte after the run — which is what makes a dropped content
		// restore visible in the very first sample of the replay.
		m.Write(a, byte(step*7+13))
		m.Write(a^0x0001, byte(step*11+29))

		for _, p := range stateProbeAddrs {
			out = append(out, m.Read(p))
		}
		out = append(out,
			byte(m.ScreenPage), // the page the ULA is told to fetch
			m.EFF7Value(),      // port $EFF7 read-back
			m.ROMMappingNR8E(), // NextReg $8E read-back
			m.AltROMReg(),      // NextReg $8C read-back
			m.DFFDValue(),      // port $DFFD read-back
		)
		for slot := byte(0); slot < 8; slot++ {
			out = append(out, m.GetMMU(slot)) // NextReg $50-$57 read-back
		}

		// What a contended access costs the CPU, sampled across one eight
		// T-state window of the ULA's pattern. Both the bank paged in and the
		// NextReg $08 contention-disable bit are visible here.
		for k := uint64(0); k < 8; k++ {
			*ts = m.contendStart + k
			before := *ts
			m.ContendMemory(a)
			out = append(out, byte(*ts-before))
			*ts = m.contendStart + k
			before = *ts
			m.ContendMemory(0x4000)
			out = append(out, byte(*ts-before))
		}
	}
	return out
}

// The property that matters: from a captured state, the memory must show the
// machine exactly what it showed the first time.
func TestMemoryReplayFromCapturedStateReproducesWhatTheMachineSees(t *testing.T) {
	var ts uint64
	m := primedNextMemory(t, &ts)

	st := m.SaveState()
	first := driveMemory(m, &ts, 0x9E37, 400)

	if err := m.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveMemory(m, &ts, 0x9E37, 400)

	if !bytes.Equal(first, second) {
		t.Errorf("replay from the same captured state showed the machine something different "+
			"(first divergence at transcript byte %d): some part of the memory system is not being captured",
			firstDiff(first, second))
	}
}

// The same property on a classic machine, where the Beta Disk overlay, +3
// special paging and the +3 contention pattern are the state that moves.
func TestMemoryReplayOnClassicModelReproducesWhatTheMachineSees(t *testing.T) {
	var ts uint64
	m := primedPlus3Memory(t, &ts)

	st := m.SaveState()
	first := driveMemory(m, &ts, 0x77AA, 400)

	if err := m.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	second := driveMemory(m, &ts, 0x77AA, 400)

	if !bytes.Equal(first, second) {
		t.Errorf("replay from the same captured state showed the machine something different "+
			"(first divergence at transcript byte %d): some part of the memory system is not being captured",
			firstDiff(first, second))
	}
}

// The negative that gives the replay tests their teeth. A capture that stored
// only the paging ports would round-trip the ports perfectly and still restore
// a different machine, because the window is not a function of the ports: a
// NextReg $50-$57 override outlives any port write, and replaying the ports on
// restore would clear it. This is the answer to "must a restore re-apply
// paging" — it must not.
func TestMemoryRestoreKeepsAWindowThePortsDoNotDescribe(t *testing.T) {
	var ts uint64
	m := primedNextMemory(t, &ts)

	// Put a bank at $C000-$FFFF that the paging ports do not name: MMU slot 6
	// (NextReg $56) mapped to 8K bank 32, i.e. 16K pool bank 16, while $7FFD
	// selects bank 0. Paging is locked first, so a restore that tried to
	// replay PageMemory(port7FFD) would be refused outright as well — and the
	// NextReg MMU write still lands, because the lock does not gate it.
	m.ClearFPGABootROM()
	m.ClearConfigMode()
	m.ResetPaging()
	m.PageMemory(0x20) // bank 0 at $C000, paging locked from here on
	m.SetMMU(6, 32)
	m.SetMMU(7, 33)

	want := m.Read(0xC000)
	if want == m.ram[0][0] {
		t.Fatalf("fixture is not discriminating: bank 16 and bank 0 hold the same byte at $C000")
	}

	st := m.SaveState()
	driveMemory(m, &ts, 0x0BAD, 200)

	if err := m.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := m.Read(0xC000); got != want {
		t.Errorf("Read($C000) after restore = %#02x, want %#02x: the restored window followed the "+
			"paging ports instead of the captured MMU slots", got, want)
	}
	if !m.IsMMUOverridden(6) || m.GetMMU(6) != 32 {
		t.Errorf("MMU slot 6 after restore = %#02x (override %v), want 32 (override true)",
			m.GetMMU(6), m.IsMMUOverridden(6))
	}
	if m.PagingEnabled {
		t.Error("the paging lock was not restored: a locked machine came back unlocked")
	}
}

// A capture must not alias the live memory. The registry copies the blob too,
// but a device handing back a view of its own 2 MB pool is a bug worth
// catching where it would be made.
func TestMemoryCaptureIsIndependentOfLaterChanges(t *testing.T) {
	var ts uint64
	m := primedNextMemory(t, &ts)
	m.ClearFPGABootROM()
	m.ClearConfigMode()

	before := m.Read(0x8000)
	beforeMMU := m.SnapshotMMU()
	st := m.SaveState()

	m.Write(0x8000, before^0xFF)
	m.SetMMU(4, 0x5A)
	m.SetMMU(5, 0x5B)
	driveMemory(m, &ts, 0xFEED, 100)

	if err := m.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := m.Read(0x8000); got != before {
		t.Errorf("Read($8000) = %#02x after restore, want %#02x: the capture aliased live RAM",
			got, before)
	}
	if got := m.SnapshotMMU(); got != beforeMMU {
		t.Errorf("MMU after restore = %v, want %v: the capture aliased the live slot table",
			got, beforeMMU)
	}
}

func TestMemoryStateIDIsStable(t *testing.T) {
	var ts uint64
	m := primedPlus3Memory(t, &ts)
	if got := m.StateID(); got != "memory" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "memory")
	}
}

func TestMemoryLoadStateRejectsRubbish(t *testing.T) {
	var ts uint64
	m := primedPlus3Memory(t, &ts)
	if err := m.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
}

// A blob that decodes but describes a page map pointing outside the RAM pool
// must be refused before anything is applied — the map indexes the pool
// directly on the next read, so accepting it would turn a bad state into a
// panic several instructions later, in a place with no way back to the cause.
func TestMemoryLoadStateRejectsAnImpossiblePageMapWithoutApplyingAnything(t *testing.T) {
	var ts uint64
	m := primedPlus3Memory(t, &ts)

	st := m.SaveState()
	before := driveMemory(m, &ts, 0x2222, 20)
	if err := m.LoadState(st); err != nil {
		t.Fatalf("LoadState of our own capture: %v", err)
	}

	var s memState
	if err := gob.NewDecoder(bytes.NewReader(st)).Decode(&s); err != nil {
		t.Fatalf("decoding our own state: %v", err)
	}
	s.ReadMap[1] = 999
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s); err != nil {
		t.Fatalf("re-encoding: %v", err)
	}

	if err := m.LoadState(buf.Bytes()); err == nil {
		t.Fatal("a page map pointing outside the RAM pool must be refused")
	}
	// And refused without applying any of it: the machine must still be
	// standing where the good capture left it.
	after := driveMemory(m, &ts, 0x2222, 20)
	if !bytes.Equal(before, after) {
		t.Error("the rejected state was partly applied: the machine no longer replays its own capture")
	}
}

// The replay tests above have a blind spot, and a mutation audit found it: 12
// of the 28 field restores could be deleted with the whole package still
// green. Both of the AY's causes apply here.
//
// Some fields cannot reach the transcript at all. portEFF7Reg2 only ever feeds
// the Pentagon-1024 mapping mode, which this core does not model, so no byte
// the CPU reads can depend on it. Others do reach the transcript, but the
// randomised drive sequence happened to leave them holding the same value at
// capture time and at restore time — an odd number of Multiface page-ins is
// not something a xorshift schedule guarantees — so deleting the restore left
// them already correct.
//
// The fix is the AY's: stop asserting on what the machine sees and assert on
// the capture. Two profiles disagree on every captured field and every backing
// store; capture the first, drive to the second, restore the first, and
// re-capture. A field that is captured and not restored makes the two blobs
// differ, whether or not it reaches a single byte of behaviour.

// nextForRoundTrip is a Next with the overlay images fitted and every backing
// store carrying its own pattern, and nothing else — the profiles below supply
// all of the paging, so that what each field holds is stated rather than
// inherited.
func nextForRoundTrip(t *testing.T, ts *uint64) *Memory {
	t.Helper()
	installtest.RedirectConfig(t)
	m, err := New("", roms.ModelNext)
	if err != nil {
		t.Fatalf("memory.New(ModelNext): %v", err)
	}
	boot := make([]byte, 0x2000)
	for i := range boot {
		boot[i] = byte(0x10 + i*13)
	}
	m.SetFPGABootROM(boot)
	mf := make([]byte, 0x2000)
	for i := range mf {
		mf[i] = byte(0x20 + i*19)
	}
	m.SetMultifaceROM(mf)
	fillDistinctPattern(m)
	m.SetTStatePtr(ts)
	return m
}

// writeThroughEveryBackingStore moves every byte array the capture carries:
// the RAM pool, the four legacy ROM banks, both Alt-ROM buffers and the
// Multiface RAM. Each is reachable only through the overlay that owns it, so
// this pages them in one at a time — and it first normalises the window, so
// that both profiles write the same addresses into the same stores. Without
// that, the second profile would be writing through whatever window the first
// one left behind, and "the ROM banks differ" would rest on paging this test
// is not trying to exercise.
func writeThroughEveryBackingStore(m *Memory, seed int) {
	m.PagingFrozen = false
	m.ClearFPGABootROM()
	m.ClearConfigMode()
	m.SetMultifaceActive(false)
	m.SetAltROMReg(0x00)        // no Alt-ROM write redirect
	m.SetLayer2MapControl(0x00) // no Layer-2 write redirect
	m.ResetPaging()             // unlock paging, clear every MMU override
	m.SetEFF7(0x00)             // drop the bit-3 "RAM bank 0 at $0000" override
	// In and straight back out of special paging: the exit transition is what
	// restores the classic $4000/$8000/$C000 map, and ResetPaging alone does
	// not (it owns slot 3 only), so a profile that had been left in special
	// paging would otherwise still be writing into banks 4/6.
	m.PageMemoryPlus3(0x01)
	m.PageMemoryPlus3(0x00)
	m.PageMemory(0x00)

	// The Multiface owns $2000-$3FFF while it is paged in, above the
	// config-mode window in the write mux.
	m.SetMultifaceActive(true)
	for a := 0x2000; a < 0x2040; a++ {
		m.Write(uint16(a), byte(seed+a*3))
	}
	m.SetMultifaceActive(false)

	// The config-mode window is how the firmware fills the four legacy ROM
	// banks (NR$04 = $00-$03) and the two Alt-ROM buffers ($06, $07).
	m.EnterConfigMode()
	for _, p := range []byte{0x00, 0x01, 0x02, 0x03, 0x06, 0x07} {
		m.SetConfigModeRAMPage(p)
		for a := 0; a < 0x40; a++ {
			m.Write(uint16(a), byte(seed+int(p)*7+a*5))
		}
	}
	m.ClearConfigMode()

	// And the pool, through an ordinary write above $4000.
	for a := 0x4000; a < 0x4040; a++ {
		m.Write(uint16(a), byte(seed+a*11))
	}
}

// primedForRoundTrip is profile A: a Next locked into +3 special paging with
// every overlay paged in, two NextReg MMU overrides standing, and the Beta
// interface fitted and active.
func primedForRoundTrip(t *testing.T, ts *uint64) *Memory {
	t.Helper()
	m := nextForRoundTrip(t, ts)
	writeThroughEveryBackingStore(m, 0x11)

	betaImage := make([]byte, PageSize)
	for i := range betaImage {
		betaImage[i] = byte(0xB0 + i*23)
	}
	m.EnableBeta(betaImage)
	m.SetBetaActive(true)

	m.SetLayer2MapControl(0x49) // write-enable, shadow, segment 1, read-enable off
	m.SetLayer2MapControl(0x15) // bit 4 set: segment offset 5
	m.SetLayer2ActiveBank(0x2A)
	m.SetLayer2ShadowBank(0x35)
	m.SetAltROMReg(0x53)

	// The paging ports go in the order the lock allows: $EFF7 and $DFFD
	// while paging is still enabled, then special paging, and only then the
	// $7FFD write that locks the pager for good.
	m.SetEFF7(0x0C) // bits 2 and 3 both latched
	m.SetDFFD(0x0A)
	m.PageMemoryPlus3(0x03) // special paging, mode 1: banks 4,5,6,7
	m.PageMemory(0x2D)      // shadow screen, and bit 5 locks paging

	// The MMU overrides come after every classic write, because each of those
	// re-syncs the slots it owns and clears their override flags.
	m.SetMMU(2, 0x3C)
	m.SetMMU(5, 0x41)

	m.RAMContentionDisabled = true
	m.PagingFrozen = true
	m.SetMultifaceActive(true)
	m.SetConfigModeRAMPage(0x02)
	m.EnterConfigMode()
	m.RearmFPGABootROM()
	return m
}

// driveEverythingMemory is profile B, and it disagrees with profile A on every
// field a capture carries. Parity is the trap the AY documents: a flag toggled
// an even number of times is back where it started and hides a lost restore
// completely, so nothing here toggles — each flag is driven to the opposite of
// the value profile A left, once.
func driveEverythingMemory(m *Memory) {
	writeThroughEveryBackingStore(m, 0x8A)

	m.DisableBeta()

	m.SetLayer2MapControl(0x84) // read-enable, write-enable and shadow off, segment 2
	m.SetLayer2MapControl(0x12) // bit 4 set: segment offset 2
	m.SetLayer2ActiveBank(0x51)
	m.SetLayer2ShadowBank(0x6E)
	m.SetAltROMReg(0x26)

	m.SetDFFD(0x03)
	m.PageMemory(0x02)      // normal screen, and no lock this time
	m.PageMemoryPlus3(0x04) // normal paging, ROM index high bit set

	m.SetMMU(4, 0x77)

	m.RAMContentionDisabled = false
	m.SetConfigModeRAMPage(0x13)
	// writeThroughEveryBackingStore already left PagingFrozen, the Multiface,
	// config mode and the bootrom clear, which is the opposite of every one of
	// profile A's settings.
}

func TestEveryCapturedMemoryFieldSurvivesARoundTrip(t *testing.T) {
	var ts uint64
	m := primedForRoundTrip(t, &ts)
	want := m.SaveState()

	driveEverythingMemory(m)
	if bytes.Equal(m.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := m.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := m.SaveState(); !bytes.Equal(got, want) {
		t.Errorf("re-capturing after a restore did not reproduce the captured state "+
			"(first divergence at blob byte %d): some field is captured but not restored",
			firstDiff(want, got))
	}
}

// The guard the test above depends on. A mutation score is bounded by what the
// fixture moves, so if a later change retunes either profile until a field
// stops differing, the round trip silently stops covering it — exactly as the
// replay tests had been doing for twelve fields.
func TestTheMemoryDriveSequenceChangesEveryCapturedField(t *testing.T) {
	var ts uint64
	m := primedForRoundTrip(t, &ts)
	before := decodeMemStateForTest(t, m.SaveState())
	driveEverythingMemory(m)
	after := decodeMemStateForTest(t, m.SaveState())

	for _, f := range []struct {
		name string
		same bool
	}{
		{"RAM", equalBulk(before.RAM, after.RAM)},
		{"ROM", equalBulk(before.ROM, after.ROM)},
		{"ReadMap", before.ReadMap == after.ReadMap},
		{"WriteMap", before.WriteMap == after.WriteMap},
		{"Port7FFD", before.Port7FFD == after.Port7FFD},
		{"Port1FFD", before.Port1FFD == after.Port1FFD},
		{"PortDFFD", before.PortDFFD == after.PortDFFD},
		{"PortEFF7Reg2", before.PortEFF7Reg2 == after.PortEFF7Reg2},
		{"PortEFF7Reg3", before.PortEFF7Reg3 == after.PortEFF7Reg3},
		{"SpecialPaging", before.SpecialPaging == after.SpecialPaging},
		{"PagingEnabled", before.PagingEnabled == after.PagingEnabled},
		{"PagingFrozen", before.PagingFrozen == after.PagingFrozen},
		{"ScreenPage", before.ScreenPage == after.ScreenPage},
		{"SlotBank", before.SlotBank == after.SlotBank},
		{"MMUOverride", before.MMUOverride == after.MMUOverride},
		{"AltROMReg", before.AltROMReg == after.AltROMReg},
		{"AltROM0", bytes.Equal(before.AltROM0, after.AltROM0)},
		{"AltROM1", bytes.Equal(before.AltROM1, after.AltROM1)},
		{"FPGABootROMActive", before.FPGABootROMActive == after.FPGABootROMActive},
		{"ConfigModeActive", before.ConfigModeActive == after.ConfigModeActive},
		{"ConfigModeRAMPage", before.ConfigModeRAMPage == after.ConfigModeRAMPage},
		{"MFActive", before.MFActive == after.MFActive},
		{"MFRAM", bytes.Equal(before.MFRAM, after.MFRAM)},
		{"L2WrEn", before.L2WrEn == after.L2WrEn},
		{"L2RdEn", before.L2RdEn == after.L2RdEn},
		{"L2Shadow", before.L2Shadow == after.L2Shadow},
		{"L2Segment", before.L2Segment == after.L2Segment},
		{"L2Offset", before.L2Offset == after.L2Offset},
		{"L2ActiveBank", before.L2ActiveBank == after.L2ActiveBank},
		{"L2ShadowBank", before.L2ShadowBank == after.L2ShadowBank},
		{"BetaEnabled", before.BetaEnabled == after.BetaEnabled},
		{"BetaActive", before.BetaActive == after.BetaActive},
		{"RAMContentionDisabled", before.RAMContentionDisabled == after.RAMContentionDisabled},
	} {
		if f.same {
			t.Errorf("%s is unchanged by the drive sequence, so a lost restore of it "+
				"would go undetected", f.name)
		}
	}
}

func decodeMemStateForTest(t *testing.T, b []byte) memState {
	t.Helper()
	var s memState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}

// equalBulk compares the two multi-bank stores element by element, so a guard
// failure means every bank matched rather than that the slice headers did.
func equalBulk(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// firstDiff reports where two transcripts part company, so a failure names the
// step rather than dumping thousands of bytes.
func firstDiff(a, b []byte) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	if len(b) > len(a) {
		return len(a)
	}
	return -1
}
