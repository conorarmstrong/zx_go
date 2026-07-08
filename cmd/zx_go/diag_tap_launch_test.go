package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
)

// TestDiagTapLaunch reproduces issue #10: launching a .tap from the
// NextZXOS Browser (image mode) via ENTER does nothing. It builds a
// bootable FAT32 image from the distro with output.tap at the root,
// boots it, navigates to OUTPUT.TAP, and presses ENTER.
//
//	ZX_GO_DIAG=1 ZX_GO_TEST_TAP=/path/output.tap NEX_RENDER_OUT_DIR=/tmp/shots \
//	  go test ./cmd/zx_go -run TestDiagTapLaunch -v -count=1 -timeout 600s
func TestDiagTapLaunch(t *testing.T) {
	if os.Getenv("ZX_GO_DIAG") == "" {
		t.Skip("diagnostic — set ZX_GO_DIAG=1 to run")
	}
	tapPath := os.Getenv("ZX_GO_TEST_TAP")
	if tapPath == "" {
		t.Skip("set ZX_GO_TEST_TAP to a .tap file")
	}
	tapData, err := os.ReadFile(tapPath)
	if err != nil {
		t.Fatalf("read tap: %v", err)
	}
	root := "../../roms/next/sd"
	if _, err := os.Stat(root); err != nil {
		t.Skip("distro SD folder absent")
	}

	// Build a bootable image from the distro, add OUTPUT.TAP at the root.
	img, err := buildNextSDImage(root)
	if err != nil {
		t.Fatalf("build image: %v", err)
	}
	if _, err := sdcard.AddFileToFAT32(img, "", "output.tap", tapData); err != nil {
		t.Fatalf("add tap: %v", err)
	}
	tmp := filepath.Join(t.TempDir(), "tap.img")
	if err := os.WriteFile(tmp, img, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZX_GO_NEXT_SD_IMG", tmp)
	// Deterministic runs: pin the RTC so instruction counts are stable.
	t.Setenv("ZX_GO_RTC_FIXED", "2026-07-08T12:00:00Z")

	emu := bootNextToMenu(t)

	shot := func(name string) {
		dir := os.Getenv("NEX_RENDER_OUT_DIR")
		if dir == "" {
			return
		}
		var buf bytes.Buffer
		if writeScreenshotPNG(emu, &buf) == nil {
			_ = os.WriteFile(dir+"/"+name+".png", buf.Bytes(), 0o644)
		}
	}

	// Open the Browser.
	nexPressCombo(emu, [][2]int{{6, 0x01}}, 6, 14) // ENTER
	nexRunFrames(emu, 200)
	shot("tap-01-browser")
	t.Logf("browser: PC=%#04x", emu.cpu.PC)

	// Navigate down toward OUTPUT.TAP (12th root entry ≈ 11 downs).
	// Screenshot every couple of steps so the cursor position is visible.
	for i := 0; i < 11; i++ {
		nexPressCombo(emu, [][2]int{{0, 0x01}, {4, 0x10}}, 4, 12) // CAPS+6 = DOWN
	}
	shot("tap-02-on-output")
	t.Logf("on-output: PC=%#04x", emu.cpu.PC)

	// Trace RST8/esxDOS API numbers and LD-BYTES tape-trap PC hits during
	// the launch, to see how far the tapload.bas → .tapein → M_TAPEIN →
	// LOAD "t:" chain gets.
	apiSeq := map[byte]int{}
	tapeTrapHits := map[uint16]int{}
	tapeTrapPCs := map[uint16]bool{0x0556: true, 0x04C6: true, 0x0562: true, 0x04D7: true, 0x056A: true}
	trace := true
	// Full RST8 call log with return state: at the $0008 fetch record the
	// inline API byte + A-in; when PC next reaches the return address
	// (param+1), record A-out and carry-out. Also keep a ring of recent
	// PCs so the instructions leading to the abort are visible.
	type rstCall struct {
		api        byte
		aIn        byte
		retAddr    uint16
		aOut       byte
		carryOut   bool
		returned   bool
		callerHint uint16 // paramAddr (where the RST8 inline byte lives)
		ringAt     int    // ringPos when the call was made
	}
	var rstLog []rstCall
	const ringN = 4096
	type pcEvt struct {
		pc uint16
		a  byte
		f  byte
	}
	var pcRing [ringN]pcEvt
	ringPos := 0
	// METADATA dot-command landmarks (runtime addrs mapped from the
	// binary): err_custom, exit_error, and each LD HL,msg error site.
	landmarks := map[uint16]string{
		0x2157: "LD HL,msg_nodata(Unable to access metadata)",
		0x2400: "LD HL,msg_allocerror(Out of memory bank)",
		0x2405: "LD HL,msg_metafull(No room for metadata)",
		0x23FB: "LD HL,msg_badnextzxos",
		0x2408: "err_custom (XOR A/SCF)",
		0x240A: "exit_error",
	}
	var landmarkHits []string
	metadataResident := func() bool {
		// METADATA's first bytes are E5 CF 88 (PUSH HL / RST8 / $88).
		return emu.mem.Read(0x2000) == 0xE5 && emu.mem.Read(0x2001) == 0xCF && emu.mem.Read(0x2002) == 0x88
	}
	lastMetaExitRing := -1
	var postExitLog []pcEvt // full instruction stream from METADATA exit onward
	var raiseDump []string
	var trampDump []string
	emu.cpu.AddPreFetchHook("diag-tape", func(pc uint16) {
		if !trace {
			return
		}
		pcRing[ringPos%ringN] = pcEvt{pc, emu.cpu.A, emu.cpu.F}
		ringPos++
		if lastMetaExitRing >= 0 && len(postExitLog) < 400_000 {
			postExitLog = append(postExitLog, pcEvt{pc, emu.cpu.A, emu.cpu.F})
		}
		// First $5B3E (NextZXOS RAM trampoline) after METADATA exits: the
		// fork point vs the oracle. Dump the stub code, stack and regs.
		if pc == 0x5B3E && lastMetaExitRing >= 0 && trampDump == nil {
			var stub, stack []byte
			for a := uint16(0x5B30); a < 0x5B70; a++ {
				stub = append(stub, emu.mem.Read(a))
			}
			for a := emu.cpu.SP - 8; a < emu.cpu.SP+24; a++ {
				stack = append(stack, emu.mem.Read(a))
			}
			trampDump = []string{
				fmt.Sprintf("regs: A=%02X F=%02X BC=%04X DE=%04X HL=%04X SP=%04X IX=%04X IY=%04X",
					emu.cpu.A, emu.cpu.F, emu.cpu.BC(), emu.cpu.DE(), emu.cpu.HL(), emu.cpu.SP, emu.cpu.IX, emu.cpu.IY),
				fmt.Sprintf("$5B30-$5B6F: % x", stub),
				fmt.Sprintf("stack SP-8..SP+23 (SP=%04X): % x", emu.cpu.SP, stack),
			}
		}
		// At the error-raise site ($0059 entry, first visit after the dot
		// exits): dump the CPU-visible bytes around the raising code and
		// full registers — identifies the bank and the raise mechanics.
		if pc == 0x0059 && lastMetaExitRing >= 0 && raiseDump == nil {
			var w1, w2 []byte
			for a := uint16(0x0040); a < 0x00A0; a++ {
				w1 = append(w1, emu.mem.Read(a))
			}
			for a := uint16(0x2030); a < 0x2060; a++ {
				w2 = append(w2, emu.mem.Read(a))
			}
			raiseDump = []string{
				fmt.Sprintf("regs: A=%02X F=%02X BC=%04X DE=%04X HL=%04X SP=%04X IX=%04X IY=%04X",
					emu.cpu.A, emu.cpu.F, emu.cpu.BC(), emu.cpu.DE(), emu.cpu.HL(), emu.cpu.SP, emu.cpu.IX, emu.cpu.IY),
				fmt.Sprintf("$0040-$009F: % x", w1),
				fmt.Sprintf("$2030-$205F: % x", w2),
			}
		}
		if name, ok := landmarks[pc]; ok && len(landmarkHits) < 64 && metadataResident() {
			landmarkHits = append(landmarkHits,
				fmt.Sprintf("%s A=$%02X Cy=%v HL=$%04X", name, emu.cpu.A, emu.cpu.F&1 != 0, emu.cpu.HL()))
			if pc == 0x240A {
				lastMetaExitRing = ringPos
			}
		}
		// Complete any pending call whose return address this is.
		for i := len(rstLog) - 1; i >= 0 && i >= len(rstLog)-8; i-- {
			if !rstLog[i].returned && rstLog[i].retAddr == pc {
				rstLog[i].returned = true
				rstLog[i].aOut = emu.cpu.A
				rstLog[i].carryOut = emu.cpu.F&0x01 != 0
				break
			}
		}
		if pc == 0x0008 {
			sp := emu.cpu.SP
			pa := uint16(emu.mem.Read(sp)) | uint16(emu.mem.Read(sp+1))<<8
			api := emu.mem.Read(pa)
			apiSeq[api]++
			if len(rstLog) < 512 {
				rstLog = append(rstLog, rstCall{api: api, aIn: emu.cpu.A, retAddr: pa + 1, callerHint: pa, ringAt: ringPos})
			}
		}
		if tapeTrapPCs[pc] {
			tapeTrapHits[pc]++
		}
	})

	// Trap writes to ERR_NR ($5C3A = bank 5 offset $1C3A): a write of
	// anything but $FF (OK) is a BASIC error report. Snapshot the report
	// code, the writer PC and the failing line (PPC $5C45) + statement
	// (SUBPPC $5C47) at that moment.
	type errEvt struct {
		code    byte
		writer  uint16
		line    uint16
		stmt    byte
		instant int
		chAdd   uint16
		xPtr    uint16
		ctx     []byte
	}
	var errEvts []errEvt
	var ringAtErr []pcEvt
	var dotWindowAtErr []byte
	var allErrNR []string
	emu.mem.SetRAMWriteHook(func(bank int, addr uint16, val byte) {
		if bank == 5 && addr == 0x1C3A && len(allErrNR) < 40 {
			allErrNR = append(allErrNR, fmt.Sprintf("ERR_NR=%02X pc=%04X ring=%d", val, emu.cpu.PC, ringPos))
		}
		if bank == 5 && addr == 0x1C3A && val != 0xFF {
			line := uint16(emu.mem.Read(0x5C45)) | uint16(emu.mem.Read(0x5C46))<<8
			errEvts = append(errEvts, errEvt{code: val, writer: emu.cpu.PC, line: line, stmt: emu.mem.Read(0x5C47), instant: len(errEvts)})
			if ringAtErr == nil {
				n := ringN
				if ringPos < n {
					n = ringPos
				}
				for i := 0; i < n; i++ {
					ringAtErr = append(ringAtErr, pcRing[(ringPos-n+i)%ringN])
				}
				// Snapshot the CPU-visible $2000-$204F window: reveals which
				// binary/overlay bank is mapped there at abort time.
				for a := uint16(0x2000); a < 0x2050; a++ {
					dotWindowAtErr = append(dotWindowAtErr, emu.mem.Read(a))
				}
				// CH_ADD ($5C5D) = interpreter text pointer, and X_PTR
				// ($5C5F) = the error-position copy ERROR-1 just made.
				// Dump 48 bytes around X_PTR: the exact BASIC text (or
				// command buffer) being executed when the error fired.
				chAdd := uint16(emu.mem.Read(0x5C5D)) | uint16(emu.mem.Read(0x5C5E))<<8
				xPtr := uint16(emu.mem.Read(0x5C5F)) | uint16(emu.mem.Read(0x5C60))<<8
				var ctx []byte
				startA := xPtr - 32
				for a := startA; a < xPtr+16; a++ {
					ctx = append(ctx, emu.mem.Read(a))
				}
				errEvts[len(errEvts)-1].chAdd = chAdd
				errEvts[len(errEvts)-1].xPtr = xPtr
				errEvts[len(errEvts)-1].ctx = ctx
			}
		}
	})

	// Press ENTER to launch, capturing finer frames to catch a mode menu.
	emu.kbd.PressMatrixKey(6, 0x01, true)
	nextRunFrames(emu, 12)
	emu.kbd.PressMatrixKey(6, 0x01, false)
	for i := 0; i < 20; i++ {
		nextRunFrames(emu, 20)
		shot(fmt.Sprintf("tap-e-%02d", i))
		t.Logf("enter+%03dframes: PC=%#04x blank=%v", (i+1)*20, emu.cpu.PC, uniformImage(emu.renderFrame()))
	}
	trace = false
	shot("tap-03-after-enter")
	t.Logf("after-enter: PC=%#04x blank=%v", emu.cpu.PC, uniformImage(emu.renderFrame()))
	var apis []string
	for a, n := range apiSeq {
		apis = append(apis, fmt.Sprintf("$%02X x%d", a, n))
	}
	t.Logf("RST8 esxDOS APIs during launch: %v", apis)
	t.Logf("LD-BYTES tape-trap PC hits: %v", tapeTrapHits)
	emu.mem.SetRAMWriteHook(nil)
	for _, e := range errEvts {
		t.Logf("ERR_NR write #%d: code=$%02X (report %d) writerPC=%#04x line=%d stmt=%d CH_ADD=%#04x X_PTR=%#04x",
			e.instant, e.code, int(e.code)+1, e.writer, e.line, e.stmt, e.chAdd, e.xPtr)
		if e.ctx != nil {
			t.Logf("  text around X_PTR-32..+16: % x", e.ctx)
			t.Logf("  as ASCII: %q", string(e.ctx))
		}
	}
	for _, s := range allErrNR {
		t.Logf("errnr-write: %s", s)
	}
	// Dump every RST8 between METADATA's exit and the error, with ring
	// positions — the post-dot path where ours and the oracle diverge.
	for i, c := range rstLog {
		mark := ""
		if c.carryOut {
			mark = "  <-- Cy=1"
		}
		if c.ringAt >= lastMetaExitRing-100 {
			t.Logf("rst8[%03d] ring=%d api=$%02X at=%#04x A_in=$%02X -> A_out=$%02X Cy=%v%s",
				i, c.ringAt, c.api, c.callerHint, c.aIn, c.aOut, c.carryOut, mark)
		}
	}
	for _, l := range landmarkHits {
		t.Logf("landmark: %s", l)
	}
	for _, l := range raiseDump {
		t.Logf("raise-site: %s", l)
	}
	for _, l := range trampDump {
		t.Logf("tramp-5b3e: %s", l)
	}
	t.Logf("ring index at METADATA exit=%d, at first error=%d (ringPos now %d)", lastMetaExitRing, ringPos, ringPos)
	// Write the full post-METADATA-exit instruction stream for offline
	// analysis (covers exit -> error regardless of the gap size).
	if out := os.Getenv("DIAG_RING_OUT"); out != "" && postExitLog != nil {
		var sb strings.Builder
		for i, e := range postExitLog {
			fmt.Fprintf(&sb, "%d pc=%04x a=%02x f=%02x\n", i, e.pc, e.a, e.f)
		}
		if err := os.WriteFile(out, []byte(sb.String()), 0o644); err != nil {
			t.Logf("ring write failed: %v", err)
		} else {
			t.Logf("%d post-exit events written to %s", len(postExitLog), out)
		}
	}
	t.Logf("$2000-$204F at abort: % x", dotWindowAtErr)
	// Reference: the first bytes of the METADATA binary for comparison.
	if md, err := os.ReadFile("../../roms/next/sd/dot/METADATA"); err == nil {
		t.Logf("METADATA file first $50: % x", md[:0x50])
	}

	// Give it more time in case a mode menu appeared needing a choice.
	nexRunFrames(emu, 300)
	shot("tap-04-later")
	t.Logf("later: PC=%#04x", emu.cpu.PC)

	// Select "1" = 128K mode at the TAP Loader menu and let the tape
	// load (fast-load may take a while; screenshot the progress).
	nexPressCombo(emu, [][2]int{{3, 0x01}}, 8, 16) // '1'
	for i := 1; i <= 12; i++ {
		nexRunFrames(emu, 250)
		shot(fmt.Sprintf("tap-load-%02d", i))
		t.Logf("load+%04dframes: PC=%#04x blank=%v", i*250, emu.cpu.PC, uniformImage(emu.renderFrame()))
	}
	fmt.Println("done")
}
