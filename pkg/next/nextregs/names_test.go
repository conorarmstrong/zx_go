package nextregs

import "testing"

func TestNameKnownAndUnknown(t *testing.T) {
	cases := map[byte]string{
		0x00: "Machine ID",
		0x07: "CPU Speed",
		0x0B: "Joystick I/O Mode",
		0x50: "MMU slot 0",
		0x57: "MMU slot 7",
		0xA8: "ESP GPIO0 Output Enable",
		0xC0: "Interrupt Control",
		0xCC: "DMA INT EN 0",
		0x83: "Internal Port Enables 1",
	}
	for r, want := range cases {
		if got := Name(r); got != want {
			t.Errorf("Name($%02X) = %q, want %q", r, got, want)
		}
	}
	if got := Name(0xE0); got != "" {
		t.Errorf("Name($E0) = %q, want empty", got)
	}
}

func TestDecode(t *testing.T) {
	cases := []struct {
		r    byte
		v    byte
		want string
	}{
		{0x07, 0x33, "actual=28MHz programmed=28MHz"},
		{0x07, 0x00, "actual=3.5MHz programmed=3.5MHz"},
		{0xC0, 0xAD, "vector=5 stackless im=2 hw-im2"},
		{0xC0, 0x00, "vector=0 im=0"},
		{0x0B, 0xA1, "io-mode mode=uart uart1"},
		{0x0B, 0x01, "mode=bit-bang uart1"},
		{0xCC, 0x83, "nmi line ula"},
		{0xCC, 0x00, "-"},
		{0x15, 0x07, "layers=LSU over-border sprites"},
	}
	for _, c := range cases {
		if got := Decode(c.r, c.v); got != c.want {
			t.Errorf("Decode($%02X, $%02X) = %q, want %q", c.r, c.v, got, c.want)
		}
	}
	if got := Decode(0xE0, 0x12); got != "" {
		t.Errorf("Decode of a register with no fields = %q, want empty", got)
	}
}

func TestPeekRunsTheReadHandlerWithoutTracing(t *testing.T) {
	d := New()
	d.SetOnRead(0x40, func(*Dispatcher) byte { return 0x5A })
	traced := false
	d.SetTracer(func(byte, byte, bool) { traced = true })
	if got := d.Peek(0x40); got != 0x5A {
		t.Errorf("Peek = $%02X, want the handler's $5A", got)
	}
	if traced {
		t.Error("Peek fired the tracer")
	}
}
