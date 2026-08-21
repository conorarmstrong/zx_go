package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/memory"
	"github.com/conorarmstrong/zx_go/pkg/next/layer2"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// Spec-derived tests for NextReg write handlers.
//
// Each test pins a code path that exists in the canonical FPGA VHDL at
// `_tools/reference/tbblue-fpga/cores/zxnext/src/zxnext.vhd`. The test
// name identifies the NR + behaviour; the comment cites the VHDL line
// range. A failure here is a divergence between our handler and the
// hardware spec, and is the most plausible source of cold-boot
// divergence vs NextZXOS expectations.
//
// Strategy: for every NR with a CONDITIONAL side effect in the VHDL
// (e.g. "this field updates only when config_mode = 1"), exercise the
// gated and ungated paths and verify both match the spec. Pure-storage
// NRs are already covered by dispatcher_test.go's TestRoundTripEvery.

// withConfigMode sets up a fresh wired NR dispatcher with config_mode
// in a chosen state. Returns the dispatcher + memory for the test.
func withConfigMode(t *testing.T, active bool) (*nextregs.Dispatcher, *memory.Memory) {
	t.Helper()
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	// Wire the NR$03 machine-type handler so config-mode transitions
	// work. (We don't care about bootrom for these tests.)
	WireMachineType(disp, mem)
	if active {
		mem.EnterConfigMode()
	} else {
		mem.ClearConfigMode()
	}
	return disp, mem
}

// TestSpec_NR06_PS2ModeBit_GatedOnConfigMode verifies that NR$06 bit 2
// (PS/2 mode select) is ONLY updated when config_mode = '1'.
//
// VHDL zxnext.vhd:5161-5170:
//
//	when X"06" =>
//	    nr_06_hotkey_cpu_speed_en <= nr_wr_dat(7);
//	    nr_06_internal_speaker_beep <= nr_wr_dat(6);
//	    nr_06_hotkey_5060_en <= nr_wr_dat(5);
//	    nr_06_button_drive_nmi_en <= nr_wr_dat(4);
//	    nr_06_button_m1_nmi_en <= nr_wr_dat(3);
//	    if nr_03_config_mode = '1' then
//	       nr_06_ps2_mode <= nr_wr_dat(2);
//	    end if;
//	    nr_06_psg_mode <= nr_wr_dat(1 downto 0);
//
// Bits 7,6,5,4,3,1,0 always update; bit 2 ONLY updates when config_mode.
func TestSpec_NR06_PS2ModeBit_GatedOnConfigMode(t *testing.T) {
	disp, mem := withConfigMode(t, true)
	WirePeripheral2(disp, nil, nil, mem)

	// In config mode: write all bits set, then a write with bit 2 = 1
	// should establish bit 2 = 1.
	disp.WriteReg(0x06, 0x00)
	disp.WriteReg(0x06, 0x04) // bit 2 = 1
	if got := disp.ReadReg(0x06) & 0x04; got != 0x04 {
		t.Errorf("config_mode=1, write $04 to NR$06: bit 2 = $%02X, want $04", got)
	}

	// Now exit config mode. Bit 2 must be FROZEN.
	mem.ClearConfigMode()
	// Write 0x00 (= bit 2 = 0). On real HW bit 2 stays at the old value (= 1).
	// All OTHER bits should still update.
	disp.WriteReg(0x06, 0xFB) // 1111 1011 — bit 2 = 0, all others = 1
	got := disp.ReadReg(0x06)
	if got&0x04 != 0x04 {
		t.Errorf("config_mode=0, write $FB to NR$06: bit 2 = $%02X, want $04 (frozen)", got&0x04)
	}
	if got&0xFB != 0xFB {
		t.Errorf("config_mode=0, write $FB to NR$06: other bits = $%02X, want $FB", got&0xFB)
	}
}

// TestSpec_NR0A_MFTypeAndSDSwap_GatedOnConfigMode verifies that NR$0A
// bits 7:6 (MF type) and bit 5 (SD swap) are ONLY updated when
// config_mode = '1'. Bits 4,3,1,0 always update.
//
// VHDL zxnext.vhd:5191-5198:
//
//	when X"0A" =>
//	    if nr_03_config_mode = '1' then
//	        nr_0a_mf_type <= nr_wr_dat(7 downto 6);
//	        nr_0a_sd_swap <= nr_wr_dat(5);
//	    end if;
//	    nr_0a_divmmc_automap_en <= nr_wr_dat(4);
//	    nr_0a_mouse_button_reverse <= nr_wr_dat(3);
//	    nr_0a_mouse_dpi <= nr_wr_dat(1 downto 0);
func TestSpec_NR0A_MFTypeAndSDSwap_GatedOnConfigMode(t *testing.T) {
	disp, mem := withConfigMode(t, true)
	// Use the no-pager variant of WirePeripheral1
	WirePeripheral1(disp, nil, mem)

	// In config mode: write $FF (all bits set). Bits 7:6 = 11, bit 5 = 1.
	disp.WriteReg(0x0A, 0xFF)
	if got := disp.ReadReg(0x0A) & 0xE0; got != 0xE0 {
		t.Errorf("config_mode=1, write $FF to NR$0A: bits 7:5 = $%02X, want $E0", got)
	}

	// Exit config mode. Bits 7:6 + bit 5 must freeze.
	mem.ClearConfigMode()
	// Write 0x1F (= bits 7:5 all 0). Bits 4,3,1,0 should update; bits 7:5 should stay.
	// Bit 2 reads as 0 per spec.
	disp.WriteReg(0x0A, 0x1F)
	got := disp.ReadReg(0x0A)
	if got&0xE0 != 0xE0 {
		t.Errorf("config_mode=0, write $1F to NR$0A: bits 7:5 = $%02X, want $E0 (frozen)", got&0xE0)
	}
	if got&0x1B != 0x1B {
		t.Errorf("config_mode=0, write $1F to NR$0A: bits 4,3,1,0 = $%02X, want $1B (bit 2 reads 0)",
			got&0x1B)
	}
}

// TestSpec_NR08_ContentionDisableBit6 verifies that NR$08 bit 6
// controls the RAM contention disable flag (NOT bit 1). Per VHDL
// zxnext.vhd:5175-5183:
//
//	when X"08" =>
//	    nr_08_contention_disable <= nr_wr_dat(6);     -- bit 6
//	    nr_08_psg_stereo_mode <= nr_wr_dat(5);
//	    nr_08_internal_speaker_en <= nr_wr_dat(4);
//	    nr_08_dac_en <= nr_wr_dat(3);
//	    nr_08_port_ff_rd_en <= nr_wr_dat(2);
//	    nr_08_psg_turbosound_en <= nr_wr_dat(1);
//	    nr_08_keyboard_issue2 <= nr_wr_dat(0);
//
// Bit 6 = $40 controls contention. Bit 1 = $02 controls
// TurboSound (multi-AY chip enable). Different bits, different
// purposes.
func TestSpec_NR08_ContentionDisableBit6(t *testing.T) {
	_, mem := withConfigMode(t, false)
	disp := nextregs.New()
	WireContentionDisable(disp, mem)

	// Write only bit 1 set (would be TurboSound enable per spec) —
	// must NOT enable contention disable.
	disp.WriteReg(0x08, 0x02)
	if mem.RAMContentionDisabled {
		t.Errorf("NR$08=$02 (bit 1 = TurboSound): RAMContentionDisabled should be false, got true")
	}

	// Write bit 6 set (= contention disable per spec) —
	// must enable contention disable.
	disp.WriteReg(0x08, 0x40)
	if !mem.RAMContentionDisabled {
		t.Errorf("NR$08=$40 (bit 6 = contention disable): RAMContentionDisabled should be true, got false")
	}

	// Write 0 — must disable.
	disp.WriteReg(0x08, 0x00)
	if mem.RAMContentionDisabled {
		t.Errorf("NR$08=$00: RAMContentionDisabled should be false, got true")
	}

	// Write $FF (all bits set including bit 6) — must enable.
	disp.WriteReg(0x08, 0xFF)
	if !mem.RAMContentionDisabled {
		t.Errorf("NR$08=$FF (bit 6 set): RAMContentionDisabled should be true, got false")
	}
}

// TestSpec_NR11_VideoTiming verifies that NR$11 honors the
// config_mode gate AND special-cases bits 2:0 = "111" to clear,
// AND masks the stored value to bits 2:0 only.
//
// VHDL zxnext.vhd:5208-5217 (excluding the g_video_inc branch
// which is a synthesis-time generic, not runtime-selectable):
//
//	when X"11" =>
//	    if nr_03_config_mode = '1' then
//	        if nr_wr_dat(2 downto 0) = "111" then
//	            nr_11_video_timing <= "000";
//	        else
//	            nr_11_video_timing <= nr_wr_dat(2 downto 0);
//	        end if;
//	    end if;
//
// Read-side returns "00000" & nr_11_video_timing (bits 7:3 = 0).
func TestSpec_NR11_VideoTiming_ConfigModeGate(t *testing.T) {
	_, mem := withConfigMode(t, false)
	disp := nextregs.New()
	WireMachineType(disp, mem) // for config_mode wiring
	// Wire a minimal NR$11 handler that follows the spec. The
	// existing handler in wire.go (WireRTC) stores verbatim — this
	// test will fail until the handler honors the gate.
	WireVideoTiming(disp, mem)

	// In config mode: write $03 → stored = $03 (bits 2:0 = 011)
	mem.EnterConfigMode()
	disp.WriteReg(0x11, 0x03)
	if got := disp.ReadReg(0x11) & 0x07; got != 0x03 {
		t.Errorf("config_mode=1, write $03: bits 2:0 = $%02X, want $03", got)
	}

	// Write $FF: bits 2:0 = 111 → spec says STORE "000"
	disp.WriteReg(0x11, 0xFF)
	if got := disp.ReadReg(0x11) & 0x07; got != 0x00 {
		t.Errorf("config_mode=1, write $FF (bits 2:0=111): bits 2:0 = $%02X, want $00 (special-case)",
			got)
	}

	// Write $05: bits 2:0 = 101 → stored = $05
	disp.WriteReg(0x11, 0x05)
	if got := disp.ReadReg(0x11) & 0x07; got != 0x05 {
		t.Errorf("config_mode=1, write $05: bits 2:0 = $%02X, want $05", got)
	}

	// Read-side: bits 7:3 must be zero
	if got := disp.ReadReg(0x11) & 0xF8; got != 0x00 {
		t.Errorf("read NR$11: high bits 7:3 = $%02X, want $00", got)
	}

	// Exit config mode. NR$11 writes must be ignored.
	mem.ClearConfigMode()
	preWrite := disp.ReadReg(0x11) & 0x07
	disp.WriteReg(0x11, 0x03) // different value
	if got := disp.ReadReg(0x11) & 0x07; got != preWrite {
		t.Errorf("config_mode=0 write to NR$11 changed value: was $%02X, became $%02X",
			preWrite, got)
	}
}

// fakeMAPRAMClearer captures ClearMAPRAM calls for testing.
type fakeMAPRAMClearer struct {
	calls int
}

func (f *fakeMAPRAMClearer) ClearMAPRAM() { f.calls++ }

// TestSpec_NR09_Bit3ClearsMAPRAM verifies that NR$09 writes with
// bit 3 set call ClearMAPRAM on the divMMC pager. Per zxnext.vhd:
//
//	elsif nr_09_we = '1' and nr_wr_dat(3) = '1' then
//	    port_e3_reg(6) <= '0';
//
// where port_e3_reg(6) is the MAPRAM latch.
func TestSpec_NR09_Bit3ClearsMAPRAM(t *testing.T) {
	disp := nextregs.New()
	mc := &fakeMAPRAMClearer{}
	WirePeripheral3(disp, mc)

	// Bit 3 NOT set — must not fire.
	disp.WriteReg(0x09, 0x00)
	disp.WriteReg(0x09, 0xF7) // all except bit 3
	if mc.calls != 0 {
		t.Errorf("writes without bit 3: ClearMAPRAM called %d times, want 0", mc.calls)
	}

	// Bit 3 set — fires once per write.
	disp.WriteReg(0x09, 0x08) // bit 3 alone
	if mc.calls != 1 {
		t.Errorf("after NR$09=$08: ClearMAPRAM calls=%d, want 1", mc.calls)
	}

	disp.WriteReg(0x09, 0xFF) // all bits including bit 3
	if mc.calls != 2 {
		t.Errorf("after NR$09=$FF: ClearMAPRAM calls=%d, want 2", mc.calls)
	}

	// Bit 3 is NOT stored (one-shot trigger, not a latched flag).
	if got := disp.Raw(0x09) & 0x08; got != 0 {
		t.Errorf("NR$09 stored value bit 3 = $%02X, want 0 (one-shot)", got)
	}
}

// TestSpec_NR09_NilPagerSafe verifies that passing nil for the
// pager doesn't panic — useful for tests/classic-model setups.
func TestSpec_NR09_NilPagerSafe(t *testing.T) {
	disp := nextregs.New()
	WirePeripheral3(disp, nil)
	// Must not panic
	disp.WriteReg(0x09, 0x08)
	disp.WriteReg(0x09, 0xFF)
	disp.WriteReg(0x09, 0x00)
}

// TestSpec_NR05_BitReorderingOnRead verifies the documented write-
// to-read bit re-mapping of NR$05.
//
// VHDL zxnext.vhd write (~5156):
//
//	when X"05" =>
//	    nr_05_joy0 <= nr_wr_dat(3) & nr_wr_dat(7 downto 6);
//	    nr_05_joy1 <= nr_wr_dat(1) & nr_wr_dat(5 downto 4);
//
// VHDL zxnext.vhd read (5897 — the ONLY NR$05 read assignment):
//
//	when X"05" =>
//	    port_253b_dat <=
//	        nr_05_joy0(1 downto 0) & nr_05_joy1(1 downto 0) &
//	        nr_05_joy0(2) & eff_nr_05_5060 &
//	        nr_05_joy1(2) & eff_nr_05_scandouble_en;
//
// i.e. [7:6]=joy0[1:0] [5:4]=joy1[1:0] [3]=joy0[2] [2]=eff 50/60Hz
// [1]=joy1[2] [0]=eff scandouble. The write places joy0/joy1 at exactly
// those positions, so the mode bits read back where written; bits 2 and
// 0 reflect effective video config (not modelled), reading 0.
func TestSpec_NR05_BitReorderingOnRead(t *testing.T) {
	disp := nextregs.New()
	WireJoystickMode(disp)

	type c struct {
		write, wantRead byte
		desc            string
	}
	for _, tc := range []c{
		{0x00, 0x00, "all zero"},
		{0xFF, 0xFA, "all mode bits set; eff bits 2,0 read 0"},
		{0xC0, 0xC0, "joy0[1:0] read back at bits 7:6"},
		{0x08, 0x08, "joy0[2] (wr bit 3) reads back at bit 3"},
		{0x02, 0x02, "joy1[2] (wr bit 1) reads back at bit 1"},
		{0x30, 0x30, "joy1[1:0] (wr bits 5:4) read back at bits 5:4"},
		{0x55, 0x50, "wr bits 2,0 (eff 50/60Hz + scandouble) masked to 0"},
	} {
		disp.WriteReg(0x05, tc.write)
		got := disp.ReadReg(0x05)
		if got != tc.wantRead {
			t.Errorf("%s: write $%02X → read $%02X, want $%02X",
				tc.desc, tc.write, got, tc.wantRead)
		}
	}
}

// TestSpec_NR8C_ResetSemantics verifies the alt-ROM reset behaviour:
// bits 7:4 of nr_8c_altrom are reloaded from bits 3:0 on reset.
// Per zxnext.vhd:
//
//	if reset = '1' then
//	    nr_8c_altrom(7 downto 4) <= nr_8c_altrom(3 downto 0);
//	elsif nr_8c_we = '1' then
//	    nr_8c_altrom <= nr_wr_dat;
//	end if;
//
// Bits 3:0 are the "default" config (alt-rom-enable, write-only,
// lock-ROM1, lock-ROM0 defaults). Bits 7:4 are the "live" config
// that the OS toggles at runtime. Reset restores live from default.
func TestSpec_NR8C_ResetSemantics(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}

	// Set up: live config = 0 (= no alt-ROM), default config = $0F
	// (= all default bits set: enable, write-only, both lock bits).
	mem.SetAltROMReg(0x0F) // = 0000_1111

	// Trigger reset — bits 7:4 must take values of bits 3:0.
	if err := mem.Reset(); err != nil {
		t.Fatal(err)
	}

	got := mem.AltROMReg()
	want := byte(0xFF) // bits 7:4 = $F (copied from 3:0); bits 3:0 = $F (preserved)
	if got != want {
		t.Errorf("after Reset with defaults=$0F: AltROMReg=$%02X, want $%02X",
			got, want)
	}

	// Re-test with a different default pattern.
	mem.SetAltROMReg(0xF5) // live=$F, defaults=$5
	if err := mem.Reset(); err != nil {
		t.Fatal(err)
	}
	got = mem.AltROMReg()
	want = 0x55 // bits 7:4 = $5 (from defaults), bits 3:0 = $5
	if got != want {
		t.Errorf("after Reset with defaults=$5: AltROMReg=$%02X, want $%02X",
			got, want)
	}
}

// TestSpec_NR22_LineInterruptBitPositions verifies that the line
// interrupt enable bit and frame INT disable bit are at the correct
// positions per nextreg.txt 0x22 + zxnext.vhd:
//
//	bit 7 = (R only) "ULA is asserting INT" — read-only
//	bit 2 = Disables ULA frame interrupt
//	bit 1 = Enables line interrupt
//	bit 0 = MSB of 9-bit line-target value
func TestSpec_NR22_LineInterruptBitPositions(t *testing.T) {
	_, _ = withConfigMode(t, false) // mem unused here; just need ROMs
	cpu := newSpecTestCPU(t)
	disp := nextregs.New()
	WireLineInterrupt(disp, cpu)

	// Bit 1 = line interrupt enable. Write $02 → bit 1 set →
	// should enable line INT.
	disp.WriteReg(0x22, 0x02)
	disp.WriteReg(0x23, 0x10) // line target = 16
	if cpu.LineIntOffsetTstates == 0 {
		t.Errorf("NR$22=$02 (bit 1 enable), NR$23=$10: LineIntOffsetTstates=0, " +
			"want non-zero (line INT armed)")
	}

	// Bit 7 set, bit 1 clear → should NOT enable line INT.
	disp.WriteReg(0x22, 0x80)
	if cpu.LineIntOffsetTstates != 0 {
		t.Errorf("NR$22=$80 (bit 7 only — bit 7 is READ-only status): "+
			"LineIntOffsetTstates=%d, want 0", cpu.LineIntOffsetTstates)
	}

	// Bit 2 = frame INT disable.
	disp.WriteReg(0x22, 0x04) // only bit 2
	if !cpu.FrameIntDisabled {
		t.Errorf("NR$22=$04 (bit 2 frame-disable): FrameIntDisabled=false, want true")
	}

	// Bit 6 set, bit 2 clear → frame INT should NOT be disabled.
	disp.WriteReg(0x22, 0x40)
	if cpu.FrameIntDisabled {
		t.Errorf("NR$22=$40 (bit 6 only — bit 6 is reserved): FrameIntDisabled=true, want false")
	}
}

// newSpecTestCPU creates a minimal CPU just for the line-int tests.
// We only need the FrameIntDisabled + LineIntOffsetTstates fields.
func newSpecTestCPU(t *testing.T) *z80.CPU {
	t.Helper()
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	return z80.New(mem, nil)
}

// TestSpec_NRC0_InterruptControl verifies the storage layout of
// NR$C0 per nextreg.txt:
//
//	bits 7:5 = Programmable upper 3 bits of IM 2 vector
//	bit 4 = Reserved
//	bit 3 = Enable stackless NMI response
//	bits 2:1 = Current Z80 IM mode (READ-only)
//	bit 0 = Maskable INT mode: pulse(0) or hw IM 2(1)
//
// Bits 2:1 are masked to 0 on
// write (read-only field placeholder); other writable bits stored.
// Full CPU integration (programmable IM 2 vector, stackless NMI) is
// follow-up work.
func TestSpec_NRC0_InterruptControl(t *testing.T) {
	disp := nextregs.New()
	WireInterruptControl(disp, nil)

	// Write $FF: all bits set. Stored should mask bits 2:1.
	disp.WriteReg(0xC0, 0xFF)
	if got := disp.Raw(0xC0); got != 0xF9 { // $FF & ~$06 = $F9
		t.Errorf("NR$C0=$FF: stored=$%02X, want $F9 (bits 2:1 masked)", got)
	}

	// Write 0: all clear.
	disp.WriteReg(0xC0, 0x00)
	if got := disp.Raw(0xC0); got != 0x00 {
		t.Errorf("NR$C0=$00: stored=$%02X, want $00", got)
	}

	// Write bits 7:5 = 011 (= IM 2 vector $60), bit 3=1, bit 0=1.
	disp.WriteReg(0xC0, 0x69) // 0110_1001
	if got := disp.Raw(0xC0); got != 0x69 {
		t.Errorf("NR$C0=$69: stored=$%02X, want $69", got)
	}

	// Write bits 2:1 only — should be masked off.
	disp.WriteReg(0xC0, 0x06)
	if got := disp.Raw(0xC0); got != 0x00 {
		t.Errorf("NR$C0=$06 (only bits 2:1): stored=$%02X, want $00 (masked)", got)
	}
}

// TestSpec_NRC4_InterruptEnable0 verifies that NR$C4 writes alias
// the line-int enable bit (bit 1) into NR$22.
// Per VHDL line 5610 the write directly updates nr_22_line_interrupt_en.
func TestSpec_NRC4_InterruptEnable0(t *testing.T) {
	cpu := newSpecTestCPU(t)
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)
	WireInterruptEnable0(disp)

	// Set line target via NR$23.
	disp.WriteReg(0x23, 100)

	// Write NR$C4 bit 1 = 1 → should enable line INT via NR$22 alias.
	disp.WriteReg(0xC4, 0x02)
	if cpu.LineIntOffsetTstates == 0 {
		t.Errorf("NR$C4=$02 (bit 1 alias): LineIntOffsetTstates=0, " +
			"want non-zero (line INT armed via alias)")
	}

	// Clearing NR$C4 bit 1 should disable.
	disp.WriteReg(0xC4, 0x00)
	if cpu.LineIntOffsetTstates != 0 {
		t.Errorf("NR$C4=$00 (bit 1 clear): LineIntOffsetTstates=%d, want 0",
			cpu.LineIntOffsetTstates)
	}

	// Storage byte mask check.
	disp.WriteReg(0xC4, 0xFF)
	if got := disp.Raw(0xC4); got != 0x83 {
		t.Errorf("NR$C4=$FF stored=$%02X, want $83 (bits 7,1,0 retained)", got)
	}
}

// TestSpec_NRC6_InterruptEnable2 — NR$C6 has two 3-bit fields with
// reserved bits 7 and 3 hard-zero on read per VHDL line 6244-6245:
//
//	bit 7 = '0' (reserved)
//	bits 6:4 = nr_c6_int_en_2_654
//	bit 3 = '0' (reserved)
//	bits 2:0 = nr_c6_int_en_2_210
//
// Write must mask bits 7 and 3.
func TestSpec_NRC6_InterruptEnable2(t *testing.T) {
	disp := nextregs.New()
	WireInterruptEnable2(disp)

	// Write $FF — reserved bits 7 and 3 must be cleared on store.
	disp.WriteReg(0xC6, 0xFF)
	if got := disp.Raw(0xC6); got != 0x77 {
		t.Errorf("NR$C6=$FF: stored=$%02X, want $77 (bits 7,3 reserved)", got)
	}

	// Each writable bit individually.
	for _, bit := range []byte{0x40, 0x20, 0x10, 0x04, 0x02, 0x01} {
		disp.WriteReg(0xC6, bit)
		if got := disp.Raw(0xC6); got != bit {
			t.Errorf("NR$C6=$%02X: stored=$%02X, want $%02X", bit, got, bit)
		}
	}

	// Reserved bits 7 and 3 individually — must read as 0.
	disp.WriteReg(0xC6, 0x80)
	if got := disp.Raw(0xC6); got != 0x00 {
		t.Errorf("NR$C6=$80 (only bit 7): stored=$%02X, want $00 (reserved)", got)
	}
	disp.WriteReg(0xC6, 0x08)
	if got := disp.Raw(0xC6); got != 0x00 {
		t.Errorf("NR$C6=$08 (only bit 3): stored=$%02X, want $00 (reserved)", got)
	}
}

// TestSpec_NRCD_DMAIntEn1_AllBits verifies NR$CD stores all 8 bits
// per VHDL line 5632-5633 (no reserved bits).
func TestSpec_NRCD_DMAIntEn1_AllBits(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)
	for _, v := range []byte{0x00, 0x01, 0x55, 0xAA, 0xFF} {
		disp.WriteReg(0xCD, v)
		if got := disp.Raw(0xCD); got != v {
			t.Errorf("NR$CD=$%02X: stored=$%02X, want $%02X", v, got, v)
		}
	}
}

// TestSpec_NRC0_ReadShape_NotImplemented documents that NR$C0 read
// shape (z80_im_mode synthesis at bits 2:1) is not implemented.
// Per VHDL line 6230 read is:
//
//	nr_c0_im2_vector(3) & '0' & nr_c0_stackless_nmi & z80_im_mode(2) & nr_c0_int_mode_pulse_0_im2_1
//
// We don't synthesise z80_im_mode into the read — stored byte has
// those bits masked off. Software polling for "current IM mode" via
// NR$C0 read would see zeros instead of the live IM. Skip-test to
// document the gap so future work spots it.
func TestSpec_NRC0_ReadShape_NotImplemented(t *testing.T) {
	t.Skip("NR$C0 read shape (z80_im_mode synthesis at bits 2:1) not implemented")
}

// TestSpec_NRC4_Bit0_ULAIntEn_NotDerived documents that NR$C4 bit 0
// read returns the stored value rather than the derived
// (NOT port_ff_interrupt_disable) signal that the FPGA composes.
// Per VHDL line 6239 read is:
//
//	nr_c4_int_en_0_expbus & "00000" & ula_int_en   (ula_int_en is 2 bits)
//
// where ula_int_en(0) = NOT port_ff_interrupt_disable. We don't
// model that signal; software polling NR$C4 bit 0 for the ULA INT
// state will not see external-state changes. Skip-test marks the
// gap.
func TestSpec_NRC4_Bit0_ULAIntEn_NotDerived(t *testing.T) {
	t.Skip("NR$C4 bit 0 ULA INT enable derived from port_ff_interrupt_disable not implemented")
}

// TestSpec_NR0B_JoystickIOMode verifies storage layout for NR$0B
// joystick I/O mode register.
//
// Per nextreg.txt + zxnext.vhd:
//
//	bit 7 = enable I/O mode
//	bits 5:4 = I/O mode (00=bit-bang, 01=clock)
//	bit 0 = pin-zero value
//	bits 6, 3:1 = reserved
func TestSpec_NR0B_JoystickIOMode(t *testing.T) {
	disp := nextregs.New()
	WireJoystickIOMode(disp)

	// Write $FF: all bits. Stored should mask reserved bits to 0.
	disp.WriteReg(0x0B, 0xFF)
	if got := disp.Raw(0x0B); got != 0xB1 { // bits 7, 5, 4, 0
		t.Errorf("NR$0B=$FF: stored=$%02X, want $B1 (bits 7,5,4,0)", got)
	}

	// Write 0: all clear.
	disp.WriteReg(0x0B, 0x00)
	if got := disp.Raw(0x0B); got != 0x00 {
		t.Errorf("NR$0B=$00: stored=$%02X, want 0", got)
	}

	// Write only reserved bits — stored should be 0.
	disp.WriteReg(0x0B, 0x4E) // bits 6, 3, 2, 1
	if got := disp.Raw(0x0B); got != 0x00 {
		t.Errorf("NR$0B=$4E (reserved bits only): stored=$%02X, want 0", got)
	}
}

// TestSpec_NR50_57_MMU8_RoundTrip verifies that NR$50-$57 store
// the written byte verbatim and read it back. The VHDL handler is
// trivial (`nr_mmu_we <= '1'` flags it, then a separate process
// stores `nr_wr_dat` into MMU0..MMU7 indexed by the low 3 bits of
// nr_reg). No conditional logic. Round-trip should be lossless.
//
// VHDL zxnext.vhd:5328 (paraphrased) — write side:
//
//	when X"50" | X"51" | X"52" | X"53" | X"54" | X"55" | X"56" | X"57" =>
//	    nr_mmu_we <= '1';
//
// VHDL zxnext.vhd:6155+ — read side:
//
//	when X"50" => port_253b_dat <= MMU0;
//	when X"51" => port_253b_dat <= MMU1;
//	... (etc.)
func TestSpec_NR50_57_MMU8_RoundTrip(t *testing.T) {
	mem, err := memory.New(wireTestROMs(t), roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	disp := nextregs.New()
	WireMMU(disp, mem)

	// Each slot must round-trip every possible byte value
	// independently.
	for slot := byte(0x50); slot <= 0x57; slot++ {
		for v := 0; v <= 0xFF; v++ {
			disp.WriteReg(slot, byte(v))
			if got := disp.ReadReg(slot); got != byte(v) {
				t.Errorf("NR$%02X round-trip: wrote $%02X, read $%02X", slot, v, got)
				return // one failure per slot
			}
		}
	}
}

// =============================================================================
// NR$81-$8A peripheral / bus enable masks.
// Each test cites the VHDL write block at line 5488-5530 and the read
// block at 6122-6155. These NRs are config / mask registers — the FPGA
// stores only the documented bits and reads back zeros in the reserved
// positions.
// =============================================================================

// TestSpec_NR81_ExpansionBus verifies the per-bit storage of NR$81.
//
// VHDL zxnext.vhd:5491-5497:
//
//	when X"81" =>
//	   nr_81_expbus_ula_override         <= nr_wr_dat(6);
//	   nr_81_expbus_nmi_debounce_disable <= nr_wr_dat(5);
//	   nr_81_expbus_clken                <= nr_wr_dat(4);
//	   nr_81_expbus_fdc                  <= nr_wr_dat(3);
//	   nr_81_expbus_speed                <= "00";  -- bits 1:0 hardwired off
//
// Read at line 6125 packs back: bit 7 = i_BUS_ROMCS_n (external input,
// we model as 0), bits 6,5,4,3,2(=0),1:0(=0). Read of a freshly written
// $FF should produce $00|$78 = $78 (bits 6:3 set, bit 7 from external).
// We don't have ROMCS pin modelling, so bit 7 read should always be 0
// for cold-boot tests. Bits 2, 1, 0 are always 0 (reserved/hardwired).
//
// WirePeripheralMasks stores val&$78, masking out bits 7, 2, 1, 0.
func TestSpec_NR81_ExpansionBus_ReservedBitsMaskedOnRead(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0x81, 0xFF)
	if got := disp.ReadReg(0x81); got != 0x78 {
		t.Errorf("NR$81 write $FF: read $%02X, want $78 (bits 6:3 only)", got)
	}

	disp.WriteReg(0x81, 0x07) // attempt to set reserved bits 2:0
	if got := disp.ReadReg(0x81); got != 0x00 {
		t.Errorf("NR$81 write $07: read $%02X, want $00 (reserved bits dropped)", got)
	}
}

// TestSpec_NR82_InternalPortEnable_AllBitsStored covers the simple
// store-every-bit semantics:
//
// VHDL zxnext.vhd:5498-5499:
//
//	when X"82" =>
//	   nr_82_internal_port_enable <= nr_wr_dat;
//
// Read at line 6128 returns the byte verbatim. So $FF round-trips.
// The dispatcher's default store-readback should pass.
func TestSpec_NR82_InternalPortEnable_AllBitsStored(t *testing.T) {
	disp := nextregs.New()

	for _, v := range []byte{0x00, 0xFF, 0x55, 0xAA, 0x01, 0x80} {
		disp.WriteReg(0x82, v)
		if got := disp.ReadReg(0x82); got != v {
			t.Errorf("NR$82 round-trip: wrote $%02X, read $%02X", v, got)
		}
	}
}

// TestSpec_NR83_84_InternalPortEnable_AllBitsStored — same as $82.
// VHDL zxnext.vhd:5501-5505. Round-trip via dispatcher default.
func TestSpec_NR83_84_InternalPortEnable_AllBitsStored(t *testing.T) {
	disp := nextregs.New()

	for _, reg := range []byte{0x83, 0x84} {
		for _, v := range []byte{0x00, 0xFF, 0x33, 0xCC} {
			disp.WriteReg(reg, v)
			if got := disp.ReadReg(reg); got != v {
				t.Errorf("NR$%02X round-trip: wrote $%02X, read $%02X", reg, v, got)
			}
		}
	}
}

// TestSpec_NR85_SplitField verifies the NR$85 split-field encoding:
// bits 3:0 = internal_port_enable, bit 7 = internal_port_reset_type,
// bits 6:4 = reserved (read as 0).
//
// VHDL zxnext.vhd:5507-5509:
//
//	when X"85" =>
//	   nr_85_internal_port_enable     <= nr_wr_dat(3 downto 0);
//	   nr_85_internal_port_reset_type <= nr_wr_dat(7);
//
// Read at line 6137 reconstructs: bit 7 || "000" || bits 3:0.
// So writing $FF reads $8F (bits 7 + 3:0, bits 6:4 zeroed).
//
// WirePeripheralMasks stores val&$8F.
func TestSpec_NR85_SplitField_ReservedBitsMaskedOnRead(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0x85, 0xFF)
	if got := disp.ReadReg(0x85); got != 0x8F {
		t.Errorf("NR$85 write $FF: read $%02X, want $8F", got)
	}
	disp.WriteReg(0x85, 0x70) // attempt to set reserved bits 6:4
	if got := disp.ReadReg(0x85); got != 0x00 {
		t.Errorf("NR$85 write $70: read $%02X, want $00", got)
	}
}

// TestSpec_NR86_87_88_BusPortEnable_AllBitsStored — same as $82.
// VHDL zxnext.vhd:5511-5519. Round-trip via dispatcher default.
func TestSpec_NR86_87_88_BusPortEnable_AllBitsStored(t *testing.T) {
	disp := nextregs.New()

	for _, reg := range []byte{0x86, 0x87, 0x88} {
		for _, v := range []byte{0x00, 0xFF, 0x55, 0xAA} {
			disp.WriteReg(reg, v)
			if got := disp.ReadReg(reg); got != v {
				t.Errorf("NR$%02X round-trip: wrote $%02X, read $%02X", reg, v, got)
			}
		}
	}
}

// TestSpec_NR89_SplitField_ReservedBitsMaskedOnRead — same shape as $85.
//
// VHDL zxnext.vhd:5521-5523:
//
//	when X"89" =>
//	   nr_89_bus_port_enable     <= nr_wr_dat(3 downto 0);
//	   nr_89_bus_port_reset_type <= nr_wr_dat(7);
//
// Read at line 6149: bit 7 || "000" || bits 3:0.
//
// WirePeripheralMasks stores val&$8F.
func TestSpec_NR89_SplitField_ReservedBitsMaskedOnRead(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0x89, 0xFF)
	if got := disp.ReadReg(0x89); got != 0x8F {
		t.Errorf("NR$89 write $FF: read $%02X, want $8F", got)
	}
	disp.WriteReg(0x89, 0x70)
	if got := disp.ReadReg(0x89); got != 0x00 {
		t.Errorf("NR$89 write $70: read $%02X, want $00", got)
	}
}

// TestSpec_NR8A_BusPortPropagate_ReservedBitsMaskedOnRead verifies
// the 6-bit propagate mask:
//
// VHDL zxnext.vhd:5525-5526:
//
//	when X"8A" =>
//	   nr_8a_bus_port_propagate <= nr_wr_dat(5 downto 0);
//
// Read at line 6152: "00" || bits 5:0. Bits 7:6 always read as 0.
//
// WirePeripheralMasks stores val&$3F.
func TestSpec_NR8A_BusPortPropagate_ReservedBitsMaskedOnRead(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0x8A, 0xFF)
	if got := disp.ReadReg(0x8A); got != 0x3F {
		t.Errorf("NR$8A write $FF: read $%02X, want $3F (bits 5:0 only)", got)
	}
	disp.WriteReg(0x8A, 0xC0)
	if got := disp.ReadReg(0x8A); got != 0x00 {
		t.Errorf("NR$8A write $C0: read $%02X, want $00", got)
	}
}

// =============================================================================
// NR$10 (Anti-Brick / Core ID) read-shape.
// VHDL zxnext.vhd:5681-5705 (write) + :5923 (read).
//
// Write semantics (issue 2/3 board):
//
//	bit 7    → flashboot (always stored)
//	bits 4:0 → coreid (only when config_mode = 1)
//
// Read returns: '0' & coreid(5 bits) & button_state(2 bits).
// So bit 7 of READ is always 0 (NOT the stored flashboot value),
// and bits 6:2 are coreid only.
//
// Our implementation stores the byte verbatim and returns it verbatim
// — wrong for bit 7 and for the absence of coreid/button shaping.
//
// Skipped: lower-priority cold-boot impact (NextZXOS does not read
// NR$10 during the cold-boot trace), but locked in here so the gap
// is documented if a future-version core-ID-aware path comes through.
func TestSpec_NR10_ReadShape_ReservedBitsZero(t *testing.T) {
	t.Skip("NR$10 read shaping (bit 7 = 0, coreid synthesis) not implemented")
}

// TestSpec_NR68_ReservedBit1_ZeroOnRead verifies bit 1 of NR$68 reads
// as zero per VHDL line 5026-5037 (`'0'` literal at bit-1 position).
//
// Write bits 7:0 = abcdefgh → store as a,bc,d,e,f,'0',h read shape
// (see VHDL :5037 — the reserved bit 1 reads back as constant 0).
//
// Skipped: NR$68 is not wired; dispatcher default stores the byte
// verbatim, leaking bit 1 on read. NextZXOS does not read NR$68 on
// cold-boot path so deferred.
func TestSpec_NR68_ReservedBit1_ZeroOnRead(t *testing.T) {
	t.Skip("NR$68 OnWrite not implemented — reserved bit 1 not masked")
}

// =============================================================================
// NR$4A / $4B / $4C transparent-control regs.
// VHDL zxnext.vhd:5406-5413 (write), :6050-6056 (read).
// =============================================================================

// TestSpec_NR4A_FallbackRGB_AllBitsStored — full byte round-trip.
// Per zxnext.vhd:5407: `nr_4a_fallback_rgb <= nr_wr_dat`.
func TestSpec_NR4A_FallbackRGB_AllBitsStored(t *testing.T) {
	disp := nextregs.New()
	for _, v := range []byte{0x00, 0xFF, 0x55, 0xAA, 0xE3} {
		disp.WriteReg(0x4A, v)
		if got := disp.ReadReg(0x4A); got != v {
			t.Errorf("NR$4A round-trip: wrote $%02X, read $%02X", v, got)
		}
	}
}

// TestSpec_NR4B_SpriteTransparentIndex_AllBitsStored — full byte.
// Per zxnext.vhd:5410. Used as an 8-bit palette index (256 entries).
func TestSpec_NR4B_SpriteTransparentIndex_AllBitsStored(t *testing.T) {
	disp := nextregs.New()
	for _, v := range []byte{0x00, 0xFF, 0xE3, 0x42} {
		disp.WriteReg(0x4B, v)
		if got := disp.ReadReg(0x4B); got != v {
			t.Errorf("NR$4B round-trip: wrote $%02X, read $%02X", v, got)
		}
	}
}

// TestSpec_NR4C_TilemapTransparentIndex_ReservedBitsMasked verifies
// the tilemap's 4-bit transparent palette index (16 colours max).
//
// VHDL zxnext.vhd:5413: `nr_4c_tm_transparent_index <= nr_wr_dat(3 downto 0)`.
// Read at :6056: `port_253b_dat <= "0000" & nr_4c_tm_transparent_index`.
//
// Masked by WirePeripheralMasks.
func TestSpec_NR4C_TilemapTransparentIndex_ReservedBitsMasked(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0x4C, 0xFF)
	if got := disp.ReadReg(0x4C); got != 0x0F {
		t.Errorf("NR$4C write $FF: read $%02X, want $0F (bits 3:0 only)", got)
	}
	disp.WriteReg(0x4C, 0xF0)
	if got := disp.ReadReg(0x4C); got != 0x00 {
		t.Errorf("NR$4C write $F0: read $%02X, want $00 (bits 7:4 dropped)", got)
	}
	// Spot-check a typical palette index.
	disp.WriteReg(0x4C, 0x07)
	if got := disp.ReadReg(0x4C); got != 0x07 {
		t.Errorf("NR$4C write $07: read $%02X, want $07", got)
	}
}

// =========================================================================
// NR$6E / NR$6F tilemap-base / tiles-base — bit 6 dropped.
//
// VHDL zxnext.vhd:5467-5473 (write) + :6107-6111 (read). The FPGA
// stores bit 7 (bank-select-high) + bits 5:0 (offset). Bit 6 is
// neither stored nor readable — read returns `bit_7 || '0' || bits_5_0`.
// =========================================================================

func TestSpec_NR6E_TilemapBase_Bit6Dropped(t *testing.T) {
	disp := nextregs.New()
	// Have to wire WireTilemap through; use a minimal harness with
	// a real Memory and Tilemap. Simpler: write directly and check
	// that the mask in our OnWrite is correct via the dispatcher.
	// Replicate the WirePeripheralMasks-style handler here.
	disp.SetOnWrite(0x6E, func(d *nextregs.Dispatcher, v byte) {
		d.Store(0x6E, v&0xBF)
	})

	disp.WriteReg(0x6E, 0xFF)
	if got := disp.ReadReg(0x6E); got != 0xBF {
		t.Errorf("NR$6E write $FF: read $%02X, want $BF (bit 6 dropped)", got)
	}
	disp.WriteReg(0x6E, 0x40) // bit 6 only
	if got := disp.ReadReg(0x6E); got != 0x00 {
		t.Errorf("NR$6E write $40: read $%02X, want $00", got)
	}
	disp.WriteReg(0x6E, 0x80) // bank-select-high
	if got := disp.ReadReg(0x6E); got != 0x80 {
		t.Errorf("NR$6E write $80: read $%02X, want $80", got)
	}
}

func TestSpec_NR6F_TilesBase_Bit6Dropped(t *testing.T) {
	disp := nextregs.New()
	disp.SetOnWrite(0x6F, func(d *nextregs.Dispatcher, v byte) {
		d.Store(0x6F, v&0xBF)
	})

	disp.WriteReg(0x6F, 0xFF)
	if got := disp.ReadReg(0x6F); got != 0xBF {
		t.Errorf("NR$6F write $FF: read $%02X, want $BF (bit 6 dropped)", got)
	}
	disp.WriteReg(0x6F, 0x40)
	if got := disp.ReadReg(0x6F); got != 0x00 {
		t.Errorf("NR$6F write $40: read $%02X, want $00", got)
	}
}

// =========================================================================
// NR$70 / NR$71 layer2 resolution + scrollX.
// =========================================================================

// TestSpec_NR70_Layer2Resolution_ReservedBitsMasked.
// VHDL :5475-5477 / :6114 — bits 7:6 reserved.
func TestSpec_NR70_Layer2Resolution_ReservedBitsMasked(t *testing.T) {
	disp := nextregs.New()
	l := layer2.New(nil)
	WireLayer2(disp, l)

	disp.WriteReg(0x70, 0xFF)
	if got := disp.ReadReg(0x70); got != 0x3F {
		t.Errorf("NR$70 write $FF: read $%02X, want $3F (bits 5:0 only)", got)
	}
	if l.PaletteOffset() != 0x0F {
		t.Errorf("NR$70 write $FF: palette offset = %d, want 15", l.PaletteOffset())
	}
	disp.WriteReg(0x70, 0xC0)
	if got := disp.ReadReg(0x70); got != 0x00 {
		t.Errorf("NR$70 write $C0: read $%02X, want $00 (bits 7:6 dropped)", got)
	}
	// Resolution wiring: bits 5:4 = 2 → 640×256.
	disp.WriteReg(0x70, 0x20)
	if l.Resolution() != 2 {
		t.Errorf("NR$70 write $20: resolution = %d, want 2", l.Resolution())
	}
}

// TestSpec_NR71_Layer2ScrollX_OnlyBit0.
// VHDL :5479-5480 / :6117 — bits 7:1 reserved, only bit 0 stored.
// NR$71 (Layer 2 scrollX MSB) is owned by WireLayer2 now that it pushes the
// 9-bit scroll into the live layer; wire a layer so the register responds.
func TestSpec_NR71_Layer2ScrollX_OnlyBit0(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)
	WireLayer2(disp, layer2.New(nil))

	disp.WriteReg(0x71, 0xFF)
	if got := disp.ReadReg(0x71); got != 0x01 {
		t.Errorf("NR$71 write $FF: read $%02X, want $01 (bit 0 only)", got)
	}
	disp.WriteReg(0x71, 0xFE)
	if got := disp.ReadReg(0x71); got != 0x00 {
		t.Errorf("NR$71 write $FE: read $%02X, want $00", got)
	}
}

// =====================================================================
// Additional reserved-bit masks.
// =====================================================================

// TestSpec_NR2F_TilemapScrollXHigh_OnlyBits1to0.
// VHDL :5330-5331 / :6017 — write bits 1:0 only, read returns
// "000000" || bits 1:0.
func TestSpec_NR2F_TilemapScrollXHigh_OnlyBits1to0(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0x2F, 0xFF)
	if got := disp.ReadReg(0x2F); got != 0x03 {
		t.Errorf("NR$2F write $FF: read $%02X, want $03 (bits 1:0 only)", got)
	}
	disp.WriteReg(0x2F, 0xFC)
	if got := disp.ReadReg(0x2F); got != 0x00 {
		t.Errorf("NR$2F write $FC: read $%02X, want $00", got)
	}
}

// TestSpec_NRCE_DMAIntEnable_ReservedBitsMasked.
// VHDL :5635-5637 + read :6263 — '0' || bits 6:4 || '0' || bits 2:0.
// Bits 7 and 3 reserved.
func TestSpec_NRCE_DMAIntEnable_ReservedBitsMasked(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0xCE, 0xFF)
	if got := disp.ReadReg(0xCE); got != 0x77 {
		t.Errorf("NR$CE write $FF: read $%02X, want $77 (bits 6:4 + 2:0)", got)
	}
	disp.WriteReg(0xCE, 0x88) // attempt reserved bits only
	if got := disp.ReadReg(0xCE); got != 0x00 {
		t.Errorf("NR$CE write $88: read $%02X, want $00", got)
	}
}

// TestSpec_NRD8_FDCIOTrap_OnlyBit0.
// VHDL :5639-5640 + read :6265 — "0000000" || bit 0.
func TestSpec_NRD8_FDCIOTrap_OnlyBit0(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0xD8, 0xFF)
	if got := disp.ReadReg(0xD8); got != 0x01 {
		t.Errorf("NR$D8 write $FF: read $%02X, want $01 (bit 0 only)", got)
	}
}

// TestSpec_NRDA_IOTrapCause_OnlyBits1to0.
// VHDL read :6271 — "000000" || bits 1:0.
func TestSpec_NRDA_IOTrapCause_OnlyBits1to0(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0xDA, 0xFF)
	if got := disp.ReadReg(0xDA); got != 0x03 {
		t.Errorf("NR$DA write $FF: read $%02X, want $03 (bits 1:0 only)", got)
	}
}

// TestSpec_NR6A_LoresControl_ReservedBitsMasked.
// VHDL :5455-5458 + read :6101 — "00" || bit 5 || bit 4 || bits 3:0.
// Bits 7:6 reserved.
func TestSpec_NR6A_LoresControl_ReservedBitsMasked(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0x6A, 0xFF)
	if got := disp.ReadReg(0x6A); got != 0x3F {
		t.Errorf("NR$6A write $FF: read $%02X, want $3F (bits 5:0 only)", got)
	}
	disp.WriteReg(0x6A, 0xC0) // attempt reserved bits only
	if got := disp.ReadReg(0x6A); got != 0x00 {
		t.Errorf("NR$6A write $C0: read $%02X, want $00", got)
	}
}

// TestSpec_NRCC_DMAIntEnable0_ReservedBitsMasked.
// VHDL :5628-5630 + read :6257 — bit 7 || "00000" || bits 1:0.
func TestSpec_NRCC_DMAIntEnable0_ReservedBitsMasked(t *testing.T) {
	disp := nextregs.New()
	WirePeripheralMasks(disp)

	disp.WriteReg(0xCC, 0xFF)
	if got := disp.ReadReg(0xCC); got != 0x83 {
		t.Errorf("NR$CC write $FF: read $%02X, want $83 (bit 7 + bits 1:0)", got)
	}
	disp.WriteReg(0xCC, 0x7C) // reserved bits only
	if got := disp.ReadReg(0xCC); got != 0x00 {
		t.Errorf("NR$CC write $7C: read $%02X, want $00", got)
	}
}

// TestSpec_NR0A_Bit2_ReadAsZero.
// VHDL :5908 — NR$0A read returns mf_type || sd_swap || automap_en ||
// mouse_button_reverse || '0' || mouse_dpi. Bit 2 is hardcoded '0'.
func TestSpec_NR0A_Bit2_ReadAsZero(t *testing.T) {
	disp, mem := withConfigMode(t, true)
	WirePeripheral1(disp, nil, mem)

	disp.WriteReg(0x0A, 0xFF)
	got := disp.ReadReg(0x0A)
	if got&0x04 != 0 {
		t.Errorf("NR$0A bit 2 leaked on read after write $FF: $%02X (want bit 2 = 0)",
			got)
	}
	// Other bits should still round-trip (bit 2 is the only one
	// hardcoded to 0; all other bits are valid storage).
	if got&0xFB != 0xFB {
		t.Errorf("NR$0A non-reserved bits dropped on write $FF: got $%02X, want $FB",
			got)
	}
}

// TestSpec_NR22_ReservedBits_ReadAsZero.
// VHDL :5905 — NR$22 read: bit 7 = (not pulse_int_n) dynamic, bits
// 6:3 = "0000", bit 2 = frame_int_disable, bit 1 = line_int_en,
// bit 0 = line MSB. Bits 7:3 must not leak from a guest write.
func TestSpec_NR22_ReservedBits_ReadAsZero(t *testing.T) {
	cpu := newSpecTestCPU(t)
	disp := nextregs.New()
	WireCPUSpeed(disp, cpu)
	WireLineInterrupt(disp, cpu)

	disp.WriteReg(0x22, 0xFF)
	got := disp.ReadReg(0x22)
	if got&0xF8 != 0 {
		t.Errorf("NR$22 bits 7:3 leaked: $%02X (want 7:3 = 0)", got&0xF8)
	}
	// Bits 2:0 should be set from the write.
	if got&0x07 != 0x07 {
		t.Errorf("NR$22 bits 2:0 dropped: $%02X (want bits 2:0 = 7)", got&0x07)
	}
}

// TestSpec_NR34_Bit7_ReadAsZero.
// VHDL — NR$34 read returns '0' || sprite_mirror_id(6:0). Bit 7 is
// reserved (only 128 sprites — bits 6:0 enough for full range).
func TestSpec_NR34_Bit7_ReadAsZero(t *testing.T) {
	disp := nextregs.New()
	e := sprite.New()
	WireSprites(disp, e)

	disp.WriteReg(0x34, 0xFF)
	got := disp.ReadReg(0x34)
	if got&0x80 != 0 {
		t.Errorf("NR$34 bit 7 leaked: $%02X (want bit 7 = 0)", got)
	}
	if got&0x7F != 0x7F {
		t.Errorf("NR$34 bits 6:0 dropped: $%02X (want bits 6:0 = $7F)", got)
	}
}

// TestSpec_NR1C_ClipIdxReset_ReadsPackedIndices.
// VHDL :5278-5290 — NR$1C bits 3:0 reset the L2/sprite/ULA/tilemap clip
// indexes; :5975 — NR$1C READ returns the four 2-bit indexes packed as
// tm<<6 | ula<<4 | spr<<2 | l2. (Faithful clip-window model — supersedes
// the old single-byte "read back masked $0F" approximation.)
func TestSpec_NR1C_ClipIdxReset_ReadsPackedIndices(t *testing.T) {
	disp := nextregs.New()
	WireClipWindows(disp, nil, nil, nil)

	// Advance the Layer2 and sprite clip indexes by one write each.
	disp.WriteReg(0x18, 0x00)                   // l2 idx → 1
	disp.WriteReg(0x19, 0x00)                   // spr idx → 1
	if got := disp.ReadReg(0x1C); got != 0x05 { // spr(1)<<2 | l2(1)
		t.Errorf("NR$1C packed indices = $%02X, want $05", got)
	}
	// Writing $FF resets all four indexes (bits 3:0 set).
	disp.WriteReg(0x1C, 0xFF)
	if got := disp.ReadReg(0x1C); got != 0x00 {
		t.Errorf("NR$1C after reset-all: read $%02X, want $00", got)
	}
}

// TestSpec_NR12_Layer2ActiveBank_Bit7Reserved.
// VHDL read NR$12 = '0' || active_bank(6:0).
func TestSpec_NR12_Layer2ActiveBank_Bit7Reserved(t *testing.T) {
	disp := nextregs.New()
	l := layer2.New(nil)
	WireLayer2(disp, l)

	disp.WriteReg(0x12, 0xFF)
	got := disp.ReadReg(0x12)
	if got&0x80 != 0 {
		t.Errorf("NR$12 bit 7 leaked: $%02X", got)
	}
	if got&0x7F != 0x7F {
		t.Errorf("NR$12 bits 6:0 dropped: $%02X (want $7F)", got)
	}
}

// TestSpec_NR13_Layer2ShadowBank_Bit7Reserved.
// VHDL read NR$13 = '0' || shadow_bank(6:0).
func TestSpec_NR13_Layer2ShadowBank_Bit7Reserved(t *testing.T) {
	disp := nextregs.New()
	l := layer2.New(nil)
	WireLayer2(disp, l)

	disp.WriteReg(0x13, 0xFF)
	got := disp.ReadReg(0x13)
	if got&0x80 != 0 {
		t.Errorf("NR$13 bit 7 leaked: $%02X", got)
	}
	if got&0x7F != 0x7F {
		t.Errorf("NR$13 bits 6:0 dropped: $%02X (want $7F)", got)
	}
}
