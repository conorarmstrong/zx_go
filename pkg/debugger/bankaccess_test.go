package debugger

import (
	stdbytes "bytes"
	"testing"
)

// fakeBanks is a deterministic BankAccessor backing for tests. It
// holds three named pools (ram / rom / divmmc-ram) of sized banks,
// addressable by [kind][bank][off].
type fakeBanks struct {
	ram    [][]byte
	rom    [][]byte
	divmmc [][]byte
}

func (f *fakeBanks) pool(kind string) ([][]byte, bool) {
	switch kind {
	case "ram":
		return f.ram, true
	case "rom":
		return f.rom, true
	case "divmmc-ram":
		return f.divmmc, true
	}
	return nil, false
}

func (f *fakeBanks) BankPeek(kind string, bank, off, n int) ([]byte, error) {
	return BankPeekFrom(f, kind, bank, off, n)
}

func (f *fakeBanks) BankPoke(kind string, bank, off int, data []byte) error {
	return BankPokeFrom(f, kind, bank, off, data)
}

func (f *fakeBanks) Bank(kind string, bank int) ([]byte, error) {
	p, ok := f.pool(kind)
	if !ok {
		return nil, ErrUnknownKind
	}
	if bank < 0 || bank >= len(p) {
		return nil, ErrBankOutOfRange
	}
	return p[bank], nil
}

func TestBankPeekHappy(t *testing.T) {
	f := &fakeBanks{ram: [][]byte{
		stdbytes.Repeat([]byte{0xAA}, 8192),
		stdbytes.Repeat([]byte{0xBB}, 8192),
	}}
	got, err := f.BankPeek("ram", 1, 0, 4)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []byte{0xBB, 0xBB, 0xBB, 0xBB}
	if !stdbytes.Equal(got, want) {
		t.Fatalf("got %x want %x", got, want)
	}
}

func TestBankPeekUnknownKind(t *testing.T) {
	f := &fakeBanks{}
	if _, err := f.BankPeek("nope", 0, 0, 1); err != ErrUnknownKind {
		t.Fatalf("expected ErrUnknownKind, got %v", err)
	}
}

func TestBankPeekBankOutOfRange(t *testing.T) {
	f := &fakeBanks{ram: [][]byte{make([]byte, 16)}}
	if _, err := f.BankPeek("ram", 5, 0, 1); err != ErrBankOutOfRange {
		t.Fatalf("expected ErrBankOutOfRange, got %v", err)
	}
}

func TestBankPeekOffsetOutOfRange(t *testing.T) {
	f := &fakeBanks{ram: [][]byte{make([]byte, 16)}}
	if _, err := f.BankPeek("ram", 0, 14, 4); err != ErrOffsetOutOfRange {
		t.Fatalf("expected ErrOffsetOutOfRange, got %v", err)
	}
}

func TestBankPeekZeroLengthIsError(t *testing.T) {
	f := &fakeBanks{ram: [][]byte{make([]byte, 16)}}
	if _, err := f.BankPeek("ram", 0, 0, 0); err != ErrZeroLength {
		t.Fatalf("expected ErrZeroLength, got %v", err)
	}
}

func TestBankPokeHappy(t *testing.T) {
	f := &fakeBanks{ram: [][]byte{make([]byte, 8)}}
	if err := f.BankPoke("ram", 0, 2, []byte{1, 2, 3}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []byte{0, 0, 1, 2, 3, 0, 0, 0}
	if !stdbytes.Equal(f.ram[0], want) {
		t.Fatalf("after poke: got %x want %x", f.ram[0], want)
	}
}

func TestBankPokeOverflowRejected(t *testing.T) {
	f := &fakeBanks{ram: [][]byte{make([]byte, 4)}}
	err := f.BankPoke("ram", 0, 2, []byte{1, 2, 3})
	if err != ErrOffsetOutOfRange {
		t.Fatalf("expected ErrOffsetOutOfRange, got %v", err)
	}
}

func TestBankPokeUnknownKindLeavesPoolsAlone(t *testing.T) {
	f := &fakeBanks{ram: [][]byte{{0, 0, 0, 0}}}
	err := f.BankPoke("nope", 0, 0, []byte{0xFF})
	if err != ErrUnknownKind {
		t.Fatalf("expected ErrUnknownKind, got %v", err)
	}
	if !stdbytes.Equal(f.ram[0], []byte{0, 0, 0, 0}) {
		t.Fatalf("ram[0] unexpectedly mutated: %x", f.ram[0])
	}
}
