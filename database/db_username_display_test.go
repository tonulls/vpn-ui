package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// legacyUsernameUser represents the users table before username_display existed.
type legacyUsernameUser struct {
	Id                int    `gorm:"primaryKey;autoIncrement"`
	Username          string `gorm:"uniqueIndex"`
	Password          string
	Nickname          string
	IsSuperAdmin      bool `gorm:"default:0"`
	Permissions       int  `gorm:"default:0"`
	IsReseller        bool `gorm:"default:0"`
	Enable            bool `gorm:"default:1"`
	SubscriptionLimit int  `gorm:"default:0"`
	TwoFactorEnable   bool `gorm:"default:0"`
	TwoFactorToken    string
}

func (legacyUsernameUser) TableName() string { return "users" }

func TestInitDBAddsDisplayUsernameWithoutChangingLegacyLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open legacy DB: %v", err)
	}
	if err := legacyDB.AutoMigrate(&legacyUsernameUser{}); err != nil {
		t.Fatalf("create legacy users table: %v", err)
	}
	legacy := &legacyUsernameUser{
		Username: "MiXeDLegacy", Password: "already-hashed", IsSuperAdmin: true, Enable: true,
	}
	if err := legacyDB.Create(legacy).Error; err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	sqlDB, err := legacyDB.DB()
	if err != nil {
		t.Fatalf("get legacy SQL DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close legacy SQL DB: %v", err)
	}

	if err := InitDB(path); err != nil {
		t.Fatalf("upgrade legacy DB: %v", err)
	}
	upgraded := &model.User{}
	if err := GetDB().First(upgraded, legacy.Id).Error; err != nil {
		t.Fatalf("read upgraded user: %v", err)
	}
	if upgraded.Username != "MiXeDLegacy" {
		t.Errorf("legacy login key = %q; want unchanged spelling", upgraded.Username)
	}
	if upgraded.DisplayUsername() != "MiXeDLegacy" {
		t.Errorf("legacy displayed username = %q; want fallback to stored spelling", upgraded.DisplayUsername())
	}
}
