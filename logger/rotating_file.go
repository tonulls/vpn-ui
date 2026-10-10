package logger

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mhsanaei/3x-ui/v2/logretention"
)

const (
	maxLogFileBytes = 10 * 1024 * 1024
	maxLogBackups   = 5
)

type rotatingFile struct {
	mu        sync.Mutex
	path      string
	file      *os.File
	size      int64
	maxBytes  int64
	maxBackup int
}

func openRotatingFile(path string, maxBytes int64, maxBackup int) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	for i := 1; i <= maxBackup; i++ {
		if _, err := logretention.TrimIfTooLarge(fmt.Sprintf("%s.%d", path, i), maxBytes); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	rotator := &rotatingFile{path: path, file: f, size: info.Size(), maxBytes: maxBytes, maxBackup: maxBackup}
	if rotator.size >= rotator.maxBytes {
		if err := rotator.rotateLocked(); err != nil {
			f.Close()
			return nil, err
		}
	}
	return rotator, nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	originalLen := len(p)
	if w.maxBytes <= 0 {
		n, err := w.file.Write(p)
		return n, err
	}
	if int64(len(p)) > w.maxBytes {
		p = newestCompleteTail(p, int(w.maxBytes))
	}
	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return 0, err
	}
	return originalLen, nil
}

func (w *rotatingFile) rotateLocked() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	recoverWriter := func(cause error) error {
		f, reopenErr := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
		if reopenErr != nil {
			return fmt.Errorf("%v (also failed to reopen log: %w)", cause, reopenErr)
		}
		info, statErr := f.Stat()
		if statErr != nil {
			f.Close()
			return fmt.Errorf("%v (also failed to stat reopened log: %w)", cause, statErr)
		}
		w.file, w.size = f, info.Size()
		return cause
	}
	if w.maxBackup > 0 {
		_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.maxBackup))
		for i := w.maxBackup - 1; i >= 1; i-- {
			oldPath := fmt.Sprintf("%s.%d", w.path, i)
			if _, err := os.Stat(oldPath); err == nil {
				if err := os.Rename(oldPath, fmt.Sprintf("%s.%d", w.path, i+1)); err != nil {
					return recoverWriter(err)
				}
			}
		}
		if w.size > 0 {
			if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
				return recoverWriter(err)
			}
		} else {
			_ = os.Remove(w.path)
		}
	} else if err := os.Truncate(w.path, 0); err != nil && !os.IsNotExist(err) {
		return recoverWriter(err)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o660)
	if err != nil {
		return recoverWriter(err)
	}
	w.file = f
	w.size = 0
	return nil
}

func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// newestCompleteTail keeps the newest bytes and, when possible, starts at a line
// boundary. An individual overlong log line is retained as a bounded partial tail.
func newestCompleteTail(data []byte, maxBytes int) []byte {
	if maxBytes <= 0 || len(data) <= maxBytes {
		return data
	}
	tail := data[len(data)-maxBytes:]
	if end := bytes.IndexByte(tail, '\n'); end >= 0 && end < len(tail)-1 {
		tail = tail[end+1:]
	}
	return tail
}
