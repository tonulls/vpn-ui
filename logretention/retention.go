// Package logretention provides bounded file rotation for application-managed and
// externally-written logs. Active external logs use copy-truncate so their writers
// can continue using the same file descriptor.
package logretention

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const MaxFileBytes int64 = 10 * 1024 * 1024

// RotateIfTooLarge archives the newest maxBytes of an oversized active file, shifts
// numbered backups, and truncates the original inode so external writers can continue.
func RotateIfTooLarge(path string, maxBytes int64, backups int) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if maxBytes <= 0 || info.Size() <= maxBytes {
		return false, nil
	}

	tail, err := newestTail(f, info.Size(), maxBytes)
	if err != nil {
		return false, err
	}
	if backups > 0 {
		if err := writeBackup(path, tail, info.Mode().Perm(), maxBytes, backups); err != nil {
			return false, err
		}
	}
	if err := f.Truncate(0); err != nil {
		return false, err
	}
	return true, nil
}

// CopyTail writes the newest maxBytes from source to destination, starting at a
// line boundary when possible. It does not modify the source.
func CopyTail(source, destination string, maxBytes int64) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	tail, readErr := newestTail(f, info.Size(), maxBytes)
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	tmp := destination + ".tmp"
	if err := os.WriteFile(tmp, tail, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp, destination); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// TrimIfTooLarge bounds an inactive log/archive while preserving its newest lines.
func TrimIfTooLarge(path string, maxBytes int64) (bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return false, err
	}
	if maxBytes <= 0 || info.Size() <= maxBytes {
		f.Close()
		return false, nil
	}
	tail, readErr := newestTail(f, info.Size(), maxBytes)
	closeErr := f.Close()
	if readErr != nil {
		return false, readErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	tmp := path + ".trim.tmp"
	if err := os.WriteFile(tmp, tail, info.Mode().Perm()); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func newestTail(f *os.File, size, maxBytes int64) ([]byte, error) {
	start := size - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	tail, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if end := bytes.IndexByte(tail, '\n'); end >= 0 && end < len(tail)-1 {
			tail = tail[end+1:]
		}
	}
	return tail, nil
}

// TrimNumberedBackups ensures every retained path.1..path.N file is within maxBytes.
func TrimNumberedBackups(path string, backups int, maxBytes int64) error {
	for i := 1; i <= backups; i++ {
		if _, err := TrimIfTooLarge(fmt.Sprintf("%s.%d", path, i), maxBytes); err != nil {
			return err
		}
	}
	return nil
}

// KeepNewestFiles removes older lexically sortable files until at most keep remain.
func KeepNewestFiles(pattern string, keep int) error {
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if keep < 0 {
		keep = 0
	}
	for len(files) > keep {
		if err := os.Remove(files[0]); err != nil && !os.IsNotExist(err) {
			return err
		}
		files = files[1:]
	}
	return nil
}

func writeBackup(path string, tail []byte, mode os.FileMode, maxBytes int64, backups int) error {
	tmp := fmt.Sprintf("%s.1.tmp", path)
	if err := os.WriteFile(tmp, tail, mode); err != nil {
		return err
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", path, backups))
	for i := backups - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(old); err == nil {
			if _, err := TrimIfTooLarge(old, maxBytes); err != nil {
				_ = os.Remove(tmp)
				return err
			}
			if err := os.Rename(old, fmt.Sprintf("%s.%d", path, i+1)); err != nil {
				_ = os.Remove(tmp)
				return err
			}
		} else if !os.IsNotExist(err) {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, filepath.Clean(path)+".1"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
