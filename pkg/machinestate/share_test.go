package machinestate

import (
	"bytes"
	"testing"
)

// A time-travel ring holds many captures of one machine, and between two of
// them most of memory is unchanged. ShareWith lets a capture keep the previous
// capture's copy of every chunk it has not changed, so a deep ring costs the
// memory that changed rather than the whole machine per entry. It must change
// nothing a reader can see: the bytes, the size and what a restore applies.

func TestShareWithKeepsTheBytesAndSharesUnchangedChunks(t *testing.T) {
	mem := &fake{id: "mem", state: make([]byte, 5*shareChunk+100)}
	for i := range mem.state {
		mem.state[i] = byte(i * 7)
	}
	cpu := &fake{id: "cpu", state: []byte{1, 2, 3}}
	r := New()
	r.Register(mem, cpu)

	first := r.Capture().ShareWith(State{})
	mem.state[2*shareChunk+5] ^= 0xFF // one chunk changes
	cpu.state[0] = 9
	plain := r.Capture()
	second := plain.ShareWith(first)

	if !bytes.Equal(second.Bytes(), plain.Bytes()) {
		t.Fatal("sharing changed the capture's bytes")
	}
	if second.Size() != plain.Size() {
		t.Errorf("Size = %d, want %d", second.Size(), plain.Size())
	}
	var m1, m2 entry
	for i := range second.entries {
		if second.entries[i].id == "mem" {
			m1, m2 = first.entries[i], second.entries[i]
		}
	}
	if len(m2.chunks) != 6 {
		t.Fatalf("memory is in %d chunks, want 6", len(m2.chunks))
	}
	for i := range m2.chunks {
		same := &m2.chunks[i][0] == &m1.chunks[i][0]
		if i == 2 && same {
			t.Error("the changed chunk shares the earlier capture's copy")
		}
		if i != 2 && !same {
			t.Errorf("unchanged chunk %d holds its own copy", i)
		}
	}

	mem.state[0] = 0xAA
	if err := r.Restore(second); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := r.Capture(); !bytes.Equal(got.Bytes(), plain.Bytes()) {
		t.Error("restoring the shared capture did not apply the bytes it was captured as")
	}
}

// Sharing never reaches into a different device's chunks or across a change of
// length.
func TestShareWithIgnoresAMismatchedEarlierCapture(t *testing.T) {
	a := &fake{id: "a", state: bytes.Repeat([]byte{1}, shareChunk)}
	r := New()
	r.Register(a)
	before := r.Capture().ShareWith(State{})
	a.state = bytes.Repeat([]byte{1}, shareChunk+1)
	after := r.Capture()
	shared := after.ShareWith(before)
	if !bytes.Equal(shared.Bytes(), after.Bytes()) {
		t.Fatal("sharing across a length change altered the bytes")
	}
}

func TestHeldBytesCountsASharedPieceOnce(t *testing.T) {
	mem := &fake{id: "mem", state: make([]byte, 4*shareChunk)}
	r := New()
	r.Register(mem)
	a := r.Capture().ShareWith(State{})
	mem.state[0] = 1
	b := r.Capture().ShareWith(a)
	if got, want := HeldBytes(a, b), 5*shareChunk; got != want {
		t.Errorf("HeldBytes = %d, want %d: four pieces, one of them changed", got, want)
	}
}
