package memory

import "testing"

// Core 3.02.05 narrows the boot ROM to $0000-$1FFF (zxnext.vhd on the 30205
// branch: `cpu_a(15 downto 13) = "000" and bootrom_en`, where 3.02.03 had
// `cpu_a(15 downto 14) = "00"`), so $2000-$3FFF reads the SRAM decode, which
// in config mode is the NR$04 page.
func TestBootROMWindow(t *testing.T) {
	for _, c := range []struct {
		name    string
		lowOnly bool
		want    byte
	}{{"3.02.03", false, 0xB0}, {"3.02.05", true, 0x5A}} {
		t.Run(c.name, func(t *testing.T) {
			m := newNextMem(t)
			rom := make([]byte, 0x2000)
			for i := range rom {
				rom[i] = 0xB0
			}
			m.SetFPGABootROM(rom)
			m.SetFPGABootROMLowOnly(c.lowOnly)
			m.SetConfigModeRAMPage(0x10) // RAM bank 0 in the config window
			m.Write(0x2100, 0x5A)        // config mode: writes reach the page
			if got := m.Read(0x0100); got != 0xB0 {
				t.Errorf("$0100 = %#02x, want the boot ROM", got)
			}
			if got := m.Read(0x2100); got != c.want {
				t.Errorf("$2100 = %#02x, want %#02x", got, c.want)
			}
		})
	}
}
