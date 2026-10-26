package config

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// AtomicWriteIfChanged retains an existing regular file only when its bytes and
// permissions match. Changed, missing, or unsafe files use synced publication.
func AtomicWriteIfChanged(path string, data []byte, mode os.FileMode) error {
	if sameFileContent(path, data, mode) {
		return nil
	}

	return AtomicWrite(path, data, mode)
}

func sameFileContent(path string, data []byte, mode os.FileMode) (same bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode() != mode.Perm() || info.Size() != int64(len(data)) {
		return false
	}

	file, err := os.Open(path) //nolint:gosec // G304: Callers select the same trusted local path used for atomic publication.
	if err != nil {
		return false
	}

	defer func() {
		if file.Close() != nil {
			same = false
		}
	}()

	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !equalFileBytes(file, data) {
		return false
	}

	current, err := os.Lstat(path)

	return err == nil && os.SameFile(opened, current) && current.Mode() == mode.Perm() && current.Size() == int64(len(data))
}

// Compare with bounded scratch space even for a large generated media index.
func equalFileBytes(file *os.File, data []byte) bool {
	var buffer [32 << 10]byte

	for len(data) > 0 {
		chunk := buffer[:min(len(data), len(buffer))]
		if _, err := io.ReadFull(file, chunk); err != nil || !bytes.Equal(chunk, data[:len(chunk)]) {
			return false
		}

		data = data[len(chunk):]
	}

	n, err := file.Read(buffer[:1])

	return n == 0 && errors.Is(err, io.EOF)
}
