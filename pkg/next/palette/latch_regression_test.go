package palette

import "testing"

// NR$44 is a two-byte protocol and nr_palette_sub_idx is the half-pair
// latch. The FPGA clears it on NR$40, NR$41 and NR$43 as well as at
// the end of a pair (zxnext.vhd:5375, 5381, 5394):
//
//	when X"40" => nr_palette_idx <= nr_wr_dat; nr_palette_sub_idx <= '0';
//	when X"41" => ...                          nr_palette_sub_idx <= '0';
//	when X"43" => ...                          nr_palette_sub_idx <= '0';
//
// We cleared have9 only after the second $44 write, so an odd $44
// followed by a change of index or palette left the latch armed: the
// next $44 was taken as the *second* byte of a pair that the guest
// thought it had abandoned, and every colour after it landed one write
// out of step.
func TestIndexWriteDropsAHalfCompletedNR44Pair(t *testing.T) {
	b := NewBank()
	b.SetIndex(4)
	b.WriteNR44(0x01) // first half of a pair, then abandoned
	b.SetIndex(9)     // NR$40

	// This must be read as a fresh first half, not as the second byte
	// of the abandoned pair.
	b.WriteNR44(0x02)
	b.WriteNR44(0x01) // completes: high $02, low bit 1
	if got := b.Index(); got != 10 {
		t.Errorf("index after one complete pair from 9 = %d, want 10", got)
	}
}

func TestWrite8DropsAHalfCompletedNR44Pair(t *testing.T) {
	b := NewBank()
	b.WriteNR44(0x01)
	b.Write8(0x55) // NR$41
	if b.have9 {
		t.Error("NR$41 left the NR$44 half-pair latch armed")
	}
}

func TestSelectDropsAHalfCompletedNR44Pair(t *testing.T) {
	b := NewBank()
	b.WriteNR44(0x01)
	b.Select(0x02) // NR$43
	if b.have9 {
		t.Error("NR$43 left the NR$44 half-pair latch armed")
	}
}

func TestIndexWriteClearsTheLatchDirectly(t *testing.T) {
	b := NewBank()
	b.WriteNR44(0x01)
	b.SetIndex(9) // NR$40
	if b.have9 {
		t.Error("NR$40 left the NR$44 half-pair latch armed")
	}
}
