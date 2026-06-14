package rzx

import (
	"crypto/dsa" //nolint:staticcheck // the RZX security model mandates DSA
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // the RZX security model mandates SHA-1
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
)

// RZX competition recordings are protected with a DSA signature over the
// file (FUSE rzx.c security blocks). The signature lives in the sign-end
// block (0x21); the sign-start block (0x20) carries the key id. This file
// computes and verifies that signature. (The chosen DSA key is the
// caller's, so a signature is interoperable only with verifiers that hold
// the matching public key — adequate for tamper detection and round-trip
// verification, which is what an emulator needs.)

// ErrNoSignature is returned when a verify/sign is attempted on a file
// without a sign-end block.
var ErrNoSignature = errors.New("rzx: no sign-end block")

// findSignEnd returns the file's sign-end block payload, or nil.
func findSignEnd(f *File) *SignEndBlock {
	for _, b := range f.Blocks {
		if b.Type == BlockSignEnd {
			return b.SignEnd
		}
	}
	return nil
}

// signedDigest returns the SHA-1 digest of the serialized file with the
// sign-end signature cleared — the bytes the DSA signature covers. Both
// Sign and Verify compute it identically so the signature round-trips.
func signedDigest(f *File) ([20]byte, error) {
	var zero [20]byte
	end := findSignEnd(f)
	var saved []byte
	if end != nil {
		saved, end.Signature = end.Signature, nil
	}
	data, err := f.Write(WriteOptions{Signed: true})
	if end != nil {
		end.Signature = saved
	}
	if err != nil {
		return zero, err
	}
	return sha1.Sum(data), nil
}

// Sign computes the DSA signature over the recording and stores it in the
// sign-end block (which must already be present in f.Blocks).
func Sign(f *File, priv *dsa.PrivateKey) error {
	end := findSignEnd(f)
	if end == nil {
		return ErrNoSignature
	}
	digest, err := signedDigest(f)
	if err != nil {
		return err
	}
	r, s, err := dsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return fmt.Errorf("rzx: sign: %w", err)
	}
	end.Signature = encodeSig(r, s)
	return nil
}

// Verify checks the recording's DSA signature against pub. It returns
// false (no error) for a well-formed but invalid signature, and an error
// only when the file lacks a signature or is malformed.
func Verify(f *File, pub *dsa.PublicKey) (bool, error) {
	end := findSignEnd(f)
	if end == nil || len(end.Signature) == 0 {
		return false, ErrNoSignature
	}
	r, s, err := decodeSig(end.Signature)
	if err != nil {
		return false, err
	}
	digest, err := signedDigest(f)
	if err != nil {
		return false, err
	}
	return dsa.Verify(pub, digest[:], r, s), nil
}

// encodeSig serializes the (r,s) MPIs as 2-byte big-endian length prefix
// followed by the big-endian magnitude.
func encodeSig(r, s *big.Int) []byte {
	var out []byte
	for _, v := range []*big.Int{r, s} {
		b := v.Bytes()
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(b)))
		out = append(out, l[:]...)
		out = append(out, b...)
	}
	return out
}

// decodeSig is the inverse of encodeSig.
func decodeSig(data []byte) (r, s *big.Int, err error) {
	read := func() (*big.Int, error) {
		if len(data) < 2 {
			return nil, errors.New("rzx: truncated signature")
		}
		n := int(binary.BigEndian.Uint16(data))
		data = data[2:]
		if len(data) < n {
			return nil, errors.New("rzx: truncated signature")
		}
		v := new(big.Int).SetBytes(data[:n])
		data = data[n:]
		return v, nil
	}
	if r, err = read(); err != nil {
		return nil, nil, err
	}
	if s, err = read(); err != nil {
		return nil, nil, err
	}
	return r, s, nil
}
