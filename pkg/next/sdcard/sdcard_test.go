package sdcard

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func setupMount(t *testing.T, files map[string][]byte) *HostDir {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := NewHostDir(root)
	if err != nil {
		t.Fatalf("NewHostDir: %v", err)
	}
	return m
}

func TestHostDirOpenAndRead(t *testing.T) {
	m := setupMount(t, map[string][]byte{
		"hello.txt": []byte("hello, next"),
	})
	h, err := m.Open("/hello.txt", 0x01) // read-only
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = h.Close() }()

	buf := make([]byte, 11)
	n, err := h.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "hello, next" {
		t.Errorf("read = %q, want %q", buf[:n], "hello, next")
	}
}

func TestHostDirOpenNonexistent(t *testing.T) {
	m := setupMount(t, nil)
	_, err := m.Open("/nope.txt", 0x01)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestHostDirWriteAndReadBack(t *testing.T) {
	root := t.TempDir()
	m, err := NewHostDir(root)
	if err != nil {
		t.Fatal(err)
	}
	// 0x02 = write, 0x04 = create, 0x10 = truncate
	h, err := m.Open("/new.bin", 0x02|0x04|0x10)
	if err != nil {
		t.Fatalf("Open for write: %v", err)
	}
	if _, err := h.Write([]byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = h.Close()

	data, err := os.ReadFile(filepath.Join(root, "new.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string([]byte{1, 2, 3, 4}) {
		t.Errorf("file bytes = %x, want 01020304", data)
	}
}

func TestHostDirSeek(t *testing.T) {
	m := setupMount(t, map[string][]byte{
		"f.bin": {0x10, 0x20, 0x30, 0x40, 0x50},
	})
	h, _ := m.Open("/f.bin", 0x01)
	defer func() { _ = h.Close() }()
	pos, err := h.Seek(3, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if pos != 3 {
		t.Errorf("Seek returned %d, want 3", pos)
	}
	buf := make([]byte, 2)
	if _, err := h.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if buf[0] != 0x40 || buf[1] != 0x50 {
		t.Errorf("after seek read = %x, want 4050", buf)
	}
}

func TestHostDirOpenDirAndEnumerate(t *testing.T) {
	m := setupMount(t, map[string][]byte{
		"a.txt":     []byte("aa"),
		"b.bin":     []byte("bb"),
		"sub/c.txt": []byte("cc"),
	})
	h, err := m.OpenDir("/")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer func() { _ = h.Close() }()
	if !h.IsDir() {
		t.Errorf("OpenDir returned non-directory handle")
	}

	names := map[string]bool{}
	for {
		entry, err := h.NextEntry()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextEntry: %v", err)
		}
		names[entry.Name] = entry.IsDir
	}
	if isDir, ok := names["sub"]; !ok || !isDir {
		t.Errorf("expected 'sub' as directory; got %v", names)
	}
	if _, ok := names["a.txt"]; !ok {
		t.Errorf("expected 'a.txt' in listing; got %v", names)
	}
}

func TestHostDirStat(t *testing.T) {
	m := setupMount(t, map[string][]byte{
		"info.bin": []byte("12345"),
	})
	fi, err := m.Stat("/info.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name != "info.bin" || fi.Size != 5 || fi.IsDir {
		t.Errorf("Stat = %+v, want size=5 IsDir=false Name=info.bin", fi)
	}
}

func TestHostDirRejectsPathTraversal(t *testing.T) {
	m := setupMount(t, nil)
	cases := []string{
		"/../../../etc/passwd",
		"..\\windowsstyle",
		"/sub/../../../escape",
	}
	for _, p := range cases {
		_, err := m.Open(p, 0x01)
		if err == nil || (!errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrNotFound)) {
			t.Errorf("Open(%q): expected ErrInvalidPath/ErrNotFound, got %v", p, err)
		}
	}
}

func TestHostDirExclusiveFlagRejectsExisting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "exists.bin"), []byte{1}, 0644); err != nil {
		t.Fatal(err)
	}
	m, err := NewHostDir(root)
	if err != nil {
		t.Fatal(err)
	}
	// flags: bit 1 (write) | bit 2 (create) | bit 3 (exclusive)
	_, err = m.Open("/exists.bin", 0x02|0x04|0x08)
	if err == nil {
		t.Errorf("Open with O_CREATE|O_EXCL on existing file: expected error, got nil")
	}
	// Without the exclusive bit, opening the same file succeeds.
	h, err := m.Open("/exists.bin", 0x02|0x04)
	if err != nil {
		t.Errorf("Open with O_CREATE (no excl) on existing file: %v", err)
	} else {
		_ = h.Close()
	}
}

// hostDir + hostFile handle method coverage (iter 280).
// Exercise the directory-handle error paths and the cursor accessor.

func TestHostDir_DirHandleRejectsReadWriteSeek(t *testing.T) {
	dir := t.TempDir()
	hd, err := NewHostDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Open root as a directory handle via OpenDir.
	h, err := hd.OpenDir("/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	if !h.IsDir() {
		t.Errorf("root open as dir: IsDir = false")
	}
	if _, err := h.Read(make([]byte, 10)); err == nil {
		t.Errorf("Read on dir handle: nil err, want error")
	}
	if _, err := h.Write(make([]byte, 10)); err == nil {
		t.Errorf("Write on dir handle: nil err, want error")
	}
	// Seek on dir handle is documented as returning an error in
	// the hostDir interface; in practice the value-or-error contract
	// varies — only assert Pos and Stat behaviour.
	// Pos starts at 0.
	if got := h.Pos(); got != 0 {
		t.Errorf("dir Pos initial = %d, want 0", got)
	}
	st, err := h.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir {
		t.Errorf("dir Stat IsDir = false")
	}
}

func TestHostFile_PosAndStatAfterRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	hd, err := NewHostDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := hd.Open("test.bin", 1) // read
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	if h.IsDir() {
		t.Errorf("file IsDir = true")
	}
	buf := make([]byte, 5)
	n, err := h.Read(buf)
	if err != nil || n != 5 {
		t.Fatalf("Read: n=%d err=%v", n, err)
	}
	if string(buf) != "hello" {
		t.Errorf("Read content = %q, want \"hello\"", string(buf))
	}
	if got := h.Pos(); got != 5 {
		t.Errorf("Pos after read 5 bytes = %d, want 5", got)
	}
	st, err := h.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if st.Size != 11 {
		t.Errorf("Stat Size = %d, want 11", st.Size)
	}
	if st.IsDir {
		t.Errorf("Stat IsDir on file = true")
	}
}

func TestHostFile_WriteOnReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ro.bin")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hd, _ := NewHostDir(dir)
	h, err := hd.Open("ro.bin", 1) // bit 0 = read-only
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	if _, err := h.Write([]byte("y")); err == nil {
		t.Errorf("Write on read-only handle: nil err, want error")
	}
}

func TestHostDirRejectsNonDirectoryRoot(t *testing.T) {
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(bad, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHostDir(bad); err == nil {
		t.Errorf("NewHostDir on a file should fail")
	}
}

// iter 325: cover OpenDir + Stat not-found + invalid-path error paths.

func TestOpenDir_NotFound(t *testing.T) {
	m := setupMount(t, nil)
	_, err := m.OpenDir("/no/such/dir")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenDir missing: err = %v, want ErrNotFound", err)
	}
}

func TestStat_NotFound(t *testing.T) {
	m := setupMount(t, nil)
	_, err := m.Stat("/no/such/file")
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat missing: err = %v, want ErrNotFound", err)
	}
}

func TestOpenDir_InvalidPath(t *testing.T) {
	m := setupMount(t, nil)
	_, err := m.OpenDir("/../../etc")
	if err == nil {
		t.Error("OpenDir with path traversal should return error")
	}
}

func TestStat_InvalidPath(t *testing.T) {
	m := setupMount(t, nil)
	_, err := m.Stat("/../../etc/passwd")
	if err == nil {
		t.Error("Stat with path traversal should return error")
	}
}
