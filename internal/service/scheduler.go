package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

// The unified scheduler owns every recurring background job. Features register
// a ScheduledJob instead of spawning their own ticker goroutine, which gives
// one place for the admin UI to list jobs, trigger them manually, and read a
// shared execution history (model.ScheduledTaskRun in the log database).
//
// A job's Run reports didWork=false for scans that found nothing due; those
// still refresh the in-memory state but write no run-log row, so
// high-frequency scans (10-30s) do not flood the log database.

const (
	ScheduledTaskTriggerSchedule = "schedule"
	ScheduledTaskTriggerManual   = "manual"

	ScheduledTaskStatusSuccess = "success"
	ScheduledTaskStatusFailed  = "failed"

	schedulerTickInterval = 5 * time.Second
	scheduledRunTimeout   = 30 * time.Minute
)

var (
	ErrScheduledJobNotFound    = errors.New("scheduled job not found")
	ErrScheduledJobRunning     = errors.New("scheduled job is already running")
	ErrScheduledJobPrimaryOnly = errors.New("this job only runs on the primary node")
)

type ScheduledJob struct {
	// Name is the stable identifier used in the API and run logs.
	Name        string
	Description string
	// PrimaryOnly jobs are skipped (and cannot be triggered) on replica nodes.
	PrimaryOnly bool
	// Interval returns the current scan cadence; it is re-evaluated after every
	// run so setting changes apply without a restart.
	Interval func() time.Duration
	// Enabled gates scheduled runs (manual triggers bypass it). nil means
	// always enabled.
	Enabled func() bool
	Run     func(ctx context.Context) (message string, didWork bool, err error)
}

type scheduledJobState struct {
	job            ScheduledJob
	running        bool
	nextRunAt      time.Time
	lastRunAt      *time.Time
	lastStatus     string
	lastMessage    string
	lastDurationMs int64
}

type ScheduledJobSnapshot struct {
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	PrimaryOnly     bool       `json:"primary_only"`
	Enabled         bool       `json:"enabled"`
	Running         bool       `json:"running"`
	IntervalSeconds int64      `json:"interval_seconds"`
	NextRunAt       *time.Time `json:"next_run_at"`
	LastRunAt       *time.Time `json:"last_run_at"`
	LastStatus      string     `json:"last_status"`
	LastMessage     string     `json:"last_message"`
	LastDurationMs  int64      `json:"last_duration_ms"`
}

var scheduler struct {
	sync.Mutex
	jobs    map[string]*scheduledJobState
	started bool
}

// RegisterScheduledJob adds a job to the unified scheduler. Registration is
// idempotent by name; late registrations (after StartScheduler) are picked up
// on the next tick.
func RegisterScheduledJob(job ScheduledJob) {
	if job.Name == "" || job.Run == nil || job.Interval == nil {
		log.Printf("ignoring invalid scheduled job registration: %q", job.Name)
		return
	}
	scheduler.Lock()
	defer scheduler.Unlock()
	if scheduler.jobs == nil {
		scheduler.jobs = make(map[string]*scheduledJobState)
	}
	if _, exists := scheduler.jobs[job.Name]; exists {
		return
	}
	scheduler.jobs[job.Name] = &scheduledJobState{
		job: job,
		// First run happens shortly after startup rather than one full interval
		// later, matching the run-then-tick behavior of the old loops.
		nextRunAt: time.Now().Add(schedulerTickInterval),
	}
}

// StartScheduler launches the single scan loop. Safe to call once from app
// startup; subsequent calls are no-ops.
func StartScheduler() {
	scheduler.Lock()
	if scheduler.started {
		scheduler.Unlock()
		return
	}
	scheduler.started = true
	scheduler.Unlock()

	go func() {
		ticker := time.NewTicker(schedulerTickInterval)
		defer ticker.Stop()
		for range ticker.C {
			dispatchDueScheduledJobs()
		}
	}()
}

func dispatchDueScheduledJobs() {
	now := time.Now()
	scheduler.Lock()
	due := make([]*scheduledJobState, 0)
	for _, state := range scheduler.jobs {
		if state.running || now.Before(state.nextRunAt) {
			continue
		}
		state.nextRunAt = now.Add(state.job.Interval())
		if state.job.PrimaryOnly && !IsPrimaryNode() {
			continue
		}
		if state.job.Enabled != nil && !state.job.Enabled() {
			continue
		}
		state.running = true
		due = append(due, state)
	}
	scheduler.Unlock()

	for _, state := range due {
		go executeScheduledJob(state, ScheduledTaskTriggerSchedule)
	}
}

// TriggerScheduledJob runs a job immediately (bypassing its Enabled gate) in
// response to an admin request.
func TriggerScheduledJob(name string) error {
	scheduler.Lock()
	state, ok := scheduler.jobs[name]
	if !ok {
		scheduler.Unlock()
		return ErrScheduledJobNotFound
	}
	if state.job.PrimaryOnly && !IsPrimaryNode() {
		scheduler.Unlock()
		return ErrScheduledJobPrimaryOnly
	}
	if state.running {
		scheduler.Unlock()
		return ErrScheduledJobRunning
	}
	state.running = true
	state.nextRunAt = time.Now().Add(state.job.Interval())
	scheduler.Unlock()

	go executeScheduledJob(state, ScheduledTaskTriggerManual)
	return nil
}

func executeScheduledJob(state *scheduledJobState, trigger string) {
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), scheduledRunTimeout)
	defer cancel()

	var message string
	var didWork bool
	var runErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				runErr = fmt.Errorf("panic: %v", recovered)
			}
		}()
		message, didWork, runErr = state.job.Run(ctx)
	}()
	duration := time.Since(startedAt)

	status := ScheduledTaskStatusSuccess
	if runErr != nil {
		status = ScheduledTaskStatusFailed
		if message == "" {
			message = runErr.Error()
		}
		log.Printf("scheduled job %q failed: %v", state.job.Name, runErr)
	}

	scheduler.Lock()
	state.running = false
	if didWork || runErr != nil || trigger == ScheduledTaskTriggerManual {
		ranAt := startedAt
		state.lastRunAt = &ranAt
		state.lastStatus = status
		state.lastMessage = message
		state.lastDurationMs = duration.Milliseconds()
	}
	scheduler.Unlock()

	// Idle scans leave no trace beyond the in-memory state.
	if !didWork && runErr == nil && trigger != ScheduledTaskTriggerManual {
		return
	}
	run := model.ScheduledTaskRun{
		ID:          model.NextLogID(),
		TaskName:    state.job.Name,
		Status:      status,
		TriggerType: trigger,
		NodeName:    CurrentNodeName(),
		Message:     truncateAuditValue(message, 1000),
		DurationMs:  duration.Milliseconds(),
		StartedAt:   startedAt,
	}
	if db, err := model.LogDB(); err == nil {
		if err := db.Create(&run).Error; err != nil {
			log.Printf("failed to record scheduled task run for %q: %v", state.job.Name, err)
		}
	}
}

// ScheduledJobSnapshots returns the current registry state sorted by name for
// the admin API.
func ScheduledJobSnapshots() []ScheduledJobSnapshot {
	scheduler.Lock()
	defer scheduler.Unlock()
	snapshots := make([]ScheduledJobSnapshot, 0, len(scheduler.jobs))
	for _, state := range scheduler.jobs {
		enabled := true
		if state.job.Enabled != nil {
			enabled = state.job.Enabled()
		}
		nextRunAt := state.nextRunAt
		snapshot := ScheduledJobSnapshot{
			Name:            state.job.Name,
			Description:     state.job.Description,
			PrimaryOnly:     state.job.PrimaryOnly,
			Enabled:         enabled,
			Running:         state.running,
			IntervalSeconds: int64(state.job.Interval() / time.Second),
			NextRunAt:       &nextRunAt,
			LastRunAt:       state.lastRunAt,
			LastStatus:      state.lastStatus,
			LastMessage:     state.lastMessage,
			LastDurationMs:  state.lastDurationMs,
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(left, right int) bool { return snapshots[left].Name < snapshots[right].Name })
	return snapshots
}
