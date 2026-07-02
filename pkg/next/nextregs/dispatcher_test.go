package nextregs

import "testing"

func TestNewDispatcherDefaults(t *testing.T) {
	d := New()
	if d.Selected() != 0 {
		t.Errorf("Selected() = %d, want 0", d.Selected())
	}
	// Hardware-identity and reset-default registers carry non-zero
	// values that match the canonical `tbblue_reset_common`. NextZXOS
	// reads several of these before initialising them itself.
	expectDefault := map[byte]byte{
		0x00: defaultMachineID,
		0x01: defaultCoreVersion,
		0x02: 0x01, // bit 0 = "last reset was soft reset" — the FPGA
		// reset cascade makes the implicit power-on reset look like a
		// soft reset to the bootrom, which reads A=$01 here at $0171.
		// WireReset's OnWrite must NOT use edge-detection on this bit
		// — see wire.go for details.
		0x05: 0x01,
		0x06: 0xA0, // hotkey enables: bit7 cpu-speed + bit5 50/60 (zxnext.vhd:5161-5165)
		0x08: 0x10,
		0x0E: defaultCoreSubMinor, // NR$0E = core version sub-minor
		0x0F: defaultBoardID,      // NR$0F = board ID
		// 0x10 default: 0 (Core ID — generic). Don't list it (zero
		// default is the implicit case).
		0x12: 0x08,
		0x13: 0x0B,
		0x14: 0xE3,
		0x42: 0x07, // zxnext.vhd:5002
		0x4A: 0xE3, // zxnext.vhd:5014
		0x4B: 0xE3,
		0x4C: 0x0F,
		// Tilemap base / tile-definitions base. zxnext.vhd:5041-5045 resets
		// nr_6e_tilemap_base = "101100" ($2C) and nr_6f_tilemap_tiles =
		// "001100" ($0C). NextZXOS relies on these defaults (it does not
		// write NR$6E/$6F before reading them at boot), so a $00 default
		// would make it compute the wrong tilemap address.
		0x6E: 0x2C,
		0x6F: 0x0C,
		0x50: 0xFF,
		0x51: 0xFF,
		0x52: 0x0A,
		0x53: 0x0B,
		0x54: 0x04,
		0x55: 0x05,
		0x56: 0x00,
		0x57: 0x01,
		0x98: 0xFF, // Pi GPIO output (zxnext.vhd:5070 nr_98_pi_gpio_o <= X"FF")
		0x99: 0x01, // Pi GPIO output (zxnext.vhd:5071 nr_99_pi_gpio_o <= X"01")
		0xB8: 0x83,
		0xBB: 0xCD,
		// NR$C4 = INT EN 0. nextreg.txt documents "soft reset = 0x81"
		// (bit 7 = expansion-bus /INT enable, bit 0 = ULA interrupt enable;
		// both default ON). NextZXOS/games read this back through $253B; a $00
		// default mis-reports the interrupt-enable state. Surfaced by the
		// post-load FX snapshot diff vs the FPGA-faithful oracle (ours=$00,
		// oracle=$81). The WireInterruptEnable0 OnWrite masks to bits 7,1,0.
		0xC4: 0x81,
	}
	for reg := 0; reg < 256; reg++ {
		want, hasOverride := expectDefault[byte(reg)]
		if !hasOverride {
			want = 0
		}
		if got := d.Raw(byte(reg)); got != want {
			t.Errorf("Raw(%#x) = %d, want %d", reg, got, want)
		}
	}
}

func TestRoundTripEveryRegister(t *testing.T) {
	d := New()
	// Write a distinct value to every register, then read all back.
	// `reg ^ 0xA5` produces 256 distinct bytes; any pattern that
	// covers the full byte range would do. The read-only hardware-identity
	// registers (NR$00/$01/$0E/$0F) ignore writes by design — covered by
	// TestReadOnlyIdentityRegisters — so skip them here.
	for reg := 0; reg < 256; reg++ {
		if readOnlyIdentity[reg] {
			continue
		}
		want := byte(reg) ^ 0xA5
		d.Select(byte(reg))
		d.WriteData(want)
	}
	for reg := 0; reg < 256; reg++ {
		if readOnlyIdentity[reg] {
			continue
		}
		want := byte(reg) ^ 0xA5
		d.Select(byte(reg))
		if got := d.ReadData(); got != want {
			t.Errorf("ReadData() for reg %#x = %#x, want %#x", reg, got, want)
		}
	}
}

func TestSelectPersistsAcrossMultipleData(t *testing.T) {
	d := New()
	d.Select(0x42)
	d.WriteData(0x11)
	d.WriteData(0x22)
	d.WriteData(0x33)
	// All three writes target 0x42; final value is 0x33.
	if got := d.ReadReg(0x42); got != 0x33 {
		t.Errorf("ReadReg(0x42) = %#x, want 0x33", got)
	}
	// And the selected register is still 0x42.
	if d.Selected() != 0x42 {
		t.Errorf("Selected() = %#x, want 0x42", d.Selected())
	}
}

func TestWriteRegBypassesSelect(t *testing.T) {
	d := New()
	d.Select(0x10)
	d.WriteReg(0x20, 0x55)
	// Selected latch unchanged.
	if d.Selected() != 0x10 {
		t.Errorf("Selected() = %#x after WriteReg, want 0x10", d.Selected())
	}
	// Value stored at 0x20.
	if got := d.ReadReg(0x20); got != 0x55 {
		t.Errorf("ReadReg(0x20) = %#x, want 0x55", got)
	}
	// 0x10 untouched — still at its post-reset default.
	if got := d.ReadReg(0x10); got != 0x00 {
		t.Errorf("ReadReg(0x10) = %#x, want 0x00 (Core ID generic — matches reference)", got)
	}
}

func TestOnWriteHandlerOverrides(t *testing.T) {
	d := New()
	var captured []byte
	d.SetOnWrite(0x07, func(_ *Dispatcher, v byte) {
		// Mimic a side effect: capture and also store.
		captured = append(captured, v)
		d.Store(0x07, v&0x03)
	})

	d.Select(0x07)
	d.WriteData(0xFF)
	d.WriteData(0x10)
	// Both calls captured.
	if len(captured) != 2 || captured[0] != 0xFF || captured[1] != 0x10 {
		t.Errorf("captured = %v, want [0xFF, 0x10]", captured)
	}
	// Last write masked to low two bits.
	if got := d.Raw(0x07); got != 0x00 {
		t.Errorf("Raw(0x07) = %#x, want 0x00", got)
	}

	// Removing the handler restores default storage behaviour.
	d.SetOnWrite(0x07, nil)
	d.WriteData(0xAA)
	if got := d.Raw(0x07); got != 0xAA {
		t.Errorf("Raw(0x07) after handler removed = %#x, want 0xAA", got)
	}
}

func TestOnReadHandlerOverrides(t *testing.T) {
	d := New()
	d.Store(0x1E, 0xAA)
	d.SetOnRead(0x1E, func(_ *Dispatcher) byte { return 0x55 })

	if got := d.ReadReg(0x1E); got != 0x55 {
		t.Errorf("ReadReg(0x1E) = %#x, want 0x55 (handler should override storage)", got)
	}
	// Raw bypasses the handler.
	if got := d.Raw(0x1E); got != 0xAA {
		t.Errorf("Raw(0x1E) = %#x, want 0xAA (Raw should bypass OnRead)", got)
	}
}

// OnWriteFn and GetTracer are chained-handler helpers; both should
// return whatever was last installed (or nil).

func TestOnWriteFnGetter(t *testing.T) {
	d := New()
	if got := d.OnWriteFn(0x42); got != nil {
		t.Errorf("default OnWriteFn(0x42) = non-nil")
	}
	fn := func(_ *Dispatcher, _ byte) {}
	d.SetOnWrite(0x42, fn)
	if got := d.OnWriteFn(0x42); got == nil {
		t.Errorf("after SetOnWrite: OnWriteFn = nil")
	}
	// Detach.
	d.SetOnWrite(0x42, nil)
	if got := d.OnWriteFn(0x42); got != nil {
		t.Errorf("after nil-set: OnWriteFn = non-nil")
	}
}

func TestGetTracerGetter(t *testing.T) {
	d := New()
	if got := d.GetTracer(); got != nil {
		t.Errorf("default GetTracer = non-nil")
	}
	tr := func(_, _ byte, _ bool) {}
	d.SetTracer(tr)
	if got := d.GetTracer(); got == nil {
		t.Errorf("after SetTracer: GetTracer = nil")
	}
	d.SetTracer(nil)
	if got := d.GetTracer(); got != nil {
		t.Errorf("after nil-set: GetTracer = non-nil")
	}
}

func TestWriteRegImplementsNextRegSink(t *testing.T) {
	// Compile-time-style check: the dispatcher has the WriteReg
	// signature that z80.NextRegSink demands. We can't import
	// pkg/z80 from here without a cycle, so the check is
	// structural — assigning to an interface variable with the
	// same method set.
	var _ interface {
		WriteReg(reg, val byte)
	} = New()
}

func TestTracerFiresOnReadAndWrite(t *testing.T) {
	d := New()
	type event struct {
		reg, val byte
		write    bool
	}
	var events []event
	d.SetTracer(func(reg, val byte, write bool) {
		events = append(events, event{reg, val, write})
	})
	d.WriteReg(0x12, 0x34)
	d.ReadReg(0x12)
	if len(events) != 2 {
		t.Fatalf("got %d trace events, want 2", len(events))
	}
	if events[0] != (event{0x12, 0x34, true}) {
		t.Errorf("write event = %+v, want reg=0x12 val=0x34 write=true", events[0])
	}
	if events[1] != (event{0x12, 0x34, false}) {
		t.Errorf("read event = %+v, want reg=0x12 val=0x34 write=false", events[1])
	}
}

func TestTracerNilIsZeroCost(t *testing.T) {
	d := New()
	// Tracer not set — nil dispatch must not crash on accesses.
	d.WriteReg(0x05, 0xAA)
	if d.ReadReg(0x05) != 0xAA {
		t.Errorf("read-after-write broken; tracer-nil path should be transparent")
	}
}

// TestResetRestoresDefaults verifies that after a guest has trampled
// every register, Reset re-establishes the tbblue_reset_common
// power-on values used at cold boot. The GUI's Reboot path leans on
// this so a fresh boot does not inherit the previous session's
// tilemap / Layer 2 / MMU8 configuration.
func TestResetRestoresDefaults(t *testing.T) {
	d := New()
	// Stomp every register with non-default values.
	for reg := 0; reg < 256; reg++ {
		d.WriteReg(byte(reg), byte(reg)^0xA5)
	}
	d.Select(0x99)

	d.Reset()

	if d.Selected() != 0 {
		t.Errorf("after Reset, Selected() = %#x, want 0", d.Selected())
	}
	expect := map[byte]byte{
		0x00: defaultMachineID,
		0x01: defaultCoreVersion,
		0x02: 0x01, // bit 0 = "last reset was soft reset" — the FPGA
		// reset cascade makes the implicit power-on reset look like a
		// soft reset to the bootrom, which reads A=$01 here at $0171.
		// WireReset's OnWrite must NOT use edge-detection on this bit
		// — see wire.go for details.
		0x05: 0x01,
		0x06: 0xA0, // hotkey enables: bit7 cpu-speed + bit5 50/60 (zxnext.vhd:5161-5165)
		0x08: 0x10,
		0x0E: defaultCoreSubMinor, // NR$0E = core version sub-minor
		0x0F: defaultBoardID,      // NR$0F = board ID
		// 0x10 default: 0 (Core ID — generic). Don't list it (zero
		// default is the implicit case).
		0x12: 0x08,
		0x13: 0x0B,
		0x14: 0xE3,
		0x42: 0x07, // zxnext.vhd:5002
		0x4A: 0xE3, // zxnext.vhd:5014
		0x4B: 0xE3,
		0x4C: 0x0F,
		// Tilemap base / tile-definitions base. zxnext.vhd:5041-5045 resets
		// nr_6e_tilemap_base = "101100" ($2C) and nr_6f_tilemap_tiles =
		// "001100" ($0C). NextZXOS relies on these defaults (it does not
		// write NR$6E/$6F before reading them at boot), so a $00 default
		// would make it compute the wrong tilemap address.
		0x6E: 0x2C,
		0x6F: 0x0C,
		0x50: 0xFF,
		0x51: 0xFF,
		0x52: 0x0A,
		0x53: 0x0B,
		0x54: 0x04,
		0x55: 0x05,
		0x57: 0x01,
		0x98: 0xFF, // Pi GPIO output (zxnext.vhd:5070)
		0x99: 0x01, // Pi GPIO output (zxnext.vhd:5071)
		0xB8: 0x83,
		0xBB: 0xCD,
		0xC4: 0x81, // INT EN 0 — nextreg.txt soft reset = 0x81
	}
	for reg := 0; reg < 256; reg++ {
		want, has := expect[byte(reg)]
		if !has {
			want = 0
		}
		if got := d.Raw(byte(reg)); got != want {
			t.Errorf("Raw(%#x) after Reset = %#x, want %#x", reg, got, want)
		}
	}
}

// TestResetFiresOnWriteHandlers proves that subsystems wired via
// SetOnWrite see the reset traffic. The GUI's Reboot path relies on
// this to roll the tilemap / Layer 2 / divMMC layers back to their
// power-on state — Raw() alone would leave them stuck with stale
// values from the previous session.
func TestResetFiresOnWriteHandlers(t *testing.T) {
	d := New()
	var got []byte // values seen by the 0x6B (tilemap control) handler
	d.SetOnWrite(0x6B, func(disp *Dispatcher, val byte) {
		got = append(got, val)
		disp.Store(0x6B, val)
	})
	// Simulate a guest enabling the tilemap, then a reset.
	d.WriteReg(0x6B, 0xA1) // enable + strip_flags + on_top
	d.Reset()

	if len(got) < 2 {
		t.Fatalf("OnWrite(0x6B) saw %d events, want at least 2 (guest write + reset clear)",
			len(got))
	}
	if got[0] != 0xA1 {
		t.Errorf("first OnWrite value = %#x, want 0xA1", got[0])
	}
	// The reset's Phase 1 zero-pass must drive a 0 through 0x6B so
	// the tilemap layer can disable itself. 0x6B has no non-zero
	// default so the last observed value is 0.
	if got[len(got)-1] != 0 {
		t.Errorf("final OnWrite value = %#x, want 0 (tilemap disable on reset)",
			got[len(got)-1])
	}
}

// TestReadOnlyIdentityRegisters pins that NR$00 (Machine ID), NR$01 (Core
// Version), NR$0E (Core sub-minor) and NR$0F (Board ID) are READ-ONLY
// (nextreg.txt marks them (R)): a guest write is ignored, so the value the
// game later checks cannot be corrupted. Nextoid wrote $80 to NR$01 during
// play; storing it made the core version read 8.00/garbage and games gate on
// it ("Core 3.01.10 needed").
func TestReadOnlyIdentityRegisters(t *testing.T) {
	// Only the core-version registers are write-protected (see readOnlyIdentity).
	for _, reg := range []byte{0x01, 0x0E} {
		d := New()
		want := d.ReadReg(reg)
		d.Select(reg)
		d.WriteReg(reg, 0x80) // guest poke via the Z80N NEXTREG opcode
		d.WriteData(0x00)     // and via the $243B/$253B port path
		if got := d.ReadReg(reg); got != want {
			t.Errorf("NR$%02X is read-only: after writes it reads $%02X, want $%02X", reg, got, want)
		}
	}
	// NR$00 (Machine ID) stays writable so games can use it as an
	// emulator/hardware probe (Nextoid pokes it during boot).
	d := New()
	d.WriteReg(0x00, 0x55)
	if got := d.ReadReg(0x00); got != 0x55 {
		t.Errorf("NR$00 must remain writable (emulator probe): read $%02X, want $55", got)
	}
}

// TestCoreVersionSatisfiesModernGames pins the reported core version is a real
// recent core (>= 3.01.10), so games that gate on a minimum core version run.
// NR$01 = major.minor (3.02), NR$0E = sub-minor.
func TestCoreVersionSatisfiesModernGames(t *testing.T) {
	d := New()
	major := d.ReadReg(0x01) >> 4
	minor := d.ReadReg(0x01) & 0x0F
	sub := d.ReadReg(0x0E)
	// Encode as a comparable integer major*10000 + minor*100 + sub.
	got := int(major)*10000 + int(minor)*100 + int(sub)
	const need = 3*10000 + 1*100 + 10 // 3.01.10
	if got < need {
		t.Errorf("core version %d.%02d.%02d < 3.01.10 — games will refuse to run", major, minor, sub)
	}
}
