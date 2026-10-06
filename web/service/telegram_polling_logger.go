package service

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
)

// Avoid filling the service log during a prolonged Telegram/network outage while
// still emitting occasional reminders that polling is retrying in the background.
const telegramPollingLogInterval = 15 * time.Minute

type telegramPollingLogger struct {
	mu       sync.Mutex
	lastPoll time.Time
	token    string
}

func newTelegramPollingLogger(token string) *telegramPollingLogger {
	return &telegramPollingLogger{token: token}
}

func (l *telegramPollingLogger) Debugf(string, ...any) {}

func (l *telegramPollingLogger) Errorf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if l.token != "" {
		message = strings.ReplaceAll(message, l.token, "BOT_TOKEN")
	}
	if strings.HasPrefix(message, "Retrying getting updates in") {
		return
	}
	if strings.Contains(message, "terminated by other getUpdates request") {
		if !l.shouldLogPollingError(time.Now()) {
			return
		}
		logger.Warningf("Telegram getUpdates conflict (HTTP 409): another poll for this bot token is active; retrying in the background: %s", message)
		return
	}
	if strings.HasPrefix(message, "Execution error getUpdates:") || strings.HasPrefix(message, "Getting updates:") {
		if !l.shouldLogPollingError(time.Now()) {
			return
		}
		logger.Warningf("Telegram API is unreachable; update polling continues in the background: %s", message)
		return
	}
	logger.Errorf("Telegram bot: %s", message)
}

func (l *telegramPollingLogger) shouldLogPollingError(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.lastPoll.IsZero() && now.Sub(l.lastPoll) < telegramPollingLogInterval {
		return false
	}
	l.lastPoll = now
	return true
}
