package observe

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Paging bounds. A limit arriving from a query string is the normal case, so
// ClampLimit exists rather than every caller remembering to check.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// ClampLimit returns a usable page size for a limit that may be zero,
// negative, or absurd.
func ClampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageSize
	case limit > MaxPageSize:
		return MaxPageSize
	default:
		return limit
	}
}

// Cursor pages through entries by key rather than by offset.
//
// Entries are appended continuously, so an offset shifts under the reader:
// page two of a list that gained three rows while you were reading page one
// repeats three entries and skips none visibly. A key cursor cannot do that.
type Cursor struct {
	// BeforeID returns entries older than this one. Empty starts at the newest.
	BeforeID string

	Limit int
}

// Query selects entries. A zero Query returns the most recent page of
// everything.
type Query struct {
	// Kinds restricts to these kinds. Empty means every kind.
	Kinds []Kind

	// MinLevel restricts to entries at or above a level. Nil means every
	// level.
	MinLevel *slog.Level

	// RequestID returns everything recorded under one request, which is the
	// query the whole package exists to answer.
	RequestID string

	Since time.Time
	Until time.Time

	// Contains matches the summary and the payload.
	Contains string

	Cursor Cursor
}

// ErrNoEntry reports that an id names no entry.
var ErrNoEntry = errors.New("observe: no such entry")

// Store persists entries.
//
// Record takes a slice because the recorder batches: a request that issues
// forty queries should cost one round trip rather than forty. A store must
// assign an ID to any entry arriving without one, and must leave one that
// arrives with an ID alone.
//
// Entries returns newest first. A store must honour the cursor exactly,
// because the dashboard pages on it and a store that returned an extra row
// would show a duplicate at every page boundary.
type Store interface {
	Record(ctx context.Context, entries []Entry) error
	Entries(ctx context.Context, q Query) ([]Entry, error)

	// Entry returns one entry, or an error satisfying
	// errors.Is(err, ErrNoEntry).
	Entry(ctx context.Context, id string) (Entry, error)

	// Prune deletes entries older than a cutoff and reports how many went.
	//
	// Retention is the store's only defence against unbounded growth, so this
	// is part of the contract rather than an extra a store may decline.
	Prune(ctx context.Context, before time.Time) (int64, error)
}
