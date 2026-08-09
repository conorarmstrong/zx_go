package opus

import "testing"

// The Opus Discovery pages its 8 KB ROM over the Spectrum ROM using an M1
// address trap, and the change is DELAYED BY ONE M1 CYCLE. The hardware is a
// pair of flip-flops: the first recognises the address, the second clocks the
// paging state on the following opcode fetch.
//
// That delay is not an implementation detail, it is load-bearing, and all
// three artifacts agree on it:
//
//   - v2.2 disassembly, RST 8: "When a RST 0008 is encountered, the
//     instruction 'LD HL,(CH_ADD)' is executed, the Discovery ROM is paged
//     in, and the execution continues at address 000B." So the byte AT $0008
//     still comes from the Spectrum ROM.
//   - 48K ROM $0008 really is 2A 5D 5C, LD HL,($5C5D) — CH_ADD.
//   - 48K ROM $1708 is 23, INC HL; the Opus ROM has 00 (NOP) at $1708 and 2B
//     (DEC HL) at $1709, described as "NOP = INC HL / DEC HL Compensate."
//     The NOP is a placeholder for the Spectrum byte that actually executes.
//
// Page-out is the same shape: Opus $1748 is C9, a bare RET, and the
// disassembly says "Although it is merely just a RET, the address is detected
// by the hardware, and the Spectrum ROM is paged in again". The RET itself
// must still be fetched from the Opus ROM.

// TestPagerStartsPagedIn pins power-on: the Opus ROM is over the Spectrum ROM
// at reset, so the Z80 executes the Opus reset vector at $0000
// (DI / LD SP,$1019 / JP $1748).
func TestPagerStartsPagedIn(t *testing.T) {
	var p Pager
	p.Reset()
	if !p.Active() {
		t.Fatal("Opus ROM is not paged in at reset")
	}
}

// TestPageOutTakesEffectAfterTheRET is the page-out delay. The RET at $1748
// must be fetched from the Opus ROM; only the fetch after it sees the
// Spectrum ROM.
func TestPageOutTakesEffectAfterTheRET(t *testing.T) {
	var p Pager
	p.Reset()

	p.PreFetch(PageOut)
	if !p.Active() {
		t.Error("the RET at $1748 was not fetched from the Opus ROM")
	}
	p.PreFetch(0x0000) // wherever the RET returned to
	if p.Active() {
		t.Error("the Opus ROM is still paged in after the RET at $1748")
	}
}

// TestRST8PageInTakesEffectAtEntry1 is the page-in delay, and the reason it
// matters: the instruction at $0008 has to be the Spectrum's
// LD HL,(CH_ADD), and the Opus ROM must be in by $000B (ENTRY_1).
func TestRST8PageInTakesEffectAtEntry1(t *testing.T) {
	var p Pager
	p.Reset()
	p.PreFetch(PageOut)
	p.PreFetch(0x1234) // now running from the Spectrum ROM

	p.PreFetch(RST8)
	if p.Active() {
		t.Error("$0008 was fetched from the Opus ROM; it must be the Spectrum's LD HL,(CH_ADD)")
	}
	p.PreFetch(0x000B)
	if !p.Active() {
		t.Error("the Opus ROM is not paged in at $000B (ENTRY_1)")
	}
}

// TestEntryPointPageInCompensatesForINCHL is the $1708 trap. The Spectrum's
// INC HL at $1708 executes, then the Opus ROM's DEC HL at $1709 cancels it.
func TestEntryPointPageInCompensatesForINCHL(t *testing.T) {
	var p Pager
	p.Reset()
	p.PreFetch(PageOut)
	p.PreFetch(0x1234)

	p.PreFetch(PageIn)
	if p.Active() {
		t.Error("$1708 was fetched from the Opus ROM; the Spectrum's INC HL must execute")
	}
	p.PreFetch(0x1709)
	if !p.Active() {
		t.Error("the Opus ROM is not paged in at $1709 (the compensating DEC HL)")
	}
}

// TestKeyIntPageInLetsTheSpectrumPushBC pins the third trap. The frame
// interrupt handler falls through to KEY-INT at $0048, and the Opus hooks it
// to run INIT_RAM2 once at power-on. The one-M1 delay means the Spectrum's
// own PUSH BC at $0048 executes and the Opus ROM takes over at $0049 —
// which is why the Opus ROM carries a matching C5 at $0048 and only diverges
// at $0049 (PUSH HL against the Spectrum's PUSH DE).
func TestKeyIntPageInLetsTheSpectrumPushBC(t *testing.T) {
	var p Pager
	p.Reset()
	p.PreFetch(PageOut)
	p.PreFetch(0x1234)

	p.PreFetch(KeyInt)
	if p.Active() {
		t.Error("$0048 was fetched from the Opus ROM; the Spectrum's PUSH BC must execute")
	}
	p.PreFetch(0x0049)
	if !p.Active() {
		t.Error("the Opus ROM is not paged in at $0049")
	}
}

// TestOrdinaryFetchesDoNotPage guards against a trap that is too eager.
func TestOrdinaryFetchesDoNotPage(t *testing.T) {
	var p Pager
	p.Reset()
	p.PreFetch(PageOut)
	p.PreFetch(0x0000)
	for _, pc := range []uint16{
		0x0007, 0x0009, 0x000B, 0x0047, 0x0049, 0x1707, 0x1709, 0x1747, 0x1749,
		0x4000, 0x8000, 0xFFFF,
	} {
		p.PreFetch(pc)
		if p.Active() {
			t.Fatalf("a fetch at %#04x paged the Opus ROM in", pc)
		}
	}
}

// TestRepeatedTrapsAreIdempotent pins that re-entering a trap while already
// in that state changes nothing.
func TestRepeatedTrapsAreIdempotent(t *testing.T) {
	var p Pager
	p.Reset()
	for i := 0; i < 4; i++ {
		p.PreFetch(PageIn)
		p.PreFetch(0x1709)
		if !p.Active() {
			t.Fatalf("iteration %d: page-in while already in dropped the ROM", i)
		}
	}
	p.PreFetch(PageOut)
	p.PreFetch(0x0000)
	for i := 0; i < 4; i++ {
		p.PreFetch(PageOut)
		p.PreFetch(0x0000)
		if p.Active() {
			t.Fatalf("iteration %d: page-out while already out paged the ROM in", i)
		}
	}
}

// TestPageOutWhileOutStillArmsCleanly pins the interleaving that broke a
// naive one-flag implementation: a page-in trap immediately followed by a
// page-out trap must leave the LAST trap winning, not both.
func TestTrapsInSuccessionApplyTheLastOne(t *testing.T) {
	var p Pager
	p.Reset()
	p.PreFetch(PageOut)
	p.PreFetch(0x0000) // out

	p.PreFetch(RST8)    // arms page-in
	p.PreFetch(PageOut) // page-in applies here, and arms page-out
	if !p.Active() {
		t.Fatal("the armed page-in did not apply")
	}
	p.PreFetch(0x0000)
	if p.Active() {
		t.Error("the armed page-out did not apply")
	}
}
