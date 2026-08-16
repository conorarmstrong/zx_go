package machinestate

import (
	"bytes"
	"errors"
	"testing"
)

var errBroken = errors.New("cannot load")

// The registry's whole job is to make "did we capture everything" a question
// with an answer. A rewind that restores the CPU and the RAM and forgets the
// sound chip does not fail loudly: it produces a machine that runs, and then
// diverges quietly from the one you rewound. So the tests here are about the
// properties that make an omission detectable rather than about plumbing.

// fake is a device whose state is a single mutable byte slice, which is enough
// to exercise capture, restore and ordering.
type fake struct {
	id    string
	state []byte
	loads int
}

func (f *fake) StateID() string { return f.id }

func (f *fake) SaveState() []byte {
	out := make([]byte, len(f.state))
	copy(out, f.state)
	return out
}

func (f *fake) LoadState(b []byte) error {
	f.loads++
	f.state = make([]byte, len(b))
	copy(f.state, b)
	return nil
}

func TestCaptureThenRestoreReturnsEveryDeviceToItsEarlierState(t *testing.T) {
	a := &fake{id: "cpu", state: []byte{1, 2, 3}}
	b := &fake{id: "ay", state: []byte{9}}
	r := New()
	r.Register(a, b)

	snap := r.Capture()

	a.state = []byte{7, 7, 7}
	b.state = []byte{0}

	if err := r.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !bytes.Equal(a.state, []byte{1, 2, 3}) {
		t.Errorf("cpu state = %v, want the captured [1 2 3]", a.state)
	}
	if !bytes.Equal(b.state, []byte{9}) {
		t.Errorf("ay state = %v, want the captured [9]", b.state)
	}
}

// A capture has to be a copy, not a view. A device that keeps mutating after
// the snapshot was taken would otherwise rewrite its own history, and the
// rewind would restore the present.
func TestACaptureIsIndependentOfLaterDeviceMutation(t *testing.T) {
	d := &fake{id: "ram", state: []byte{1, 2, 3}}
	r := New()
	r.Register(d)

	snap := r.Capture()
	d.state[0] = 99 // mutate in place, the way a RAM bank would

	if err := r.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if d.state[0] != 1 {
		t.Errorf("restored byte = %d, want 1: the capture aliased live device memory", d.state[0])
	}
}

// Two captures of an unchanged machine must be byte-identical, because that is
// what lets a test compare machine states directly. Map iteration order would
// destroy this, so the encoding is ordered by StateID.
func TestCaptureIsDeterministicRegardlessOfRegistrationOrder(t *testing.T) {
	mk := func(order ...string) State {
		r := New()
		for _, id := range order {
			r.Register(&fake{id: id, state: []byte(id)})
		}
		return r.Capture()
	}

	first := mk("ay", "cpu", "ula")
	second := mk("ula", "ay", "cpu")

	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Error("the same devices registered in a different order produced different bytes; " +
			"a state blob that depends on registration order cannot be compared between runs")
	}
}

// The failure this whole design exists to prevent: a device that is part of the
// machine but was never registered. Restoring a snapshot taken before it
// existed must say so rather than silently leave it at its current value.
func TestRestoreRefusesASnapshotThatIsMissingARegisteredDevice(t *testing.T) {
	r := New()
	r.Register(&fake{id: "cpu", state: []byte{1}})
	snap := r.Capture()

	// The AY joins the machine after the snapshot was taken.
	ay := &fake{id: "ay", state: []byte{5}}
	r.Register(ay)

	err := r.Restore(snap)
	if err == nil {
		t.Fatal("restoring a snapshot with no state for a registered device must fail, " +
			"not leave that device holding present-day values")
	}
	if ay.loads != 0 {
		t.Error("a rejected restore must not partially apply")
	}
}

// The mirror image: a snapshot carrying a device the machine no longer has.
// Silently ignoring it would hide a genuine configuration change, such as a
// disk interface being detached between capture and rewind.
func TestRestoreRefusesASnapshotCarryingAnUnknownDevice(t *testing.T) {
	full := New()
	full.Register(&fake{id: "cpu", state: []byte{1}}, &fake{id: "fdc", state: []byte{2}})
	snap := full.Capture()

	fewer := New()
	fewer.Register(&fake{id: "cpu", state: []byte{1}})

	if err := fewer.Restore(snap); err == nil {
		t.Fatal("restoring a snapshot that carries an unregistered device must fail")
	}
}

// Registering the same identifier twice means two devices would overwrite each
// other's state, and the rewind would restore one of them at random.
func TestRegisterRejectsADuplicateIdentifier(t *testing.T) {
	r := New()
	r.Register(&fake{id: "ay", state: []byte{1}})

	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate StateID must panic at wiring time, " +
				"not silently drop one device's state at rewind time")
		}
	}()
	r.Register(&fake{id: "ay", state: []byte{2}})
}

// A device that fails to load leaves the machine half-restored, which is worse
// than not restoring at all: the caller believes the rewind worked.
func TestRestoreReportsADeviceThatRefusesItsState(t *testing.T) {
	r := New()
	r.Register(&fake{id: "cpu", state: []byte{1}}, &broken{})
	snap := r.Capture()

	err := r.Restore(snap)
	if err == nil {
		t.Fatal("a device that fails to load its state must surface as an error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("fdc")) {
		t.Errorf("the error must name the device that failed, got %q", err)
	}
}

type broken struct{}

func (broken) StateID() string   { return "fdc" }
func (broken) SaveState() []byte { return []byte{1} }
func (broken) LoadState([]byte) error {
	return errBroken
}

// The package doc calls a half-restored machine "the worst outcome available
// here": it runs, and the caller believes the rewind worked. check() validates
// the device SET before anything is applied, but it cannot know whether each
// blob will decode, so a device rejecting its state mid-loop left every
// earlier device already restored.
//
// The earlier test asserted only that an error came back, which is exactly the
// weaker claim the doc warns against.
func TestAFailedRestoreLeavesEveryDeviceUntouched(t *testing.T) {
	good := &fake{id: "aaa", state: []byte{1}}
	r := New()
	r.Register(good, &broken{}) // "fdc" sorts after "aaa", so good is applied first
	snap := r.Capture()

	good.state = []byte{9}

	if err := r.Restore(snap); err == nil {
		t.Fatal("a device that refuses its state must fail the restore")
	}

	// The property is the device's observable STATE, not how many times
	// LoadState was called. Rolling back necessarily calls it again, so a
	// call-count assertion would only be satisfiable by a design that can
	// validate every blob without touching a device, which the Device
	// interface deliberately does not offer.
	if !bytes.Equal(good.state, []byte{9}) {
		t.Errorf("device %q holds %v after a failed restore, want its pre-restore value [9]: "+
			"the machine was left half-rewound", good.id, good.state)
	}
}

// And the rollback has to be real rather than incidental: a device restored
// before the failure must come back to its pre-restore value, not to the value
// the snapshot carried.
func TestARolledBackDeviceDoesNotKeepTheSnapshotValue(t *testing.T) {
	good := &fake{id: "aaa", state: []byte{1}}
	r := New()
	r.Register(good, &broken{})
	snap := r.Capture() // captures aaa = [1]

	good.state = []byte{9}
	_ = r.Restore(snap)

	if bytes.Equal(good.state, []byte{1}) {
		t.Error("device kept the snapshot value [1] after a failed restore: " +
			"the failure was reported but the machine still moved")
	}
}

// Size is documented as what bounds a rewind ring, so it has to agree with
// what Bytes actually emits. Counting only the payload undercounts every
// capture by the framing, and a ring sized against a memory budget would
// exceed it.
func TestSizeAgreesWithTheEncodedLength(t *testing.T) {
	r := New()
	r.Register(&fake{id: "cpu", state: make([]byte, 100)}, &fake{id: "ay", state: make([]byte, 7)})
	s := r.Capture()

	if got, want := s.Size(), len(s.Bytes()); got != want {
		t.Errorf("Size() = %d but the encoding is %d bytes: a ring sized from Size() would "+
			"hold %d bytes more than its budget per capture", got, want, want-got)
	}
}

// halfApplier fails its load, but only AFTER mutating itself. That is the case
// the rollback did not cover: Restore unwound the devices applied BEFORE the
// failure and left the failing device holding whatever it had written before
// it gave up.
//
// Our own devices all decode fully before applying, so none of them can do
// this. The registry cannot assume that of every device that will ever
// implement the interface, and the cost of covering it is one more entry in a
// loop that only runs on the failure path.
type halfApplier struct {
	state []byte
	loads int
}

func (h *halfApplier) StateID() string   { return "zzz-half" } // sorts last
func (h *halfApplier) SaveState() []byte { return append([]byte(nil), h.state...) }
func (h *halfApplier) LoadState(b []byte) error {
	h.loads++
	if h.loads == 1 {
		h.state = []byte{0xBA, 0xD1} // mutates first...
		return errBroken             // ...then fails
	}
	// A later call carries the rollback blob, which is well-formed, so it
	// succeeds. A device that failed EVERY load could not be unwound by
	// anything and the registry reports that case separately.
	h.state = append([]byte(nil), b...)
	return nil
}

func TestAFailedRestoreAlsoUnwindsTheDeviceThatFailed(t *testing.T) {
	good := &fake{id: "aaa", state: []byte{1}}
	bad := &halfApplier{state: []byte{7}}
	r := New()
	r.Register(good, bad)
	snap := r.Capture() // aaa=[1], zzz-half=[7]

	good.state = []byte{9}
	bad.state = []byte{8}

	if err := r.Restore(snap); err == nil {
		t.Fatal("a device that refuses its state must fail the restore")
	}
	if !bytes.Equal(good.state, []byte{9}) {
		t.Errorf("earlier device = %v, want its pre-restore [9]", good.state)
	}
	if bytes.Equal(bad.state, []byte{0xBA, 0xD1}) {
		t.Error("the failing device kept the half-applied bytes it wrote before erroring: " +
			"a failed restore must unwind it too, not just the devices before it")
	}
	if !bytes.Equal(bad.state, []byte{8}) {
		t.Errorf("failing device = %v, want its pre-restore [8]", bad.state)
	}
	if bad.loads < 2 {
		t.Error("the failing device was never asked to unwind")
	}
}

// The other half of the same story: a device that cannot be unwound at all
// leaves the machine genuinely inconsistent, and that has to be reported
// differently from a clean refusal. The caller can retry after the first; after
// the second it cannot keep running.
func TestAnUnwindableFailureIsReportedAsWorseThanARefusal(t *testing.T) {
	r := New()
	r.Register(&fake{id: "aaa", state: []byte{1}}, &broken{})
	snap := r.Capture()

	err := r.Restore(snap)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("could not be rolled back")) {
		t.Errorf("a device that fails its own rollback must say the machine is inconsistent, got %q", err)
	}
}
