// Package observeschedule records scheduled runs.
//
// Separate from observejob because a scheduled run and the job it queues
// answer different questions. The queue says what work exists; the schedule
// says whether 4:20 came round, whether it came round twice, and how long it
// has been failing. And a run that **failed to enqueue** leaves no job row at
// all, so without this it did not happen as far as the queue is concerned —
// which is the failure mode worth having a panel for.
//
// Install it on the scheduler, in the process that runs it — which is one
// process by design, started by `lemmego run tasker:schedule`:
//
//	observeschedule.Watch(sched, recorder)
//
// Needs tasker v0.2.0 or later, where Scheduler.Observe landed. Registration
// is additive and per-scheduler.
package observeschedule

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lemmego/observe"
	"github.com/lemmego/tasker/scheduler"
)

// Watcher is the name entries from this package carry.
const Watcher = "schedule"

type schedulePayload struct {
	// ID and Schedule are the registration rather than the job type,
	// because several entries can schedule the same job and the question is
	// about the entry.
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	Queue    string `json:"queue,omitempty"`
	Job      string `json:"job,omitempty"`
}

// Watch records every scheduled run onto the recorder.
func Watch(sched *scheduler.Scheduler, recorder *observe.Recorder) {
	if sched == nil || recorder == nil {
		return
	}

	sched.Observe(func(ctx context.Context, run scheduler.Run) {
		// Its own correlation id, for the same reason a job execution gets
		// one: a scheduled firing is its own unit of work, and anything it
		// causes should group under it rather than under nothing.
		if observe.RequestIDFrom(ctx) == "" {
			ctx = observe.WithRequestID(ctx, observe.NewID())
		}

		entry := observe.Entry{
			Kind:     observe.KindSchedule,
			At:       run.At,
			Duration: run.Duration,
			Level:    slog.LevelInfo,
			Summary:  "scheduled " + run.ID,
			Watcher:  Watcher,
		}
		if run.Err != nil {
			entry.Level = slog.LevelError
			entry.Err = run.Err.Error()
		}

		payload := schedulePayload{
			ID: run.ID, Schedule: run.Schedule, Queue: string(run.Queue),
		}
		if run.Job != nil {
			payload.Job = fmt.Sprintf("%T", run.Job)
		}
		if data, err := json.Marshal(payload); err == nil {
			entry.Data = data
		}

		recorder.Record(ctx, entry)
	})

	recorder.Watching(Watcher)
}
