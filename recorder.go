package observe

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultQueueSize is how many entries may be waiting to be stored.
//
// Generous, because the queue draining is the normal case and a full one means
// the store is not keeping up. One busy request can easily produce forty
// entries, so a small buffer would drop under ordinary load rather than under
// trouble.
const DefaultQueueSize = 1024

// DefaultBatchSize is how many entries one write carries.
const DefaultBatchSize = 64

// DefaultFlushInterval is how long a partial batch waits for company.
const DefaultFlushInterval = time.Second

// Stats reports what the recorder has done, so a dropped entry is never
// silent.
type Stats struct {
	// Recorded is how many entries were accepted onto the queue.
	Recorded uint64 `json:"recorded"`

	// Dropped is how many were discarded because the queue was full.
	//
	// This is the number to watch. It rising means the store cannot keep up,
	// and the trade is deliberate: recording must never slow a request, so a
	// full queue drops rather than blocks.
	Dropped uint64 `json:"dropped"`

	// Failed is how many entries a store refused to write.
	Failed uint64 `json:"failed"`

	// Sampled is how many a sampler declined.
	//
	// Counted separately from Dropped because they mean opposite things: a
	// dropped entry is the store failing to keep up, a sampled one is the
	// configuration working as asked. Without this number, a list that is
	// one tenth as long as expected looks like a broken watcher rather than
	// like OBSERVE_SAMPLE=0.1.
	Sampled uint64 `json:"sampled"`

	// Queued is how many are waiting right now.
	Queued int `json:"queued"`

	// Paused reports whether this process is recording, so the dashboard
	// does not present an empty list as an idle application.
	Paused bool `json:"paused"`
}

// Recorder accepts entries and writes them to a store, off the caller's
// goroutine.
//
// The hand-off is a bounded channel with a drop counter and a loud log. The
// reason is that the things being recorded sit on hot paths: the driver
// wrapper runs inside every statement, and an slog handler runs inside every
// log call. Anything synchronous there is paid by the whole application, so a
// slow or unreachable store must cost dropped entries and never latency.
//
// A nil *Recorder is usable and records nothing, so a watcher installed
// before the recorder exists is not a panic.
type Recorder struct {
	store Store

	queue chan Entry
	done  chan struct{}
	wg    sync.WaitGroup

	closeOnce sync.Once

	recorded atomic.Uint64
	dropped  atomic.Uint64
	failed   atomic.Uint64
	sampled  atomic.Uint64

	// dropLogged keeps a saturated queue from writing a log line per dropped
	// entry — which would turn one problem into two, since the slog watcher
	// would record those lines too.
	dropLogged atomic.Bool

	// paused stops recording without tearing the recorder down.
	//
	// Per-process, which is the whole of what it claims to be: the dashboard
	// button pauses the process that served the click. That is exactly right
	// in development, where there is one process and a pause is how you read
	// a list that will not stop scrolling — and it is not a deployment-wide
	// switch, which is what OBSERVE_ENABLED and a sampler are for.
	paused atomic.Bool

	// storing is set while a batch is being written.
	//
	// The context guard closes the recursion for anything that receives a
	// context, which is most things. It cannot close it for a log line
	// emitted without one — slog.Error rather than slog.ErrorContext — from
	// inside a database driver during this store's own write. That line would
	// be recorded, producing a write, producing a line.
	//
	// So the slog watcher also declines while this is set. The cost is that
	// log lines emitted during a flush window of a few milliseconds are not
	// recorded; they still reach the log itself. The alternative is a write
	// loop that never ends.
	storing atomic.Bool

	batchSize     int
	flushInterval time.Duration
	sampler       func(Entry) bool

	// watchers names what is installed, so a reader can tell "nothing
	// happened" from "nothing was watching".
	mu       sync.RWMutex
	watchers map[string]struct{}

	// subs are the live tails. See subscribe.go.
	subsMu sync.RWMutex
	subs   map[*subscriber]struct{}
}

// Option configures a Recorder.
type Option func(*Recorder)

// WithQueueSize sets how many entries may be waiting. Zero or less keeps the
// default.
func WithQueueSize(n int) Option {
	return func(r *Recorder) {
		if n > 0 {
			r.queue = make(chan Entry, n)
		}
	}
}

// WithBatchSize sets how many entries one write carries.
func WithBatchSize(n int) Option {
	return func(r *Recorder) {
		if n > 0 {
			r.batchSize = n
		}
	}
}

// WithFlushInterval sets how long a partial batch waits.
func WithFlushInterval(d time.Duration) Option {
	return func(r *Recorder) {
		if d > 0 {
			r.flushInterval = d
		}
	}
}

// WithSampler installs a predicate deciding whether an entry is kept.
//
// This is what makes recording affordable outside development. Sample on the
// request rather than on the entry where you can — keeping a tenth of the
// queries of every request gives you ten percent of a story about all of
// them, where keeping every query of a tenth of requests gives you the whole
// story about some, which is the one you can debug.
func WithSampler(keep func(Entry) bool) Option {
	return func(r *Recorder) { r.sampler = keep }
}

// New returns a Recorder writing to a store, and starts its writer.
//
// Close it, or the writer goroutine outlives the application.
func New(store Store, opts ...Option) *Recorder {
	r := &Recorder{
		store:         store,
		done:          make(chan struct{}),
		batchSize:     DefaultBatchSize,
		flushInterval: DefaultFlushInterval,
		watchers:      make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.queue == nil {
		r.queue = make(chan Entry, DefaultQueueSize)
	}

	r.wg.Add(1)
	go r.write()
	return r
}

// Record queues an entry.
//
// It never blocks and never returns an error, because every caller is on a
// path that must not care: a handler cannot usefully respond to "the debug
// recorder is busy".
func (r *Recorder) Record(ctx context.Context, e Entry) {
	if r == nil || r.paused.Load() || !Recording(ctx) {
		return
	}

	if e.ID == "" {
		e.ID = NewID()
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.RequestID == "" {
		e.RequestID = RequestIDFrom(ctx)
	}

	// Context tags first, then the entry's own, then the derived one. The
	// order decides which survive the MaxTags cap, and a request-scoped tag
	// — who this was — is worth more than a watcher's detail.
	tags := TagsFrom(ctx)
	if len(e.Tags) > 0 {
		tags = append(append([]string(nil), tags...), e.Tags...)
	}
	if e.Failed() {
		tags = append(append([]string(nil), tags...), TagFailed)
	}
	e.Tags = NormaliseTags(tags)

	if r.sampler != nil && !r.sampler(e) {
		r.sampled.Add(1)
		return
	}

	// Before the queue, so a tail is live rather than a view of the last
	// flush — and so an entry the queue has no room for is still seen by
	// whoever is watching, which is exactly when somebody is watching.
	r.publish(e)

	select {
	case r.queue <- e:
		r.recorded.Add(1)
	default:
		// Counted and complained about, never silent — but once, not once per
		// entry, because the complaint goes through slog and the slog watcher
		// records it.
		r.dropped.Add(1)
		if r.dropLogged.CompareAndSwap(false, true) {
			slog.Warn("observe: the entry queue is full, so entries are being dropped; "+
				"the store is not keeping up",
				"queue", cap(r.queue), "kind", e.Kind, "watcher", e.Watcher)
		}
	}
}

// Pause stops this process recording.
//
// Entries already queued are still written, so pausing does not lose what was
// in flight — the point is to stop adding, not to discard.
//
// It is deliberately per-process and not persisted. A command in another
// process cannot reach this, which is why there is no observe:pause: it would
// pause the CLI's own recorder and report success, leaving the web process
// recording exactly as before. A deployment-wide switch is OBSERVE_ENABLED
// plus a restart, or a sampler.
func (r *Recorder) Pause() {
	if r != nil {
		r.paused.Store(true)
	}
}

// Resume starts recording again.
func (r *Recorder) Resume() {
	if r != nil {
		r.paused.Store(false)
	}
}

// Paused reports whether this process is recording.
func (r *Recorder) Paused() bool {
	return r != nil && r.paused.Load()
}

// Watching registers that a watcher is installed, so a reader can tell an
// empty panel from an unwatched one.
func (r *Recorder) Watching(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.watchers[name] = struct{}{}
	r.mu.Unlock()
}

// Watchers names the installed watchers.
func (r *Recorder) Watchers() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.watchers))
	for name := range r.watchers {
		names = append(names, name)
	}
	return names
}

// Storing reports whether a batch is being written right now.
//
// The slog watcher consults this; see the field's own documentation for why
// the context guard is not sufficient on its own.
func (r *Recorder) Storing() bool {
	if r == nil {
		return false
	}
	return r.storing.Load()
}

// Stats reports what the recorder has done.
func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return Stats{
		Recorded: r.recorded.Load(),
		Dropped:  r.dropped.Load(),
		Failed:   r.failed.Load(),
		Sampled:  r.sampled.Load(),
		Queued:   len(r.queue),
		Paused:   r.paused.Load(),
	}
}

// Store returns the store entries are written to, for reading them back.
func (r *Recorder) Store() Store {
	if r == nil {
		return nil
	}
	return r.store
}

// Close drains what is queued and stops the writer.
//
// Idempotent, so it is safe as a ShutdownProvider, which may be called twice.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		close(r.done)
		r.wg.Wait()
		r.drain(ctx)
		r.closeSubscribers()
	})
	return nil
}

func (r *Recorder) write() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop()

	batch := make([]Entry, 0, r.batchSize)

	for {
		select {
		case e := <-r.queue:
			batch = append(batch, e)
			if len(batch) >= r.batchSize {
				r.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.flush(batch)
				batch = batch[:0]
			}
		case <-r.done:
			if len(batch) > 0 {
				r.flush(batch)
			}
			return
		}
	}
}

// drain writes whatever was still queued when Close was called.
func (r *Recorder) drain(ctx context.Context) {
	batch := make([]Entry, 0, r.batchSize)
	for {
		select {
		case e := <-r.queue:
			batch = append(batch, e)
			if len(batch) >= r.batchSize {
				r.flush(batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				r.flush(batch)
			}
			return
		}
	}
}

func (r *Recorder) flush(batch []Entry) {
	if r.store == nil {
		return
	}

	// The store's own writes must not be recorded, or the SQL store writing an
	// entry through the watched database never terminates. Its deadline is its
	// own: a shutting-down application's cancelled context must not discard
	// the batch it is trying to drain.
	ctx, cancel := context.WithTimeout(WithoutRecording(context.Background()), 10*time.Second)
	defer cancel()

	r.storing.Store(true)
	defer r.storing.Store(false)

	if err := r.store.Record(ctx, batch); err != nil {
		r.failed.Add(uint64(len(batch)))
		slog.Error("observe: a batch of entries could not be stored",
			"entries", len(batch), "error", err)
	}
}

// closeSubscribers ends every live tail.
//
// These connections are long-lived by construction — a tail is open for as
// long as somebody has the dashboard in a tab — so a recorder that shut down
// without closing them would hang the process on every graceful stop.
func (r *Recorder) closeSubscribers() {
	r.subsMu.Lock()
	subs := r.subs
	r.subs = nil
	r.subsMu.Unlock()

	for sub := range subs {
		sub.close()
	}
}
