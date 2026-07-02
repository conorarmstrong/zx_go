package uart

import (
	"strings"
	"testing"
)

func TestStartEmpty(t *testing.T) {
	u := New()
	if got := u.Status(); got != StatusTXReady {
		t.Errorf("Status on fresh UART = %#x, want %#x (TX-ready only)", got, StatusTXReady)
	}
	if u.ReadData() != 0 {
		t.Errorf("ReadData on empty UART should return 0")
	}
}

func TestATReturnsOK(t *testing.T) {
	u := New()
	for _, b := range []byte("AT\r") {
		u.WriteData(b)
	}
	// After AT\r, status should report RX available.
	if u.Status()&StatusRXReady == 0 {
		t.Errorf("Status after AT\\r should have RXReady bit set")
	}
	want := "OK\r\n"
	got := make([]byte, len(want))
	for i := range got {
		got[i] = u.ReadData()
	}
	if string(got) != want {
		t.Errorf("response = %q, want %q", got, want)
	}
	if u.Status()&StatusRXReady != 0 {
		t.Errorf("Status after reading response should clear RXReady")
	}
}

func TestNonATCommandsReturnError(t *testing.T) {
	u := New()
	for _, b := range []byte("HELLO\r") {
		u.WriteData(b)
	}
	if u.Status()&StatusRXReady == 0 {
		t.Errorf("non-AT command should queue ERROR response")
	}
	got := make([]byte, len("ERROR\r\n"))
	for i := range got {
		got[i] = u.ReadData()
	}
	if string(got) != "ERROR\r\n" {
		t.Errorf("got %q, want ERROR\\r\\n", got)
	}
}

func TestATPlusCommandReturnsOK(t *testing.T) {
	u := New()
	for _, b := range []byte("AT+CWMODE=1\r") {
		u.WriteData(b)
	}
	want := "OK\r\n"
	got := make([]byte, len(want))
	for i := range got {
		got[i] = u.ReadData()
	}
	if string(got) != want {
		t.Errorf("AT+CWMODE response = %q, want %q", got, want)
	}
}

func TestATGMRReturnsIdentity(t *testing.T) {
	u := New()
	for _, b := range []byte("AT+GMR\r") {
		u.WriteData(b)
	}
	// Drain the RX FIFO; we expect identity string followed by OK.
	var buf []byte
	for u.Status()&StatusRXReady != 0 {
		buf = append(buf, u.ReadData())
	}
	s := string(buf)
	if !strings.Contains(s, "zx_go") || !strings.Contains(s, "OK") {
		t.Errorf("AT+GMR response = %q, want identity + OK", s)
	}
}

func TestATCIPSENDReturnsPrompt(t *testing.T) {
	u := New()
	for _, b := range []byte("AT+CIPSEND=4\r") {
		u.WriteData(b)
	}
	want := ">\r\n"
	got := make([]byte, len(want))
	for i := range got {
		got[i] = u.ReadData()
	}
	if string(got) != want {
		t.Errorf("AT+CIPSEND response = %q, want %q", got, want)
	}
}

func TestATCaseInsensitive(t *testing.T) {
	u := New()
	for _, b := range []byte("at\r") {
		u.WriteData(b)
	}
	if u.Status()&StatusRXReady == 0 {
		t.Errorf("lowercase at\\r should also queue OK")
	}
}

// TestStatusTXAlwaysReady documents the stub's "TX FIFO has space"
// invariant — we drop everything so there's always room.
func TestStatusTXAlwaysReady(t *testing.T) {
	u := New()
	if u.Status()&StatusTXReady == 0 {
		t.Error("default Status: TX-ready bit not set")
	}
	for i := 0; i < 100; i++ {
		u.WriteData(byte(i))
	}
	if u.Status()&StatusTXReady == 0 {
		t.Error("after 100 writes: TX-ready cleared (stub should always be ready)")
	}
}

// TestATGMRUsesSetVersion verifies SetVersion replaces the identity
// string returned by AT+GMR.
func TestATGMRUsesSetVersion(t *testing.T) {
	u := New()
	u.SetVersion("my-build-1.0")
	for _, b := range []byte("AT+GMR\r") {
		u.WriteData(b)
	}
	var buf []byte
	for u.Status()&StatusRXReady != 0 {
		buf = append(buf, u.ReadData())
	}
	if !strings.Contains(string(buf), "my-build-1.0") {
		t.Errorf("AT+GMR response %q doesn't contain custom version", buf)
	}
}

// TestBareCRIsNoOp — sending "\r" alone shouldn't queue anything.
func TestBareCRIsNoOp(t *testing.T) {
	u := New()
	u.WriteData('\r')
	if u.Status()&StatusRXReady != 0 {
		t.Error("bare CR queued a response — should be no-op")
	}
}

// TestATPlusGenericPrefix verifies arbitrary AT+xxx commands return OK
// even when not specifically matched (the AT+ catch-all branch).
func TestATPlusGenericPrefix(t *testing.T) {
	u := New()
	for _, b := range []byte("AT+CWJAP=\"net\",\"pw\"\r") {
		u.WriteData(b)
	}
	var buf []byte
	for u.Status()&StatusRXReady != 0 {
		buf = append(buf, u.ReadData())
	}
	if !strings.Contains(string(buf), "OK") {
		t.Errorf("AT+CWJAP response = %q, want OK", buf)
	}
}

// TestMultipleCommandsInSequence verifies the TX-buffer resets after
// each \r, so subsequent commands don't merge with prior bytes.
func TestMultipleCommandsInSequence(t *testing.T) {
	u := New()
	for _, b := range []byte("AT\r") {
		u.WriteData(b)
	}
	// Drain.
	for u.Status()&StatusRXReady != 0 {
		u.ReadData()
	}
	// Second command.
	for _, b := range []byte("BAD\r") {
		u.WriteData(b)
	}
	var buf []byte
	for u.Status()&StatusRXReady != 0 {
		buf = append(buf, u.ReadData())
	}
	if !strings.Contains(string(buf), "ERROR") {
		t.Errorf("second command response = %q, want ERROR (TX-buffer state leaked?)", buf)
	}
}
