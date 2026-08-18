package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/next/sprite"
)

// The LoRes layer's registers were decoded and stored and then read by nothing:
// NR$6A landed in the dispatcher and no render path ever asked for it. These
// pin the four registers that feed lores.vhd's input ports onto the Config the
// renderer actually uses.
//
// Every bit position below is from the FPGA source rather than from folklore:
//
//	nr_15_lores_en             <= nr_wr_dat(7)        zxnext.vhd:5229
//	nr_32_lores_scrollx        <= nr_wr_dat           zxnext.vhd:5340
//	nr_33_lores_scrolly        <= nr_wr_dat           zxnext.vhd:5343
//	nr_6a_lores_radastan       <= nr_wr_dat(5)        zxnext.vhd:5456
//	nr_6a_lores_radastan_xor   <= nr_wr_dat(4)        zxnext.vhd:5457
//	nr_6a_lores_palette_offset <= nr_wr_dat(3:0)      zxnext.vhd:5458

func TestNR15Bit7IsTheLoResMasterEnable(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	st := WireLoRes(d, &cfg)

	if st.Enabled() {
		t.Fatal("LoRes is enabled before anything wrote NR$15")
	}
	d.WriteReg(0x15, 0x80)
	if !st.Enabled() {
		t.Error("NR$15 bit 7 did not enable the LoRes layer (zxnext.vhd:5229)")
	}
	d.WriteReg(0x15, 0x7F)
	if st.Enabled() {
		t.Error("clearing NR$15 bit 7 did not disable the LoRes layer")
	}
}

func TestNR6ACarriesTheModeAndPaletteOffset(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	WireLoRes(d, &cfg)

	d.WriteReg(0x6A, 0x20|0x07) // bit 5 radastan, offset 7
	if !cfg.Radastan {
		t.Error("NR$6A bit 5 did not select Radastan mode (zxnext.vhd:5456)")
	}
	if cfg.PaletteOffset != 7 {
		t.Errorf("palette offset = %d, want 7 (zxnext.vhd:5458)", cfg.PaletteOffset)
	}

	d.WriteReg(0x6A, 0x00)
	if cfg.Radastan {
		t.Error("clearing NR$6A bit 5 did not leave LoRes mode")
	}
}

// dfile_i is NOT the Timex bit on its own. The FPGA feeds the layer
//
//	lores_dfile_0 <= port_ff_screen_mode(0) xor nr_6a_lores_radastan_xor
//
// (zxnext.vhd:6796), whose comment says "radastan can coexist with standard
// timex display files". Wiring the Timex bit straight through would put
// Radastan on the wrong display file for every program that sets bit 4.
func TestTheDisplayFileIsTheTimexBitXoredWithNR6ABit4(t *testing.T) {
	for _, tc := range []struct {
		timex bool
		xor   bool
		want  bool
	}{
		{false, false, false},
		{true, false, true},
		{false, true, true},
		{true, true, false},
	} {
		d := nextregs.New()
		var cfg lores.Config
		st := WireLoRes(d, &cfg)

		v := byte(0)
		if tc.xor {
			v |= 0x10
		}
		d.WriteReg(0x6A, v)
		st.SetTimexDfile(tc.timex)

		if cfg.Dfile != tc.want {
			t.Errorf("timex=%v xor=%v: Dfile = %v, want %v", tc.timex, tc.xor, cfg.Dfile, tc.want)
		}
	}
}

// The xor is applied whichever order the two inputs arrive in: a program that
// sets the Timex mode first and NR$6A second must end up in the same place.
func TestTheDisplayFileIsRecomputedWhicheverArrivesLast(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	st := WireLoRes(d, &cfg)

	st.SetTimexDfile(true)
	d.WriteReg(0x6A, 0x10) // xor set after the Timex bit
	if cfg.Dfile {
		t.Error("Dfile should be true xor true = false when NR$6A arrives second")
	}
}

func TestNR32AndNR33AreTheLoResScrollOffsets(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	WireLoRes(d, &cfg)

	d.WriteReg(0x32, 0x5A)
	d.WriteReg(0x33, 0xA5)
	if cfg.ScrollX != 0x5A {
		t.Errorf("ScrollX = %#02x, want %#02x (zxnext.vhd:5340)", cfg.ScrollX, 0x5A)
	}
	if cfg.ScrollY != 0xA5 {
		t.Errorf("ScrollY = %#02x, want %#02x (zxnext.vhd:5343)", cfg.ScrollY, 0xA5)
	}
}

// NR$15 carries five other things, and WireLoRes chains rather than replacing
// so they keep working. The chain is the thing under test, so the real owner
// has to be installed first: on a bare dispatcher the previous handler is nil,
// WireLoRes takes its fallback branch, and this passes with the whole chain
// deleted while the sprite enable, zero-on-top, over-border and border-clip all
// silently freeze on the real machine.
func TestTheLoResEnableChainsToTheRealNR15Owner(t *testing.T) {
	d := nextregs.New()
	sprites := sprite.New()
	WireLayerPriority(d, NewLayerPriority(), sprites)

	var cfg lores.Config
	st := WireLoRes(d, &cfg)

	// $45 is Sonic's value: sprites on, zero-on-top, layer priority; plus the
	// LoRes enable in bit 7.
	d.WriteReg(0x15, 0x80|0x45)

	if !st.Enabled() {
		t.Error("the LoRes enable did not take effect through the chain")
	}
	if !sprites.Enabled() {
		t.Error("chaining dropped the sprite enable: NR$15 bit 0 never reached the engine")
	}
	if got := d.ReadReg(0x15); got != 0x80|0x45 {
		t.Errorf("NR$15 reads back %#02x, want %#02x", got, 0x80|0x45)
	}
}

// The enable is read from the register file rather than cached, so it cannot go
// stale when the dispatcher's own LoadState assigns its register array without
// firing any handler.
func TestTheEnableSurvivesARegisterFileRestore(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	st := WireLoRes(d, &cfg)

	d.WriteReg(0x15, 0x80)
	blob := d.SaveState()
	d.WriteReg(0x15, 0x00)
	if st.Enabled() {
		t.Fatal("the enable did not clear, so the restore below proves nothing")
	}

	if err := d.LoadState(blob); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !st.Enabled() {
		t.Error("the enable did not come back with the register file: a cached copy " +
			"would survive the rewind while the register it mirrors moved underneath it")
	}
}

// A reset clears port $FF on the FPGA, so the cached Timex bit must go with it.
func TestAResetClearsTheCachedTimexDisplayFile(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	st := WireLoRes(d, &cfg)

	st.SetTimexDfile(true)
	if !cfg.Dfile {
		t.Fatal("the fixture did not set the display file, so this proves nothing")
	}

	d.Reset()
	if cfg.Dfile {
		t.Error("the pre-reset Timex display file leaked through the reset: Radastan " +
			"would read from $6000 where the hardware reads $4000")
	}
}

// Install-time seeding: anything that put a byte in these registers without
// going through a write -- applyTBBLUEFWBootDefaults uses Store, and the
// dispatcher's LoadState assigns directly -- must still be reflected.
func TestTheConfigIsSeededFromTheRegistersAtInstallTime(t *testing.T) {
	d := nextregs.New()
	d.Store(0x6A, 0x20|0x03)
	d.Store(0x32, 0x40)
	d.Store(0x33, 0x50)

	var cfg lores.Config
	WireLoRes(d, &cfg)

	if !cfg.Radastan || cfg.PaletteOffset != 3 || cfg.ScrollX != 0x40 || cfg.ScrollY != 0x50 {
		t.Errorf("config did not start from the register file: %+v", cfg)
	}
}

// The ULA clip window (NR$1A) is the layer's clip_x1_i..clip_y2_i. Its zero
// value clips everything: the layer's clip test is inclusive at both ends, so
// an all-zero window admits exactly the pixel at (0,0). The FPGA resets it to
// the whole area (zxnext.vhd:4971-4974), so a config that never receives the
// reset window renders one pixel and leaves the rest of the screen to the ULA
// underneath -- which reads as the layer not working rather than as a missing
// default.
//
// The index-walking and reset semantics of the window itself are pinned in
// wire_clip_test.go, next to the rest of clipWindow; this is only about the
// window reaching the layer.
func TestTheULAClipWindowReachesTheLoResConfig(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	cw := WireClipWindows(d, nil, nil)
	cw.SetULAClipSink(func(x1, x2, y1, y2 byte) {
		cfg.ClipX1, cfg.ClipX2, cfg.ClipY1, cfg.ClipY2 = x1, x2, y1, y2
	})

	if cfg.ClipX2 != 0xFF || cfg.ClipY2 != 0xBF {
		t.Fatalf("the reset window did not reach the layer on install: got x=%d..%d y=%d..%d, "+
			"want 0..255 and 0..191", cfg.ClipX1, cfg.ClipX2, cfg.ClipY1, cfg.ClipY2)
	}

	d.WriteReg(0x1A, 10)
	d.WriteReg(0x1A, 200)
	if cfg.ClipX1 != 10 || cfg.ClipX2 != 200 {
		t.Errorf("writes did not reach the layer: x = %d..%d, want 10..200",
			cfg.ClipX1, cfg.ClipX2)
	}
}

// Two consumers are expected -- NR$1A is the ULA's window as well as the LoRes
// layer's -- so a second sink must not silence the first.
func TestEveryULAClipSinkIsFed(t *testing.T) {
	d := nextregs.New()
	cw := WireClipWindows(d, nil, nil)

	var first, second byte
	cw.SetULAClipSink(func(x1, _, _, _ byte) { first = x1 })
	cw.SetULAClipSink(func(x1, _, _, _ byte) { second = x1 })

	d.WriteReg(0x1A, 42)
	if first != 42 || second != 42 {
		t.Errorf("sinks saw %d and %d, want 42 and 42: a later sink silenced an earlier one",
			first, second)
	}
}
