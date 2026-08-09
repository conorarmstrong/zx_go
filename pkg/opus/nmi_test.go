package opus

import "testing"

// The Opus has no DMA. The WD1770's DRQ line is wired to the Z80's NMI, and
// the ROM moves the sector one byte per non-maskable interrupt. The v2.2
// disassembly is unambiguous about it:
//
//	1886 NMI_READ   EX AF,AF' / LD A,B / OR C / LD A,(2803) (get byte from
//	                hardware) / JP Z,1892 / LD (DE),A / INC DE / DEC BC /
//	                EX AF,AF' / RETN
//	1895 NMI_WRITE  EX AF,AF' / LD A,(DE) / LD (2803),A (Give byte to
//	                hardware) / INC DE / DEC BC / EX AF,AF' / RETN
//
// and the caller sets HL to whichever of those routines applies, because
// "address +66 holds the instruction JP (HL)". The command is then written
// straight to $2800 and the ROM waits for BUSY to clear:
//
//	1519 DO_M_I/O  LD (2800),A / XOR A
//	151E WAIT_NMI  DEC A / JR NZ,151E   "Wait a while for the NMI to finish"
//	1520 WAIT_I/O  LD A,(2800) / BIT 0,A / RET Z
//
// So the controller must raise an NMI per byte and clear BUSY when the sector
// is done, or the ROM spins forever.

// countNMIs wires a counter to the device's NMI line.
func countNMIs(d *Device) *int {
	n := 0
	d.SetNMI(func() { n++ })
	return &n
}

// clock is a T-state counter the test advances by hand, standing in for the
// CPU's. settle runs it forward far enough for the next byte to become due.
//
// newClock establishes a baseline first, because in a real machine the
// pre-fetch hook has been ticking the controller since power-on long before
// any disk command is issued.
type clock struct{ t uint64 }

func newClock(d *Device) *clock {
	c := &clock{t: 1000}
	d.TickTo(c.t)
	return c
}

func (c *clock) settle(d *Device) {
	c.t += BytePeriodTStates
	d.TickTo(c.t)
}

// advance moves the clock by n T-states.
func (c *clock) advance(d *Device, n uint64) {
	c.t += n
	d.TickTo(c.t)
}

// TestReadSectorRaisesAnNMIPerByte is the core of the transfer.
func TestReadSectorRaisesAnNMIPerByte(t *testing.T) {
	img := NewImage()
	want, err := img.ReadSector(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		want[i] = byte(i ^ 0x5A)
	}
	if err := img.WriteSector(0, 1, want); err != nil {
		t.Fatal(err)
	}

	d := New()
	d.Mount(0, img)
	selectDrive(d, 0)
	nmis := countNMIs(d)

	clk := newClock(d)
	d.Write(FDCBase+1, 0x00) // track
	d.Write(FDCBase+2, 0x01) // sector
	d.Write(FDCBase+0, 0x8C) // the command FIND_ADDR builds for a read

	if d.Read(FDCBase+0)&StatusBusy == 0 {
		t.Fatal("BUSY is clear immediately after the command")
	}
	clk.settle(d)
	if *nmis == 0 {
		t.Fatal("no NMI was raised once the first byte was due")
	}

	// Drain the sector exactly as NMI_READ does.
	got := make([]byte, 0, SectorSize)
	for i := 0; i < SectorSize; i++ {
		if d.Read(FDCBase+0)&StatusDRQ == 0 {
			t.Fatalf("byte %d: DRQ is not asserted, so no NMI would arrive", i)
		}
		got = append(got, d.Read(FDCBase+3))
		clk.settle(d)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %#02x, want %#02x", i, got[i], want[i])
		}
	}
	if *nmis != SectorSize {
		t.Errorf("%d NMIs raised for a %d-byte sector, want one per byte", *nmis, SectorSize)
	}
	if st := d.Read(FDCBase + 0); st&StatusBusy != 0 {
		t.Errorf("status = %#02x: BUSY must clear so WAIT_I/O returns", st)
	}
	if st := d.Read(FDCBase + 0); st&StatusDRQ != 0 {
		t.Errorf("status = %#02x: DRQ must clear at the end of the transfer", st)
	}
}

// TestWriteSectorTakesBytesFromNMIs pins the write direction end to end.
func TestWriteSectorTakesBytesFromNMIs(t *testing.T) {
	img := NewImage()
	d := New()
	d.Mount(0, img)
	selectDrive(d, 0)
	nmis := countNMIs(d)

	clk := newClock(d)
	d.Write(FDCBase+1, 0x03) // track 3
	d.Write(FDCBase+2, 0x05) // sector 5
	d.Write(FDCBase+0, 0xAF) // the command FIND_ADDR builds for a write

	clk.settle(d)
	if *nmis == 0 {
		t.Fatal("no NMI was raised once the controller was ready for a byte")
	}
	for i := 0; i < SectorSize; i++ {
		if d.Read(FDCBase+0)&StatusDRQ == 0 {
			t.Fatalf("byte %d: DRQ is not asserted", i)
		}
		d.Write(FDCBase+3, byte(i^0xA5))
		clk.settle(d)
	}
	if st := d.Read(FDCBase + 0); st&StatusBusy != 0 {
		t.Errorf("status = %#02x: BUSY must clear after the write", st)
	}
	got, err := img.ReadSector(3, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i] != byte(i^0xA5) {
			t.Fatalf("image byte %d = %#02x, want %#02x", i, got[i], byte(i^0xA5))
		}
	}
	if *nmis != SectorSize {
		t.Errorf("%d NMIs raised, want %d", *nmis, SectorSize)
	}
}

// TestNoNMIWithoutATransfer guards the quiet path: Type I commands move no
// data, so they must not touch the NMI line.
func TestNoNMIWithoutATransfer(t *testing.T) {
	d := New()
	d.Mount(0, NewImage())
	selectDrive(d, 0)
	nmis := countNMIs(d)

	clk := newClock(d)
	d.Write(FDCBase+0, 0x00) // RESTORE
	clk.settle(d)
	d.Write(FDCBase+0, 0x1B) // SEEK
	clk.settle(d)
	d.Write(FDCBase+0, 0xD0) // FORCE INTERRUPT
	clk.settle(d)
	if *nmis != 0 {
		t.Errorf("%d NMIs raised by Type I commands, want 0", *nmis)
	}
	if st := d.Read(FDCBase + 0); st&StatusBusy != 0 {
		t.Errorf("status = %#02x: a Type I command must not leave BUSY set", st)
	}
}

// TestForceInterruptAbortsATransfer pins the ROM's BREAK path, which writes
// $D0 to stop a drive mid-command.
func TestForceInterruptAbortsATransfer(t *testing.T) {
	d := New()
	d.Mount(0, NewImage())
	selectDrive(d, 0)

	clk := newClock(d)
	d.Write(FDCBase+2, 0x01)
	d.Write(FDCBase+0, 0x8C)
	clk.settle(d)
	if d.Read(FDCBase+0)&StatusBusy == 0 {
		t.Fatal("precondition: the read should be running")
	}
	d.Write(FDCBase+0, 0xD0) // FORCE INTERRUPT
	if st := d.Read(FDCBase + 0); st&(StatusBusy|StatusDRQ) != 0 {
		t.Errorf("status = %#02x after FORCE INTERRUPT, want BUSY and DRQ clear", st)
	}
}

// TestMissingDiskDoesNotHang pins the failure path: with no disk the command
// must complete with an error rather than leaving BUSY set forever, which
// would wedge WAIT_I/O.
func TestMissingDiskDoesNotHang(t *testing.T) {
	d := New()
	selectDrive(d, 0)
	nmis := countNMIs(d)
	clk := newClock(d)
	d.Write(FDCBase+2, 0x01)
	d.Write(FDCBase+0, 0x8C)
	clk.settle(d)

	st := d.Read(FDCBase + 0)
	if st&StatusBusy != 0 {
		t.Errorf("status = %#02x: BUSY must clear with no disk present", st)
	}
	if st&StatusRecordNotFound == 0 {
		t.Errorf("status = %#02x: want RECORD NOT FOUND with no disk present", st)
	}
	if *nmis != 0 {
		t.Errorf("%d NMIs raised with no disk, want 0", *nmis)
	}
}

// TestDRQIsNotInstant is the bug this timing exists to prevent. The Opus ROM's
// NMI_READ needs about 83 T-states to run:
//
//	1886 EX AF,AF' / LD A,B / OR C / LD A,($2803) / JP Z,$1892 /
//	     LD (DE),A / INC DE / DEC BC / EX AF,AF' / RETN
//
// The DRQ that fetches byte n+1 is raised while the handler is still inside
// the LD A,($2803) that took byte n. If the controller asserted it there and
// then, the CPU would take the next NMI the moment that instruction retired —
// re-entering the handler at $188C, before the store, before RETN. DE and BC
// would never advance and the transfer would spin on the stack instead of
// moving data.
func TestDRQIsNotInstant(t *testing.T) {
	d := New()
	d.Mount(0, NewImage())
	selectDrive(d, 0)
	nmis := countNMIs(d)

	clk := newClock(d)
	clk.advance(d, 1000) // the counter does not start at zero in a real machine
	d.Write(FDCBase+2, 0x00)
	d.Write(FDCBase+0, 0x8C)
	clk.advance(d, BytePeriodTStates-1)
	if *nmis != 0 {
		t.Fatalf("%d NMIs before a byte period elapsed, want 0", *nmis)
	}
	if d.Read(FDCBase+0)&StatusDRQ != 0 {
		t.Error("DRQ asserted before a byte period elapsed")
	}
	clk.advance(d, 1)
	if *nmis != 1 {
		t.Errorf("%d NMIs after one byte period, want 1", *nmis)
	}

	// And taking that byte must not immediately produce the next one.
	d.Read(FDCBase + 3)
	if d.Read(FDCBase+0)&StatusDRQ != 0 {
		t.Error("DRQ re-asserted instantly after the byte was taken")
	}
	if *nmis != 1 {
		t.Errorf("%d NMIs, want the next byte to still be pending", *nmis)
	}
}

// TestByteSpacingOutrunsTheHandler pins the margin explicitly.
func TestByteSpacingOutrunsTheHandler(t *testing.T) {
	const handlerTStates = 83
	if BytePeriodTStates <= handlerTStates {
		t.Errorf("byte period %d T-states does not clear the %d-T-state handler",
			BytePeriodTStates, handlerTStates)
	}
}

// TestTransferSurvivesAFrameRebase pins the wrap. The CPU's T-state counter is
// frame-relative: it is reduced by a frame's worth every time the frame ends,
// so a transfer running across a frame boundary sees the clock go backwards.
// Comparing against an absolute deadline stalls the sector there, leaving BUSY
// asserted and the ROM polling $2800 forever.
func TestTransferSurvivesAFrameRebase(t *testing.T) {
	const frame = 69888

	d := New()
	d.Mount(0, NewImage())
	selectDrive(d, 0)
	nmis := countNMIs(d)

	now := uint64(frame - 500) // near the end of a frame
	d.TickTo(now)
	d.Write(FDCBase+2, 0x00)
	d.Write(FDCBase+0, 0x8C)

	// Tick at instruction granularity, as the pre-fetch hook does, and rebase
	// at the frame boundary exactly as the CPU does. Four frames is far more
	// than one sector needs, so if the transfer ever stalls it will not
	// finish.
	got := 0
	for step := 0; step < 4*frame/8 && got < SectorSize; step++ {
		now += 8
		if now >= frame {
			now -= frame
		}
		d.TickTo(now)
		if d.Read(FDCBase+0)&StatusDRQ != 0 {
			d.Read(FDCBase + 3)
			got++
		}
	}
	if got != SectorSize {
		t.Fatalf("moved %d of %d bytes: the transfer stalled across a frame rebase (status %#02x)",
			got, SectorSize, d.Read(FDCBase+0))
	}
	if *nmis != SectorSize {
		t.Errorf("%d NMIs, want %d", *nmis, SectorSize)
	}
	if st := d.Read(FDCBase + 0); st&StatusBusy != 0 {
		t.Errorf("status = %#02x: BUSY must clear at the end of the sector", st)
	}
}
