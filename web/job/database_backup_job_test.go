package job

import (
	"testing"
	"time"
)

func TestDatabaseBackupDue(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	interval := 24 * time.Hour
	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{name: "first backup", last: time.Time{}, want: true},
		{name: "interval not reached", last: now.Add(-23 * time.Hour), want: false},
		{name: "interval reached", last: now.Add(-24 * time.Hour), want: true},
		{name: "overdue", last: now.Add(-26 * time.Hour), want: true},
		{name: "clock moved backward", last: now.Add(time.Hour), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := databaseBackupDue(tt.last, now, interval); got != tt.want {
				t.Fatalf("databaseBackupDue(%v, %v, %v) = %v, want %v", tt.last, now, interval, got, tt.want)
			}
		})
	}
}

func TestDatabaseBackupRetryDue(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	retryAfter := now.Add(15 * time.Minute)
	if databaseBackupRetryDue(retryAfter, now) {
		t.Fatal("backup retry should wait until the backoff expires")
	}
	if !databaseBackupRetryDue(retryAfter, retryAfter) {
		t.Fatal("backup retry should run when the backoff expires")
	}
	if !databaseBackupRetryDue(time.Time{}, now) {
		t.Fatal("no pending retry should allow an attempt")
	}
}
