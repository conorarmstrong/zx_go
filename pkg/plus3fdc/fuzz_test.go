package plus3fdc

import "testing"

// Fuzz the signature-dispatched disk-image parsers (DSK / EDSK / UDI / SAD)
// against arbitrary bytes: a malformed image must be rejected or parsed
// without panicking, and walking the resulting tracks for sectors must be
// crash-free too, since the FDC does exactly that walk on every read.
//
// Run: go test ./pkg/plus3fdc/ -run x -fuzz FuzzParseDiskImage -fuzztime 60s
func FuzzParseDiskImage(f *testing.F) {
	f.Add([]byte("MV - CPCEMU Disk-File\r\nDisk-Info\r\n"))
	f.Add([]byte("EXTENDED CPC DSK File\r\nDisk-Info\r\n"))
	f.Add(append(append([]byte{}, udiSignature...), make([]byte, 32)...))
	f.Add(append(append([]byte{}, sadSignature...), make([]byte, 32)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := ParseDiskImage(data)
		if err != nil || d == nil {
			return // rejected — fine
		}
		for h := 0; h < d.Sides; h++ {
			for c := 0; c < d.Cylinders; c++ {
				tr := d.Track(h, c)
				if tr == nil {
					continue
				}
				for r := 0; r < 256; r++ {
					tr.FindSector(byte(r))
				}
			}
		}
	})
}
