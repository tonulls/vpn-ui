package job

import (
	"os"
	"path/filepath"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/logretention"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// ClearLogsJob rolls security and access logs once a day.
type ClearLogsJob struct{}

// NewClearLogsJob creates a new log cleanup job instance.
func NewClearLogsJob() *ClearLogsJob {
	return new(ClearLogsJob)
}

func ensureFileExists(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return err
	}
	return file.Close()
}

// Run rolls the banned-IP log by one day and keeps three daily generations of
// persistent Xray access history (today plus the two previous days).
func (j *ClearLogsJob) Run() {
	ipLimit := xray.GetIPLimitLogPath()
	banned := xray.GetIPLimitBannedLogPath()
	bannedPrev := xray.GetIPLimitBannedPrevLogPath()
	accessCurrent := xray.GetAccessPersistentLogPath()
	accessPrev := xray.GetAccessPersistentPrevLogPath()
	accessPrev2 := xray.GetAccessPersistentPrev2LogPath()

	all := []string{ipLimit, banned, bannedPrev, accessCurrent, accessPrev, accessPrev2}
	for _, path := range all {
		if err := ensureFileExists(path); err != nil {
			logger.Warning("Failed to ensure log file exists:", path, "-", err)
		}
	}

	// This is an intentionally transient diagnostic file; the current day's content
	// is kept until midnight and then cleared. Size-triggered backups are handled by
	// LogRetentionJob if a busy day reaches the hard cap first.
	if err := os.Truncate(ipLimit, 0); err != nil {
		logger.Warning("Failed to clear IP limit log:", ipLimit, "-", err)
	}

	if err := rotateDailyLog(banned, []string{bannedPrev}); err != nil {
		logger.Warning("Failed to roll banned-IP log:", err)
	}
	accessPersistentLogMu.Lock()
	err := rotateDailyLog(accessCurrent, []string{accessPrev, accessPrev2})
	accessPersistentLogMu.Unlock()
	if err != nil {
		logger.Warning("Failed to roll persistent access log:", err)
	}
}

func rotateDailyLog(current string, previous []string) error {
	for i := len(previous) - 1; i > 0; i-- {
		older, newer := previous[i-1], previous[i]
		if err := os.Remove(newer); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(older, newer); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if len(previous) > 0 {
		if err := logretention.CopyTail(current, previous[0], logretention.MaxFileBytes); err != nil {
			return err
		}
	}
	return os.Truncate(current, 0)
}
