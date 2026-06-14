package rzx

import (
	"crypto/dsa" //nolint:staticcheck // RZX security mandates DSA
	"math/big"
	"testing"
)

// Precomputed L1024N160 DSA key (generated once) so the test is fast and
// deterministic — DSA parameter generation is too slow to run per-test.
const (
	dsaP = "83080073768e9c1e36100bf6b8b9479f3fdd0bcf23d9c0bc9da6e7bc9fb133d66ed23bff15c2a31bc5f8f85fc41326da056831c22cfe570a2f0fb007a9cc6d7c18ccfd8ca3b8b8a1ee8c5c2ceaa0d1a982b0383a27760417f3bfedf3b2133c5ed6e2e8d67a65d9f50cfa0030f6bb637f8b3860a083993351e0b1f676d97724cd"
	dsaQ = "f681f3676ffc9bc01bd364827468b782d95e9a11"
	dsaG = "70ca85e3add9cc103f03c3662780dabccf7e56253e845918e3ade99d94a80e9d2374a09b72d111b882e6f075a3f5ccc9cd0f40da9f8b32bbbeff4fb34122472437267739ce857412503747af22d60d28a56a7ba59f0256e00f67b0450d926e9dc9ed79bea46f56d1eba26345121b20989ba253e998381a8e6bcea470b783a1c7"
	dsaY = "202cf48d035c534f663fbd4eb8e69d56baa0a63f069f034614e2ae2611d45a7bac7bc9e231259ebda7acef1f254d7a0fbbeb0c8e9c61315696098765f6d62ce4637604d5b3d87fb88f79dc2bac542fcc67b0d4aabbd63d3afe81d10dc78fa869eecb1e3884ce2734226028f0706202c73b3b3046cf524ebe6e80f371340c1274"
	dsaX = "315a8a746a3bf22118477289d755be15f70ccf97"
)

func hexInt(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		t.Fatalf("bad hex %q", s)
	}
	return v
}

func testKey(t *testing.T) *dsa.PrivateKey {
	return &dsa.PrivateKey{
		PublicKey: dsa.PublicKey{
			Parameters: dsa.Parameters{P: hexInt(t, dsaP), Q: hexInt(t, dsaQ), G: hexInt(t, dsaG)},
			Y:          hexInt(t, dsaY),
		},
		X: hexInt(t, dsaX),
	}
}

// TestRZXSignVerify signs a recording, verifies it, and confirms that
// tampering with a signed block invalidates the signature.
func TestRZXSignVerify(t *testing.T) {
	priv := testKey(t)
	f := &File{Blocks: []Block{
		{Type: BlockSignStart, SignStart: &SignStartBlock{KeyID: 1, WeekCode: 0x2026}},
		{Type: BlockCreator, Creator: &CreatorBlock{Program: "zx_go", Major: 1, Minor: 0}},
		{Type: BlockSignEnd, SignEnd: &SignEndBlock{}},
	}}

	if err := Sign(f, priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(findSignEnd(f).Signature) == 0 {
		t.Fatal("Sign did not store a signature")
	}

	ok, err := Verify(f, &priv.PublicKey)
	if err != nil || !ok {
		t.Fatalf("Verify after Sign: ok=%v err=%v", ok, err)
	}

	// Tamper with a signed block → verification must fail.
	f.Blocks[1].Creator.Program = "evil"
	ok, err = Verify(f, &priv.PublicKey)
	if err != nil {
		t.Fatalf("Verify (tampered): unexpected error %v", err)
	}
	if ok {
		t.Error("Verify should fail after tampering with a signed block")
	}
}
