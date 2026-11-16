package ctc

import "fmt"

var stateNames = [...]string{"reset", "reset/tc", "trigger", "run", "run/tc"}

// DebugLines describes the four channels for a debugger. It reads only.
func (b *Bank) DebugLines() []string {
	var out []string
	for i := 0; i < NumChannels; i++ {
		c := b.Channel(i)
		if c == nil {
			continue
		}
		mode := "timer"
		if c.controlReg&0x08 != 0 { // control_reg(3) = D6 counter mode
			mode = "counter"
		}
		presc := 16
		if c.controlReg&0x04 != 0 { // control_reg(2) = D5 prescaler 256
			presc = 256
		}
		st := "?"
		if int(c.state) < len(stateNames) {
			st = stateNames[c.state]
		}
		out = append(out, fmt.Sprintf("ch%d  %-8s %-7s presc %3d  tc %3d  count %3d  int %v  zc/to %v",
			i, st, mode, presc, c.timeConst, c.tCount, c.IntEnabled(), c.zcToD))
	}
	return out
}
