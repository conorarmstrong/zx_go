package testharness

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/machinestate"
	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// The +3 disk controller's capture, put to a real commercial loader.
//
// pkg/plus3fdc's own state tests drive the controller through a script we
// wrote, and a script only reaches the paths whoever wrote it thought of. A
// commercial +3 loader does not work from that list: it recalibrates, seeks,
// senses the interrupt afterwards, drains a seven-byte result phase it only
// half wanted, reads multi-sector runs across a cylinder, and checks the CRC
// the controller left behind — all from code nobody here wrote, at timings
// nobody here chose. Capturing a machine in the middle of that and resuming it
// is the strongest evidence available that the capture is complete.
//
// The media stays where it belongs. The manifest is
// testdata/local_titles.tsv, which is gitignored and shared with
// TestScreenLocalTitles: tab-separated name, path, model, frames, with # for
// comments. This test reads the plus3 rows. No title, path or image from it
// ever enters the repository.
//
// # A skip here is not a pass
//
// CI has no commercial disks and never will, so CI always skips this file.
// That means a green run of this package is NOT evidence that the +3 FDC
// capture works, and every skip below says so in as many words. This is worth
// the noise: the compatibility work in this package has already been bitten
// once by a verdict that was logged and never asserted, where a green suite
// meant less than it looked like it meant.
//
// # What the experiment does
//
// Boot a title, run until the controller is mid-command at a frame boundary,
// and capture the whole machine. Run a window of frames and record what every
// device holds at the end of it. Restore the capture, run the same window, and
// require every device to hold exactly the same bytes. Then, as the negative,
// restore everything EXCEPT the controller and require the run to come out
// different — otherwise the first comparison passed for reasons that had
// nothing to do with the FDC.
//
// Two things are controlled for deliberately.
//
// The media is reset between runs. Captures do not carry the mounted disk, by
// design (pkg/plus3fdc/state.go says why: rewinding emulated time does not
// un-write a sector any more than it un-ejects a disk). A title that writes
// inside the window would therefore make the second run read back different
// bytes, and the divergence would be blamed on the capture. Re-inserting the
// image before each replay removes that variable. The re-insert has to come
// first and the restore second, because restoring is what re-resolves the
// controller's live track pointer against the media now mounted.
//
// Weak sectors are not reproducible, and that is the medium, not the capture:
// every read of a byte flagged weak returns fresh randomness, which is what a
// real copy-protected sector does on every revolution. A protected title can
// therefore replay differently for a legitimate reason. The two cases are told
// apart by running the replay twice: a lost restore lands in the same wrong
// place every time, while randomness does not, so a replay that disagrees with
// itself is reported as unusable media rather than as a failure.
//
// # What this reaches, measured
//
// Deleting each of the 38 field restores in UPD765.loadState in turn, this test
// catches 8: phase, resultLen, resultIdx, ioPos, ioEnd, ioCRC, pcn, and the
// re-resolution of the live track pointer. Those are exactly the fields a
// commercial loader depends on across the moment of capture, and the phase one
// is caught as a panic in the controller's own command handling rather than as
// the assertion below — an inconsistent phase and command length is a state no
// running machine can reach.
//
// The other 30 survive for two reasons, neither of which a bigger fixture
// fixes. Some are re-established by the loader before it uses them: it writes
// all nine command bytes again for every read, so cmdBuf, cmdLen, us and hd
// converge back to the captured values on their own. The rest belong to
// commands a game loader never issues — FORMAT, SCAN, READ DELETED DATA — and
// no amount of real media will reach them. Widening the fixture from three
// titles at two capture points to eight at three killed nothing further, and
// sweeping all 62 local plus3 titles only adds titles, not fields.
// pkg/plus3fdc's own state tests are what cover the remaining 30; this test
// covers the ones a script would have to have thought of.

// unprovenTail is the tail of every skip in this file.
const unprovenTail = "\n\nA SKIP HERE IS NOT A PASS. Nothing about the +3 FDC capture " +
	"has been verified by this run. The check needs commercial disk media, which is not in " +
	"the repository and never will be, so CI always lands here — a green suite is NOT " +
	"evidence that capturing and restoring a machine mid-load works. It becomes evidence " +
	"only on a machine that has the media, listed in pkg/testharness/testdata/local_titles.tsv " +
	"(gitignored) with at least one plus3 row: name<TAB>path<TAB>plus3<TAB>frames."

const (
	// The +3 powers on into its ROM menu and starts the disk when ENTER
	// selects the highlighted Loader entry, which is the same sequence
	// Screening uses for a .dsk.
	fdcCaptureBootFrames = 200
	fdcCaptureMenuFrames = 50

	// How long to hunt for a frame boundary with the controller mid-command,
	// and how much guest time a replay covers. A hundred frames is two
	// seconds of load: several sectors, at least one seek, and the result
	// phases between them on every title measured.
	fdcCaptureScanFrames = 1200
	fdcCaptureWindow     = 100

	// A bound on the default run, because the manifest is a game collection
	// and can be any length. ZX_GO_FDC_CAPTURE=all sweeps every plus3 row.
	fdcCaptureTitles    = 3
	fdcCaptureRowBudget = 12
)

// fdcCapturePoints are the mid-command frame boundaries to capture at, counted
// from the moment the loader starts. The first is +3DOS opening the disk; the
// later one is usually the title's own loader, which is the code worth
// catching. Both are kept low enough that titles with a short load still reach
// them.
var fdcCapturePoints = []int{1, 4}

// fdcCaptureRig is one booted +3 with a title in drive A, plus the two device
// registries the experiment compares.
type fdcCaptureRig struct {
	h    *Harness
	fdc  *plus3fdc.Plus3FDC
	path string

	// all is the machine as cmd/zx_go would register it for this model: CPU,
	// memory, ULA, AY, keyboard and the controller. sansFDC is the same set
	// with the controller left out, which is how the negative control gets a
	// restore that reaches everything else and deliberately misses one device.
	all     *machinestate.Registry
	sansFDC *machinestate.Registry
	devices []machinestate.Device
}

func newFDCCaptureRig(t *testing.T, path string) *fdcCaptureRig {
	t.Helper()
	h, err := New(roms.ModelPlus3)
	if err != nil {
		t.Skipf("+3 harness: %v", err)
	}
	t.Cleanup(h.CloseFiles)
	fdc := h.Peripherals().Plus3FDC()
	if fdc == nil {
		t.Fatal("the +3 harness has no floppy controller, so there is nothing here to capture")
	}
	if err := h.InsertPlus3Disk(0, path); err != nil {
		t.Skipf("this title's image did not load: %v", err)
	}
	h.Reboot()
	h.RunFrames(fdcCaptureBootFrames)
	h.TapKey("Return")
	h.RunFrames(fdcCaptureMenuFrames)

	r := &fdcCaptureRig{h: h, fdc: fdc, path: path}
	r.devices = []machinestate.Device{h.CPU(), h.MemoryBus(), h.ULA(), h.ULA().AY(), h.kbd, fdc}
	r.all = machinestate.New()
	r.all.Register(r.devices...)
	r.sansFDC = machinestate.New()
	r.sansFDC.Register(r.devices[:len(r.devices)-1]...)
	return r
}

// runToMidCommand advances to the nth frame boundary at which the controller is
// busy with a command, and reports the main status register there. Reading the
// status port is the one question that can be asked of a µPD765 without
// changing it.
func (r *fdcCaptureRig) runToMidCommand(nth int) (byte, bool) {
	seen := 0
	for i := 0; i < fdcCaptureScanFrames; i++ {
		r.h.RunFrames(1)
		st, ok := r.fdc.HandlePortRead(0x2FFD)
		if !ok {
			return 0, false
		}
		if st&0x10 == 0 { // MSR CB: the controller is between commands
			continue
		}
		if seen++; seen == nth {
			return st, true
		}
	}
	return 0, false
}

// blobs is what every device holds right now, device by device rather than as
// one machine-wide encoding, so a failure can name the device that diverged.
func (r *fdcCaptureRig) blobs() map[string][]byte {
	out := make(map[string][]byte, len(r.devices))
	for _, d := range r.devices {
		out[d.StateID()] = append([]byte(nil), d.SaveState()...)
	}
	return out
}

// replay puts the machine back to a capture and runs the window again, with the
// image freshly re-inserted so the media is the same for every run.
func (r *fdcCaptureRig) replay(t *testing.T, s machinestate.State) map[string][]byte {
	t.Helper()
	if err := r.h.InsertPlus3Disk(0, r.path); err != nil {
		t.Fatalf("re-inserting the image between runs: %v", err)
	}
	if err := r.all.Restore(s); err != nil {
		t.Fatalf("restoring the captured machine: %v", err)
	}
	r.h.RunFrames(fdcCaptureWindow)
	return r.blobs()
}

// differingDevices names the devices whose bytes differ, in a stable order.
func differingDevices(a, b map[string][]byte) []string {
	var out []string
	for id, av := range a {
		if !bytes.Equal(av, b[id]) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// TestLocalPlus3LoadResumesFromAMidLoadCapture is the whole experiment. Read
// the file comment above before reading a green result as anything.
func TestLocalPlus3LoadResumesFromAMidLoadCapture(t *testing.T) {
	rows, present := readLocalTitles(t)
	if !present {
		t.Skip("no testdata/local_titles.tsv, so no commercial +3 media to load." + unprovenTail)
	}
	var titles []localTitle
	for _, row := range rows {
		if row.model != roms.ModelPlus3 {
			continue
		}
		if _, err := os.Stat(row.path); err != nil {
			continue
		}
		titles = append(titles, row)
	}
	if len(titles) == 0 {
		t.Skip("testdata/local_titles.tsv lists no plus3 row whose image is present." + unprovenTail)
	}
	if os.Getenv("ZX_GO_FDC_CAPTURE") != "all" && len(titles) > fdcCaptureRowBudget {
		titles = titles[:fdcCaptureRowBudget]
	}

	proved, discriminating, used := 0, 0, 0
	for _, tc := range titles {
		if os.Getenv("ZX_GO_FDC_CAPTURE") != "all" && used >= fdcCaptureTitles {
			break
		}
		usable := false
		for _, nth := range fdcCapturePoints {
			t.Run(fmt.Sprintf("%s/command %d", tc.name, nth), func(t *testing.T) {
				rig := newFDCCaptureRig(t, tc.path)
				msr, ok := rig.runToMidCommand(nth)
				if !ok {
					t.Skipf("this title's loader was never mid-command at a frame boundary %d times "+
						"within %d frames, so there was no mid-load state to capture.%s",
						nth, fdcCaptureScanFrames, unprovenTail)
				}
				usable = true

				captured := rig.all.Capture()
				capturedSansFDC := rig.sansFDC.Capture()
				fdcAtCapture := rig.fdc.SaveState()

				rig.h.RunFrames(fdcCaptureWindow)
				forward := rig.blobs()

				// The guard the comparison below depends on. A window that
				// leaves the controller holding what it held at the capture
				// point cannot tell a restored field from an unrestored one,
				// however green it comes out.
				if bytes.Equal(fdcAtCapture, rig.fdc.SaveState()) {
					t.Skipf("the controller holds the same state %d frames after the capture as it "+
						"did at it, so this capture point cannot detect a lost restore.%s",
						fdcCaptureWindow, unprovenTail)
				}

				replayed := rig.replay(t, captured)
				if diff := differingDevices(forward, replayed); len(diff) > 0 {
					// Told apart by repetition: randomness does not land in
					// the same wrong place twice, a lost restore does.
					again := rig.replay(t, captured)
					if len(differingDevices(replayed, again)) > 0 {
						// Two replays of one capture disagreeing points at
						// weak sectors, because a lost restore is
						// deterministic and lands in the same wrong place
						// every time.
						//
						// That reasoning is sound and it is NOT enough to
						// skip on. Any other source of nondeterminism reaches
						// this branch too, and an adversarial review found it
						// firing on genuine capture defects — a silent skip
						// turned a real failure into a pass. So the default is
						// now to FAIL, and a title actually known to carry
						// weak sectors must be named explicitly.
						if os.Getenv("ZX_GO_FDC_WEAK_SECTORS") != tc.name {
							t.Fatalf("resuming this load produced a different machine on each "+
								"replay (%v). That is usually weak sectors, but it is also what a "+
								"capture defect looks like when the missing state feeds "+
								"nondeterminism, so it is NOT treated as an excuse. If this title "+
								"genuinely has weak sectors, re-run with "+
								"ZX_GO_FDC_WEAK_SECTORS=%q to record that judgement explicitly.%s",
								differingDevices(replayed, again), tc.name, unprovenTail)
						}
						t.Skipf("%s is recorded as a weak-sector title via "+
							"ZX_GO_FDC_WEAK_SECTORS, so its replay difference is not evidence "+
							"either way.%s", tc.name, unprovenTail)
					}
					t.Fatalf("resuming a real load from a capture taken mid-command (MSR %02X) ran "+
						"into a different machine: %v differ after the same %d frames. The capture "+
						"is missing state the load depends on", msr, diff, fdcCaptureWindow)
				}
				proved++

				// The negative. Restore everything but the controller — which
				// is left a window ahead of the machine around it — and the
				// run must come out different, or the comparison above was
				// never about the FDC.
				if err := rig.sansFDC.Restore(capturedSansFDC); err != nil {
					t.Fatalf("restoring every device but the controller: %v", err)
				}
				rig.h.RunFrames(fdcCaptureWindow)
				if len(differingDevices(forward, rig.blobs())) == 0 {
					t.Logf("NOT DISCRIMINATING: this load came out identical with the controller's "+
						"state left unrestored, so the pass above is not evidence about the FDC. "+
						"MSR at the capture was %02X.", msr)
					return
				}
				discriminating++
			})
		}
		if usable {
			used++
		}
	}

	if proved == 0 {
		t.Skip("no listed +3 title reached a mid-command capture that could be tested." + unprovenTail)
	}
	t.Logf("PROVEN on local media: %d mid-load captures resumed identically, %d of them "+
		"demonstrably because the controller's state was restored.", proved, discriminating)
	if discriminating == 0 {
		t.Error("every capture point resumed identically with the FDC left unrestored, so none of " +
			"them says anything about the FDC capture: the fixture, not the controller, needs fixing")
	}
}
