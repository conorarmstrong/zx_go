package rtc

import (
	"testing"
	"time"
)

func TestReadReturnsHostTimeInBCD(t *testing.T) {
	r := New()
	// 2025-03-14 (Pi Day) 15:09:26 — Friday.
	r.SetNow(func() time.Time {
		return time.Date(2025, 3, 14, 15, 9, 26, 0, time.UTC)
	})

	if got := r.Read(RegSeconds); got != 0x26 {
		t.Errorf("seconds = %#x, want 0x26 (BCD for 26)", got)
	}
	if got := r.Read(RegMinutes); got != 0x09 {
		t.Errorf("minutes = %#x, want 0x09", got)
	}
	if got := r.Read(RegHours); got != 0x15 {
		t.Errorf("hours = %#x, want 0x15", got)
	}
	if got := r.Read(RegDayOfMonth); got != 0x14 {
		t.Errorf("day = %#x, want 0x14", got)
	}
	if got := r.Read(RegMonth); got != 0x03 {
		t.Errorf("month = %#x, want 0x03", got)
	}
	if got := r.Read(RegYear); got != 0x25 {
		t.Errorf("year = %#x, want 0x25 (two-digit, BCD)", got)
	}
	if got := r.Read(RegDayOfWeek); got != 6 { // Friday = 6 in DS1307 (Sun=1)
		t.Errorf("day-of-week = %d, want 6 (Friday)", got)
	}
}

func TestReadControlIsZero(t *testing.T) {
	r := New()
	if got := r.Read(RegControl); got != 0 {
		t.Errorf("control register = %#x, want 0", got)
	}
}

func TestReadOutOfRangeIsZero(t *testing.T) {
	r := New()
	if got := r.Read(0x10); got != 0 {
		t.Errorf("out-of-range read = %#x, want 0", got)
	}
}

func TestWriteIsAccepted(t *testing.T) {
	// Clock-register writes are dropped silently; confirms no panic.
	r := New()
	r.Write(RegSeconds, 0xFF)
	r.Write(0x99, 0xFF)
}

func TestBCDEdgeCases(t *testing.T) {
	cases := []struct {
		in   int
		want byte
	}{
		{0, 0x00},
		{9, 0x09},
		{10, 0x10},
		{59, 0x59},
		{99, 0x99},
		{100, 0x99}, // clamps
		{-1, 0x00},  // clamps
	}
	for _, c := range cases {
		if got := bcd(c.in); got != c.want {
			t.Errorf("bcd(%d) = %#x, want %#x", c.in, got, c.want)
		}
	}
}

// =====================================================================
// Additional RTC tests (iter 218).
// =====================================================================

// TestAllDaysOfWeek covers each weekday → DS1307 day-of-week mapping
// (Sunday = 1, Monday = 2, ..., Saturday = 7).
func TestAllDaysOfWeek(t *testing.T) {
	r := New()
	// Sunday 2026-05-24 — Sunday=1 in DS1307.
	cases := []struct {
		date string
		want byte
	}{
		{"2026-05-24", 1}, // Sunday
		{"2026-05-25", 2}, // Monday
		{"2026-05-26", 3}, // Tuesday
		{"2026-05-27", 4}, // Wednesday (today per project memory)
		{"2026-05-28", 5}, // Thursday
		{"2026-05-29", 6}, // Friday
		{"2026-05-30", 7}, // Saturday
	}
	for _, c := range cases {
		ti, err := time.Parse("2006-01-02", c.date)
		if err != nil {
			t.Fatalf("parse %s: %v", c.date, err)
		}
		r.SetNow(func() time.Time { return ti })
		if got := r.Read(RegDayOfWeek); got != c.want {
			t.Errorf("date %s: DOW = %d, want %d", c.date, got, c.want)
		}
	}
}

// TestYearTwoDigit verifies year stores only the last two digits (BCD).
// 2099 → $99; 2100 → $00; 2000 → $00.
func TestYearTwoDigit(t *testing.T) {
	r := New()
	for _, c := range []struct {
		year int
		want byte
	}{
		{2025, 0x25},
		{2000, 0x00},
		{2099, 0x99},
		{2100, 0x00}, // wraps
		{2050, 0x50},
	} {
		ti := time.Date(c.year, 1, 1, 0, 0, 0, 0, time.UTC)
		r.SetNow(func() time.Time { return ti })
		if got := r.Read(RegYear); got != c.want {
			t.Errorf("year %d: $%02X, want $%02X", c.year, got, c.want)
		}
	}
}

// TestReadingAllZeroDateTime verifies the minimum-value path doesn't
// produce garbage (e.g. negative seconds when t.Second() somehow goes
// wrong — defensive guard against bcd(-1)).
func TestReadingAllZeroDateTime(t *testing.T) {
	r := New()
	r.SetNow(func() time.Time {
		return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	for _, reg := range []byte{RegSeconds, RegMinutes, RegHours} {
		if got := r.Read(reg); got != 0 {
			t.Errorf("reg $%02X at zero time: got $%02X, want 0", reg, got)
		}
	}
	if got := r.Read(RegDayOfMonth); got != 0x01 {
		t.Errorf("day-of-month for Jan 1: $%02X, want $01", got)
	}
	if got := r.Read(RegMonth); got != 0x01 {
		t.Errorf("month for January: $%02X, want $01", got)
	}
}

// TestHoursIs24HourFormat — RTC presents 24-hour BCD with bit 6 = 0.
// Confirms 13:00 → $13 (not 1pm flag).
func TestHoursIs24HourFormat(t *testing.T) {
	r := New()
	r.SetNow(func() time.Time {
		return time.Date(2025, 3, 14, 13, 0, 0, 0, time.UTC)
	})
	got := r.Read(RegHours)
	if got != 0x13 {
		t.Errorf("hours 13:00 = $%02X, want $13 (24-hour BCD)", got)
	}
	if got&0x40 != 0 {
		t.Error("hours bit 6 (12/24 mode flag) is set — should be 0 (24-hour)")
	}
}
