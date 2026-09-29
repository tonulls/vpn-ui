package job

import (
	"strconv"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/shirou/gopsutil/v4/cpu"
)

// CheckCpuJob samples CPU and emits a single alert when usage crosses the configured
// threshold upward. Calls are serialized so slow samples can never overlap.
type CheckCpuJob struct {
	tgbotService   service.Tgbot
	settingService service.SettingService
	mu             sync.Mutex
	wasAbove       bool
}

func NewCheckCpuJob() *CheckCpuJob {
	return new(CheckCpuJob)
}

func (j *CheckCpuJob) Run() {
	j.mu.Lock()
	defer j.mu.Unlock()

	botEnabled, err := j.settingService.GetTgbotEnabled()
	if err != nil || !botEnabled {
		j.wasAbove = false
		return
	}
	enabled, err := j.settingService.GetTgNotifyCPU()
	if err != nil || !enabled {
		j.wasAbove = false
		return
	}
	threshold, err := j.settingService.GetTgCpu()
	if err != nil || threshold < 1 || threshold > 100 {
		return
	}

	percentages, err := cpu.Percent(time.Second, false)
	if err != nil || len(percentages) == 0 {
		return
	}
	percent := percentages[0]
	notify, nowAbove := cpuAlertTransition(j.wasAbove, percent, float64(threshold))
	j.wasAbove = nowAbove
	if !notify {
		return
	}

	msg := j.tgbotService.I18nBot("tgbot.messages.cpuThreshold",
		"Percent=="+strconv.FormatFloat(percent, 'f', 2, 64),
		"Threshold=="+strconv.Itoa(threshold))
	j.tgbotService.NotifyCPUThreshold(msg)
}

func cpuAlertTransition(wasAbove bool, usage, threshold float64) (notify bool, nowAbove bool) {
	nowAbove = usage >= threshold
	return !wasAbove && nowAbove, nowAbove
}
