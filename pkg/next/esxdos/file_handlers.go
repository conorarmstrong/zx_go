package esxdos

import (
	"errors"
	"io"
	"strings"

	"github.com/conorarmstrong/zx_go/pkg/next/sdcard"
	"github.com/conorarmstrong/zx_go/pkg/z80"
)

// readPath reads a null- or 0xFF-terminated ASCII path string from
// the emulated memory bus starting at addr. The esxDOS convention
// accepts either terminator. Returns the path (without the
// terminator) and the number of bytes consumed including the
// terminator. Soft cap at 256 bytes to bound malicious / corrupt
// pointers.
func readPath(mem Memory, addr uint16) string {
	var sb strings.Builder
	for i := 0; i < 256; i++ {
		b := mem.Read(addr + uint16(i))
		if b == 0 || b == 0xFF {
			break
		}
		sb.WriteByte(b)
	}
	return sb.String()
}

// writeBytes copies src into emulated memory starting at addr.
func writeBytes(mem Memory, addr uint16, src []byte) {
	for i, b := range src {
		mem.Write(addr+uint16(i), b)
	}
}

// allocHandle assigns a fresh esxDOS file handle to fh and stores
// it in the dispatcher's handle table. Handles wrap at 1..127 so
// 0 stays reserved for "no handle"; 128+ wouldn't survive A-register
// reads on the guest side because some software treats the high
// bit as a flag.
func (d *Dispatcher) allocHandle(fh sdcard.Handle) (byte, error) {
	for tries := 0; tries < 127; tries++ {
		d.nextHandle = (d.nextHandle + 1) & 0x7F
		if d.nextHandle == 0 {
			d.nextHandle = 1
		}
		if _, used := d.files[d.nextHandle]; !used {
			d.files[d.nextHandle] = fh
			return d.nextHandle, nil
		}
	}
	return 0, NewError(ESX_EIO, "esxdos: no free handles")
}

// mapMountError translates sdcard.* sentinels to esxDOS error codes.
func mapMountError(err error) error {
	if errors.Is(err, sdcard.ErrNotFound) {
		return NewError(ESX_ENOENT, err.Error())
	}
	if errors.Is(err, sdcard.ErrInvalidPath) {
		return NewError(ESX_EINVAL, err.Error())
	}
	return NewError(ESX_EIO, err.Error())
}

// F_OPEN: open file at path *(HL), flags in B. Returns handle in A.
func (d *Dispatcher) handleFOpen(cpu *z80.CPU, mem Memory) error {
	path := readPath(mem, cpu.HL())
	flags := cpu.B
	fh, err := d.mount.Open(path, flags)
	if err != nil {
		return mapMountError(err)
	}
	h, err := d.allocHandle(fh)
	if err != nil {
		_ = fh.Close()
		return err
	}
	cpu.A = h
	return nil
}

// F_CLOSE: close handle in A.
func (d *Dispatcher) handleFClose(cpu *z80.CPU, _ Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	_ = fh.Close()
	delete(d.files, cpu.A)
	return nil
}

// fReadChunkSize caps the host-side buffer used per F_READ call so
// a guest can't request 64 KB and have us allocate 64 KB in one
// go. Real NextZXOS reads in much smaller chunks; this is just a
// guard. If the guest's BC exceeds the cap we read+copy in
// chunks, accumulating bytes-read in BC.
const fReadChunkSize = 4096

// F_READ: read BC bytes from handle A into buffer at HL. On return
// BC holds the bytes actually read.
func (d *Dispatcher) handleFRead(cpu *z80.CPU, mem Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	if fh.IsDir() {
		return NewError(ESX_EWRTYPE, "esxdos: F_READ on directory handle")
	}
	want := cpu.BC()
	dst := cpu.HL()
	var totalRead uint16
	buf := make([]byte, fReadChunkSize)
	for want > 0 {
		chunk := want
		if chunk > fReadChunkSize {
			chunk = fReadChunkSize
		}
		n, err := io.ReadFull(fh, buf[:chunk])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return mapMountError(err)
		}
		writeBytes(mem, dst, buf[:n])
		dst += uint16(n)
		totalRead += uint16(n)
		want -= uint16(n)
		if n < int(chunk) {
			break // hit EOF
		}
	}
	cpu.SetBC(totalRead)
	return nil
}

// F_WRITE: write BC bytes from buffer at HL to handle A. BC on
// return holds bytes actually written.
func (d *Dispatcher) handleFWrite(cpu *z80.CPU, mem Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	count := cpu.BC()
	buf := make([]byte, count)
	for i := uint16(0); i < count; i++ {
		buf[i] = mem.Read(cpu.HL() + i)
	}
	n, err := fh.Write(buf)
	if err != nil {
		return mapMountError(err)
	}
	cpu.SetBC(uint16(n))
	return nil
}

// F_SEEK: seek file handle A. L = whence (0=start, 1=current,
// 2=end). BCDE = signed 32-bit offset. On return BCDE holds the
// new position.
func (d *Dispatcher) handleFSeek(cpu *z80.CPU, _ Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	whence := int(cpu.L) // 0/1/2 maps directly to io.Seek* constants
	offset := int64(cpu.BC())<<16 | int64(cpu.DE())
	// Make offset signed-32: top bit means negative.
	if offset&0x80000000 != 0 {
		offset -= 0x100000000
	}
	pos, err := fh.Seek(offset, whence)
	if err != nil {
		return mapMountError(err)
	}
	cpu.SetBC(uint16(pos >> 16))
	cpu.SetDE(uint16(pos & 0xFFFF))
	return nil
}

// F_FGETPOS: return the current cursor position of handle A as a
// 32-bit value in BCDE (BC = high word, DE = low word). Mirrors the
// F_SEEK return convention.
func (d *Dispatcher) handleFGetPos(cpu *z80.CPU, _ Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	pos := fh.Pos()
	cpu.SetBC(uint16(pos >> 16))
	cpu.SetDE(uint16(pos & 0xFFFF))
	return nil
}

// F_FTRUNCATE: truncate handle A to the 32-bit size in BCDE (BC =
// high word, DE = low word). The file cursor is unaffected. A
// directory or read-only handle returns an error.
func (d *Dispatcher) handleFTruncate(cpu *z80.CPU, _ Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	size := int64(cpu.BC())<<16 | int64(cpu.DE())
	if err := fh.Truncate(size); err != nil {
		return mapMountError(err)
	}
	return nil
}

// F_SYNC: flush handle A's buffered writes to stable storage.
func (d *Dispatcher) handleFSync(cpu *z80.CPU, _ Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	if err := fh.Sync(); err != nil {
		return mapMountError(err)
	}
	return nil
}

// F_FSTAT: stat the file at HL into the 11-byte stat structure
// starting at DE. esxDOS stat layout:
//
//	+0..2  date (4-2-1 bit-packed DOS-style)
//	+3..4  time (4-2-1 bit-packed)
//	+5     attribute (FAT-style; 0x10 = directory)
//	+6..7  size lo word
//	+8..9  size hi word
//	+10    reserved
//
// Only size and the directory bit are filled in; date/time/attribute
// stay zero — host mtime is available via ModTime for a caller that
// wants to convert it.
func (d *Dispatcher) handleFStat(cpu *z80.CPU, mem Memory) error {
	path := readPath(mem, cpu.HL())
	fi, err := d.mount.Stat(path)
	if err != nil {
		return mapMountError(err)
	}
	stat := make([]byte, 11)
	if fi.IsDir {
		stat[5] = 0x10
	}
	stat[6] = byte(fi.Size & 0xFF)
	stat[7] = byte((fi.Size >> 8) & 0xFF)
	stat[8] = byte((fi.Size >> 16) & 0xFF)
	stat[9] = byte((fi.Size >> 24) & 0xFF)
	writeBytes(mem, cpu.DE(), stat)
	return nil
}

// F_OPENDIR: open directory at HL.
func (d *Dispatcher) handleFOpenDir(cpu *z80.CPU, mem Memory) error {
	path := readPath(mem, cpu.HL())
	fh, err := d.mount.OpenDir(path)
	if err != nil {
		return mapMountError(err)
	}
	h, err := d.allocHandle(fh)
	if err != nil {
		_ = fh.Close()
		return err
	}
	cpu.A = h
	return nil
}

// F_READDIR: read next directory entry from handle A into the
// buffer at HL. The entry is a packed structure: name (variable,
// null-terminated, max 13 bytes) followed by an 11-byte stat
// block. On end-of-directory return ESX_EWRTYPE so callers can
// distinguish "no more" from a real error.
//
// The entry layout here is a simplification: NULL-terminated name
// (max 13 chars), then the 11-byte stat block from F_FSTAT. Real
// esxDOS uses a slightly different layout.
func (d *Dispatcher) handleFReadDir(cpu *z80.CPU, mem Memory) error {
	fh, ok := d.files[cpu.A]
	if !ok {
		return NewError(ESX_EBADF, "esxdos: bad handle")
	}
	if !fh.IsDir() {
		return NewError(ESX_EWRTYPE, "esxdos: F_READDIR on file handle")
	}
	entry, err := fh.NextEntry()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return NewError(ESX_EWRTYPE, "esxdos: end of directory")
		}
		return mapMountError(err)
	}
	name := entry.Name
	if len(name) > 12 {
		name = name[:12]
	}
	buf := make([]byte, 0, len(name)+1+11)
	buf = append(buf, []byte(name)...)
	buf = append(buf, 0)
	stat := make([]byte, 11)
	if entry.IsDir {
		stat[5] = 0x10
	}
	stat[6] = byte(entry.Size & 0xFF)
	stat[7] = byte((entry.Size >> 8) & 0xFF)
	stat[8] = byte((entry.Size >> 16) & 0xFF)
	stat[9] = byte((entry.Size >> 24) & 0xFF)
	buf = append(buf, stat...)
	writeBytes(mem, cpu.HL(), buf)
	return nil
}
