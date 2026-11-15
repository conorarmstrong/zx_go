package uart

// ESPBaudPrescaler is the divisor for 115200 baud at the 28 MHz system clock
// (uart.vhd:136), the ESP8266 AT firmware's default rate.
const espPrescaler = 243

// ESP is the far end of UART 0: the ESP8266 module on the Next's board. It
// listens to the guest's o_Tx_0 line and drives i_Rx_0 through the same serial
// PHY the FPGA uses, fixed at 115200 baud 8N1 like the module's AT firmware,
// and answers through the AT-command responder in uart.go.
//
// Because it sits on the line and not on a register, a guest that programs a
// different baud rate or frame gets what a real module would give it:
// garbage in, and nothing sensible back. There is no Wi-Fi behind it (see
// doc.go).
type ESP struct {
	rx   *RX
	tx   *TX
	app  *UART
	line bool // last o_Tx_0 level seen
	out  bool // level driven onto i_Rx_0
}

// NewESP returns a module with an idle line.
func NewESP() *ESP {
	e := &ESP{rx: NewRX(), tx: NewTX(), app: New(), line: true, out: true}
	e.rx.SetFrame(0x18)
	e.rx.SetPrescaler(espPrescaler)
	e.tx.SetFrame(0x18)
	e.tx.SetPrescaler(espPrescaler)
	e.tx.SetCTS(true)
	return e
}

// SetVersion sets the AT+GMR identity string.
func (e *ESP) SetVersion(v string) { e.app.SetVersion(v) }

// Clock advances one i_CLK given the level the guest is transmitting, and
// returns the level to drive onto i_Rx_0.
func (e *ESP) Clock(guestTx bool) bool {
	e.line = guestTx
	e.rx.Clock(guestTx)
	if e.rx.Avail() {
		e.app.WriteData(e.rx.Byte())
	}
	if !e.tx.Busy() && e.app.Status()&StatusRXReady != 0 {
		e.tx.Send(e.app.ReadData())
	}
	e.out = e.tx.Clock()
	return e.out
}

// Quiescent reports a module that another Clock with the line unchanged would
// leave unchanged: both PHYs idle and nothing waiting to be sent.
func (e *ESP) Quiescent() bool {
	return e.rx.idleStable(e.line) && e.tx.idleStable() && e.out &&
		e.app.Status()&StatusRXReady == 0
}
