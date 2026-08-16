package testharness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/conorarmstrong/zx_go/pkg/plus3fdc"
	"github.com/conorarmstrong/zx_go/pkg/roms"
)

// A +3DOS bootable disk, built here rather than vendored.
//
// Every third-party candidate for "a disk the +3 ROM will actually boot" was
// rejected on licensing, so the disk this file builds is our own: assembled at
// test time from the +3DOS boot specification in the +3 manual, written to a
// temporary file, and mounted in drive A. Nothing binary is committed.
//
// Building it also buys coverage that no borrowed disk could. A loader that
// happens to exist issues whatever commands it was written to issue; this one
// is aimed deliberately at the FDC paths a synthetic stub misses — a SEEK to a
// cylinder that is not the one the head starts on, the SENSE INTERRUPT result
// phase that reports where the head ended up, and a four-sector READ DATA that
// makes the controller step R inside a single execution phase.
//
// The boot rule is the one in _tools/bootcheck: the +3's Loader reads side 0,
// track 0, sector 1, and executes those 512 bytes only if they sum to 3 modulo
// 256. It loads them at $FE00 and enters at $FE10 — past the 16-byte disk
// specification — with pages 4/7/6/3 at $0000/$4000/$8000/$C000.

const (
	bootOrg   = 0xFE00 // where the Loader puts the boot sector
	bootEntry = 0xFE10 // and where it enters it, past the disk specification

	// The +3's two FDC ports, as the guest program and the tests address them.
	fdcStatusPort = 0x2FFD // main status register, read only
	fdcDataPort   = 0x3FFD // command / parameter / data / result

	// Where the guest program leaves its evidence. All four are in page 3,
	// which the boot memory map holds at $C000, so the test reads them back
	// through the same paging the guest ran under.
	resRecalAddr = 0xC000 // 2 bytes: SENSE INTERRUPT after RECALIBRATE
	resSeekAddr  = 0xC002 // 2 bytes: SENSE INTERRUPT after SEEK
	resReadAddr  = 0xC004 // 7 bytes: the READ DATA result phase
	resParkAddr  = 0xC00C // 2 bytes: SENSE INTERRUPT after the closing SEEK
	resSigAddr   = 0xC010 // 4 bytes: "BOOT", written last of all

	// The multi-sector read: four sectors of the payload cylinder, into
	// page 6 at $8000.
	payloadAddr = 0x8000
	payloadCyl  = 2
	payloadFrom = 1
	payloadTo   = 4

	// Where the head is parked once the transfer is done. It exists so the run
	// does not end with the head where the transfer left it: a rewind into the
	// middle of the transfer has to put the head back, and a capture whose PCN
	// happens to match the present one cannot show whether it did.
	parkCyl = 5
)

// Geometry of the disk itself: the standard +3 "system" layout, 40 single-sided
// tracks of nine 512-byte sectors numbered 1..9.
const (
	diskCylinders = 40
	diskSectors   = 9
	diskSectorLen = 512
	diskFiller    = 0xE5
)

// payloadByte is what the builder writes at (sector, offset) on the payload
// cylinder and what the test expects to find in RAM afterwards. It varies with
// both coordinates on purpose: a transfer that returns the right number of
// bytes from the wrong sector, or the same sector four times over, has to fail.
func payloadByte(sector, offset int) byte {
	return byte(offset*7+sector*131) ^ 0x5A
}

// asm is a tiny Z80 emitter with labels. Bytes go down in order, labels are
// recorded as they are passed, and the displacements are filled in afterwards.
// The boot program's jumps are the one part of it that cannot be reviewed by
// reading, so they are computed rather than hand-counted.
type asm struct {
	org    uint16
	buf    []byte
	labels map[string]int
	rels   map[int]string // index of a JR displacement byte → its target label
	abss   map[int]string // index of an absolute address word → its target label
}

func newAsm(org uint16) *asm {
	return &asm{
		org:    org,
		labels: map[string]int{},
		rels:   map[int]string{},
		abss:   map[int]string{},
	}
}

func (a *asm) db(b ...byte) { a.buf = append(a.buf, b...) }
func (a *asm) dw(v uint16)  { a.db(byte(v), byte(v>>8)) }

func (a *asm) label(name string) { a.labels[name] = len(a.buf) }

// jr emits a relative jump (JR, JR cc) to a label that need not exist yet.
func (a *asm) jr(op byte, target string) {
	a.db(op)
	a.rels[len(a.buf)] = target
	a.db(0)
}

// call emits CALL nn to a label.
func (a *asm) call(target string) {
	a.db(0xCD)
	a.abss[len(a.buf)] = target
	a.dw(0)
}

// ldHLlabel emits LD HL,label; ldHL emits LD HL,nn.
func (a *asm) ldHLlabel(target string) {
	a.db(0x21)
	a.abss[len(a.buf)] = target
	a.dw(0)
}
func (a *asm) ldHL(v uint16) { a.db(0x21); a.dw(v) }

// assemble resolves the fix-ups and returns the code together with the label
// addresses. The addresses are what the capture test uses to stop the machine
// at a named point in the middle of a disk operation.
func (a *asm) assemble(t *testing.T) ([]byte, map[string]uint16) {
	t.Helper()
	out := append([]byte(nil), a.buf...)
	for at, name := range a.rels {
		to, ok := a.labels[name]
		if !ok {
			t.Fatalf("boot program: relative jump to undefined label %q", name)
		}
		d := to - (at + 1)
		if d < -128 || d > 127 {
			t.Fatalf("boot program: JR to %q is %d bytes away, out of range", name, d)
		}
		out[at] = byte(int8(d))
	}
	for at, name := range a.abss {
		to, ok := a.labels[name]
		if !ok {
			t.Fatalf("boot program: absolute reference to undefined label %q", name)
		}
		addr := a.org + uint16(to)
		out[at], out[at+1] = byte(addr), byte(addr>>8)
	}
	addrs := make(map[string]uint16, len(a.labels))
	for name, at := range a.labels {
		addrs[name] = a.org + uint16(at)
	}
	return out, addrs
}

// bootProgram is the guest half of the test: the code the +3 ROM loads off the
// disk and jumps into. It talks to the µPD765 directly through ports $2FFD
// (main status) and $3FFD (data), because the boot memory map has the ROM paged
// out — there is nothing to call.
//
// It records what the controller told it at each stage. That is what makes the
// assertions in this file assertions about the FDC rather than about a screen:
// the seek is proved by the PCN that SENSE INTERRUPT reports, the multi-sector
// step by the R the result phase comes back with.
func bootProgram(t *testing.T) ([]byte, map[string]uint16) {
	t.Helper()
	a := newAsm(bootEntry)

	send := func(b byte) { a.db(0x3E, b); a.call("send") }
	ldE := func(n byte) { a.db(0x1E, n) }
	// The two FDC ports differ only in the high address byte, so once BC holds
	// the status port a single LD B swings it to the data port.
	ldBCstatus := func() { a.db(0x01); a.dw(fdcStatusPort) }
	toDataPort := func() { a.db(0x06, byte(fdcDataPort>>8)) }

	// Pin the memory map the boot specification promises — pages 4/7/6/3 —
	// rather than trusting whatever the Loader happened to leave: $1FFD = 5 is
	// special paging, configuration 2.
	a.db(0xF3)             // DI
	a.db(0x01, 0xFD, 0x1F) // LD BC,$1FFD
	a.db(0x3E, 0x05)       // LD A,$05
	a.db(0xED, 0x79)       // OUT (C),A
	a.db(0x06, 0x7F)       // LD B,$7F        → BC = $7FFD
	a.db(0x3E, 0x10)       // LD A,$10
	a.db(0xED, 0x79)       // OUT (C),A
	a.db(0x31, 0x00, 0xFE) // LD SP,$FE00     → below the sector we are running

	// SPECIFY: step rate / head times, non-DMA. Every +3 loader issues it, and
	// it exercises the command that completes with no result phase at all.
	send(0x03)
	send(0x2F)
	send(0x03)

	// RECALIBRATE drive 0, then collect its two SENSE INTERRUPT bytes. This is
	// the reference point for the seek that follows: it puts the head on
	// cylinder 0, so the PCN the next SENSE INTERRUPT reports can only be
	// non-zero if a real seek moved it.
	send(0x07)
	send(0x00)
	send(0x08)
	a.ldHL(resRecalAddr)
	ldE(2)
	a.call("readres")

	// SEEK drive 0, head 0, to the payload cylinder, then SENSE INTERRUPT.
	send(0x0F)
	send(0x00)
	send(payloadCyl)
	// The seek is complete here and nothing has read its result yet: ST0 and
	// the new PCN are latched inside the controller, invisible at the ports.
	// The capture test stops here for exactly that reason.
	a.label("seekdone")
	send(0x08)
	a.ldHL(resSeekAddr)
	ldE(2)
	a.call("readres")

	// READ DATA (MFM, no skip) for sectors payloadFrom..payloadTo: nine command
	// bytes from the table at the end of the program.
	a.ldHLlabel("cmdread")
	ldE(9)
	a.label("sendcmd")
	a.db(0x7E) // LD A,(HL)
	a.db(0x23) // INC HL
	a.call("send")
	a.db(0x1D) // DEC E
	a.jr(0x20, "sendcmd")

	// Execution phase: stream bytes for as long as the controller holds EXM.
	// Four sectors arrive back to back inside this one loop — the FDC steps R
	// itself, and nothing here counts sectors.
	a.ldHL(payloadAddr)
	a.label("exloop")
	ldBCstatus()         // LD BC,$2FFD
	a.db(0xED, 0x78)     // IN A,(C)
	a.db(0xE6, 0x20)     // AND EXM
	a.jr(0x28, "exdone") // JR Z,exdone
	toDataPort()         // LD B,$3F         → BC = $3FFD
	a.db(0xED, 0x78)     // IN A,(C)
	a.db(0x77)           // LD (HL),A
	a.db(0x23)           // INC HL
	a.jr(0x18, "exloop")
	a.label("exdone")

	// Result phase: seven bytes of ST0/ST1/ST2 + CHRN.
	a.ldHL(resReadAddr)
	ldE(7)
	a.call("readres")

	// Park the head on a different cylinder. Ordinary for a loader, and it is
	// what stops the end of the run from looking like the middle of it: the
	// controller's present cylinder is now 5 where every capture point has 2.
	send(0x0F)
	send(0x00)
	send(parkCyl)
	send(0x08)
	a.ldHL(resParkAddr)
	ldE(2)
	a.call("readres")

	// The signature goes down last, so finding it proves every stage above ran
	// to completion rather than merely being reached.
	a.ldHLlabel("sig")
	a.db(0x11)
	a.dw(resSigAddr) // LD DE,resSigAddr
	a.db(0x01, 0x04, 0x00)
	a.db(0xED, 0xB0) // LD BC,4 / LDIR
	a.label("spin")
	a.jr(0x18, "spin")

	// send: write A to the data port once the controller asks for a byte.
	a.label("send")
	a.db(0xF5) // PUSH AF
	a.label("sendwait")
	ldBCstatus()     // LD BC,$2FFD
	a.db(0xED, 0x78) // IN A,(C)
	a.db(0xE6, 0xC0) // AND RQM|DIO
	a.db(0xFE, 0x80) // CP RQM               → DIO clear: CPU to FDC
	a.jr(0x20, "sendwait")
	a.db(0xF1) // POP AF
	a.db(0x01)
	a.dw(fdcDataPort) // LD BC,$3FFD
	a.db(0xED, 0x79)  // OUT (C),A
	a.db(0xC9)        // RET

	// readres: read E bytes of a result phase to (HL).
	a.label("readres")
	a.label("resloop")
	ldBCstatus()     // LD BC,$2FFD
	a.db(0xED, 0x78) // IN A,(C)
	a.db(0xE6, 0xC0) // AND RQM|DIO
	a.db(0xFE, 0xC0) // CP RQM|DIO           → DIO set: FDC to CPU
	a.jr(0x20, "resloop")
	toDataPort()     // LD B,$3F             → BC = $3FFD
	a.db(0xED, 0x78) // IN A,(C)
	a.db(0x77)       // LD (HL),A
	a.db(0x23)       // INC HL
	a.db(0x1D)       // DEC E
	a.jr(0x20, "resloop")
	a.db(0xC9) // RET

	a.label("cmdread")
	a.db(0x46, 0x00, payloadCyl, 0x00, payloadFrom, 0x02, payloadTo, 0x2A, 0xFF)
	a.label("sig")
	a.db('B', 'O', 'O', 'T')

	return a.assemble(t)
}

// bootSector is the 512 bytes of side 0, track 0, sector 1: the +3DOS disk
// specification, then the program, then the checksum adjuster that makes the
// Loader accept it.
func bootSector(t *testing.T) []byte {
	t.Helper()
	sec := make([]byte, diskSectorLen)
	// The disk specification, from the +3 manual: type 0 (single-sided, single
	// track), 40 tracks, 9 sectors, 512-byte sectors, one reserved track, 1K
	// blocks, two directory blocks, and the read/write and format gap lengths.
	copy(sec, []byte{
		0, 0, diskCylinders, diskSectors, 2, 1, 3, 2, 0x2A, 0x52,
		0, 0, 0, 0, 0, 0,
	})
	prog, _ := bootProgram(t)
	if len(prog) > len(sec)-(bootEntry-bootOrg) {
		t.Fatalf("boot program is %d bytes, too big for the sector", len(prog))
	}
	copy(sec[bootEntry-bootOrg:], prog)
	sec[15] = bootAdjuster(sec)
	return sec
}

// bootAdjuster is byte 15 of the boot sector. The +3's Loader executes the
// sector only if its 512 bytes sum to 3 modulo 256, and byte 15 of the disk
// specification is the byte reserved for making that true.
func bootAdjuster(sec []byte) byte {
	var sum byte
	for i, b := range sec {
		if i == 15 {
			continue
		}
		sum += b
	}
	return 3 - sum
}

// buildPlus3BootDisk writes the disk to a temporary file and returns its path.
// Sectors are laid down through the FDC's own FormatTrack, so the image is
// built by the same code path a guest FORMAT would drive, and serialised with
// SerializeDSK.
func buildPlus3BootDisk(t *testing.T) string {
	t.Helper()
	d := &plus3fdc.Disk{
		Sides:     1,
		Cylinders: diskCylinders,
		Tracks:    [][]*plus3fdc.Track{make([]*plus3fdc.Track, diskCylinders)},
	}
	boot := bootSector(t)
	for cyl := 0; cyl < diskCylinders; cyl++ {
		sectors := make([]plus3fdc.Sector, diskSectors)
		for i := range sectors {
			r := i + 1
			data := bytes.Repeat([]byte{diskFiller}, diskSectorLen)
			switch {
			case cyl == 0 && r == 1:
				copy(data, boot)
			case cyl == payloadCyl && r >= payloadFrom && r <= payloadTo:
				for o := range data {
					data[o] = payloadByte(r, o)
				}
			}
			sectors[i] = plus3fdc.Sector{C: byte(cyl), H: 0, R: byte(r), N: 2, Data: data}
		}
		if err := d.FormatTrack(0, cyl, sectors); err != nil {
			t.Fatalf("FormatTrack cylinder %d: %v", cyl, err)
		}
	}
	raw, err := d.SerializeDSK()
	if err != nil {
		t.Fatalf("SerializeDSK: %v", err)
	}
	path := filepath.Join(t.TempDir(), "plus3boot.dsk")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write disk image: %v", err)
	}
	return path
}

// TestBuiltDiskMeetsTheROMBootRule checks the image before any emulator runs,
// the way _tools/bootcheck does: the Loader will not execute a boot sector
// whose 512 bytes do not sum to 3 modulo 256, and a disk that fails here is
// refused with no way to tell it apart from a machine that never got the key.
func TestBuiltDiskMeetsTheROMBootRule(t *testing.T) {
	path := buildPlus3BootDisk(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back the built disk: %v", err)
	}
	d, err := plus3fdc.ParseDiskImage(raw)
	if err != nil {
		t.Fatalf("the built disk does not parse: %v", err)
	}
	sec := d.Track(0, 0).FindSector(1)
	if sec == nil {
		t.Fatal("side 0 track 0 has no sector 1, so there is nothing for the Loader to read")
	}
	if len(sec.Data) != diskSectorLen {
		t.Fatalf("boot sector is %d bytes, want %d", len(sec.Data), diskSectorLen)
	}
	var sum byte
	for _, b := range sec.Data {
		sum += b
	}
	if sum != 3 {
		t.Errorf("boot sector checksum is %d, want 3 — the +3 Loader would refuse this disk", sum)
	}
}

// bootPlus3 mounts the built disk and drives the +3 to the point where the boot
// sector has run. The +3 powers on into its ROM menu and stays there until
// ENTER selects the highlighted "Loader", which is the entry point to the real
// DOS_BOOT path: seek, read track 0 sector 1, checksum it, page 4/7/6/3 in and
// jump to $FE10.
func bootPlus3(t *testing.T, path string) *Harness {
	t.Helper()
	h := newPlus3WithDisk(t, path)
	h.RunFrames(menuFrames)
	h.TapKey("Return")
	h.RunFrames(loadFrames)
	return h
}

// menuFrames is how long the ROM takes to reach its power-on menu, loadFrames
// how long the Loader and the boot sector then need. Both are generous: the FDC
// transfers take no emulated time, so the whole load fits in a handful of
// frames.
const (
	menuFrames = 200
	loadFrames = 150
)

// newPlus3WithDisk builds a +3 with the disk in drive A, reset and ready to run
// but not yet run. The capture test needs this seam so it can install its hook
// before the Loader starts.
func newPlus3WithDisk(t *testing.T, path string) *Harness {
	t.Helper()
	h, err := New(roms.ModelPlus3)
	if err != nil {
		t.Fatalf("+3 harness: %v", err)
	}
	t.Cleanup(h.CloseFiles)
	if err := h.InsertPlus3Disk(0, path); err != nil {
		t.Fatalf("InsertPlus3Disk: %v", err)
	}
	h.Reboot()
	return h
}

// memBytes reads n bytes of guest memory through the paging the guest left in
// place, which for this disk is the boot map: 4/7/6/3.
func memBytes(h *Harness, addr uint16, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = h.Memory(addr + uint16(i))
	}
	return out
}

// TestBuiltDiskBootsThroughTheRealROM is the claim the whole file rests on: the
// +3 ROM, unmodified, read this disk and executed it. The signature is written
// by the last instruction of the guest program, after every FDC stage, so its
// presence cannot be produced by anything short of the boot sector running to
// the end.
func TestBuiltDiskBootsThroughTheRealROM(t *testing.T) {
	h := bootPlus3(t, buildPlus3BootDisk(t))
	if got := string(memBytes(h, resSigAddr, 4)); got != "BOOT" {
		t.Fatalf("the boot sector never signed off: $%04X holds %q, want %q\nscreen:\n%s",
			resSigAddr, got, "BOOT", h.ScreenText())
	}
}

// TestBootedDiskExercisesARealSeek pins the seek. RECALIBRATE leaves the head
// on cylinder 0 and SENSE INTERRUPT says so; the SEEK that follows has to move
// it, and the second SENSE INTERRUPT is where the controller admits whether it
// did. Both results also carry ST0 with SE set, which is the bit that
// distinguishes a completed seek from a controller that merely accepted the
// command.
func TestBootedDiskExercisesARealSeek(t *testing.T) {
	h := bootPlus3(t, buildPlus3BootDisk(t))
	if got, want := memBytes(h, resRecalAddr, 2), []byte{0x20, 0x00}; !bytes.Equal(got, want) {
		t.Errorf("SENSE INTERRUPT after RECALIBRATE = % 02X, want % 02X (ST0.SE, PCN 0)", got, want)
	}
	if got, want := memBytes(h, resSeekAddr, 2), []byte{0x20, payloadCyl}; !bytes.Equal(got, want) {
		t.Errorf("SENSE INTERRUPT after SEEK = % 02X, want % 02X (ST0.SE, PCN %d)",
			got, want, payloadCyl)
	}
	// The closing seek, which is a track-to-track step rather than a move off
	// track 0 — the case where a controller that only ever recalibrates looks
	// right.
	if got, want := memBytes(h, resParkAddr, 2), []byte{0x20, parkCyl}; !bytes.Equal(got, want) {
		t.Errorf("SENSE INTERRUPT after the closing SEEK = % 02X, want % 02X (ST0.SE, PCN %d)",
			got, want, parkCyl)
	}
}

// TestBootedDiskExercisesTheResultPhaseAndAMultiSectorRead pins the other two
// paths at once, because the result phase is where the multi-sector transfer
// reports what it did.
//
// R comes back one past EOT: the controller stepped it from payloadFrom to
// payloadTo inside a single execution phase, which is the whole difference
// between a multi-sector read and four single-sector ones. ST0 is an abnormal
// termination and ST1 carries EN — running out of cylinder at EOT is not a
// normal completion, and a controller that reported one would be the bug this
// assertion exists to catch.
func TestBootedDiskExercisesTheResultPhaseAndAMultiSectorRead(t *testing.T) {
	h := bootPlus3(t, buildPlus3BootDisk(t))
	got := memBytes(h, resReadAddr, 7)
	want := []byte{0x40, 0x80, 0x00, payloadCyl, 0x00, payloadTo + 1, 0x02}
	if !bytes.Equal(got, want) {
		t.Errorf("READ DATA result phase = % 02X, want % 02X\n"+
			"(ST0 abnormal, ST1.EN, ST2 clear, C=%d H=0 R=%d N=2)",
			got, want, payloadCyl, payloadTo+1)
	}
}

// TestBootedDiskLoadedEverySectorOfTheTransfer checks the bytes themselves. The
// result phase says four sectors moved; this says the right four moved, in
// order, with nothing dropped or repeated.
func TestBootedDiskLoadedEverySectorOfTheTransfer(t *testing.T) {
	h := bootPlus3(t, buildPlus3BootDisk(t))
	assertPayloadLoaded(t, h)
}

// guestReport is everything the boot program wrote down, for a failure message.
// A disk test that fails with "the bytes are wrong" and nothing else sends the
// reader back to re-run it with prints in; the controller's own account of the
// seek and the transfer is what says which stage went wrong.
func guestReport(h *Harness) string {
	return fmt.Sprintf("recalibrate=% 02X seek=% 02X read result=% 02X park=% 02X signature=%q",
		memBytes(h, resRecalAddr, 2), memBytes(h, resSeekAddr, 2),
		memBytes(h, resReadAddr, 7), memBytes(h, resParkAddr, 2),
		string(memBytes(h, resSigAddr, 4)))
}

// assertPayloadLoaded compares guest RAM against what the builder put on the
// payload cylinder. It reports the first difference rather than every one:
// 2048 mismatches is not more informative than the first.
func assertPayloadLoaded(t *testing.T, h *Harness) {
	t.Helper()
	for r := payloadFrom; r <= payloadTo; r++ {
		base := payloadAddr + (r-payloadFrom)*diskSectorLen
		for o := 0; o < diskSectorLen; o++ {
			want := payloadByte(r, o)
			if got := h.Memory(uint16(base + o)); got != want {
				t.Fatalf("sector %d byte %d (at $%04X) = $%02X, want $%02X — "+
					"the multi-sector read did not deliver this sector",
					r, o, base+o, got, want)
			}
		}
	}
}
