package debugger

import "errors"

// BankAccessor exposes a flat-byte view of the emulator's physical
// memory pools, independent of whatever the CPU has mapped at the
// moment. The visual and telnet debuggers both consume this so
// "inspect bank 5 while bank 0 is paged in" works uniformly.
//
// Kinds are intentionally string-typed so the surface stays open
// to whatever pools an emulator backend wants to expose; the canonical
// names this package uses are "ram", "rom", and "divmmc-ram". A
// backend may add machine-specific kinds (e.g. "altrom") and they
// will flow through the telnet command + visual UI unchanged.
type BankAccessor interface {
	// Bank returns a slice aliasing the physical bytes of bank
	// number `bank` in `kind`. Writes to the returned slice MUST
	// be visible to the running emulator. Returns ErrUnknownKind
	// if `kind` is not exposed by this backend, and
	// ErrBankOutOfRange if `bank` is outside the pool.
	Bank(kind string, bank int) ([]byte, error)

	// BankPeek copies n bytes starting at `off` in `kind` bank
	// `bank`. Returns ErrZeroLength if n <= 0,
	// ErrOffsetOutOfRange if [off, off+n) exits the bank.
	BankPeek(kind string, bank, off, n int) ([]byte, error)

	// BankPoke writes `data` into `kind` bank `bank` starting at
	// `off`. Returns ErrOffsetOutOfRange if the write would exit
	// the bank.
	BankPoke(kind string, bank, off int, data []byte) error
}

// Sentinel errors. Callers are encouraged to compare with `==`
// rather than string-matching the Error output.
var (
	ErrUnknownKind      = errors.New("unknown bank kind")
	ErrBankOutOfRange   = errors.New("bank out of range")
	ErrOffsetOutOfRange = errors.New("offset out of range")
	ErrZeroLength       = errors.New("zero length")
)

// BankPeekFrom implements the bounds-checked peek logic on top of a
// backend's Bank() lookup. Backends embed this so their Bank() is
// the only method they need to write — the bounds dance is shared.
func BankPeekFrom(b interface {
	Bank(string, int) ([]byte, error)
}, kind string, bank, off, n int) ([]byte, error) {
	if n <= 0 {
		return nil, ErrZeroLength
	}
	pool, err := b.Bank(kind, bank)
	if err != nil {
		return nil, err
	}
	if off < 0 || off+n > len(pool) {
		return nil, ErrOffsetOutOfRange
	}
	out := make([]byte, n)
	copy(out, pool[off:off+n])
	return out, nil
}

// BankPokeFrom is the symmetric counterpart to BankPeekFrom. The
// write goes directly into the backend's bank slice, so the running
// emulator sees the change on the next read.
func BankPokeFrom(b interface {
	Bank(string, int) ([]byte, error)
}, kind string, bank, off int, data []byte) error {
	pool, err := b.Bank(kind, bank)
	if err != nil {
		return err
	}
	if off < 0 || off+len(data) > len(pool) {
		return ErrOffsetOutOfRange
	}
	copy(pool[off:], data)
	return nil
}
