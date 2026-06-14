// Package uart models the Spectrum Next's ESP32 UART at NextRegs
// 0xA8 (data) / 0xA9 (status, TX-ready + RX-available), wired by
// pkg/next.WireUART. The feasible scope is implemented: TX/RX FIFOs and
// an AT-command responder (AT / AT+GMR / AT+CIPSEND / generic OK) so
// software that probes for an ESP and runs its handshake gets sensible
// replies and never hangs.
//
// Real ESP Wi-Fi networking — opening TCP/IP sockets to actual servers
// via AT+CIPSTART etc. — is deliberately OUT OF SCOPE: a live network
// stack is beyond a reference emulator and carries obvious safety
// concerns. AT connection commands are accepted (OK) but do not connect.
package uart
