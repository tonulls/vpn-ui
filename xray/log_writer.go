package xray

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/config"
	"github.com/mhsanaei/3x-ui/v2/logger"
)

// NewLogWriter returns a LogWriter that forwards Xray output to the shared panel log.
func NewLogWriter() *LogWriter {
	return &LogWriter{}
}

// NewExternalSelectorLogWriter routes temporary External Selector probe output
// to its own journal instead of mixing it into the main Xray log.
func NewExternalSelectorLogWriter() *LogWriter {
	return &LogWriter{externalSelector: true}
}

// LogWriter processes and filters log output from the Xray process, handling crash detection and message filtering.
type LogWriter struct {
	lastLine         string
	externalSelector bool
}

var externalSelectorLogMu sync.Mutex

const externalSelectorLogMaxBytes = 10 * 1024 * 1024

func appendExternalSelectorLog(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if err := os.MkdirAll(config.GetLogFolder(), 0o750); err != nil {
		logger.Warning("External Selector: cannot create log directory: ", err)
		return
	}

	// Serialize append and trim together so concurrent probe output cannot be lost
	// while the file is atomically replaced with its newest tail.
	externalSelectorLogMu.Lock()
	defer externalSelectorLogMu.Unlock()

	path := filepath.Join(config.GetLogFolder(), "external-selector.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		logger.Warning("External Selector: cannot open module log: ", err)
		return
	}
	_, writeErr := f.WriteString(time.Now().Format("2006-01-02 15:04:05") + " " + line + "\n")
	closeErr := f.Close()
	if writeErr != nil {
		logger.Warning("External Selector: cannot append module log: ", writeErr)
		return
	}
	if closeErr != nil {
		logger.Warning("External Selector: cannot close module log: ", closeErr)
		return
	}
	if err := trimExternalSelectorLog(path); err != nil {
		logger.Warning("External Selector: cannot trim module log: ", err)
	}
}

// trimExternalSelectorLog keeps the newest complete lines within the configured
// byte cap. It reads only the tail, so even an already oversized file cannot cause
// an unbounded allocation during cleanup.
func trimExternalSelectorLog(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if info.Size() <= externalSelectorLogMaxBytes {
		return f.Close()
	}
	start := info.Size() - externalSelectorLogMaxBytes
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		f.Close()
		return err
	}
	tail, readErr := io.ReadAll(io.LimitReader(f, externalSelectorLogMaxBytes))
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if start > 0 {
		// Skip the partial oldest line. If the retained tail is itself one oversized
		// line, preserve its newest bytes rather than emptying the whole journal.
		if end := bytes.IndexByte(tail, '\n'); end >= 0 && end < len(tail)-1 {
			tail = tail[end+1:]
		}
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, tail, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// Write processes and filters log output from the Xray process, handling crash detection and message filtering.
func (lw *LogWriter) Write(m []byte) (n int, err error) {
	crashRegex := regexp.MustCompile(`(?i)(panic|exception|stack trace|fatal error)`)

	// Convert the data to a string
	message := strings.TrimSpace(string(m))

	// Check if the message contains a crash
	if crashRegex.MatchString(message) {
		logger.Debug("Core crash detected:\n", message)
		lw.lastLine = message
		err1 := writeCrashReport(m)
		if err1 != nil {
			logger.Error("Unable to write crash report:", err1)
		}
		return len(m), nil
	}

	regex := regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d{6}) \[([^\]]+)\] (.+)$`)
	messages := strings.SplitSeq(message, "\n")

	for msg := range messages {
		matches := regex.FindStringSubmatch(msg)

		if len(matches) > 3 {
			level := matches[2]
			msgBody := matches[3]
			msgBodyLower := strings.ToLower(msgBody)

			if lw.externalSelector {
				appendExternalSelectorLog("[" + level + "] " + msgBody)
				lw.lastLine = ""
				continue
			}

			if strings.Contains(msgBodyLower, "tls handshake error") ||
				strings.Contains(msgBodyLower, "connection ends") {
				logger.Debug("XRAY: " + msgBody)
				lw.lastLine = ""
				continue
			}

			if strings.Contains(msgBodyLower, "failed") {
				logger.Error("XRAY: " + msgBody)
			} else {
				switch level {
				case "Debug":
					logger.Debug("XRAY: " + msgBody)
				case "Info":
					logger.Info("XRAY: " + msgBody)
				case "Warning":
					logger.Warning("XRAY: " + msgBody)
				case "Error":
					logger.Error("XRAY: " + msgBody)
				default:
					logger.Debug("XRAY: " + msg)
				}
			}
			lw.lastLine = ""
		} else if msg != "" {
			if lw.externalSelector {
				appendExternalSelectorLog(msg)
				lw.lastLine = msg
				continue
			}
			msgLower := strings.ToLower(msg)

			if strings.Contains(msgLower, "tls handshake error") ||
				strings.Contains(msgLower, "connection ends") {
				logger.Debug("XRAY: " + msg)
				lw.lastLine = msg
				continue
			}

			if strings.Contains(msgLower, "failed") {
				logger.Error("XRAY: " + msg)
			} else {
				logger.Debug("XRAY: " + msg)
			}
			lw.lastLine = msg
		}
	}

	return len(m), nil
}
