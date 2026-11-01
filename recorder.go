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

	batchSize      int
	flushInterval  time.Duration
	sampler        func(Entry) bool
	requestSampler func(requestID string) bool

	// recordArgs and normaliseStatements trade detail for volume. Both
	// default to the detailed answer, because this package was built for a
	// machine with one user on it and that is still where most of its use is.
	recordArgs          bool
	normaliseStatements bool

	// watchers names what is installed, so a reader can tell "nothing
	// happened" from "nothing was watching".
	mu       sync.RWMutex
	watchers map[string]struct{}

	// subs are the live tails. See subscribe.go.
	subsMu sync.RWMutex
	subs   map[*subscriber]struct{}

	// sinks receive every flushed batch alongside the store.
	sinks []Sink

	// source identifies this process. Rendered once at construction, because
	// it is stamped on every entry and does not change.
	source   Source
	sourceID string
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

// WithRequestSampler installs the coarse gate: whether a whole request is
// kept, decided from its correlation id alone.
//
// This is the one that makes sampling affordable, and the reason it takes an
// id rather than an Entry is the whole of it. A predicate over an Entry can
// only run once an Entry exists, which is after the caller has redacted every
// bound parameter, collapsed the statement and marshalled the payload — so a
// statement about to be discarded paid full price first. An id is available
// before any of that.
//
// The decision is made once, where the id is minted, and cached on the
// context; see [WithSampling] and [ShouldRecord]. [observe.SampleRequests]
// is the usual argument.
//
// It composes with [WithSampler] rather than replacing it: this one is coarse
// and per request, that one is fine and per entry, and "keep a tenth of
// requests, and within those drop cache hits" is a reasonable thing to want.
func WithRequestSampler(keep func(requestID string) bool) Option {
	return func(r *Recorder) { r.requestSampler = keep }
}

// WithoutArgs stops bound parameters being recorded with a statement.
//
// The most expensive thing on the query path and the least useful in
// aggregate. Every parameter costs a redaction and a string, and what they
// buy is the ability to replay one statement exactly — which is worth a
// great deal while debugging one request and almost nothing while watching
// ten thousand.
//
// The statement itself is still recorded, so a slow query is still findable
// and an N+1 is still detectable. What goes is the ability to say which id
// it was looking for.
//
// Not a privacy control on its own. It drops the bound parameters, and a
// value written into the statement text is part of the statement — an ORM
// binds, so this is rare, but hand-written SQL that interpolates does not.
// [WithNormalisedStatements] is what removes those, which is the second
// reason the production profile turns on both.
func WithoutArgs() Option {
	return func(r *Recorder) { r.recordArgs = false }
}

// WithNormalisedStatements records the shape of a statement rather than its
// text.
//
// Literals become placeholders, so fifty executions differing only in an id
// collapse to one string. That is a volume decision and not a CPU one — the
// normaliser is a lexer and costs more than copying the statement — and it
// is the right trade once entries are being shipped or kept, where the same
// statement written out ten thousand times is ten thousand copies of it.
//
// It also makes grouping agree with itself: N+1 detection normalises at read
// time, and reading an already-normalised statement is idempotent.
func WithNormalisedStatements() Option {
	return func(r *Recorder) { r.normaliseStatements = true }
}

// WithSinks sends every flushed batch somewhere beyond the store.
//
// A deployment can ship to a collector and keep a small memory ring for the
// local dashboard, or ship only — the store and the sinks are independent.
//
//	observe.New(store, observe.WithSinks(exporter))
//
// Deliberately not [Recorder.Subscribe]. A subscriber that falls behind is
// closed, which is right for a dashboard tail and wrong for a shipper: one
// slow minute would stop it forever, silently. A sink is handed the batch
// the store is handed, and owns its own buffering.
//
// Sinks are closed when the recorder is, if they implement [SinkCloser].
func WithSinks(sinks ...Sink) Option {
	return func(r *Recorder) {
		for _, sink := range sinks {
			if sink != nil {
				r.sinks = append(r.sinks, sink)
			}
		}
	}
}

// Sinks reports how many sinks are installed, for a diagnostic.
func (r *Recorder) Sinks() int {
	if r == nil {
		return 0
	}
	return len(r.sinks)
}

// WithSource identifies the process recording entries.
//
//	observe.New(store, observe.WithSource(observe.Source{
//	    Service: "productshore", Environment: "production", Release: commit,
//	}))
//
// Host and Instance are filled in from [DefaultSource] when left empty, so a
// caller supplies only what it knows and still gets a usable identity.
//
// Rendered once here rather than per entry: it is stamped on every one and
// never changes.
func WithSource(s Source) Option {
	return func(r *Recorder) {
		if s.Host == "" {
			s.Host = hostname()
		}
		if s.Instance == "" {
			s.Instance = newInstanceID()
		}
		r.source = s
		r.sourceID = s.String()
	}
}

// Source returns the configured identity, for an exporter to put in an
// envelope.
func (r *Recorder) Source() Source {
	if r == nil {
		return Source{}
	}
	return r.source
}

// WithProfile applies a profile's options.
//
// Listed before any explicit option so an explicit one wins, which is the
// order New applies them in anyway.
func WithProfile(p Profile) Option {
	return func(r *Recorder) {
		for _, opt := range p.Options() {
			opt(r)
		}
	}
}

// RecordsArgs reports whether bound parameters are recorded.
//
// Read by the query paths before they render anything, because rendering is
// the cost being avoided.
func (r *Recorder) RecordsArgs() bool { return r == nil || r.recordArgs }

// NormalisesStatements reports whether statements are recorded as shapes.
func (r *Recorder) NormalisesStatements() bool {
	return r != nil && r.normaliseStatements
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
		recordArgs:    true,
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

	// The gate, enforced here as well as offered through ShouldRecord.
	//
	// ShouldRecord is an optimisation a watcher calls to avoid building an
	// entry it is about to lose; this is what makes the decision binding. A
	// watcher that forgets to ask costs performance, not correctness — which
	// is the right way round, because the alternative is a sampling rate that
	// silently means nothing as soon as one call site is added without it.
	if !sampledIn(ctx) && !bypassesSampling(e) {
		r.sampled.Add(1)
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
	if e.Source == "" {
		e.Source = r.sourceID
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

// ShouldRecord reports whether a watcher should build an entry at all.
//
// Call it first, before marshalling a payload or rendering an argument:
//
//	if !recorder.ShouldRecord(ctx) {
//	    return
//	}
//
// It folds together the two reasons not to bother — the recursion guard, and
// a request this recorder sampled out — so a watcher has one question to ask
// rather than two to remember.
//
// # Ask it only about work that succeeded
//
// Anything that failed is recorded whatever the sampling says, and Record
// enforces that — see bypassesSampling. So a caller that already knows the
// outcome asks only when there is nothing to keep:
//
//	if err == nil && !rec.ShouldRecord(ctx) {
//	    return
//	}
//
// A caller that asks anyway loses failures it would otherwise have kept. A
// caller that never asks is correct and slower, which is the right way round
// for an optimisation.
func (r *Recorder) ShouldRecord(ctx context.Context) bool {
	if r == nil || r.paused.Load() {
		return false
	}
	if !Recording(ctx) {
		// The recursion guard, not a sampling decision: not counted, because
		// a store writing its own entries is not telemetry anybody asked for
		// and reporting it as sampled-out would be noise in a number people
		// read to size their sampling rate.
		return false
	}
	if !sampledIn(ctx) {
		r.sampled.Add(1)
		return false
	}
	return true
}

// ShouldSampleRequest decides whether a whole correlation id is kept.
//
// Called once where the id is minted. With no request sampler installed
// everything is kept, which is the right default for development and the
// thing OBSERVE_SAMPLE exists to change.
func (r *Recorder) ShouldSampleRequest(id string) bool {
	if r == nil {
		return false
	}
	if r.requestSampler == nil {
		return true
	}
	return r.requestSampler(id)
}

// bypassesSampling reports whether an entry is kept whatever the rate.
//
// Anything that failed, plus exceptions and dumps. The policy lives here
// rather than at each call site so a watcher added later inherits it instead
// of having to remember it — which is the failure mode this package exists to
// point at.
//
//   - Anything that failed. A sampled-out request that broke is the request
//     you most need, and a telemetry tool that drops nine failures in ten is
//     not one. Every monitoring product keeps errors unconditionally, and an
//     error view showing a tenth of the errors is worse than no error view,
//     because it reads as a number rather than as a sample.
//   - An exception, which is the panic case of the above and also stands
//     alone: it carries its own stack.
//   - A dump. Somebody asked for it by name, in code they wrote, and losing
//     it to a dice roll would make the feature untrustworthy.
//
// The cost is that a badly broken application samples itself less, because
// more of its entries qualify. That is the right way round — the queue's
// drop counter is what bounds it, and it is reported — but it is worth
// knowing before reading a rate as a guarantee about volume.
func bypassesSampling(e Entry) bool {
	return e.Failed() || e.Kind == KindException || e.Kind == KindDump
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

		// After the drain, so whatever was still queued has already been
		// handed to the sinks and an exporter flushes it rather than
		// discarding it.
		for _, sink := range r.sinks {
			closer, ok := sink.(SinkCloser)
			if !ok {
				continue
			}
			if err := closer.Close(ctx); err != nil {
				slog.Warn("observe: a sink did not close cleanly", "error", err)
			}
		}
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

	// After the store and never instead of it. A sink that fails must not
	// cost the local dashboard its entries, and a store that fails must not
	// stop them being shipped — the two destinations answer different
	// questions and neither is a fallback for the other.
	for _, sink := range r.sinks {
		if err := sink.Record(ctx, batch); err != nil {
			// Counted against the same number as a failed store write,
			// because from the caller's side the entry went nowhere either
			// way. Logged at warn rather than error: a sink is usually a
			// network and usually comes back, where a failing store is the
			// thing you were relying on.
			r.failed.Add(uint64(len(batch)))
			slog.Warn("observe: a batch of entries could not be sent to a sink",
				"entries", len(batch), "error", err)
		}
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
