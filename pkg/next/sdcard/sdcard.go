// Package sdcard backs the Spectrum Next's esxDOS file API with a
// host filesystem mount. HostDir is a "directory-as-SD-card"
// implementation: esxDOS paths like "/demos/test.nex" translate to
// host paths under a mount root using filepath.Join. This avoids
// implementing FAT16/32 parsing while remaining sufficient for
// running NextZXOS dot commands and loading .NEX files.
//
// A FAT-image backend can also be used behind the same Mount
// interface when disk-image fidelity is needed (sector access, FAT
// idiosyncrasies, on-disk format compatibility) — see fat16.go and
// fat32.go.
package sdcard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Mount is the file-system surface esxDOS handlers consume. The
// interface mirrors the parts of esxDOS the handler set calls into;
// concrete implementations include HostDir.
type Mount interface {
	// Open opens a file at the given esxDOS-style path. The flags
	// follow esxDOS conventions: bit 0 = read, bit 1 = write,
	// bit 2 = create, bit 3 = exclusive, bit 4 = truncate, etc.
	// HostDir honours bits 0 (read) and 1 (write); the rest are
	// accepted and either implied or ignored.
	Open(path string, flags byte) (Handle, error)

	// OpenDir opens a directory for enumeration via Handle.NextEntry.
	OpenDir(path string) (Handle, error)

	// Stat returns metadata for a path.
	Stat(path string) (FileInfo, error)
}

// Handle is an open file or directory. Close releases the
// underlying resource. The other methods are valid for files only
// (when IsDir() is false) or directories only (when true).
type Handle interface {
	io.Closer

	IsDir() bool

	// Read fills buf and returns the number of bytes read; for
	// files, this advances the file cursor.
	Read(buf []byte) (int, error)

	// Write writes buf to the file at the current cursor; for
	// read-only or directory handles, returns an error.
	Write(buf []byte) (int, error)

	// Seek sets the file cursor relative to whence (0 = start,
	// 1 = current, 2 = end).
	Seek(offset int64, whence int) (int64, error)

	// Pos returns the current cursor position. For directories
	// this is the iteration index.
	Pos() int64

	// Truncate resizes the file to size bytes, extending with zeros
	// or discarding the tail as needed. Returns an error for
	// directory or read-only handles.
	Truncate(size int64) error

	// Sync flushes any buffered writes to stable storage. A no-op
	// (nil) for read-only handles.
	Sync() error

	// Stat returns the metadata for this handle's target.
	Stat() (FileInfo, error)

	// NextEntry reads the next directory entry (for directory
	// handles). Returns io.EOF when the directory is exhausted.
	NextEntry() (FileInfo, error)
}

// FileInfo is the subset of file metadata esxDOS handlers care
// about. Times are unix-style seconds since epoch in the host
// timezone: HostDir doesn't model the Next's RTC, so dates come
// from the host file's modification time.
type FileInfo struct {
	Name    string
	Size    int64
	IsDir   bool
	ModTime int64
}

// ErrNotFound is returned when a path doesn't resolve to a file
// or directory.
var ErrNotFound = errors.New("sdcard: not found")

// ErrInvalidPath is returned when an esxDOS path would escape the
// mount root (".." traversal) or is otherwise malformed.
var ErrInvalidPath = errors.New("sdcard: invalid path")

// HostDir is a Mount backed by a directory on the host
// filesystem. Construct via NewHostDir.
type HostDir struct {
	root string
}

// NewHostDir creates a HostDir mount rooted at the given host
// directory. The directory must exist; subdirectories and files
// inside it are reachable through esxDOS paths.
func NewHostDir(root string) (*HostDir, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("sdcard: stat %s: %w", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("sdcard: %s is not a directory", root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("sdcard: abs %s: %w", root, err)
	}
	return &HostDir{root: abs}, nil
}

// resolve converts an esxDOS path to a host filesystem path,
// rejecting any traversal that would escape root.
func (m *HostDir) resolve(esxPath string) (string, error) {
	// Normalise: treat as posix; reject any segment that's exactly
	// ".." or contains backslashes (those would be interpreted
	// differently on Windows hosts).
	if strings.Contains(esxPath, "\\") {
		return "", ErrInvalidPath
	}
	clean := filepath.Clean("/" + esxPath)
	host := filepath.Join(m.root, filepath.FromSlash(clean))
	hostAbs, err := filepath.Abs(host)
	if err != nil {
		return "", fmt.Errorf("sdcard: abs %s: %w", host, err)
	}
	if !strings.HasPrefix(hostAbs, m.root) {
		return "", ErrInvalidPath
	}
	return hostAbs, nil
}

// Open opens a file under the mount root. esxDOS flags: bit 0 =
// read, bit 1 = write, bit 2 = create (if missing), bit 4 =
// truncate.
func (m *HostDir) Open(esxPath string, flags byte) (Handle, error) {
	host, err := m.resolve(esxPath)
	if err != nil {
		return nil, err
	}
	osFlags := 0
	read := flags&0x01 != 0
	write := flags&0x02 != 0
	switch {
	case read && write:
		osFlags = os.O_RDWR
	case write:
		osFlags = os.O_WRONLY
	default:
		osFlags = os.O_RDONLY
	}
	if flags&0x04 != 0 {
		osFlags |= os.O_CREATE
	}
	// Bit 3 = exclusive open: fail if file already exists. Only
	// meaningful in combination with O_CREATE; on its own esxDOS
	// docs say it's a no-op.
	if flags&0x08 != 0 {
		osFlags |= os.O_EXCL
	}
	if flags&0x10 != 0 {
		osFlags |= os.O_TRUNC
	}
	f, err := os.OpenFile(host, osFlags, 0644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, esxPath)
		}
		return nil, fmt.Errorf("sdcard: open %s: %w", esxPath, err)
	}
	return &hostFile{file: f, path: esxPath, writable: write}, nil
}

// OpenDir opens a directory for enumeration.
func (m *HostDir) OpenDir(esxPath string) (Handle, error) {
	host, err := m.resolve(esxPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(host)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, esxPath)
		}
		return nil, fmt.Errorf("sdcard: opendir %s: %w", esxPath, err)
	}
	return &hostDir{entries: entries}, nil
}

// Stat returns metadata for the given esxDOS path.
func (m *HostDir) Stat(esxPath string) (FileInfo, error) {
	host, err := m.resolve(esxPath)
	if err != nil {
		return FileInfo{}, err
	}
	st, err := os.Stat(host)
	if err != nil {
		if os.IsNotExist(err) {
			return FileInfo{}, fmt.Errorf("%w: %s", ErrNotFound, esxPath)
		}
		return FileInfo{}, fmt.Errorf("sdcard: stat %s: %w", esxPath, err)
	}
	return FileInfo{
		Name:    st.Name(),
		Size:    st.Size(),
		IsDir:   st.IsDir(),
		ModTime: st.ModTime().Unix(),
	}, nil
}

// hostFile is a Handle for a regular file on the host.
type hostFile struct {
	file     *os.File
	path     string
	writable bool
}

func (h *hostFile) Close() error { return h.file.Close() }
func (h *hostFile) IsDir() bool  { return false }
func (h *hostFile) Read(buf []byte) (int, error) {
	return h.file.Read(buf)
}
func (h *hostFile) Write(buf []byte) (int, error) {
	if !h.writable {
		return 0, fmt.Errorf("sdcard: %s opened read-only", h.path)
	}
	return h.file.Write(buf)
}
func (h *hostFile) Seek(offset int64, whence int) (int64, error) {
	return h.file.Seek(offset, whence)
}
func (h *hostFile) Pos() int64 {
	pos, _ := h.file.Seek(0, io.SeekCurrent)
	return pos
}
func (h *hostFile) Truncate(size int64) error {
	if !h.writable {
		return fmt.Errorf("sdcard: %s opened read-only", h.path)
	}
	return h.file.Truncate(size)
}
func (h *hostFile) Sync() error { return h.file.Sync() }
func (h *hostFile) Stat() (FileInfo, error) {
	st, err := h.file.Stat()
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:    st.Name(),
		Size:    st.Size(),
		IsDir:   false,
		ModTime: st.ModTime().Unix(),
	}, nil
}
func (h *hostFile) NextEntry() (FileInfo, error) {
	return FileInfo{}, fmt.Errorf("sdcard: %s is not a directory", h.path)
}

// hostDir is a Handle for a directory listing.
type hostDir struct {
	entries []os.DirEntry
	idx     int
}

func (d *hostDir) Close() error { return nil }
func (d *hostDir) IsDir() bool  { return true }
func (d *hostDir) Read(_ []byte) (int, error) {
	return 0, errors.New("sdcard: read on directory handle")
}
func (d *hostDir) Write(_ []byte) (int, error) {
	return 0, errors.New("sdcard: write on directory handle")
}
func (d *hostDir) Seek(_ int64, _ int) (int64, error) {
	return 0, errors.New("sdcard: seek on directory handle")
}
func (d *hostDir) Pos() int64             { return int64(d.idx) }
func (d *hostDir) Truncate(_ int64) error { return errors.New("sdcard: truncate on directory handle") }
func (d *hostDir) Sync() error            { return nil }
func (d *hostDir) Stat() (FileInfo, error) {
	return FileInfo{IsDir: true}, nil
}
func (d *hostDir) NextEntry() (FileInfo, error) {
	if d.idx >= len(d.entries) {
		return FileInfo{}, io.EOF
	}
	e := d.entries[d.idx]
	d.idx++
	info, err := e.Info()
	if err != nil {
		return FileInfo{Name: e.Name(), IsDir: e.IsDir()}, nil
	}
	return FileInfo{
		Name:    e.Name(),
		Size:    info.Size(),
		IsDir:   e.IsDir(),
		ModTime: info.ModTime().Unix(),
	}, nil
}
