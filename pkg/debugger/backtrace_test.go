package debugger

import "testing"

// memReader builds a readFunc from a sparse map of byte values.
// Anything not in the map reads as 0x00.
func memReader(bytes map[uint16]byte) readFunc {
	return func(a uint16) byte { return bytes[a] }
}

// TestBacktraceClassification pins the four FrameOrigin branches
// down with hand-crafted stack layouts. The shared backend has to
// classify each correctly — the telnet `backtrace` command and the
// visual call-frame widget both depend on this.
func TestBacktraceClassification(t *testing.T) {
	tests := []struct {
		name string
		// Memory layout. Stack words and the bytes preceding each
		// claimed return address.
		mem map[uint16]byte
		// Stack starts at SP. We provide 4 words (8 bytes) above SP.
		sp   uint16
		want []FrameOrigin
		// Expected target for the first frame, when relevant.
		wantTarget   uint16
		wantCallSite uint16
	}{
		{
			name: "CALL nn precedes return",
			mem: map[uint16]byte{
				// SP+0/+1 = return address $1234
				0x6000: 0x34, 0x6001: 0x12,
				// $1231 = CD 78 56  -> CALL $5678
				0x1231: 0xCD, 0x1232: 0x78, 0x1233: 0x56,
			},
			sp:           0x6000,
			want:         []FrameOrigin{OriginCall, OriginSpeculative, OriginSpeculative, OriginSpeculative},
			wantTarget:   0x5678,
			wantCallSite: 0x1231,
		},
		{
			name: "Conditional CALL Z precedes return",
			mem: map[uint16]byte{
				0x6000: 0x10, 0x6001: 0x10,
				// $100D = CC 00 20  -> CALL Z,$2000
				0x100D: 0xCC, 0x100E: 0x00, 0x100F: 0x20,
			},
			sp:           0x6000,
			want:         []FrameOrigin{OriginCallCond, OriginSpeculative, OriginSpeculative, OriginSpeculative},
			wantTarget:   0x2000,
			wantCallSite: 0x100D,
		},
		{
			name: "RST $20 precedes return",
			mem: map[uint16]byte{
				0x6000: 0x21, 0x6001: 0x00,
				// $0020 = E7  -> RST $20
				0x0020: 0xE7,
			},
			sp:           0x6000,
			want:         []FrameOrigin{OriginRst, OriginSpeculative, OriginSpeculative, OriginSpeculative},
			wantTarget:   0x0020,
			wantCallSite: 0x0020,
		},
		{
			name: "All-zero predecessors → speculative",
			mem: map[uint16]byte{
				0x6000: 0xAA, 0x6001: 0xBB,
			},
			sp:   0x6000,
			want: []FrameOrigin{OriginSpeculative, OriginSpeculative, OriginSpeculative, OriginSpeculative},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frames := Backtrace(memReader(tc.mem), tc.sp, 4, 0)
			if len(frames) != 4 {
				t.Fatalf("got %d frames, want 4", len(frames))
			}
			for i, want := range tc.want {
				if frames[i].Origin != want {
					t.Errorf("frame %d: Origin=%v want %v", i, frames[i].Origin, want)
				}
			}
			if tc.want[0] != OriginSpeculative {
				if frames[0].CallTarget != tc.wantTarget {
					t.Errorf("frame 0 CallTarget = $%04X want $%04X",
						frames[0].CallTarget, tc.wantTarget)
				}
				if frames[0].CallSite != tc.wantCallSite {
					t.Errorf("frame 0 CallSite = $%04X want $%04X",
						frames[0].CallSite, tc.wantCallSite)
				}
			}
		})
	}
}

// TestBacktraceDisasmAttached: the disasmAt parameter controls how
// many instructions to decode at each non-speculative return
// address. Speculative frames never carry a disasm slice.
func TestBacktraceDisasmAttached(t *testing.T) {
	mem := map[uint16]byte{
		0x6000: 0x00, 0x6001: 0x10, // returns to $1000
		// $0FFD = CD 00 10 -> CALL $1000
		0x0FFD: 0xCD, 0x0FFE: 0x00, 0x0FFF: 0x10,
		// $1000 onwards: F3 (DI), C3 EF 00 (JP $00EF), AF (XOR A)
		0x1000: 0xF3, 0x1001: 0xC3, 0x1002: 0xEF, 0x1003: 0x00, 0x1004: 0xAF,
	}
	frames := Backtrace(memReader(mem), 0x6000, 1, 3)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	f := frames[0]
	if f.Origin != OriginCall {
		t.Fatalf("Origin = %v want OriginCall", f.Origin)
	}
	if len(f.Disasm) != 3 {
		t.Fatalf("Disasm length = %d want 3", len(f.Disasm))
	}
	if f.Disasm[0].Mnem != "DI" {
		t.Errorf("first disasm mnem = %q want DI", f.Disasm[0].Mnem)
	}
}

// TestBacktraceHintFormat pins the Hint() formatter so the telnet
// front-end and any future structured-log consumer agree.
func TestBacktraceHintFormat(t *testing.T) {
	mem := map[uint16]byte{
		0x6000: 0x00, 0x6001: 0x10, // returns to $1000
		0x0FFD: 0xCD, 0x0FFE: 0x00, 0x0FFF: 0x10,
	}
	frames := Backtrace(memReader(mem), 0x6000, 1, 0)
	want := "CALL $1000 @ $0FFD"
	if got := frames[0].Hint(); got != want {
		t.Errorf("Hint() = %q want %q", got, want)
	}
	// Speculative frame: empty hint.
	frames = Backtrace(memReader(map[uint16]byte{0x6000: 0x42, 0x6001: 0x00}), 0x6000, 1, 0)
	if got := frames[0].Hint(); got != "" {
		t.Errorf("speculative Hint() = %q want empty", got)
	}
}

// TestFrameOrigin_String pins the human-readable mapping used by
// telnet output.
func TestFrameOrigin_String(t *testing.T) {
	cases := []struct {
		o    FrameOrigin
		want string
	}{
		{OriginCall, "from CALL nn"},
		{OriginCallCond, "from CALL cc"},
		{OriginRst, "from RST"},
		{OriginSpeculative, "speculative"},
		{FrameOrigin(99), "speculative"}, // default arm
	}
	for _, c := range cases {
		if got := c.o.String(); got != c.want {
			t.Errorf("FrameOrigin(%d).String() = %q, want %q", int(c.o), got, c.want)
		}
	}
}
