package memory

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// A PagingState is deliberately opaque — it is a snapshot a peripheral holds,
// not a struct anyone should build by hand — but a peripheral holding one when
// the machine state is captured has to carry it into its own capture, and it
// cannot reach the fields to do so. The Multiface is the case that matters: it
// takes a snapshot when its window pages in and puts it back when the session
// ends, so a rewind that landed mid-session and lost the snapshot would end the
// session by restoring nothing.
//
// pagingStateWire is the wire form. Every field of PagingState is present,
// including valid: a decoded snapshot that lost it would be ignored by
// RestorePagingState without saying anything.
type pagingStateWire struct {
	Port7FFD           byte
	Port1FFD           byte
	SpecialPaging      bool
	MemoryPageReadMap  [4]int
	MemoryPageWriteMap [4]int
	ScreenPage         int
	PagingEnabled      bool
	Valid              bool
}

// MarshalBinary encodes the snapshot.
func (p *PagingState) MarshalBinary() ([]byte, error) {
	w := pagingStateWire{
		Port7FFD:           p.port7FFD,
		Port1FFD:           p.port1FFD,
		SpecialPaging:      p.specialPaging,
		MemoryPageReadMap:  p.memoryPageReadMap,
		MemoryPageWriteMap: p.memoryPageWriteMap,
		ScreenPage:         p.screenPage,
		PagingEnabled:      p.pagingEnabled,
		Valid:              p.valid,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(w); err != nil {
		return nil, fmt.Errorf("memory: encoding paging snapshot: %w", err)
	}
	return buf.Bytes(), nil
}

// UnmarshalBinary restores a snapshot encoded by MarshalBinary. The decode
// completes before anything is written, so a malformed blob leaves the
// snapshot alone rather than half-filled.
func (p *PagingState) UnmarshalBinary(b []byte) error {
	var w pagingStateWire
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&w); err != nil {
		return fmt.Errorf("memory: decoding paging snapshot: %w", err)
	}
	p.port7FFD = w.Port7FFD
	p.port1FFD = w.Port1FFD
	p.specialPaging = w.SpecialPaging
	p.memoryPageReadMap = w.MemoryPageReadMap
	p.memoryPageWriteMap = w.MemoryPageWriteMap
	p.screenPage = w.ScreenPage
	p.pagingEnabled = w.PagingEnabled
	p.valid = w.Valid
	return nil
}
