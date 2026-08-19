package machinestate

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// A captured State can be written to a file and read back, so the machines
// whose state no snapshot format can express — the Spectrum Next, the SAM
// Coupé, the ZX80 and ZX81 — get save states through the same registry that
// already drives rewind.
//
// The container is deliberately self-describing. A State on its own is a bare
// sequence of device blobs with no way to tell which machine produced it, so
// loading a SAM state into a +3 would reach Restore and be refused there with a
// device-set error rather than "this is not a SAM state". Tagging it makes the
// refusal say what actually happened.

// A round trip through the container returns the same state.
func TestAStateSurvivesTheContainer(t *testing.T) {
	r := New()
	r.Register(&fake{id: "alpha", state: []byte{1, 2, 3}})
	r.Register(&fake{id: "beta", state: []byte{9}})
	want := r.Capture()

	machine, got, err := Decode(Encode("sam", want))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if machine != "sam" {
		t.Errorf("machine = %q, want %q", machine, "sam")
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Error("the state did not survive the container")
	}
}

// A decoded state has to be applicable, not merely equal — the point of the
// exercise is restoring a machine from a file.
func TestADecodedStateRestoresAMachine(t *testing.T) {
	live := &fake{id: "alpha", state: []byte{1, 2, 3}}
	r := New()
	r.Register(live)
	saved := Encode("next", r.Capture())

	live.state = []byte{7, 7, 7} // the machine runs on

	_, restored, err := Decode(saved)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := r.Restore(restored); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !bytes.Equal(live.state, []byte{1, 2, 3}) {
		t.Errorf("device holds %v, want the captured [1 2 3]", live.state)
	}
}

// An empty state is legal and must round-trip, because a machine can be
// captured before any device is registered and the failure should surface at
// Restore as a device-set mismatch rather than as a corrupt file.
func TestAnEmptyStateRoundTrips(t *testing.T) {
	_, got, err := Decode(Encode("zx81", State{}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got.Devices()) != 0 {
		t.Errorf("devices = %v, want none", got.Devices())
	}
}

// Rubbish is refused rather than decoded into a plausible-looking state. Each
// of these is a distinct way a file can be wrong, and every one of them would
// otherwise produce a State that Restore would take seriously.
func TestTheContainerRefusesRubbish(t *testing.T) {
	good := Encode("sam", func() State {
		r := New()
		r.Register(&fake{id: "alpha", state: []byte{1, 2, 3}})
		return r.Capture()
	}())

	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"not ours", []byte("PK\x03\x04 a zip file")},
		{"truncated header", good[:4]},
		{"truncated body", good[:len(good)-3]},
		{"a device name longer than the file", oversizedDeviceName(good)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Decode(tc.blob); err == nil {
				t.Error("Decode accepted it, want an error")
			}
		})
	}
}

// oversizedDeviceName corrupts the first device-name length field to a value
// far larger than the file, which is the shape a hostile or truncated save
// state takes: a length that would slice past the end of the buffer. Reading it
// without a bounds check panics on whichever goroutine happened to load it.
//
// The field sits after the magic, the version byte, and the machine tag's own
// length-prefixed bytes.
func oversizedDeviceName(good []byte) []byte {
	out := append([]byte{}, good...)
	at := len(containerMagic) + 1
	machineLen := int(binary.BigEndian.Uint32(out[at:]))
	at += 4 + machineLen
	binary.BigEndian.PutUint32(out[at:], 0xFFFFFF00)
	return out
}

// A future version must be refused with a message that says so, rather than
// being parsed as though its layout were the current one.
func TestAFutureVersionIsRefusedByName(t *testing.T) {
	blob := Encode("sam", State{})
	// The version byte follows the magic.
	blob[len(containerMagic)] = 0xFE

	_, _, err := Decode(blob)
	if err == nil {
		t.Fatal("a future version was accepted")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error = %q, want it to name the version", err)
	}
}

// The machine tag is what makes a wrong-machine load say what happened. It must
// survive verbatim, including a name that is not one of ours.
func TestTheMachineTagSurvives(t *testing.T) {
	for _, name := range []string{"", "sam", "next", "zx81", "a name with spaces"} {
		got, _, err := Decode(Encode(name, State{}))
		if err != nil {
			t.Fatalf("Decode(%q): %v", name, err)
		}
		if got != name {
			t.Errorf("machine = %q, want %q", got, name)
		}
	}
}
