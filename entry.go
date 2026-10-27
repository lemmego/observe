// Package observe records what an application did while answering a request,
// so the question "what happened on that request?" has an answer.
//
// A request, and underneath it every query, cache operation, job, mail and log
// line it caused, in order, with timings. Logs cannot give you that: they are
// a flat stream with no notion of which request a line belongs to.
//
// The root package is the standard library only, which is what lets an
// application instrument its queries without importing a web framework, a
// store or a watcher. [Wrap] and [RecordQuery] are the two entry points most
// applications need; the wiring lives in observe/provider.
package observe

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"
)

// Kind is what an entry describes.
//
// It is a string rather than an integer so a stored entry stays readable in
// the database and an application can add a kind of its own without waiting
// for a release here.
type Kind string

const (
	KindRequest Kind = "request"
	KindQuery   Kind = "query"
	KindLog     Kind = "log"
	KindCache   Kind = "cache"
	KindJob     Kind = "job"
	KindMail    Kind = "mail"
)

// Entry is one recorded thing.
type Entry struct {
	// ID identifies this entry. A store assigns it if it is empty.
	ID string `json:"id"`

	Kind Kind `json:"kind"`

	// RequestID groups entries under the request that caused them.
	//
	// It is empty when nothing correlated them, which is a normal answer
	// rather than an error: work on a worker goroutine in another process has
	// no request behind it, and a driver that implements only the legacy
	// non-context interfaces gives us nowhere to read it from. An uncorrelated
	// entry is still recorded, and still says which watcher produced it.
	RequestID string `json:"request_id,omitempty"`

	At       time.Time     `json:"at"`
	Duration time.Duration `json:"duration"`

	// Level marks severity. Logs carry their own; everything else uses
	// LevelError when it failed and LevelInfo when it did not, so one filter
	// answers "show me what went wrong" across every kind.
	Level slog.Level `json:"level"`

	// Summary is one line for a list view: a method and path, a collapsed
	// statement, a job name.
	Summary string `json:"summary"`

	// Data is the kind-specific payload.
	//
	// json.RawMessage so it round-trips byte-identically. A store that
	// re-marshalled it could reorder keys, and this JSON is a contract with
	// the dashboard.
	Data json.RawMessage `json:"data,omitempty"`

	// Err is the error's text, when there was one.
	Err string `json:"error,omitempty"`

	// Watcher names what produced this entry — "sqldriver", "slog", "cache".
	//
	// Recorded so the dashboard can say which watchers are active. Without
	// it, an empty query panel reads as "this application makes no queries"
	// rather than as "nothing is watching them".
	Watcher string `json:"watcher"`
}

// Failed reports whether this entry describes something that went wrong.
func (e Entry) Failed() bool { return e.Err != "" || e.Level >= slog.LevelError }

// NewID returns an identifier for an entry or a request.
//
// Sortable by time, because a store orders by it and a cursor pages on it: the
// first eight bytes are the timestamp in nanoseconds, so lexicographic order
// over the hex is chronological order. The remaining bytes are random, which
// is what keeps two entries recorded in the same nanosecond distinct.
func NewID() string {
	var buf [16]byte
	ns := uint64(time.Now().UnixNano())
	for i := range 8 {
		buf[i] = byte(ns >> (56 - 8*i))
	}
	// crypto/rand.Read cannot fail as of Go 1.24: it panics rather than
	// returning an error, so there is nothing to handle here.
	rand.Read(buf[8:])
	return hex.EncodeToString(buf[:])
}
