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

	// KindException is a panic, with the stack that produced it.
	//
	// Separate from the request it happened in, rather than a flag on it,
	// because the two answer different questions. A request entry says this
	// request failed; an exception entry says what failed and where, carries
	// a stack that is far too large to hang off every request, and groups
	// with the other times the same thing broke.
	KindException Kind = "exception"

	// KindOutbound is an HTTP request this application made.
	//
	// Named for its direction rather than for being a "client request",
	// because the word that matters when reading a list is which way it
	// went: a request entry is something that arrived, an outbound entry is
	// something that left. A slow page is as often a slow third party as a
	// slow query, and before this there was nothing to tell them apart.
	KindOutbound Kind = "outbound"

	// KindDump is a value somebody asked to see.
	//
	// The one kind produced on purpose rather than observed, and the one
	// meant to be deleted before merge — observe.Dump is this project's dd(),
	// and a dump that reaches production is a mistake the dashboard will
	// happily show you.
	KindDump Kind = "dump"

	// KindNotification is one notification delivered, or not, on one channel.
	KindNotification Kind = "notification"

	// KindView is one view rendered, with how long it took.
	//
	// The panel exists for one question — why is this page slow when the
	// queries are fast — and it is the only one a query entry cannot
	// answer, because a template that loops over an already-loaded relation
	// issues no statements at all.
	KindView Kind = "view"

	// KindGate is one authorization decision.
	//
	// Recorded whether it allowed or denied, because the interesting case is
	// the denial nobody expected and the allow nobody intended, and a panel
	// showing only refusals cannot show the second.
	KindGate Kind = "gate"

	// KindEvent is one application event dispatched.
	KindEvent Kind = "event"

	// KindSchedule is one firing of a scheduled job.
	//
	// Separate from KindJob because a scheduled run and the job it queues
	// answer different questions: the queue says what work exists, the
	// schedule says whether 4:20 came round and what happened when it did —
	// including a run that failed to enqueue at all, which leaves no job to
	// look at.
	KindSchedule Kind = "schedule"
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

	// Source says which process recorded this, rendered compactly as
	// service/host/instance.
	//
	// Stamped by the recorder from its configured [Source], so no watcher
	// has to know about it. Empty when nothing was configured, which is the
	// ordinary case for a single-process development machine where the
	// answer would be the same on every row.
	//
	// The full structured source travels in the export envelope instead,
	// where it costs once per batch rather than once per entry.
	Source string `json:"source,omitempty"`

	// Tags label this entry for retrieval that a request id cannot serve.
	//
	// RequestID groups what one request did. A tag groups across requests —
	// "everything this user did", "every entry that failed", "everything on
	// this tenant" — which is the question a correlation id structurally
	// cannot answer, because each request has a different one.
	//
	// Most tags arrive from the context rather than from the watcher that
	// built the entry: see WithTags. The recorder normalises them and adds
	// TagFailed, so a caller never has to.
	Tags []string `json:"tags,omitempty"`
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
