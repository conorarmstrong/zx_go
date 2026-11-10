package testharness

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// programCTC0 puts channel 0 in timer mode, prescaler 16, auto-restart,
// interrupt enabled, time constant 10. Port $183B selects channel 0.
func programCTC0(t *testing.T, h *Harness) {
	t.Helper()
	// D7=1 int enable, D6=0 timer, D5=0 prescaler 16, D4=0 falling,
	// D3=0 auto, D2=1 time constant follows, D0=1 control word.
	h.ULA().WritePort(0x183B, 0x85)
	h.ULA().WritePort(0x183B, 10)
}

// TestNextCTCReachesIM2 is the end-to-end reachability test ROADMAP item 2
// asks for: program a CTC channel through the guest port decode, enable its
// NR$C5 bit, switch NR$C0 to hardware IM2, and observe the daisy chain raise
// /INT with the CTC's vector. Without the wiring the channel and the chain
// were both modelled but unreachable.
func TestNextCTCReachesIM2(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	if h.NextIM2() == nil || h.NextCTC() == nil {
		t.Fatal("IM2/CTC were not wired into the Next harness")
	}

	// Disable the ULA frame and line interrupts so the CTC is the only
	// source; otherwise the frame pulse would win the chain first.
	h.NextRegs().WriteReg(0x22, 0x04) // frame INT disable
	h.NextRegs().WriteReg(0xC4, 0x00) // ULA + line INT enables off
	h.NextRegs().WriteReg(0xC5, 0x01) // CTC channel 0 enable
	h.NextRegs().WriteReg(0xC0, 0x01) // hardware IM2 mode
	h.CPU().IFF1 = false              // observe the request before acceptance

	programCTC0(t, h)

	h.RunFrames(1) // plenty of 28 MHz CTC clocks

	if !h.NextIM2().Status(next.IM2SourceCTC0) {
		t.Fatal("CTC0 did not set its IM2 status bit")
	}
	if !h.NextIM2().INTAsserted() {
		t.Fatal("CTC0 request did not assert /INT through the daisy chain")
	}

	// Stop further CTC pulses, then accept the latched request. The
	// vector base is NR$C0 bits 7:5 (zero here), the chain index is 3,
	// so the vector byte is $06.
	h.NextRegs().WriteReg(0xC5, 0x00)
	h.CPU().IFF1 = true
	before := z80.IntFireCount
	h.CPU().ExecuteFrame(100)
	if z80.IntFireCount == before {
		t.Fatal("CPU did not accept the CTC interrupt")
	}
	if h.NextIM2().INTAsserted() {
		t.Error("/INT still asserted after the CTC interrupt was acknowledged")
	}
}

// The CTC's port decode owns $1C3B..$1F3B even though those addresses select
// no channel. They must be answered (read 0) rather than fall through to the
// floating bus, or a probe of the unused slots sees a live Spectrum.
func TestNextCTCPortDecode(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	if v, ok := h.ULA().ReadPort(0x183B); !ok || v != 0 {
		t.Errorf("$183B = (%d,%v), want (0,true)", v, ok)
	}
	if v, ok := h.ULA().ReadPort(0x1C3B); !ok || v != 0 {
		t.Errorf("$1C3B = (%d,%v), want (0,true) for a decoded-but-empty slot", v, ok)
	}
	if _, ok := h.ULA().ReadPort(0x173B); ok {
		t.Error("$173B was claimed by the CTC")
	}
}

// The ULA frame and line interrupts are the other two IM2 sources the CPU
// generates itself. Verify both reach the chain and that NR$C8's status
// read/clear paths work.
func TestNextULARandLineSourcesReachIM2(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	h.CPU().IFF1 = false
	h.NextRegs().WriteReg(0xC0, 0x01) // hardware IM2
	h.NextRegs().WriteReg(0xC4, 0x03) // ULA + line enables

	h.RunFrames(1)
	if !h.NextIM2().Status(next.IM2SourceULA) {
		t.Fatal("ULA frame INT did not set the IM2 status bit")
	}
	if !h.NextIM2().INTAsserted() {
		t.Fatal("ULA frame INT did not assert /INT")
	}
	// The status bit remains set while the request is pending (as the
	// docs say). Acknowledge, end the service, drop the source, then
	// clear the record.
	if _, ok := h.NextIM2().Acknowledge(); !ok {
		t.Fatal("ULA request could not be acknowledged")
	}
	h.NextIM2().RETISeen()
	h.NextIM2().Lower(next.IM2SourceULA)
	h.NextRegs().WriteReg(0xC8, 0x01)
	if h.NextIM2().Status(next.IM2SourceULA) {
		t.Error("NR$C8 bit 0 did not clear the ULA status bit")
	}

	// Now disable the frame INT and schedule a line INT instead. The
	// target is absolute raster line 100; the line source must latch.
	h.NextRegs().WriteReg(0x22, 0x06) // frame disable + line enable
	h.NextRegs().WriteReg(0x23, 100)
	h.RunFrames(1)
	if !h.NextIM2().Status(next.IM2SourceLine) {
		t.Fatal("line INT did not set the IM2 status bit")
	}
	if !h.NextIM2().INTAsserted() {
		t.Fatal("line INT did not assert /INT")
	}
	if _, ok := h.NextIM2().Acknowledge(); !ok {
		t.Fatal("line request could not be acknowledged")
	}
	h.NextIM2().RETISeen()
	h.NextIM2().Lower(next.IM2SourceLine)
	h.NextRegs().WriteReg(0xC8, 0x02)
	if h.NextIM2().Status(next.IM2SourceLine) {
		t.Error("NR$C8 bit 1 did not clear the line status bit")
	}
}

// im2Asserts raises one source and reports whether /INT came up, then
// drops it and acknowledges so the chain is left idle.
func im2Asserts(h *Harness, src int) bool {
	im2 := h.NextIM2()
	im2.Raise(src)
	im2.Lower(src)
	got := im2.INTAsserted()
	if got {
		im2.Acknowledge()
		im2.RETISeen()
	}
	return got
}

// The ULA frame INT enable is ula_int_en(0) = not port_ff_interrupt_disable
// (zxnext.vhd:6711, :1949), which resets to enabled. A guest that only flips
// NR$C0 to hardware IM2 must therefore still receive the frame interrupt.
func TestNextIM2FrameEnabledAtReset(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	h.NextRegs().WriteReg(0xC0, 0x01)
	if !im2Asserts(h, next.IM2SourceULA) {
		t.Error("frame INT did not reach the chain with no NR$C4/NR$22 write")
	}
	if im2Asserts(h, next.IM2SourceLine) {
		t.Error("line INT reached the chain although nr_22_line_interrupt_en resets to 0")
	}
}

// NR$22 bit 2 is port_ff_interrupt_disable (zxnext.vhd:3619-3621) and bit 1 is
// nr_22_line_interrupt_en (:5297); both feed the chain's enables.
func TestNextIM2EnablesFollowNR22(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	h.NextRegs().WriteReg(0xC0, 0x01)

	h.NextRegs().WriteReg(0x22, 0x04)
	if im2Asserts(h, next.IM2SourceULA) {
		t.Error("frame INT reached the chain with NR$22 bit 2 set")
	}
	h.NextRegs().WriteReg(0x22, 0x02)
	if !im2Asserts(h, next.IM2SourceULA) {
		t.Error("frame INT blocked with NR$22 bit 2 clear")
	}
	if !im2Asserts(h, next.IM2SourceLine) {
		t.Error("line INT blocked with NR$22 bit 1 set")
	}
}

// NR$C4 bit 0 writes port_ff_reg(6) <= not bit 0 (zxnext.vhd:3622-3623) and
// bit 1 writes nr_22_line_interrupt_en (:5610), so NR$C4 and NR$22 alias.
func TestNextIM2EnablesFollowNRC4(t *testing.T) {
	h, err := New(roms.ModelNext)
	if err != nil {
		t.Fatalf("New(ModelNext): %v", err)
	}
	h.NextRegs().WriteReg(0xC0, 0x01)

	h.NextRegs().WriteReg(0xC4, 0x00)
	if im2Asserts(h, next.IM2SourceULA) {
		t.Error("frame INT reached the chain with NR$C4 bit 0 clear")
	}
	if h.NextRegs().Raw(0x22)&0x04 == 0 {
		t.Error("NR$C4 bit 0 clear did not set NR$22 bit 2")
	}
	h.NextRegs().WriteReg(0xC4, 0x03)
	if !im2Asserts(h, next.IM2SourceULA) || !im2Asserts(h, next.IM2SourceLine) {
		t.Error("NR$C4=$03 did not enable frame and line INT")
	}
}
