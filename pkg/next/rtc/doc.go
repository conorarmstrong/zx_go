// Package rtc implements a minimal i2c-style real-time clock backed by
// the host system clock. NextReg 0x10 (SCL) and 0x11 (SDA) drive the
// i2c lines; software that polls for time-of-day or date gets a valid
// answer. Sprint 8 brings up the stub; no battery state is persisted.
package rtc
