package z80

import "testing"

// t80n_mcode.vhd asserts I_RETN for BOTH RETN (ED $45 + mirrors)
// and RETI (ED $4D + mirrors); zxnext.vhd feeds it to the divMMC
// as i_retn_seen, clearing the automap latch. The hook must fire
// for every mirror of both instructions (the development log).
func TestRETNHookFiresForRETIAndRETN(t *testing.T) {
	for _, op := range []byte{0x45, 0x55, 0x65, 0x75, 0x4D, 0x5D, 0x6D, 0x7D} {
		mem := &flatMem{}
		c := New(mem, nullULA{})
		fired := 0
		c.SetRETNHook(func() { fired++ })
		mem[0] = 0xED
		mem[1] = op
		c.SP = 0x8000
		c.StepInstruction()
		if fired != 1 {
			t.Errorf("ED %02X: hook fired %d times, want 1", op, fired)
		}
	}
}
