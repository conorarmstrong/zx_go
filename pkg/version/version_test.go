package version

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	if !strings.HasPrefix(String(), "zx_go ") || !strings.Contains(String(), Version) {
		t.Errorf("String() = %q, want it to embed the product name + version", String())
	}
}

// TestVersionMatchesTheChangelog pins the constant to the newest release
// documented in CHANGELOG.md.
//
// This replaces a test that asserted Version equalled a hardcoded literal,
// which guarded nothing: the literal was simply edited to match whatever the
// constant already said, or — as happened through v1.7.0 and v1.8.0 — neither
// was touched at all and the binary reported a version two releases stale.
// Tying it to the changelog means the release notes and the reported version
// cannot drift apart without a test failing.
func TestVersionMatchesTheChangelog(t *testing.T) {
	data, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	m := regexp.MustCompile(`(?m)^## \[(v\d+\.\d+\.\d+)\]`).FindSubmatch(data)
	if m == nil {
		t.Fatal("no '## [vX.Y.Z]' release heading found in CHANGELOG.md")
	}
	if got, want := Version, string(m[1]); got != want {
		t.Errorf("version.Version = %q, but the newest CHANGELOG entry is %q.\n"+
			"Bump the constant when cutting a release, or add the changelog section first.", got, want)
	}
}

// TestVersionIsWellFormed guards the shape the release tooling relies on.
func TestVersionIsWellFormed(t *testing.T) {
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Errorf("Version = %q, want the form vX.Y.Z", Version)
	}
}
