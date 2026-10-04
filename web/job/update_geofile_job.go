package job

import (
	"context"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

const (
	// GeofileUpdateSchedule is how often the built-in geo data files are refreshed.
	//
	// Daily, because that is roughly how often the upstream rule sets publish and
	// there is nothing to gain from asking more than once per release. The transfer
	// itself is cheap when nothing changed: downloadGeofile issues a conditional GET
	// on the local mtime, so an unchanged file costs one 304 rather than 74MB.
	//
	// The cost of a tick is not zero, though - a file that DID change is followed by
	// an Xray restart, which drops every live connection. Once a day is the most
	// disruption this is worth.
	GeofileUpdateSchedule = "@every 24h"

	// GeofileUpdateRetryDelay spaces retries after a failed refresh. Geo assets
	// change infrequently, and retrying every minute only adds upstream load and
	// risks repeating an Xray interruption.
	GeofileUpdateRetryDelay = 4 * time.Hour

	// GeofileUpdateStartupDelay is when the first refresh runs after the panel starts.
	//
	// robfig/cron schedules an "@every" job one full interval AFTER the scheduler
	// starts, so with no startup run a host that reboots daily would never refresh at
	// all. Ten minutes rather than immediately: startTask is still bringing up every
	// protocol and restarting Xray, and this job ends in another restart, so it must
	// not land on top of that. It is also longer than the SSL job's five minutes on
	// purpose, so the two do not restart things at the same moment on every boot.
	GeofileUpdateStartupDelay = 10 * time.Minute
)

// UpdateGeofileJob refreshes the built-in geo data files when the operator has
// left auto-update on.
//
// Why this exists: routing rules that name a geosite/geoip category fail SILENTLY
// as the data ages. The file still parses and the core still starts, so nothing
// errors anywhere - the categories are simply months out of date and the rules
// quietly stop matching what the operator thinks they match.
type UpdateGeofileJob struct {
	serverService  service.ServerService
	settingService service.SettingService

	// update overrides the service call so a test can assert the gate without
	// reaching the network. Nil in production, so the zero-value job behaves like
	// every other job in this package.
	update func() error

	runMu           sync.Mutex
	retryMu         sync.Mutex
	ctx             context.Context
	retryTimer      *time.Timer
	retryDue        time.Time
	retryID         uint64
	lastRunAt       time.Time
	lastRunWasRetry bool
	retryDelay      time.Duration
}

// NewUpdateGeofileJob creates a new geo data refresh job.
func NewUpdateGeofileJob() *UpdateGeofileJob {
	return &UpdateGeofileJob{retryDelay: GeofileUpdateRetryDelay}
}

// SetContext binds delayed retries to the web server lifecycle, so a stopped
// server never fires a retry into its replacement instance.
func (j *UpdateGeofileJob) SetContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	j.retryMu.Lock()
	j.stopRetryLocked()
	j.ctx = ctx
	j.retryMu.Unlock()
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			j.cancelRetry()
		}()
	}
}

// Run refreshes the geo data files if auto-update is on, and does nothing at all
// otherwise. A failed attempt schedules one retry four hours later.
func (j *UpdateGeofileJob) Run() {
	j.run(false, time.Time{})
}

func (j *UpdateGeofileJob) run(isRetry bool, retryScheduledFor time.Time) {
	j.runMu.Lock()
	defer j.runMu.Unlock()

	j.retryMu.Lock()
	now := time.Now()
	if isRetry && (j.ctx == nil || j.ctx.Err() != nil) {
		j.retryMu.Unlock()
		return
	}
	if !isRetry && !j.retryDue.IsZero() && now.Before(j.retryDue) {
		due := j.retryDue
		j.retryMu.Unlock()
		logger.Debug("geofiles: skipping the regular refresh; a retry is scheduled for", due.Format(time.RFC3339))
		return
	}
	// The regular 24-hour schedule and the one-shot retry can fall on the same
	// instant. Whichever runs first owns that attempt; do not immediately repeat it.
	if !j.lastRunAt.IsZero() {
		if (isRetry && !j.lastRunAt.Before(retryScheduledFor)) ||
			(!isRetry && j.lastRunWasRetry && now.Sub(j.lastRunAt) < time.Minute) {
			j.retryMu.Unlock()
			return
		}
	}
	j.lastRunAt = now
	j.lastRunWasRetry = isRetry
	j.retryMu.Unlock()

	// The scheduler is built without cron.Recover (web.go), so a panic here takes
	// the whole panel down rather than just this tick. Worth guarding: this job
	// writes files the core parses at startup and can restart that core.
	defer func() {
		if r := recover(); r != nil {
			logger.Error("geofiles: the auto-update job panicked, the files were NOT refreshed:", r)
			j.scheduleRetry()
		}
	}()

	skipped, err := j.tick()
	switch {
	case err != nil:
		// Keep the current Xray process up on download failures; successful files
		// wait on disk until the complete retry succeeds.
		logger.Warning("geofiles: the scheduled refresh failed:", err)
		j.scheduleRetry()
	case skipped == "auto-update is off":
		logger.Debug("geofiles: skipping the scheduled refresh,", skipped)
		j.cancelRetry()
	case skipped != "":
		logger.Debug("geofiles: skipping the scheduled refresh,", skipped)
		j.scheduleRetry()
	default:
		logger.Info("geofiles: scheduled refresh completed")
		j.cancelRetry()
	}
}

func (j *UpdateGeofileJob) scheduleRetry() {
	j.retryMu.Lock()
	defer j.retryMu.Unlock()
	if j.ctx == nil || j.ctx.Err() != nil || j.retryTimer != nil {
		return
	}
	delay := j.retryDelay
	if delay <= 0 {
		delay = GeofileUpdateRetryDelay
	}
	j.retryDue = time.Now().Add(delay)
	j.retryID++
	id := j.retryID
	j.retryTimer = time.AfterFunc(delay, func() {
		j.retryMu.Lock()
		if j.retryID != id {
			j.retryMu.Unlock()
			return
		}
		j.retryTimer = nil
		scheduledFor := j.retryDue
		j.retryDue = time.Time{}
		ctx := j.ctx
		j.retryMu.Unlock()
		if ctx != nil && ctx.Err() == nil {
			j.run(true, scheduledFor)
		}
	})
	logger.Warningf("geofiles: next refresh attempt scheduled in %s", delay)
}

func (j *UpdateGeofileJob) cancelRetry() {
	j.retryMu.Lock()
	j.stopRetryLocked()
	j.retryMu.Unlock()
}

// stopRetryLocked invalidates an already-firing timer as well as a pending one.
func (j *UpdateGeofileJob) stopRetryLocked() {
	j.retryID++
	if j.retryTimer != nil {
		j.retryTimer.Stop()
		j.retryTimer = nil
	}
	j.retryDue = time.Time{}
}

// tick is Run without the logging, so the skip paths can be asserted in a test.
// A non-empty reason means nothing was attempted.
func (j *UpdateGeofileJob) tick() (skipped string, err error) {
	on, err := j.settingService.GetGeofileAutoUpdate()
	if err != nil {
		return "", err
	}
	if !on {
		return "auto-update is off", nil
	}

	// Never run on top of an operator who is updating by hand from the Geofiles
	// dialog. UpdateGeofile would serialize behind the per-file lock anyway, but the
	// operator's own run publishes the progress the overview is watching and a second
	// run would take that state out from under it.
	if st := j.serverService.GeofileRunState(); st.Running {
		return "an update started from the panel is already running", nil
	}

	return "", j.updateAll()
}

// updateAll is the one call into the service layer, indirected only so a test can
// stand in for it.
func (j *UpdateGeofileJob) updateAll() error {
	if j.update != nil {
		return j.update()
	}
	// "" means every built-in file. This restarts Xray on the way out, which is the
	// point: the core reads geo data at startup, so a refreshed file that nothing
	// reloads has changed nothing.
	return j.serverService.UpdateGeofile("")
}
