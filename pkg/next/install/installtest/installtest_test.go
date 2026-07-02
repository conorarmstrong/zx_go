package installtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install"
)

func TestRedirectConfig_SetsEnvAndReturnsPath(t *testing.T) {
	dir := RedirectConfig(t)
	if dir == "" {
		t.Fatal("RedirectConfig returned empty path")
	}
	if got := os.Getenv("ZX_GO_NEXT_ROM_DIR"); got != dir {
		t.Errorf("ZX_GO_NEXT_ROM_DIR = %q, want %q", got, dir)
	}
	// install.Path() must now resolve to (and create) the temp dir.
	resolved, err := install.Path()
	if err != nil {
		t.Fatalf("install.Path after RedirectConfig: %v", err)
	}
	if resolved != dir {
		t.Errorf("install.Path = %q, want %q", resolved, dir)
	}
	if st, err := os.Stat(resolved); err != nil || !st.IsDir() {
		t.Errorf("RedirectConfig path %q not a directory: %v", resolved, err)
	}
}

func TestAssertSandboxed_PassesAfterRedirect(t *testing.T) {
	RedirectConfig(t)
	// Should not fail; if it does, t.Fatalf inside AssertSandboxed
	// would terminate this test and we'd see it in the failure
	// report rather than panic.
	AssertSandboxed(t)
}

// TestAssertSandboxed_FailsOnRealPath uses a sub-test where Path()
// returns a non-temp dir. We can't easily wedge install.Path() into
// returning a non-temp value because RedirectConfig sets the env var
// AND TempDir always sits in /tmp or /var/folders. Skip this case;
// the rejection branch is exercised by isUnderTempDir tests above.
// The PASS path is what matters for AssertSandboxed's main contract.

func TestRedirectConfig_DistinctTempDirsPerInvocation(t *testing.T) {
	d1 := RedirectConfig(t)
	d2 := RedirectConfig(t)
	if d1 == d2 {
		t.Errorf("RedirectConfig returned same dir twice: %q", d1)
	}
	if filepath.Base(d1) != "next" {
		t.Errorf("RedirectConfig path %q should end in the \"next\" segment", d1)
	}
}

// isUnderTempDir unit tests. This helper guards against the "test
// writes to developer's real install dir" footgun; AssertSandboxed
// leans on it. Bugs here would let bad tests silently clobber real
// ROMs, so behaviour is pinned exhaustively.

func TestIsUnderTempDir_MacOSPaths(t *testing.T) {
	cases := []string{
		"/var/folders/abc/123/T/zxtest123",
		"/private/var/folders/xx/yyy/T/sub",
		"/var/folders/x/y/T/something/zx_go/next",
	}
	for _, c := range cases {
		if !isUnderTempDir(c) {
			t.Errorf("macOS temp path %q: isUnderTempDir = false, want true", c)
		}
	}
}

func TestIsUnderTempDir_LinuxPaths(t *testing.T) {
	cases := []string{
		"/tmp/test",
		"/tmp/zx_go/next",
		"/private/tmp/abc",
	}
	for _, c := range cases {
		if !isUnderTempDir(c) {
			t.Errorf("Linux temp path %q: isUnderTempDir = false, want true", c)
		}
	}
}

func TestIsUnderTempDir_WindowsPaths(t *testing.T) {
	cases := []string{
		`C:\Users\user\AppData\Local\Temp\go-build123\zxtest`,
		`C:\Temp\foo`,
		`D:\AppData\Local\Temp\test`,
	}
	for _, c := range cases {
		if !isUnderTempDir(c) {
			t.Errorf("Windows temp path %q: isUnderTempDir = false, want true", c)
		}
	}
}

func TestIsUnderTempDir_RealPathsRejected(t *testing.T) {
	cases := []string{
		"/Users/dev/Library/Application Support/zx_go/next", // macOS real config
		"/home/user/.config/zx_go/next",                     // Linux real config
		`C:\Users\user\AppData\Roaming\zx_go\next`,          // Windows real config
		"/etc/foo",
		"/var/lib/something",
		"",
	}
	for _, c := range cases {
		if isUnderTempDir(c) {
			t.Errorf("real path %q: isUnderTempDir = true, want false", c)
		}
	}
}

// TestIsUnderTempDir_SimilarButNotTemp guards against false-positives
// from paths that happen to share a prefix with temp prefixes but
// aren't actually temp.
func TestIsUnderTempDir_SimilarButNotTemp(t *testing.T) {
	// "/tmpfoo" doesn't start with "/tmp/" — should be rejected.
	cases := []string{
		"/tmpfoo/bar",
		"/var/folders_thing",
		"/private/var/foldersx/y",
	}
	for _, c := range cases {
		if isUnderTempDir(c) {
			t.Errorf("near-miss path %q: isUnderTempDir = true, want false", c)
		}
	}
}
