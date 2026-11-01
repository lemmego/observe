package observe_test

import (
	"context"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

// TestSamplingKeepsWholeRequests is the property the whole function exists
// for.
//
// Keeping a tenth of the queries of every request gives you ten percent of a
// story about all of them; keeping every query of a tenth of requests gives
// you the whole story about some — and the whole story is the one you can
// debug. A per-entry random draw would pass a test that only counted entries.
func TestSamplingKeepsWholeRequests(t *testing.T) {
	keep := observe.SampleRequests(0.5)

	for _, id := range []string{"req-a", "req-b", "req-c", "req-d", "req-e"} {
		first := keep(id)
		for i := 0; i < 50; i++ {
			if keep(id) != first {
				t.Fatalf("%s was sampled inconsistently; its entries would be "+
					"split across the boundary and the request would read as "+
					"half a story", id)
			}
		}
	}
}

func TestSamplingKeepsRoughlyTheRate(t *testing.T) {
	keep := observe.SampleRequests(0.1)

	kept := 0
	const requests = 4000
	for i := 0; i < requests; i++ {
		if keep(observe.NewID()) {
			kept++
		}
	}

	// Generous bounds: the point is that the rate is honoured at all, not
	// that FNV is a perfect hash. A sampler that kept everything or nothing
	// is what this catches, and both are easy mistakes.
	if kept < requests/20 || kept > requests/4 {
		t.Errorf("kept %d of %d at a rate of 0.1", kept, requests)
	}
}

func TestTheRateBoundsAreHonoured(t *testing.T) {
	if observe.SampleRequests(1) != nil {
		t.Error("a rate of 1 returns a sampler; nil is the cheaper way to say " +
			"keep everything, and the recorder checks for it")
	}
	if observe.SampleRequests(1.5) != nil {
		t.Error("a rate above 1 returns a sampler")
	}
	if keep := observe.SampleRequests(0); keep("x") {
		t.Error("a rate of 0 kept an entry")
	}
}

// An entry with no request id belongs to no group, so dropping it by the same
// rate would thin a population rather than select from it.
func TestAnUncorrelatedEntryIsKept(t *testing.T) {
	keep := observe.SampleRequests(0.0001)
	if !keep("") {
		t.Error("work with no request id was sampled away")
	}
}

// TestSampledEntriesAreCounted.
//
// Without the counter, a list one tenth as long as expected looks like a
// broken watcher rather than like OBSERVE_SAMPLE=0.1 — and the two are
// debugged very differently.
func TestSampledEntriesAreCounted(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store, observe.WithSampler(func(observe.Entry) bool { return false }))
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
	}

	stats := recorder.Stats()
	if stats.Sampled != 10 {
		t.Errorf("Sampled = %d, want 10", stats.Sampled)
	}
	// Separate from Dropped, which means the opposite thing: the store
	// failing to keep up rather than the configuration working as asked.
	if stats.Dropped != 0 {
		t.Errorf("Dropped = %d; a sampled entry is not a dropped one", stats.Dropped)
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPauseStopsRecordingWithoutLosingWhatIsQueued(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	ctx := context.Background()

	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "before"})
	recorder.Pause()
	if !recorder.Paused() {
		t.Fatal("Paused() is false after Pause()")
	}
	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "during"})
	recorder.Resume()
	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "after"})

	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	found, err := store.Entries(ctx, observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("recorded %d entries, want the two outside the pause", len(found))
	}
	for _, entry := range found {
		if entry.Summary == "during" {
			t.Error("an entry recorded while paused was kept")
		}
	}
}
