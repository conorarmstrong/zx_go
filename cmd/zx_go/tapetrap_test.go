package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
	"github.com/conorarmstrong/zx_go/pkg/ula"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// writeTempTAP writes a one-block .tap (flag + data + XOR checksum, with the
// 2-byte little-endian length prefix) and returns its path.
func writeTempTAP(t *testing.T, flag byte, data []byte) string {
	t.Helper()
	body := append([]byte{flag}, data...)
	var csum byte
	for _, b := range body {
		csum ^= b
	}
	body = append(body, csum)
	out := []byte{byte(len(body)), byte(len(body) >> 8)}
	out = append(out, body...)
	p := filepath.Join(t.TempDir(), "syn.tap")
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The fast-load trap is allowed only when the 48 BASIC ROM (which holds
// LD-BYTES at $0556) is paged at $0000: always on the 48K, ROM bank 1 on the
// 128/+2/Pentagon, never on the Next. This is the gate whose 48K-only form
// silently broke 128K tape loading.
func TestTapeTrapROMActive(t *testing.T) {
	emu48, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	if !tapeTrapROMActive(emu48.mem) {
		t.Error("48K: tape trap should always be active")
	}

	emu128, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	if tapeTrapROMActive(emu128.mem) {
		t.Error("128K with the editor ROM (bank 0) mapped: trap must NOT be active")
	}
	emu128.mem.PageMemory(0x10) // 7FFD bit 4 → ROM 1 (48 BASIC)
	if !tapeTrapROMActive(emu128.mem) {
		t.Error("128K with 48 BASIC (bank 1) mapped: trap must be active")
	}

	emuN, err := newEmulator(roms.ModelNext)
	if err != nil {
		t.Fatal(err)
	}
	if tapeTrapROMActive(emuN.mem) {
		t.Error("Next: classic tape trap must never be active")
	}
}

// On 128K with the 48 BASIC ROM paged in, the LD-BYTES trap loads a block's
// bytes into memory and returns via the stacked address — the behaviour that
// was unreachable while the trap was 48K-only.
func TestTapeTrapLoadsBlockOn128K(t *testing.T) {
	emu, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	tp := ula.NewTapePlayer()
	if err := tp.LoadTAP(writeTempTAP(t, 0xFF, []byte{0x11, 0x22, 0x33, 0x44})); err != nil {
		t.Fatal(err)
	}
	emu.ula.SetTapePlayer(tp)
	tp.Play()
	installTapeTrap(emu)

	emu.mem.PageMemory(0x10) // page in the 48 BASIC ROM

	// LD-BYTES entry contract: A = expected flag, carry set = LOAD, IX = dest,
	// DE = byte count.
	emu.cpu.A = 0xFF
	emu.cpu.F |= z80.FLAG_C
	emu.cpu.IX = 0x8000
	emu.cpu.D, emu.cpu.E = 0x00, 0x04
	// Stack a return address ($4000) for the trap's emulated RET.
	emu.cpu.SP -= 2
	emu.mem.Write(emu.cpu.SP, 0x00)
	emu.mem.Write(emu.cpu.SP+1, 0x40)
	emu.cpu.PC = 0x0556

	if !emu.cpu.TrapCheck(0x0556) {
		t.Fatal("tape trap did not fire on 128K with the 48 BASIC ROM mapped")
	}
	for i, want := range []byte{0x11, 0x22, 0x33, 0x44} {
		if got := emu.mem.Read(0x8000 + uint16(i)); got != want {
			t.Errorf("loaded byte %d = $%02X, want $%02X", i, got, want)
		}
	}
	if emu.cpu.PC != 0x4000 {
		t.Errorf("PC after trap = $%04X, want $4000 (stacked return)", emu.cpu.PC)
	}
	if emu.cpu.F&z80.FLAG_C == 0 {
		t.Error("carry should be set (load success)")
	}
}

// End-to-end: boot 128K, pick "Tape Loader" from the menu, and confirm the
// real Renegade tape's blocks all load (the trap fires for each). Skips when
// the user's game file isn't present (it isn't committed).
func TestRenegade128KLoadsEndToEnd(t *testing.T) {
	const gamePath = "../../games/Renegade 128.tap"
	if _, err := os.Stat(gamePath); err != nil {
		t.Skip("game file not present; skipping end-to-end load test")
	}
	emu, err := newEmulator(roms.Model128K)
	if err != nil {
		t.Fatal(err)
	}
	tp := ula.NewTapePlayer()
	if err := tp.LoadTAP(gamePath); err != nil {
		t.Fatal(err)
	}
	emu.ula.SetTapePlayer(tp)
	tp.Play()
	installTapeTrap(emu)

	// Boot to the 128 menu, then ENTER (row 6 bit 0) to pick "Tape Loader"
	// (the default-highlighted top item), then let the loader run. Blocks 0-1
	// load instantly via the trap; blocks 2-8 load through the ROM/edge-timed
	// loader at real-tape speed, so the whole ~70 KB game finishes around frame
	// ~37000 (ERR_NR stays $FF — no "R Tape loading error"). The budget leaves
	// headroom; it's fast in wall-clock because headless runs many thousands of
	// frames/second.
	for i := 0; i < 45000; i++ {
		runOneFrameHeadless(emu, roms.Model128K)
		switch i {
		case 200:
			emu.kbd.PressMatrixKey(6, 0x01, true)
		case 206:
			emu.kbd.PressMatrixKey(6, 0x01, false)
		}
		if !tp.HasMoreBlocks() {
			break // every block consumed (ROM trap + custom loader) → loaded
		}
	}
	if tp.HasMoreBlocks() {
		t.Errorf("tape not fully loaded: %d/%d blocks consumed",
			tp.CurrentBlock(), tp.BlockCount())
	}
}

// tapeLoadingActive gates fast-tape turbo: true only while a tape is playing
// with blocks still to load.
func TestTapeLoadingActive(t *testing.T) {
	emu, err := newEmulator(roms.Model48K)
	if err != nil {
		t.Fatal(err)
	}
	if emu.tapeLoadingActive() {
		t.Error("no tape: should not be loading-active")
	}
	tp := ula.NewTapePlayer()
	if err := tp.LoadTAP(writeTempTAP(t, 0xFF, []byte{0x01, 0x02})); err != nil {
		t.Fatal(err)
	}
	emu.ula.SetTapePlayer(tp)
	if emu.tapeLoadingActive() {
		t.Error("tape loaded but not playing: should not be loading-active")
	}
	tp.Play()
	if !emu.tapeLoadingActive() {
		t.Error("tape playing with a block to load: should be loading-active")
	}
	tp.NextBlock() // consume the only block
	if emu.tapeLoadingActive() {
		t.Error("all blocks consumed: should not be loading-active")
	}
}
