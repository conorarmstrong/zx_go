package z80

import "testing"

// zxnext.vhd:4111 feeds the divMMC's i_retn_seen from z80_retn_seen_28,
// which is im2_control.vhd's o_retn_seen (:236). That block decodes the
// opcode bytes off the bus and matches ED $45 exactly (:128-135, :171-175),
// not the T80N's internal I_RETN (which is never exported). So the hook
// fires for RETN ED $45 only: not for its mirrors $55/$65/$75, and not for
// RETI ED $4D or its mirrors. Firing for RETI dropped the divMMC automap
// whenever an interrupt service routine returned while NextZXOS code was
// running mapped in.
func TestRETNHookFiresForEDRETNOnly(t *testing.T) {
	for _, op := range []byte{0x45, 0x55, 0x65, 0x75, 0x4D, 0x5D, 0x6D, 0x7D} {
		mem := &flatMem{}
		c := New(mem, nullULA{})
		fired := 0
		c.SetRETNHook(func() { fired++ })
		mem[0] = 0xED
		mem[1] = op
		c.SP = 0x8000
		c.StepInstruction()
		want := 0
		if op == 0x45 {
			want = 1
		}
		if fired != want {
			t.Errorf("ED %02X: hook fired %d times, want %d", op, fired, want)
		}
	}
}
