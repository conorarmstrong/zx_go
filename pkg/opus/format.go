package opus

// WRITE TRACK — formatting.
//
// The controller is handed a raw track image a byte at a time and has to pick
// the sectors out of it. That stream is not a convenience format invented for
// the emulator: it is what would physically be written to the medium, gaps and
// sync and address marks included, and the Opus ROM builds it from the
// run-length table at $1BDB (TAB_18_01) with the track, side, sector and size
// substituted into the $F0-$F4 placeholders.
//
// A .opd image stores only sector data — no ID fields, no gaps — so what this
// does is recover (sector, data) pairs from the stream and drop the rest.

// TrackRawBytes is how many bytes pass under the head in one revolution, and
// so how long a WRITE TRACK lasts. A double-density track at 250 kbit/s and
// 300 rpm holds about this many. It matters because WRITE TRACK has no length
// in the command: it runs from one index pulse to the next, and the ROM's
// WAIT_I/O loop has nothing to wait for but BUSY clearing on its own.
//
// The ROM feeds rather more than this — 11 + 18*340 = 6131 bytes of gap, IDs
// and sectors, then a trailing gap of 1024 — so every sector is safely down
// before the index pulse cuts the tail short, exactly as on real hardware.
const TrackRawBytes = 6250

// Field marks in the stream.
const (
	markIDAddress   = 0xFE // ID address mark: track, side, sector, size follow
	markData        = 0xFB // data address mark: the sector's bytes follow
	markDeletedData = 0xF8 // deleted-data mark, treated the same here
	markSync        = 0xA1 // the sync byte a mark must follow to count
	markSyncWrite   = 0xF5 // what the CPU writes to produce an A1 with a missing clock
)

// Format parser state.
const (
	fieldNone = iota
	fieldID
	fieldData
)

// startFormat begins a WRITE TRACK.
func (f *fdc) startFormat() {
	d := f.disk()
	if d == nil {
		f.status = StatusRecordNotFound
		return
	}
	if f.drive >= 0 && f.drive < len(f.wprot) && f.wprot[f.drive] {
		f.status = StatusWriteProtect
		return
	}
	f.formatting = true
	f.fmtPos, f.fmtSync, f.fmtField = 0, 0, fieldNone
	f.fmtID = f.fmtID[:0]
	f.fmtData = f.fmtData[:0]
	f.fmtHaveID = false
	f.status = StatusMotorOn | StatusBusy
	f.scheduleDRQ()
}

// formatByte consumes one byte of the raw track stream. It reports whether the
// track is complete, which happens at the next index pulse.
func (f *fdc) formatByte(v byte) bool {
	f.fmtPos++

	switch f.fmtField {
	case fieldID:
		f.fmtID = append(f.fmtID, v)
		if len(f.fmtID) == 4 {
			f.fmtHaveID = true
			f.fmtField = fieldNone
		}
		return f.fmtPos >= TrackRawBytes
	case fieldData:
		f.fmtData = append(f.fmtData, v)
		if len(f.fmtData) >= f.fmtNeed {
			f.commitFormatSector()
			f.fmtField = fieldNone
		}
		return f.fmtPos >= TrackRawBytes
	}

	// Scanning for the next address mark. A mark only counts when it follows
	// the sync run, so the same byte value inside a data field is just data.
	switch v {
	case markSync, markSyncWrite:
		f.fmtSync++
	case markIDAddress:
		if f.fmtSync >= 3 {
			f.fmtField = fieldID
			f.fmtID = f.fmtID[:0]
			f.fmtHaveID = false
		}
		f.fmtSync = 0
	case markData, markDeletedData:
		if f.fmtSync >= 3 && f.fmtHaveID {
			f.fmtField = fieldData
			f.fmtData = f.fmtData[:0]
			f.fmtNeed = 128 << (f.fmtID[3] & 3)
		}
		f.fmtSync = 0
	default:
		f.fmtSync = 0
	}
	return f.fmtPos >= TrackRawBytes
}

// commitFormatSector stores a completed sector into the image.
//
// The physical position is where the head actually is, not what the ID field
// claims: a flat image has nowhere to record an ID whose track disagrees with
// the head, so honouring it would silently write to the wrong place. Sectors
// outside the image's geometry are dropped for the same reason — the format is
// describing a disk this file cannot represent.
func (f *fdc) commitFormatSector() {
	f.fmtHaveID = false
	d := f.disk()
	if d == nil || f.fmtNeed != SectorSize {
		return
	}
	sector := int(f.fmtID[2])
	_ = d.WriteSector(int(f.track), sector, f.fmtData)
}

// endFormat completes a WRITE TRACK at the index pulse.
func (f *fdc) endFormat() {
	f.formatting = false
	f.fmtField, f.fmtSync = fieldNone, 0
	f.fmtHaveID = false
	f.status = StatusMotorOn
}
