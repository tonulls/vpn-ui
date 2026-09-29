package service

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

type telegramEvent string

const (
	telegramEventLoginSuccess telegramEvent = "login-success"
	telegramEventLoginFailure telegramEvent = "login-failure"
	telegramEventCPU          telegramEvent = "cpu"
	telegramEventBackup       telegramEvent = "backup"
)

var forumTopicLocks sync.Map // map[telegramEvent]*sync.Mutex

func (t *Tgbot) UserLoginNotify(username, ip, loginTime string, status LoginStatus) {
	if username == "" || ip == "" || loginTime == "" {
		logger.Warning("UserLoginNotify skipped: username, IP, and time are required")
		return
	}

	event := telegramEventLoginSuccess
	message := t.I18nBot("tgbot.messages.loginSuccess")
	if status == LoginFail {
		event = telegramEventLoginFailure
		message = t.I18nBot("tgbot.messages.loginFailed")
	}
	message += t.I18nBot("tgbot.messages.hostname", "Hostname=="+html.EscapeString(hostname))
	message += t.I18nBot("tgbot.messages.username", "Username=="+html.EscapeString(username))
	message += t.I18nBot("tgbot.messages.ip", "IP=="+html.EscapeString(ip))
	message += t.I18nBot("tgbot.messages.time", "Time=="+html.EscapeString(loginTime))
	t.notifyEvent(event, message)
}

func (t *Tgbot) NotifyCPUThreshold(message string) {
	t.notifyEvent(telegramEventCPU, message)
}

func (t *Tgbot) notifyEvent(event telegramEvent, message string) {
	if message == "" || !t.IsRunning() {
		return
	}
	botEnabled, err := t.settingService.GetTgbotEnabled()
	if err != nil || !botEnabled {
		return
	}
	eventEnabled, err := t.eventEnabled(event)
	if err != nil || !eventEnabled {
		return
	}

	sendDirect, err := t.settingService.GetTgNotifyDirect()
	if err == nil && sendDirect {
		t.sendEventToAdmins(message)
	}

	sendForum, err := t.settingService.GetTgNotifyForum()
	if err != nil || !sendForum {
		return
	}
	forumEnabled, err := t.settingService.GetTgForumEnable()
	if err != nil || !forumEnabled {
		return
	}

	chatID, err := t.forumChatID()
	if err != nil {
		logger.Warningf("Telegram forum notification skipped: %v", err)
		return
	}
	topicID, err := t.getOrCreateForumTopic(event, chatID)
	if err != nil {
		logger.Warningf("Telegram forum topic for %q unavailable: %v", event, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:          tu.ID(chatID),
		MessageThreadID: topicID,
		Text:            message,
		ParseMode:       "HTML",
	})
	if err != nil {
		logger.Warningf("Send forum notification %q failed: %v", event, err)
	}
}

func (t *Tgbot) eventEnabled(event telegramEvent) (bool, error) {
	switch event {
	case telegramEventLoginSuccess:
		return t.settingService.GetTgNotifyLoginSuccess()
	case telegramEventLoginFailure:
		return t.settingService.GetTgNotifyLoginFailure()
	case telegramEventCPU:
		return t.settingService.GetTgNotifyCPU()
	default:
		return false, fmt.Errorf("unknown Telegram event %q", event)
	}
}

func (t *Tgbot) sendEventToAdmins(message string) {
	recipients, err := t.settingService.GetTgBotChatId()
	if err != nil {
		logger.Warningf("Read Telegram private notification recipients: %v", err)
		return
	}
	for _, rawID := range strings.Split(recipients, ",") {
		rawID = strings.TrimSpace(rawID)
		if rawID == "" {
			continue
		}
		chatID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			logger.Warningf("Invalid Telegram private chat ID %q: %v", rawID, err)
			continue
		}
		t.SendMsgToTgbot(chatID, message)
	}
}

func (t *Tgbot) forumChatID() (int64, error) {
	chatIDText, err := t.settingService.GetTgForumChatId()
	if err != nil {
		return 0, err
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(chatIDText), 10, 64)
	if err != nil || chatID >= 0 || !strings.HasPrefix(strconv.FormatInt(-chatID, 10), "100") {
		return 0, fmt.Errorf("invalid forum supergroup chat ID")
	}
	return chatID, nil
}

func (t *Tgbot) getOrCreateForumTopic(event telegramEvent, chatID int64) (int, error) {
	getTopicID, setTopicID, topicName, err := t.forumTopicSetting(event)
	if err != nil {
		return 0, err
	}
	storedID, err := getTopicID()
	if err != nil {
		return 0, err
	}
	if id, err := parseForumTopicID(storedID); err == nil && id > 0 {
		return id, nil
	} else if err != nil {
		return 0, err
	}

	lockValue, _ := forumTopicLocks.LoadOrStore(event, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	storedID, err = getTopicID()
	if err != nil {
		return 0, err
	}
	if id, err := parseForumTopicID(storedID); err == nil && id > 0 {
		return id, nil
	} else if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	topic, err := bot.CreateForumTopic(ctx, &telego.CreateForumTopicParams{
		ChatID: tu.ID(chatID),
		Name:   topicName,
	})
	if err != nil {
		return 0, err
	}
	if topic == nil || topic.MessageThreadID <= 0 {
		return 0, fmt.Errorf("Telegram returned an invalid message thread ID")
	}
	id := strconv.Itoa(topic.MessageThreadID)
	if err := setTopicID(id); err != nil {
		return 0, fmt.Errorf("persist created topic ID: %w", err)
	}
	return topic.MessageThreadID, nil
}

func (t *Tgbot) forumTopicSetting(event telegramEvent) (func() (string, error), func(string) error, string, error) {
	switch event {
	case telegramEventLoginSuccess:
		return t.settingService.GetTgTopicLoginSuccess, t.settingService.SetTgTopicLoginSuccess, "Успешные входы", nil
	case telegramEventLoginFailure:
		return t.settingService.GetTgTopicLoginFailure, t.settingService.SetTgTopicLoginFailure, "Неуспешные входы", nil
	case telegramEventCPU:
		return t.settingService.GetTgTopicCPU, t.settingService.SetTgTopicCPU, "Нагрузка на ЦП", nil
	case telegramEventBackup:
		return t.settingService.GetTgBackupTopicId, t.settingService.SetTgBackupTopicId, "Резервные копии", nil
	default:
		return nil, nil, "", fmt.Errorf("unknown Telegram event %q", event)
	}
}

func parseForumTopicID(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("forum topic ID must be a positive integer")
	}
	return id, nil
}
