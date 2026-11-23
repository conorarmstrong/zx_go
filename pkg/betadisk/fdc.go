package betadisk

// WD1793 status-register bits. The meaning of bits 1,2,4,5 depends on the
// command class (Type I head-movement vs Type II/III data-transfer); the
// aliases below name each interpretation.
const (
	statusBusy    = 0x01 // all commands: command in progress
	statusIndex   = 0x02 // Type I: index pulse
	statusDRQ     = 0x02 // Type II/III: data register needs service
	statusTrack0  = 0x04 // Type I: head over track 0
	statusLost    = 0x04 // Type II/III: lost data
	statusCRCErr  = 0x08 // CRC error
	statusSeekErr = 0x10 // Type I: seek error
	statusRNF     = 0x10 // Type II/III: record not found
	statusHeadDmp = 0x20 // Type I: head loaded
	statusWrFault = 0x20 // Type II write: write fault
	statusWrProt  = 0x40 // write commands: write protected
	statusNotRdy  = 0x80 // no disk / motor off
)

// stepRatesMs mirrors Fuse's rates[] for the Beta's FD1793
// (wd_fdc_alloc_fdc, wd_fdc.c:107-111): step-rate command bits 00/01/10/11
// → 6/12/20/30 ms per cylinder — the delay wd_fdc_type_i arms per step
// (event_add tstates + rates[b&3]*processor_speed/1000, wd_fdc.c:503).
var stepRatesMs = [4]uint64{6, 12, 20, 30}

// FDC models a WD1793 floppy-disk controller as wired into the Beta Disk
// interface. Data transfers advance one byte per data-register access — the
// service contract TR-DOS relies on. Command COMPLETION timing is modelled
// with Fuse's delayed-event law (wd_fdc.c): a Type-I seek does NOT set INTRQ
// at issue; completion is armed `steps × step-rate (+ verify 15ms + 5ms)`
// ahead and lands at the next port operation past the armed timestamp. TR-DOS
// DOES depend on this: its glue ($3D2F wait chain) reads IN $1F while waiting
// — which clears INTRQ on every read (Fuse wd_fdc_sr_read, wd_fdc.c:374) —
// and then polls IN $FF (Fuse beta_sp_read: intrq→bit7, datarq→bit6,
// beta.c:368-387) expecting the completion event to RE-ARM the line
// (cr_write resets INTRQ at issue, wd_fdc.c:928). Instant completion makes
// the INTRQ the glue consumed at issue the only pulse that ever exists: the
// $FF poll reads zero forever and the boot retry loop never ends.
type FDC struct {
	statusReg byte
	trackReg  byte
	sectorReg byte
	dataReg   byte
	cmdType1  bool // whether a Type I command is executing (live flag)
	// statusType1 mirrors Fuse's sticky f->status_type (wd_fdc.c): TYPE1
	// from reset (wd_fdc.c:82), set by every Type I command (cr_write
	// :966) and by Force Interrupt (:935), cleared only when a Type II/III
	// command arms (:996+:1020+). sr_read (:376) mirrors the index bit on
	// EVERY status read while TYPE1 — long after the seek completed. The
	// live cmdType1 flag cannot model this: the partner's hub polls bit1
	// waiting for a live index pulse with no command in flight.
	statusType1 bool

	side        int
	drive       int
	intrq       bool
	lastStepDir int // +1 (in) or -1 (out); remembered for a bare Step command

	// headLoad mirrors Fuse's wd_fdc head_load for the FD1793/Beta wiring:
	// disk_ready() on Beta 128 IS head_load ("READY = HLD", wd_fdc.c:200).
	// It is set by a Type-I command with the verify bit (wd_fdc.c:514) and
	// cleared by master reset (wd_fdc.c:68) and the 200ms motor-off timer
	// (wd_fdc.c:784 — not modelled here; no microsecond timing is modelled,
	// so head_load persists until reset, like a drive whose motor never gets
	// the timeout while DOS keeps re-verifying).
	headLoad bool

	// Spindle/index model, following Fuse's fdd.c. TR-DOS gates its boot
	// restore wait on the Type-I status Index bit: sr_read mirrors it from
	// `!d->loaded || d->index_pulse` (wd_fdc.c:378), where d->loaded is disk
	// INSERTION (fdd.c:293) — not head-load — and index_pulse toggles once
	// per revolution (fdd.c:467), high for 10ms and low for 190ms (Fuse's
	// index_event scheduling). Without a live pulse the partner DOS polls its
	// restore-complete loop forever and never issues the motor-on/seek/read
	// sequence. clk is the machine T-states getter (nil = index not modelled,
	// legacy behaviour kept for harnesses without a clock); frameT is one
	// frame in the same units — Fuse's 200ms revolution is 10 frames at
	// 50Hz, so the index period is 10*frameT and the high window half a
	// frame (10ms).
	clk      func() uint64
	frameT   uint64
	motorOn  bool
	idxHigh  bool
	idxNextT uint64

	disks        [4]*Image
	writeProtect [4]bool
	headTrack    [4]int // physical head position per drive

	// Armed completion event (Fuse event.c / wd_fdc.c): pending completions
	// fire at their armed timestamp; port accesses are the event boundaries.
	armT  uint64 // T-states at which the pending completion fires; 0 none
	armFn func() // the completion the event runs (finishTypeI etc.)

	// Active Type II/III data transfer.
	buf     []byte
	bufPos  int
	writing bool // true while a Write Sector transfer is in flight
	// Where an in-flight write is headed, latched when the command was issued:
	// the WD1793 finds the sector once, at the start, so a mid-transfer change
	// to the drive/side/sector registers cannot redirect bytes already flowing.
	// Kept as plain values rather than a completion closure because a closure
	// is the one thing a state capture cannot encode.
	wrDrive, wrCyl, wrSide, wrSec int
}

// NewFDC returns an idle controller with no disks mounted.
func NewFDC() *FDC { return &FDC{} }

// Reset performs a WD1793 master reset following Fuse's wd_fdc_master_reset
// (wd_fdc.c:61-95): transfers abort, head_load drops (the Beta also stops
// its spindle), the SELECTED drive's head steps out to track 0 (`while
// (!d->tr00) fdd_step(OUT)` :85-88), the three address registers clear, and
// the status register lands on WD_FDC_SR_LOST = $04 — bit 2 is "Lost data OR
// TRACK00" (wd_fdc.h:43), parked over track 0. Mounted disks stay mounted.
func (f *FDC) Reset() {
	f.statusReg = statusTrack0 // Fuse :94: status_register = WD_FDC_SR_LOST
	f.headTrack[f.drive] = 0   // Fuse :85-88: the selected drive parks at 0
	f.trackReg = 0             // Fuse :91-93
	f.sectorReg = 0
	f.dataReg = 0
	f.buf = nil
	f.bufPos = 0
	f.writing = false
	f.cmdType1 = false
	f.statusType1 = true // Fuse wd_fdc_master_reset: status_type = TYPE1 (wd_fdc.c:82)
	f.intrq = false
	f.headLoad = false // Fuse wd_fdc_master_reset: head_load = 0 (wd_fdc.c:68)
	f.motorOn = false  // master reset stops the spindle (fdd_reset via fdd Motoroff)
	f.idxHigh = false
	f.armFn, f.armT = nil, 0 // a master reset cancels pending events (event_remove_type)
}

// SetClock wires the machine T-states clock needed by the index-pulse model.
// frameT is one frame length in the same units (the CPU's FrameTStates).
// Without a clock the index bit keeps the legacy insertion mirroring so
// harnesses that construct the FDC bare are unaffected. clk must be
// ABSOLUTE monotonic (Fuse's tstates shape): the index pulse and every
// completion event arm at clk()+delay, so a clock that wraps (a folded
// frame-relative counter) makes those targets unreachable.
func (f *FDC) SetClock(clk func() uint64, frameT uint64) {
	f.clk = clk
	f.frameT = frameT
}

// armAfter schedules fn at now+ms in machine T-states — Fuse's
// event_add(tstates + delay*processor_speed/1000). A frame at 50Hz spans
// frameT T-states = 20ms, so 1ms = frameT/20. With no clock wired the
// completion lands immediately (legacy behaviour for bare harnesses).
func (f *FDC) armAfter(ms uint64, fn func()) {
	if f.clk == nil || f.frameT == 0 {
		fn()
		return
	}
	f.armT = f.clk() + (f.frameT/20)*ms
	f.armFn = fn
}

// Tick is the event boundary: port accesses are where a pending armed
// completion becomes visible to the guest (Fuse fires events from the CPU
// tstates loop; the fork has no per-tstate loop, so every Beta port read/
// write advances it first).
func (f *FDC) Tick() {
	if f.armFn == nil || f.clk == nil || f.clk() < f.armT {
		return
	}
	fn := f.armFn
	f.armFn, f.armT = nil, 0
	fn()
}

// spinUp mirrors Fuse wd_fdc_spinup for the FD1793/Beta wiring
// (wd_fdc.c:880-906), branch chosen by the controller state the command has
// just left cr_write in:
//
//	Type I (state=SEEK, set at cr_write :965 before spinup is called):
//	the SEEK branch (:881-897) — head loads only with the H bit (:883);
//	without H and without V it UNloads (:890-895), Beta motor off.
//	With V=1,H=0 the head stays as it was; the load lands at the end of
//	wd_fdc_type_i's type_i_verify (:510-521).
//	Everything else (Type II/III state READ/WRITE/READID, and Force
//	Interrupt which cr_write parks at NONE before calling
//	spinup(f, b & 0xf7) at :950): the else branch (:898-906) — the head
//	loads unconditionally and the Beta spindle starts.
//
// head_load is the Beta's READY (disk_ready :198-201), so this branch choice
// is what the Not-Ready status bit reads. fdd_motoron only spins when a disk
// is inserted (fdd.c); starting the motor arms the index pulse train — the
// first pulse lands one revolution later (Fuse schedules index_event at 190ms
// while index_pulse is low).
func (f *FDC) spinUp(cmd byte) {
	if cmd&0x80 == 0 { // Type I — state is SEEK
		if cmd&0x08 != 0 { // H: head load (:883-888)
			f.headLoad = true
			f.motorStart()
		} else if cmd&0x04 == 0 { // V=0: head unload (:890-895)
			f.headLoad = false
			f.motorStop()
		}
		return
	}
	f.headLoad = true // state != SEEK: unconditional head load (:899)
	f.motorStart()
}

// motorStart is Fuse's fdd_motoron(d, 1) on the Beta: the spindle starts (a
// clock-less rig has no spindle to model), arming the index train with the
// first pulse one revolution out.
func (f *FDC) motorStart() {
	if f.clk == nil || f.motorOn || !f.DiskPresent() {
		return
	}
	f.motorOn = true
	f.idxHigh = false
	f.idxNextT = f.clk() + 19*f.frameT/2
}

// motorStop is Fuse's fdd_motoron(d, 0): the spindle stops and the index
// train dies with it.
func (f *FDC) motorStop() {
	f.motorOn = false
	f.idxHigh = false
}

// syncIndex advances the index phase to "now", following Fuse fdd_event
// (:467-476): the pulse toggles and re-arms every half/9.5 frame while the
// motor runs with a disk inserted; it stops otherwise.
func (f *FDC) syncIndex(now uint64) {
	if !f.motorOn || !f.DiskPresent() {
		return
	}
	if f.frameT == 0 {
		// Clock wired with no frame length (bare harness): never re-arm,
		// never spin — idxNextT = now + 0 would loop forever here.
		return
	}
	for now >= f.idxNextT {
		f.idxHigh = !f.idxHigh
		if f.idxHigh {
			f.idxNextT = now + f.frameT/2 // 10ms high (Fuse index_event 10ms)
		} else {
			f.idxNextT = now + 19*f.frameT/2 // 190ms low
		}
	}
}

// diskReady reports controller readiness. On the Beta 128 wiring this IS
// head_load ("READY = HLD", Fuse wd_fdc.c:200) — a disk mounted is not
// enough; TR-DOS must land a Type-I verify first.
func (f *FDC) diskReady() bool { return f.headLoad }

// Mount attaches an image to a drive (0..3). A nil image ejects.
func (f *FDC) Mount(drive int, img *Image) {
	if drive < 0 || drive > 3 {
		return
	}
	f.disks[drive] = img
}

// SetWriteProtect marks a drive's disk read-only.
func (f *FDC) SetWriteProtect(drive int, wp bool) {
	if drive < 0 || drive > 3 {
		return
	}
	f.writeProtect[drive] = wp
}

// SelectDrive chooses the active drive (0..3).
func (f *FDC) SelectDrive(drive int) {
	if drive < 0 || drive > 3 {
		return
	}
	f.drive = drive
}

// SetSide chooses the active head (0 or 1).
func (f *FDC) SetSide(side int) { f.side = side & 1 }

// Intrq reports the interrupt-request line (set at command completion).
func (f *FDC) Intrq() bool { return f.intrq }

// Drq reports the data-request line (a byte is ready / needed).
func (f *FDC) Drq() bool { return f.statusReg&statusDRQ != 0 && !f.cmdType1 }

func (f *FDC) curDisk() *Image { return f.disks[f.drive] }

// DiskPresent reports whether the currently-selected drive holds a disk.
func (f *FDC) DiskPresent() bool { return f.curDisk() != nil }

// ReadTrackReg / WriteTrackReg / ReadSectorReg / WriteSectorReg expose the
// track and sector registers (Beta ports $3F and $5F).
func (f *FDC) ReadTrackReg() byte    { return f.trackReg }
func (f *FDC) WriteTrackReg(v byte)  { f.trackReg = v }
func (f *FDC) ReadSectorReg() byte   { return f.sectorReg }
func (f *FDC) WriteSectorReg(v byte) { f.sectorReg = v }

// ReadStatus returns the status register (Beta port $1F read). Reading status
// clears INTRQ, matching the WD1793. The ready bit (bit 7, Fuse
// WD_FDC_SR_MOTORON "Motor on or READY") is recomputed on every read from
// disk_ready(): cleared when ready, set when not (Fuse wd_fdc_sr_read,
// wd_fdc.c:388-391). TR-DOS polls this bit before trusting a command.
func (f *FDC) ReadStatus() byte {
	f.intrq = false
	if f.statusType1 { // Fuse sr_read (wd_fdc.c:376-381): mirrors the index
		// bit on EVERY read while the sticky status_type is TYPE1:
		// Index bit from `!d->loaded || d->index_pulse` — d->loaded is disk
		// insertion, and index_pulse is the live once-per-revolution strobe.
		f.statusReg &^= statusIndex
		if f.clk != nil {
			f.syncIndex(f.clk())
			if !f.DiskPresent() || (f.motorOn && f.idxHigh) {
				f.statusReg |= statusIndex
			}
		} else if !f.DiskPresent() {
			// Fuse sr_read (:378): `!d->loaded || index_pulse` — d->loaded
			// is disk INSERTION; with no disk the index bit always reads
			// high. With a disk and no clock there is no strobe, so it
			// reads low — that is the honest clock-less mirror.
			f.statusReg |= statusIndex
		}
	}
	if f.diskReady() {
		f.statusReg &^= statusNotRdy
	} else {
		f.statusReg |= statusNotRdy
	}
	return f.statusReg
}

// WriteCommand issues a command (Beta port $1F write), mirroring Fuse
// wd_fdc_cr_write (wd_fdc.c:924-1029) in exact order:
//
//  1. Force Interrupt ($D0-$DF) is honoured even while busy (:930-953) and
//     spins the drive up on its way out (:950).
//  2. Any other command arriving while busy is ignored (:955-956).
//  3. Type I (:964-974): state=SEEK, sticky status_type=TYPE1, clear
//     CRC/RNF/Index, then spinUp — which for state==SEEK takes the H/V
//     branch (:881-897): load with H, UNLOAD with V=0, hold with V=1,H=0 —
//     then wd_fdc_type_i.
//  4. Type II/III (:975-1028): the READY gate runs BEFORE spinUp
//     (:975-983/:1008-1013) — a not-ready controller (head unloaded, since
//     Beta's disk_ready IS head_load, :198-201) is rejected without ever
//     spinning. Only past the gate does spinUp's else branch (:898-906)
//     head-load unconditionally and start the Beta spindle.
//
// The gate-before-spinup order is why TR-DOS's boot sequence works: its
// first Restore (with V) loads the head, which makes the drive READY for the
// reads that follow; a raw Read Sector from a cold reset gets cleanly rejected
// with INTRQ instead of silently pretending.
func (f *FDC) WriteCommand(cmd byte) {
	// Force Interrupt first — valid during busy (Fuse cr_write 0xd0-0xdf arm).
	if cmd&0xF0 == 0xD0 {
		f.intrq = false
		f.forceInterrupt(cmd)
		return
	}
	if f.statusReg&statusBusy != 0 { // Fuse: `if (status & BUSY) return`
		return
	}
	f.intrq = false
	if cmd&0x80 == 0 { // Type I — spinUp takes the SEEK branch (:881-897)
		f.spinUp(cmd)
		switch {
		case cmd&0xF0 == 0x00: // Restore
			f.typeISeek(0, cmd)
		case cmd&0xF0 == 0x10: // Seek (to track in data register)
			f.typeISeek(int(f.dataReg), cmd)
		case cmd&0xE0 == 0x20: // Step
			f.typeIStep(cmd, f.lastStepDir)
		case cmd&0xE0 == 0x40: // Step In (towards higher tracks)
			f.lastStepDir = +1
			f.typeIStep(cmd, +1)
		case cmd&0xE0 == 0x60: // Step Out (towards track 0)
			f.lastStepDir = -1
			f.typeIStep(cmd, -1)
		}
		return
	}
	// Type II/III — the READY gate comes FIRST (:975-983/:1008-1013).
	if !f.diskReady() {
		f.rejectTypeII()
		return
	}
	f.spinUp(cmd) // else branch head-load + Beta spindle (:898-906)
	switch {
	case cmd&0xE0 == 0x80: // Read Sector
		f.readSector()
	case cmd&0xE0 == 0xA0: // Write Sector
		f.writeSector()
	case cmd&0xF0 == 0xC0: // Read Address
		f.readAddress()
	default:
		// Read Track / Write Track ($E0/$F0): unsupported by TR-DOS in
		// normal use — complete immediately so a stray command can't hang.
		f.endTypeII(0)
	}
}

// armTypeI is Fuse wd_fdc_type_i's completion schedule collapsed to one
// armed event (wd_fdc.c:462-556): every track to move arms one event at
// rates[b&3] ms; after the last step a verify adds 15ms (head-load event,
// :514-521) plus 5ms (seek_verify hop, :441-446) before read_id lands and
// the tail clears BUSY and sets INTRQ (:553-555). A command with no steps
// and no verify completes synchronously at issue — exactly Fuse's tail.
// The track/head registers land at completion, like the per-step events.
func (f *FDC) armTypeI(cur, target, dir int, cmd byte) {
	if target < 0 { // a step out past track 0 lands on track 0 (fdd_step floor)
		target = 0
	}
	steps := 0
	if cur != target {
		if dir < 0 && f.headTrack[f.drive] == 0 {
			// Fuse's tr00 shortcut (wd_fdc.c:496-498): head over track 0
			// stepping out — track pins to 0, NO step, NO event.
		} else {
			steps = cur - target
			if steps < 0 {
				steps = -steps
			}
		}
	}
	finalHead := f.headTrack[f.drive] + dir*steps
	if finalHead < 0 {
		finalHead = 0
	}
	finalTrack := int(f.trackReg)
	isStep := cmd&0xF0 == 0x40 || cmd&0xF0 == 0x60 // STEP / STEP-IN / STEP-OUT
	if cmd&0xF0 == 0x00 {                          // restore lands at track 0 (tr00 pin, :497)
		finalTrack = 0
	} else if cmd&0x10 != 0 { // U: update track register (:493-494)
		if isStep {
			// Fuse wd_fdc.c:478-480: a STEP with u jumps straight to
			// type_i_update at ISSUE — track_register += direction,
			// exactly once, the register moving relative to ITSELF.
			// The physical head (fdd_step, and headTrack here) moves
			// on every step regardless of u, so after a u=0 step the
			// register can legitimately sit behind the head — it does
			// NOT chase headTrack. The tr00 pin still lands after
			// the increment (:496-498): at track 0 stepping out the
			// register pins to 0; off track 0 the byte wraps, as it
			// does in Fuse's libspectrum_byte.
			finalTrack += dir
			if dir < 0 && f.headTrack[f.drive] == 0 {
				finalTrack = 0
			}
			f.trackReg = byte(finalTrack)
		} else {
			// SEEK: the register walks one per step event (:489-494)
			// — collapsed to the target by this one-shot model.
			finalTrack = target
		}
	}

	f.statusReg |= statusBusy
	delay := uint64(steps) * stepRatesMs[cmd&0x03]
	if cmd&0x04 != 0 { // verify: head-load 15ms + seek_verify hop 5ms
		f.headLoad = true
		delay += 20
	}
	if delay == 0 {
		f.trackReg = byte(finalTrack)
		f.headTrack[f.drive] = finalHead
		f.finishTypeI()
		return
	}
	f.armAfter(delay, func() {
		f.trackReg = byte(finalTrack)
		f.headTrack[f.drive] = finalHead
		f.finishTypeI()
	})
}

func (f *FDC) typeISeek(target int, cmd byte) {
	f.cmdType1 = true
	// Fuse cr_write Type I (wd_fdc.c:963-969): status_type = TYPE1 and the
	// CRC/RNF/Index bits are cleared at command issue.
	f.statusType1 = true
	f.statusReg &^= statusCRCErr | statusRNF | statusIndex
	if cmd&0xF0 == 0x00 { // Restore: track_register=$FF, target 0 (wd_fdc.c:483-485)
		f.trackReg = 0xFF
		target = 0
	}
	if target < 0 {
		target = 0
	}
	cur := int(f.trackReg)
	dir := +1
	if cur > target {
		dir = -1
	}
	f.armTypeI(cur, target, dir, cmd)
}

func (f *FDC) typeIStep(cmd byte, dir int) {
	f.cmdType1 = true
	// Fuse cr_write Type I: status_type = TYPE1, CRC/RNF/Index cleared.
	f.statusType1 = true
	f.statusReg &^= statusCRCErr | statusRNF | statusIndex
	cur := int(f.trackReg)
	f.armTypeI(cur, cur+dir, dir, cmd)
}

// rejectTypeII mirrors Fuse's ready gate on Type II/III commands
// (wd_fdc.c:975-980, 1008-1013): a not-ready controller clears Busy, drops
// to idle and raises INTRQ without executing anything.
func (f *FDC) rejectTypeII() {
	f.buf = nil
	f.bufPos = 0
	f.writing = false
	f.statusReg &^= statusBusy | statusDRQ
	f.intrq = true
}

// finishTypeI is Fuse wd_fdc_type_i's completion tail verbatim (:545-552):
// mirror the tr00 flip-flop into bit 2 ("Lost data OR TRACK00", wd_fdc.h:43),
// clear BUSY, raise INTRQ — and touch NOTHING else. Bit 5 is Fuse's
// SPINUP/record-type bit (wd_fdc.h:40) and the FD1793/Beta never writes it
// (no status_register bit-5 store exists anywhere in wd_fdc.c); the fork's
// old "head loaded here" write was invented. Head-load lives in head_load and
// surfaces as bit 7 through the disk_ready recompute in sr_read (:388-391).
// Track-0 in bit 2 reflects the head position only — never disk presence:
// TR-DOS gates its boot step loop on this bit (the $3E32 `and $04` check in
// the partner ROM), and Fuse reports it even for a drive with no disk.
func (f *FDC) finishTypeI() {
	if f.headTrack[f.drive] == 0 {
		f.statusReg |= statusTrack0
	} else {
		f.statusReg &^= statusTrack0
	}
	f.statusReg &^= statusBusy
	f.intrq = true
}

func (f *FDC) readSector() {
	f.cmdType1 = false
	f.statusType1 = false // Fuse: status_type = TYPE2 (wd_fdc.c:996+)
	disk := f.curDisk()
	if disk == nil {
		// Fuse has no write-whole-status failure path: an
		// unreachable disk fails the track walk as sticky RNF
		// (`status_register |= WD_FDC_SR_RNF` :589/:601/:740/:1068)
		// and Ready (bit 7) stays truthful through the sr_read
		// disk_ready recompute (:388-391).
		f.statusReg |= statusRNF
		f.statusReg &^= statusBusy | statusDRQ
		f.intrq = true
		return
	}
	data, err := disk.ReadSector(f.headTrack[f.drive], f.side, int(f.sectorReg))
	if err != nil {
		f.statusReg |= statusRNF // sticky, Fuse :589/:601/:740/:1068
		f.statusReg &^= statusBusy | statusDRQ
		f.intrq = true
		return
	}
	f.buf = data
	f.bufPos = 0
	f.writing = false
	f.statusReg = statusBusy | statusDRQ
}

func (f *FDC) writeSector() {
	f.cmdType1 = false
	f.statusType1 = false // Fuse: status_type = TYPE2 (wd_fdc.c:996+)
	disk := f.curDisk()
	if disk == nil {
		// Fuse has no write-whole-status failure path: an
		// unreachable disk fails the track walk as sticky RNF
		// (`status_register |= WD_FDC_SR_RNF` :589/:601/:740/:1068)
		// and Ready (bit 7) stays truthful through the sr_read
		// disk_ready recompute (:388-391).
		f.statusReg |= statusRNF
		f.statusReg &^= statusBusy | statusDRQ
		f.intrq = true
		return
	}
	if f.writeProtect[f.drive] {
		f.statusReg |= statusWrProt // sticky OR, Fuse's failure-path law
		f.statusReg &^= statusBusy | statusDRQ
		f.intrq = true
		return
	}
	cyl, side, sec := f.headTrack[f.drive], f.side, int(f.sectorReg)
	// Validate the target up-front so an out-of-range write fails as RNF
	// rather than mid-transfer.
	if _, err := disk.ReadSector(cyl, side, sec); err != nil {
		f.statusReg |= statusRNF // sticky OR, Fuse failure-path law
		f.statusReg &^= statusBusy | statusDRQ
		f.intrq = true
		return
	}
	f.buf = make([]byte, SectorSize)
	f.bufPos = 0
	f.writing = true
	f.wrDrive, f.wrCyl, f.wrSide, f.wrSec = f.drive, cyl, side, sec
	f.statusReg = statusBusy | statusDRQ
}

// commitWrite lands a completed write transfer on the sector the command was
// addressed to. The disk is looked up now instead of being latched with the
// address, so a disk ejected mid-transfer takes no write; on real hardware
// pulling the disk part-way through leaves the sector damaged in a way no
// image format records, and dropping the write is the only unambiguous choice
// available.
func (f *FDC) commitWrite() {
	if disk := f.disks[f.wrDrive]; disk != nil {
		_ = disk.WriteSector(f.wrCyl, f.wrSide, f.wrSec, f.buf)
	}
}

func (f *FDC) readAddress() {
	f.cmdType1 = false
	f.statusType1 = false // Fuse: status_type = TYPE2 (wd_fdc.c:996+)
	if f.curDisk() == nil {
		// Fuse has no write-whole-status failure path: an
		// unreachable disk fails the track walk as sticky RNF
		// (`status_register |= WD_FDC_SR_RNF` :589/:601/:740/:1068)
		// and Ready (bit 7) stays truthful through the sr_read
		// disk_ready recompute (:388-391).
		f.statusReg |= statusRNF
		f.statusReg &^= statusBusy | statusDRQ
		f.intrq = true
		return
	}
	// The 6-byte ID field the WD1793 streams: track, side, sector, length
	// code (01 = 256 bytes), and a two-byte CRC (we report 0 — TR-DOS doesn't
	// verify it on the address read).
	f.buf = []byte{f.trackReg, byte(f.side), 1, 0x01, 0x00, 0x00}
	// Read Address also copies the track byte into the sector register.
	f.sectorReg = f.trackReg
	f.bufPos = 0
	f.writing = false
	f.statusReg = statusBusy | statusDRQ
}

// forceInterrupt implements the WD1793 Type IV command exactly as Fuse's
// wd_fdc_cr_write does for $D0-$DF (wd_fdc.c:930-954): clear Busy/WriteFault/
// CRC/Error and the Index-DRQ bit, go idle with Type-I status meanings, abort
// any transfer, raise INTRQ ONLY if bit 3 of the command asks for it (the
// caller already cleared INTRQ on the register write — an unconditional raise
// here makes the partner consume a phantom interrupt), mirror the Track-0
// flip-flop into the LOST bit, and spin up (head-load) with the 'h' bit
// masked out.
func (f *FDC) forceInterrupt(cmd byte) {
	f.armFn, f.armT = nil, 0 // Fuse cr_write force-interrupt: event_remove_type (:931)
	f.statusReg &^= statusBusy | statusWrFault | statusCRCErr | statusIndex | statusDRQ
	f.cmdType1 = true
	f.statusType1 = true // Fuse cr_write force-interrupt: status_type = TYPE1 (wd_fdc.c:935)
	f.buf = nil
	f.bufPos = 0
	f.writing = false
	if cmd&0x08 != 0 {
		f.intrq = true
	}
	if f.headTrack[f.drive] == 0 {
		f.statusReg |= statusLost // tr00 flip-flop (same bit position)
	} else {
		f.statusReg &^= statusLost
	}
	f.spinUp(0xFF) // Fuse: wd_fdc_spinup(f, b & 0xf7) (:950) — state is NONE,
	// the else branch loads the head unconditionally; 0xFF (Type II form)
	// takes that branch here, matching the masked h-bit cr_write passes.
}

// endTypeII clears Busy/DRQ, sets the given residual status bits, ends any
// transfer, and raises INTRQ.
func (f *FDC) endTypeII(extra byte) {
	f.cmdType1 = false
	f.buf = nil
	f.writing = false
	// Fuse's completions are sticky: they OR in the residual bits and drop
	// Busy/DRQ — they never rewrite the whole byte (wd_fdc.c:545-555 is the
	// Type-I shape; the data paths match it). A wholesale write here erased
	// sticky error bits the failed walk had just recorded.
	f.statusReg |= extra
	f.statusReg &^= statusBusy | statusDRQ
	f.intrq = true
}

// ReadData returns the next byte of a read transfer (Beta port $7F read).
func (f *FDC) ReadData() byte {
	if f.writing || f.buf == nil || f.bufPos >= len(f.buf) {
		return f.dataReg
	}
	f.dataReg = f.buf[f.bufPos]
	f.bufPos++
	if f.bufPos >= len(f.buf) {
		f.endTypeII(0) // transfer complete
	} else {
		f.statusReg = statusBusy | statusDRQ
	}
	return f.dataReg
}

// WriteData feeds the next byte of a write transfer (Beta port $7F write), or
// just latches the data register when no write is in flight (e.g. before a Seek).
func (f *FDC) WriteData(v byte) {
	f.dataReg = v
	if !f.writing || f.buf == nil || f.bufPos >= len(f.buf) {
		return
	}
	f.buf[f.bufPos] = v
	f.bufPos++
	if f.bufPos >= len(f.buf) {
		f.commitWrite()
		f.endTypeII(0)
	} else {
		f.statusReg = statusBusy | statusDRQ
	}
}
