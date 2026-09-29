package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mhsanaei/3x-ui/v2/config"
	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mymmrac/telego/telegoutil"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SendScheduledDatabaseBackup sends one consistent, database-only archive to the
// configured forum backup topic. Scheduling and the last-success timestamp belong
// to the job layer.
func (t *Tgbot) SendScheduledDatabaseBackup() error {
	backupEnabled, err := t.settingService.GetTgBackupEnable()
	if err != nil || !backupEnabled {
		return fmt.Errorf("scheduled backup is disabled")
	}
	return t.SendDatabaseBackupArchive()
}

// SendDatabaseBackupArchive performs one on-demand archive send, irrespective of
// the schedule switch. It still requires the Telegram bot and forum destination.
func (t *Tgbot) SendDatabaseBackupArchive() error {
	if !t.IsRunning() || bot == nil {
		return fmt.Errorf("Telegram bot is not running")
	}
	enabled, err := t.settingService.GetTgbotEnabled()
	if err != nil || !enabled {
		return fmt.Errorf("Telegram bot is disabled")
	}
	forumEnabled, err := t.settingService.GetTgForumEnable()
	if err != nil || !forumEnabled {
		return fmt.Errorf("Telegram forum is disabled")
	}
	chatID, err := t.forumChatID()
	if err != nil {
		return err
	}

	databaseBytes, err := snapshotDatabaseForTelegramBackup()
	if err != nil {
		return fmt.Errorf("create consistent database snapshot: %w", err)
	}
	password := ""
	encrypt, err := t.settingService.GetTgBackupEncrypt()
	if err != nil {
		return err
	}
	if encrypt {
		password, err = t.settingService.GetTgBackupPassword()
		if err != nil {
			return err
		}
		if password == "" {
			return fmt.Errorf("archive encryption is enabled but no password is configured")
		}
	}
	backupIntervalHours, err := t.settingService.GetTgBackupIntervalHours()
	if err != nil {
		return err
	}
	createdAt := time.Now().UTC()
	archive, _, err := CreateDatabaseBackupArchive(databaseBytes, DatabaseBackupArchiveOptions{
		AppVersion:          config.GetVersion(),
		CreatedAt:           createdAt,
		BackupIntervalHours: backupIntervalHours,
		Password:            password,
	})
	if err != nil {
		return fmt.Errorf("create database backup archive: %w", err)
	}
	if len(archive) > MaxDatabaseBackupArchiveBytes {
		return fmt.Errorf("database backup archive exceeds the configured size limit")
	}

	topicID, err := t.getOrCreateForumTopic(telegramEventBackup, chatID)
	if err != nil {
		return fmt.Errorf("get backup forum topic: %w", err)
	}
	name := "vpn-ui-backup-" + createdAt.Format("20060102-150405") + ".zip"
	if encrypt {
		name += ".enc"
	}
	document := telegoutil.Document(
		telegoutil.ID(chatID),
		telegoutil.FileFromBytes(archive, name),
	)
	document.MessageThreadID = topicID
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := bot.SendDocument(ctx, document); err != nil {
		return fmt.Errorf("send database backup to Telegram forum: %w", err)
	}
	return nil
}

// snapshotDatabaseForTelegramBackup uses SQLite VACUUM INTO, which incorporates
// committed WAL content into a consistent snapshot while the panel remains live.
// It then removes only the write-only backup password and turns off automatic
// backup/encryption in the copy, so restoring the archive cannot unexpectedly
// start sending unencrypted backups or restore the archive's own password.
func snapshotDatabaseForTelegramBackup() ([]byte, error) {
	liveDB := database.GetDB()
	if liveDB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	tempDir, err := os.MkdirTemp("", "vpn-ui-tg-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)
	snapshotPath := filepath.Join(tempDir, "vpn-ui.db")
	if err := liveDB.Exec("VACUUM INTO ?", snapshotPath).Error; err != nil {
		return nil, fmt.Errorf("SQLite VACUUM INTO failed: %w", err)
	}
	if err := os.Chmod(snapshotPath, 0o600); err != nil {
		return nil, err
	}

	snapshotDB, err := gorm.Open(sqlite.Open(snapshotPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		return nil, fmt.Errorf("open staged database snapshot: %w", err)
	}
	if err := snapshotDB.Exec("DELETE FROM settings WHERE key = ?", "tgBackupPassword").Error; err != nil {
		closeDatabaseQuietly(snapshotDB)
		return nil, fmt.Errorf("remove archive password from snapshot: %w", err)
	}
	if err := snapshotDB.Exec("UPDATE settings SET value = 'false' WHERE key IN (?, ?)", "tgBackupEnable", "tgBackupEncrypt").Error; err != nil {
		closeDatabaseQuietly(snapshotDB)
		return nil, fmt.Errorf("disable automatic backup in snapshot: %w", err)
	}
	// Compact the staged database after deleting the password. A plain DELETE can
	// leave old cell bytes in free pages even though the setting row is gone.
	if err := snapshotDB.Exec("VACUUM").Error; err != nil {
		closeDatabaseQuietly(snapshotDB)
		return nil, fmt.Errorf("compact sanitized database snapshot: %w", err)
	}
	if err := snapshotDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		closeDatabaseQuietly(snapshotDB)
		return nil, fmt.Errorf("checkpoint sanitized database snapshot: %w", err)
	}
	if err := closeDatabase(snapshotDB); err != nil {
		return nil, fmt.Errorf("close staged database snapshot: %w", err)
	}
	if err := database.ValidateSQLiteDB(snapshotPath); err != nil {
		return nil, fmt.Errorf("validate staged database snapshot: %w", err)
	}
	info, err := os.Stat(snapshotPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxDatabaseBackupDatabaseBytes {
		return nil, fmt.Errorf("database snapshot exceeds the %d-byte size limit", MaxDatabaseBackupDatabaseBytes)
	}
	return os.ReadFile(snapshotPath)
}

func closeDatabase(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func closeDatabaseQuietly(db *gorm.DB) {
	if err := closeDatabase(db); err != nil {
		fmt.Fprintf(os.Stderr, "close temporary backup database: %v\n", err)
	}
}
