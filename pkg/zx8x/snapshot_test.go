package zx8x

import "testing"

type fakeMem struct{ m map[uint16]byte }

func (f *fakeMem) Write(addr uint16, val byte) { f.m[addr] = val }

// A ZX81 .P file is a raw dump of memory from $4009 onward (the system-variable
// area minus the first nine bytes, which the ROM reinitialises). Loading injects
// the bytes back at $4009.
func TestLoadPInjectsAt4009(t *testing.T) {
	mem := &fakeMem{m: map[uint16]byte{}}

	LoadP(mem, []byte{0x11, 0x22, 0x33})

	if mem.m[0x4009] != 0x11 || mem.m[0x400A] != 0x22 || mem.m[0x400B] != 0x33 {
		t.Errorf("bytes not injected at $4009: got %02X %02X %02X",
			mem.m[0x4009], mem.m[0x400A], mem.m[0x400B])
	}
}

// A ZX80 .O file is a raw dump from $4000 onward.
func TestLoadOInjectsAt4000(t *testing.T) {
	mem := &fakeMem{m: map[uint16]byte{}}

	LoadO(mem, []byte{0xAB, 0xCD})

	if mem.m[0x4000] != 0xAB || mem.m[0x4001] != 0xCD {
		t.Errorf("bytes not injected at $4000: got %02X %02X", mem.m[0x4000], mem.m[0x4001])
	}
}
