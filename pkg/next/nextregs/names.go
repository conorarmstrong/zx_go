package nextregs

import (
	"fmt"
	"strings"
)

// The NextReg name and field table for debuggers and traces. Names and field
// layouts follow the core's nextreg.txt; registers it marks reserved have no
// entry.

// field is one named bit range of a register. A one-bit field with no enum is
// a flag, shown by name when set; anything else is shown as name=value, or
// name=enum[value] when an enum is given.
type field struct {
	name   string
	hi, lo uint8
	enum   []string
}

type regInfo struct {
	name   string
	fields []field
}

func flag(name string, bit uint8) field { return field{name: name, hi: bit, lo: bit} }

func bits(name string, hi, lo uint8, enum ...string) field {
	return field{name: name, hi: hi, lo: lo, enum: enum}
}

var speeds = []string{"3.5MHz", "7MHz", "14MHz", "28MHz"}

var regTable = map[byte]regInfo{
	0x00: {"Machine ID", nil},
	0x01: {"Core Version", []field{bits("major", 7, 4), bits("minor", 3, 0)}},
	0x02: {"Reset", []field{flag("bus-reset", 7), flag("iotrap-nmi", 4), flag("mf-nmi", 3), flag("divmmc-nmi", 2), flag("hard", 1), flag("soft", 0)}},
	0x03: {"Machine Type", []field{bits("timing", 6, 4), flag("timing-lock", 3), bits("machine", 2, 0, "config", "48K", "128K", "+3", "Pentagon")}},
	0x04: {"Config Mapping", nil},
	0x05: {"Peripheral 1 Setting", []field{bits("joy1", 7, 6), bits("joy2", 5, 4), flag("joy1-msb", 3), flag("60Hz", 2), flag("joy2-msb", 1), flag("scandoubler", 0)}},
	0x06: {"Peripheral 2 Setting", []field{flag("f8-hotkeys", 7), flag("beep-internal", 6), flag("f3-hotkey", 5), flag("drive-nmi", 4), flag("m1-nmi", 3), flag("ps2-mouse", 2), bits("audio", 1, 0, "YM", "AY", "ZXN-8950", "hold")}},
	0x07: {"CPU Speed", []field{bits("actual", 5, 4, speeds...), bits("programmed", 1, 0, speeds...)}},
	0x08: {"Peripheral 3 Setting", []field{flag("7ffd-unlocked", 7), flag("no-contention", 6), flag("acb-stereo", 5), flag("speaker", 4), flag("dacs", 3), flag("ff-read", 2), flag("turbosound", 1), flag("issue2", 0)}},
	0x09: {"Peripheral 4 Setting", []field{flag("ay2-mono", 7), flag("ay1-mono", 6), flag("ay0-mono", 5), flag("sprite-lockstep", 4), flag("hdmi-mute", 2), bits("scanlines", 1, 0)}},
	0x0A: {"Peripheral 5 Setting", []field{bits("mf-type", 7, 6), flag("sd-swap", 5), flag("automap", 4), flag("mouse-swap", 3), flag("sd-lock", 2), bits("dpi", 1, 0)}},
	0x0B: {"Joystick I/O Mode", []field{flag("io-mode", 7), bits("mode", 5, 4, "bit-bang", "clock", "uart", "uart"), flag("uart1", 0)}},
	0x0E: {"Core Version Sub-minor", nil},
	0x0F: {"Board ID", []field{bits("board", 3, 0)}},
	0x10: {"Core Boot", []field{bits("core", 6, 2), flag("drive-btn", 1), flag("m1-btn", 0)}},
	0x11: {"Video Timing", []field{bits("mode", 2, 0)}},
	0x12: {"Layer 2 Active RAM Bank", []field{bits("bank", 6, 0)}},
	0x13: {"Layer 2 Shadow RAM Bank", []field{bits("bank", 6, 0)}},
	0x14: {"Global Transparency Colour", nil},
	0x15: {"Sprite and Layers System", []field{flag("lores", 7), flag("sprite0-top", 6), flag("border-clip", 5), bits("layers", 4, 2, "SLU", "LSU", "SUL", "LUS", "USL", "ULS", "blend", "blend-5"), flag("over-border", 1), flag("sprites", 0)}},
	0x16: {"Layer 2 X Scroll LSB", nil},
	0x17: {"Layer 2 Y Scroll", nil},
	0x18: {"Clip Window Layer 2", nil},
	0x19: {"Clip Window Sprites", nil},
	0x1A: {"Clip Window ULA/LoRes", nil},
	0x1B: {"Clip Window Tilemap", nil},
	0x1C: {"Clip Window Control", []field{bits("tm", 7, 6), bits("ula", 5, 4), bits("spr", 3, 2), bits("l2", 1, 0)}},
	0x1E: {"Active Video Line MSB", nil},
	0x1F: {"Active Video Line LSB", nil},
	0x20: {"Generate Maskable Interrupt", []field{flag("line", 7), flag("ula", 6), bits("ctc", 3, 0)}},
	0x22: {"Line Interrupt Control", []field{flag("ula-int", 7), flag("ula-off", 2), flag("line-on", 1), flag("line-msb", 0)}},
	0x23: {"Line Interrupt Value LSB", nil},
	0x26: {"ULA X Scroll", nil},
	0x27: {"ULA Y Scroll", nil},
	0x28: {"PS/2 Keymap Address MSB", nil},
	0x29: {"PS/2 Keymap Address LSB", nil},
	0x2A: {"PS/2 Keymap Data MSB", nil},
	0x2B: {"PS/2 Keymap Data LSB", nil},
	0x2C: {"DAC B Mirror", nil},
	0x2D: {"DAC A+D Mirror", nil},
	0x2E: {"DAC C Mirror", nil},
	0x2F: {"Tilemap X Scroll MSB", nil},
	0x30: {"Tilemap X Scroll LSB", nil},
	0x31: {"Tilemap Offset Y", nil},
	0x32: {"LoRes X Scroll", nil},
	0x33: {"LoRes Y Scroll", nil},
	0x34: {"Sprite Number", nil},
	0x35: {"Sprite Attribute 0", nil},
	0x36: {"Sprite Attribute 1", nil},
	0x37: {"Sprite Attribute 2", nil},
	0x38: {"Sprite Attribute 3", nil},
	0x39: {"Sprite Attribute 4", nil},
	0x40: {"Palette Index", nil},
	0x41: {"Palette Value 8-bit", nil},
	0x42: {"ULANext Attribute Format", nil},
	0x43: {"Palette Control", []field{flag("no-autoinc", 7), bits("select", 6, 4, "ULA1", "L2-1", "SPR1", "TM1", "ULA2", "L2-2", "SPR2", "TM2"), flag("spr2", 3), flag("l2-2", 2), flag("ula2", 1), flag("ulanext", 0)}},
	0x44: {"Palette Value 9-bit", nil},
	0x4A: {"Fallback Colour", nil},
	0x4B: {"Sprite Transparency Index", nil},
	0x4C: {"Tilemap Transparency Index", nil},
	0x50: {"MMU slot 0", nil},
	0x51: {"MMU slot 1", nil},
	0x52: {"MMU slot 2", nil},
	0x53: {"MMU slot 3", nil},
	0x54: {"MMU slot 4", nil},
	0x55: {"MMU slot 5", nil},
	0x56: {"MMU slot 6", nil},
	0x57: {"MMU slot 7", nil},
	0x60: {"Copper Data 8-bit", nil},
	0x61: {"Copper Address LSB", nil},
	0x62: {"Copper Control", []field{bits("mode", 7, 6, "stop", "reset", "start", "frame"), bits("addr-msb", 2, 0)}},
	0x63: {"Copper Data 16-bit", nil},
	0x64: {"Vertical Line Count Offset", nil},
	0x68: {"ULA Control", []field{flag("ula-off", 7), bits("blend", 6, 5), flag("ext-keys-cancel", 4), flag("ula+", 3), flag("half-scroll", 2), flag("stencil", 0)}},
	0x69: {"Display Control 1", []field{flag("layer2", 7), flag("shadow", 6), bits("timex", 5, 0)}},
	0x6A: {"LoRes Control", []field{flag("radastan", 5), flag("radastan-xor", 4), bits("pal-offset", 3, 0)}},
	0x6B: {"Tilemap Control", []field{flag("on", 7), flag("80col", 6), flag("no-attr", 5), flag("pal2", 4), flag("text", 3), flag("512tiles", 1), flag("on-top", 0)}},
	0x6C: {"Default Tilemap Attribute", []field{bits("pal-offset", 7, 4), flag("xmirror", 3), flag("ymirror", 2), flag("rotate", 1), flag("ula-over", 0)}},
	0x6E: {"Tilemap Base Address", []field{flag("bank7", 7), bits("msb", 5, 0)}},
	0x6F: {"Tile Definitions Base Address", []field{flag("bank7", 7), bits("msb", 5, 0)}},
	0x70: {"Layer 2 Control", []field{bits("res", 5, 4, "256x192", "320x256", "640x256", "res3"), bits("pal-offset", 3, 0)}},
	0x71: {"Layer 2 X Scroll MSB", nil},
	0x75: {"Sprite Attribute 0 (inc)", nil},
	0x76: {"Sprite Attribute 1 (inc)", nil},
	0x77: {"Sprite Attribute 2 (inc)", nil},
	0x78: {"Sprite Attribute 3 (inc)", nil},
	0x79: {"Sprite Attribute 4 (inc)", nil},
	0x7F: {"User Register 0", nil},
	0x80: {"Expansion Bus Enable", []field{flag("on", 7), flag("romcs", 6), flag("no-io", 5), flag("no-mem", 4)}},
	0x81: {"Expansion Bus Control", nil},
	0x82: {"Internal Port Enables 0", nil},
	0x83: {"Internal Port Enables 1", nil},
	0x84: {"Internal Port Enables 2", nil},
	0x85: {"Internal Port Enables 3", nil},
	0x86: {"Expansion Bus Port Enables 0", nil},
	0x87: {"Expansion Bus Port Enables 1", nil},
	0x88: {"Expansion Bus Port Enables 2", nil},
	0x89: {"Expansion Bus Port Enables 3", nil},
	0x8A: {"Expansion Bus IO Propagate", nil},
	0x8C: {"Alternate ROM", []field{flag("on", 7), flag("write-only", 6), flag("lock48", 5), flag("lock128", 4), bits("after-reset", 3, 0)}},
	0x8E: {"128K Memory Mapping", []field{flag("dffd0", 7), bits("bank", 6, 4), flag("change", 3), flag("special", 2), bits("rom", 1, 0)}},
	0x8F: {"Memory Mapping Mode", []field{bits("mode", 1, 0)}},
	0x90: {"Pi GPIO Output Enable 0", nil},
	0x91: {"Pi GPIO Output Enable 1", nil},
	0x92: {"Pi GPIO Output Enable 2", nil},
	0x93: {"Pi GPIO Output Enable 3", nil},
	0x98: {"Pi GPIO 0", nil},
	0x99: {"Pi GPIO 1", nil},
	0x9A: {"Pi GPIO 2", nil},
	0x9B: {"Pi GPIO 3", nil},
	0xA0: {"Pi Peripheral Enable", []field{flag("uart", 5), flag("uart-swap", 4), flag("i2c", 3), flag("spi-slave", 1), flag("spi", 0)}},
	0xA2: {"Pi I2S Audio Control", nil},
	0xA8: {"ESP GPIO0 Output Enable", []field{flag("gpio0-out", 0)}},
	0xA9: {"ESP GPIO0", []field{flag("gpio0", 0)}},
	0xB0: {"Extended Keys 0", nil},
	0xB1: {"Extended Keys 1", nil},
	0xB2: {"Extended MD Pad Buttons", nil},
	0xB8: {"divMMC Entry Points 0", nil},
	0xB9: {"divMMC Entry Points Valid 0", nil},
	0xBA: {"divMMC Entry Points Timing 0", nil},
	0xBB: {"divMMC Entry Points 1", nil},
	0xC0: {"Interrupt Control", []field{bits("vector", 7, 5), flag("stackless", 3), bits("im", 2, 1), flag("hw-im2", 0)}},
	0xC2: {"NMI Return Address LSB", nil},
	0xC3: {"NMI Return Address MSB", nil},
	0xC4: {"INT EN 0", []field{flag("expbus", 7), flag("line", 1), flag("ula", 0)}},
	0xC5: {"INT EN 1", []field{bits("ctc", 7, 0)}},
	0xC6: {"INT EN 2", uartIntFields},
	0xC8: {"INT Status 0", []field{flag("line", 1), flag("ula", 0)}},
	0xC9: {"INT Status 1", []field{bits("ctc", 7, 0)}},
	0xCA: {"INT Status 2", uartIntFields},
	0xCC: {"DMA INT EN 0", []field{flag("nmi", 7), flag("line", 1), flag("ula", 0)}},
	0xCD: {"DMA INT EN 1", []field{bits("ctc", 7, 0)}},
	0xCE: {"DMA INT EN 2", uartIntFields},
	0xD8: {"I/O Traps", nil},
	0xD9: {"I/O Trap Write", nil},
	0xDA: {"I/O Trap Cause", nil},
	0xF0: {"XDEV CMD", nil},
	0xF8: {"XADC REG", nil},
	0xF9: {"XADC D0", nil},
	0xFA: {"XADC D1", nil},
}

var uartIntFields = []field{flag("u1-tx", 6), flag("u1-rx-full", 5), flag("u1-rx", 4), flag("u0-tx", 2), flag("u0-rx-full", 1), flag("u0-rx", 0)}

// Name returns the register's name, or "" for a reserved register.
func Name(r byte) string { return regTable[r].name }

// Decode describes a value of register r field by field: flags by name when
// set, other fields as name=value. It returns "-" when the register has fields
// and none is set, and "" for a register with no field layout.
func Decode(r, v byte) string {
	info, ok := regTable[r]
	if !ok || len(info.fields) == 0 {
		return ""
	}
	var parts []string
	for _, f := range info.fields {
		width := f.hi - f.lo + 1
		x := int(v>>f.lo) & (1<<width - 1)
		switch {
		case width == 1 && f.enum == nil:
			if x != 0 {
				parts = append(parts, f.name)
			}
		case f.enum != nil && x < len(f.enum):
			parts = append(parts, f.name+"="+f.enum[x])
		case width == 8:
			if x != 0 {
				parts = append(parts, fmt.Sprintf("%s=%08b", f.name, x))
			}
		default:
			parts = append(parts, fmt.Sprintf("%s=%d", f.name, x))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}
