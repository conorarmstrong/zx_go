package uart

import "fmt"

// DebugLines describes both UARTs for a debugger: the baud prescaler, the frame
// register, the FIFO fill, the status outputs and the outside lines. It reads
// only.
func (b *Block) DebugLines() []string {
	sel := 0
	if b.selectR {
		sel = 1
	}
	out := []string{fmt.Sprintf("selected UART %d  reset %v", sel, b.reset)}
	for u := 0; u < 2; u++ {
		presc := uint32(b.prescMSB[u])<<14 | uint32(b.prescLSB[u])
		baud := "-"
		if presc != 0 {
			baud = fmt.Sprintf("%d", 28000000/presc)
		}
		out = append(out,
			fmt.Sprintf("UART %d  prescaler %d (~%s baud at 28 MHz)  frame $%02X  hw-flow %v", u, presc, baud, b.framing[u], b.HWFlow(u)),
			fmt.Sprintf("  rx fifo %d/512  tx fifo %d/64  rx-avail %v  near-full %v  tx-empty %v  err %v  break %v",
				b.rxFifo[u].stored, b.txFifo[u].stored, b.oRxAvail[u], b.oRxNear[u], b.oTxEmpty[u], b.oRxErr[u], b.oRxBreak[u]),
			fmt.Sprintf("  lines: tx %v  rx-in %v  cts_n %v  rtr_n %v  loopback %v", b.oTx[u], b.rxDrv[u], b.ctsN[u], b.oRtrN[u], b.loop[u]),
		)
	}
	peer := "none"
	if b.peer != nil {
		peer = "attached"
		if b.peerCut {
			peer = "cut off (UART 0 routed to the joystick port)"
		}
	}
	return append(out, "UART 0 peer: "+peer)
}
