package main

import (
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// TestSonicControllableNoAutorun is a behaviour lock-in: once Sonic.nex is
// loaded and started, the character must NOT drift on its own when no input is
// held (the user reported "sonic runs right on his own"), and it MUST respond
// to a held Kempston direction. Sonic reads the Kempston joystick (port $1F);
// in the GUI the arrow keys drive those bits. The test skips when the
// (gitignored, copyrighted) ROM is absent, mirroring sonic_game_renders_test.go.
func TestSonicControllableNoAutorun(t *testing.T) {
	const sdPath = "/games/Next/Sonic/sonic.nex"
	prev := cliFlagsActive
	nf := cliFlags{}
	if prev != nil {
		nf = *prev
	}
	nf.noSound = true
	cliFlagsActive = &nf
	t.Cleanup(func() { cliFlagsActive = prev })

	emu, err := newNextEmulator()
	if err != nil {
		t.Skipf("no ROMs: %v", err)
	}
	emu.startNexloadMacro(sdPath)
	step := func(kemp byte) {
		emu.ula.KempstonState = kemp
		emu.cpu.ExecuteFrame(frameTStatesForModel(roms.ModelNext))
		if emu.peripherals != nil {
			emu.peripherals.Frame()
		}
		if emu.kbd != nil {
			emu.kbd.Tick()
		}
		if emu.nexloadMacro != nil && emu.nexloadMacro.tick(emu) {
			emu.nexloadMacro = nil
		}
	}
	// Boot + load into gameplay. The nexload macro may still be ticking (it
	// holds the *keyboard* through title init), but that does not gate the
	// Kempston joystick the game actually reads.
	for f := 0; f < 1450; f++ {
		step(0)
	}
	// Press Kempston fire to start the game.
	for i := 0; i < 10; i++ {
		step(0x10)
	}
	for i := 0; i < 20; i++ {
		step(0)
	}

	sonicX := func() int { return int(emu.nextSprites.Sprite(14).X) }

	// 1) No auto-run: with no input held, Sonic must stay put.
	before := sonicX()
	for i := 0; i < 150; i++ {
		step(0x00)
	}
	idleDrift := sonicX() - before
	if idleDrift < 0 {
		idleDrift = -idleDrift
	}
	if idleDrift > 8 {
		t.Fatalf("Sonic auto-ran while idle: sprite X drifted %d px (want <=8)", idleDrift)
	}

	// 2) Responsive: a held direction must move Sonic. Try LEFT then RIGHT so
	//    the test is robust to which edge of the level Sonic spawns against.
	moved := func(kemp byte) int {
		s0 := sonicX()
		for i := 0; i < 150; i++ {
			step(kemp)
		}
		d := sonicX() - s0
		if d < 0 {
			d = -d
		}
		// settle (release) so the next probe starts clean
		for i := 0; i < 40; i++ {
			step(0x00)
		}
		return d
	}
	leftMove := moved(0x02)
	rightMove := moved(0x01)
	if leftMove < 16 && rightMove < 16 {
		t.Fatalf("Sonic did not respond to input: leftMove=%d rightMove=%d (want one >=16)", leftMove, rightMove)
	}
	t.Logf("controllable: idleDrift=%d leftMove=%d rightMove=%d", idleDrift, leftMove, rightMove)

	// 3) GUI path: with NO joystick configured, the Next must still route the
	//    arrow keys to Kempston (the FPGA always decodes $1F) so the game is
	//    controllable out of the box. Verify against the live model.
	emu.joystickType = JoystickNone
	if got := emu.effectiveJoystick(); got != JoystickKempston {
		t.Fatalf("unconfigured Next: effectiveJoystick()=%v, want Kempston (arrows would drive nothing)", got)
	}
}
