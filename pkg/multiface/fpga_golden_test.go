package multiface

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestMultifaceMatchesFPGAGolden replays the Multiface golden — captured from
// the real FPGA device/multiface.vhd (the NMI / invisible / mf_enable state
// machine) simulated under GHDL — through the FPGA-faithful Core and asserts
// that the three module outputs (mf_enabled_o, nmi_disable_o, mf_port_en_o)
// match the hardware exactly, cycle for cycle. Tooling: _tools/multiface-vhdl-test/.
//
// Each golden "S" line is one rising-edge clock of the module: the input signal
// vector that was present at that edge, followed by the outputs sampled just
// after it. The replay drives Core.Clock with the same input vector and asserts
// the sampled outputs match.
//
// Golden line format (see tb_multiface.vhd):
//
//	S mode=<n> a=<addr8_hex> iord=<0|1> iowr=<0|1> button=<0|1> m1=<0|1>
//	  mreq=<0|1> a0066=<0|1> retn=<0|1> en=<0|1> | mfen=<b> nmidis=<b> porten=<b>
//
// The mode is set by the preceding "; RESET" group (carried on the S line).
// The decoded 8-bit address "a" plus iord/iowr tells us which of the four
// pre-decoded port strobes (enable_rd/wr, disable_rd/wr) the module saw — we
// reconstruct them here exactly as zxnext.vhd:2612-2616,2730-2733 do.
func TestMultifaceMatchesFPGAGolden(t *testing.T) {
	f, err := os.Open("testdata/multiface_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	kv := func(fields []string, key string) int {
		for _, fld := range fields {
			if strings.HasPrefix(fld, key+"=") {
				return atoi(fld[len(key)+1:])
			}
		}
		return -1
	}
	hexv := func(fields []string, key string) int {
		for _, fld := range fields {
			if strings.HasPrefix(fld, key+"=") {
				n, _ := strconv.ParseUint(fld[len(key)+1:], 16, 16)
				return int(n)
			}
		}
		return -1
	}
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}

	// per-mode port addresses (zxnext.vhd:2612-2613)
	enableAddr := func(mode int) int {
		switch {
		case mode&0x2 != 0:
			return 0x9F
		case mode&0x1 != 0:
			return 0xBF
		default:
			return 0x3F
		}
	}
	disableAddr := func(mode int) int {
		switch {
		case mode&0x2 != 0:
			return 0x1F
		case mode&0x1 != 0:
			return 0x3F
		default:
			return 0xBF
		}
	}

	var c *Core

	sc := bufio.NewScanner(f)
	ln, steps := 0, 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// "; RESET" begins a fresh device (the module's reset pulse). Mode is
		// read from the first S line of the group.
		if strings.HasPrefix(line, ";") {
			if strings.Contains(line, "RESET") {
				c = nil // will be (re)created on the next S line's mode
			}
			continue
		}
		fld := strings.Fields(line)
		if fld[0] != "S" {
			t.Fatalf("line %d: unknown golden op %q", ln, fld[0])
		}

		mode := kv(fld, "mode")
		addr := hexv(fld, "a")
		iord := kv(fld, "iord") != 0
		iowr := kv(fld, "iowr") != 0
		button := kv(fld, "button") != 0
		m1 := kv(fld, "m1")     // m1_n
		mreq := kv(fld, "mreq") // mreq_n
		a0066 := kv(fld, "a0066") != 0
		retn := kv(fld, "retn") != 0
		en := kv(fld, "en") != 0

		wantMF := kv(fld, "mfen")
		wantNMI := kv(fld, "nmidis")
		wantPort := kv(fld, "porten")

		if c == nil {
			c = NewCore()
			c.SetMode(byte(mode))
			c.Reset() // the do_reset group already pulsed reset; model that here
		}
		c.SetMode(byte(mode))
		c.SetEnable(en)

		// Reconstruct the four pre-decoded port strobes exactly as the FPGA
		// feeder does (zxnext.vhd:2615-2616 address compare + 2730-2733 io
		// direction gate). enable_i must also be set (the io-enable gate).
		var in Inputs
		in.Button = button
		in.A0066 = a0066
		in.M1n = m1 != 0
		in.MReqn = mreq != 0
		in.RetnSeen = retn
		if en {
			isEnableAddr := addr == enableAddr(mode)
			isDisableAddr := addr == disableAddr(mode)
			in.PortEnableRd = iord && isEnableAddr
			in.PortEnableWr = iowr && isEnableAddr
			in.PortDisableRd = iord && isDisableAddr
			in.PortDisableWr = iowr && isDisableAddr
		}

		c.Clock(in)

		gotMF := b2i(c.MFEnabled())
		gotNMI := b2i(c.NMIDisable())
		gotPort := b2i(c.MFPortEn())
		if gotMF != wantMF || gotNMI != wantNMI || gotPort != wantPort {
			t.Errorf("line %d mode=%d a=%02X iord=%d iowr=%d btn=%d a0066=%d retn=%d en=%d: "+
				"got mfen=%d nmidis=%d porten=%d; want mfen=%d nmidis=%d porten=%d",
				ln, mode, addr, b2i(iord), b2i(iowr), b2i(button), b2i(a0066), b2i(retn), b2i(en),
				gotMF, gotNMI, gotPort, wantMF, wantNMI, wantPort)
		}
		steps++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if steps < 100 {
		t.Fatalf("expected the full golden replay, only saw %d steps", steps)
	}
}
