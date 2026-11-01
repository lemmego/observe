// Package observejob records background jobs — both halves.
//
// # An enqueue and an execution are two entries, and they are linked
//
// A job is queued in one process and run in another, so the two cannot share
// a correlation id the way a request and its queries do. What they share is
// the job row: Watch's dispatch middleware stamps the queueing request's id
// into Metadata, and the execution middleware reads it back off the row the
// worker puts on the context. So an execution entry names the request that
// queued it even though it happened minutes later in a different process,
// and a request view can show "this request queued three jobs".
//
// That needs tasker v0.2.1 or later. Before v0.2.0 there was no dispatch hook
// at all and enqueueing was invisible — which meant a job that was queued and
// never picked up looked exactly like a job nobody queued, and that is the
// difference between "no worker is running" and "the dispatch call is not
// being reached".
//
// Install it once at boot in every process that either queues or runs jobs:
//
//	observejob.Watch(recorder)
//
// tasker.AddGlobalMiddleware and tasker.AddDispatchMiddleware are both
// process-wide and additive, so this does not displace an application's own.
// Use them rather than their Set... counterparts, which replace.
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

// MetadataKey is the job-row metadata key the queueing request's id travels
// in.
//
// Exported because it is on the wire: it is written by one process and read
// by another, it is visible in the jobs table, and an application that
// inspects a row or writes its own dispatch middleware should not have to
// guess the string.
const MetadataKey = "observe_request_id"

// Watch records every enqueue and every job execution onto the recorder.
func Watch(recorder *observe.Recorder) {
	if recorder == nil {
		return
	}
	watchDispatch(recorder)
	watchExecution(recorder)
	recorder.Watching(Watcher)
}

// watchDispatch records the enqueue and stamps the row so the execution can
// find its way back.
func watchDispatch(recorder *observe.Recorder) {
	tasker.AddDispatchMiddleware(func(ctx context.Context, rows []*tasker.JobRow,
		next func(ctx context.Context) error) error {

		requestID := observe.RequestIDFrom(ctx)
		if requestID != "" {
			for _, row := range rows {
				if row.Metadata == nil {
					row.Metadata = map[string]string{}
				}
				// Not overwritten if something already set it: a caller
				// re-dispatching a row, or an application with its own
				// correlation scheme, knows better than this does.
				if row.Metadata[MetadataKey] == "" {
					row.Metadata[MetadataKey] = requestID
				}
			}
		}

		started := time.Now()
		err := next(ctx)

		// After next, because an enqueue that failed is the one most worth
		// seeing and the error is only known here.
		for _, row := range rows {
			entry := observe.Entry{
				Kind:     observe.KindJob,
				At:       started,
				Duration: time.Since(started),
				Level:    slog.LevelInfo,
				Summary:  "queued " + row.Kind,
				Watcher:  Watcher,
			}
			if err != nil {
				entry.Level = slog.LevelError
				entry.Err = err.Error()
			}
			if data, jerr := json.Marshal(jobPayload{
				Op:    "queued",
				Kind:  row.Kind,
				Queue: string(row.Queue),
				UUID:  row.UUID,
				// The length, never the payload. See the note in the
				// execution half.
				PayloadBytes: len(row.Payload),
			}); jerr == nil {
				entry.Data = data
			}
			recorder.Record(ctx, entry)
		}

		return err
	})
}

func watchExecution(recorder *observe.Recorder) {
	tasker.AddGlobalMiddleware(func(ctx context.Context, job tasker.Job,
		raw []byte, next func(ctx context.Context) error) error {

		// The id of the request that queued this, off the row the worker put
		// on the context. Read before anything replaces the context.
		queuedBy := ""
		var row *tasker.JobRow
		if executing, ok := tasker.RowFromContext(ctx); ok {
			row = executing
			queuedBy = executing.Metadata[MetadataKey]
		}

		// A job is its own unit of work, so it gets its own correlation id —
		// which is what makes everything it does, its queries included, group
		// under it. Unless the caller already established one, in which case
		// that wins: a job handler that calls observe.WithRequestID itself
		// knows better than this does.
		//
		// Deliberately not the queueing request's id. Reusing it would file
		// a worker's queries under a web request that finished minutes
		// earlier, which reads as one impossibly long request; the link is a
		// field on the entry instead.
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
		payload := jobPayload{
			Op:   "ran",
			Kind: kind,
			// The length, never the payload. A job's payload is whatever the
			// application serialised into it, which routinely includes an
			// email address, a token, or a whole user.
			PayloadBytes: len(raw),
			QueuedBy:     queuedBy,
		}
		if row != nil {
			payload.Queue = string(row.Queue)
			payload.UUID = row.UUID
			payload.Attempt = row.Attempt
		}
		if data, jerr := json.Marshal(payload); jerr == nil {
			entry.Data = data
		}
		if err != nil {
			entry.Level = slog.LevelError
			entry.Err = err.Error()
		}

		recorder.Record(ctx, entry)
		return err
	})
}

type jobPayload struct {
	// Op is "queued" or "ran". One kind rather than two, because both are
	// questions about the same job and a reader filtering the panel wants
	// them side by side; the field is what separates them.
	Op string `json:"op"`

	Kind         string `json:"kind"`
	Queue        string `json:"queue,omitempty"`
	UUID         string `json:"uuid,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	PayloadBytes int    `json:"payload_bytes"`

	// QueuedBy is the request id of the request that queued this job, when
	// there was one. It is the link across the process boundary, and it is a
	// field rather than the entry's own RequestID on purpose — see the note
	// in watchExecution.
	QueuedBy string `json:"queued_by,omitempty"`
}
