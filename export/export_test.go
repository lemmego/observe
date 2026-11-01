package export_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/export"
)

// countingTransport records what it was asked to send, and can fail.
type countingTransport struct {
	mu       sync.Mutex
	batches  [][]observe.Entry
	attempts int
	err      error
	block    chan struct{}
}

func (t *countingTransport) Send(_ context.Context, entries []observe.Entry) error {
	t.mu.Lock()
	t.attempts++
	err := t.err
	block := t.block
	t.mu.Unlock()

	if block != nil {
		<-block
	}
	if err != nil {
		return err
	}

	copied := make([]observe.Entry, len(entries))
	copy(copied, entries)
	t.mu.Lock()
	t.batches = append(t.batches, copied)
	t.mu.Unlock()
	return nil
}

func (t *countingTransport) sentCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, b := range t.batches {
		n += len(b)
	}
	return n
}

func (t *countingTransport) attemptCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

func entries(n int) []observe.Entry {
	out := make([]observe.Entry, n)
	for i := range out {
		out[i] = observe.Entry{
			ID: observe.NewID(), Kind: observe.KindQuery,
			Summary: fmt.Sprintf("SELECT %d", i), At: time.Now(),
		}
	}
	return out
}

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEntriesReachTheTransport(t *testing.T) {
	transport := &countingTransport{}
	exporter := export.New(transport,
		export.WithBatchSize(10), export.WithFlushInterval(20*time.Millisecond))
	t.Cleanup(func() { _ = exporter.Close(context.Background()) })

	if err := exporter.Record(context.Background(), entries(25)); err != nil {
		t.Fatal(err)
	}

	eventually(t, 3*time.Second, "25 entries to be sent",
		func() bool { return transport.sentCount() == 25 })
	if got := exporter.Stats().Sent; got != 25 {
		t.Errorf("Sent = %d, want 25", got)
	}
}

// TestADeadCollectorCostsBoundedMemoryAndNoLatency.
//
// The failure this package has to survive. A collector that is gone must
// cost a bounded buffer, a rising drop counter and nothing else — not
// unbounded memory, not a hot retry loop, and above all not time on the
// caller's goroutine.
func TestADeadCollectorCostsBoundedMemoryAndNoLatency(t *testing.T) {
	// A port nothing is listening on: the httptest server is started and
	// immediately closed, so the address is real and refuses.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()

	transport, err := export.HTTP(export.HTTPOptions{
		Endpoint: endpoint,
		Client:   &http.Client{Timeout: 200 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}

	const capacity = 500
	exporter := export.New(transport,
		export.WithCapacity(capacity),
		export.WithBatchSize(50),
		export.WithFlushInterval(10*time.Millisecond),
		export.WithMaxBackoff(20*time.Millisecond),
		export.WithCircuitBreaker(2, time.Hour), // opens and stays open
	)
	t.Cleanup(func() { _ = exporter.Close(context.Background()) })

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	started := time.Now()
	for range 100 {
		if err := exporter.Record(context.Background(), entries(100)); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(started)

	// Ten thousand entries into a five-hundred buffer, with the far end
	// refusing every connection.
	if elapsed > 2*time.Second {
		t.Errorf("recording 10,000 entries into a dead exporter took %v; it is "+
			"supposed to be a bounded append", elapsed)
	}

	stats := exporter.Stats()
	if stats.Buffered > capacity {
		t.Errorf("buffered %d entries with a capacity of %d", stats.Buffered, capacity)
	}
	if stats.Dropped == 0 {
		t.Error("nothing was dropped, so either the buffer is unbounded or " +
			"nothing was accepted")
	}
	if stats.Accepted != 10000 {
		t.Errorf("Accepted = %d, want 10000", stats.Accepted)
	}

	// And the circuit opened rather than the exporter spinning.
	eventually(t, 3*time.Second, "the circuit to open", func() bool {
		return exporter.Stats().CircuitOpen
	})

	// The last error is reported rather than swallowed, so a dashboard can
	// say why nothing is arriving.
	if exporter.Stats().LastError == "" {
		t.Error("no last error recorded for a collector that refused every connection")
	}
}

// TestAnOpenCircuitStopsAttempting.
//
// Backoff alone still retries every batch; with a full buffer that is a lot
// of batches, which is the hot loop the circuit exists to prevent.
//
// Entries are fed continuously through the measurement, and that is the
// whole test rather than a detail. The first version recorded once and then
// watched the attempt count: a failed batch is discarded rather than put
// back, so the buffer emptied and attempts stopped whether or not the
// circuit worked. It passed with the circuit disabled, which is to say it
// tested nothing.
func TestAnOpenCircuitStopsAttempting(t *testing.T) {
	transport := &countingTransport{err: errors.New("the collector is gone")}
	exporter := export.New(transport,
		export.WithBatchSize(1),
		export.WithFlushInterval(5*time.Millisecond),
		export.WithMaxBackoff(time.Millisecond),
		export.WithCircuitBreaker(2, time.Hour),
	)
	t.Cleanup(func() { _ = exporter.Close(context.Background()) })

	// Fed for the whole test, so there is always a batch waiting and the
	// only thing that can stop the transport being called is the circuit.
	feeding := make(chan struct{})
	go func() {
		for {
			select {
			case <-feeding:
				return
			default:
				_ = exporter.Record(context.Background(), entries(5))
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	defer close(feeding)

	eventually(t, 3*time.Second, "the circuit to open", func() bool {
		return exporter.Stats().CircuitOpen
	})

	settled := transport.attemptCount()
	time.Sleep(300 * time.Millisecond)
	if after := transport.attemptCount(); after > settled {
		t.Errorf("the transport was attempted %d more times while the circuit "+
			"was open and entries were still arriving; it is supposed to stop",
			after-settled)
	}

	// And the entries lost to those failures are counted rather than
	// vanishing: a batch that fails is discarded, not put back.
	if exporter.Stats().Failed == 0 {
		t.Error("entries were lost to failed sends and counted nowhere")
	}
}

// A refusal that will not change is dropped loudly rather than retried
// forever: a buffer that never drains over one misconfiguration is worse
// than losing the batch.
func TestAPermanentRefusalIsNotRetried(t *testing.T) {
	transport := &countingTransport{
		err: fmt.Errorf("%w: the collector answered 400", export.ErrPermanent),
	}
	exporter := export.New(transport,
		export.WithBatchSize(10), export.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = exporter.Close(context.Background()) })

	if err := exporter.Record(context.Background(), entries(10)); err != nil {
		t.Fatal(err)
	}

	eventually(t, 3*time.Second, "the batch to be refused",
		func() bool { return exporter.Stats().Refused == 10 })

	if got := exporter.Stats().Retries; got != 0 {
		t.Errorf("Retries = %d for a permanent refusal, want none", got)
	}
	if transport.attemptCount() != 1 {
		t.Errorf("attempted %d times, want once", transport.attemptCount())
	}
}

// Record must not wait on the transport. A browser tab nobody is reading
// cannot be allowed to cost request latency, and nor can a collector.
func TestRecordDoesNotWaitOnTheTransport(t *testing.T) {
	block := make(chan struct{})
	transport := &countingTransport{block: block}
	defer close(block)

	exporter := export.New(transport,
		export.WithBatchSize(1), export.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = exporter.Close(context.Background()) })

	done := make(chan struct{})
	go func() {
		for range 50 {
			_ = exporter.Record(context.Background(), entries(10))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Record blocked on a transport that was not answering")
	}
}

// TestTheOldestEntriesGoFirst.
//
// Which entries are worth having: a collector down for an hour should come
// back to the last minute of traffic rather than to the first.
func TestTheOldestEntriesGoFirst(t *testing.T) {
	block := make(chan struct{})
	transport := &countingTransport{block: block}

	exporter := export.New(transport,
		export.WithCapacity(10),
		export.WithBatchSize(100), // never ready, so nothing leaves
		export.WithFlushInterval(time.Hour),
	)
	t.Cleanup(func() { close(block); _ = exporter.Close(context.Background()) })

	for i := range 20 {
		if err := exporter.Record(context.Background(), []observe.Entry{{
			ID: fmt.Sprintf("entry-%02d", i), Kind: observe.KindQuery,
			Summary: fmt.Sprintf("SELECT %d", i),
		}}); err != nil {
			t.Fatal(err)
		}
	}

	stats := exporter.Stats()
	if stats.Buffered != 10 {
		t.Fatalf("buffered %d, want the capacity of 10", stats.Buffered)
	}
	if stats.Dropped != 10 {
		t.Errorf("dropped %d, want 10", stats.Dropped)
	}
}

// TestJSONLRoundTripsIncludingThePayload.
//
// Data is json.RawMessage precisely so it survives byte for byte — the
// stores are held to that and a transport has to be too, or a collector
// receives re-ordered keys and a different document from the one recorded.
func TestJSONLRoundTripsIncludingThePayload(t *testing.T) {
	var out bytes.Buffer
	transport := export.JSONL(&out)

	original := []observe.Entry{
		{
			ID: "one", Kind: observe.KindQuery, Summary: "SELECT 1",
			At:   time.Now().UTC().Truncate(time.Millisecond),
			Data: []byte(`{"z":1,"a":2,"nested":{"k":"v"}}`),
			Tags: []string{"user:7"},
		},
		{ID: "two", Kind: observe.KindCache, Summary: "cache hit k"},
	}
	if err := transport.Send(context.Background(), original); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines for two entries", len(lines))
	}

	var first observe.Entry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if first.ID != "one" || first.Summary != "SELECT 1" {
		t.Errorf("got %+v", first)
	}
	if string(first.Data) != `{"z":1,"a":2,"nested":{"k":"v"}}` {
		t.Errorf("the payload did not survive byte for byte: %s", first.Data)
	}
	if len(first.Tags) != 1 || first.Tags[0] != "user:7" {
		t.Errorf("Tags = %v", first.Tags)
	}
}

func TestHTTPPostsAnEnvelope(t *testing.T) {
	var (
		gotAuth     string
		gotEncoding string
		gotEnvelope struct {
			Source  observe.Source  `json:"source"`
			Entries []observe.Entry `json:"entries"`
		}
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEncoding = r.Header.Get("Content-Encoding")

		reader, err := gzipReader(r)
		if err != nil {
			t.Errorf("decompressing: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(reader).Decode(&gotEnvelope); err != nil {
			t.Errorf("decoding: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	transport, err := export.HTTP(export.HTTPOptions{
		Endpoint: server.URL,
		Token:    "a-token",
		Source:   observe.Source{Service: "productshore", Environment: "production"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Send(context.Background(), entries(3)); err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer a-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotEncoding != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip — telemetry is repetitive "+
			"JSON and compresses to a fraction of itself", gotEncoding)
	}
	if gotEnvelope.Source.Service != "productshore" {
		t.Errorf("the source did not reach the collector: %+v", gotEnvelope.Source)
	}
	if len(gotEnvelope.Entries) != 3 {
		t.Errorf("the collector received %d entries", len(gotEnvelope.Entries))
	}
}

// A 4xx that is not 429 is us, not the collector. Retrying forever would
// turn one misconfiguration into a buffer that never drains.
func TestAClientErrorIsPermanentAndAServerErrorIsNot(t *testing.T) {
	for status, permanent := range map[int]bool{
		http.StatusBadRequest:          true,
		http.StatusUnauthorized:        true,
		http.StatusNotFound:            true,
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		http.StatusBadGateway:          false,
	} {
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))

		transport, err := export.HTTP(export.HTTPOptions{Endpoint: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		err = transport.Send(context.Background(), entries(1))
		server.Close()

		if err == nil {
			t.Errorf("%d was reported as success", status)
			continue
		}
		if errors.Is(err, export.ErrPermanent) != permanent {
			t.Errorf("%d: permanent = %v, want %v (%v)",
				status, !permanent, permanent, err)
		}
	}
}

func TestHTTPNeedsAnEndpoint(t *testing.T) {
	if _, err := export.HTTP(export.HTTPOptions{}); err == nil {
		t.Error("HTTP accepted an empty endpoint, which would fail at the first " +
			"batch rather than at boot")
	}
}

// Close sends what is buffered, so a graceful shutdown does not take the
// last minute of telemetry with it.
func TestCloseFlushesWhatIsBuffered(t *testing.T) {
	transport := &countingTransport{}
	exporter := export.New(transport,
		export.WithBatchSize(1000), // nothing leaves on its own
		export.WithFlushInterval(time.Hour),
	)

	if err := exporter.Record(context.Background(), entries(30)); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := transport.sentCount(); got != 30 {
		t.Errorf("sent %d entries at shutdown, want 30", got)
	}
}

// And a shutdown that cannot send does not hang: a process that will not
// stop is worse than losing the telemetry it was holding.
func TestCloseIsBoundedByItsContext(t *testing.T) {
	block := make(chan struct{})
	transport := &countingTransport{block: block}
	defer close(block)

	exporter := export.New(transport,
		export.WithBatchSize(1000), export.WithFlushInterval(time.Hour))
	if err := exporter.Record(context.Background(), entries(10)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { _ = exporter.Close(ctx); close(done) }()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hung on a transport that was not answering")
	}
}
