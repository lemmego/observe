// Package observeview records view renders.
//
// The panel exists for one question — why is this page slow when the queries
// are fast — and it is the only question a query entry cannot answer, because
// a template that loops over an already-loaded relation issues no statements
// at all. Before this, that page looked identical to a fast one.
//
// Install it once at boot:
//
//	observeview.Watch(recorder)
//
// Needs api v0.10.0 or later, where app.ObserveRenders landed. Registration
// is process-wide and additive.
//
// # What it does not record
//
// No props and no rendered output. An Inertia page's props are the response
// body, and the body is where a credential lives — the rule this package
// follows everywhere else. The view's name, its status and how long it took
// are what a view panel is for.
package observeview

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe"
)

// Watcher is the name entries from this package carry.
const Watcher = "view"

type viewPayload struct {
	// View is the component path or template file, which is what a reader
	// recognises. A renderer that will not name itself is reported by its
	// Go type, which api decides rather than this package.
	View   string `json:"view"`
	Status int    `json:"status,omitempty"`
}

// Watch records every view render onto the recorder.
func Watch(recorder *observe.Recorder) {
	if recorder == nil {
		return
	}

	app.ObserveRenders(func(ctx context.Context, rendered app.Rendered) {
		entry := observe.Entry{
			Kind:     observe.KindView,
			Duration: rendered.Duration,
			Level:    slog.LevelInfo,
			Summary:  "rendered " + rendered.View,
			Watcher:  Watcher,
		}
		if rendered.Err != nil {
			// A render that failed has already written a partial body by the
			// time it is reported, which is the failure that reaches a
			// browser as a truncated page rather than as an error — so it is
			// worth finding in a list.
			entry.Level = slog.LevelError
			entry.Err = rendered.Err.Error()
		}
		if data, err := json.Marshal(viewPayload{
			View:   rendered.View,
			Status: rendered.Status,
		}); err == nil {
			entry.Data = data
		}

		recorder.Record(ctx, entry)
	})

	recorder.Watching(Watcher)
}
