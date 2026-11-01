package observe_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

// TestEveryEntryCarriesTheSource.
//
// A shared store across several web processes is the normal deployment, so
// without this two instances' entries are indistinguishable and "which host
// is slow" has no answer. Stamped by the recorder, so no watcher has to know
// about it — and a watcher added later inherits it.
func TestEveryEntryCarriesTheSource(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store, observe.WithSource(observe.Source{
		Service: "productshore", Host: "web-7f3", Instance: "ab12",
	}))
	ctx := context.Background()

	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
	recorder.Record(ctx, observe.Entry{Kind: observe.KindCache, Summary: "cache hit k"})

	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	found, err := store.Entries(ctx, observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("recorded %d entries", len(found))
	}
	for _, entry := range found {
		if entry.Source != "productshore/web-7f3/ab12" {
			t.Errorf("%s carries source %q", entry.Summary, entry.Source)
		}
	}
}

// Host and Instance are filled in, so a caller supplies only what it knows
// and still gets a usable identity.
func TestTheSourceFillsInWhatItCanLearn(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store, observe.WithSource(observe.Source{Service: "shop"}))
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	source := recorder.Source()
	if source.Host == "" {
		t.Error("Host was left empty; the operating system knows it")
	}
	if source.Instance == "" {
		t.Error("Instance was left empty, so two runs on one host are " +
			"indistinguishable and a restart looks like continuous history")
	}
	// But not a release: nothing here can guess one, and an entry labelled
	// with the wrong release sends somebody to the wrong commit.
	if source.Release != "" {
		t.Errorf("Release = %q was invented", source.Release)
	}
}

// TestTwoRunsOnOneHostDiffer.
//
// What makes a restart visible rather than looking like continuous history.
//
// It also caught a real bug, which is why it runs a hundred rounds rather
// than one: the first version took the front of a NewID, and NewID is
// sortable by time — its first eight hex characters are four bytes of
// nanoseconds, about four seconds of resolution. Two replicas starting
// together shared an instance id, and starting together is exactly what
// replicas do after a deploy.
func TestTwoRunsOnOneHostDiffer(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		recorder := observe.New(memory.New(),
			observe.WithSource(observe.Source{Service: "shop"}))
		id := recorder.Source().Instance
		if seen[id] {
			t.Fatalf("instance id %q was issued twice; two replicas starting "+
				"together would be indistinguishable", id)
		}
		seen[id] = true
		_ = recorder.Close(context.Background())
	}
}

func TestTwoRecordersDiffer(t *testing.T) {
	first := observe.New(memory.New(), observe.WithSource(observe.Source{Service: "shop"}))
	second := observe.New(memory.New(), observe.WithSource(observe.Source{Service: "shop"}))
	t.Cleanup(func() {
		_ = first.Close(context.Background())
		_ = second.Close(context.Background())
	})

	if first.Source().Instance == second.Source().Instance {
		t.Error("two runs share an instance id")
	}
}

func TestTheRenderingPutsTheLongestLivedPartFirst(t *testing.T) {
	// So a prefix means something: "shop/" selects a service, "shop/web-7f3"
	// selects a host, and the whole thing selects one run.
	got := observe.Source{Service: "shop", Host: "web-7f3", Instance: "ab12"}.String()
	if got != "shop/web-7f3/ab12" {
		t.Errorf("String() = %q", got)
	}

	// Empty parts are omitted rather than leaving empty segments, which
	// would make a prefix match the wrong thing.
	if got := (observe.Source{Host: "web-7f3"}).String(); got != "web-7f3" {
		t.Errorf("String() = %q for a host alone", got)
	}
	if got := (observe.Source{}).String(); got != "" {
		t.Errorf("String() = %q for nothing", got)
	}
}

// It goes in a column on every row, so it is capped for the reason tags are:
// what a process knows about itself mostly came from its environment, and an
// environment variable is not a length anybody checked.
func TestTheRenderedSourceIsBounded(t *testing.T) {
	got := observe.Source{
		Service: strings.Repeat("s", 200), Host: strings.Repeat("h", 200),
	}.String()
	if len(got) != observe.MaxSourceLength {
		t.Errorf("length %d, want the cap of %d", len(got), observe.MaxSourceLength)
	}
}

// A development machine has one process, so the answer would be the same on
// every row and an unconfigured source is left empty rather than invented.
func TestAnUnconfiguredRecorderStampsNothing(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	ctx := context.Background()

	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	found, _ := store.Entries(ctx, observe.Query{})
	if len(found) != 1 {
		t.Fatalf("recorded %d entries", len(found))
	}
	if found[0].Source != "" {
		t.Errorf("Source = %q with nothing configured", found[0].Source)
	}
}

// An entry that already says where it came from keeps saying it. A batch
// replayed from a file through a second recorder must not be relabelled with
// the machine replaying it.
func TestAnEntryThatAlreadyHasASourceKeepsIt(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store, observe.WithSource(observe.Source{Host: "replayer"}))
	ctx := context.Background()

	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindQuery, Summary: "SELECT 1", Source: "shop/web-7f3/ab12",
	})
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	found, _ := store.Entries(ctx, observe.Query{})
	if len(found) != 1 || found[0].Source != "shop/web-7f3/ab12" {
		t.Errorf("Source = %q, want the one it arrived with", found[0].Source)
	}
}
