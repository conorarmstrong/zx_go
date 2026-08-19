package machinestate

import (
	"encoding/binary"
	"fmt"
)

// A file container for a captured State.
//
// Every machine here has a registry, but only the classic Spectrums have a
// snapshot format that can express them: .sna, .z80 and .szx all describe a
// 48K/128K memory map and a Z80, and none of them has anywhere to put the
// Next's 2 MB and its NextRegs, the SAM's LMPR/HMPR/VMPR paging and SAA1099, or
// the ZX81's CPU-generated display. Those machines get a save state built from
// the registry instead — the same capture the rewind ring already takes,
// written to a file.
//
// This is OUR format and it is deliberately not portable. It carries device
// blobs whose layout is each device's own gob encoding, so it is a save state
// for this emulator at this version, not an interchange format. The version
// byte exists so a file from a later build is refused by name rather than
// misparsed.

const (
	// containerMagic identifies the format. It is checked before anything else
	// is read, so pointing the loader at a .z80 or a disk image says "not one
	// of ours" instead of failing somewhere in the middle of a length field.
	containerMagic = "ZXGOSTATE"

	// containerVersion is the layout version. Bump it when the framing below
	// changes; changes to a DEVICE's own blob are that device's business and
	// are caught by its LoadState.
	containerVersion = 1
)

// Encode wraps a State in a self-describing container tagged with the machine
// it came from.
//
// The tag is what lets a wrong-machine load say what actually happened.
// Without it, restoring a SAM state into a +3 reaches Registry.Restore and is
// refused there as a device-set mismatch — correct, but it reads as a corrupt
// file rather than as the wrong file.
func Encode(machine string, s State) []byte {
	body := s.Bytes()
	out := make([]byte, 0, len(containerMagic)+1+4+len(machine)+len(body))
	out = append(out, containerMagic...)
	out = append(out, containerVersion)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(machine)))
	out = append(out, hdr[:]...)
	out = append(out, machine...)
	return append(out, body...)
}

// Decode reads a container written by Encode, returning the machine tag and the
// state.
//
// Every length is checked against what is actually left in the buffer before it
// is used to slice, so a truncated or hostile file is an error rather than a
// panic on the goroutine that happened to load it.
func Decode(b []byte) (string, State, error) {
	if len(b) < len(containerMagic)+1+4 {
		return "", State{}, fmt.Errorf("machinestate: too short to be a save state (%d bytes)", len(b))
	}
	if string(b[:len(containerMagic)]) != containerMagic {
		return "", State{}, fmt.Errorf("machinestate: not a zx_go save state")
	}
	b = b[len(containerMagic):]

	if v := b[0]; v != containerVersion {
		return "", State{}, fmt.Errorf(
			"machinestate: save-state version %d, this build reads version %d", v, containerVersion)
	}
	b = b[1:]

	machine, b, err := takeString(b, "machine name")
	if err != nil {
		return "", State{}, err
	}

	var s State
	for len(b) > 0 {
		id, rest, err := takeString(b, "device name")
		if err != nil {
			return "", State{}, err
		}
		blob, rest, err := takeBytes(rest, "device state")
		if err != nil {
			return "", State{}, err
		}
		s.entries = append(s.entries, entry{id: id, blob: blob})
		b = rest
	}
	return machine, s, nil
}

// takeString reads a length-prefixed string.
func takeString(b []byte, what string) (string, []byte, error) {
	v, rest, err := takeBytes(b, what)
	return string(v), rest, err
}

// takeBytes reads a length-prefixed byte slice, copying it so the result does
// not alias the caller's buffer.
func takeBytes(b []byte, what string) ([]byte, []byte, error) {
	if len(b) < 4 {
		return nil, nil, fmt.Errorf("machinestate: truncated %s length", what)
	}
	n := binary.BigEndian.Uint32(b[:4])
	b = b[4:]
	if uint64(n) > uint64(len(b)) {
		return nil, nil, fmt.Errorf("machinestate: %s claims %d bytes, %d remain", what, n, len(b))
	}
	out := make([]byte, n)
	copy(out, b[:n])
	return out, b[n:], nil
}
