package next_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A model nothing can reach is not a feature, and the difference is invisible
// from outside: a subsystem that is implemented, pinned against the FPGA VHDL
// and imported by nobody looks exactly like one a guest uses every frame. This
// project has already published the wrong answer once, describing the zxnDMA's
// bus arbitration as "modelled" in three documents while nothing drove the pin
// it depends on. Review caught that; the documents did not.
//
// So reachability is checked here rather than remembered. Every subsystem under
// pkg/next is either imported by production code, or listed below with the
// reason it is not and what would change that. Both directions fail: a
// subsystem that quietly stops being wired, and one on the list that has since
// been wired and should come off it.
//
// This is deliberately a crude check. It proves a package is linked into the
// emulator, not that a guest can drive every register it owns. It is the floor,
// not the ceiling.
func TestEveryNextSubsystemIsReachableOrDeclaredUnreachable(t *testing.T) {
	// Declared unreachable: the package exists and is verified, but nothing in
	// the shipped emulator constructs it, so no guest can exercise it.
	unreachable := map[string]string{
		"ctc": "the Z80 CTC's four counter/timer channels are complete and pinned " +
			"by GHDL-captured golden vectors, but no CTC port is decoded in pkg/ula " +
			"or pkg/next/wire.go and nothing constructs the device. Reachable once " +
			"the IM2 daisy chain is wired, which is what its interrupts feed. " +
			"ROADMAP item 3.",
	}

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locating the repo: %v", err)
	}
	root = filepath.Dir(root) // pkg/next -> repo root

	subsystems, err := os.ReadDir(filepath.Join(root, "pkg", "next"))
	if err != nil {
		t.Fatalf("listing pkg/next: %v", err)
	}

	for _, e := range subsystems {
		if !e.IsDir() || e.Name() == "testdata" {
			continue
		}
		name := e.Name()
		pkgPath := "github.com/conorarmstrong/zx_go/pkg/next/" + name
		if !hasGoFiles(t, filepath.Join(root, "pkg", "next", name)) {
			continue
		}

		wired := productionImporters(t, root, pkgPath, "pkg/next/"+name)
		reason, declared := unreachable[name]

		switch {
		case wired > 0 && declared:
			t.Errorf("pkg/next/%s is declared unreachable but %d production file(s) "+
				"import it. Wiring it was the goal, so take it off the list and "+
				"update the coverage tables that say it does not work.\n  was: %s",
				name, wired, reason)
		case wired == 0 && !declared:
			t.Errorf("pkg/next/%s is imported by no production file, so no guest can "+
				"reach it, and nothing says so. Either wire it, or add it to the "+
				"unreachable list here with the reason and what would change it, and "+
				"say so in docs/spectrum-next.md, COMPARISON.md and "+
				"VHDL_CONFORMANCE.md.", name)
		}
	}
}

func hasGoFiles(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			return true
		}
	}
	return false
}

// productionImporters counts non-test .go files outside the package itself that
// import it.
func productionImporters(t *testing.T, root, pkgPath, selfRel string) int {
	t.Helper()
	n := 0
	for _, tree := range []string{"cmd", "pkg"} {
		err := filepath.Walk(filepath.Join(root, tree), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			if strings.HasPrefix(filepath.ToSlash(rel), selfRel+"/") {
				return nil // the package's own files
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), `"`+pkgPath+`"`) {
				n++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", tree, err)
		}
	}
	return n
}
