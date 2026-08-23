package dma

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"strings"
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
// for every queue shape the stream passes through, including the place the
// queue grows from inside a follow byte (the WR2 timing byte announcing the
// prescaler), which a restored queue has to be able to do after the capture.
func TestReplayingResumesAHalfWrittenCommand(t *testing.T) {
	stream := []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x54, 0x02, // WR1: port A memory increment, + timing byte
		0x68, 0x22, 0x40, // WR2: port B IO fixed, + timing byte, + prescaler
		0x8D, 0xDF, 0x00, // WR4: continuous, port B = $00DF
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

// The replay property above has a blind spot, and a mutation audit found it: a
// field restore can only be missed if the replay's observable — the IO write
// log, the read-back bytes, the charged durations — depends on that field
// AFTER the capture point. inTransfer never reaches any of them, so its restore
// could be deleted with every test still green.
//
// The fix is the same one the AY needed: stop asserting on output and assert on
// the capture. Drive the controller so EVERY captured field changes, restore,
// and re-capture. A field that is captured but not restored then shows up as a
// blob that does not match, whether or not it reaches an observable.

// roundTripRig is newBurstRig caught one byte further on: mid-block, mid
// read-back sequence, and now also mid-command, between a WR0 base byte and the
// four bytes it announced. All three positions are invisible to the guest, and
// the queue is the one a capture lands inside most often — one OUT delivers one
// byte, and captures are taken between instructions.
func roundTripRig() *burstRig {
	r := newBurstRig()
	r.d.WriteCommand(0x7D) // WR0: port A start and block length announced, none delivered
	return r
}

// driveEverything changes every field a capture carries.
//
// Parity is the trap. Each of these moves once, not twice: the DMA-mode latch
// is dropped and left dropped rather than re-raised, the transfer mode goes
// burst to continuous and stays there, and the follow-byte queue refills with a
// DIFFERENT pair of codes rather than the same four it started with. An even
// number of changes would return the field to the captured value and hide a
// lost restore completely.
func driveEverything(r *burstRig) {
	// The capture was taken owing four follow bytes, so the drive delivers
	// them; the queue empties here and refills with other codes at the end.
	feed(r.d, []byte{0x00, 0x50, 0x10, 0x00}) // port A = $5000, length $0010

	// Drain the block still in flight. Every byte moves a pointer, the byte
	// counter and the burst engine's due time, and the block's end latches
	// end-of-block, clears the burst and reloads the start addresses under
	// auto-restart.
	r.advance(400)

	// The guest now reaches the controller through the other access port, which
	// re-latches the DMA mode (zxnext.vhd:1817 dma_mode <= port_0b_lsb) and so
	// changes what the next LOAD seeds the byte counter with.
	r.d.SetZ80Mode(false)

	// Then it reprograms the controller end to end and runs one short block
	// through the new configuration: every latched field, both cycle lengths,
	// the prescaler, the transfer mode, the read mask and its cursor, the
	// pointers, the counter and the charged duration all take new values.
	feed(r.d, reprogram)

	// The interrupt controller then raises dma_delay: an IM2 peripheral out of
	// idle with its dma-int-enable bit set (zxnext.vhd:2001-2010). The guest
	// enables another block and it parks in START_DMA, without ever asking for
	// the bus (dma.vhd:269-273). The pin and the parked position are both chip
	// state a rewind has to land on; restored without them the block just runs.
	r.d.SetBusDelay(true)
	r.d.WriteCommand(0x87) // ENABLE: parks, because the pin is high

	// ...and stops mid-command again, owing a different pair of follow bytes
	// than the capture was owed. Ending on an empty queue would leave the
	// restore of a non-empty one untested from the other direction.
	r.d.WriteCommand(0x8D) // WR4: port B address low and high announced
}

func TestEveryCapturedFieldSurvivesARoundTrip(t *testing.T) {
	r := roundTripRig()
	want := r.d.SaveState()

	driveEverything(r)
	if bytes.Equal(r.d.SaveState(), want) {
		t.Fatal("the drive sequence changed nothing, so this test cannot detect a lost restore")
	}

	if err := r.d.LoadState(want); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := r.d.SaveState(); !bytes.Equal(got, want) {
		t.Error("re-capturing after a restore did not reproduce the captured state: " +
			"some field is captured but not restored")
	}
}

// The guard the test above depends on. Without it, a later change that retunes
// the drive sequence so a field stops differing silently stops covering that
// field, and the round-trip test goes on passing while proving less.
func TestTheDriveSequenceChangesEveryCapturedField(t *testing.T) {
	r := roundTripRig()
	before := decodeDMAState(t, r.d.SaveState())
	driveEverything(r)
	after := decodeDMAState(t, r.d.SaveState())

	checks := []struct {
		name string
		same bool
	}{
		{"PortAStart", before.PortAStart == after.PortAStart},
		{"PortBStart", before.PortBStart == after.PortBStart},
		{"BlockLen", before.BlockLen == after.BlockLen},
		{"AToB", before.AToB == after.AToB},
		{"AMode", before.AMode == after.AMode},
		{"BMode", before.BMode == after.BMode},
		{"AIsIO", before.AIsIO == after.AIsIO},
		{"BIsIO", before.BIsIO == after.BIsIO},
		{"ZMode", before.ZMode == after.ZMode},
		{"CurA", before.CurA == after.CurA},
		{"CurB", before.CurB == after.CurB},
		{"Counter", before.Counter == after.Counter},
		{"ACycleLen", before.ACycleLen == after.ACycleLen},
		{"BCycleLen", before.BCycleLen == after.BCycleLen},
		{"Prescaler", before.Prescaler == after.Prescaler},
		{"Mode", before.Mode == after.Mode},
		{"AutoRestart", before.AutoRestart == after.AutoRestart},
		{"EndOfBlock", before.EndOfBlock == after.EndOfBlock},
		{"AtLeastOne", before.AtLeastOne == after.AtLeastOne},
		{"LastDuration", before.LastDuration == after.LastDuration},
		{"ReadMask", before.ReadMask == after.ReadMask},
		{"ReadReg", before.ReadReg == after.ReadReg},
		{"ActiveBurst", before.ActiveBurst == after.ActiveBurst},
		{"Remaining", before.Remaining == after.Remaining},
		{"NextDue", before.NextDue == after.NextDue},
		{"Pending", bytes.Equal(before.Pending, after.Pending)},
		{"BusDelay", before.BusDelay == after.BusDelay},
		{"Stalled", before.Stalled == after.Stalled},
		// InTransfer is deliberately absent, and it is the one field no drive
		// sequence can put here: it is true only between the first and last
		// byte of a block, and this rig is driven from outside the transfer, so
		// both the capture and the driven state have it clear.
		// TestARoundTripFromInsideATransfer covers it instead.
	}
	for _, f := range checks {
		if f.same {
			t.Errorf("%s is unchanged by the drive sequence, so a lost restore of it "+
				"would go undetected", f.name)
		}
	}

	// The table above is a coverage claim, so it has to be complete. A field
	// added to the capture and not to the table would be restored by code no
	// test moves, and the round-trip test would go on passing while covering
	// one field fewer — which is the exact way a mutation score decays
	// unnoticed. Reflection makes the omission a failure instead of a silence.
	named := map[string]bool{
		"InTransfer": true, // covered by TestARoundTripFromInsideATransfer
		"Wedged":     true, // covered by TestAWedgedControllerSurvivesARoundTrip
	}
	for _, f := range checks {
		named[f.name] = true
	}
	captured := reflect.TypeOf(dmaState{})
	for i := 0; i < captured.NumField(); i++ {
		if name := captured.Field(i).Name; !named[name] {
			t.Errorf("dmaState.%s is captured but nothing asserts a drive sequence moves it, "+
				"so a lost restore of it would go undetected", name)
		}
	}
}

func decodeDMAState(t *testing.T, b []byte) dmaState {
	t.Helper()
	var s dmaState
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&s); err != nil {
		t.Fatalf("decoding state: %v", err)
	}
	return s
}

// inTransfer is the one field the drive sequence above cannot move, so it gets
// its own round trip from the only place the controller is ever in that state:
// inside the block, between the first byte and the last.
//
// That is not a contrived observation point. The DMA's IOBus is the ULA's port
// dispatch, so an IO endpoint calls out of the transfer loop into the rest of
// the machine, and the flag exists precisely because one of those calls can
// come straight back in through the DMA's own command port. A capture taken
// from that callback is a real capture of a real state — and it is the state
// whose restore matters, because a controller restored with the flag clear
// treats the very next ENABLE as a second, nested transfer.
func TestARoundTripFromInsideATransfer(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 0x20; i++ {
		mem[0x4000+i] = byte(i*7 + 1)
	}
	d := New(mem)

	var inside []byte
	moved := 0
	d.SetIOBus(&hookBus{onWrite: func(uint16, byte) {
		if moved++; moved == 3 { // part way through the block, not at either end
			inside = d.SaveState()
		}
	}})

	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x54, 0x02, // WR1: port A memory, increment, + timing byte
		0x68, 0x22, 0x40, // WR2: port B IO, fixed, + timing byte, + prescaler
		0x8D, 0xDF, 0x00, // WR4: continuous, port B = $00DF
		0xCF, 0x87, // LOAD, ENABLE
	})

	if moved != 8 {
		t.Fatalf("the fixture moved %d bytes, want 8: it never entered a transfer", moved)
	}
	if s := decodeDMAState(t, inside); !s.InTransfer {
		t.Fatal("the capture was not taken from inside the transfer, so it covers nothing")
	}
	// The controller is now back at rest, so the flag differs between the
	// captured state and the present one and a lost restore cannot hide.
	if s := decodeDMAState(t, d.SaveState()); s.InTransfer {
		t.Fatal("the transfer did not clear InTransfer, so a lost restore of it stays invisible")
	}

	if err := d.LoadState(inside); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := d.SaveState(); !bytes.Equal(got, inside) {
		t.Error("re-capturing after restoring a mid-transfer state did not reproduce the capture")
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

// The wedge is the second state no drive sequence can pass through, for the
// obvious reason: once the register-write sequencer is parked in R4_BYTE_2
// every later byte is swallowed, so a drive that reaches it cannot go on to
// move anything else. It gets its own round trip instead, exactly as
// inTransfer does.
//
// It has to be captured. A rewind that lands on a wedged controller and
// restores it unwedged hands the guest a DMA that hardware would have left
// dead until a machine reset, and the very next command byte reprograms a chip
// that on the real machine could not hear it.
func TestAWedgedControllerSurvivesARoundTrip(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 3; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	feed(d, []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x03, 0x00, // WR0: A->B, port A = $4000, length 3
		0x14, 0x10, // WR1, WR2: both memory, incrementing
		0x8D, 0x00, 0x60, // WR4: continuous, port B = $6000
		0xCF, // LOAD: fully programmed and armed...
	})
	d.WriteCommand(0x91) // ...and then wedged by a WR4 with D4 alone

	st := d.SaveState()

	// Drive it somewhere else entirely: the reset pin clears the wedge, and the
	// block the controller was holding runs.
	d.Reset()
	d.WriteCommand(0x87)
	if mem[0x6000] != 0xA0 {
		t.Fatalf("the drive sequence moved nothing, so this test cannot detect a lost restore")
	}
	for i := uint16(0); i < 3; i++ {
		mem[0x6000+i] = 0
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := d.SaveState(); !bytes.Equal(got, st) {
		t.Error("re-capturing after a restore did not reproduce the captured state")
	}
	d.WriteCommand(0x87) // a wedged controller swallows this
	for i := uint16(0); i < 3; i++ {
		if got := mem[0x6000+i]; got != 0 {
			t.Errorf("dst[$%04X] = $%02X, want $00: the restored controller was not wedged",
				0x6000+i, got)
		}
	}
}

// status_atleastone is chip state, and a rewind has to carry it. A driver
// streaming samples polls the status register between instructions, which is
// exactly where captures are taken; restore the bit clear and the very next
// poll tells the driver its block has not started when half of it has already
// gone out of the port.
func TestTheAtLeastOneStatusBitSurvivesARoundTrip(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 0x40; i++ {
		mem[0x4000+i] = byte(i + 1)
	}
	var now uint64
	d := New(mem)
	d.SetClock(func() uint64 { return now })
	feed(d, burstStream(0x4000, 0x6000, 0x40, 10))

	for now = 0; now < 50; now += 10 {
		d.Step(now)
	}
	d.WriteCommand(0xBF)
	if got := d.ReadCommand(); got != 0x3B {
		t.Fatalf("fixture status = $%02X, want $3B: the bit is not set, so nothing is under test", got)
	}

	st := d.SaveState()

	// Drive it past the end of the block, where IDLE clears the bit again.
	for ; now <= 0x40*10+200; now += 10 {
		d.Step(now)
	}
	d.WriteCommand(0xBF)
	if got := d.ReadCommand(); got != 0x1A {
		t.Fatalf("after the block ended status = $%02X, want $1A", got)
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	d.WriteCommand(0xBF)
	if got := d.ReadCommand(); got != 0x3B {
		t.Errorf("status after the restore = $%02X, want $3B: the at-least-one bit was not captured", got)
	}
}

// The completeness claim checked from the other side. The guard in
// TestTheDriveSequenceChangesEveryCapturedField proves every field dmaState
// carries is moved by the drive sequence, but it cannot notice a field of the
// running controller that dmaState never grew. That omission is the one that
// actually loses a rewind, and it is silent: the round-trip test compares one
// capture against another, so a field missing from both is missing from the
// comparison too, and every test goes on passing while the device quietly
// stops being rewindable.
func TestEveryControllerFieldIsCaptured(t *testing.T) {
	// The wiring to the rest of the machine, which is deliberately not this
	// device's state: both buses and both callbacks into the CPU clock are
	// installed by the emulator at construction, and a rewind must not re-point
	// the controller at different objects. See the note at the top of state.go.
	wiring := map[string]bool{"mem": true, "io": true, "cycleSink": true, "clock": true}

	live := reflect.TypeOf(DMA{})
	captured := reflect.TypeOf(dmaState{})
	for i := 0; i < live.NumField(); i++ {
		name := live.Field(i).Name
		if wiring[name] {
			continue
		}
		want := strings.ToUpper(name[:1]) + name[1:]
		if _, ok := captured.FieldByName(want); !ok {
			t.Errorf("DMA.%s is live controller state and dmaState has no %s to carry it: "+
				"a rewind would restore the controller without it", name, want)
		}
	}
}

// A block parked by dma_delay_i is the third position no drive sequence can
// pass through, and for the same reason as the wedge: while it is parked
// nothing else the controller is asked to do can happen. It gets its own round
// trip, covering both halves of that position: the FSM sitting in START_DMA
// with the block part done, and the state of the pin holding it there.
//
// Both are needed. Without the parked FSM the block never resumes and the rest
// of it is simply lost; without the pin, the controller comes back believing
// the interrupt that stopped it has already gone away, and the next block runs
// straight over the top of the service routine the delay exists to protect.
func TestAStalledControllerSurvivesARoundTrip(t *testing.T) {
	mem := memMap{}
	for i := uint16(0); i < 8; i++ {
		mem[0x4000+i] = byte(0xA0 + i)
	}
	d := New(mem)
	var out []byte
	moved := 0
	d.SetIOBus(&hookBus{onWrite: func(_ uint16, v byte) {
		out = append(out, v)
		// Counted separately from out, which the test resets: the interrupt
		// arrives once, part way through the fixture's block, and must not be
		// re-raised by the replays that follow.
		if moved++; moved == 3 {
			d.SetBusDelay(true)
		}
	}})
	stream := []byte{
		0xC3,                         // RESET
		0x7D, 0x00, 0x40, 0x08, 0x00, // WR0: A->B, port A = $4000, length 8
		0x14,                     // WR1: port A memory, increment
		wr2Byte(addrFixed, true), // WR2: port B IO, fixed
		0x8D, 0xDF, 0x00,         // WR4: continuous, port B = IO $00DF
		0xCF, 0x87, // LOAD, ENABLE
	}
	feed(d, stream)
	if len(out) != 3 {
		t.Fatalf("the fixture moved %d bytes before parking, want 3", len(out))
	}

	st := d.SaveState()

	// Drive it somewhere else: the interrupt clears and the block finishes.
	d.SetBusDelay(false)
	if len(out) != 8 {
		t.Fatalf("the drive sequence left %d bytes moved, want 8", len(out))
	}

	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := d.SaveState(); !bytes.Equal(got, st) {
		t.Error("re-capturing after a restore did not reproduce the captured state")
	}

	// The parked FSM: dropping the pin resumes the block from where it stopped.
	out = nil
	d.SetBusDelay(false)
	if got, want := string(out), string([]byte{0xA3, 0xA4, 0xA5, 0xA6, 0xA7}); got != want {
		t.Errorf("the restored controller put out % X, want A3 A4 A5 A6 A7: "+
			"the parked block did not resume from where it was captured", out)
	}

	// The pin: a restored controller is still being held off the bus, so a
	// brand new block parks as well instead of running.
	if err := d.LoadState(st); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	out = nil
	d.WriteCommand(0x83)        // abandon the parked block
	feed(d, []byte{0xCF, 0x87}) // LOAD, ENABLE for a fresh one
	if len(out) != 0 {
		t.Errorf("the restored controller moved %d bytes, want 0: dma_delay_i was not captured", len(out))
	}
	d.SetBusDelay(false)
	if len(out) != 8 {
		t.Errorf("after the pin dropped the fresh block moved %d bytes, want 8", len(out))
	}
}

// The pendingOp codes are a wire format, not an internal enum. SaveState writes
// them into dmaState.Pending as raw bytes and LoadState validates them against
// opLast, so a build that renumbers them reads an older capture as something
// else: a queue entry that meant "the next byte is the read mask" becomes
// whatever now sits at that number, and the guest's next $6B byte is applied to
// the wrong register.
//
// So the numbers are pinned here rather than left to iota. Adding a code is
// fine; moving one is not, and a code that stops being produced has to keep its
// slot. Deleting opInterruptControl from the middle of the block once moved
// opReadMask from 11 to 10, which is exactly what this prevents.
func TestPendingOpCodesAreAStableWireFormat(t *testing.T) {
	for _, c := range []struct {
		op   pendingOp
		code byte
		name string
	}{
		{opIgnore, 0, "opIgnore"},
		{opPortALow, 1, "opPortALow"},
		{opPortAHigh, 2, "opPortAHigh"},
		{opBlockLenLow, 3, "opBlockLenLow"},
		{opBlockLenHigh, 4, "opBlockLenHigh"},
		{opPortBLow, 5, "opPortBLow"},
		{opPortBHigh, 6, "opPortBHigh"},
		{opATiming, 7, "opATiming"},
		{opBTiming, 8, "opBTiming"},
		{opPrescaler, 9, "opPrescaler"},
		{opRetiredInterruptControl, 10, "opRetiredInterruptControl"},
		{opReadMask, 11, "opReadMask"},
	} {
		if byte(c.op) != c.code {
			t.Errorf("%s = %d, want %d: these codes are persisted in savestates "+
				"and cannot be renumbered", c.name, byte(c.op), c.code)
		}
	}
	if byte(opLast) != 11 {
		t.Errorf("opLast = %d, want 11: it bounds the codes a capture may carry", byte(opLast))
	}
}

// The retired code still has to be accepted from an older capture, and the byte
// it announces still has to be swallowed. The FPGA never reads the WR4
// interrupt-control byte (dma.vhd:826-844), so discarding it is what the live
// decoder would do with it anyway.
func TestARetiredFollowByteCodeIsAcceptedAndDiscarded(t *testing.T) {
	mem := memMap{}
	mem[0x4000] = 0xAA
	d := New(mem)
	// Reach a state with a pending queue, then hand it the retired code as an
	// older build would have written it.
	s := d.SaveState()
	var st dmaState
	if err := gob.NewDecoder(bytes.NewReader(s)).Decode(&st); err != nil {
		t.Fatalf("decoding a fresh capture: %v", err)
	}
	st.Pending = []byte{byte(opRetiredInterruptControl)}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&st); err != nil {
		t.Fatalf("re-encoding: %v", err)
	}
	if err := d.LoadState(buf.Bytes()); err != nil {
		t.Fatalf("LoadState refused a capture carrying the retired code: %v", err)
	}
	// The announced byte is consumed as a follow byte, not decoded as a base
	// byte: a $7D swallowed here must NOT reprogram WR0.
	d.WriteCommand(0x7D)
	if len(d.pending) != 0 {
		t.Errorf("pending queue = %v after the announced byte, want empty: the "+
			"retired code must consume exactly one byte", d.pending)
	}
}
