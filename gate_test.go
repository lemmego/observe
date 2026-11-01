package observe_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

// sampledOut returns a recorder keeping nothing, and a context carrying that
// decision the way the HTTP middleware caches it.
func sampledOut(t *testing.T) (*observe.Recorder, observe.Store, context.Context) {
	t.Helper()
	store := memory.New()
	recorder := observe.New(store,
		observe.WithRequestSampler(func(string) bool { return false }))
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	id := observe.NewID()
	ctx := observe.WithSampling(
		observe.WithRequestID(context.Background(), id),
		recorder.ShouldSampleRequest(id))
	return recorder, store, ctx
}

func drained(t *testing.T, recorder *observe.Recorder, store observe.Store) []observe.Entry {
	t.Helper()
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatalf("reading entries: %v", err)
	}
	return found
}

func TestAnOrdinaryEntryOnASampledOutRequestIsNotRecorded(t *testing.T) {
	recorder, store, ctx := sampledOut(t)

	if recorder.ShouldRecord(ctx) {
		t.Error("ShouldRecord said yes for a sampled-out request")
	}
	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})

	if found := drained(t, recorder, store); len(found) != 0 {
		t.Errorf("recorded %d entries on a sampled-out request", len(found))
	}
}

// TestAPanicOnASampledOutRequestIsStillRecorded.
//
// The case the gate is most likely to break and the one that matters most. A
// sampled-out request that panicked is the request you most need, and a
// telemetry tool that drops nine crashes in ten is not one.
//
// Asserted through Record rather than through ShouldRecord, because that is
// where the rule has to live: a watcher that forgot to ask would still be
// correct, and one that asked would lose this.
func TestAPanicOnASampledOutRequestIsStillRecorded(t *testing.T) {
	recorder, store, ctx := sampledOut(t)

	recorder.Record(ctx, observe.Entry{
		Kind:    observe.KindException,
		Level:   slog.LevelError,
		Summary: "runtime error: invalid memory address",
		Err:     "runtime error: invalid memory address",
	})

	found := drained(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d entries, want the exception kept despite sampling", len(found))
	}
	if found[0].Kind != observe.KindException {
		t.Errorf("kept %q", found[0].Kind)
	}
}

// Every monitoring product keeps errors unconditionally, and an error view
// showing a tenth of the errors is worse than none: it reads as a number
// rather than as a sample.
func TestAnythingThatFailedIsKeptDespiteSampling(t *testing.T) {
	recorder, store, ctx := sampledOut(t)

	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindQuery, Summary: "SELECT 1", Err: "connection refused",
	})
	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindLog, Summary: "could not build the block", Level: slog.LevelError,
	})
	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindRequest, Summary: "GET /feed", Level: slog.LevelError,
	})
	// And a dump, which somebody asked for by name in code they wrote.
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })
	observe.Dump(ctx, "why is this nil", 7)

	// The one that should go.
	recorder.Record(ctx, observe.Entry{Kind: observe.KindCache, Summary: "cache hit k"})

	found := drained(t, recorder, store)
	if len(found) != 4 {
		t.Fatalf("kept %d entries, want the failed query, log and request plus "+
			"the dump — and not the cache hit", len(found))
	}
	for _, entry := range found {
		if entry.Kind == observe.KindCache {
			t.Error("a cache hit survived sampling")
		}
	}
}

// TestTheRecursionGuardIsNotCountedAsSampling.
//
// A store writing its own entries is not telemetry anybody asked for.
// Reporting it as sampled-out would be noise in a number people read to size
// their sampling rate.
func TestTheRecursionGuardIsNotCountedAsSampling(t *testing.T) {
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	internal := observe.WithoutRecording(context.Background())
	if recorder.ShouldRecord(internal) {
		t.Error("ShouldRecord said yes inside the recursion guard")
	}
	if got := recorder.Stats().Sampled; got != 0 {
		t.Errorf("Sampled = %d, want the recursion guard uncounted", got)
	}
}

func TestTheGateCountsWhatItSkips(t *testing.T) {
	recorder, _, ctx := sampledOut(t)

	for i := 0; i < 5; i++ {
		if recorder.ShouldRecord(ctx) {
			t.Fatal("ShouldRecord said yes")
		}
	}
	if got := recorder.Stats().Sampled; got != 5 {
		t.Errorf("Sampled = %d, want 5 — without this a list a tenth as long "+
			"as expected reads as a broken watcher", got)
	}
}

// Work with no decision on its context is kept. "Nobody sampled this" does
// not read as "this was sampled out".
func TestWorkWithNoDecisionIsKept(t *testing.T) {
	recorder := observe.New(memory.New(),
		observe.WithRequestSampler(func(string) bool { return false }))
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	// No WithSampling: a background goroutine, a command, an application
	// calling Record directly.
	if !recorder.ShouldRecord(context.Background()) {
		t.Error("an entry outside any request was sampled away by a decision " +
			"nobody made about it")
	}
}

// The coarse gate and the fine sampler compose: "keep a tenth of requests,
// and within those drop cache hits" is a reasonable thing to want.
func TestTheEntrySamplerStillAppliesWithinAKeptRequest(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store,
		observe.WithRequestSampler(func(string) bool { return true }),
		observe.WithSampler(func(e observe.Entry) bool { return e.Kind != observe.KindCache }))
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	id := observe.NewID()
	ctx := observe.WithSampling(
		observe.WithRequestID(context.Background(), id), recorder.ShouldSampleRequest(id))

	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
	recorder.Record(ctx, observe.Entry{Kind: observe.KindCache, Summary: "cache hit k"})

	found := drained(t, recorder, store)
	if len(found) != 1 || found[0].Kind != observe.KindQuery {
		t.Errorf("got %d entries; the fine sampler did not apply within a kept request",
			len(found))
	}
}

// A failed statement is kept whatever the rate, which is why the SQL path
// asks the gate only when the statement worked.
func TestAFailedStatementIsRecordedOnASampledOutRequest(t *testing.T) {
	recorder, store, ctx := sampledOut(t)
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	observe.RecordQuery(ctx, "sqldriver", "SELECT 1", nil, 0, nil)
	observe.RecordQuery(ctx, "sqldriver", "SELECT bad", nil, 0, errors.New("syntax error"))

	found := drained(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d statements, want only the failed one", len(found))
	}
	if found[0].Err == "" {
		t.Error("the statement that worked was kept and the failed one was not")
	}
}
