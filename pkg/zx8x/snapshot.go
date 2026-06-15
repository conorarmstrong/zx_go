package zx8x

// MemWriter is the minimal memory interface the snapshot loaders need.
type MemWriter interface {
	Write(addr uint16, val byte)
}

// Snapshot load addresses. A ZX81 .P file is a raw dump from $4009 (the system
// variables minus the nine bytes the ROM reinitialises on load); a ZX80 .O file
// is a raw dump from $4000.
const (
	zx81PBase = 0x4009
	zx80OBase = 0x4000
)

// LoadP injects a ZX81 .P snapshot into mem, starting at $4009. Bytes that would
// fall outside the 16-bit address space are dropped.
func LoadP(mem MemWriter, data []byte) {
	loadRaw(mem, zx81PBase, data)
}

// LoadO injects a ZX80 .O snapshot into mem, starting at $4000.
func LoadO(mem MemWriter, data []byte) {
	loadRaw(mem, zx80OBase, data)
}

func loadRaw(mem MemWriter, base int, data []byte) {
	for i, b := range data {
		addr := base + i
		if addr > 0xFFFF {
			break
		}
		mem.Write(uint16(addr), b)
	}
}
