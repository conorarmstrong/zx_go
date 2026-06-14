package main

import "testing"

func TestIsCallLike(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		want  bool
	}{
		{"CALL nn", []byte{0xCD, 0x00, 0x10}, true},
		{"CALL Z,nn", []byte{0xCC, 0x00, 0x10}, true},
		{"CALL NZ,nn", []byte{0xC4, 0x00, 0x10}, true},
		{"CALL NC,nn", []byte{0xD4, 0x00, 0x10}, true},
		{"CALL C,nn", []byte{0xDC, 0x00, 0x10}, true},
		{"CALL PO,nn", []byte{0xE4, 0x00, 0x10}, true},
		{"CALL PE,nn", []byte{0xEC, 0x00, 0x10}, true},
		{"CALL P,nn", []byte{0xF4, 0x00, 0x10}, true},
		{"CALL M,nn", []byte{0xFC, 0x00, 0x10}, true},
		{"RST 0", []byte{0xC7}, true},
		{"RST 8", []byte{0xCF}, true},
		{"RST 38", []byte{0xFF}, true},
		{"Z80N PUSH NN", []byte{0xED, 0x8A, 0x12, 0x34}, true},
		{"plain JP", []byte{0xC3, 0x00, 0x10}, false},
		{"JR", []byte{0x18, 0x05}, false},
		{"NOP", []byte{0x00}, false},
		{"RET", []byte{0xC9}, false},
		{"ED other", []byte{0xED, 0x44}, false}, // NEG
		{"empty", []byte{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var op byte
			if len(tc.bytes) > 0 {
				op = tc.bytes[0]
			}
			if got := isCallLike(op, tc.bytes); got != tc.want {
				t.Errorf("isCallLike(%v) = %v, want %v", tc.bytes, got, tc.want)
			}
		})
	}
}
