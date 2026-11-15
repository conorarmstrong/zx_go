// Package uart models the Spectrum Next's two UARTs and the module behind the
// first.
//
// Block is serial/uart.vhd: the register block at ports $133B (Tx and status),
// $143B (Rx and prescaler), $153B (select) and $163B (frame), the 512-byte Rx
// and 64-byte Tx FIFO of each UART, and the status and error logic. It is
// clocked one 28 MHz i_CLK at a time and replays a golden captured from the
// real uart.vhd under GHDL (testdata/uartblock_golden.txt, tooling in
// _tools/uartblock-vhdl-test), including every output change and every
// register read. Advance skips the clocks of a quiescent Block, and the same
// golden is replayed through it.
//
// TX, RX and FIFOP (serial.go) are the serial PHY and the FIFO pointer manager
// Block is built from: uart_tx.vhd, uart_rx.vhd and fifop.vhd, each with its
// own golden.
//
// ESP is the module on UART 0's lines: a 115200 baud 8N1 receiver and
// transmitter on the same PHY, answering through UART, the AT-command
// responder (uart.go). Real Wi-Fi networking is deliberately OUT OF SCOPE: a
// live network stack is beyond a reference emulator and carries obvious safety
// concerns. AT connection commands are accepted (OK) but do not connect.
// Nothing is attached to UART 1, which has the Raspberry Pi behind it on the
// real board.
//
// Not modelled: the UARTs routed to the joystick ports (NR$0B I/O mode,
// zxnext.vhd:3340-3347), which repurposes joystick pins as a serial link to an
// external device.
package uart
