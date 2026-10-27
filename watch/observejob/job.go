// Package observejob records background jobs as they run.
//
// # A job correlates to itself, not to the request that queued it
//
// tasker.Manager.Dispatch has no hook, so enqueueing is not observable at
// all: this sees execution only. And a job runs on a worker goroutine in a
// different process, so even with a dispatch hook the two halves would need
// an id carried in the payload to meet.
//
// What that means in practice: a job entry stands on its own, and a request
// that dispatched three jobs shows nothing about them. Looking for a job in
// the request that queued it will not find it.
//
// Install it once at boot, in the worker process:
//
//	observejob.Watch(recorder)
//
// tasker.AddGlobalMiddleware is process-wide and additive, so this does not
// displace an application's own middleware. Use AddGlobalMiddleware rather
// than SetGlobalMiddlewares, which replaces.
package observejob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/tasker"
)

// Watcher is the name entries from this package carry.
const Watcher = "job"

// Watch records every job execution onto the recorder.
func Watch(recorder *observe.Recorder) {
	if recorder == nil {
		return
	}

	tasker.AddGlobalMiddleware(func(ctx context.Context, job tasker.Job,
		raw []byte, next func(ctx context.Context) error) error {

		// A job is its own unit of work, so it gets its own correlation id —
		// which is what makes everything it does, its queries included, group
		// under it. Unless the caller already established one, in which case
		// that wins: a job handler that calls observe.WithRequestID itself
		// knows better than this does.
		if observe.RequestIDFrom(ctx) == "" {
			ctx = observe.WithRequestID(ctx, observe.NewID())
		}

		// The Go type, which is what tasker's own middleware uses: Job is
		// one method and carries no name of its own.
		kind := fmt.Sprintf("%T", job)

		started := time.Now()
		err := next(ctx)

		entry := observe.Entry{
			Kind:     observe.KindJob,
			At:       started,
			Duration: time.Since(started),
			Level:    slog.LevelInfo,
			Summary:  "job " + kind,
			Watcher:  Watcher,
		}
		if data, jerr := json.Marshal(jobPayload{
			Kind: kind,
			// The length, never the payload. A job's payload is whatever the
			// application serialised into it, which routinely includes an
			// email address, a token, or a whole user.
			PayloadBytes: len(raw),
		}); jerr == nil {
			entry.Data = data
		}
		if err != nil {
			entry.Level = slog.LevelError
			entry.Err = err.Error()
		}

		recorder.Record(ctx, entry)
		return err
	})

	recorder.Watching(Watcher)
}

type jobPayload struct {
	Kind         string `json:"kind"`
	PayloadBytes int    `json:"payload_bytes"`
}
