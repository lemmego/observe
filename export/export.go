// Package export ships recorded entries somewhere else.
//
// An observe.Sink that buffers, batches, retries and gives up in a bounded
// way, in front of a Transport that does the actual sending. The split is
// the point: every destination needs the same buffering and the same
// backoff, and writing that twice is how one of them ends up subtly worse.
//
// # Why this is a goroutine and not an agent
//
// Laravel Nightwatch ships a separate agent process because PHP cannot do
// background work — a request ends and the process is gone, so something
// else has to own the buffer and the retries. That is a constraint of the
// runtime, not a property of the design, and Go does not have it.
//
// So this runs in the application: no second thing to deploy, no socket to
// supervise, no version skew between an app and an agent. The cost is
// honest — an application killed hard loses whatever was still buffered —
// and [JSONL] is the escape hatch for anybody who would rather have a
// sidecar tail a file.
//
// # Nothing here blocks the application
//
// Record is a bounded append under a mutex and returns immediately. The
// sending happens on this package's own goroutine, and when the buffer is
// full the *oldest* entries go, because a collector that has been down for
// an hour should come back to the last minute rather than the first.
//
// The whole package is the standard library, so an application that ships
// its telemetry somewhere pays nothing in dependencies for it.
package export

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lemmego/observe"
)

// Defaults.
const (
	// DefaultCapacity is how many entries may be waiting to be sent.
	//
	// Larger than the recorder's own queue, because this one absorbs an
	// outage rather than a slow write: at a few hundred entries a second it
	// is tens of seconds of buffer, which covers a collector restart without
	// covering for a collector that is gone.
	DefaultCapacity = 4096

	// DefaultBatchSize is how many entries one Send carries.
	DefaultBatchSize = 256

	// DefaultFlushInterval is how long a partial batch waits for company.
	DefaultFlushInterval = 5 * time.Second

	// DefaultMaxBackoff caps the retry delay.
	DefaultMaxBackoff = 30 * time.Second

	// DefaultCircuitThreshold is how many consecutive failures open the
	// circuit.
	DefaultCircuitThreshold = 5

	// DefaultCircuitCooldown is how long the circuit stays open.
	//
	// The number that turns a dead collector from a hot loop into one
	// attempt a minute. Without it, backoff alone still retries every
	// thirty seconds per batch and a full buffer means a lot of batches.
	DefaultCircuitCooldown = time.Minute
)

// ErrPermanent tells the exporter not to retry.
//
// A Transport returns it, wrapped, when the destination refused the batch
// for a reason that will not change — a 400, a 404, a rejected token. Those
// are worth dropping loudly rather than retrying forever, because retrying
// forever turns one misconfiguration into a buffer that never drains and a
// log that never stops.
var ErrPermanent = errors.New("observe/export: the destination refused this batch permanently")

// Transport sends one batch.
//
// It may block; that is what the exporter's goroutine is for. It should
// respect the context's deadline, which the exporter sets.
type Transport interface {
	Send(ctx context.Context, entries []observe.Entry) error
}

// Stats reports what the exporter has done, so nothing it drops is silent.
type Stats struct {
	// Accepted is how many entries were taken from the recorder.
	Accepted uint64 `json:"accepted"`

	// Dropped is how many were discarded to make room. This is the number to
	// watch: it rising means the destination is not keeping up, or is gone.
	Dropped uint64 `json:"dropped"`

	// Sent is how many reached the destination.
	Sent uint64 `json:"sent"`

	// Refused is how many were discarded because the destination rejected
	// them permanently — a misconfiguration rather than an outage.
	Refused uint64 `json:"refused"`

	// Failed is how many were lost because a send did not get through.
	//
	// Separate from Dropped, which is the buffer overflowing, and from
	// Refused, which is the destination saying no. This one is the network
	// saying nothing — and it is counted because a batch that fails is
	// discarded rather than put back, so without this number those entries
	// would vanish with no trace at all.
	Failed uint64 `json:"failed"`

	// Retries is how many Send attempts failed and were tried again.
	Retries uint64 `json:"retries"`

	// Buffered is how many are waiting right now.
	Buffered int `json:"buffered"`

	// CircuitOpen reports that sending is suspended after repeated failures.
	CircuitOpen bool `json:"circuit_open"`

	// LastError is the most recent send failure, for a dashboard to show.
	LastError string `json:"last_error,omitempty"`
}

// Option configures an Exporter.
type Option func(*Exporter)

// WithCapacity sets how many entries may be waiting.
func WithCapacity(n int) Option {
	return func(e *Exporter) {
		if n > 0 {
			e.capacity = n
		}
	}
}

// WithBatchSize sets how many entries one Send carries.
func WithBatchSize(n int) Option {
	return func(e *Exporter) {
		if n > 0 {
			e.batchSize = n
		}
	}
}

// WithFlushInterval sets how long a partial batch waits.
func WithFlushInterval(d time.Duration) Option {
	return func(e *Exporter) {
		if d > 0 {
			e.flushInterval = d
		}
	}
}

// WithMaxBackoff caps the retry delay.
func WithMaxBackoff(d time.Duration) Option {
	return func(e *Exporter) {
		if d > 0 {
			e.maxBackoff = d
		}
	}
}

// WithCircuitBreaker sets how many consecutive failures suspend sending, and
// for how long.
func WithCircuitBreaker(threshold int, cooldown time.Duration) Option {
	return func(e *Exporter) {
		if threshold > 0 {
			e.circuitThreshold = threshold
		}
		if cooldown > 0 {
			e.circuitCooldown = cooldown
		}
	}
}

// Exporter buffers entries and sends them through a Transport.
type Exporter struct {
	transport Transport

	capacity         int
	batchSize        int
	flushInterval    time.Duration
	maxBackoff       time.Duration
	circuitThreshold int
	circuitCooldown  time.Duration

	mu     sync.Mutex
	buffer []observe.Entry

	// wake carries one notification. Buffered with room for one, so a burst
	// of appends costs one wake-up rather than one per batch, and an append
	// never blocks on a sender that is already awake.
	wake chan struct{}

	done      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once

	accepted atomic.Uint64
	dropped  atomic.Uint64
	sent     atomic.Uint64
	refused  atomic.Uint64
	failed   atomic.Uint64
	retries  atomic.Uint64

	circuitOpen atomic.Bool
	lastErr     atomic.Pointer[string]

	// now is injectable so a test does not have to wait out a cooldown.
	now func() time.Time
}

var (
	_ observe.Sink       = (*Exporter)(nil)
	_ observe.SinkCloser = (*Exporter)(nil)
)

// New returns an exporter sending through a transport, and starts it.
//
// Close it, or the sending goroutine outlives the application and whatever
// is buffered is never sent.
func New(transport Transport, opts ...Option) *Exporter {
	e := &Exporter{
		transport:        transport,
		capacity:         DefaultCapacity,
		batchSize:        DefaultBatchSize,
		flushInterval:    DefaultFlushInterval,
		maxBackoff:       DefaultMaxBackoff,
		circuitThreshold: DefaultCircuitThreshold,
		circuitCooldown:  DefaultCircuitCooldown,
		wake:             make(chan struct{}, 1),
		done:             make(chan struct{}),
		now:              time.Now,
	}
	for _, opt := range opts {
		opt(e)
	}

	e.wg.Add(1)
	go e.run()
	return e
}

// Record accepts a batch. It never blocks and never fails.
//
// When the buffer is full the oldest entries go, and the reason is which
// entries are worth having: a collector that has been down for an hour
// should come back to the last minute of traffic rather than to the first.
func (e *Exporter) Record(_ context.Context, entries []observe.Entry) error {
	if e == nil || len(entries) == 0 {
		return nil
	}

	e.mu.Lock()
	e.buffer = append(e.buffer, entries...)
	e.accepted.Add(uint64(len(entries)))

	if overflow := len(e.buffer) - e.capacity; overflow > 0 {
		// Copied down rather than resliced, because a reslice keeps the
		// original backing array alive and the dropped entries with it —
		// which on a buffer that is permanently full is a leak of exactly
		// the size of the thing meant to bound it.
		kept := len(e.buffer) - overflow
		copy(e.buffer, e.buffer[overflow:])
		for i := kept; i < len(e.buffer); i++ {
			e.buffer[i] = observe.Entry{}
		}
		e.buffer = e.buffer[:kept]
		e.dropped.Add(uint64(overflow))
	}
	ready := len(e.buffer) >= e.batchSize
	e.mu.Unlock()

	if ready {
		e.signal()
	}
	return nil
}

func (e *Exporter) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Stats reports what the exporter has done.
func (e *Exporter) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	e.mu.Lock()
	buffered := len(e.buffer)
	e.mu.Unlock()

	stats := Stats{
		Accepted:    e.accepted.Load(),
		Dropped:     e.dropped.Load(),
		Sent:        e.sent.Load(),
		Refused:     e.refused.Load(),
		Failed:      e.failed.Load(),
		Retries:     e.retries.Load(),
		Buffered:    buffered,
		CircuitOpen: e.circuitOpen.Load(),
	}
	if last := e.lastErr.Load(); last != nil {
		stats.LastError = *last
	}
	return stats
}

// Close sends what is buffered and stops.
//
// Bounded by the context: a shutdown that waits forever on a collector that
// is gone is a process that will not stop, which is worse than losing the
// telemetry it was holding.
func (e *Exporter) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		close(e.done)
		e.wg.Wait()
		e.drain(ctx)
	})
	return nil
}

// run is the sending loop.
func (e *Exporter) run() {
	defer e.wg.Done()

	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	var (
		failures int
		openedAt time.Time
	)

	for {
		select {
		case <-e.done:
			return
		case <-e.wake:
		case <-ticker.C:
		}

		// The circuit, checked before taking a batch: a suspended exporter
		// leaves entries in the buffer to be dropped by age rather than
		// taking them out and throwing them away.
		if e.circuitOpen.Load() {
			if e.now().Sub(openedAt) < e.circuitCooldown {
				continue
			}
			// Half open: one attempt decides.
			e.circuitOpen.Store(false)
		}

		batch := e.take()
		if len(batch) == 0 {
			continue
		}

		if err := e.send(batch); err != nil {
			if errors.Is(err, observe.ErrNoEntry) || errors.Is(err, ErrPermanent) {
				// Refused rather than failed: retrying will not help, and a
				// buffer that never drains over a misconfiguration is worse
				// than losing the batch.
				e.refused.Add(uint64(len(batch)))
				failures = 0
				continue
			}
			// Discarded rather than put back. Requeuing would head-of-line
			// block the buffer on a batch the destination cannot take, and
			// the buffer already drops its oldest to make room — so a
			// failing batch would be retried forever while newer entries
			// were thrown away to hold it. Counted, because entries that
			// vanish with no number against them are the thing this package
			// exists not to do.
			e.failed.Add(uint64(len(batch)))
			failures++
			if failures >= e.circuitThreshold {
				e.circuitOpen.Store(true)
				openedAt = e.now()
				slog.Warn("observe/export: suspending after repeated failures",
					"failures", failures, "cooldown", e.circuitCooldown, "error", err)
			}
			continue
		}
		failures = 0
	}
}

// take removes up to batchSize entries.
func (e *Exporter) take() []observe.Entry {
	e.mu.Lock()
	defer e.mu.Unlock()

	n := min(len(e.buffer), e.batchSize)
	if n == 0 {
		return nil
	}
	batch := make([]observe.Entry, n)
	copy(batch, e.buffer[:n])

	rest := len(e.buffer) - n
	copy(e.buffer, e.buffer[n:])
	for i := rest; i < len(e.buffer); i++ {
		e.buffer[i] = observe.Entry{}
	}
	e.buffer = e.buffer[:rest]

	if rest > 0 {
		// More to do: wake ourselves rather than waiting for the ticker, so
		// a backlog drains at the speed of the transport rather than at the
		// speed of the flush interval.
		e.signal()
	}
	return batch
}

// send makes one attempt with retries, and reports whether it got through.
func (e *Exporter) send(batch []observe.Entry) error {
	// Capped from the start, not only after doubling. A caller asking for a
	// maximum of a millisecond and then waiting a second for the first retry
	// would be getting the opposite of what they configured — and it is the
	// first retry that happens most, because most failures do not recur.
	backoff := min(time.Second, e.maxBackoff)

	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(
			observe.WithoutRecording(context.Background()), 30*time.Second)
		err := e.transport.Send(ctx, batch)
		cancel()

		if err == nil {
			e.sent.Add(uint64(len(batch)))
			e.lastErr.Store(nil)
			return nil
		}

		message := err.Error()
		e.lastErr.Store(&message)

		if errors.Is(err, ErrPermanent) {
			return err
		}
		// Two attempts here, then the circuit decides. Retrying a batch
		// twenty times in a loop is how a dead collector becomes a hot loop;
		// the circuit is the thing that bounds it, so this stays short.
		if attempt >= 1 {
			return err
		}

		e.retries.Add(1)
		select {
		case <-e.done:
			return err
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, e.maxBackoff)
	}
}

// drain makes a bounded attempt at whatever is left.
func (e *Exporter) drain(ctx context.Context) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		batch := e.take()
		if len(batch) == 0 {
			return
		}
		if err := e.transport.Send(ctx, batch); err != nil {
			slog.Warn("observe/export: entries were lost at shutdown",
				"entries", len(batch), "error", err)
			return
		}
		e.sent.Add(uint64(len(batch)))
	}
}
