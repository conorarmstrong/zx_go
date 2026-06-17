package main

import (
	"crypto/md5"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"sort"
	"strconv"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/snapshot"
)

// TestRenderSnapshotToPNG is a manual diagnostic: load a snapshot, run some
// frames, and write the rendered screen to a PNG so it can be eyeballed.
//
//	ZX_GO_RENDER_SNAP=/path/game.z80 \
//	ZX_GO_RENDER_FRAMES=300 \
//	ZX_GO_RENDER_OUT=/tmp/out.png \
//	go test ./cmd/zx_go -run TestRenderSnapshotToPNG -count=1 -v
func TestRenderSnapshotToPNG(t *testing.T) {
	snapPath := os.Getenv("ZX_GO_RENDER_SNAP")
	if snapPath == "" {
		t.Skip("set ZX_GO_RENDER_SNAP to a snapshot path")
	}
	frames := 300
	if v := os.Getenv("ZX_GO_RENDER_FRAMES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			frames = n
		}
	}
	out := os.Getenv("ZX_GO_RENDER_OUT")
	if out == "" {
		out = "/tmp/snaprender.png"
	}

	model := roms.Model128K
	emu, err := newEmulator(model)
	if err != nil {
		t.Fatalf("newEmulator: %v", err)
	}
	snap := snapshot.New()
	if err := snap.Load(snapPath); err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if err := applySnapshotToEmulator(emu, snap); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	t.Logf("after load: PC=%#04x SP=%#04x IFF1=%v IM=%d Halted=%v 7FFD=%#02x",
		emu.cpu.PC, emu.cpu.SP, emu.cpu.IFF1, emu.cpu.IM, emu.cpu.Halted, snap.Memory.Port7FFD)

	pcHist := map[uint16]int{}
	hashes := map[string]int{}
	var changedAt = -1
	var firstHash string
	for i := 0; i < frames; i++ {
		runOneFrameHeadless(emu, model)
		pcHist[emu.cpu.PC]++
		img := emu.renderFrame()
		sum := md5.Sum(img.Pix)
		h := hex.EncodeToString(sum[:])
		hashes[h]++
		if i == 0 {
			firstHash = h
		} else if h != firstHash && changedAt < 0 {
			changedAt = i
			save(t, img, "/tmp/snap_changed.png")
		}
	}
	t.Logf("distinct screens over %d frames: %d (first change at frame %d)", frames, len(hashes), changedAt)
	// top PC values
	type pc struct {
		v uint16
		n int
	}
	var top []pc
	for v, n := range pcHist {
		top = append(top, pc{v, n})
	}
	sort.Slice(top, func(a, b int) bool { return top[a].n > top[b].n })
	for i := 0; i < len(top) && i < 8; i++ {
		t.Logf("  PC %#04x : %d frames", top[i].v, top[i].n)
	}

	save(t, emu.renderFrame(), out)
	t.Logf("wrote %s after %d frames (PC now %#04x)", out, frames, emu.cpu.PC)
}

func save(t *testing.T, img image.Image, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}
