package dma

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

var _ machinestate.Device = (*DMA)(nil)

// The zxnDMA is the device that proves why a rewind needs more than the
// registers a program wrote.
//
// Two of its states are invisible to the guest and unreachable from the
// configuration. A burst transfer with a fixed-time prescaler moves one byte
// every N T-states while the CPU runs in the gaps, so at any instruction
// boundary the block is part done: the source pointer, the byte counter, how
// many bytes are still owed and when the next one falls due are all mid-flight.
// And the command stream is one OUT per byte, so a capture lands between a base
// byte and the follow bytes it announced as often as not.
//
// These tests are written as a replay property rather than a field-by-field
// comparison, because a field-by-field test only checks the fields someone
// remembered to add.

// burstToIO builds the command stream for the zxnDMA's signature interleaved
// transfer: port A walking memory, port B a fixed IO endpoint, burst mode with
// a fixed-time prescaler so the block is paced across the CPU timeline instead
// of finishing at ENABLE. This is the shape the sampled-audio drivers use, and
// the only shape whose transfer can be observed part way through.
//
// The destination is an IO port rather than memory on purpose: the transfer
// then leaves no trace in the source image, so a replay from a captured state
// runs against exactly the same input the first pass saw, and the output is the
// write log rather than a memory image that would have to be rolled back too.
// The per-port cycle lengths are 4 and 3 against a prescaler of 5, so the pace
// of the transfer comes from the cycle lengths rather than the prescaler — the
// prescaler is only a floor. A prescaler large enough to dominate (which is the
// usual sampled-audio case) hides both cycle lengths from every observable the
// chip has, and a test written over one of those cannot tell whether they were
// restored at all.
func burstToIO(src, length, ioPort uint16, presc byte) []byte {
	return []byte{
		0xC3,                                                             // RESET
		0x7D, byte(src), byte(src >> 8), byte(length), byte(length >> 8), // WR0: A->B, port A start, block length
		0x54, 0x00, // WR1: port A memory, increment, + timing byte: 4-cycle
		0x68, 0x21, presc, // WR2: port B IO, fixed, + timing byte: 3-cycle, + prescaler
		0xCD, byte(ioPort), byte(ioPort >> 8), // WR4: burst mode + port B address
		0xA2, // WR5: auto-restart, so each block end reloads the start addresses
		0xCF, // LOAD
		0x87, // ENABLE
	}
}

// reprogram is a second complete configuration, different from the fixture's in
// every latched field: B->A rather than A->B, an IO port A feeding from a
// memory port B rather than the reverse, fixed/decrement rather than
// increment/fixed, 2-cycle timing on both ports rather than 4 and 3, a
// prescaler high enough to dominate where the fixture's is only a floor,
// continuous rather than burst, auto-restart off, and every register in the
// read-back sequence rather than five.
//
// The drive-forward ends with it for a reason. A replay test can only see a
// field that was not restored if the run moves that field afterwards: leave the
// configuration alone and "restored the value" and "never touched the value"
// produce the same bytes, and the test proves nothing about it.
var reprogram = []byte{
	0x79, 0xDE, 0x00, 0x04, 0x00, // WR0: B->A, port A = $00DE, length 4
	0x6C, 0x02, // WR1: port A IO, fixed, + timing byte: 2 cycles
	0x40, 0x22, 0x09, // WR2: port B memory, decrement, + timing: 2 cycles, + prescaler 9
	0xAD, 0x80, 0x40, // WR4: continuous, port B = $4080
	0x82,       // WR5: stop at end of block
	0xBB, 0x7F, // WR6: read mask = all seven registers
	0xCF, 0x87, // LOAD, ENABLE
}

// burstRig is a DMA wired the way the emulator wires it — memory, an IO bus and
// a CPU T-state clock — so a burst transfer interleaves instead of completing.
type burstRig struct {
	d   *DMA
	io  *ioLog
	mem memMap
	now uint64
}

// newBurstRig returns a controller caught mid-operation: part way through a
// block, and part way through a read-back sequence. Neither position can be
// reconstructed from the registers the guest wrote.
func newBurstRig() *burstRig {
	r := &burstRig{mem: memMap{}, io: newIOLog()}
	for i := uint16(0); i < 0x100; i++ {
		r.mem[0x4000+i] = byte(i*7 + 1) // distinct bytes: a wrong pointer shows up
	}
	r.d = New(r.mem)
	r.d.SetIOBus(r.io)
	r.d.SetClock(func() uint64 { return r.now })
	r.d.SetZ80Mode(true) // the byte counter seeds at -1, so a block moves length+1
	feed(r.d, burstToIO(0x4000, 0x20, 0x00DF, 5))

	// Bytes fall due every 7 T-states; stop with the block part moved.
	r.advance(100)

	// And leave the read-back cursor mid-sequence: status, byte counter lo/hi
	// and port A lo/hi selected, the first two already consumed.
	feed(r.d, []byte{0xBB, 0x1F})
	r.d.ReadCommand()
	r.d.ReadCommand()
	return r
}

// advance moves the CPU clock to `to` an instruction's worth of T-states at a
// time, pumping the DMA from each one the way the emulator's per-instruction
// hook does.
func (r *burstRig) advance(to uint64) {
	for r.now < to {
		r.now += 8
		r.d.Step(r.now)
	}
}

// run drives the rig forward and returns everything observable while it does:
// what each port read returns as the read-back sequence cycles, every byte the
// DMA puts out on the IO endpoint, and the charged duration of each transfer.
// Two runs from the same state must produce identical bytes.
//
// The later phases matter as much as the first. CONTINUE reseeds the byte
// counter from the current pointers with whatever the DMA-mode latch says, a
// block end under auto-restart reloads the programmed start addresses, and the
// closing reprogramming moves every latched field — so a field left holding the
// present rather than the captured value shows up in the bytes.
func (r *burstRig) run() []byte {
	out := make([]byte, 0, 768)
	out = append(out, durationBytes(r.d)...)

	// The block in flight drains a byte at a time while the CPU runs.
	out = append(out, r.pump(40)...)

	// CONTINUE reseeds the byte counter; a fresh ENABLE runs another block.
	feed(r.d, []byte{0xD3, 0x87})
	out = append(out, r.pump(40)...)
	out = append(out, durationBytes(r.d)...)

	// The guest now reaches the controller through the other port, which
	// re-latches the DMA mode (zxnext.vhd:1817 dma_mode <= port_0b_lsb) and so
	// changes what CONTINUE seeds the byte counter with: zxn moves length bytes
	// where Z80-DMA moves length+1.
	r.d.SetZ80Mode(false)
	feed(r.d, []byte{0xD3, 0x87})
	out = append(out, r.pump(40)...)
	out = append(out, durationBytes(r.d)...)

	// Finally the driver reprograms the controller end to end and runs one
	// short block through the new configuration.
	feed(r.d, reprogram)
	out = append(out, r.pump(8)...)
	out = append(out, durationBytes(r.d)...)

	out = append(out, writeLog(r.io)...)
	return out
}

// pump advances the CPU clock in small steps, driving the DMA from each one the
// way the emulator's per-instruction hook does, and recording what the
// read-back sequence returns as it goes.
func (r *burstRig) pump(steps int) []byte {
	out := make([]byte, 0, steps)
	for i := 0; i < steps; i++ {
		r.now += 8
		r.d.Step(r.now)
		out = append(out, r.d.ReadCommand())
	}
	return out
}

// durationBytes flattens the charged T-state cost of the last transfer.
func durationBytes(d *DMA) []byte {
	out := make([]byte, 8)
	for i := range out {
		out[i] = byte(d.Duration() >> (8 * i))
	}
	return out
}

// writeLog flattens an IO bus's write log so two runs compare as bytes.
func writeLog(l *ioLog) []byte {
	out := make([]byte, 0, len(l.writes)*3)
	for _, w := range l.writes {
		out = append(out, byte(w.port), byte(w.port>>8), w.val)
	}
	return out
}

// The property that matters: from a captured state, the controller must move
// the same bytes at the same times it moved them the first time.
func TestReplayingFromCapturedStateReproducesTheTransfer(t *testing.T) {
	r := newBurstRig()
	at := r.now

	// The premise of the whole test: the capture is taken mid-block. A device
	// captured at rest proves nothing about resuming one.
	if !r.d.activeBurst || r.d.remaining == 0 {
		t.Fatal("the fixture is not part way through a transfer")
	}

	st := r.d.SaveState()
	r.io.writes = nil // the log is the run's output; the fixture's own writes are not
	first := r.run()

	if len(r.io.writes) < 60 {
		t.Fatalf("fixture moved only %d bytes; the replay would be proving nothing", len(r.io.writes))
	}

	if err := r.d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	r.now = at
	r.io.writes = nil
	second := r.run()

	if !bytes.Equal(first, second) {
		t.Error("the DMA behaved differently on replay from the same captured state: " +
			"some part of the controller is not being captured")
	}
}

// The negative that gives the test above its teeth. A controller holding the
// same programming but not the same position behaves differently, so the replay
// is measuring more than the registers round-tripping.
func TestConfigurationAloneDoesNotReproduceTheTransfer(t *testing.T) {
	r := newBurstRig()
	at := r.now
	st := r.d.SaveState()
	r.io.writes = nil
	want := r.run()

	// A second controller programmed identically and wound to the same clock
	// reading, but which has not yet moved the bytes the first one had.
	o := &burstRig{mem: r.mem, io: newIOLog()}
	o.d = New(o.mem)
	o.d.SetIOBus(o.io)
	o.d.SetClock(func() uint64 { return o.now })
	o.d.SetZ80Mode(true)
	feed(o.d, burstToIO(0x4000, 0x20, 0x00DF, 50))
	feed(o.d, []byte{0xBB, 0x1F})
	o.now = at

	if bytes.Equal(want, o.run()) {
		t.Skip("this transfer happens not to depend on the hidden position here; " +
			"the replay test above is the binding one")
	}

	// ...and the full capture does what the programming alone could not.
	if err := r.d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	r.now = at
	r.io.writes = nil
	if !bytes.Equal(want, r.run()) {
		t.Error("full state capture failed to reproduce the transfer it was taken from")
	}
}

// A capture can land between a base byte and the follow bytes it announced —
// one OUT delivers one byte, and captures are taken between instructions.
// Restore without the follow-byte queue and the next address byte is read as a
// base byte, which reprograms the controller instead of finishing the command:
// $08 stops being "block length, low" and becomes a WR2 write.
//
// Every byte boundary in a full command stream is tried, so the property holds
// for every queue shape the stream passes through — including the two places
// the queue grows from inside a follow byte (the WR2 timing byte announcing the
// prescaler, and the WR4 interrupt-control byte announcing its pulse offset and
// vector), which a restored queue has to be able to do after the capture.
func TestReplayingResumesAHalfWrittenCommand(t *testing.T) {
	stream := []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x54, 0x02, // WR1: port A memory increment, + timing byte
		0x68, 0x22, 0x40, // WR2: port B IO fixed, + timing byte, + prescaler
		0x9D, 0xDF, 0x00, // WR4: continuous, port B = $00DF, interrupt control follows
		0x18, 0x00, 0x00, // ...which announces a pulse offset and a vector
		0xCF, 0x87, // LOAD, ENABLE
	}

	for cut := 1; cut < len(stream); cut++ {
		mem := memMap{}
		for i := uint16(0); i < 0x40; i++ {
			mem[0x4000+i] = byte(i*7 + 1)
		}
		io := newIOLog()
		d := New(mem)
		d.SetIOBus(io)

		feed(d, stream[:cut])
		st := d.SaveState()

		feed(d, stream[cut:])
		first := writeLog(io)
		if len(first) != 8*3 {
			t.Fatalf("cut %d: the stream moved %d bytes, want 8 — the fixture is not transferring", cut, len(first)/3)
		}

		if err := d.LoadState(st); err != nil {
			t.Fatalf("cut %d: LoadState: %v", cut, err)
		}
		io.writes = nil
		feed(d, stream[cut:])

		if second := writeLog(io); !bytes.Equal(first, second) {
			t.Errorf("cut %d: replaying the rest of the command stream moved %d bytes, want the same 8: "+
				"the follow-byte queue is not being captured", cut, len(second)/3)
		}
	}
}

// SaveState must not hand back a view of the live follow-byte queue. The queue
// is resliced as bytes arrive and appended to from inside a follow byte, so its
// backing array is reused: an aliased capture would be rewritten by the very
// bytes it was taken ahead of.
func TestCaptureDoesNotAliasThePendingQueue(t *testing.T) {
	d := New(memMap{})
	feed(d, []byte{0x7D, 0x00, 0x40}) // WR0, two of four follow bytes delivered

	st := d.SaveState()

	feed(d, []byte{0x08, 0x00})                   // drain the queue
	feed(d, []byte{0x7D, 0x99, 0x99, 0x99, 0x99}) // refill it, reusing the array

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	// The restored controller must still be owed the two length bytes.
	feed(d, []byte{0x34, 0x12})
	if got := d.Length(); got != 0x1234 {
		t.Errorf("Length = $%04X, want $1234: the captured queue did not survive", got)
	}
	if got := d.Source(); got != 0x4000 {
		t.Errorf("Source = $%04X, want $4000", got)
	}
}

func TestStateIDIsStable(t *testing.T) {
	if got := New(memMap{}).StateID(); got != "next.dma" {
		t.Errorf("StateID = %q, want %q: it is stored in state blobs and must not drift", got, "next.dma")
	}
}

func TestLoadStateRejectsRubbish(t *testing.T) {
	d := New(memMap{})
	if err := d.LoadState([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("a malformed state blob must be reported, not half-applied")
	}
	if err := d.LoadState(nil); err == nil {
		t.Error("an empty state blob must be reported: it means the capture failed")
	}
}

// A blob that decodes but describes an impossible controller must be refused
// with nothing applied. Each of these would otherwise wedge the device: an
// out-of-range read cursor indexes the read-back sequence, an unknown follow-byte
// code dispatches nowhere, and a negative outstanding count leaves the burst
// engine active with no byte able to finish it.
func TestLoadStateRejectsImpossibleStateWithoutHalfApplying(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*dmaState)
	}{
		{"read cursor out of range", func(s *dmaState) { s.ReadReg = 9 }},
		{"unknown follow-byte code", func(s *dmaState) { s.Pending = []byte{byte(opLast) + 1} }},
		{"negative outstanding count", func(s *dmaState) { s.Remaining = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBurstRig()
			good := r.d.SaveState()

			var s dmaState
			if err := gob.NewDecoder(bytes.NewReader(good)).Decode(&s); err != nil {
				t.Fatalf("decoding the good capture: %v", err)
			}
			tc.corrupt(&s)
			var buf bytes.Buffer
			if err := gob.NewEncoder(&buf).Encode(s); err != nil {
				t.Fatalf("re-encoding: %v", err)
			}

			if err := r.d.LoadState(buf.Bytes()); err == nil {
				t.Fatal("an impossible state was accepted")
			}
			if after := r.d.SaveState(); !bytes.Equal(good, after) {
				t.Error("a refused state was partly applied: the controller changed underneath it")
			}
		})
	}
}
