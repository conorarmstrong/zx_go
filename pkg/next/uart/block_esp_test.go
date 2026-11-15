package uart

import (
	"reflect"
	"testing"
)

// io drives register accesses the way the machine does: the strobe held for
// two clocks, the read sampled on the last.
type io struct{ b *Block }

func (a io) wr(reg uint8, v byte) {
	a.b.SetIO(reg, false, true, v)
	a.b.Clock()
	a.b.Clock()
	a.b.SetIO(reg, false, false, v)
	a.b.Clock() // real accesses are instructions apart: the Tx FIFO write
	a.b.Clock() // needs the strobe to fall before the next one rises
}

func (a io) rd(reg uint8) byte {
	a.b.SetIO(reg, true, false, 0)
	a.b.Clock()
	a.b.Clock()
	v := a.b.CPUData()
	a.b.SetIO(reg, false, false, 0)
	a.b.Clock()
	a.b.Clock()
	return v
}

func (a io) send(s string) {
	for i := 0; i < len(s); i++ {
		a.wr(3, s[i])
	}
}

// drain reads every byte the Rx FIFO holds.
func (a io) drain() string {
	var out []byte
	for a.rd(3)&0x01 != 0 {
		out = append(out, a.rd(0))
	}
	return string(out)
}

func newBlockWithESP() (*Block, io) {
	b := NewBlock()
	b.SetPeer(NewESP())
	b.SetReset(true)
	b.Clock()
	b.Clock()
	b.SetReset(false)
	return b, io{b}
}

// An idle Block does no work: ten million clocks must cost a handful of
// executed ones.
func TestAdvanceSkipsAQuietBlock(t *testing.T) {
	b, _ := newBlockWithESP()
	ran := 0
	b.Advance(10_000_000, func(int) { ran++ })
	if ran > 64 {
		t.Errorf("a quiet Block executed %d of 10,000,000 clocks", ran)
	}
}

// The module on UART 0 answers an AT probe at 115200 baud through the real
// register block, bit by bit, with the power-up prescaler.
func TestESPAnswersATProbeAtPowerUpBaud(t *testing.T) {
	b, a := newBlockWithESP()
	a.send("AT\r")
	b.Advance(60_000, nil) // 3 bytes out, 4 back, at 243 clocks per bit
	if got := a.drain(); got != "OK\r\n" {
		t.Errorf("reply to AT: %q, want %q", got, "OK\r\n")
	}
}

// UART 1 has the Pi behind it and nothing is attached in this emulator, so
// anything sent there goes nowhere and nothing comes back.
func TestUART1HasNoPeer(t *testing.T) {
	b, a := newBlockWithESP()
	a.wr(1, 0x40) // select UART 1
	a.send("AT\r")
	b.Advance(60_000, nil)
	if got := a.drain(); got != "" {
		t.Errorf("UART 1 answered %q with no peer attached", got)
	}
}

// The module is fixed at 115200 baud, as a real one is, so a guest programmed
// to a different rate is not understood.
func TestESPDoesNotAnswerAtTheWrongBaud(t *testing.T) {
	b, a := newBlockWithESP()
	a.wr(0, 0x00|100&0x7F) // prescaler LSB: 100
	a.wr(0, 0x80)          // prescaler bits 13:7: 0
	a.send("AT\r")
	b.Advance(60_000, nil)
	if got := a.drain(); got == "OK\r\n" {
		t.Error("the module understood a guest at the wrong baud rate")
	}
}

// A frame error on the line is reported, not swallowed: send a byte with a
// 7N1 frame to a module expecting 8N1.
func TestESPAnswersTwoCommandsInARow(t *testing.T) {
	b, a := newBlockWithESP()
	a.send("AT\r")
	b.Advance(60_000, nil)
	first := a.drain()
	a.send("AT+GMR\r")
	b.Advance(200_000, nil)
	second := a.drain()
	if first != "OK\r\n" || len(second) < 4 || second[len(second)-4:] != "OK\r\n" {
		t.Errorf("replies %q then %q", first, second)
	}
}

// Advance must be indistinguishable from clocking: run the same conversation
// with the ESP both ways and require every transition of both serial lines to
// land on the same clock, and the Blocks to end in the same state.
func TestAdvanceMatchesClockingWithAPeer(t *testing.T) {
	type edge struct {
		clk  int
		line int
		v    bool
	}
	run := func(advance bool) (*Block, []edge) {
		b, a := newBlockWithESP()
		a.send("AT\r")
		var edges []edge
		prevTx, prevRx := true, true
		clk := 0
		observe := func() {
			if b.Tx(0) != prevTx {
				prevTx = b.Tx(0)
				edges = append(edges, edge{clk, 0, prevTx})
			}
			if b.peerLine != prevRx {
				prevRx = b.peerLine
				edges = append(edges, edge{clk, 1, prevRx})
			}
		}
		for chunk := 0; chunk < 30; chunk++ {
			base := clk
			if advance {
				b.Advance(5000, func(i int) { clk = base + i + 1; observe() })
			} else {
				for i := 0; i < 5000; i++ {
					b.Clock()
					clk = base + i + 1
					observe()
				}
			}
			clk = base + 5000
			if chunk == 14 {
				a.send("AT+GMR\r")
				clk += 28 // the sends are 28 clocks of access
			}
		}
		return b, edges
	}
	slow, slowEdges := run(false)
	fast, fastEdges := run(true)
	if len(slowEdges) < 40 {
		t.Fatalf("setup: only %d line transitions in the conversation", len(slowEdges))
	}
	if len(fastEdges) != len(slowEdges) {
		t.Fatalf("%d line transitions with Advance, %d clocking", len(fastEdges), len(slowEdges))
	}
	for i := range slowEdges {
		if fastEdges[i] != slowEdges[i] {
			t.Fatalf("transition %d: Advance %+v, clocking %+v", i, fastEdges[i], slowEdges[i])
		}
	}
	slow.quiet, fast.quiet, slow.executed, fast.executed = 0, 0, 0, 0
	// An idle transmitter's baud timer free-runs and nothing reads it: it is
	// reloaded on every state change (uart_tx.vhd:133-142), the only way out
	// of idle, so skipping idle clocks leaves it at a different value and no
	// consumer can tell.
	for u := 0; u < 2; u++ {
		if slow.tx[u].state == txIdle && fast.tx[u].state == txIdle {
			slow.tx[u].txTimer, fast.tx[u].txTimer = 0, 0
		}
	}
	se, fe := slow.peer.(*ESP), fast.peer.(*ESP)
	if se.tx.state == txIdle && fe.tx.state == txIdle {
		se.tx.txTimer, fe.tx.txTimer = 0, 0
	}
	if d := firstDiff("block", reflect.ValueOf(slow), reflect.ValueOf(fast)); d != "" {
		t.Errorf("final state differs: %s", d)
	}
	if fast.Executed() >= slow.Executed() && slow.Executed() != 0 {
		t.Log("note: Advance did not skip anything in this conversation")
	}
}

// spyPeer records the level the block drives at it.
type spyPeer struct{ sawLow bool }

func (p *spyPeer) Clock(blockTx bool) bool {
	if !blockTx {
		p.sawLow = true
	}
	return true
}
func (p *spyPeer) Quiescent() bool { return true }

// With the peer cut off (UART 0 routed to the joystick port) it sees an idle
// line however much the block transmits, and the block takes Rx from SetRx.
func TestPeerCutHidesTheTxLineAndTheRxLine(t *testing.T) {
	for _, cut := range []bool{false, true} {
		b := NewBlock()
		p := &spyPeer{}
		b.SetPeer(p)
		b.SetPeerCut(cut)
		a := io{b}
		a.wr(3, 0x55)
		for i := 0; i < 5000; i++ {
			b.Clock()
		}
		if p.sawLow == cut {
			t.Errorf("cut=%v: peer saw a low level = %v", cut, p.sawLow)
		}
	}
}
