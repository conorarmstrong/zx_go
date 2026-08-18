package next

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/lores"
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
)

// The ULA+ enable is one bit written from two unrelated places, which is the
// whole reason it needs modelling rather than approximating. Every line below
// is from the FPGA source:
//
//	port_bf3b_ulap_mode  <= cpu_do(7 downto 6)          zxnext.vhd:4532
//	port_bf3b_ulap_index <= cpu_do(5 downto 0)          zxnext.vhd:4534 (mode 00 only)
//	port_ff3b_ulap_en    <= cpu_do(0)                   zxnext.vhd:4549 (mode 01 only)
//	port_ff3b_ulap_en    <= nr_wr_dat(3)                zxnext.vhd:4551 (any NR$68 write)
//	port_ff3b_ulap_en    <= '0'                         zxnext.vhd:4547 (reset)
//	ulap_en_i <= ulap_en_0 and not ulanext_en_0         zxnext.vhd:4246
//	nr_43_ulanext_en <= nr_wr_dat(0)                    zxnext.vhd:5394

func TestPortFF3BSetsTheULAPlusEnableOnlyInModeGroup01(t *testing.T) {
	u := NewULAPlus()

	// Mode group 00 is the palette-index group: an $FF3B write there is a
	// palette write, not the enable.
	u.WriteBF3B(0x00)
	u.WriteFF3B(0x01)
	if u.Enabled() {
		t.Error("an $FF3B write in mode group 00 set the enable; that group is palette data")
	}

	u.WriteBF3B(0x40) // bits 7:6 = 01, the mode group
	u.WriteFF3B(0x01)
	if !u.Enabled() {
		t.Error("an $FF3B write in mode group 01 did not set the enable (zxnext.vhd:4549)")
	}
	u.WriteFF3B(0x00)
	if u.Enabled() {
		t.Error("clearing bit 0 in mode group 01 did not clear the enable")
	}
}

// The index only latches in mode group 00, so a mode-group write does not
// scribble over the palette index a program set up beforehand.
func TestTheULAPlusIndexLatchesOnlyInModeGroup00(t *testing.T) {
	u := NewULAPlus()

	u.WriteBF3B(0x2A) // mode 00, index $2A
	if got := u.Index(); got != 0x2A {
		t.Fatalf("index = %#02x, want %#02x", got, 0x2A)
	}
	u.WriteBF3B(0x40 | 0x3F) // mode 01, low bits must be ignored
	if got := u.Index(); got != 0x2A {
		t.Errorf("index = %#02x after a mode-group write, want it unchanged at %#02x "+
			"(zxnext.vhd:4533-4534)", got, 0x2A)
	}
}

// NR$68 bit 3 writes the SAME bit as the port. The register decode has its own
// nr_68_ulap_en commented out in the core precisely because there is only one
// storage location for this (zxnext.vhd:5448 vs :4551).
func TestNR68Bit3WritesTheSameEnableAsThePort(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	u := WireULAPlus(d, &cfg)

	d.WriteReg(0x68, 0x08)
	if !u.Enabled() {
		t.Error("NR$68 bit 3 did not set the ULA+ enable (zxnett.vhd:4551)")
	}
	d.WriteReg(0x68, 0x00)
	if u.Enabled() {
		t.Error("clearing NR$68 bit 3 did not clear the enable")
	}

	// And the port writes the same bit the register just cleared.
	u.WriteBF3B(0x40)
	u.WriteFF3B(0x01)
	if d.ReadReg(0x68)&0x08 == 0 {
		t.Error("NR$68 bit 3 does not read back the enable the port set: the register " +
			"is reproducing what was written to it rather than reporting the real bit")
	}
}

// A reset clears the enable, the mode group and the index.
func TestAResetClearsTheULAPlusState(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	u := WireULAPlus(d, &cfg)

	u.WriteBF3B(0x15) // mode 00, index $15
	u.WriteBF3B(0x40)
	u.WriteFF3B(0x01)
	if !u.Enabled() || u.Index() != 0x15 {
		t.Fatal("the fixture did not set the state it is about to reset")
	}

	d.Reset()
	if u.Enabled() {
		t.Error("the enable survived a reset (zxnext.vhd:4547)")
	}
	if u.Index() != 0 {
		t.Errorf("index = %#02x after reset, want 0", u.Index())
	}
}

// The layer input is not the enable on its own. ULAnext mode takes priority:
//
//	ulap_en_i <= ulap_en_0 and not ulanext_en_0    zxnext.vhd:4246
//
// with the port comment "translate radastan pixel to ula+ palette". With
// ULAnext on, Radastan resolves through the plain palette offset instead.
func TestTheLayerInputIsGatedByULAnext(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	u := WireULAPlus(d, &cfg)

	u.WriteBF3B(0x40)
	u.WriteFF3B(0x01)
	if !cfg.ULAPlus {
		t.Fatal("the enable did not reach the layer")
	}

	d.WriteReg(0x43, 0x01) // NR$43 bit 0: ULAnext enable
	if cfg.ULAPlus {
		t.Error("ULAnext did not gate the layer's ULA+ input (zxnext.vhd:4246)")
	}
	d.WriteReg(0x43, 0x00)
	if !cfg.ULAPlus {
		t.Error("clearing ULAnext did not restore the layer's ULA+ input")
	}
}

// The layer input follows whichever half moves, in either order.
func TestTheLayerInputFollowsBothHalvesInEitherOrder(t *testing.T) {
	d := nextregs.New()
	var cfg lores.Config
	u := WireULAPlus(d, &cfg)

	// ULAnext first, then the enable.
	d.WriteReg(0x43, 0x01)
	u.WriteBF3B(0x40)
	u.WriteFF3B(0x01)
	if cfg.ULAPlus {
		t.Error("enable set while ULAnext was already on should leave the input off")
	}
	d.WriteReg(0x43, 0x00)
	if !cfg.ULAPlus {
		t.Error("clearing ULAnext afterwards did not turn the input on")
	}
}

// Port $FF3B reads the enable back in every mode group except 00, which is the
// palette-data group (zxnext.vhd:4562-4566).
func TestPortFF3BReadsTheEnableOutsideModeGroup00(t *testing.T) {
	u := NewULAPlus()
	u.WriteBF3B(0x40)
	u.WriteFF3B(0x01)

	if got, ok := u.ReadFF3B(); !ok || got&0x01 == 0 {
		t.Errorf("$FF3B read = %#02x ok=%v in mode group 01, want the enable set", got, ok)
	}
	if got, _ := u.ReadFF3B(); got&^0x01 != 0 {
		t.Errorf("$FF3B read = %#02x, want only bit 0 (zxnext.vhd:4566)", got)
	}

	u.WriteBF3B(0x00) // palette group: this read is not the enable
	if _, ok := u.ReadFF3B(); ok {
		t.Error("$FF3B answered the enable in mode group 00, which is palette data")
	}
}
