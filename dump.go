package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Dump records a value, the way a print statement does, but under the request
// that produced it.
//
//	observe.Dump(ctx, "why is this nil", order)
//
// It is this project's dd(): meant to be added while chasing something and
// taken out before merge. Unlike a print statement it lands beside the
// queries and log lines of the same request, in order, which is usually the
// thing that makes the answer obvious.
//
// # It records exactly what you give it
//
// Every other kind in this package withholds bodies and redacts values,
// because nothing chose what went into them. This one is chosen, so it does
// as it is told — which means dumping a user, a request or a config struct
// puts whatever those hold into the store. That is the right behaviour for a
// debugging tool and the reason this kind should not survive to production.
func Dump(ctx context.Context, label string, values ...any) {
	recorder := Default()
	if recorder == nil {
		return
	}

	rendered := make([]string, 0, len(values))
	for _, v := range values {
		rendered = append(rendered, renderDumped(v))
	}

	entry := Entry{
		Kind:    KindDump,
		At:      time.Now(),
		Level:   slog.LevelInfo,
		Summary: label,
		Watcher: WatcherDump,
	}
	if data, err := json.Marshal(dumpPayload{Label: label, Values: rendered}); err == nil {
		entry.Data = data
	}

	recorder.Record(ctx, entry)
	recorder.Watching(WatcherDump)
}

// WatcherDump is the watcher name dump entries carry.
const WatcherDump = "dump"

type dumpPayload struct {
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

// renderDumped turns a dumped value into text.
//
// Deliberately not redact.go's renderValue, which withholds a []byte and
// rewrites anything secret-looking. Those rules exist because nothing chose
// what went into the entries they guard. A dump was chosen, and a debugger
// that hid the thing you asked to see would be useless.
//
// JSON when it will encode, because that is readable in the dashboard and
// keeps a struct's field names; %+v otherwise, so a channel, a func or a
// cyclic structure is still reported rather than silently dropping the whole
// entry.
func renderDumped(v any) string {
	if data, err := json.Marshal(v); err == nil {
		return string(data)
	}
	return fmt.Sprintf("%+v", v)
}
