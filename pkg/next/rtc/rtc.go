// Package rtc implements a minimal i2c-style real-time clock for
// the Spectrum Next. NextReg 0x10 / 0x11 carry the SCL / SDA i2c
// lines that real hardware uses to talk to a DS1307-class RTC chip
// at i2c address 0x68.
//
// The RTC's clock registers (0x00-0x07) always reflect the host date /
// time, formatted to match the DS1307 register set; guest software
// using the standard NextZXOS RTC routines sees plausible values. The
// DS1307's 56-byte battery-backed NVRAM (registers 0x08-0x3F) is
// modelled and persisted across runs via SetPersistPath.
package rtc

import (
	"os"
	"time"
)

// DS1307 register addresses (bytes the RTC chip would normally
// hold at i2c offset 0..7). Reads return values derived from
// time.Now(); date / time writes are accepted but ignored.
const (
	RegSeconds    byte = 0x00
	RegMinutes    byte = 0x01
	RegHours      byte = 0x02
	RegDayOfWeek  byte = 0x03
	RegDayOfMonth byte = 0x04
	RegMonth      byte = 0x05
	RegYear       byte = 0x06
	RegControl    byte = 0x07
)

// RTC reads the host system clock and presents it through the
// DS1307 register format the standard NextZXOS RTC API expects.
type RTC struct {
	// now is overridable for tests; production code leaves it nil
	// and the package uses time.Now().
	now func() time.Time
	// ram is the DS1307's battery-backed general-purpose RAM
	// (registers 0x08..0x3F). Indices 0x00..0x07 are unused here
	// (the clock registers are derived from the host time).
	ram [0x40]byte
	// persistPath, when set, backs the RAM on disk: it is loaded by
	// SetPersistPath and rewritten on every RAM write (battery-backed).
	persistPath string
}

// New returns an RTC backed by the host system clock.
func New() *RTC { return &RTC{} }

// SetNow installs a time provider — useful for tests that need a
// deterministic clock. Pass nil to revert to time.Now().
func (r *RTC) SetNow(fn func() time.Time) { r.now = fn }

func (r *RTC) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// bcd converts an integer 0..99 to its BCD representation.
func bcd(v int) byte {
	if v < 0 {
		v = 0
	}
	if v > 99 {
		v = 99
	}
	return byte((v/10)<<4 | (v % 10))
}

// Read returns the DS1307-style byte at register address reg: 0x00-0x07
// are the live clock registers, 0x08-0x3F are the battery-backed NVRAM,
// and anything else returns 0.
func (r *RTC) Read(reg byte) byte {
	if traceReads {
		traceRead(reg)
	}
	t := r.currentTime()
	switch reg {
	case RegSeconds:
		return bcd(t.Second())
	case RegMinutes:
		return bcd(t.Minute())
	case RegHours:
		// 24-hour mode (bit 6 = 0).
		return bcd(t.Hour())
	case RegDayOfWeek:
		// DS1307 stores 1..7 with Sunday=1.
		return byte(int(t.Weekday()) + 1)
	case RegDayOfMonth:
		return bcd(t.Day())
	case RegMonth:
		return bcd(int(t.Month()))
	case RegYear:
		// Two-digit year (00..99); software adds 2000.
		return bcd(t.Year() % 100)
	case RegControl:
		return 0 // 1 Hz output disabled
	}
	// Registers 0x08..0x3F are the battery-backed NVRAM.
	if reg >= 0x08 && reg < 0x40 {
		return r.ram[reg]
	}
	return 0
}

// Write stores a value to the battery-backed NVRAM (registers
// 0x08..0x3F) and persists it if a path is configured. Writes to the
// clock registers (0x00..0x07) are discarded — the RTC reflects host
// time, by design.
func (r *RTC) Write(reg, val byte) {
	if reg >= 0x08 && reg < 0x40 {
		r.ram[reg] = val
		r.save()
	}
}

// SetPersistPath enables battery-backed persistence of the NVRAM at the
// given file path, loading any existing contents immediately. Pass ""
// to disable persistence. Tests must use a temp path, never the install
// dir.
func (r *RTC) SetPersistPath(path string) {
	r.persistPath = path
	r.load()
}

// save writes the NVRAM region (0x08..0x3F) to persistPath, if set.
// Errors are ignored — persistence is best-effort.
func (r *RTC) save() {
	if r.persistPath == "" {
		return
	}
	_ = os.WriteFile(r.persistPath, r.ram[0x08:0x40], 0o644)
}

// load reads the NVRAM region from persistPath, if it exists.
func (r *RTC) load() {
	if r.persistPath == "" {
		return
	}
	data, err := os.ReadFile(r.persistPath)
	if err != nil {
		return
	}
	copy(r.ram[0x08:0x40], data)
}
