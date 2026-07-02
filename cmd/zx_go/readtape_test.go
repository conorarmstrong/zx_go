package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestReadTapeArmsAtHitAndCaps pins the recorder's observable contract:
// reads before the arming checkpoint (n-th execution of startPC) are
// dropped; once armed, each RAM read is one tape line with a
// monotonically increasing sequence number; recording stops at the
// line cap. The line format is the cross-emulator interchange the
// tapediff compares, so it is asserted exactly.
func TestReadTapeArmsAtHitAndCaps(t *testing.T) {
	var buf bytes.Buffer
	rt := newReadTape(&buf, 0x3E00, 2, 3) // arm at the 2nd $3E00, cap 3 lines

	rt.onRead(5, 0x0100, 0xAA) // pre-arm: dropped
	rt.onExecute(0x3E00)       // hit #1 (not yet armed)
	rt.onRead(5, 0x0101, 0xBB) // still pre-arm: dropped
	rt.onExecute(0x1234)
	rt.onExecute(0x3E00) // hit #2 -> armed
	rt.onExecute(0x27AA)
	rt.onRead(0, 0x1F4F, 0xC7)  // seq 0
	rt.onRead(0, 0x1F50, 0x00)  // seq 1
	rt.onRead(11, 0x0000, 0x18) // seq 2 -> reaches cap -> done
	rt.onRead(1, 0x2222, 0x99)  // post-cap: dropped
	rt.flush()

	want := "0 R b0:1f4f =c7 @27aa\n" +
		"1 R b0:1f50 =00 @27aa\n" +
		"2 R b11:0000 =18 @27aa\n"
	if got := buf.String(); got != want {
		t.Fatalf("tape mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestParseReadTapeFrom(t *testing.T) {
	cases := []struct {
		in      string
		wantPC  uint16
		wantHit int
	}{
		{"", 0, 0},
		{"$3E00", 0x3E00, 1},
		{"$3E00:2", 0x3E00, 2},
		{"0x27AA:5", 0x27AA, 5},
		{"garbage", 0, 0},
	}
	for _, c := range cases {
		pc, hit := parseReadTapeFrom(c.in)
		if pc != c.wantPC || hit != c.wantHit {
			t.Errorf("parseReadTapeFrom(%q) = ($%04X,%d), want ($%04X,%d)", c.in, pc, hit, c.wantPC, c.wantHit)
		}
	}
}

// TestReadTapeImmediateArmWhenNoTrigger verifies that with no start
// trigger (startPC 0, hit 0) the tape records from the first read.
func TestReadTapeImmediateArmWhenNoTrigger(t *testing.T) {
	var buf bytes.Buffer
	rt := newReadTape(&buf, 0, 0, 0) // no trigger, no cap
	rt.onExecute(0x8000)
	rt.onRead(2, 0x0010, 0x42)
	rt.flush()
	if got := buf.String(); !strings.HasPrefix(got, "0 R b2:0010 =42 @8000\n") {
		t.Fatalf("expected immediate recording, got %q", got)
	}
}

// TestReadTapePortWriteRecords — the tape logs guest OUT instructions
// as `SEQ W pPPPP =VV @PC` records. Port-level visibility is what
// disambiguates multi-occupant RAM code in oracle diffs: the value
// stream alone cannot prove WHICH register/port a staging write hit.
func TestReadTapePortWriteRecords(t *testing.T) {
	var buf bytes.Buffer
	rt := newReadTape(&buf, 0, 0, 10) // no trigger = armed immediately
	rt.onExecute(0x6D2F)
	rt.onPortWrite(0x253B, 0x81)
	rt.flush()
	got := buf.String()
	want := "0 W p253b =81 @6d2f\n"
	if got != want {
		t.Errorf("port record = %q, want %q", got, want)
	}
}

// TestReadTapePortWriteRespectsArmAndCap — port records obey the same
// arming and max-record lifecycle as RAM-read records.
func TestReadTapePortWriteRespectsArmAndCap(t *testing.T) {
	var buf bytes.Buffer
	rt := newReadTape(&buf, 0x1234, 1, 2)
	rt.onPortWrite(0x253B, 0x01) // not armed yet → dropped
	rt.onExecute(0x1234)         // arms
	rt.onPortWrite(0x243B, 0x02)
	rt.onPortWrite(0x253B, 0x81)
	rt.onPortWrite(0x253B, 0xFF) // over cap → dropped
	rt.flush()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2: %q", len(lines), buf.String())
	}
}
