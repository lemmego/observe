package observe_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

func TestNormaliseTagsBoundsWhatItIsGiven(t *testing.T) {
	t.Run("the separator cannot survive", func(t *testing.T) {
		// A tag carrying the separator would make a store's anchored LIKE
		// match the wrong thing, which is somebody else's data in an answer
		// about this one.
		got := observe.NormaliseTags([]string{"user:7,admin"})
		if len(got) != 1 || strings.Contains(got[0], ",") {
			t.Errorf("got %q, want the comma gone", got)
		}
	})

	t.Run("duplicates collapse", func(t *testing.T) {
		got := observe.NormaliseTags([]string{"user:7", "user:7", " user:7 "})
		if len(got) != 1 {
			t.Errorf("got %q, want one", got)
		}
	})

	t.Run("the count is capped", func(t *testing.T) {
		many := make([]string, observe.MaxTags+10)
		for i := range many {
			many[i] = "tag:" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		}
		if got := observe.NormaliseTags(many); len(got) != observe.MaxTags {
			t.Errorf("kept %d tags, want the cap of %d", len(got), observe.MaxTags)
		}
	})

	t.Run("the length is capped by truncation", func(t *testing.T) {
		// Truncated rather than dropped: "user:12345…" still groups
		// usefully, where a missing tag groups not at all.
		got := observe.NormaliseTags([]string{"user:" + strings.Repeat("9", 200)})
		if len(got) != 1 {
			t.Fatalf("got %q, want the tag kept", got)
		}
		if len(got[0]) != observe.MaxTagLength {
			t.Errorf("length %d, want %d", len(got[0]), observe.MaxTagLength)
		}
	})

	t.Run("nothing becomes nil", func(t *testing.T) {
		// nil rather than an empty slice, so an entry with no tags marshals
		// without the field — and a dashboard does not render a blank chip.
		if got := observe.NormaliseTags([]string{"", "  ", ","}); got != nil {
			t.Errorf("got %#v, want nil", got)
		}
	})
}

func TestWithTagsIsAdditive(t *testing.T) {
	ctx := observe.WithTags(context.Background(), "user:7")
	ctx = observe.WithTags(ctx, "tenant:acme")

	got := observe.TagsFrom(ctx)
	if len(got) != 2 {
		t.Fatalf("got %q, want both — two middlewares each adding one must not fight", got)
	}
}

// TestContextTagsReachEveryEntry is the reason tags live on the context.
//
// A watcher can only tag what it knows, and no watcher knows who is signed
// in — so tagging per watcher would mean teaching each one about
// authentication, and would still miss whichever watcher is added next.
func TestContextTagsReachEveryEntry(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)

	ctx := observe.WithTags(
		observe.WithRequestID(context.Background(), "req-1"), "user:7")

	// Two kinds recorded by two different callers, neither of which mentions
	// a tag.
	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
	recorder.Record(ctx, observe.Entry{Kind: observe.KindCache, Summary: "cache hit k"})

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	found, err := store.Entries(context.Background(), observe.Query{Tags: []string{"user:7"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Errorf("found %d entries tagged user:7, want both kinds", len(found))
	}
}

// TestFailedIsDerivedRatherThanSupplied.
//
// Entry already knows: Failed() reads the level and the error text. As a tag
// it spans kinds in one filter, where a level filter is also satisfied by a
// warn-level gate denial that refused somebody on purpose.
func TestFailedIsDerivedRatherThanSupplied(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	ctx := context.Background()

	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "fine"})
	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindQuery, Summary: "broken", Err: "connection refused",
	})
	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindLog, Summary: "logged", Level: slog.LevelError,
	})

	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	found, err := store.Entries(ctx, observe.Query{Tags: []string{observe.TagFailed}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Errorf("found %d entries tagged %q, want the one with an error and "+
			"the one at error level", len(found), observe.TagFailed)
	}
}

// A tag set on the entry and a tag on the context both survive, with the
// context's first — the order decides which survive the cap, and who this
// was is worth more than a watcher's detail.
func TestEntryTagsAndContextTagsAreBothKept(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	ctx := observe.WithTags(context.Background(), "user:7")

	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindRequest, Summary: "GET /", Tags: []string{"status:200"},
	})
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	found, err := store.Entries(context.Background(),
		observe.Query{Tags: []string{"user:7", "status:200"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("found %d entries carrying both tags, want 1", len(found))
	}
	if found[0].Tags[0] != "user:7" {
		t.Errorf("tags = %q, want the context's first so it survives the cap",
			found[0].Tags)
	}
}
