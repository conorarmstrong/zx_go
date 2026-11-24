package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// nexTypeLineBlind types an ASCII string without confirming each character
// against the OS, unmapped characters silently skipped.
//
// The NEXLOAD harness uses nexTypeLine, which confirms every character against
// the OS's own copy of the command line and is immune to the OS being busy.
// That cannot be used here: NextBASIC's LOAD leaves the previous line in the
// bank-7 mirror instead of clearing it, and a running program never touches it
// at all, so there is nothing to confirm against once LOAD has been submitted.
// This suite therefore keeps the timing-based typist it was written against.
func nexTypeLineBlind(emu *emulator, s string) {
	for _, c := range s {
		if keys, ok := nexKeyMatrix[c]; ok {
			nexPressCombo(emu, keys, 4, 10)
		}
	}
}

// runBasicFromMenu launches a NextBASIC program the way a user would: open the
// Command Line, change to the program's directory, LOAD it, then RUN.
//
// The CD matters. NextBASIC's LOAD resolves against the current directory, so
// `LOAD "/full/path/prog.bas"` fails with "File not found" — verified — while
// `.cd <dir>` followed by `LOAD "prog.bas"` works.
func runBasicFromMenu(emu *emulator, sdDir, file string, runFrames int) {
	enter := func(frames int) {
		nexPressCombo(emu, [][2]int{{6, 0x01}}, 4, 12)
		nexRunFrames(emu, frames)
	}
	nexPressCombo(emu, [][2]int{{0, 0x01}, {4, 0x10}}, 4, 10) // DOWN -> Command Line
	enter(80)

	// The path is quoted because .cd splits its argument on spaces: an
	// unquoted "/games/next/nextbasic invaders" is read as two paths and both
	// are reported missing, which is why NextBASIC Invaders would not load.
	nexTypeLineBlind(emu, `.cd "`+strings.ToLower(sdDir)+`"`)
	nexRunFrames(emu, 10)
	enter(120)

	nexTypeLineBlind(emu, `load "`+strings.ToLower(file)+`"`)
	nexRunFrames(emu, 15)
	enter(400)

	// Some programs autostart on LOAD; a RUN afterwards is harmless for those
	// and necessary for the rest.
	nexTypeLineBlind(emu, "run")
	nexRunFrames(emu, 15)
	enter(runFrames)
}

// TestNextBasicSDGames screens the NextBASIC (.bas) games on the SD card.
//
// These cannot be reached by NEXLOAD, which loads .nex files, so without this
// they were simply unscreened — including NextBASIC Invaders, which the
// compatibility manifest still lists as a Known issue.
func TestNextBasicSDGames(t *testing.T) {
	const sdRoot = "../../roms/next/sd"
	gamesDir := filepath.Join(sdRoot, "games", "Next")
	// As TestNexloadSDGames: NEX_SD_GAMES_SUBDIR screens another directory
	// under the card root, and NEX_SD_SHARD=i/n splits it across processes.
	// For another directory only one program per title is run, the one
	// mainProgram picks, since a title ships its loader beside the rest.
	sub := os.Getenv("NEX_SD_GAMES_SUBDIR")
	if sub != "" {
		gamesDir = filepath.Join(sdRoot, sub)
	}
	shard, shards := 0, 1
	if v := os.Getenv("NEX_SD_SHARD"); v != "" {
		if _, err := fmt.Sscanf(v, "%d/%d", &shard, &shards); err != nil || shards < 1 || shard < 0 || shard >= shards {
			t.Fatalf("NEX_SD_SHARD=%q: want i/n with 0 <= i < n", v)
		}
	}
	if _, err := os.Stat(gamesDir); err != nil {
		t.Skipf("no Next SD games present (gitignored): %v", err)
	}

	var basFiles []string
	_ = filepath.Walk(gamesDir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.EqualFold(filepath.Ext(p), ".bas") {
			basFiles = append(basFiles, p)
		}
		return nil
	})
	sort.Strings(basFiles)
	if sub != "" {
		basFiles = mainPrograms(gamesDir, basFiles)
	}
	if len(basFiles) == 0 {
		t.Skip("no .bas games on the SD card")
	}

	for i, host := range basFiles {
		if i%shards != shard {
			continue
		}
		rel, err := filepath.Rel(sdRoot, filepath.Dir(host))
		if err != nil {
			continue
		}
		sdDir := "/" + filepath.ToSlash(rel)
		name := filepath.Base(filepath.Dir(host))
		if sub != "" {
			r, _ := filepath.Rel(gamesDir, host)
			name = filepath.ToSlash(r)
		}

		t.Run(name, func(t *testing.T) {
			emu := bootNextToMenu(t)
			runBasicFromMenu(emu, sdDir, filepath.Base(host), 1200)

			px, cols := drawnInDisplayWindow(emu)
			nonBlank := px >= 1000 && cols >= 2
			if dir := os.Getenv("NEX_RENDER_OUT_DIR"); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
				var buf bytes.Buffer
				if writeScreenshotPNG(emu, &buf) == nil {
					safe := strings.NewReplacer(" ", "_", "/", "__").Replace(name)
					_ = os.WriteFile(filepath.Join(dir, "bas-"+safe+".png"), buf.Bytes(), 0o644)
				}
			}
			verdict := "Renders"
			if !nonBlank {
				verdict = "Blank"
			}
			t.Logf("BASVERDICT %-10s %-22s PC=%#04x pixels=%d colours=%d", verdict, name, emu.cpu.PC, px, cols)
		})
	}
}

// mainPrograms keeps one .bas per title directory (the first path element
// under root): the shallowest, preferring a name that says it starts the game
// or matches the directory, then the first in order.
func mainPrograms(root string, files []string) []string {
	best := map[string]string{}
	score := func(p string) int {
		rel, _ := filepath.Rel(root, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		s := 10 * len(parts)
		for _, w := range []string{"run", "start", "load", "main", "autoexec", "boot", "launch"} {
			if strings.Contains(base, w) {
				s -= 5
				break
			}
		}
		title := strings.ToLower(parts[0])
		if strings.Contains(title, base) || strings.Contains(base, strings.SplitN(title, "-", 2)[0]) {
			s -= 3
		}
		return s
	}
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			continue
		}
		title := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if cur, ok := best[title]; !ok || score(f) < score(cur) {
			best[title] = f
		}
	}
	out := make([]string, 0, len(best))
	for _, f := range best {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// drawnInDisplayWindow counts pixels differing from the frame's dominant
// colour, measured inside the 256x192 display window with the border cropped
// off, and the number of distinct colours there.
//
// uniformImage is too weak to judge a launch: a bare BASIC prompt with a
// single flashing cursor block is "not uniform" and passes it, which reported
// NextBASIC Invaders as rendering when it had not run at all.
func drawnInDisplayWindow(emu *emulator) (pixels, colours int) {
	img := emu.lastFrame() // nexRunFrames rendered it
	const bl, bt, w, h = 32, 24, 256, 192
	hist := map[uint32]int{}
	for y := bt; y < bt+h; y++ {
		for x := bl; x < bl+w; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			hist[(r>>8)<<24|(g>>8)<<16|(b>>8)<<8|(a>>8)]++
		}
	}
	best := 0
	for _, n := range hist {
		if n > best {
			best = n
		}
	}
	return w*h - best, len(hist)
}

// TestOtherSDFormats screens the titles in NEX_SD_GAMES_SUBDIR that ship
// neither a .nex nor a .bas, through the NextZXOS Command Line as a user would
// start them: a tape with `.tapein` then LOAD "", a snapshot (.snx, .sna,
// .z80) with NextBASIC's SPECTRUM "file", and a NEX or +3DOS BASIC file
// shipped without its extension with .nexload or LOAD. Report-only, like the BASIC screen;
// skipped unless the subdirectory is given.
func TestOtherSDFormats(t *testing.T) {
	const sdRoot = "../../roms/next/sd"
	sub := os.Getenv("NEX_SD_GAMES_SUBDIR")
	if sub == "" {
		t.Skip("NEX_SD_GAMES_SUBDIR not set")
	}
	gamesDir := filepath.Join(sdRoot, sub)
	titles, err := os.ReadDir(gamesDir)
	if err != nil {
		t.Skipf("%s: %v", gamesDir, err)
	}
	for _, title := range titles {
		if !title.IsDir() {
			continue
		}
		var tap, snap, nex string
		var basic []string
		hasNative := false
		_ = filepath.Walk(filepath.Join(gamesDir, title.Name()), func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".nex", ".bas":
				hasNative = true
			case ".tap":
				if tap == "" {
					tap = p
				}
			case ".snx", ".sna", ".z80":
				if snap == "" {
					snap = p
				}
			case "", ".00":
				// Programs shipped without their extension: a NEX file
				// keeps its "NextV" signature, a +3DOS file its header,
				// whose byte 15 is 0 for a BASIC program.
				head := make([]byte, 16)
				if f, err := os.Open(p); err == nil {
					n, _ := io.ReadFull(f, head)
					head = head[:n]
					_ = f.Close()
				}
				switch {
				case bytes.HasPrefix(head, []byte("NextV")) && nex == "":
					nex = p
				case len(head) == 16 && bytes.HasPrefix(head, []byte("PLUS3DOS")) && head[15] == 0:
					basic = append(basic, p)
				}
			}
			return nil
		})
		// Of several BASIC programs, the one named after the title is the
		// one to start; the rest are the parts it chains to.
		bas := ""
		squash := strings.NewReplacer(" ", "", "-", "", "_", "")
		for _, b := range basic {
			if bas == "" || strings.HasPrefix(squash.Replace(strings.ToLower(title.Name())), squash.Replace(strings.ToLower(filepath.Base(b)))) {
				bas = b
			}
		}
		if hasNative || (tap == "" && snap == "" && nex == "" && bas == "") {
			continue
		}
		host, kind := nex, "nex"
		switch {
		case host != "":
		case bas != "":
			host, kind = bas, "basic"
		case tap != "":
			host, kind = tap, "tape"
		default:
			host, kind = snap, "snapshot"
		}
		rel, _ := filepath.Rel(sdRoot, filepath.Dir(host))
		sdDir := "/" + filepath.ToSlash(rel)
		file := strings.ToLower(filepath.Base(host))
		name := title.Name()
		t.Run(name, func(t *testing.T) {
			emu := bootNextToMenu(t)
			enter := func(frames int) {
				nexPressCombo(emu, [][2]int{{6, 0x01}}, 4, 12)
				nexRunFrames(emu, frames)
			}
			nexPressCombo(emu, [][2]int{{0, 0x01}, {4, 0x10}}, 4, 10) // DOWN -> Command Line
			enter(80)
			nexTypeLineBlind(emu, `.cd "`+strings.ToLower(sdDir)+`"`)
			nexRunFrames(emu, 10)
			enter(120)
			switch kind {
			case "nex":
				nexTypeLineBlind(emu, `.nexload "`+file+`"`)
			case "basic":
				nexTypeLineBlind(emu, `load "`+file+`"`)
			case "tape":
				// .tapein mounts the tape; LOAD "t:" points LOAD at the tape
				// device, as .tapein's own help says, and LOAD "" then
				// loads from it.
				nexTypeLineBlind(emu, `.tapein "`+file+`"`)
				nexRunFrames(emu, 10)
				enter(120)
				nexTypeLineBlind(emu, `load "t:"`)
				nexRunFrames(emu, 10)
				enter(60)
				nexTypeLineBlind(emu, `load ""`)
			default:
				nexTypeLineBlind(emu, `spectrum "`+file+`"`)
			}
			nexRunFrames(emu, 10)
			enter(1500)
			px, cols := drawnInDisplayWindow(emu)
			if dir := os.Getenv("NEX_RENDER_OUT_DIR"); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
				var buf bytes.Buffer
				if writeScreenshotPNG(emu, &buf) == nil {
					_ = os.WriteFile(filepath.Join(dir, "other-"+name+".png"), buf.Bytes(), 0o644)
				}
			}
			t.Logf("OTHERVERDICT %-8s %-44s %s PC=%#04x pixels=%d colours=%d", kind, name, file, emu.cpu.PC, px, cols)
		})
	}
}
