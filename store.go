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

	// Source restricts to entries whose source contains this text.
	//
	// A substring rather than an exact match, so "everything this instance
	// did" and "everything this host did" are the same filter at different
	// precisions — service/host/instance is rendered longest-lived first
	// precisely so a prefix means something.
	Source string

	// Tags restricts to entries carrying **every** one of these tags.
	//
	// Every rather than any, because tags narrow: a reader asking for
	// "user:7" and "failed" wants what that user broke, not everything that
	// user did plus everything that broke.
	Tags []string

	Cursor Cursor
}

// ErrNoEntry reports that an id names no entry.
var ErrNoEntry = errors.New("observe: no such entry")

// Sink receives batches of entries.
//
// The write half of [Store], split out so somewhere to send entries does not
// have to be somewhere to read them back from. An exporter shipping to a
// collector cannot answer Entries or Counts, and making it return an error
// from four methods it has no business implementing would be a lie told four
// times.
//
// # Record must not block
//
// It is called on the recorder's writer goroutine, which is the one thing
// standing between a slow destination and the queue filling up. A sink that
// waits on a network does its waiting on its own goroutine and accepts the
// batch into a bounded buffer of its own — which is what observe/export does,
// and why it exists rather than a transport being a Sink directly.
//
// Returning an error is for reporting, not for retrying: the recorder counts
// it and logs it and moves on, because it has nowhere to put a batch it
// cannot hand over.
type Sink interface {
	Record(ctx context.Context, entries []Entry) error
}

// SinkCloser is a sink with a lifecycle.
//
// A sink holding a buffer or a connection needs telling when to let go, and
// the recorder closes every sink that says so when it closes itself — so an
// exporter flushes what it is holding on a graceful shutdown rather than
// taking it to the grave.
type SinkCloser interface {
	Sink
	Close(ctx context.Context) error
}

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
	Sink
	Entries(ctx context.Context, q Query) ([]Entry, error)

	// Entry returns one entry, or an error satisfying
	// errors.Is(err, ErrNoEntry).
	Entry(ctx context.Context, id string) (Entry, error)

	// Prune deletes entries older than a cutoff and reports how many went.
	//
	// Retention is the store's only defence against unbounded growth, so this
	// is part of the contract rather than an extra a store may decline.
	Prune(ctx context.Context, before time.Time) (int64, error)

	// Counts reports how many entries of each kind were recorded at or after
	// a time. A zero time counts everything.
	//
	// Part of the contract rather than something the dashboard works out for
	// itself, because it cannot: a page of entries is a page, and counting
	// what is on it would answer "how many exceptions are in the last fifty
	// rows" — which is not a question anybody has. A kind with no entries is
	// omitted rather than reported as zero, so a caller can tell "none" from
	// "a kind this store has never heard of".
	Counts(ctx context.Context, since time.Time) (map[Kind]int64, error)
}
