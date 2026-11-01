// Package observeevents records the application's own dispatched events.
//
// app.Dispatch is in-process and synchronous, so an event is a fan-out of
// function calls with nothing to look at afterwards: whether the dispatch
// happened at all is not recoverable from anywhere. That is the question
// this answers — a feature whose cross-cutting reaction never fires looks
// identical to one whose listener is broken, and the two are debugged
// differently.
//
// Install it once at boot, before anything that dispatches:
//
//	observeevents.Watch(a, recorder, "launch.published", "comment.created")
//
// # It has to be told which events to watch
//
// The emitter has no "every event" registration — On takes a name — so there
// is no way to subscribe to events an application has not named. Listing them
// is the honest interface rather than one that silently covers some of them.
//
// Needs api v0.10.0 or later, where EventListener gained a context. Before it
// an event entry could not say which request caused it, which is most of the
// value.
//
// # What it does not record
//
// Not the payload. An application's event payloads are its own domain
// objects — a *models.User is a plausible payload and holds a password hash —
// and this package has no way to know which are safe. The event name, the
// payload's type and the number of listeners it reached are what a panel
// needs.
package observeevents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe"
)

// Watcher is the name entries from this package carry.
const Watcher = "event"

type eventPayload struct {
	Event string `json:"event"`

	// PayloadType is the Go type of what was dispatched, never the value.
	PayloadType string `json:"payload_type,omitempty"`
}

// Deliberately no listener count. The emitter exposes none through app.App,
// and a field that is always zero is worse than an absent one: it reads as
// "nothing is listening" on an application where something is.

// Watch records the named events onto the recorder.
//
// Each name is registered separately, because that is the only registration
// the emitter has. An unknown name is not an error: an application may watch
// an event a provider only dispatches under some configurations.
func Watch(a app.App, recorder *observe.Recorder, events ...string) {
	if a == nil || recorder == nil || len(events) == 0 {
		return
	}

	for _, event := range events {
		name := event
		a.On(name, func(ctx context.Context, payload any) error {
			if !recorder.ShouldRecord(ctx) {
				return nil
			}
			entry := observe.Entry{
				Kind:    observe.KindEvent,
				Level:   slog.LevelInfo,
				Summary: "dispatched " + name,
				Watcher: Watcher,
			}
			out := eventPayload{Event: name}
			if payload != nil {
				out.PayloadType = fmt.Sprintf("%T", payload)
			}
			if data, err := json.Marshal(out); err == nil {
				entry.Data = data
			}
			recorder.Record(ctx, entry)

			// Never an error. A listener's error is logged by the emitter,
			// and an observer reporting one would put its own noise in the
			// application's log for every event it watched.
			return nil
		})
	}

	recorder.Watching(Watcher)
}
