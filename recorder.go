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

	// Queued is how many are waiting right now.
	Queued int `json:"queued"`
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

	// dropLogged keeps a saturated queue from writing a log line per dropped
	// entry — which would turn one problem into two, since the slog watcher
	// would record those lines too.
	dropLogged atomic.Bool

	batchSize     int
	flushInterval time.Duration
	sampler       func(Entry) bool

	// watchers names what is installed, so a reader can tell "nothing
	// happened" from "nothing was watching".
	mu       sync.RWMutex
	watchers map[string]struct{}
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
	if r == nil || !Recording(ctx) {
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

	if r.sampler != nil && !r.sampler(e) {
		return
	}

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

// Stats reports what the recorder has done.
func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return Stats{
		Recorded: r.recorded.Load(),
		Dropped:  r.dropped.Load(),
		Failed:   r.failed.Load(),
		Queued:   len(r.queue),
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

	if err := r.store.Record(ctx, batch); err != nil {
		r.failed.Add(uint64(len(batch)))
		slog.Error("observe: a batch of entries could not be stored",
			"entries", len(batch), "error", err)
	}
}
