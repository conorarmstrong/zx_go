package testharness

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/install/installtest"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// Screening real commercial titles, without any of them entering the repo.
//
// The compatibility manifest carried thirteen "Untested" entries because
// verifying one meant a person watching a screen. This drives the same check
// mechanically. The titles themselves are commercial and stay on the machine
// that owns them: the test reads a tab-separated list from
// testdata/local_titles.tsv, which is gitignored, and skips entirely when it
// is absent. CI sees a skip; a developer with a game collection sees verdicts.
//
// Columns: name, path, model, frames
// Lines beginning with # are comments.

type localTitle struct {
	name   string
	path   string
	model  roms.SpectrumModel
	frames int
}

func loadLocalTitles(t *testing.T) []localTitle {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "local_titles.tsv"))
	if err != nil {
		t.Skip("no testdata/local_titles.tsv — screening of local commercial titles is opt-in")
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	r.Comma = '\t'
	r.Comment = '#'
	r.FieldsPerRecord = 4
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse local_titles.tsv: %v", err)
	}
	models := map[string]roms.SpectrumModel{
		"48k": roms.Model48K, "128k": roms.Model128K, "plus2": roms.ModelPlus2,
		"plus3": roms.ModelPlus3, "pentagon": roms.ModelPentagon, "next": roms.ModelNext,
	}
	var out []localTitle
	for _, row := range rows {
		m, ok := models[strings.ToLower(row[2])]
		if !ok {
			t.Fatalf("unknown model %q for %q", row[2], row[0])
		}
		n, err := strconv.Atoi(row[3])
		if err != nil {
			t.Fatalf("bad frame count for %q: %v", row[0], err)
		}
		out = append(out, localTitle{name: row[0], path: row[1], model: m, frames: n})
	}
	return out
}

// TestScreenLocalTitles boots each listed title and reports a verdict.
//
// It is a measurement harness, not a pass/fail gate, and it never fails the
// build. The corpus it reads is a developer's own game collection: its
// contents are uncontrolled, and some titles legitimately will not load —
// copy-protected disks, for one, whose track layout is documented as not
// modelled. Turning those into test failures would leave the suite
// permanently red for anyone who happens to own such a disk, and would say
// nothing about the emulator that the recorded verdict does not already say.
//
// Every outcome, load failures included, is logged as a verdict for the
// manifest. Read the output; do not rely on the exit status.
func TestScreenLocalTitles(t *testing.T) {
	for _, tc := range loadLocalTitles(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := os.Stat(tc.path); err != nil {
				t.Skipf("not present: %v", err)
			}
			// A Next screened this way runs on the embedded 48K ROM in a
			// sandboxed install dir, not NextZXOS: bank injection and the
			// tape trap both need the plain ROM, and a test must never touch
			// the real install directory.
			if tc.model == roms.ModelNext {
				installtest.RedirectConfig(t)
				installOpenBottomROM(t)
			}
			h, err := New(tc.model)
			if err != nil {
				t.Fatalf("New(%v): %v", tc.model, err)
			}
			defer h.CloseFiles()

			s, err := h.ScreenFile(tc.path, tc.frames)
			if err != nil {
				t.Logf("VERDICT LoadError  %v", err)
				return
			}
			v := Classify(s)
			t.Logf("VERDICT %-10s pixels=%-6d colours=%-3d moved=%-5v err=%q",
				v, s.Pixels, s.Colours, s.Moved, s.Error)

			if dir := os.Getenv("ZX_GO_SCREEN_SHOTS"); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
				safe := strings.NewReplacer("/", "_", " ", "_", ":", "").Replace(tc.name)
				if err := h.SaveScreenshot(filepath.Join(dir, safe+".png")); err != nil {
					t.Logf("screenshot: %v", err)
				}
			}
		})
	}
}
