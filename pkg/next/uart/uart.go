// Package uart is the stub for the Spectrum Next's ESP32 UART.
// NextReg 0xA8 carries the data byte; NextReg 0xA9 carries the
// status (TX-ready / RX-available bits).
//
// Real Wi-Fi networking is explicitly out of scope. The stub
// responds to "AT\r" with "OK\r\n"
// so probing software detects "an ESP is there", and discards
// everything else. RX buffer is whatever the canned response
// happens to be; TX is dropped.
package uart

import "strings"

// Status bit constants (NextReg 0xA9 reads).
const (
	StatusTXReady = 0x02 // 1 = the TX FIFO has space
	StatusRXReady = 0x01 // 1 = a byte is waiting in RX
)

// UART models the ESP UART surface.
type UART struct {
	rxBuf   []byte // bytes the guest will see on subsequent reads
	txBuf   []byte // accumulated outgoing line; flushed on \r
	version string // AT+GMR identity payload
}

// DefaultVersion is what AT+GMR returns when SetVersion has not
// been called. Callers (e.g. the emulator binary) override this
// with their build identity.
const DefaultVersion = "zx_go-uart"

// New returns an empty UART. Initial state: TX FIFO has space,
// RX FIFO is empty, AT+GMR returns DefaultVersion.
func New() *UART { return &UART{version: DefaultVersion} }

// SetVersion replaces the AT+GMR identity string. Useful for
// keeping the stub's reported version in sync with the host
// build without hard-coding it in this package.
func (u *UART) SetVersion(v string) { u.version = v }

// Status returns the NextReg 0xA9 status byte: TX-ready always
// set (we accept any byte), RX-ready set when rxBuf has pending
// bytes.
func (u *UART) Status() byte {
	v := byte(StatusTXReady)
	if len(u.rxBuf) > 0 {
		v |= StatusRXReady
	}
	return v
}

// ReadData returns the next byte from the RX FIFO and removes it,
// or 0 if the buffer is empty.
func (u *UART) ReadData() byte {
	if len(u.rxBuf) == 0 {
		return 0
	}
	b := u.rxBuf[0]
	u.rxBuf = u.rxBuf[1:]
	return b
}

// WriteData appends a byte to the outgoing TX buffer. When a CR
// (0x0D) lands the accumulated line is parsed and a canned
// response is queued into the RX FIFO:
//
//   - "AT"             -> "OK\r\n"   (probe / liveness)
//   - any "AT+..."     -> "OK\r\n"   (config commands accepted
//     silently — no real Wi-Fi state is modelled)
//   - "AT+GMR"         -> identity + "OK\r\n"
//   - "AT+CIPSEND"     -> ">\r\n"    (prompt for the bytes payload)
//   - anything else    -> "ERROR\r\n"
//
// This is enough for software that probes whether an ESP UART is
// present to get past its handshake. Real Wi-Fi is out of scope.
func (u *UART) WriteData(b byte) {
	if b == '\r' {
		cmd := strings.TrimSpace(strings.ToUpper(string(u.txBuf)))
		u.txBuf = u.txBuf[:0]
		u.respondTo(cmd)
		return
	}
	u.txBuf = append(u.txBuf, b)
}

func (u *UART) respondTo(cmd string) {
	switch {
	case cmd == "":
		// Bare CR is no-op.
	case cmd == "AT":
		u.rxBuf = append(u.rxBuf, []byte("OK\r\n")...)
	case cmd == "AT+GMR":
		u.rxBuf = append(u.rxBuf, []byte(u.version+"\r\nOK\r\n")...)
	case strings.HasPrefix(cmd, "AT+CIPSEND"):
		u.rxBuf = append(u.rxBuf, []byte(">\r\n")...)
	case strings.HasPrefix(cmd, "AT+") || strings.HasPrefix(cmd, "AT"):
		u.rxBuf = append(u.rxBuf, []byte("OK\r\n")...)
	default:
		u.rxBuf = append(u.rxBuf, []byte("ERROR\r\n")...)
	}
}
