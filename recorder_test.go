package observe_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

// blockingStore stands in for a store that cannot keep up — a saturated
// database, an unreachable one, a slow disk.
type blockingStore struct {
	release chan struct{}

	mu      sync.Mutex
	written int
}

func (s *blockingStore) Record(_ context.Context, entries []observe.Entry) error {
	<-s.release
	s.mu.Lock()
	s.written += len(entries)
	s.mu.Unlock()
	return nil
}

func (s *blockingStore) Entries(context.Context, observe.Query) ([]observe.Entry, error) {
	return nil, nil
}
func (s *blockingStore) Entry(context.Context, string) (observe.Entry, error) {
	return observe.Entry{}, observe.ErrNoEntry
}
func (s *blockingStore) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

// TestRecordingNeverBlocksTheCaller is the invariant the whole design turns
// on.
//
// The driver wrapper runs inside every statement and an slog handler inside
// every log call, so anything synchronous there is paid by the whole
// application. A store that has stopped answering must therefore cost dropped
// entries and never latency.
//
// Asserting that entries were recorded would NOT catch the bug: an unbounded
// blocking writer records everything, eventually, while every request waits.
// So this asserts the drop counter rises and the calls stay fast.
func TestRecordingNeverBlocksTheCaller(t *testing.T) {
	store := &blockingStore{release: make(chan struct{})}
	recorder := observe.New(store, observe.WithQueueSize(8), observe.WithBatchSize(4))
	t.Cleanup(func() {
		close(store.release)
		_ = recorder.Close(context.Background())
	})

	ctx := context.Background()
	const attempts = 2000

	started := time.Now()
	for range attempts {
		recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
	}
	elapsed := time.Since(started)

	stats := recorder.Stats()
	if stats.Dropped == 0 {
		t.Fatalf("nothing was dropped against a store that never answers, so "+
			"the writer is holding entries without bound or blocking the "+
			"caller (recorded %d of %d)", stats.Recorded, attempts)
	}
	if stats.Recorded+stats.Dropped != attempts {
		t.Errorf("recorded %d + dropped %d != %d attempted; entries went "+
			"missing without being counted", stats.Recorded, stats.Dropped, attempts)
	}

	// Two thousand calls that each only try a non-blocking channel send are
	// microseconds of work. A second means something waited.
	if elapsed > time.Second {
		t.Errorf("%d calls took %s against a stalled store; recording is "+
			"blocking the caller", attempts, elapsed)
	}
}

// TestCloseDrainsWhatIsQueued covers the other half: dropping under pressure
// is the trade, losing a quiet queue at shutdown is a bug.
func TestCloseDrainsWhatIsQueued(t *testing.T) {
	store := memory.New()
	// A long flush interval so nothing is written by the ticker — only Close
	// can have drained it.
	recorder := observe.New(store,
		observe.WithBatchSize(1000),
		observe.WithFlushInterval(time.Hour))

	for i := range 10 {
		recorder.Record(context.Background(), observe.Entry{
			Kind:    observe.KindQuery,
			Summary: "SELECT " + itoa(i),
		})
	}

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 10 {
		t.Errorf("got %d entries after Close, want 10; a queued entry was "+
			"lost at shutdown", len(entries))
	}
}

// TestCloseIsIdempotent because a ShutdownProvider may be called twice, and
// closing a closed channel panics.
func TestCloseIsIdempotent(t *testing.T) {
	recorder := observe.New(memory.New())
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestANilRecorderRecordsNothing so a watcher installed before the recorder
// exists is not a panic.
func TestANilRecorderRecordsNothing(t *testing.T) {
	var recorder *observe.Recorder
	recorder.Record(context.Background(), observe.Entry{Kind: observe.KindQuery})
	recorder.Watching("sqldriver")
	if got := recorder.Stats(); got.Recorded != 0 {
		t.Errorf("a nil recorder reported %d recorded", got.Recorded)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Errorf("closing a nil recorder: %v", err)
	}
}

// TestRecordingIsSkippedUnderWithoutRecording is the recursion guard.
//
// The SQL store writes entries through the same database the driver wrapper
// watches, so a recorded write produces an entry, which produces a write.
// Nothing downstream would stop it.
func TestRecordingIsSkippedUnderWithoutRecording(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	ctx := observe.WithoutRecording(context.Background())
	if observe.Recording(ctx) {
		t.Fatal("Recording reported true under WithoutRecording")
	}

	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "INSERT INTO observe_entries"})

	if got := recorder.Stats().Recorded; got != 0 {
		t.Errorf("%d entries were recorded under WithoutRecording; the store's "+
			"own writes would loop", got)
	}
}

// TestTheSamplerDecidesBeforeTheQueue so sampling actually saves the work it
// exists to save.
func TestTheSamplerDecidesBeforeTheQueue(t *testing.T) {
	recorder := observe.New(memory.New(), observe.WithSampler(func(e observe.Entry) bool {
		return e.Kind == observe.KindRequest
	}))
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	recorder.Record(context.Background(), observe.Entry{Kind: observe.KindQuery})
	recorder.Record(context.Background(), observe.Entry{Kind: observe.KindRequest})

	if got := recorder.Stats().Recorded; got != 1 {
		t.Errorf("Recorded = %d, want 1; the sampler was not consulted", got)
	}
	if got := recorder.Stats().Dropped; got != 0 {
		t.Errorf("Dropped = %d; a sampled-out entry is not a drop, and counting "+
			"it as one would make the drop counter useless as an alert", got)
	}
}

// TestWatchersAreNamed so an empty query panel can be told from an unwatched
// one.
func TestWatchersAreNamed(t *testing.T) {
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	recorder.Watching("sqldriver")
	recorder.Watching("slog")
	recorder.Watching("sqldriver")

	names := recorder.Watchers()
	if len(names) != 2 {
		t.Fatalf("Watchers = %v, want two distinct names", names)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "sqldriver") || !strings.Contains(joined, "slog") {
		t.Errorf("Watchers = %v, want sqldriver and slog", names)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
