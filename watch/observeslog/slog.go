// Package observeslog records log lines alongside the request that produced
// them.
//
// This is what turns a request view from a list of queries into an account of
// what happened: the warning your handler logged sits between the two
// statements it sits between in the code.
//
// Install it through lemmego/log's Extra, which folds it in alongside the
// console and the file rather than replacing either:
//
//	&logger.Provider{Options: log.Options{
//	    Extra: []slog.Handler{observeslog.Handler()},
//	}},
//	&observeprovider.Provider{ … },
//
// The ordering between those two does not matter. The handler resolves the
// recorder when a record arrives rather than when it is constructed, so it is
// installable before observe's provider has run — which it has to be, since
// LoadProviders is written before anything exists.
package observeslog

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/lemmego/observe"
)

// Watcher is the name entries from this handler carry.
const Watcher = "slog"

// Handler returns an slog.Handler that records to observe's default recorder.
func Handler() slog.Handler { return &handler{} }

type handler struct {
	// fixed are the attrs from WithAttrs, already flattened and already
	// qualified by whatever groups were open when they were added.
	//
	// Qualified at WithAttrs time rather than at Handle time, because an
	// attr belongs to the groups that were open when it arrived — not to
	// every group ever opened. Keeping a flat list and a flat group path and
	// combining them later puts a later group's name on an earlier attr,
	// which is wrong and reads as the attr having moved.
	fixed map[string]any

	// groups are the groups open now, which qualify a record's own attrs.
	groups []string
}

var _ slog.Handler = (*handler)(nil)

// Enabled is always true.
//
// The recorder's own sampler decides what is kept, and a level filter here
// would silently shadow it: a debug record dropped at this gate can never be
// recovered by turning sampling up.
func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(ctx context.Context, record slog.Record) error {
	recorder := observe.Default()
	if recorder == nil || !observe.Recording(ctx) {
		return nil
	}

	// Declined while a batch is being written. A line logged by a database
	// driver during the store's own write arrives with no context to carry
	// the guard, so recording it would produce a write, producing a line.
	// See Recorder.Storing.
	if recorder.Storing() {
		return nil
	}

	// And observe's own complaints are never recorded. A saturated queue
	// logs that it is dropping entries; recording that line is one more
	// entry for a queue that is already full.
	if strings.HasPrefix(record.Message, "observe:") {
		return nil
	}

	entry := observe.Entry{
		Kind:    observe.KindLog,
		At:      record.Time,
		Level:   record.Level,
		Summary: record.Message,
		Watcher: Watcher,
	}

	fields := make(map[string]any, record.NumAttrs()+len(h.fixed))
	for key, value := range h.fixed {
		fields[key] = value
	}
	// Copied out during the call, never retained. Record.Attrs is single-pass
	// over a shared backing array, which is why Fanout clones per handler —
	// holding the record and iterating later races with whoever else was
	// given it.
	record.Attrs(func(attr slog.Attr) bool {
		collect(fields, h.groups, attr)
		return true
	})

	// An error attribute is promoted, so "show me what went wrong" finds it
	// without reading the payload of every entry.
	if value, ok := fields["error"]; ok {
		entry.Err = stringify(value)
	}

	if len(fields) > 0 {
		if data, err := json.Marshal(observe.RedactMap(fields)); err == nil {
			entry.Data = data
		}
	}

	recorder.Record(ctx, entry)
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	// A fresh map rather than writing into h.fixed: two loggers derived from
	// the same parent would otherwise overwrite each other's attributes.
	fixed := make(map[string]any, len(h.fixed)+len(attrs))
	for key, value := range h.fixed {
		fixed[key] = value
	}
	for _, attr := range attrs {
		collect(fixed, h.groups, attr)
	}
	return &handler{fixed: fixed, groups: h.groups}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	// A fresh slice for the same reason, and h.fixed is carried as it is:
	// those attrs were added before this group was opened, so this group's
	// name does not belong on them.
	groups := make([]string, 0, len(h.groups)+1)
	groups = append(groups, h.groups...)
	groups = append(groups, name)
	return &handler{fixed: h.fixed, groups: groups}
}

// collect flattens an attribute into the payload, qualifying it with whatever
// groups it is nested under so two "id" attrs in different groups stay apart.
func collect(into map[string]any, groups []string, attr slog.Attr) {
	value := attr.Value.Resolve()

	if value.Kind() == slog.KindGroup {
		nested := append(append([]string{}, groups...), attr.Key)
		for _, inner := range value.Group() {
			collect(into, nested, inner)
		}
		return
	}

	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}
	into[key] = value.Any()
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case error:
		return typed.Error()
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "unprintable error value"
		}
		return string(encoded)
	}
}
