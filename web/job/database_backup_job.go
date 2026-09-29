package job

import (
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// DatabaseBackupJob polls settings at a short interval so changing its interval or
// enable switch applies without restarting the panel. The mutex prevents duplicate
// uploads if one Telegram request takes longer than the scheduler interval.
type DatabaseBackupJob struct {
	tgbotService   service.Tgbot
	settingService service.SettingService
	mu             sync.Mutex
	retryAfter     time.Time
}

func NewDatabaseBackupJob() *DatabaseBackupJob {
	return new(DatabaseBackupJob)
}

func (j *DatabaseBackupJob) Run() {
	j.mu.Lock()
	defer j.mu.Unlock()

	enabled, err := j.settingService.GetTgBackupEnable()
	if err != nil || !enabled {
		return
	}
	botEnabled, err := j.settingService.GetTgbotEnabled()
	if err != nil || !botEnabled {
		return
	}
	intervalHours, err := j.settingService.GetTgBackupIntervalHours()
	if err != nil || intervalHours < 1 || intervalHours > 8760 {
		logger.Warningf("Skip Telegram backup: invalid interval %d hours (%v)", intervalHours, err)
		return
	}
	lastSentAt, err := j.settingService.GetTgBackupLastSentAt()
	if err != nil {
		logger.Warningf("Skip Telegram backup: read last-sent time: %v", err)
		return
	}
	now := time.Now()
	if !databaseBackupDue(time.Unix(lastSentAt, 0), now, time.Duration(intervalHours)*time.Hour) || !databaseBackupRetryDue(j.retryAfter, now) {
		return
	}
	if err := j.tgbotService.SendScheduledDatabaseBackup(); err != nil {
		j.retryAfter = now.Add(15 * time.Minute)
		logger.Warningf("Telegram scheduled database backup failed; retry after %s: %v", j.retryAfter.Format(time.RFC3339), err)
		return
	}
	j.retryAfter = time.Time{}
	if err := j.settingService.SetTgBackupLastSentAt(now); err != nil {
		logger.Warningf("Telegram backup sent but last-sent time was not saved: %v", err)
	}
}

func databaseBackupDue(lastSentAt, now time.Time, interval time.Duration) bool {
	return lastSentAt.IsZero() || !now.Before(lastSentAt.Add(interval))
}

func databaseBackupRetryDue(retryAfter, now time.Time) bool {
	return retryAfter.IsZero() || !now.Before(retryAfter)
}
