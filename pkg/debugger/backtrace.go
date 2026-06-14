package debugger

import "fmt"

// FrameOrigin classifies how a stack word was likely deposited
// onto the stack. It's purely heuristic — we look at the bytes
// immediately preceding the address the stack word points to and
// match them against CALL / RST opcodes. The classes:
//
//   - OriginCall:        bytes at (return_addr - 3) decode as
//     `CALL nn` ($CD lo hi). This is the
//     normal "real" return address.
//   - OriginCallCond:    bytes at (return_addr - 3) decode as a
//     conditional CALL ($C4/$CC/$D4/$DC/
//     $E4/$EC/$F4/$FC lo hi).
//   - OriginRst:         byte at (return_addr - 1) is a RST
//     opcode ($C7/$CF/$D7/$DF/$E7/$EF/$F7/$FF).
//   - OriginSpeculative: no clear predecessor — could be data, a
//     PUSHed scratch value, or uninitialised
//     RAM. Useful as a "don't trust this"
//     marker.
//
// Backtrace returns a slice of Frames with one entry per stack
// word inspected; the caller decides how to render them (text
// for the telnet debugger, a Fyne widget list for the visual
// debugger, etc.).
type FrameOrigin int

const (
	OriginSpeculative FrameOrigin = iota
	OriginCall
	OriginCallCond
	OriginRst
)

func (o FrameOrigin) String() string {
	switch o {
	case OriginCall:
		return "from CALL nn"
	case OriginCallCond:
		return "from CALL cc"
	case OriginRst:
		return "from RST"
	default:
		return "speculative"
	}
}

// Frame is one entry in a Backtrace result. StackAddr is where the
// 16-bit word was read from (SP + 2*i for the i'th entry).
// ReturnAddr is the word itself (interpreted as a little-endian
// 16-bit address). Origin is the heuristic classification.
// CallSite is meaningful only when Origin is OriginCall /
// OriginCallCond / OriginRst — it's the PC of the opcode that
// (allegedly) pushed ReturnAddr onto the stack. Disasm is up to
// `disasmAt` instructions decoded at ReturnAddr, included only for
// non-speculative frames (so the caller can show what code each
// real frame would resume at).
type Frame struct {
	StackAddr  uint16
	ReturnAddr uint16
	Origin     FrameOrigin
	CallSite   uint16
	CallTarget uint16 // immediate operand of CALL (or RST vector); meaningless for OriginSpeculative
	Disasm     []DisassembledLine
}

// Backtrace walks `n` stack words starting at sp (toward higher
// addresses, matching the Z80's stack-grows-downward convention)
// and classifies each via the FrameOrigin heuristic. Up to
// `disasmAt` instructions are decoded at each non-speculative
// frame's ReturnAddr.
//
// Both the telnet debugger's `backtrace` command and the visual
// debugger's call-frame panel use this — they only differ in how
// they format the resulting []Frame for their respective UI.
func Backtrace(read readFunc, sp uint16, n int, disasmAt int) []Frame {
	if n <= 0 {
		return nil
	}
	out := make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		stackAddr := sp + uint16(i*2)
		lo := uint16(read(stackAddr))
		hi := uint16(read(stackAddr + 1))
		ret := lo | (hi << 8)
		frame := Frame{
			StackAddr:  stackAddr,
			ReturnAddr: ret,
			Origin:     OriginSpeculative,
		}
		// Classify by inspecting the bytes that would precede a
		// genuine return address. Order matters: CALL nn (3-byte) is
		// the most specific match, so test it first.
		if ret >= 3 {
			if read(ret-3) == 0xCD {
				frame.Origin = OriginCall
				frame.CallSite = ret - 3
				frame.CallTarget = uint16(read(ret-2)) | uint16(read(ret-1))<<8
			} else {
				switch read(ret - 3) {
				case 0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC:
					frame.Origin = OriginCallCond
					frame.CallSite = ret - 3
					frame.CallTarget = uint16(read(ret-2)) | uint16(read(ret-1))<<8
				}
			}
		}
		if frame.Origin == OriginSpeculative && ret >= 1 {
			if rst := read(ret - 1); rst&0xC7 == 0xC7 {
				frame.Origin = OriginRst
				frame.CallSite = ret - 1
				frame.CallTarget = uint16(rst & 0x38)
			}
		}
		if frame.Origin != OriginSpeculative && disasmAt > 0 {
			frame.Disasm = Disassemble(read, ret, disasmAt)
		}
		out = append(out, frame)
	}
	return out
}

// Hint returns a short human-readable description of where this
// frame came from. Empty for speculative frames.
func (f Frame) Hint() string {
	switch f.Origin {
	case OriginCall, OriginCallCond:
		return fmt.Sprintf("CALL $%04X @ $%04X", f.CallTarget, f.CallSite)
	case OriginRst:
		return fmt.Sprintf("RST $%02X @ $%04X", f.CallTarget, f.CallSite)
	default:
		return ""
	}
}
