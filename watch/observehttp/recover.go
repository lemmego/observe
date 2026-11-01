// Package observehttp recovers from a panic and records it before answering.
//
// It replaces the recovering middleware an application already installs —
// in a Lemmego application, api/middleware.Recoverer, which is three lines
// delegating to chi's. That one writes the stack to stderr through its own
// pretty-printer and nowhere else: not through slog, so a log watcher never
// sees it, and not into any store, so by the time somebody asks what
// happened the only copy is in a terminal that has scrolled.
//
// What observe could see before this was the *shape* of the failure — a
// request entry at 500, because the status is observable from outside — and
// nothing about its cause.
//
// The package imports observe and the standard library and nothing else, so
// it is usable from any net/http program, Lemmego or not.
package observehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/lemmego/observe"
)

// Watcher is the name entries from this package carry.
const Watcher = "exception"

// exceptionPayload is what an exception entry carries in Data.
type exceptionPayload struct {
	// Panic is the recovered value rendered as text. Not the value itself:
	// it is arbitrary, frequently not JSON-encodable, and a store that
	// failed to marshal it would drop the whole entry.
	Panic string `json:"panic"`

	// Stack is the goroutine stack at the moment of recovery.
	Stack string `json:"stack"`

	Method string `json:"method"`
	Path   string `json:"path"`
}

// Recover returns middleware that records a panic and then answers 500.
//
// Install it where the application's existing recoverer goes, and remove
// that one. Two recoverers is not twice the safety: a panic unwinds outward,
// so the one nearest the handler catches it and the other never fires. This
// has to be the nearest, or it records nothing — replacing rather than
// adding is the only arrangement that guarantees it.
//
// # Ordering
//
// This must sit *inside* observe's own request middleware, which is where it
// lands naturally: observe/provider installs that one during Provide, before
// the application's HTTP middleware is registered, and the router wraps the
// first-registered outermost. That ordering is what makes the request id
// available here, so the exception groups under the request that caused it.
//
// It is also why the panic has to be recovered here rather than there: a
// recoverer that sat outside observe's request middleware would swallow the
// panic before that middleware's own deferred recover could see it.
func Recover() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				// http.ErrAbortHandler is a deliberate abort, not a failure.
				// net/http treats it as "stop, quietly", and chi's recoverer
				// re-panics it for that reason. Recording it would turn an
				// ordinary cancelled response into a reported crash.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				stack := debug.Stack()
				record(r, recovered, stack)

				// Still said out loud. The store is for looking things up
				// afterwards; somebody watching a terminal during
				// development should not have to go and look.
				slog.Error("panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", stringify(recovered),
					"request_id", observe.RequestIDFrom(r.Context()),
					"stack", string(stack))

				// An upgraded connection is no longer speaking HTTP, so
				// there is no status line left to write. chi makes the same
				// check, and without it the write is a no-op that logs a
				// superfluous-WriteHeader complaint from net/http.
				if r.Header.Get("Connection") != "Upgrade" {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// record writes the exception entry.
//
// The context comes from the request, so the entry carries the request id
// observe's own middleware minted and groups under that request.
func record(r *http.Request, recovered any, stack []byte) {
	recorder := observe.Default()
	if recorder == nil {
		return
	}

	entry := observe.Entry{
		Kind:    observe.KindException,
		At:      time.Now(),
		Level:   slog.LevelError,
		Summary: "panic: " + stringify(recovered),
		Err:     stringify(recovered),
		Watcher: Watcher,
	}

	payload := exceptionPayload{
		Panic:  stringify(recovered),
		Stack:  string(stack),
		Method: r.Method,
		Path:   r.URL.Path,
	}
	if data, err := json.Marshal(payload); err == nil {
		entry.Data = data
	}

	ctx := r.Context()
	// A panic may well have come with a cancelled context — a client that
	// hung up, a timeout — and recording must not be skipped because of it.
	// The request id is read across first so correlation survives.
	if err := ctx.Err(); err != nil {
		ctx = observe.WithRequestID(context.Background(), observe.RequestIDFrom(ctx))
	}

	recorder.Record(ctx, entry)
	recorder.Watching(Watcher)
}

// stringify renders a recovered value as text.
//
// A panic value is anything at all. error and string cover nearly every real
// case and are worth rendering exactly; the rest go through %v rather than
// being dropped.
func stringify(v any) string {
	switch t := v.(type) {
	case error:
		return t.Error()
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
