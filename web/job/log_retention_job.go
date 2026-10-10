package job

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/config"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/logretention"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// LogRetentionJob bounds external-daemon logs that cannot rotate through the
// panel's own logger (StrongSwan, accel-ppp, 3xipl, and preview stdout).
type LogRetentionJob struct{}

func NewLogRetentionJob() *LogRetentionJob { return &LogRetentionJob{} }

func previewLogPathAllowed(path string) bool {
	if os.Getenv("VPNUI_LOCAL_PREVIEW") != "true" {
		return true
	}
	root := filepath.Clean(filepath.Dir(config.GetLogFolder()))
	target := filepath.Clean(path)
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !hasParentPrefix(rel)
}

func hasParentPrefix(path string) bool {
	return path == ".." || strings.HasPrefix(path, ".."+string(os.PathSeparator))
}

func (j *LogRetentionJob) Run() {
	active := []string{
		xray.GetIPLimitLogPath(),
		xray.GetIPLimitBannedLogPath(),
		os.Getenv("VPNUI_PREVIEW_LOG"),
	}
	if os.Getenv("VPNUI_LOCAL_PREVIEW") != "true" {
		active = append(active, "/var/log/pluto.log")
		if errorPath, err := xray.GetErrorLogPath(); err == nil && errorPath != "" && errorPath != "none" {
			active = append(active, errorPath)
		}
		for _, pattern := range []string{"/etc/vpn-ui-sstp/server-*/accel.log"} {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				logger.Warning("Failed to list module log files:", pattern, "-", err)
				continue
			}
			active = append(active, matches...)
		}
	}

	seen := make(map[string]bool, len(active))
	for _, path := range active {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) && !os.IsPermission(err) {
				logger.Warning("Failed to stat log file:", path, "-", err)
			}
			continue
		}
		if info.Size() <= logretention.MaxFileBytes {
			continue
		}
		if _, err := logretention.RotateIfTooLarge(path, logretention.MaxFileBytes, 5); err != nil {
			logger.Warning("Failed to rotate log file:", path, "-", err)
		}
	}

	inactive := []string{
		xray.GetIPLimitBannedPrevLogPath(),
		xray.GetAccessPersistentLogPath(),
		xray.GetAccessPersistentPrevLogPath(),
		xray.GetAccessPersistentPrev2LogPath(),
	}
	for _, path := range inactive {
		isAccessHistory := path == xray.GetAccessPersistentLogPath() ||
			path == xray.GetAccessPersistentPrevLogPath() || path == xray.GetAccessPersistentPrev2LogPath()
		if isAccessHistory {
			accessPersistentLogMu.Lock()
		}
		_, err := logretention.TrimIfTooLarge(path, logretention.MaxFileBytes)
		if isAccessHistory {
			accessPersistentLogMu.Unlock()
		}
		if err != nil {
			logger.Warning("Failed to cap archived log file:", path, "-", err)
		}
	}

	archiveBases := append(append([]string(nil), active...), inactive...)
	seenBackups := make(map[string]bool, len(archiveBases))
	for _, path := range archiveBases {
		if path == "" || seenBackups[path] {
			continue
		}
		seenBackups[path] = true
		if err := logretention.TrimNumberedBackups(path, 5, logretention.MaxFileBytes); err != nil {
			logger.Warning("Failed to cap numbered log backups:", path, "-", err)
		}
	}

	pattern := filepath.Join(config.GetBinFolderPath(), "core_crash_*.log")
	if err := logretention.KeepNewestFiles(pattern, 5); err != nil {
		logger.Warning("Failed to prune Xray crash reports:", err)
	}
}
