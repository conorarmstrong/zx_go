package testharness

import (
	"bytes"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
)

// Capture and resume across a real +3 disk load.
//
// pkg/plus3fdc's own state tests drive the controller through its port
// functions, which is the only way to reach the flags no ordinary transfer
// sets. What they cannot do is prove the capture is enough to resume a load
// that a guest is actually in the middle of — the machine around the controller
// is not there, and a transfer nobody is consuming does not resume.
//
// So this is the other half: the disk built in plus3_bootdisk_test.go, booted
// by the real +3 ROM, stopped part way through a disk operation, captured,
// restored, and let run to the end. The claim is the one that matters for
// rewind: the load finished with exactly the bytes it would have finished with
// had it never been interrupted.
//
// The comparison is over RAM and the controller, not over the CPU and ULA
// counters. Those count frames, and the restored run's tail is a fraction of a
// frame shorter than the original's — a difference in how long the machine
// spun after the load, not in what the load did. RAM and the controller stop
// changing the moment the guest program reaches its spin loop, so they are
// comparable across the two tails; PC is asserted separately, which is what
// says the processor resumed at all.

// capturePoint is a place in the guest program to stop and take a state. Each
// one leaves the controller holding something different, and between them they
// cover a latched seek result, a half-delivered command, a multi-sector
// transfer at two depths, and a result phase part way through being read.
type capturePoint struct {
	name  string
	label string // a label in the boot program
	hit   int    // stop the nth time the CPU is about to execute it

	// wantStatus is the main status register at the stop. It is a guard, not a
	// result: a capture point that stopped landing mid-operation would still
	// pass the resume comparison — a machine that was doing nothing resumes
	// perfectly — and would silently stop testing anything.
	wantStatus byte
	// holds says what the controller is carrying there, for the failure message.
	holds string
}

// Main status register bit patterns, as the +3 reads them from port $2FFD.
const (
	msrIdle    = 0x80 // RQM: accepting a command, nothing in progress
	msrCmd     = 0x90 // RQM|CB: part way through a command's parameter bytes
	msrExec    = 0xF0 // RQM|DIO|EXM|CB: execution phase, FDC to CPU
	msrResults = 0xD0 // RQM|DIO|CB: result phase
)

var plus3CapturePoints = []capturePoint{
	{
		name: "seek result latched but unread", label: "seekdone", hit: 1,
		wantStatus: msrIdle,
		holds:      "the completed seek's ST0 and PCN, which no port shows",
	},
	{
		name: "part way through the READ DATA command bytes", label: "sendcmd", hit: 5,
		wantStatus: msrCmd,
		holds:      "half a command in the parameter buffer",
	},
	{
		name: "early in the multi-sector transfer", label: "exloop", hit: 200,
		wantStatus: msrExec,
		holds:      "199 bytes into the first sector of four",
	},
	{
		name: "late in the multi-sector transfer", label: "exloop", hit: 1600,
		wantStatus: msrExec,
		holds:      "1599 bytes in, with R already stepped to the fourth sector",
	},
	{
		// The sixth arrival at the result reader is the second of the READ
		// DATA result's seven bytes — the first four belong to the two SENSE
		// INTERRUPTs. One byte in rather than three because the result buffer
		// is only partly overwritten by a two-byte result: stop late enough and
		// the bytes still to be read are the ones a stale buffer would hand
		// over anyway, and the capture proves nothing.
		name: "part way through the result phase", label: "resloop", hit: 6,
		wantStatus: msrResults,
		holds:      "one of the seven result bytes already taken",
	},
}

// TestPlus3DiskLoadResumesIdenticallyAfterACapture is the rewind property.
func TestPlus3DiskLoadResumesIdenticallyAfterACapture(t *testing.T) {
	path := buildPlus3BootDisk(t)
	_, labels := bootProgram(t)

	for _, cp := range plus3CapturePoints {
		t.Run(cp.name, func(t *testing.T) {
			stop, ok := labels[cp.label]
			if !ok {
				t.Fatalf("boot program has no label %q to stop at", cp.label)
			}
			h := newPlus3WithDisk(t, path)

			// The machine, and the subset of it the two runs are compared over.
			// Two registries rather than one because Restore is all-or-nothing
			// over exactly the devices registered: the load cannot resume
			// without the CPU and the memory map, but the comparison must not
			// include the CPU's frame counters.
			machine := machinestate.New()
			machine.Register(h.CPU(), h.MemoryBus(), h.ULA(), h.Peripherals().Plus3FDC())
			observed := machinestate.New()
			observed.Register(h.MemoryBus(), h.Peripherals().Plus3FDC())

			var snap machinestate.State
			var atCapture []byte
			var atStop byte
			hits := 0
			taken := false
			h.CPU().AddPreFetchHook("plus3-capture", func(pc uint16) {
				if taken || pc != stop {
					return
				}
				hits++
				if hits != cp.hit {
					return
				}
				atStop, _ = h.Peripherals().Plus3FDC().HandlePortRead(fdcStatusPort)
				snap = machine.Capture()
				atCapture = observed.Capture().Bytes()
				taken = true
			})

			h.RunFrames(menuFrames)
			h.TapKey("Return")
			for i := 0; i < loadFrames && !taken; i++ {
				h.RunFrames(1)
			}
			h.CPU().RemovePreFetchHook("plus3-capture")
			if !taken {
				t.Fatalf("never reached %s hit %d: the load did not run\nscreen:\n%s",
					cp.label, cp.hit, h.ScreenText())
			}
			if atStop != cp.wantStatus {
				t.Fatalf("stopped with main status $%02X, want $%02X — "+
					"the capture point no longer lands mid-operation, so it proves nothing",
					atStop, cp.wantStatus)
			}

			// Finish the interrupted run and record what it produced.
			h.RunFrames(tailFrames)
			assertPayloadLoaded(t, h)
			want := observed.Capture().Bytes()

			// NEGATIVE CONTROL, and without it this test proves nothing.
			//
			// A completed load is an ATTRACTOR: running on from the finished
			// state simply stays finished. So comparing the end of the resumed
			// run against the end of the first run passes identically whether
			// Restore rewound the machine or did nothing at all — verified by
			// replacing Restore with a no-op, which left every subtest green.
			//
			// Two assertions fix that. The mid-load state must differ from the
			// finished state, or the capture point is not mid-load at all; and
			// immediately after Restore the machine must be back AT that
			// mid-load state, which is the part a no-op cannot fake.
			if bytes.Equal(atCapture, want) {
				t.Fatalf("the capture point is byte-identical to the completed load, " +
					"so resuming from it would prove nothing")
			}
			if err := machine.Restore(snap); err != nil {
				t.Fatalf("restore: %v", err)
			}
			if got := observed.Capture().Bytes(); !bytes.Equal(got, atCapture) {
				t.Fatalf("after Restore the machine is not back at the capture point: " +
					"the rewind did not happen, and everything below would pass anyway")
			}

			h.RunFrames(tailFrames + 1)

			if got := observed.Capture().Bytes(); !bytes.Equal(got, want) {
				t.Errorf("resuming from %q did not reproduce the load\n"+
					"(the controller was holding %s)\n"+
					"what the resumed guest recorded: %s",
					cp.name, cp.holds, guestReport(h))
			}
			assertPayloadLoaded(t, h)
			if pc := h.CPU().PC; pc != labels["spin"] {
				t.Errorf("after the resumed load PC is $%04X, want the guest's spin loop at $%04X",
					pc, labels["spin"])
			}
		})
	}
}

// tailFrames is how long the guest needs from the deepest capture point to
// finish the load and settle into its spin loop. The restored run gets one
// frame more, because Restore lands it part way through a frame that the
// original run had already begun.
const tailFrames = 20

// TestPlus3CapturePointsEachHoldDifferentControllerState is the guard the
// machinestate package asks for: a mutation score means nothing unless the
// fixture moves the fields, and a capture point that holds the same controller
// state as the one before it adds no coverage however many times it runs.
//
// It compares the controller's captured bytes at each stop. Identical blobs
// would mean two points are the same moment in the transfer wearing different
// names.
func TestPlus3CapturePointsEachHoldDifferentControllerState(t *testing.T) {
	path := buildPlus3BootDisk(t)
	_, labels := bootProgram(t)

	seen := map[string]string{}
	for _, cp := range plus3CapturePoints {
		h := newPlus3WithDisk(t, path)
		stop := labels[cp.label]
		var blob []byte
		hits := 0
		h.CPU().AddPreFetchHook("plus3-guard", func(pc uint16) {
			if blob != nil || pc != stop {
				return
			}
			hits++
			if hits != cp.hit {
				return
			}
			blob = append([]byte(nil), h.Peripherals().Plus3FDC().SaveState()...)
		})
		h.RunFrames(menuFrames)
		h.TapKey("Return")
		for i := 0; i < loadFrames && blob == nil; i++ {
			h.RunFrames(1)
		}
		h.CPU().RemovePreFetchHook("plus3-guard")
		if blob == nil {
			t.Fatalf("%s: never reached the capture point", cp.name)
		}
		if prev, dup := seen[string(blob)]; dup {
			t.Errorf("%q captures the same controller state as %q, so it tests nothing new",
				cp.name, prev)
		}
		seen[string(blob)] = cp.name
	}
}
