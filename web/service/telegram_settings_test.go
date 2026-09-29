package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
)

func TestTelegramNotificationAndBackupDefaults(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "telegram-settings.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	settings, err := (&SettingService{}).GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting: %v", err)
	}
	if !settings.TgNotifyDirect {
		t.Fatal("private-chat notifications must default on")
	}
	if settings.TgNotifyForum || settings.TgForumEnable {
		t.Fatal("forum delivery and forum destination must default off")
	}
	if !settings.TgNotifyLoginSuccess || !settings.TgNotifyLoginFailure {
		t.Fatal("login event switches must default on")
	}
	if settings.TgNotifyCPU || settings.TgCpu != 80 {
		t.Fatalf("CPU defaults = enabled:%v threshold:%d, want disabled:80", settings.TgNotifyCPU, settings.TgCpu)
	}
	if settings.TgBackupEnable || settings.TgBackupIntervalHours != 24 || settings.TgBackupEncrypt {
		t.Fatalf("backup defaults = enabled:%v interval:%d encrypted:%v, want disabled:24h:plain", settings.TgBackupEnable, settings.TgBackupIntervalHours, settings.TgBackupEncrypt)
	}
}

func TestTelegramSettingsSaveAndReload(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telegram-roundtrip.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	s := &SettingService{}
	settings, err := s.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting: %v", err)
	}
	settings.TgForumEnable = true
	settings.TgForumChatId = "1001234567890"
	settings.TgNotifyDirect = false
	settings.TgNotifyForum = true
	settings.TgNotifyLoginSuccess = false
	settings.TgNotifyLoginFailure = true
	settings.TgNotifyCPU = true
	settings.TgCpu = 83
	settings.TgTopicLoginSuccess = "11"
	settings.TgTopicLoginFailure = "12"
	settings.TgTopicCPU = "13"
	settings.TgBackupEnable = true
	settings.TgBackupIntervalHours = 48
	settings.TgBackupTopicId = "14"
	settings.TgBackupEncrypt = true
	settings.TgBackupPassword = "roundtrip-test-secret-do-not-export"
	if err := s.UpdateAllSetting(settings); err != nil {
		t.Fatalf("UpdateAllSetting: %v", err)
	}
	if err := database.CloseDB(); err != nil {
		t.Fatalf("CloseDB before persistence check: %v", err)
	}
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("reopen saved database: %v", err)
	}

	settings, err = s.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting after save: %v", err)
	}
	if settings.TgForumChatId != "-1001234567890" {
		t.Errorf("normalized forum ID = %q", settings.TgForumChatId)
	}
	if settings.TgNotifyDirect || !settings.TgNotifyForum || settings.TgNotifyLoginSuccess || !settings.TgNotifyLoginFailure {
		t.Errorf("notification switches did not round-trip: %+v", settings)
	}
	if !settings.TgNotifyCPU || settings.TgCpu != 83 {
		t.Errorf("CPU settings = enabled:%v threshold:%d, want enabled:83", settings.TgNotifyCPU, settings.TgCpu)
	}
	if !settings.TgBackupEnable || settings.TgBackupIntervalHours != 48 || !settings.TgBackupEncrypt {
		t.Errorf("backup settings did not round-trip: enabled:%v interval:%d encrypted:%v", settings.TgBackupEnable, settings.TgBackupIntervalHours, settings.TgBackupEncrypt)
	}
	if settings.TgBackupPassword != redactedBackupPassword {
		t.Errorf("API did not mask backup password: %q", settings.TgBackupPassword)
	}
	storedPassword, err := s.GetTgBackupPassword()
	if err != nil || storedPassword != "roundtrip-test-secret-do-not-export" {
		t.Errorf("stored backup password = %q, err=%v", storedPassword, err)
	}

	// New topic IDs entered together with a destination change should survive Save.
	if settings.TgTopicLoginSuccess != "11" || settings.TgTopicLoginFailure != "12" ||
		settings.TgTopicCPU != "13" || settings.TgBackupTopicId != "14" {
		t.Errorf("topic IDs did not persist: login-success=%q login-failure=%q CPU=%q backup=%q",
			settings.TgTopicLoginSuccess, settings.TgTopicLoginFailure, settings.TgTopicCPU, settings.TgBackupTopicId)
	}

	// When only the forum destination changes, carried-over old thread IDs must
	// clear so future notifications do not target the previous group.
	settings.TgForumChatId = "-1009876543210"
	if err := s.UpdateAllSetting(settings); err != nil {
		t.Fatalf("UpdateAllSetting with a new forum destination: %v", err)
	}
	settings, err = s.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting after destination change: %v", err)
	}
	if settings.TgTopicLoginSuccess != "" || settings.TgTopicLoginFailure != "" ||
		settings.TgTopicCPU != "" || settings.TgBackupTopicId != "" {
		t.Errorf("old forum topic IDs were not cleared after destination change: %+v", settings)
	}
}

func TestBackupSnapshotOmitsPasswordAndDisablesItsOwnSchedule(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backup-snapshot.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	s := &SettingService{}
	settings, err := s.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting: %v", err)
	}
	settings.TgForumEnable = true
	settings.TgForumChatId = "-1001234567890"
	settings.TgBackupEnable = true
	settings.TgBackupEncrypt = true
	settings.TgBackupPassword = "snapshot-test-secret-do-not-export"
	if err := s.UpdateAllSetting(settings); err != nil {
		t.Fatalf("UpdateAllSetting: %v", err)
	}
	liveTemplate, err := s.GetXrayConfigTemplate()
	if err != nil {
		t.Fatalf("GetXrayConfigTemplate: %v", err)
	}
	if err := s.setString("xrayTemplateConfig", liveTemplate); err != nil {
		t.Fatalf("persist Xray template: %v", err)
	}

	snapshot, err := snapshotDatabaseForTelegramBackup()
	if err != nil {
		t.Fatalf("snapshotDatabaseForTelegramBackup: %v", err)
	}
	if bytes.Contains(snapshot, []byte("snapshot-test-secret-do-not-export")) {
		t.Fatal("database snapshot contains the archive password")
	}
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(snapshotPath, snapshot, 0o600); err != nil {
		t.Fatalf("WriteFile(snapshot): %v", err)
	}
	for key, want := range map[string]string{"tgBackupEnable": "false", "tgBackupEncrypt": "false", "tgBackupPassword": ""} {
		got, err := database.GetSettingValue(snapshotPath, key)
		if err != nil {
			t.Fatalf("GetSettingValue(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("snapshot %s = %q, want %q", key, got, want)
		}
	}
	snapshotTemplate, err := database.GetSettingValue(snapshotPath, "xrayTemplateConfig")
	if err != nil {
		t.Fatalf("snapshot xrayTemplateConfig: %v", err)
	}
	if snapshotTemplate != liveTemplate {
		t.Fatal("snapshot did not preserve the Xray template")
	}
}

func TestBackupPasswordIsWriteOnlyAndSurvivesUnrelatedSettingsUpdate(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "telegram-password.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	s := &SettingService{}
	settings, err := s.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting: %v", err)
	}
	settings.TgBackupPassword = "replace-with-non-production-test-secret"
	if err := s.UpdateAllSetting(settings); err != nil {
		t.Fatalf("UpdateAllSetting with new archive password: %v", err)
	}

	settings, err = s.GetAllSetting()
	if err != nil {
		t.Fatalf("GetAllSetting after save: %v", err)
	}
	if settings.TgBackupPassword != redactedBackupPassword {
		t.Fatalf("password API value = %q, want masked marker", settings.TgBackupPassword)
	}
	settings.TgBotToken = "updated-token"
	if err := s.UpdateAllSetting(settings); err != nil {
		t.Fatalf("unrelated UpdateAllSetting: %v", err)
	}
	storedPassword, err := s.GetTgBackupPassword()
	if err != nil {
		t.Fatalf("GetTgBackupPassword: %v", err)
	}
	if storedPassword != "replace-with-non-production-test-secret" {
		t.Fatal("unrelated settings save overwrote the write-only backup password")
	}
}
