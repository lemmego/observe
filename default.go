package observe

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// The default recorder exists because sql.Register runs at package
// initialisation, long before an application has built anything. A wrapped
// driver therefore cannot be handed a recorder; it has to find one.
//
// This is slog's arrangement and it is the right one here for the same
// reason: the thing doing the recording is deep in a call stack that has no
// business knowing about the application's wiring.
var defaultRecorder atomic.Pointer[Recorder]

// SetDefault installs the recorder that [Wrap] and [RecordQuery] use.
//
// Called by observe/provider during boot. Statements executed before this
// are not recorded, which is ordinary: the connection pool opens before the
// recorder exists.
func SetDefault(r *Recorder) { defaultRecorder.Store(r) }

// Default returns the installed recorder, or nil when there is none.
//
// A nil *Recorder is usable and records nothing, so a caller does not have to
// check.
func Default() *Recorder { return defaultRecorder.Load() }

// RecordQuery records one statement against a store this package knows
// nothing about.
//
// This is the whole answer to "what about an application that does not use
// our database stack". [Wrap] covers everything reachable through
// database/sql, which is most things; this covers the rest — MongoDB, native
// pgx, a bespoke store — in one line, and the entry it produces is treated
// identically by the dashboard and by the MCP tools. There is no second-class
// tier.
//
//	started := time.Now()
//	cur, err := coll.Find(ctx, filter)
//	observe.RecordQuery(ctx, "mongo", "users.find", args, time.Since(started), err)
//
// Pass the watcher name you want the entry attributed to, so a reader can
// tell where it came from. Arguments are redacted before they are stored.
func RecordQuery(ctx context.Context, watcher, statement string, args []any, took time.Duration, err error) {
	rec := Default()
	// Asked before anything is rendered or marshalled. Redacting every bound
	// parameter for a statement that is about to be sampled away was the
	// expensive half of this path.
	//
	// Only for a statement that worked: a failed one is kept whatever the
	// rate, so asking would lose it.
	if err == nil && !rec.ShouldRecord(ctx) {
		return
	}
	if watcher == "" {
		watcher = "custom"
	}

	statement = normalisedFor(rec, statement)

	var rendered []string
	if rec.RecordsArgs() {
		rendered = make([]string, 0, len(args))
		for i, arg := range args {
			rendered = append(rendered, RedactValue(positionalName(i), arg))
		}
	}

	entry := Entry{
		Kind:     KindQuery,
		At:       time.Now().Add(-took),
		Duration: took,
		Level:    slog.LevelInfo,
		Summary:  collapseWhitespace(statement),
		Watcher:  watcher,
	}
	entry.Data = mustJSON(sqlPayload{
		SQL:         statement,
		Args:        rendered,
		Rows:        -1,
		Correlation: correlationOf(ctx),
	})
	if err != nil {
		entry.Level = slog.LevelError
		entry.Err = err.Error()
	}

	rec.Record(ctx, entry)
	rec.Watching(watcher)
}

func positionalName(i int) string { return "$" + itoa(i+1) }

// normalisedFor applies the recorder's statement policy.
//
// A free function rather than a method so both query paths — the driver
// wrapper and this one — read the same way and cannot drift into recording
// the shape in one and the text in the other.
func normalisedFor(rec *Recorder, statement string) string {
	if rec.NormalisesStatements() {
		return NormaliseSQL(statement)
	}
	return statement
}
