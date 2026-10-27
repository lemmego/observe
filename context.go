package observe

import "context"

// Correlation travels in the context, because in Go there is nothing else it
// could travel in.
//
// A request is a goroutine, and the query three calls deep has no idea which
// request issued it. Everything this package records is recorded by something
// that already receives a context.Context — a driver's QueryerContext, an
// slog handler, a cache listener, a job middleware — so the context is both
// the only carrier available and one that is already threaded everywhere.
type contextKey int

const (
	requestIDKey contextKey = iota
	internalKey
)

// WithRequestID returns a context carrying a request identifier, so everything
// recorded under it groups together.
//
// The HTTP middleware in observe/provider calls this. Call it yourself for
// work that is not an HTTP request but is still one unit you want to read as
// a whole — a scheduled task, a CLI command, a job handler.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request identifier in a context, or "".
//
// An empty answer is ordinary: a worker process has no request behind it. A
// watcher records the entry regardless rather than dropping what it cannot
// correlate.
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithoutRecording returns a context whose work is not recorded.
//
// This is the recursion guard, and it is not optional. The SQL store writes
// entries through the same database the driver wrapper is watching, so a
// recorded write produces an entry, which produces a write, which produces an
// entry. Nothing downstream would stop it: log.Fanout keeps delivering to
// every handler even after one fails, so a looping recorder is not contained
// by anything the way a panicking one is.
//
// A store marks its own writes with this, and every watcher checks
// [Recording] before it records.
func WithoutRecording(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalKey, true)
}

// Recording reports whether work under this context should be recorded.
func Recording(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	off, _ := ctx.Value(internalKey).(bool)
	return !off
}
