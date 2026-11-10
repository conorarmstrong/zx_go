package next

import (
	"github.com/conorarmstrong/zx_go/pkg/next/nextregs"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// WireInterruptEnable1 installs the NextReg $C5 OnWrite handler.
//
// NR$C5 is INT EN 1: bits 3:0 are the live CTC channels' interrupt
// enables. Bits 7:4 name CTC channels 4..7, which the FPGA does not
// instantiate (zxnext.vhd:4067 NUM_CTC => 4, :4092-4093 tie the top four
// off); they read as zero.
func WireInterruptEnable1(d *nextregs.Dispatcher) {
	d.SetOnWrite(0xC5, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0xC5, val&0x0F)
	})
}

// WireIM2 connects the daisy chain to the CPU and to the NextReg
// interrupt-enable/status registers. It must run after
// WireInterruptControl (NR$C0), WireInterruptEnable0 (NR$C4),
// WireInterruptEnable1 (NR$C5) and WireInterruptEnable2 (NR$C6), because
// it chains onto their handlers rather than replacing them.
//
// The chain is inert until the guest sets NR$C0 bit 0 (hardware IM2
// mode). In pulse mode the existing frame/line IRQPending path is
// unchanged; the hooks below merely latch requests in a chain that is
// held in reset, exactly as the FPGA does.
func WireIM2(d *nextregs.Dispatcher, im2 *IM2Driver, cpu *z80.CPU) {
	if im2 == nil {
		return
	}

	// NR$C0 bit 0 = hardware IM2 mode; bits 7:5 = vector base.
	chainOnWrite(d, 0xC0, func(val byte) {
		on := val&0x01 != 0
		im2.SetMode(on)
		im2.SetVectorBase(val)
		if cpu != nil {
			cpu.IRQExternal = on
		}
	})

	// The ULA and line enables are not NR$C4 state of their own:
	// ula_int_en <= nr_22_line_interrupt_en & not port_ff_interrupt_disable
	// (zxnext.vhd:6711), fed to the chain at :1949-1950. NR$22 bit 2 is
	// port_ff_interrupt_disable (:3619-3621, reset to 0, so the frame INT
	// is enabled at reset) and NR$22 bit 1 is nr_22_line_interrupt_en.
	// NR$C4 bits 1:0 read back the same signals (:6239) and write them
	// through NR$22 (see WireInterruptEnable0), so both registers re-derive
	// the chain from NR$22's storage here.
	syncULAEnables := func() {
		nr22 := d.Raw(0x22)
		im2.SetEnabled(IM2SourceULA, nr22&0x04 == 0)
		im2.SetEnabled(IM2SourceLine, nr22&0x02 != 0)
		// Keep the NR$C4 read-back (bits 1:0 = ula_int_en) in step.
		var alias byte
		if nr22&0x04 == 0 {
			alias |= 0x01
		}
		if nr22&0x02 != 0 {
			alias |= 0x02
		}
		d.Store(0xC4, d.Raw(0xC4)&^0x03|alias)
	}
	chainOnWrite(d, 0x22, func(byte) { syncULAEnables() })
	chainOnWrite(d, 0xC4, func(byte) { syncULAEnables() })
	syncULAEnables()

	// NR$C5: CTC channels 0..3.
	chainOnWrite(d, 0xC5, func(val byte) {
		for ch := 0; ch < 4; ch++ {
			im2.SetEnabled(IM2SourceCTC0+ch, val&(1<<ch) != 0)
		}
	})

	// NR$C6: UART0/1, with Rx-available and Rx-half-full sharing the
	// same peripheral request line (nextreg.txt 0xC6).
	chainOnWrite(d, 0xC6, func(val byte) {
		im2.SetEnabled(IM2SourceUART0R, val&0x03 != 0)
		im2.SetEnabled(IM2SourceUART0T, val&0x04 != 0)
		im2.SetEnabled(IM2SourceUART1R, val&0x30 != 0)
		im2.SetEnabled(IM2SourceUART1T, val&0x40 != 0)
	})

	// NR$C8/C9/CA: sticky status. Reads reflect the chain's current
	// pending bits; writes clear the selected bits (write-1-to-clear).
	d.SetOnRead(0xC8, func(_ *nextregs.Dispatcher) byte {
		var v byte
		if im2.Status(IM2SourceULA) {
			v |= 0x01
		}
		if im2.Status(IM2SourceLine) {
			v |= 0x02
		}
		return v
	})
	d.SetOnWrite(0xC8, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0xC8, val&0x03)
		if val&0x01 != 0 {
			im2.ClearStatus(IM2SourceULA)
		}
		if val&0x02 != 0 {
			im2.ClearStatus(IM2SourceLine)
		}
	})
	d.SetOnRead(0xC9, func(_ *nextregs.Dispatcher) byte {
		var v byte
		for ch := 0; ch < 4; ch++ {
			if im2.Status(IM2SourceCTC0 + ch) {
				v |= 1 << uint(ch)
			}
		}
		return v
	})
	d.SetOnWrite(0xC9, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0xC9, val&0x0F)
		for ch := 0; ch < 4; ch++ {
			if val&(1<<uint(ch)) != 0 {
				im2.ClearStatus(IM2SourceCTC0 + ch)
			}
		}
	})
	d.SetOnRead(0xCA, func(_ *nextregs.Dispatcher) byte {
		var v byte
		if im2.Status(IM2SourceUART0R) {
			v |= 0x03 // Rx available + Rx half full share the line
		}
		if im2.Status(IM2SourceUART0T) {
			v |= 0x04
		}
		if im2.Status(IM2SourceUART1R) {
			v |= 0x30
		}
		if im2.Status(IM2SourceUART1T) {
			v |= 0x40
		}
		return v
	})
	d.SetOnWrite(0xCA, func(disp *nextregs.Dispatcher, val byte) {
		disp.Store(0xCA, val&0x77)
		if val&0x03 != 0 {
			im2.ClearStatus(IM2SourceUART0R)
		}
		if val&0x04 != 0 {
			im2.ClearStatus(IM2SourceUART0T)
		}
		if val&0x30 != 0 {
			im2.ClearStatus(IM2SourceUART1R)
		}
		if val&0x40 != 0 {
			im2.ClearStatus(IM2SourceUART1T)
		}
	})

	if cpu == nil {
		return
	}

	// The chain's /INT is the CPU's IRQ latch in hardware IM2 mode.
	// Set-only on assertion: the CPU clears its own latch when it
	// accepts, and a still-pending chain re-asserts on the next request
	// edge. Clearing here would race the CPU's accept path.
	im2.SetINTListener(func(active bool) {
		// Only the hardware-IM2 mode owns the latch. In pulse mode the
		// frame/line path sets it directly and this callback must not
		// clear that latch.
		if im2.HWIM2() {
			cpu.IRQPending.Store(active)
		}
	})
	cpu.IntLineActive = im2.INTAsserted
	cpu.SetINTAckHook(im2.Acknowledge)
	cpu.SetRETISeenHook(im2.RETISeen)

	// The ULA frame/line pulses are the two sources the CPU itself
	// generates. Raise them at the FPGA's pulse point; the chain's
	// request latch holds the edge until the CPU acknowledges.
	cpu.SetFrameIntHook(func() { im2.Raise(IM2SourceULA); im2.Lower(IM2SourceULA) })
	cpu.SetLineIntHook(func() { im2.Raise(IM2SourceLine); im2.Lower(IM2SourceLine) })
}
